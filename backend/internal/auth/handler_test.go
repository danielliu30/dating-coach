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

	"github.com/danielliu30/dating-coach/backend/internal/account"
)

func TestSignUpRejectsInvalidPasswords(t *testing.T) {
	routes := NewHandler(&Service{}, failOpenLimiter(t)).Routes(rejectAll)
	for _, tc := range []struct {
		name     string
		password string
		message  string
	}{
		{"empty", "", "password must be at least 8 characters"},
		{"too short", "short", "password must be at least 8 characters"},
		{"multibyte too short", strings.Repeat("é", 7), "password must be at least 8 characters"},
		{"over byte limit", strings.Repeat("a", 73), "password must be at most 72 bytes"},
		{"multibyte over byte limit", strings.Repeat("é", 30) + strings.Repeat("a", 13), "password must be at most 72 bytes"},
		{"whitespace", strings.Repeat(" ", 8), "password cannot be only whitespace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(SignUpInput{Email: "password-test@example.com", Password: tc.password, DisplayName: "Password Test"})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			routes.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/signup", strings.NewReader(string(body))))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.message) {
				t.Fatalf("sign up = %d %s, want 400 containing %q", rec.Code, rec.Body.String(), tc.message)
			}
		})
	}
}

func TestSignUpAcceptsValidPasswords(t *testing.T) {
	svc, _, pool := newTestService(t)
	routes := NewHandler(svc, failOpenLimiter(t)).Routes(rejectAll)
	for _, tc := range []struct {
		name     string
		password string
	}{
		{"valid", "correct-horse"},
		{"minimum length", strings.Repeat("a", 8)},
		{"maximum bytes", strings.Repeat("a", 72)},
		{"multibyte maximum bytes", strings.Repeat("é", 36)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			email := "password-test-" + uuid.NewString() + "@example.com"
			t.Cleanup(func() {
				if _, err := pool.Exec(context.Background(), "DELETE FROM users WHERE email = $1", email); err != nil {
					t.Errorf("delete test user: %v", err)
				}
			})
			body, err := json.Marshal(SignUpInput{Email: email, Password: tc.password, DisplayName: "Password Test"})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			routes.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/signup", strings.NewReader(string(body))))
			if rec.Code != http.StatusCreated {
				t.Fatalf("sign up = %d %s, want 201", rec.Code, rec.Body.String())
			}
			var session Session
			if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil {
				t.Fatal(err)
			}
			if session.Token == "" || session.User.Email != email {
				t.Fatal("sign up did not return the new account session")
			}
		})
	}
}

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
	svc := NewService(nil, nil, nil, nil, 0, "", nil, nil, &stubPublisher{}, 15*time.Minute, time.Minute, time.Hour)
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
		{"signout stays authenticated", http.MethodPost, "/signout", "", http.StatusUnauthorized},
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
