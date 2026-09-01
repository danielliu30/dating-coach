package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
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

// blockAll is a stand-in for RequireActive against a revoked account: it
// rejects every request that reaches it.
func blockAll(http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
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

// TestRoutesKeepDeletionReachableWhenRevoked guards the one exemption from the
// denylist: deleting an already revoked account must stay possible, because a
// deletion whose queueing failed can only be retried by its own owner.
func TestRoutesKeepDeletionReachableWhenRevoked(t *testing.T) {
	// A Redis client pointed at a closed port: the queued job is what makes the
	// deletion certain, so an unreachable denylist must not fail the request.
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: time.Second})
	t.Cleanup(func() { _ = rdb.Close() })
	svc := NewService(nil, nil, nil, 0, "", NewDenylist(rdb, time.Minute), &stubPublisher{}, time.Hour, time.Minute)
	principal := Principal{UserID: uuid.New(), Email: "deleted@example.com", Role: "user"}
	routes := NewHandler(svc, nil).Routes(authenticateAs(principal), blockAll)

	for _, tc := range []struct {
		method string
		want   int
	}{
		{http.MethodGet, http.StatusUnauthorized}, // denylist applies
		{http.MethodDelete, http.StatusAccepted},  // reached the handler
	} {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequest(tc.method, "/me", nil))
		if rec.Code != tc.want {
			t.Fatalf("%s /me = %d, want %d", tc.method, rec.Code, tc.want)
		}
	}
}
