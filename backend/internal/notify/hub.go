package notify

import (
	"sync"

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

// Event is the envelope published on a user's channel and written to their
// sockets.
type Event struct {
	Type           string `json:"type"`
	AnalysisID     string `json:"analysis_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
}

// Hub tracks this replica's notification sockets per user and bridges them to
// Redis, so an event published by any process (the worker, another replica)
// reaches every socket the user holds.
type Hub struct {
	rdb *redis.Client

	mu      sync.Mutex
	sockets map[uuid.UUID]map[uuid.UUID]func()
}

// NewHub returns a hub bridging local notification sockets to Redis.
func NewHub(rdb *redis.Client) *Hub {
	return &Hub{
		rdb:     rdb,
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
