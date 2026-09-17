package auth

import (
	"context"
	"errors"
	"os"
	"strings"
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

	svc := NewService(pool, db.New(pool), nil, nil, 0, "", nil, NewDenylist(nil, time.Minute), failingPublisher{}, time.Hour, time.Minute, 24*time.Hour)
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
// a Service wired to it and to an in-process Redis for verification codes,
// skipping the test when the variable is unset so the suite still runs without
// Postgres. TTLs are distinct so a test can tell which one a token was minted
// with.
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

	codes, mr := newTestCache(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	issuer := NewTokenIssuer("test-secret")
	svc := NewService(pool, db.New(pool), issuer, silentNotifier{}, bcrypt.MinCost, "http://app.test", codes, NewDenylist(rdb, 15*time.Minute), nil, 15*time.Minute, 30*time.Minute, 24*time.Hour)
	return svc, issuer, pool
}

// TestSignUpMintsVerifyScopedToken covers the scope split end to end, through
// the real queries: the new account exists but its token is verify-scoped and
// short-lived, and only confirming the address — by verifying with that token,
// or by signing in afterwards — yields a session-scoped one. Along the way a
// wrong code is refused and leaves the right one usable.
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

	code, _, err := svc.codes.Get(ctx, signUp.User.ID)
	if err != nil || code == "" {
		t.Fatalf("read verification code: code=%q err=%v", code, err)
	}
	wrong := "000000"
	if wrong == code {
		wrong = "000001"
	}
	if _, err := svc.VerifyEmail(ctx, email, wrong, signUp.Token); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("wrong code: error = %v, want %v", err, ErrInvalidCode)
	}
	verified, err := svc.VerifyEmail(ctx, email, code, signUp.Token)
	if err != nil {
		t.Fatalf("verify email: %v", err)
	}
	if !verified.User.EmailVerified {
		t.Fatal("verifying with the right code must mark the address verified")
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

// TestRotationKeepsTheFamilyAndSignInStartsANewOne covers what the family is
// for: every token a session rotates through belongs to one chain, so a later
// reuse can be scoped to it, while a second sign-in is a chain of its own and
// stays outside the first one.
func TestRotationKeepsTheFamilyAndSignInStartsANewOne(t *testing.T) {
	svc, _, pool := newTestService(t)
	ctx := context.Background()

	user := verifiedUser(t, svc, pool)
	first, err := svc.SignIn(ctx, user.email, user.password)
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	rotated, err := svc.Refresh(ctx, first.RefreshToken)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	other, err := svc.SignIn(ctx, user.email, user.password)
	if err != nil {
		t.Fatalf("second sign in: %v", err)
	}

	firstFamily := familyOf(t, pool, first.RefreshToken)
	if got := familyOf(t, pool, rotated.RefreshToken); got != firstFamily {
		t.Fatalf("rotated into family %s, want the presented token's %s", got, firstFamily)
	}
	if got := familyOf(t, pool, other.RefreshToken); got == firstFamily {
		t.Fatalf("signing in joined family %s, want a new one", got)
	}
}

// familyOf reads the stored family of a plain refresh token, which is the only
// way to observe it: the family is deliberately absent from the API response.
func familyOf(t *testing.T, pool *pgxpool.Pool, token string) uuid.UUID {
	t.Helper()

	var family uuid.UUID
	err := pool.QueryRow(context.Background(),
		"SELECT family_id FROM refresh_tokens WHERE token_hash = $1", hashRefreshToken(token),
	).Scan(&family)
	if err != nil {
		t.Fatalf("read refresh token family: %v", err)
	}
	return family
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
		t.Fatal("account was not marked deleted before the job was queued")
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

// TestValidatePassword pins the three password rules: the character minimum,
// the byte maximum — bcrypt's input limit, which would otherwise surface as an
// internal error from GenerateFromPassword — and the whitespace-only refusal.
func TestValidatePassword(t *testing.T) {
	cases := []struct {
		name     string
		password string
		wantErr  bool
	}{
		{"accepted", "correct-horse", false},
		{"exactly min", strings.Repeat("a", MinPasswordLen), false},
		{"exactly max", strings.Repeat("a", MaxPasswordLen), false},
		{"multibyte within max", strings.Repeat("é", MaxPasswordLen/2), false},
		{"too short", strings.Repeat("a", MinPasswordLen-1), true},
		{"multibyte too short", strings.Repeat("é", MinPasswordLen-1), true},
		{"one byte over max", strings.Repeat("a", MaxPasswordLen+1), true},
		{"multibyte over max", strings.Repeat("é", MaxPasswordLen/2+1), true},
		{"only spaces", strings.Repeat(" ", MinPasswordLen), true},
		{"only mixed whitespace", " \t\n\r    ", true},
		{"padded but not blank", "   abc   ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePassword(tc.password)
			if tc.wantErr && !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("got %v, want ErrInvalidInput", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("got %v, want nil", err)
			}
			if err != nil && strings.Contains(err.Error(), tc.password) {
				t.Fatalf("error %q echoes the password", err)
			}
		})
	}
}

// TestSignUpRejectsInvalidPassword pins that SignUp applies validatePassword
// before any query or hashing runs: an over-long or whitespace-only password
// comes back as ErrInvalidInput (HTTP 400) rather than reaching bcrypt, so a
// Service without a database is enough.
func TestSignUpRejectsInvalidPassword(t *testing.T) {
	svc := &Service{}
	for name, password := range map[string]string{
		"too short":     "short",
		"over 72 bytes": strings.Repeat("a", MaxPasswordLen+1),
		"whitespace":    strings.Repeat(" ", MinPasswordLen),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.SignUp(context.Background(), SignUpInput{
				Email:       "password-test@example.com",
				Password:    password,
				DisplayName: "Password Test",
			})
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("got %v, want ErrInvalidInput", err)
			}
		})
	}
}

