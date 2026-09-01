package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	// Postgres and Redis clients pointed at closed ports: the deletion cannot be
	// recorded, so the answer is the handler's own failure rather than the
	// denylist's, which is what this is about.
	pool, err := pgxpool.New(context.Background(), "postgres://user:pass@127.0.0.1:1/db")
	if err != nil {
		t.Fatalf("build pool: %v", err)
	}
	t.Cleanup(pool.Close)
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: time.Second})
	t.Cleanup(func() { _ = rdb.Close() })
	svc := NewService(db.New(pool), nil, nil, 0, "", NewDenylist(rdb, time.Minute), &stubPublisher{}, time.Hour, time.Minute)
	principal := Principal{UserID: uuid.New(), Email: "deleted@example.com", Role: "user"}
	routes := NewHandler(svc, nil).Routes(authenticateAs(principal), blockAll)

	for _, tc := range []struct {
		method string
		want   int
	}{
		{http.MethodGet, http.StatusUnauthorized},           // denylist applies
		{http.MethodDelete, http.StatusInternalServerError}, // reached the handler
	} {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequest(tc.method, "/me", nil))
		if rec.Code != tc.want {
			t.Fatalf("%s /me = %d, want %d", tc.method, rec.Code, tc.want)
		}
	}
}
