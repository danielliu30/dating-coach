package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/danielliu30/dating-coach/backend/internal/account"
	"github.com/danielliu30/dating-coach/backend/internal/store/db"
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
	svc := NewService(nil, nil, nil, 0, "", NewRefreshTokens(&stubRefreshStore{}, time.Hour), &stubPublisher{}, nil, time.Hour, time.Minute)
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
// whose first attempt failed can still be repeated by its owner. The database
// is unreachable here, so the deletion fails on its first step: a 500 means the
// request reached the handler rather than being refused for a revoked session.
func TestDeleteMeStaysReachableAfterRevocation(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://user:pass@127.0.0.1:1/db")
	if err != nil {
		t.Fatalf("build pool: %v", err)
	}
	t.Cleanup(pool.Close)
	svc := NewService(db.New(pool), nil, nil, 0, "", NewRefreshTokens(&stubRefreshStore{}, time.Hour), &stubPublisher{}, nil, time.Hour, time.Minute)
	principal := Principal{UserID: uuid.New(), Email: "deleted@example.com", Role: "user"}
	routes := NewHandler(svc, openLimiter(t)).Routes(authenticateAs(principal))

	for i := range 2 {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/me", nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("attempt %d: DELETE /me = %d, want %d", i, rec.Code, http.StatusInternalServerError)
		}
	}
}
