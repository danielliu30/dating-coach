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
	svc := NewService(db.New(pool), issuer, silentNotifier{}, bcrypt.MinCost, "http://app.test", nil, 15*time.Minute, 30*time.Minute, 24*time.Hour)
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
	assertTokenScope(t, issuer, verified.Token, ScopeSession, 15*time.Minute)
	if verified.RefreshToken == "" {
		t.Fatal("verifying as the account being verified must return a refresh token")
	}

	signIn, err := svc.SignIn(ctx, email, password)
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	assertTokenScope(t, issuer, signIn.Token, ScopeSession, 15*time.Minute)
	if signIn.RefreshToken == "" {
		t.Fatal("signing in must return a refresh token")
	}
}

// TestRefreshRotatesAndDetectsReuse covers the whole rotation contract against
// the real queries: an exchange yields a new pair, the token it consumed is
// dead, and presenting that dead token takes the account's other refresh tokens
// down with it.
func TestRefreshRotatesAndDetectsReuse(t *testing.T) {
	svc, issuer, pool := newTestService(t)
	ctx := context.Background()

	user := verifiedUser(t, svc, pool)
	first, err := svc.SignIn(ctx, user.email, user.password)
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}

	second, err := svc.Refresh(ctx, first.RefreshToken)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	assertTokenScope(t, issuer, second.Token, ScopeSession, 15*time.Minute)
	if second.RefreshToken == first.RefreshToken {
		t.Fatal("refresh must rotate the token, not hand the same one back")
	}

	if _, err := svc.Refresh(ctx, first.RefreshToken); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Fatalf("reusing a spent token: error = %v, want %v", err, ErrInvalidRefreshToken)
	}
	if _, err := svc.Refresh(ctx, second.RefreshToken); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Fatalf("after detected reuse: error = %v, want %v", err, ErrInvalidRefreshToken)
	}
}

// TestDeleteAccountEndsRefreshing covers the replacement for the denylist:
// deleting an account leaves its refresh tokens unusable, so the sessions it
// holds cannot outlive their own access tokens.
func TestDeleteAccountEndsRefreshing(t *testing.T) {
	svc, _, pool := newTestService(t)
	ctx := context.Background()

	publisher := &stubPublisher{}
	svc.deletions = publisher
	user := verifiedUser(t, svc, pool)
	session, err := svc.SignIn(ctx, user.email, user.password)
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}

	if err := svc.DeleteAccount(ctx, uuid.MustParse(session.User.ID)); err != nil {
		t.Fatalf("delete account: %v", err)
	}
	if len(publisher.jobs) != 1 {
		t.Fatalf("queued %d deletion jobs, want 1", len(publisher.jobs))
	}
	if _, err := svc.Refresh(ctx, session.RefreshToken); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Fatalf("refresh after deletion: error = %v, want %v", err, ErrInvalidRefreshToken)
	}
}

// credentials identifies the throwaway account a test signs in as.
type credentials struct{ email, password string }

// verifiedUser signs a fresh account up, confirms its address straight in the
// database and registers its removal, so a test can sign in as it.
func verifiedUser(t *testing.T, svc *Service, pool *pgxpool.Pool) credentials {
	t.Helper()

	ctx := context.Background()
	user := credentials{email: "refresh-test-" + uuid.NewString() + "@example.com", password: "correct-horse"}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DELETE FROM users WHERE email = $1", user.email); err != nil {
			t.Errorf("delete test user: %v", err)
		}
	})
	if _, err := svc.SignUp(ctx, SignUpInput{Email: user.email, Password: user.password, DisplayName: "Refresh Test"}); err != nil {
		t.Fatalf("sign up: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE users SET email_verified = true WHERE email = $1", user.email); err != nil {
		t.Fatalf("verify test user: %v", err)
	}
	return user
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
