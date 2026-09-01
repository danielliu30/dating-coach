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

// TestDeleteAccountFailsWhenNothingWasRecorded pins where a deletion becomes
// certain: the database. Nothing was written, so the caller has to be told the
// deletion did not happen, and nothing may have been revoked either. The refresh
// store is nil, so revoking would panic rather than pass.
func TestDeleteAccountFailsWhenNothingWasRecorded(t *testing.T) {
	// A pool pointed at a closed port stands in for a database that is down.
	pool, err := pgxpool.New(context.Background(), "postgres://user:pass@127.0.0.1:1/db")
	if err != nil {
		t.Fatalf("build pool: %v", err)
	}
	t.Cleanup(pool.Close)

	svc := NewService(db.New(pool), nil, nil, 0, "", NewRefreshTokens(nil, time.Hour), failingPublisher{}, nil, time.Hour, time.Minute)
	if err := svc.DeleteAccount(context.Background(), uuid.New()); err == nil {
		t.Fatal("DeleteAccount succeeded although the deletion was never recorded")
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
	queries := db.New(pool)
	svc := NewService(queries, issuer, silentNotifier{}, bcrypt.MinCost, "http://app.test", NewRefreshTokens(queries, 24*time.Hour), nil, nil, 15*time.Minute, 30*time.Minute)
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

// TestVerifyEmailKeepsTheAddressVerifiedWhenTheSessionFails covers the
// asymmetry between the two writes: confirming the address consumes the emailed
// token, so a later failure must not be reported as a failed verification the
// caller would retry with a token that no longer exists.
func TestVerifyEmailKeepsTheAddressVerifiedWhenTheSessionFails(t *testing.T) {
	svc, _, pool := newTestService(t)
	ctx := context.Background()

	email := "verify-session-" + uuid.NewString() + "@example.com"
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DELETE FROM users WHERE email = $1", email); err != nil {
			t.Errorf("delete test user: %v", err)
		}
	})
	signUp, err := svc.SignUp(ctx, SignUpInput{Email: email, Password: "correct-horse", DisplayName: "Verify Session"})
	if err != nil {
		t.Fatalf("sign up: %v", err)
	}
	var verificationToken string
	if err := pool.QueryRow(ctx, "SELECT verification_token FROM users WHERE email = $1", email).Scan(&verificationToken); err != nil {
		t.Fatalf("read verification token: %v", err)
	}

	svc.refresh = NewRefreshTokens(
		&stubRefreshStore{createErr: errors.New("connection refused")},
		time.Hour,
	)

	verified, err := svc.VerifyEmail(ctx, verificationToken, signUp.Token)
	if err != nil {
		t.Fatalf("verify email with the refresh store down: %v", err)
	}
	if !verified.User.EmailVerified {
		t.Fatal("the response denies a verification that already happened")
	}
	if verified.Token != "" || verified.RefreshToken != "" {
		t.Fatal("a session was reported despite the refresh token never being stored")
	}
	var stored bool
	if err := pool.QueryRow(ctx, "SELECT email_verified FROM users WHERE email = $1", email).Scan(&stored); err != nil {
		t.Fatalf("read email_verified: %v", err)
	}
	if !stored {
		t.Fatal("the address was left unverified, so the consumed token is unrecoverable")
	}
}

// TestSignInRefusesAccountPendingDeletion covers the window between requesting
// a deletion and the worker removing the row: the credentials still match, but
// sign-in must not hand out a session that would outlive the revocation if the
// removal stalls.
func TestSignInRefusesAccountPendingDeletion(t *testing.T) {
	svc, _, pool := newTestService(t)
	ctx := context.Background()

	const password = "correct-horse"
	email := "deletion-test-" + uuid.NewString() + "@example.com"
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DELETE FROM users WHERE email = $1", email); err != nil {
			t.Errorf("delete test user: %v", err)
		}
	})

	if _, err := svc.SignUp(ctx, SignUpInput{Email: email, Password: password, DisplayName: "Deletion Test"}); err != nil {
		t.Fatalf("sign up: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE users SET email_verified = true WHERE email = $1", email); err != nil {
		t.Fatalf("mark verified: %v", err)
	}
	if _, err := svc.SignIn(ctx, email, password); err != nil {
		t.Fatalf("sign in before deletion: %v", err)
	}

	if _, err := pool.Exec(ctx, "UPDATE users SET deleted_at = now() WHERE email = $1", email); err != nil {
		t.Fatalf("mark deleted: %v", err)
	}
	if _, err := svc.SignIn(ctx, email, password); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("sign in after deletion = %v, want %v", err, ErrInvalidCredentials)
	}
}

