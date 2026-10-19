package notify

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

// Event types carried on a user's notification channel.
const (
	// EventReady is written by the API to a socket once its Redis subscription
	// is live, so a client knows that anything finishing from then on is pushed
	// and only what finished before needs a refetch.
	EventReady          = "ready"
	EventAnalysisReady  = "analysis_ready"
	EventAnalysisFailed = "analysis_failed"
)

const userChannelFmt = "notify:user:%s"

// Event is the envelope published on a user's channel and written to their
// sockets.
type Event struct {
	Type           string `json:"type"`
	AnalysisID     string `json:"analysis_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
}

// subscriber receives events for a single socket.
type subscriber struct {
	events chan Event
}

// userFeed is one user's Redis subscription on this replica and the local
// sockets it fans out to. ready is closed once Redis has confirmed the
// subscription.
type userFeed struct {
	subs   map[*subscriber]struct{}
	cancel context.CancelFunc
	ready  chan struct{}
}

// Hub tracks this replica's notification sockets per user and bridges them to
// Redis, so an event published by any process (the worker, another replica)
// reaches every socket the user holds.
type Hub struct {
	rdb *redis.Client

	mu      sync.Mutex
	users   map[uuid.UUID]*userFeed
	sockets map[uuid.UUID]map[uuid.UUID]func()
}

// NewHub returns a hub bridging local notification sockets to Redis.
func NewHub(rdb *redis.Client) *Hub {
	return &Hub{
		rdb:     rdb,
		users:   map[uuid.UUID]*userFeed{},
		sockets: map[uuid.UUID]map[uuid.UUID]func(){},
	}
}

// Register indexes one of this replica's sockets by the account holding it and
// returns the function that removes it again, which the socket must call as it
// closes. drop is invoked at most once, from EndSessions, on the caller's
// goroutine: it must not block, so it should signal the socket's own loop
// rather than touch the connection.
func (h *Hub) Register(userID, connID uuid.UUID, drop func()) func() {
	h.mu.Lock()
	defer h.mu.Unlock()

	conns, ok := h.sockets[userID]
	if !ok {
		conns = map[uuid.UUID]func(){}
		h.sockets[userID] = conns
	}
	conns[connID] = drop

	return func() { h.deregister(userID, connID) }
}

// deregister forgets one socket, so a later revocation cannot call a drop
// belonging to a connection that has already gone away.
func (h *Hub) deregister(userID, connID uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()

	conns, ok := h.sockets[userID]
	if !ok {
		return
	}
	delete(conns, connID)
	if len(conns) == 0 {
		delete(h.sockets, userID)
	}
}

// EndSessions drops every notification socket this replica holds for userID;
// it is wired to revocation announcements. Each socket is forgotten before its
// drop runs, so the callback fires once even if two revocations race.
func (h *Hub) EndSessions(userID uuid.UUID) {
	h.mu.Lock()
	conns := h.sockets[userID]
	delete(h.sockets, userID)
	h.mu.Unlock()

	for _, drop := range conns {
		drop()
	}
}

// Subscribe registers a socket for userID's notifications. The first socket a
// user opens on this replica starts the Redis subscription; it returns the
// socket's event channel, a channel closed once that subscription is live, and
// the unsubscribe function the socket must call as it closes.
func (h *Hub) Subscribe(userID uuid.UUID) (<-chan Event, <-chan struct{}, func()) {
	sub := &subscriber{events: make(chan Event, 16)}

	h.mu.Lock()
	feed, exists := h.users[userID]
	if !exists {
		ctx, cancel := context.WithCancel(context.Background())
		feed = &userFeed{subs: map[*subscriber]struct{}{}, cancel: cancel, ready: make(chan struct{})}
		h.users[userID] = feed
		go h.pump(ctx, userID, feed.ready)
	}
	feed.subs[sub] = struct{}{}
	h.mu.Unlock()

	return sub.events, feed.ready, func() { h.unsubscribe(userID, sub) }
}

// unsubscribe removes a socket and, once a user has no local sockets left,
// stops their Redis subscription.
func (h *Hub) unsubscribe(userID uuid.UUID, sub *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()

	feed, ok := h.users[userID]
	if !ok {
		return
	}
	if _, ok := feed.subs[sub]; !ok {
		return
	}
	delete(feed.subs, sub)
	close(sub.events)
	if len(feed.subs) == 0 {
		delete(h.users, userID)
		feed.cancel()
	}
}

// pump forwards Redis messages on userID's channel to the local sockets until
// ctx is cancelled, closing ready once Redis confirms the subscription. A
// subscription that cannot be confirmed is retried every second; once live,
// go-redis resubscribes by itself after a dropped connection.
func (h *Hub) pump(ctx context.Context, userID uuid.UUID, ready chan struct{}) {
	pubsub := h.rdb.Subscribe(ctx, UserChannel(userID))
	defer func() {
		if err := pubsub.Close(); err != nil {
			slog.Debug("close notification subscription", "error", err, "user_id", userID)
		}
	}()

	for {
		_, err := pubsub.Receive(ctx)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return
		}
		slog.Error("confirm notification subscription", "error", err, "user_id", userID)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
	close(ready)

	// A blocking ReceiveMessage would not notice ctx being cancelled, so the
	// last socket leaving could not end the subscription; Channel can be
	// abandoned at any time.
	messages := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-messages:
			if !ok {
				return
			}
			var event Event
			if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
				slog.Error("decode notification", "error", err, "user_id", userID)
				continue
			}
			h.broadcastLocal(userID, event)
		}
	}
}

// broadcastLocal delivers an event to this replica's sockets for userID,
// dropping it for sockets whose buffer is full rather than blocking the rest:
// clients refetch on reconnect, so a dropped push is only a delay.
func (h *Hub) broadcastLocal(userID uuid.UUID, event Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	feed, ok := h.users[userID]
	if !ok {
		return
	}
	for sub := range feed.subs {
		select {
		case sub.events <- event:
		default:
			slog.Warn("dropping notification for slow client", "user_id", userID, "type", event.Type)
		}
	}
}

// UserChannel is the Redis pub/sub channel carrying one user's notifications.
func UserChannel(userID uuid.UUID) string {
	return fmt.Sprintf(userChannelFmt, userID)
}
