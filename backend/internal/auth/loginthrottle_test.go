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

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLoginThrottleBlocksBeforeCredentialLookup(t *testing.T) {
	cache, mr := newTestCache(t)
	throttle := NewLoginThrottle(cache.rdb, 3, 15*time.Minute)
	ctx := context.Background()
	for range 3 {
		if _, err := throttle.reserve(ctx, " User@Example.com "); err != nil {
			t.Fatal(err)
		}
	}
	svc := &Service{loginThrottle: throttle}
	if _, err := svc.SignIn(ctx, "user@example.com", "correct-password"); !errors.Is(err, ErrLoginThrottled) {
		t.Fatalf("SignIn=%v; must not reach missing database or password checker", err)
	}
	if _, err := throttle.reserve(ctx, "another@example.com"); err != nil {
		t.Fatal("another account was blocked:", err)
	}
	mr.FastForward(15 * time.Minute)
	if _, err := throttle.reserve(ctx, "user@example.com"); err != nil {
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

func TestLoginAttemptReservationIsAtomic(t *testing.T) {
	cache, mr := newTestCache(t)
	throttle := NewLoginThrottle(cache.rdb, 10, time.Minute)
	var wg sync.WaitGroup
	for range 25 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := throttle.reserve(context.Background(), "user@example.com"); err != nil && !errors.Is(err, ErrLoginThrottled) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got, err := cache.rdb.SCard(context.Background(), loginFailureKey("user@example.com")).Result(); err != nil || got != 10 {
		t.Fatalf("count=%d err=%v", got, err)
	}
	mr.FastForward(30 * time.Second)
	if _, err := throttle.reserve(context.Background(), "user@example.com"); !errors.Is(err, ErrLoginThrottled) {
		t.Fatalf("over-limit attempt: %v", err)
	}
	mr.FastForward(30 * time.Second)
	if _, err := throttle.reserve(context.Background(), "user@example.com"); err != nil {
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
	if _, err := throttle.reserve(context.Background(), "user@example.com"); !errors.Is(err, ErrLoginThrottleUnavailable) {
		t.Fatal(err)
	}
	if err := throttle.clear(context.Background(), "user@example.com"); !errors.Is(err, ErrLoginThrottleUnavailable) {
		t.Fatal(err)
	}
	if err := throttle.release(context.Background(), "user@example.com", "reservation"); !errors.Is(err, ErrLoginThrottleUnavailable) {
		t.Fatal(err)
	}
}

func TestConcurrentWrongPasswordsRespectLoginLimit(t *testing.T) {
	svc, _, pool := newTestService(t)
	user := verifiedUser(t, svc, pool)
	const limit, attempts = 3, 25
	svc.SetLoginThrottle(NewLoginThrottle(svc.codes.rdb, limit, time.Minute))
	routes := NewHandler(svc, NewRateLimiter(svc.codes.rdb, 100, time.Minute)).Routes(rejectAll)
	start := make(chan struct{})
	statuses := make(chan int, attempts)
	for range attempts {
		go func() {
			<-start
			rec := httptest.NewRecorder()
			routes.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/signin", strings.NewReader(`{"email":"`+user.email+`","password":"wrong"}`)))
			statuses <- rec.Code
		}()
	}
	close(start)
	checked := 0
	for range attempts {
		switch status := <-statuses; status {
		case http.StatusUnauthorized:
			checked++
		case http.StatusTooManyRequests:
		default:
			t.Errorf("unexpected status: %d", status)
		}
	}
	if checked != limit {
		t.Fatalf("%d concurrent guesses reached credential checking; want %d", checked, limit)
	}
}

func TestCancelledSignInReleasesAttempt(t *testing.T) {
	svc, _, pool := newTestService(t)
	user := verifiedUser(t, svc, pool)
	svc.SetLoginThrottle(NewLoginThrottle(svc.codes.rdb, 1, time.Minute))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.SignIn(ctx, user.email, "wrong"); err == nil || errors.Is(err, ErrLoginThrottleUnavailable) {
		t.Fatalf("cancelled sign-in: %v", err)
	}
	if got, err := svc.codes.rdb.SCard(context.Background(), loginFailureKey(user.email)).Result(); err != nil || got != 0 {
		t.Fatalf("cancelled attempt count=%d err=%v", got, err)
	}
	if _, err := svc.SignIn(context.Background(), user.email, user.password); err != nil {
		t.Fatalf("cancelled lookup consumed budget: %v", err)
	}
}

func TestCancelledContextStillClearsLoginAttempts(t *testing.T) {
	cache, _ := newTestCache(t)
	throttle := NewLoginThrottle(cache.rdb, 1, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := throttle.reserve(ctx, "user@example.com"); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := throttle.clear(ctx, "user@example.com"); err != nil {
		t.Fatalf("clear after cancellation: %v", err)
	}
	if _, err := throttle.reserve(context.Background(), "user@example.com"); err != nil {
		t.Fatalf("success counter was not cleared: %v", err)
	}
}

func TestInfrastructureErrorsDoNotConsumeLoginQuota(t *testing.T) {
	svc, _, pool := newTestService(t)
	user := verifiedUser(t, svc, pool)
	svc.SetLoginThrottle(NewLoginThrottle(svc.codes.rdb, 1, time.Minute))
	ctx := context.Background()
	brokenPool, err := pgxpool.NewWithConfig(ctx, pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	brokenPool.Close()
	healthyQueries := svc.queries
	svc.queries = db.New(brokenPool)
	for range 3 {
		if _, err := svc.SignIn(ctx, user.email, user.password); err == nil || errors.Is(err, ErrLoginThrottled) || errors.Is(err, ErrInvalidCredentials) || errors.Is(err, ErrLoginThrottleUnavailable) {
			t.Fatalf("expected database error, got %v", err)
		}
	}
	svc.queries = healthyQueries
	if _, err := svc.SignIn(ctx, user.email, user.password); err != nil {
		t.Fatalf("infrastructure retries locked out recovered account: %v", err)
	}
}

func TestReleaseOnlyRemovesItsOwnLoginReservation(t *testing.T) {
	cache, mr := newTestCache(t)
	throttle := NewLoginThrottle(cache.rdb, 2, time.Minute)
	ctx := context.Background()
	email := "user@example.com"
	if _, err := throttle.reserve(ctx, email); err != nil {
		t.Fatal(err)
	}
	reservation, err := throttle.reserve(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	for range 2 {
		if err := throttle.release(cancelled, email, reservation); err != nil {
			t.Fatal(err)
		}
	}
	if got := cache.rdb.SCard(ctx, loginFailureKey(email)).Val(); got != 1 {
		t.Fatalf("release removed another attempt: count=%d", got)
	}
	mr.FastForward(time.Minute)
	for range 2 {
		if _, err := throttle.reserve(ctx, email); err != nil {
			t.Fatal(err)
		}
	}
	if err := throttle.release(cancelled, email, reservation); err != nil {
		t.Fatal(err)
	}
	if _, err := throttle.reserve(ctx, email); !errors.Is(err, ErrLoginThrottled) {
		t.Fatalf("stale release removed current-window failures: %v", err)
	}
}

func TestCancelledContextStillReservesLoginAttempt(t *testing.T) {
	cache, _ := newTestCache(t)
	throttle := NewLoginThrottle(cache.rdb, 1, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := throttle.reserve(ctx, "user@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := throttle.reserve(context.Background(), "user@example.com"); !errors.Is(err, ErrLoginThrottled) {
		t.Fatalf("cancelled context bypassed reservation: %v", err)
	}
}