// TestUpdateDatingProfileRejectsOverlongPreferences pins the free-text cap:
// the ML analyzer refuses longer preferences, so the API must too, before any
// query runs.
func TestUpdateDatingProfileRejectsOverlongPreferences(t *testing.T) {
	svc := &Service{}
	_, err := svc.UpdateDatingProfile(context.Background(), Principal{}, DatingProfileInput{
		DatingPreferences: strings.Repeat("é", MaxDatingPreferencesLen+1),
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("got %v, want ErrInvalidInput", err)
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

// TestSignInRefusesAccountWithoutPassword covers accounts created through an
// external identity provider: CreateVerifiedUser stores them already verified
// with NoPasswordHash, and no password — not even the empty one that would
// match a naive comparison — signs them in.
func TestSignInRefusesAccountWithoutPassword(t *testing.T) {
	svc, _, pool := newTestService(t)
	ctx := context.Background()

	email := "passwordless-test-" + uuid.NewString() + "@example.com"
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DELETE FROM users WHERE email = $1", email); err != nil {
			t.Errorf("delete test user: %v", err)
		}
	})

	user, err := svc.queries.CreateVerifiedUser(ctx, db.CreateVerifiedUserParams{
		Email:        email,
		PasswordHash: NoPasswordHash,
		DisplayName:  "Passwordless Test",
		Role:         RoleCoach,
	})
	if err != nil {
		t.Fatalf("create verified user: %v", err)
	}
	if !user.EmailVerified || user.Role != RoleCoach {
		t.Fatalf("created user verified=%v role=%q, want verified coach", user.EmailVerified, user.Role)
	}

	for _, password := range []string{"", NoPasswordHash, "anything"} {
		if _, err := svc.SignIn(ctx, email, password); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("sign in with password %q = %v, want %v", password, err, ErrInvalidCredentials)
		}
	}
}
