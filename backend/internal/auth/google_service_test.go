package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// stubGoogle is a GoogleTokenVerifier that maps token strings to identities;
// anything else is rejected as ErrInvalidGoogleToken.
type stubGoogle map[string]GoogleIdentity

func (s stubGoogle) Verify(_ context.Context, idToken string) (GoogleIdentity, error) {
	if id, ok := s[idToken]; ok {
		return id, nil
	}
	return GoogleIdentity{}, ErrInvalidGoogleToken
}

// TestSignInWithGoogleCreatesVerifiedAccount covers the Google path end to
// end: a first sign-in creates a verified, password-less account with the
// requested role and opens a full session at once (no emailed code), a second
// sign-in returns the same account regardless of the role now asked for, and
// the password path keeps refusing the account.
func TestSignInWithGoogleCreatesVerifiedAccount(t *testing.T) {
	svc, issuer, pool := newTestService(t)
	ctx := context.Background()

	email := "google-test-" + uuid.NewString() + "@example.com"
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DELETE FROM users WHERE email = $1", email); err != nil {
			t.Errorf("delete test user: %v", err)
		}
	})
	svc.SetGoogleVerifier(stubGoogle{"good": {Subject: "sub-1", Email: email, Name: "Google Coach"}})

	first, err := svc.SignInWithGoogle(ctx, GoogleSignInInput{IDToken: "good", Role: RoleCoach})
	if err != nil {
		t.Fatalf("first google sign in: %v", err)
	}
	assertTokenScope(t, issuer, first.Token, ScopeSession, 15*time.Minute)
	if first.RefreshToken == "" {
		t.Fatal("google sign in returned no refresh token")
	}
	if !first.User.EmailVerified || first.User.Role != RoleCoach || first.User.DisplayName != "Google Coach" || first.User.Email != email {
		t.Fatalf("created profile = %+v, want verified coach %q", first.User, email)
	}
	if _, err := svc.SignIn(ctx, email, ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("password sign in to google account = %v, want %v", err, ErrInvalidCredentials)
	}

	again, err := svc.SignInWithGoogle(ctx, GoogleSignInInput{IDToken: "good", Role: RoleUser})
	if err != nil {
		t.Fatalf("second google sign in: %v", err)
	}
	if again.User.ID != first.User.ID || again.User.Role != RoleCoach {
		t.Fatalf("second sign in profile = %+v, want same coach account as %+v", again.User, first.User)
	}
}

// TestSignInWithGoogleVerifiesExistingPasswordAccount covers an account that
// signed up with a password but never entered its code: Google confirming the
// same address removes the untrusted credentials before signing the owner in.
func TestSignInWithGoogleVerifiesExistingPasswordAccount(t *testing.T) {
	svc, issuer, pool := newTestService(t)
	ctx := context.Background()

	const password = "correct-horse"
	email := "google-link-test-" + uuid.NewString() + "@example.com"
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DELETE FROM users WHERE email = $1", email); err != nil {
			t.Errorf("delete test user: %v", err)
		}
	})
	attacker, err := svc.SignUp(ctx, SignUpInput{Email: email, Password: password, DisplayName: "Link Test"})
	if err != nil {
		t.Fatalf("sign up: %v", err)
	}
	user, err := svc.queries.GetUserByEmail(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	// Unverified sign-ups receive only a verify token; seed a refresh token to
	// cover sessions issued before that restriction too.
	refresh, err := svc.issueRefreshToken(ctx, svc.queries, user, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SignIn(ctx, email, password); !errors.Is(err, ErrEmailNotVerified) {
		t.Fatalf("sign in before verifying = %v, want %v", err, ErrEmailNotVerified)
	}

	svc.SetGoogleVerifier(stubGoogle{"good": {Subject: "sub-2", Email: email, Name: "Ignored"}})
	session, err := svc.SignInWithGoogle(ctx, GoogleSignInInput{IDToken: "good"})
	if err != nil {
		t.Fatalf("google sign in: %v", err)
	}
	if !session.User.EmailVerified || session.User.DisplayName != "Link Test" || session.User.Role != RoleUser {
		t.Fatalf("profile after google sign in = %+v, want verified existing user", session.User)
	}
	if _, err := svc.SignIn(ctx, email, password); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("attacker password sign in = %v, want invalid credentials", err)
	}
	if _, err := svc.Refresh(ctx, refresh); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Fatalf("attacker refresh = %v, want invalid refresh token", err)
	}
	claims, err := issuer.Parse(attacker.Token)
	if err != nil {
		t.Fatal(err)
	}
	since, err := svc.denylist.RevokedSince(ctx, user.ID)
	if err != nil || since.IsZero() || claims.IssuedAt.After(since) {
		t.Fatalf("attacker session not revoked: cutoff=%v err=%v", since, err)
	}
	assertTokenScope(t, issuer, session.Token, ScopeSession, 15*time.Minute)
	if _, err := svc.Refresh(ctx, session.RefreshToken); err != nil {
		t.Fatalf("victim refresh: %v", err)
	}
	stored, err := svc.queries.GetUserByID(ctx, user.ID)
	if err != nil || !stored.EmailVerified || stored.PasswordHash != NoPasswordHash || stored.SessionsRevokedAt == nil {
		t.Fatalf("claimed account = %+v, err=%v", stored, err)
	}
}

