package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/coder/websocket"
)

// ChatEvent mirrors backend/internal/chat's Event envelope.
type ChatEvent struct {
	Type      string    `json:"type"`
	ThreadID  string    `json:"thread_id,omitempty"`
	MessageID string    `json:"message_id,omitempty"`
	ClientID  string    `json:"client_id,omitempty"`
	SenderID  string    `json:"sender_id,omitempty"`
	Body      string    `json:"body,omitempty"`
	Typing    bool      `json:"typing,omitempty"`
	Online    bool      `json:"online,omitempty"`
	Messages  []Message `json:"messages,omitempty"`
	CreatedAt string    `json:"created_at,omitempty"`
}

// Socket is one open chat connection plus the bookkeeping a scenario needs to
// assert delivery correctness.
type Socket struct {
	conn *websocket.Conn
	m    *Metrics
}

// OpenChat upgrades to the thread socket. The caller is responsible for Close.
func (c *Client) OpenChat(ctx context.Context, threadID string) (*Socket, error) {
	u, err := c.WSURL(threadID)
	if err != nil {
		return nil, err
	}
	conn, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{
		HTTPHeader: nil,
	})
	if err != nil {
		c.m.Observe("WS /chat/threads/{id}/ws", 0, 0)
		return nil, fmt.Errorf("dial ws: %w", err)
	}
	return &Socket{conn: conn, m: c.m}, nil
}

// Send transmits one message frame with the sender-matching client_id.
func (s *Socket) Send(ctx context.Context, body, clientID string) error {
	payload, err := json.Marshal(map[string]string{
		"type": "message", "body": body, "client_id": clientID,
	})
	if err != nil {
		return err
	}
	return s.conn.Write(ctx, websocket.MessageText, payload)
}

// Typing transmits a typing indicator frame.
func (s *Socket) Typing(ctx context.Context, on bool) error {
	payload, err := json.Marshal(map[string]any{"type": "typing", "typing": on})
	if err != nil {
		return err
	}
	return s.conn.Write(ctx, websocket.MessageText, payload)
}

// Read blocks for the next inbound event until ctx or the socket ends.
func (s *Socket) Read(ctx context.Context) (ChatEvent, error) {
	_, data, err := s.conn.Read(ctx)
	if err != nil {
		return ChatEvent{}, err
	}
	var ev ChatEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return ChatEvent{}, fmt.Errorf("decode event: %w", err)
	}
	return ev, nil
}

// ExpectEcho reads events until the message carrying clientID arrives, and
// returns how long the send-to-echo round trip took plus any error event that
// carried the same client_id (a rejected send). History and other frames are
// skipped; they are reported separately by the caller if needed.
func (s *Socket) ExpectEcho(ctx context.Context, clientID string, timeout time.Duration) (time.Duration, error) {
	deadline := time.Now().Add(timeout)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	start := time.Now()
	for {
		ev, err := s.Read(ctx)
		if err != nil {
			return 0, err
		}
		if ev.ClientID != clientID {
			continue
		}
		if ev.Type == "error" {
			return 0, fmt.Errorf("send rejected: %s", ev.Body)
		}
		if ev.Type == "message" {
			return time.Since(start), nil
		}
	}
}

// Close ends the socket politely.
func (s *Socket) Close() {
	_ = s.conn.Close(websocket.StatusNormalClosure, "load test done")
}
