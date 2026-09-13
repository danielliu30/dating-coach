package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
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
// same address marks it verified and signs it in, and its password still works.
func TestSignInWithGoogleVerifiesExistingPasswordAccount(t *testing.T) {
	svc, _, pool := newTestService(t)
	ctx := context.Background()

	const password = "correct-horse"
	email := "google-link-test-" + uuid.NewString() + "@example.com"
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DELETE FROM users WHERE email = $1", email); err != nil {
			t.Errorf("delete test user: %v", err)
		}
	})
	if _, err := svc.SignUp(ctx, SignUpInput{Email: email, Password: password, DisplayName: "Link Test"}); err != nil {
		t.Fatalf("sign up: %v", err)
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
	if _, err := svc.SignIn(ctx, email, password); err != nil {
		t.Fatalf("password sign in after google verified the address: %v", err)
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