// TestCreateGoogleUserRecoversLostRace covers the insert losing to a password
// sign-up that landed between lookup and insert: the unique violation resolves
// to that account, removing the unverified password before verifying it.
func TestCreateGoogleUserRecoversLostRace(t *testing.T) {
	svc, _, pool := newTestService(t)
	ctx := context.Background()

	const password = "correct-horse"
	email := "google-race-test-" + uuid.NewString() + "@example.com"
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DELETE FROM users WHERE email = $1", email); err != nil {
			t.Errorf("delete test user: %v", err)
		}
	})
	if _, err := svc.SignUp(ctx, SignUpInput{Email: email, Password: password, DisplayName: "Race Test"}); err != nil {
		t.Fatalf("sign up: %v", err)
	}

	user, err := svc.createGoogleUser(ctx, GoogleIdentity{Subject: "sub-4", Email: email, Name: "Ignored"}, RoleCoach)
	if err != nil {
		t.Fatalf("createGoogleUser after a lost race: %v", err)
	}
	if !user.EmailVerified || user.DisplayName != "Race Test" || user.Role != RoleUser || user.PasswordHash != NoPasswordHash {
		t.Fatalf("recovered user = %+v, want verified password-less account", user)
	}
	if _, err := svc.SignIn(ctx, email, password); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("password sign in after recovery = %v, want invalid credentials", err)
	}
}

func TestSignInWithGooglePreservesVerifiedPasswordAccount(t *testing.T) {
	svc, _, pool := newTestService(t)
	ctx := context.Background()
	email := "google-verified-" + uuid.NewString() + "@example.com"
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, "DELETE FROM users WHERE email = $1", email); err != nil {
			t.Errorf("delete test user: %v", err)
		}
	})
	const password = "correct-horse"
	signup, err := svc.SignUp(ctx, SignUpInput{Email: email, Password: password, DisplayName: "Verified"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.queries.VerifyUserEmail(ctx, uuid.MustParse(signup.User.ID)); err != nil {
		t.Fatal(err)
	}
	before, err := svc.SignIn(ctx, email, password)
	if err != nil {
		t.Fatal(err)
	}
	svc.SetGoogleVerifier(stubGoogle{"good": {Email: email, Name: "Ignored"}})
	session, err := svc.SignInWithGoogle(ctx, GoogleSignInInput{IDToken: "good", Role: RoleCoach})
	if err != nil {
		t.Fatal(err)
	}
	if session.User.ID != before.User.ID || !session.User.EmailVerified || session.User.DisplayName != "Verified" || session.User.Role != RoleUser {
		t.Fatalf("linked profile = %+v", session.User)
	}
	if _, err := svc.SignIn(ctx, email, password); err != nil {
		t.Fatalf("verified password sign in: %v", err)
	}
	if _, err := svc.Refresh(ctx, before.RefreshToken); err != nil {
		t.Fatalf("existing verified refresh: %v", err)
	}
	if since, err := svc.denylist.RevokedSince(ctx, uuid.MustParse(before.User.ID)); err != nil || !since.IsZero() {
		t.Fatalf("verified account revoked: %v, err=%v", since, err)
	}
}

func TestClaimGoogleUserMissing(t *testing.T) {
	svc, _, _ := newTestService(t)
	if _, err := svc.claimGoogleUser(context.Background(), uuid.New()); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("claim missing account = %v, want invalid credentials", err)
	}
}

