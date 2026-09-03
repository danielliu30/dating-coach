package auth

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"

	"github.com/danielliu30/dating-coach/backend/internal/account"
	"github.com/danielliu30/dating-coach/backend/internal/notify"
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
// deletion did not happen, and nothing may have been revoked either. The
// denylist holds a nil Redis client, so revoking would panic rather than pass.
func TestDeleteAccountFailsWhenNothingWasRecorded(t *testing.T) {
	// A pool pointed at a closed port stands in for a database that is down.
	pool, err := pgxpool.New(context.Background(), "postgres://user:pass@127.0.0.1:1/db")
	if err != nil {
		t.Fatalf("build pool: %v", err)
	}
	t.Cleanup(pool.Close)

	svc := NewService(db.New(pool), nil, nil, 0, "", NewDenylist(nil, time.Minute), failingPublisher{}, time.Hour, time.Minute)
	if err := svc.DeleteAccount(context.Background(), uuid.New()); err == nil {
		t.Fatal("DeleteAccount succeeded although the deletion was never recorded")
	}
}

// silentNotifier stands in for the SMTP notifier: sign-up sends a verification
// email, which is irrelevant to the scope of the token it returns.
type silentNotifier struct{}

// Email discards the message and reports success.
func (silentNotifier) Email(context.Context, string, string, string) error { return nil }

// Send discards the message.
func (silentNotifier) Send(context.Context, notify.Message) error { return nil }

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

// TestDeleteAccountMarksRowWhenRevocationFails pins the ordering the bound on
// the denylist entry depends on: the row is marked even when the revocation that
// follows it cannot be written, so the mark can never come after a token was
// issued.
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

	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: time.Second})
	t.Cleanup(func() { _ = rdb.Close() })
	svc.denylist = NewDenylist(rdb, time.Hour)
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
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: time.Second})
	t.Cleanup(func() { _ = rdb.Close() })
	svc.denylist = NewDenylist(rdb, time.Hour)
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

// TestNormaliseChoicesCanonicalisesAndRejectsUnknown pins the shape of a stored
// list: case and whitespace are forgiven, duplicates collapse, the result is in
// vocabulary order, and anything outside the vocabulary is refused.
func TestNormaliseChoicesCanonicalisesAndRejectsUnknown(t *testing.T) {
	got, err := normaliseChoices("dating_styles", []string{" Hinge", "in_person", "hinge", "", "TINDER"}, DatingStyles)
	if err != nil {
		t.Fatalf("normalise: %v", err)
	}
	want := []string{"in_person", "tinder", "hinge"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if _, err := normaliseChoices("dating_styles", []string{"carrier_pigeon"}, DatingStyles); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown value: got %v, want ErrInvalidInput", err)
	}
}

// TestUpdateDatingProfileRejectsPhaseOnBothSides pins that a phase cannot be
// both a strength and something being worked on; the check runs before any
// query, so a service without a database is enough.
func TestUpdateDatingProfileRejectsPhaseOnBothSides(t *testing.T) {
	svc := &Service{}
	_, err := svc.UpdateDatingProfile(context.Background(), Principal{}, DatingProfileInput{
		PhasesStrong:    []string{"flirting"},
		PhasesWorkingOn: []string{"Flirting"},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("got %v, want ErrInvalidInput", err)
	}
}
