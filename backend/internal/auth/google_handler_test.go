package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// failOpenLimiter is a rate limiter whose Redis is unreachable, so it lets
// every request through; it keeps handler tests free of a live Redis.
func failOpenLimiter(t *testing.T) *RateLimiter {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: time.Second})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewRateLimiter(rdb, 100, time.Minute)
}

// TestGoogleRouteStatusCodes pins the public contract of POST /google for the
// refusals that never touch the store: not configured, malformed body, a
// token the verifier rejects, and a role a caller may not pick.
func TestGoogleRouteStatusCodes(t *testing.T) {
	svc := NewService(nil, nil, nil, nil, 0, "", nil, nil, &stubPublisher{}, 15*time.Minute, time.Minute, time.Hour)
	routes := NewHandler(svc, failOpenLimiter(t)).Routes(rejectAll)
	post := func(body string) int {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/google", strings.NewReader(body)))
		return rec.Code
	}

	if got := post(`{"id_token":"anything"}`); got != http.StatusNotImplemented {
		t.Fatalf("without GOOGLE_CLIENT_ID = %d, want 501", got)
	}
	svc.SetGoogleVerifier(stubGoogle{"good": {Email: "x@example.com"}})
	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"malformed":  {"{", http.StatusBadRequest},
		"no token":   {`{"role":"user"}`, http.StatusBadRequest},
		"forged":     {`{"id_token":"forged"}`, http.StatusUnauthorized},
		"admin role": {`{"id_token":"good","role":"admin"}`, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			if got := post(tc.body); got != tc.want {
				t.Fatalf("POST /google %s = %d, want %d", tc.body, got, tc.want)
			}
		})
	}
}

// TestGoogleRouteOpensSession covers the happy path through the HTTP layer
// against the real store: a valid token answers 200 with a session whose
// profile is already verified and carries the requested role.
func TestGoogleRouteOpensSession(t *testing.T) {
	svc, issuer, pool := newTestService(t)
	email := "google-route-test-" + uuid.NewString() + "@example.com"
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DELETE FROM users WHERE email = $1", email); err != nil {
			t.Errorf("delete test user: %v", err)
		}
	})
	svc.SetGoogleVerifier(stubGoogle{"good": {Subject: "sub-9", Email: email, Name: "Route Test"}})
	routes := NewHandler(svc, failOpenLimiter(t)).Routes(rejectAll)

	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/google", strings.NewReader(`{"id_token":"good","role":"coach"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /google = %d: %s", rec.Code, rec.Body)
	}
	var session Session
	if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	assertTokenScope(t, issuer, session.Token, ScopeSession, 15*time.Minute)
	if !session.User.EmailVerified || session.User.Role != RoleCoach || session.User.Email != email {
		t.Fatalf("profile = %+v, want verified coach %q", session.User, email)
	}
}
