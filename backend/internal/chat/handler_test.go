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

func TestSessionActive(t *testing.T) {
	tests := []struct {
		name        string
		revocations stubRevocations
		want        bool
	}{
		{"active account keeps its socket", stubRevocations{}, true},
		{"revoked account loses its socket", stubRevocations{revoked: true}, false},
		{"unreachable redis fails closed", stubRevocations{err: errors.New("dial redis: connection refused")}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler(nil, nil, tc.revocations, nil)
			if got := h.sessionActive(context.Background(), uuid.New()); got != tc.want {
				t.Fatalf("sessionActive = %t, want %t", got, tc.want)
			}
		})
	}
}

// TestHandleIncomingRevoked drives one client event through a live socket whose
// account was revoked after the upgrade, and asserts the event is not
// dispatched: the handler has no service or hub, so acting on it would panic.
func TestHandleIncomingRevoked(t *testing.T) {
	h := NewHandler(nil, nil, stubRevocations{revoked: true}, nil)

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

	if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("close status = %v, want %v", websocket.CloseStatus(err), websocket.StatusPolicyViolation)
	}
	if <-dispatched {
		t.Fatal("handleIncoming kept the socket open for a revoked account")
	}
}
