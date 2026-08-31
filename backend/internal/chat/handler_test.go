package chat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/danielliu30/dating-coach/backend/internal/auth"
)

// stubRevocations answers Revoked from fixed values, standing in for Redis.
type stubRevocations struct {
	revoked bool
	err     error
}

// Revoked satisfies auth.Revocations.
func (s stubRevocations) Revoked(context.Context, uuid.UUID) (bool, error) {
	return s.revoked, s.err
}

func TestSessionStatus(t *testing.T) {
	tests := []struct {
		name        string
		revocations stubRevocations
		want        socketVerdict
	}{
		{"active account keeps its socket", stubRevocations{}, socketActive},
		{"revoked account loses its socket", stubRevocations{revoked: true}, socketRevoked},
		{"unreachable redis fails closed but stays retryable", stubRevocations{err: errors.New("dial redis: connection refused")}, socketUnverifiable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler(nil, nil, tc.revocations, nil)
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
		name        string
		revocations stubRevocations
		want        websocket.StatusCode
	}{
		{"revoked account is told to stop reconnecting", stubRevocations{revoked: true}, websocket.StatusPolicyViolation},
		{"unverifiable account may reconnect", stubRevocations{err: errors.New("dial redis: connection refused")}, websocket.StatusTryAgainLater},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertSocketClosed(t, tc.revocations, tc.want)
		})
	}
}

// assertSocketClosed serves one socket with a handler backed by revocations,
// feeds it a message event and fails the test unless the socket was closed with
// want and the event went undispatched.
func assertSocketClosed(t *testing.T, revocations stubRevocations, want websocket.StatusCode) {
	t.Helper()
	h := NewHandler(nil, nil, revocations, nil)

	dispatched := make(chan bool, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.CloseNow()
		event := Event{Type: EventMessage, Body: "still here"}
		principal := auth.Principal{UserID: uuid.New()}
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