func TestSignInWithGoogleRevocationFailure(t *testing.T) {
	svc, issuer, pool := newTestService(t)
	ctx := context.Background()
	email := "google-revoke-failure-" + uuid.NewString() + "@example.com"
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, "DELETE FROM users WHERE email = $1", email); err != nil {
			t.Errorf("delete test user: %v", err)
		}
	})
	const password = "correct-horse"
	attacker, err := svc.SignUp(ctx, SignUpInput{Email: email, Password: password, DisplayName: "Failure"})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetGoogleVerifier(stubGoogle{"good": {Email: email}})
	redisOptions := svc.denylist.rdb.Options()
	if err := svc.denylist.rdb.Close(); err != nil {
		t.Fatal(err)
	}
	session, err := svc.SignInWithGoogle(ctx, GoogleSignInInput{IDToken: "good"})
	if err == nil || session.Token != "" || session.RefreshToken != "" {
		t.Fatalf("revocation failure returned session: %+v, err=%v", session, err)
	}
	user, err := svc.queries.GetUserByEmail(ctx, email)
	if err != nil || user.EmailVerified || user.PasswordHash == NoPasswordHash || user.SessionsRevokedAt != nil {
		t.Fatalf("claim did not roll back after Redis failure: %+v, err=%v", user, err)
	}
	rdb := redis.NewClient(redisOptions)
	t.Cleanup(func() { _ = rdb.Close() })
	svc.denylist = NewDenylist(rdb, 15*time.Minute)
	session, err = svc.SignInWithGoogle(ctx, GoogleSignInInput{IDToken: "good"})
	if err != nil || !session.User.EmailVerified {
		t.Fatalf("retry Google sign in: verified=%v, err=%v", session.User.EmailVerified, err)
	}
	protected := Middleware(issuer, nil, svc.denylist)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, tc := range []struct {
		name   string
		token  string
		status int
	}{
		{"attacker", attacker.Token, http.StatusUnauthorized},
		{"victim", session.Token, http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			req.Header.Set("Authorization", "Bearer "+tc.token)
			resp := httptest.NewRecorder()
			protected.ServeHTTP(resp, req)
			if resp.Code != tc.status {
				t.Fatalf("status = %d, want %d", resp.Code, tc.status)
			}
		})
	}
	if _, err := svc.Refresh(ctx, session.RefreshToken); err != nil {
		t.Fatalf("victim refresh after retry: %v", err)
	}
	if _, err := svc.SignIn(ctx, email, password); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("password after revocation failure = %v, want invalid credentials", err)
	}
}

// TestSignInWithGoogleRejects covers the refusals that never reach the store:
// no verifier configured, a bad role, and a token the verifier rejects.
func TestSignInWithGoogleRejects(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.SignInWithGoogle(ctx, GoogleSignInInput{IDToken: "good"}); !errors.Is(err, ErrGoogleAuthDisabled) {
		t.Fatalf("without verifier = %v, want %v", err, ErrGoogleAuthDisabled)
	}
	svc.SetGoogleVerifier(stubGoogle{"good": {Subject: "sub-3", Email: "never-created@example.com"}})
	if _, err := svc.SignInWithGoogle(ctx, GoogleSignInInput{IDToken: "good", Role: RoleAdmin}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("admin role = %v, want %v", err, ErrInvalidInput)
	}
	if _, err := svc.SignInWithGoogle(ctx, GoogleSignInInput{IDToken: "forged"}); !errors.Is(err, ErrInvalidGoogleToken) {
		t.Fatalf("forged token = %v, want %v", err, ErrInvalidGoogleToken)
	}
	if _, err := svc.queries.GetUserByEmail(ctx, "never-created@example.com"); err == nil {
		t.Fatal("a refused sign in must not create an account")
	}
}
