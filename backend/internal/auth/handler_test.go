package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/danielliu30/dating-coach/backend/internal/account"
)

// stubPublisher records queued deletions without a broker.
type stubPublisher struct{ jobs []account.Job }

// Publish appends the job, always succeeding.
func (p *stubPublisher) Publish(_ context.Context, job account.Job) error {
	p.jobs = append(p.jobs, job)
	return nil
}

// authenticateAs is a stand-in for Middleware that puts principal on the
// request without validating a token.
func authenticateAs(principal Principal) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	}
}

// openLimiter returns a rate limiter whose Redis is unreachable, which the
// limiter treats as fail-open, so routes stay reachable without a broker.
func openLimiter(t *testing.T) *RateLimiter {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: time.Second})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewRateLimiter(rdb, 100, time.Minute)
}

// TestRefreshRouteSkipsAuthentication pins the point of the endpoint: a client
// whose access token has already expired must still reach /refresh, so it must
// not sit behind the authentication middleware.
func TestRefreshRouteSkipsAuthentication(t *testing.T) {
	svc := NewService(nil, nil, nil, 0, "", NewRefreshTokens(&stubRefreshStore{}, time.Hour), &stubPublisher{}, time.Hour, time.Minute)
	routes := NewHandler(svc, openLimiter(t)).Routes(rejectAll)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/refresh", strings.NewReader(`{"refresh_token":"nope"}`))
	routes.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("POST /refresh = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if body := rec.Body.String(); !strings.Contains(body, "invalid refresh token") {
		t.Fatalf("body = %q, want the handler's own rejection, not the middleware's", body)
	}
}

// rejectAll stands in for the authentication middleware, refusing every request
// that reaches it so routes mounted behind it are distinguishable.
func rejectAll(http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
}

// TestDeleteMeStaysReachableAfterRevocation guards the retry path: the caller's
// access token outlives the revocation of its refresh tokens, so a deletion
// whose queueing failed can still be repeated by its owner.
func TestDeleteMeStaysReachableAfterRevocation(t *testing.T) {
	store := &stubRefreshStore{}
	publisher := &stubPublisher{}
	svc := NewService(nil, nil, nil, 0, "", NewRefreshTokens(store, time.Hour), publisher, time.Hour, time.Minute)
	principal := Principal{UserID: uuid.New(), Email: "deleted@example.com", Role: "user"}
	routes := NewHandler(svc, openLimiter(t)).Routes(authenticateAs(principal))

	for i := range 2 {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/me", nil))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("attempt %d: DELETE /me = %d, want %d", i, rec.Code, http.StatusAccepted)
		}
	}
	if len(store.revokedUsers) != 2 || store.revokedUsers[0] != principal.UserID {
		t.Fatalf("revoked users = %v, want the caller revoked on both attempts", store.revokedUsers)
	}
	if len(publisher.jobs) != 2 {
		t.Fatalf("queued jobs = %d, want 2", len(publisher.jobs))
	}
}
