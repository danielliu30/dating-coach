package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// rejectAll is a stand-in for the authentication middleware against a caller
// whose access token is gone: it rejects every request that reaches it.
func rejectAll(http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
}

// TestRoutesLeaveRefreshUnauthenticated pins the point of the refresh endpoint:
// it is reached by a client whose access token has already expired, so it must
// sit outside the authentication middleware rather than behind it.
func TestRoutesLeaveRefreshUnauthenticated(t *testing.T) {
	// A Redis client pointed at a closed port: the rate limiter fails open, so
	// the public routes stay reachable without a live Redis.
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: time.Second})
	t.Cleanup(func() { _ = rdb.Close() })
	svc := NewService(nil, nil, nil, 0, "", &stubPublisher{}, 15*time.Minute, time.Minute, time.Hour)
	routes := NewHandler(svc, NewRateLimiter(rdb, 100, time.Minute)).Routes(rejectAll)

	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		// A body the handler rejects itself, so only the handler can answer 400.
		{"refresh reaches the handler", http.MethodPost, "/refresh", "{", http.StatusBadRequest},
		{"me stays authenticated", http.MethodGet, "/me", "", http.StatusUnauthorized},
		{"delete me stays authenticated", http.MethodDelete, "/me", "", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			routes.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
			}
		})
	}
}
