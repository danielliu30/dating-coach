package auth

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/danielliu30/dating-coach/backend/internal/account"
	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// failingPublisher stands in for a broker that cannot take the deletion.
type failingPublisher struct{}

// Publish always fails.
func (failingPublisher) Publish(context.Context, account.Job) error {
	return errors.New("broker is down")
}

// TestDeleteAccountKeepsSessionsWhenQueueingFails pins the order deletion runs
// in: revoking an account whose removal never reached the queue would lock its
// owner out of an account that then stays alive until an operator steps in. The
// denylist here holds a nil Redis client, so revoking would panic rather than
// quietly pass.
func TestDeleteAccountKeepsSessionsWhenQueueingFails(t *testing.T) {
	svc := NewService(nil, nil, nil, 0, "", NewDenylist(nil, time.Minute), failingPublisher{}, time.Hour, time.Minute)
	if err := svc.DeleteAccount(context.Background(), uuid.New()); err == nil {
		t.Fatal("DeleteAccount succeeded although the deletion was never queued")
	}
}

// silentNotifier stands in for the SMTP notifier: sign-up sends a verification
// email, which is irrelevant to the scope of the token it returns.
type silentNotifier struct{}

// Email discards the message and reports success.
func (silentNotifier) Email(context.Context, string, string, string) error { return nil }

// Push discards the notification and reports success.
func (silentNotifier) Push(context.Context, string, string, string) error { return nil }

// newTestService connects to the database named by TEST_DATABASE_URL and returns
// a Service wired to it, skipping the test when the variable is unset so the
// suite still runs without Postgres. TTLs are distinct so a test can tell which
// one a token was minted with.
func newTestService(t *testing.T) (*Service, *TokenIssuer, *pgxpool.Pool) {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}

	issuer := NewTokenIssuer("test-secret")
	svc := NewService(db.New(pool), issuer, silentNotifier{}, bcrypt.MinCost, "http://app.test", nil, nil, 24*time.Hour, 30*time.Minute)
	return svc, issuer, pool
}

// TestSignUpMintsVerifyScopedToken covers the scope split end to end, through
// the real queries: the new account exists but its token is verify-scoped and
// short-lived, and only confirming the address — by verifying with that token,
// or by signing in afterwards — yields a session-scoped one.
func TestSignUpMintsVerifyScopedToken(t *testing.T) {
	svc, issuer, pool := newTestService(t)
	ctx := context.Background()

	const password = "correct-horse"
	email := "scope-test-" + uuid.NewString() + "@example.com"
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DELETE FROM users WHERE email = $1", email); err != nil {
			t.Errorf("delete test user: %v", err)
		}
	})

	signUp, err := svc.SignUp(ctx, SignUpInput{Email: email, Password: password, DisplayName: "Scope Test"})
	if err != nil {
		t.Fatalf("sign up: %v", err)
	}
	if signUp.User.EmailVerified {
		t.Fatal("a new account must not start out verified")
	}
	assertTokenScope(t, issuer, signUp.Token, ScopeVerify, 30*time.Minute)

	var verificationToken string
	if err := pool.QueryRow(ctx, "SELECT verification_token FROM users WHERE email = $1", email).Scan(&verificationToken); err != nil {
		t.Fatalf("read verification token: %v", err)
	}
	verified, err := svc.VerifyEmail(ctx, verificationToken, signUp.Token)
	if err != nil {
		t.Fatalf("verify email: %v", err)
	}
	assertTokenScope(t, issuer, verified.Token, ScopeSession, 24*time.Hour)

	signIn, err := svc.SignIn(ctx, email, password)
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	assertTokenScope(t, issuer, signIn.Token, ScopeSession, 24*time.Hour)
}

// assertTokenScope fails the test unless raw carries the given scope claim and
// expires within a minute of ttl from now.
func assertTokenScope(t *testing.T, issuer *TokenIssuer, raw, scope string, ttl time.Duration) {
	t.Helper()

	claims, err := issuer.Parse(raw)
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}
	if claims.Scope != scope {
		t.Fatalf("token scope = %q, want %q", claims.Scope, scope)
	}
	if until := time.Until(claims.ExpiresAt.Time); until > ttl || until < ttl-time.Minute {
		t.Fatalf("token expires in %s, want ~%s", until, ttl)
	}
}
