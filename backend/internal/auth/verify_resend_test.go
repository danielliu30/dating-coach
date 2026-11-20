package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

type verificationNotifier struct {
	silentNotifier
	sent atomic.Int64
}

func (n *verificationNotifier) Email(context.Context, string, string, string) error {
	n.sent.Add(1)
	return nil
}

func TestVerificationResendCooldown(t *testing.T) {
	svc, _, pool := newTestService(t)
	ctx := context.Background()
	cache, mr := newTestCache(t)
	svc.codes = NewVerificationCache(cache.rdb, VerificationCodeTTL, time.Minute)
	notifier := &verificationNotifier{}
	svc.notifier = notifier
	signup := func() Session {
		email := "verify-cooldown-" + uuid.NewString() + "@example.com"
		t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE email=$1", email) })
		session, err := svc.SignUp(ctx, SignUpInput{Email: email, Password: "old-password", DisplayName: "Verify"})
		if err != nil {
			t.Fatal(err)
		}
		return session
	}
	first, second := signup(), signup()
	if notifier.sent.Load() != 2 {
		t.Fatal("signup cooldown affected another account's initial send")
	}
	if err := svc.codes.Store(ctx, first.User.ID, "original"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.codes.IncrementAttempts(ctx, first.User.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.ResendVerification(ctx, "  "+strings.ToUpper(first.User.Email)+"  "); err != nil {
		t.Fatal(err)
	}
	code, attempts, err := svc.codes.Get(ctx, first.User.ID)
	if err != nil || code != "original" || attempts != 1 || notifier.sent.Load() != 2 {
		t.Fatalf("cooldown changed code/attempts or sent email: code=%q attempts=%d emails=%d err=%v", code, attempts, notifier.sent.Load(), err)
	}

	mr.FastForward(time.Minute)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := svc.ResendVerification(ctx, first.User.Email); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	code, attempts, err = svc.codes.Get(ctx, first.User.ID)
	if err != nil || code == "original" || code == "" || attempts != 0 || notifier.sent.Load() != 3 {
		t.Fatalf("expired cooldown must allow exactly one send: code=%q attempts=%d emails=%d err=%v", code, attempts, notifier.sent.Load(), err)
	}
	if err := svc.ResendVerification(ctx, second.User.Email); err != nil {
		t.Fatal(err)
	}
	if notifier.sent.Load() != 4 {
		t.Fatal("one account's resend blocked another account")
	}

	if err := svc.codes.Invalidate(ctx, first.User.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.ResendVerification(ctx, first.User.Email); err != nil {
		t.Fatal(err)
	}
	if got, _, err := svc.codes.Get(ctx, first.User.ID); err != nil || got != "" || notifier.sent.Load() != 4 {
		t.Fatalf("invalidating the code bypassed cooldown: code=%q emails=%d err=%v", got, notifier.sent.Load(), err)
	}
}

func TestVerificationResendRedisFailure(t *testing.T) {
	svc, _, pool := newTestService(t)
	ctx := context.Background()
	cache, mr := newTestCache(t)
	svc.codes = cache
	notifier := &verificationNotifier{}
	svc.notifier = notifier
	email := "verify-redis-" + uuid.NewString() + "@example.com"
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE email=$1", email) })
	if _, err := svc.SignUp(ctx, SignUpInput{Email: email, Password: "old-password", DisplayName: "Verify"}); err != nil {
		t.Fatal(err)
	}
	mr.SetError("redis unavailable")
	if err := svc.ResendVerification(ctx, email); err == nil {
		t.Fatal("resend must fail closed when Redis cannot enforce the cooldown")
	}
	if notifier.sent.Load() != 1 {
		t.Fatal("resend sent an email without claiming the cooldown")
	}
}

func TestVerificationResendResponse(t *testing.T) {
	svc, _, pool := newTestService(t)
	ctx := context.Background()
	svc.codes.cooldown = 90 * time.Second
	email := "verify-response-" + uuid.NewString() + "@example.com"
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE email=$1", email) })
	if _, err := svc.SignUp(ctx, SignUpInput{Email: email, Password: "old-password", DisplayName: "Verify"}); err != nil {
		t.Fatal(err)
	}
	routes := NewHandler(svc, NewRateLimiter(svc.codes.rdb, 100, time.Minute)).Routes(rejectAll)
	check := func(address string) {
		t.Helper()
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/resend-verification", strings.NewReader(`{"email":"`+address+`"}`)))
		var response struct {
			Status             string `json:"status"`
			ResendAfterSeconds int    `json:"resend_after_seconds"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusAccepted || response.Status != "sent" || response.ResendAfterSeconds != 90 {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
		}
	}
	check(email)
	check(email)
	check("unknown-" + email)
	check("")
	if _, err := pool.Exec(ctx, "UPDATE users SET email_verified=true WHERE email=$1", email); err != nil {
		t.Fatal(err)
	}
	check(email)
}
