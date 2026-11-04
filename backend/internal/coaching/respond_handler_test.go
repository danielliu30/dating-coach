package coaching

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/danielliu30/dating-coach/backend/internal/auth"
)

// newTestLimiter returns a limiter of limit requests per minute on an
// in-memory Redis.
func newTestLimiter(t *testing.T, limit int) *auth.RateLimiter {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return auth.NewRateLimiter(rdb, limit, time.Minute)
}

// postRespond sends body to POST /respond from ip and returns the recorder.
func postRespond(t *testing.T, routes http.Handler, ip, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/respond", strings.NewReader(body))
	req.RemoteAddr = ip + ":40000"
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, req)
	return rec
}

func respondBody(sessionID, token, action string) string {
	return `{"session_id":"` + sessionID + `","token":"` + token + `","action":"` + action + `"}`
}

func TestRespondWithTokenRateLimitsPerIP(t *testing.T) {
	routes := NewHandler(nil, newTestLimiter(t, 2)).PublicRoutes()

	// A malformed body is answered before the service, so only the limiter
	// can turn it into a 429.
	for i, want := range []int{http.StatusBadRequest, http.StatusBadRequest, http.StatusTooManyRequests} {
		if rec := postRespond(t, routes, "192.0.2.1", "{"); rec.Code != want {
			t.Fatalf("request %d = %d, want %d", i+1, rec.Code, want)
		}
	}
	if rec := postRespond(t, routes, "192.0.2.2", "{"); rec.Code != http.StatusBadRequest {
		t.Fatalf("other IP = %d, want 400 (own budget)", rec.Code)
	}
}

func TestRespondWithTokenRateLimitsPerSession(t *testing.T) {
	limiter := newTestLimiter(t, 1)
	routes := NewHandler(nil, limiter).PublicRoutes()
	sid := uuid.NewString()
	// Spend the session's budget as if from another address.
	if ok, err := limiter.Allow(context.Background(), "booking-respond:session:"+sid); err != nil || !ok {
		t.Fatalf("prime session budget: ok=%v err=%v", ok, err)
	}
	if rec := postRespond(t, routes, "192.0.2.3", respondBody(sid, "guess", "confirm")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over session budget = %d, want 429", rec.Code)
	}
}

func TestRespondWithTokenFailsOpenWithoutRedis(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: time.Second})
	t.Cleanup(func() { _ = rdb.Close() })
	routes := NewHandler(nil, auth.NewRateLimiter(rdb, 1, time.Minute)).PublicRoutes()
	if rec := postRespond(t, routes, "192.0.2.4", "{"); rec.Code != http.StatusBadRequest {
		t.Fatalf("redis down = %d, want 400 from the handler", rec.Code)
	}
}

func TestRespondWithTokenValidatesBeforeLookup(t *testing.T) {
	routes := NewHandler(nil, nil).PublicRoutes()
	sid := uuid.NewString()
	for _, body := range []string{
		respondBody("not-a-uuid", "tok", "confirm"),
		respondBody(sid, "", "confirm"),
		respondBody(sid, "tok", "maybe"),
	} {
		if rec := postRespond(t, routes, "192.0.2.5", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400", body, rec.Code)
		}
	}
}

// coachToken recovers the raw confirmation token from the request
// email sent to the coach; only its hash is stored.
func coachToken(t *testing.T, pool *pgxpool.Pool, coach uuid.UUID) string {
	t.Helper()
	var body string
	if err := pool.QueryRow(context.Background(),
		"SELECT body FROM email_outbox WHERE to_email = $1 AND body LIKE '%token=%'",
		emailOf(t, pool, coach)).Scan(&body); err != nil {
		t.Fatal(err)
	}
	i := strings.Index(body, "token=")
	return body[i+len("token="):][:64]
}

func TestRespondWithTokenHidesSessionState(t *testing.T) {
	svc, pool := testService(t)
	ctx := context.Background()
	coach, client := insertCoach(t, pool), insertUser(t, pool, "user")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM email_outbox WHERE to_email IN ($1, $2)", emailOf(t, pool, coach), emailOf(t, pool, client))
	})
	s, err := svc.BookSession(ctx, client, BookInput{CoachID: coach.String(), ScheduledTime: nextSlot()})
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	token := coachToken(t, pool, coach)
	routes := NewHandler(svc, nil).PublicRoutes()
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, s.ID); err != nil {
			t.Fatal(err)
		}
	}

	wrong := postRespond(t, routes, "192.0.2.6", respondBody(s.ID, strings.Repeat("0", 64), "confirm"))

	exec("UPDATE coaching_sessions SET respond_by = now() - interval '1 minute' WHERE id = $1")
	expired := postRespond(t, routes, "192.0.2.6", respondBody(s.ID, token, "confirm"))
	exec("UPDATE coaching_sessions SET respond_by = now() + interval '1 hour' WHERE id = $1")

	// A request answered while its token was still stored (e.g. a race).
	exec("UPDATE coaching_sessions SET status = 'scheduled' WHERE id = $1")
	answered := postRespond(t, routes, "192.0.2.6", respondBody(s.ID, token, "confirm"))
	exec("UPDATE coaching_sessions SET status = 'pending' WHERE id = $1")

	if ok := postRespond(t, routes, "192.0.2.6", respondBody(s.ID, token, "decline")); ok.Code != http.StatusOK {
		t.Fatalf("decline = %d %s, want 200", ok.Code, ok.Body)
	}
	reused := postRespond(t, routes, "192.0.2.6", respondBody(s.ID, token, "confirm"))

	for name, rec := range map[string]*httptest.ResponseRecorder{"expired": expired, "answered": answered, "reused": reused} {
		if rec.Code != wrong.Code || rec.Body.String() != wrong.Body.String() {
			t.Errorf("%s = %d %s, want the wrong-token answer %d %s", name, rec.Code, rec.Body, wrong.Code, wrong.Body)
		}
	}
	if wrong.Code != http.StatusNotFound || !strings.Contains(wrong.Body.String(), errLinkInactive) {
		t.Fatalf("wrong token = %d %s, want 404 %q", wrong.Code, wrong.Body, errLinkInactive)
	}
}

func TestCancelClearsConfirmationToken(t *testing.T) {
	svc, pool := testService(t)
	ctx := context.Background()
	coach, client := insertCoach(t, pool), insertUser(t, pool, "user")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM email_outbox WHERE to_email IN ($1, $2)", emailOf(t, pool, coach), emailOf(t, pool, client))
	})
	s, err := svc.BookSession(ctx, client, BookInput{CoachID: coach.String(), ScheduledTime: nextSlot()})
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	if _, err := svc.SetStatus(ctx, uuid.MustParse(s.ID), client, StatusCancelled); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if row := rowOf(t, svc, s.ID); row.ConfirmationToken != nil {
		t.Fatalf("cancelled session keeps token hash %q", *row.ConfirmationToken)
	}
}
