package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestLoginThrottleBlocksBeforeCredentialLookup(t *testing.T) {
	cache, mr := newTestCache(t)
	throttle := NewLoginThrottle(cache.rdb, 3, 15*time.Minute)
	ctx := context.Background()
	for range 3 {
		if err := throttle.failed(ctx, " User@Example.com "); err != nil {
			t.Fatal(err)
		}
	}
	svc := &Service{loginThrottle: throttle}
	if _, err := svc.SignIn(ctx, "user@example.com", "correct-password"); !errors.Is(err, ErrLoginThrottled) {
		t.Fatalf("SignIn=%v; must not reach missing database or password checker", err)
	}
	if err := throttle.check(ctx, "another@example.com"); err != nil {
		t.Fatal("another account was blocked:", err)
	}
	mr.FastForward(15 * time.Minute)
	if err := throttle.check(ctx, "user@example.com"); err != nil {
		t.Fatal("cooldown did not expire:", err)
	}
}

func TestLoginThrottleCountsFailuresAndResetsOnSuccess(t *testing.T) {
	svc, _, pool := newTestService(t)
	user := verifiedUser(t, svc, pool)
	svc.SetLoginThrottle(NewLoginThrottle(svc.codes.rdb, 3, 15*time.Minute))
	ctx := context.Background()
	for range 2 {
		if _, err := svc.SignIn(ctx, user.email, "wrong"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatal(err)
		}
	}
	if _, err := svc.SignIn(ctx, user.email, user.password); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, err := svc.SignIn(ctx, strings.ToUpper(user.email), "wrong"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("failure %d after success: %v", i+1, err)
		}
	}
	if _, err := svc.SignIn(ctx, user.email, user.password); !errors.Is(err, ErrLoginThrottled) {
		t.Fatalf("correct password bypassed lockout: %v", err)
	}
}

func TestUnknownEmailsAreLoginThrottled(t *testing.T) {
	svc, _, _ := newTestService(t)
	svc.SetLoginThrottle(NewLoginThrottle(svc.codes.rdb, 3, 15*time.Minute))
	email := "unknown-" + uuid.NewString() + "@example.com"
	routes := NewHandler(svc, NewRateLimiter(svc.codes.rdb, 100, time.Minute)).Routes(rejectAll)
	for i := range 4 {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/signin", strings.NewReader(`{"email":" `+strings.ToUpper(email)+` ","password":"wrong"}`)))
		want := http.StatusUnauthorized
		if i == 3 {
			want = http.StatusTooManyRequests
			if rec.Body.String() != "{\"error\":\"too many requests\"}\n" || rec.Header().Get("Retry-After") != "900" {
				t.Fatalf("unexpected throttle response: headers=%v body=%s", rec.Header(), rec.Body)
			}
		}
		if rec.Code != want {
			t.Fatalf("attempt %d status=%d want=%d body=%s", i+1, rec.Code, want, rec.Body)
		}
	}
}

func TestLoginFailureCounterIsAtomic(t *testing.T) {
	cache, mr := newTestCache(t)
	throttle := NewLoginThrottle(cache.rdb, 10, time.Minute)
	var wg sync.WaitGroup
	for range 25 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := throttle.failed(context.Background(), "user@example.com"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got, err := cache.rdb.Get(context.Background(), loginFailureKey("user@example.com")).Int(); err != nil || got != 25 {
		t.Fatalf("count=%d err=%v", got, err)
	}
	mr.FastForward(30 * time.Second)
	if err := throttle.failed(context.Background(), "user@example.com"); err != nil {
		t.Fatal(err)
	}
	mr.FastForward(30 * time.Second)
	if err := throttle.check(context.Background(), "user@example.com"); err != nil {
		t.Fatal("failures extended the window:", err)
	}
}

func TestLoginThrottleRedisFailureIsClosed(t *testing.T) {
	cache, mr := newTestCache(t)
	throttle := NewLoginThrottle(cache.rdb, 10, time.Minute)
	mr.SetError("redis unavailable")
	svc := &Service{loginThrottle: throttle}
	if _, err := svc.SignIn(context.Background(), "user@example.com", "password"); !errors.Is(err, ErrLoginThrottleUnavailable) {
		t.Fatalf("SignIn should fail before credential lookup: %v", err)
	}
	if err := throttle.failed(context.Background(), "user@example.com"); !errors.Is(err, ErrLoginThrottleUnavailable) {
		t.Fatal(err)
	}
	if err := throttle.clear(context.Background(), "user@example.com"); !errors.Is(err, ErrLoginThrottleUnavailable) {
		t.Fatal(err)
	}
}
