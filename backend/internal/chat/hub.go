// Package chat implements real-time user <-> coach messaging over WebSockets.
//
// Fan-out goes through Redis pub/sub so any API replica can deliver a message to
// a socket held by another replica; transcripts are persisted to PostgreSQL.
package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	presenceTTL      = 45 * time.Second
	EventMessage     = "message"
	EventTyping      = "typing"
	EventPresence    = "presence"
	EventHistory     = "history"
	EventError       = "error"
	typingChannelFmt = "chat:thread:%s"
)

// Event is the envelope exchanged over the socket and over Redis pub/sub.
type Event struct {
	Type      string    `json:"type"`
	ThreadID  string    `json:"thread_id,omitempty"`
	MessageID string    `json:"message_id,omitempty"`
	SenderID  string    `json:"sender_id,omitempty"`
	Body      string    `json:"body,omitempty"`
	Typing    bool      `json:"typing,omitempty"`
	Online    bool      `json:"online,omitempty"`
	Messages  []Message `json:"messages,omitempty"`
	CreatedAt string    `json:"created_at,omitempty"`
}

// Message is a persisted chat message as returned to clients.
type Message struct {
	ID        string `json:"id"`
	ThreadID  string `json:"thread_id"`
	SenderID  string `json:"sender_id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

// subscriber receives events for a single socket.
type subscriber struct {
	events chan Event
}

// Hub tracks local sockets per thread and bridges them to Redis.
type Hub struct {
	rdb *redis.Client

	mu      sync.Mutex
	threads map[uuid.UUID]map[*subscriber]struct{}
	cancels map[uuid.UUID]context.CancelFunc
}

// NewHub returns a hub bridging local sockets to Redis; one is shared by the
// chat Handler (sockets) and Service (publishing).
func NewHub(rdb *redis.Client) *Hub {
	return &Hub{
		rdb:     rdb,
		threads: map[uuid.UUID]map[*subscriber]struct{}{},
		cancels: map[uuid.UUID]context.CancelFunc{},
	}
}

// Subscribe registers a socket for a thread and returns its event channel plus
// an unsubscribe function.
func (h *Hub) Subscribe(threadID uuid.UUID) (<-chan Event, func()) {
	sub := &subscriber{events: make(chan Event, 32)}

	h.mu.Lock()
	subs, exists := h.threads[threadID]
	if !exists {
		subs = map[*subscriber]struct{}{}
		h.threads[threadID] = subs

		ctx, cancel := context.WithCancel(context.Background())
		h.cancels[threadID] = cancel
		go h.pump(ctx, threadID)
	}
	subs[sub] = struct{}{}
	h.mu.Unlock()

	return sub.events, func() { h.unsubscribe(threadID, sub) }
}

// unsubscribe removes a socket and, once a thread has no local sockets left,
// stops its Redis subscription.
func (h *Hub) unsubscribe(threadID uuid.UUID, sub *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()

	subs, ok := h.threads[threadID]
	if !ok {
		return
	}
	delete(subs, sub)
	close(sub.events)
	if len(subs) == 0 {
		delete(h.threads, threadID)
		if cancel, ok := h.cancels[threadID]; ok {
			cancel()
			delete(h.cancels, threadID)
		}
	}
}

// pump forwards Redis messages for a thread to the local sockets.
func (h *Hub) pump(ctx context.Context, threadID uuid.UUID) {
	pubsub := h.rdb.Subscribe(ctx, channelFor(threadID))
	defer func() {
		if err := pubsub.Close(); err != nil && ctx.Err() == nil {
			slog.Error("close chat subscription", "error", err, "thread_id", threadID)
		}
	}()

	for {
		msg, err := pubsub.ReceiveMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Error("receive chat message", "error", err, "thread_id", threadID)
			time.Sleep(time.Second)
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
			slog.Error("decode chat event", "error", err)
			continue
		}
		h.broadcastLocal(threadID, event)
	}
}

// broadcastLocal delivers an event to this replica's sockets, dropping it for
// clients whose buffer is full rather than blocking the whole thread.
func (h *Hub) broadcastLocal(threadID uuid.UUID, event Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.threads[threadID] {
		select {
		case sub.events <- event:
		default:
			slog.Warn("dropping chat event for slow client", "thread_id", threadID)
		}
	}
}

// Publish broadcasts an event to every replica serving the thread.
func (h *Hub) Publish(ctx context.Context, threadID uuid.UUID, event Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode chat event: %w", err)
	}
	if err := h.rdb.Publish(ctx, channelFor(threadID), payload).Err(); err != nil {
		return fmt.Errorf("publish chat event: %w", err)
	}
	return nil
}

// MarkOnline records one live connection for a user; call it on connect and on
// ping. Presence is a set of connection IDs so a user with several devices stays
// online until the last of them goes away, and the TTL still reaps the key if a
// replica dies without cleaning up.
func (h *Hub) MarkOnline(ctx context.Context, userID, connID uuid.UUID) error {
	pipe := h.rdb.TxPipeline()
	pipe.SAdd(ctx, presenceKey(userID), connID.String())
	pipe.Expire(ctx, presenceKey(userID), presenceTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("set presence: %w", err)
	}
	return nil
}

// MarkOffline drops one connection from a user's presence set; call it when a
// socket closes.
func (h *Hub) MarkOffline(ctx context.Context, userID, connID uuid.UUID) error {
	if err := h.rdb.SRem(ctx, presenceKey(userID), connID.String()).Err(); err != nil {
		return fmt.Errorf("clear presence: %w", err)
	}
	return nil
}

// IsOnline reports whether a user holds any live connection, which the thread
// lists surface as counterpart presence.
func (h *Hub) IsOnline(ctx context.Context, userID uuid.UUID) (bool, error) {
	n, err := h.rdb.SCard(ctx, presenceKey(userID)).Result()
	if err != nil {
		return false, fmt.Errorf("check presence: %w", err)
	}
	return n > 0, nil
}

// channelFor is the Redis pub/sub channel carrying one thread's events.
func channelFor(threadID uuid.UUID) string {
	return fmt.Sprintf(typingChannelFmt, threadID)
}

// presenceKey is the Redis set holding a user's live connection IDs.
func presenceKey(userID uuid.UUID) string {
	return fmt.Sprintf("presence:user:%s", userID)
}
