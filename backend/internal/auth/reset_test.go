package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

func TestPasswordReset(t *testing.T) {
	svc, _, pool := newTestService(t)
	ctx := context.Background()
	email := "reset-" + uuid.NewString() + "@example.com"
	signup, err := svc.SignUp(ctx, SignUpInput{Email: email, Password: "old-password", DisplayName: "Reset"})
	if err != nil {
		t.Fatal(err)
	}
	id := signup.User.ID
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE email=$1", email) })
	if err := svc.ForgotPassword(ctx, "unknown-"+email); err != nil {
		t.Fatal(err)
	}
	if err := svc.ForgotPassword(ctx, email); err != nil {
		t.Fatal(err)
	}
	code, _, err := svc.resetCodes.Get(ctx, id)
	if err != nil || len(code) != 6 {
		t.Fatalf("code=%q err=%v", code, err)
	}
	if err := svc.ResetPassword(ctx, email, code, "weak"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("weak password: %v", err)
	}
	for i := int64(1); i <= 3; i++ {
		if err := svc.ResetPassword(ctx, email, "wrong", "new-password"); !errors.Is(err, ErrInvalidResetCode) {
			t.Fatal(err)
		}
		stored, attempts, err := svc.resetCodes.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if i < 3 && (attempts != i || stored != code) {
			t.Fatalf("attempts=%d code=%q", attempts, stored)
		}
		if i == 3 && stored != "" {
			t.Fatal("code not discarded")
		}
	}
	if err := svc.ResetPassword(ctx, email, code, "new-password"); !errors.Is(err, ErrInvalidResetCode) {
		t.Fatal(err)
	}
	if err := svc.ForgotPassword(ctx, email); err != nil {
		t.Fatal(err)
	}
	code, _, _ = svc.resetCodes.Get(ctx, id)
	if err := svc.ResetPassword(ctx, email, code, "new-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SignIn(ctx, email, "old-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old password: %v", err)
	}
	session, err := svc.SignIn(ctx, email, "new-password")
	if err != nil {
		t.Fatal(err)
	}
	if !session.User.EmailVerified {
		t.Fatal("email not verified")
	}
	if err := svc.ResetPassword(ctx, email, code, "another-password"); !errors.Is(err, ErrInvalidResetCode) {
		t.Fatalf("reused code: %v", err)
	}
	if err := svc.ForgotPassword(ctx, email); err != nil {
		t.Fatal(err)
	}
	code, _, _ = svc.resetCodes.Get(ctx, id)
	if err := svc.ResetPassword(ctx, email, code, "another-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Refresh(ctx, session.RefreshToken); err == nil {
		t.Fatal("old refresh accepted")
	}
}

func TestPasswordResetGoogleAndDeletedAccounts(t *testing.T) {
	svc, _, pool := newTestService(t)
	ctx := context.Background()
	email := "reset-google-" + uuid.NewString() + "@example.com"
	user, err := svc.queries.CreateVerifiedUser(ctx, db.CreateVerifiedUserParams{Email: email, PasswordHash: NoPasswordHash, DisplayName: "Google", Role: RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=$1", user.ID) })
	if err := svc.ForgotPassword(ctx, email); err != nil {
		t.Fatal(err)
	}
	code, _, _ := svc.resetCodes.Get(ctx, user.ID.String())
	if err := svc.ResetPassword(ctx, email, code, "new-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SignIn(ctx, email, "new-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE users SET deleted_at=now() WHERE id=$1", user.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.ForgotPassword(ctx, email); err != nil {
		t.Fatal(err)
	}
	code, _, _ = svc.resetCodes.Get(ctx, user.ID.String())
	if code != "" {
		t.Fatal("deleted account received a reset code")
	}
	if err := svc.ResetPassword(ctx, email, "123456", "new-password"); !errors.Is(err, ErrInvalidResetCode) {
		t.Fatal(err)
	}
}

func TestPasswordResetCacheIsolationAndExpiry(t *testing.T) {
	verify, mr := newTestCache(t)
	reset := NewResetCache(verify.rdb, ResetCodeTTL)
	ctx := context.Background()
	if err := verify.Store(ctx, "user", "111111"); err != nil {
		t.Fatal(err)
	}
	if err := reset.Store(ctx, "user", "222222"); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := verify.Get(ctx, "user"); code != "111111" {
		t.Fatal("reset overwrote verification code")
	}
	if got := mr.TTL("reset:code:user"); got != 15*time.Minute {
		t.Fatalf("TTL=%v", got)
	}
	if err := reset.Store(ctx, "user", "333333"); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := reset.Get(ctx, "user"); code != "333333" {
		t.Fatal("reset did not replace outstanding code")
	}
	mr.FastForward(ResetCodeTTL)
	if code, _, _ := reset.Get(ctx, "user"); code != "" {
		t.Fatal("expired reset code remains")
	}
}

func TestPasswordResetRoutes(t *testing.T) {
	svc, _, pool := newTestService(t)
	ctx := context.Background()
	email := "reset-route-" + uuid.NewString() + "@example.com"
	signup, err := svc.SignUp(ctx, SignUpInput{Email: email, Password: "old-password", DisplayName: "Reset"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE email=$1", email) })
	limiter := NewRateLimiter(svc.codes.rdb, 100, time.Minute)
	routes := NewHandler(svc, limiter).Routes(rejectAll)
	post := func(path, body string, want int) {
		t.Helper()
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		if rec.Code != want {
			t.Fatalf("%s = %d %s; want %d", path, rec.Code, rec.Body, want)
		}
	}
	post("/forgot-password", "{", 400)
	post("/reset-password", "{", 400)
	post("/forgot-password", `{"email":"unknown@example.com"}`, 202)
	post("/reset-password", `{"email":"unknown@example.com","code":"123456","password":"new-password"}`, 400)
	post("/reset-password", `{"email":"unknown@example.com","password":"weak"}`, 400)
	post("/forgot-password", `{"email":"`+email+`"}`, 202)
	code, _, _ := svc.resetCodes.Get(ctx, signup.User.ID)
	post("/reset-password", `{"email":"`+email+`","code":"`+code+`","password":"new-password"}`, 200)
}
