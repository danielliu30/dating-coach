package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danielliu30/dating-coach/backend/internal/auth"
	"github.com/danielliu30/dating-coach/backend/internal/store/db"
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

func TestSessionStatus(t *testing.T) {
	tests := []struct {
		name     string
		accounts stubAccounts
		want     socketVerdict
	}{
		{"active account keeps its socket", stubAccounts{active: true}, socketActive},
		{"deleted account loses its socket", stubAccounts{}, socketRevoked},
		{"unreachable database fails closed but stays retryable", stubAccounts{err: errors.New("dial postgres: connection refused")}, socketUnverifiable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler(nil, nil, tc.accounts, nil)
			if got := h.sessionStatus(context.Background(), uuid.New()); got != tc.want {
				t.Fatalf("sessionStatus = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestHandleIncomingClosesInactiveSocket drives one client event through a live
// socket whose account no longer checks out, and asserts the event is not
// dispatched (the handler has no service or hub, so acting on it would panic)
// and that the client can tell a revoked session from an unverifiable one.
func TestHandleIncomingClosesInactiveSocket(t *testing.T) {
	tests := []struct {
		name      string
		accounts  stubAccounts
		expiresAt time.Time
		want      websocket.StatusCode
	}{
		{"deleted account is told to stop reconnecting", stubAccounts{}, time.Time{}, websocket.StatusPolicyViolation},
		{"unverifiable account may reconnect", stubAccounts{err: errors.New("dial postgres: connection refused")}, time.Time{}, websocket.StatusTryAgainLater},
		{"expired token stops acting", stubAccounts{active: true}, time.Now().Add(-time.Minute), websocket.StatusPolicyViolation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertSocketClosed(t, tc.accounts, tc.expiresAt, tc.want)
		})
	}
}

// TestExpiryTimer checks that a socket only carries a deadline when its token
// has one, and that an already expired token does not wait for it.
func TestExpiryTimer(t *testing.T) {
	past := expiryTimer(time.Now().Add(-time.Minute))
	defer past.Stop()
	select {
	case <-past.C:
	case <-time.After(time.Second):
		t.Fatal("an expired token kept its socket")
	}

	never := expiryTimer(time.Time{})
	defer never.Stop()
	select {
	case <-never.C:
		t.Fatal("a token without an expiry lost its socket")
	case <-time.After(100 * time.Millisecond):
	}
}

// assertSocketClosed serves one socket with a handler backed by accounts and a
// caller whose token expires at expiresAt, feeds it a message event and fails
// the test unless the socket was closed with want and the event went
// undispatched.
func assertSocketClosed(t *testing.T, accounts stubAccounts, expiresAt time.Time, want websocket.StatusCode) {
	t.Helper()
	h := NewHandler(nil, nil, accounts, nil)

	dispatched := make(chan bool, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.CloseNow()
		event := Event{Type: EventMessage, Body: "still here"}
		principal := auth.Principal{UserID: uuid.New(), ExpiresAt: expiresAt}
		dispatched <- h.handleIncoming(r.Context(), conn, uuid.New(), principal, uuid.New(), event)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws"+server.URL[len("http"):], nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != want {
		t.Fatalf("close status = %v, want %v", websocket.CloseStatus(err), want)
	}
	if <-dispatched {
		t.Fatal("handleIncoming kept the socket open for an inactive account")
	}
}

// TestSessionStatusReadsTheAccountRow runs the check against Postgres, which is
// the point of it: deletion marks the row before anything is announced, so a
// socket's verdict follows the mark with no cache in between.
func TestSessionStatusReadsTheAccountRow(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	var userID uuid.UUID
	if err := pool.QueryRow(
		ctx,
		`INSERT INTO users (email, password_hash, display_name) VALUES ($1, 'hash', 'Live') RETURNING id`,
		uuid.NewString()+"@example.test",
	).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	})

	h := NewHandler(nil, nil, db.New(pool), nil)
	if got := h.sessionStatus(ctx, userID); got != socketActive {
		t.Fatalf("sessionStatus of a live account = %d, want %d", got, socketActive)
	}

	if _, err := pool.Exec(ctx, "UPDATE users SET deleted_at = now() WHERE id = $1", userID); err != nil {
		t.Fatalf("mark user deleted: %v", err)
	}
	if got := h.sessionStatus(ctx, userID); got != socketRevoked {
		t.Fatalf("sessionStatus of a deleted account = %d, want %d", got, socketRevoked)
	}
}

// TestHandleIncomingRejectionEchoesClientID sends an empty message, which the
// service refuses before touching storage, and asserts the error frame names
// the client's own id for that send.
func TestHandleIncomingRejectionEchoesClientID(t *testing.T) {
	h := NewHandler(NewService(nil, nil), nil, stubAccounts{active: true}, nil)
	threadID := uuid.New()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.CloseNow()
		event := Event{Type: EventMessage, ClientID: "c-42", Body: "   "}
		principal := auth.Principal{UserID: uuid.New()}
		h.handleIncoming(r.Context(), conn, threadID, principal, uuid.New(), event)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+server.URL[len("http"):], nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got Event
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	if got.Type != EventError || got.ClientID != "c-42" || got.ThreadID != threadID.String() {
		t.Fatalf("error frame = %+v, want type %q with client_id c-42 for thread %s", got, EventError, threadID)
	}
}