// TestDeleteAccountMarksRowWhenRevocationFails pins the ordering the deletion
// depends on: the row is marked even when the revocation that follows it cannot
// be written, so no session can be opened for the account afterwards even while
// the tokens it already handed out are still being revoked.
func TestDeleteAccountMarksRowWhenRevocationFails(t *testing.T) {
	svc, _, pool := newTestService(t)
	ctx := context.Background()

	email := "revoke-order-" + uuid.NewString() + "@example.com"
	t.Cleanup(func() { deleteTestUser(t, pool, email) })
	signUp, err := svc.SignUp(ctx, SignUpInput{Email: email, Password: "correct-horse", DisplayName: "Revoke Order"})
	if err != nil {
		t.Fatalf("sign up: %v", err)
	}
	userID, err := uuid.Parse(signUp.User.ID)
	if err != nil {
		t.Fatalf("parse user id: %v", err)
	}

	svc.refresh = NewRefreshTokens(
		&stubRefreshStore{revokeAllErr: errors.New("connection refused")},
		time.Hour,
	)
	svc.deletions = &stubPublisher{}

	if err := svc.DeleteAccount(ctx, userID); err != nil {
		t.Fatalf("delete account: %v", err)
	}
	var deletedAt *time.Time
	if err := pool.QueryRow(ctx, "SELECT deleted_at FROM users WHERE id = $1", userID).Scan(&deletedAt); err != nil {
		t.Fatalf("read deleted_at: %v", err)
	}
	if deletedAt == nil {
		t.Fatal("account was not marked deleted before revocation was attempted")
	}
}

// TestDeleteAccountRecordsWhatTheBrokerRefused covers a deletion requested while
// RabbitMQ is unreachable: the request still succeeds, because the mark and the
// outbox row are together what make the deletion certain, and the row is left
// unpublished for the relay to queue once the broker is back.
func TestDeleteAccountRecordsWhatTheBrokerRefused(t *testing.T) {
	svc, _, pool := newTestService(t)
	ctx := context.Background()

	email := "outbox-order-" + uuid.NewString() + "@example.com"
	t.Cleanup(func() { deleteTestUser(t, pool, email) })
	signUp, err := svc.SignUp(ctx, SignUpInput{Email: email, Password: "correct-horse", DisplayName: "Outbox Order"})
	if err != nil {
		t.Fatalf("sign up: %v", err)
	}
	userID, err := uuid.Parse(signUp.User.ID)
	if err != nil {
		t.Fatalf("parse user id: %v", err)
	}
	svc.deletions = failingPublisher{}

	if err := svc.DeleteAccount(ctx, userID); err != nil {
		t.Fatalf("delete account: %v", err)
	}
	var publishedAt *time.Time
	if err := pool.QueryRow(
		ctx,
		"SELECT published_at FROM account_deletions WHERE user_id = $1",
		userID,
	).Scan(&publishedAt); err != nil {
		t.Fatalf("read outbox row: %v", err)
	}
	if publishedAt != nil {
		t.Fatalf("deletion marked published at %s although the broker refused it", publishedAt)
	}
}

// deleteTestUser removes the account a test created, along with the outbox row a
// recorded deletion left for it, which outlives the user row by design.
func deleteTestUser(t *testing.T, pool *pgxpool.Pool, email string) {
	t.Helper()

	ctx := context.Background()
	if _, err := pool.Exec(
		ctx,
		"DELETE FROM account_deletions WHERE user_id IN (SELECT id FROM users WHERE email = $1)",
		email,
	); err != nil {
		t.Errorf("delete test outbox row: %v", err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM users WHERE email = $1", email); err != nil {
		t.Errorf("delete test user: %v", err)
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
