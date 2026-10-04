package live

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/danielliu30/dating-coach/backend/internal/auth"
	"github.com/danielliu30/dating-coach/backend/internal/notify"
)

// stubAccounts answers UserActive from fixed values, standing in for Postgres.
type stubAccounts struct {
	active bool
	err    error
}

// UserActive satisfies AccountStatus.
func (s stubAccounts) UserActive(context.Context, uuid.UUID) (bool, error) {
	return s.active, s.err
}

// harness is one notifications endpoint served over a real socket, backed by
// an in-memory Redis and a fixed caller.
type harness struct {
	hub       *notify.Hub
	rdb       *redis.Client
	principal auth.Principal
	url       string
}

// newHarness serves the handler for a caller whose token expires at expiresAt,
// with accounts answering the account-status checks.
func newHarness(t *testing.T, accounts stubAccounts, expiresAt time.Time) *harness {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	h := &harness{
		hub:       notify.NewHub(rdb),
		rdb:       rdb,
		principal: auth.Principal{UserID: uuid.New(), ExpiresAt: expiresAt},
	}
	handler := NewHandler(h.hub, accounts, nil).Routes()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), h.principal)))
	}))
	t.Cleanup(server.Close)
	h.url = "ws" + server.URL[len("http"):] + "/ws"
	return h
}

// dial opens a client socket to the harness, closed when the test ends.
func (h *harness) dial(t *testing.T, ctx context.Context) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, h.url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

// readEvent reads and decodes one frame.
func readEvent(t *testing.T, ctx context.Context, conn *websocket.Conn) notify.Event {
	t.Helper()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var event notify.Event
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return event
}

// readUntilClosed drains frames until the server closes the socket and returns
// the close frame it sent.
func readUntilClosed(t *testing.T, ctx context.Context, conn *websocket.Conn) websocket.CloseError {
	t.Helper()
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			var closeErr websocket.CloseError
			if !errors.As(err, &closeErr) {
				t.Fatalf("socket ended without a close frame: %v", err)
			}
			return closeErr
		}
	}
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestSocketAnnouncesReadyThenPushesPublishedEvents walks the happy path: the
// ready frame comes first, and an event published for the caller afterwards is
// pushed while one for another account is not.
func TestSocketAnnouncesReadyThenPushesPublishedEvents(t *testing.T) {
	h := newHarness(t, stubAccounts{active: true}, time.Now().Add(time.Hour))
	ctx := testContext(t)
	conn := h.dial(t, ctx)

	if got := readEvent(t, ctx, conn); got.Type != notify.EventReady {
		t.Fatalf("first frame = %+v, want %q", got, notify.EventReady)
	}

	if err := notify.Publish(ctx, h.rdb, uuid.New(), notify.Event{Type: notify.EventAnalysisReady, AnalysisID: "someone-else"}); err != nil {
		t.Fatal(err)
	}
	want := notify.Event{Type: notify.EventAnalysisReady, AnalysisID: uuid.NewString(), ConversationID: uuid.NewString()}
	if err := notify.Publish(ctx, h.rdb, h.principal.UserID, want); err != nil {
		t.Fatal(err)
	}
	if got := readEvent(t, ctx, conn); got != want {
		t.Fatalf("pushed %+v, want %+v", got, want)
	}
}

// TestSocketClosesWhenSessionEnds pins the close code and reason for each way a
// session can end, which is what the client keys renewal and sign-out on.
func TestSocketClosesWhenSessionEnds(t *testing.T) {
	tests := []struct {
		name       string
		accounts   stubAccounts
		expiresAt  time.Time
		revoke     bool
		wantCode   websocket.StatusCode
		wantReason string
	}{
		{"deleted account is refused at connect", stubAccounts{}, time.Time{}, false, websocket.StatusPolicyViolation, "session revoked"},
		{"unverifiable account may reconnect", stubAccounts{err: errors.New("dial postgres: connection refused")}, time.Time{}, false, websocket.StatusTryAgainLater, "could not verify session"},
		{"expired token is told to renew", stubAccounts{active: true}, time.Now().Add(-time.Minute), false, websocket.StatusPolicyViolation, "session expired"},
		{"revocation announcement drops the socket", stubAccounts{active: true}, time.Time{}, true, websocket.StatusPolicyViolation, "session revoked"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.accounts, tc.expiresAt)
			ctx := testContext(t)
			conn := h.dial(t, ctx)
			if tc.revoke {
				if got := readEvent(t, ctx, conn); got.Type != notify.EventReady {
					t.Fatalf("first frame = %+v, want %q", got, notify.EventReady)
				}
				h.hub.EndSessions(h.principal.UserID)
			}

			got := readUntilClosed(t, ctx, conn)
			if got.Code != tc.wantCode || got.Reason != tc.wantReason {
				t.Fatalf("closed with %v %q, want %v %q", got.Code, got.Reason, tc.wantCode, tc.wantReason)
			}
		})
	}
}

// TestSocketReleasesItsSubscriptionOnClose checks that a client leaving frees
// its hub registration, so a later revocation has nothing stale to call.
func TestSocketReleasesItsSubscriptionOnClose(t *testing.T) {
	h := newHarness(t, stubAccounts{active: true}, time.Time{})
	ctx := testContext(t)
	conn := h.dial(t, ctx)
	if got := readEvent(t, ctx, conn); got.Type != notify.EventReady {
		t.Fatalf("first frame = %+v, want %q", got, notify.EventReady)
	}
	if err := conn.Close(websocket.StatusNormalClosure, "bye"); err != nil {
		t.Fatalf("close: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		n, err := h.rdb.PubSubNumSub(ctx, notify.UserChannel(h.principal.UserID)).Result()
		if err != nil {
			t.Fatal(err)
		}
		if n[notify.UserChannel(h.principal.UserID)] == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("redis subscription outlived the closed socket")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSessionStatus(t *testing.T) {
	tests := []struct {
		name     string
		accounts stubAccounts
		want     socketVerdict
	}{
		{"active account keeps its socket", stubAccounts{active: true}, socketActive},
		{"deleted account loses its socket", stubAccounts{}, socketRevoked},
		{"unreachable database fails closed but stays retryable", stubAccounts{err: errors.New("boom")}, socketUnverifiable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler(nil, tc.accounts, nil)
			if got := h.sessionStatus(context.Background(), uuid.New()); got != tc.want {
				t.Fatalf("sessionStatus = %d, want %d", got, tc.want)
			}
		})
	}
}
