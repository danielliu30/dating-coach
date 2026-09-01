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

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

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
	queries := db.New(pool)
	svc := NewService(queries, issuer, silentNotifier{}, bcrypt.MinCost, "http://app.test", NewRefreshTokens(queries, 24*time.Hour), nil, 15*time.Minute, 30*time.Minute)
	return svc, issuer, pool
}

// TestSignUpMintsVerifyScopedToken covers the scope split end to end, through
// the real queries: the new account exists but its token is verify-scoped and
// short-lived, and only confirming the address — by verifying with that token,
// or by signing in afterwards — yields a session-scoped one paired with a
// refresh token.
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
	assertTokenScope(t, issuer, verified.Token, ScopeSession, 15*time.Minute)
	if verified.RefreshToken == "" {
		t.Fatal("verification opened a session without a refresh token, so it dies in 15 minutes")
	}

	signIn, err := svc.SignIn(ctx, email, password)
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	assertTokenScope(t, issuer, signIn.Token, ScopeSession, 15*time.Minute)
	if signIn.RefreshToken == "" || signIn.RefreshToken == verified.RefreshToken {
		t.Fatal("sign-in must mint its own refresh token")
	}

	refreshed, err := svc.Refresh(ctx, signIn.RefreshToken)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	assertTokenScope(t, issuer, refreshed.Token, ScopeSession, 15*time.Minute)
	if refreshed.RefreshToken == signIn.RefreshToken {
		t.Fatal("refresh did not rotate the token")
	}
	if _, err := svc.Refresh(ctx, signIn.RefreshToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("reusing the spent refresh token: err = %v, want ErrInvalidToken", err)
	}
	// The replay above means that chain leaked, so its live token goes with it.
	if _, err := svc.Refresh(ctx, refreshed.RefreshToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("live token of the replayed chain: err = %v, want ErrInvalidToken", err)
	}
	if _, err := svc.Refresh(ctx, verified.RefreshToken); err != nil {
		t.Fatalf("the session opened at verification was revoked too: %v", err)
	}
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
