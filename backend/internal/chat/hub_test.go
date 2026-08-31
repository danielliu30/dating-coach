package chat

import (
	"testing"

	"github.com/google/uuid"
)

// TestEndSessionsDropsOnlyLiveSocketsOfThatUser pins what a revocation
// announcement is allowed to touch: every socket the account holds here, once
// each, and nobody else's.
func TestEndSessionsDropsOnlyLiveSocketsOfThatUser(t *testing.T) {
	hub := NewHub(nil)
	revoked, other := uuid.New(), uuid.New()

	var first, second, spared int
	hub.Register(revoked, uuid.New(), func() { first++ })
	hub.Register(revoked, uuid.New(), func() { second++ })
	hub.Register(other, uuid.New(), func() { spared++ })

	hub.EndSessions(revoked)
	hub.EndSessions(revoked)

	if first != 1 || second != 1 {
		t.Fatalf("drops = %d and %d, want 1 each", first, second)
	}
	if spared != 0 {
		t.Fatalf("dropped %d sockets of another account", spared)
	}
}

// TestEndSessionsSkipsClosedSockets covers the socket that closed on its own
// before the revocation arrived: its drop must not run, because the connection
// it refers to is gone.
func TestEndSessionsSkipsClosedSockets(t *testing.T) {
	hub := NewHub(nil)
	userID := uuid.New()

	var dropped int
	release := hub.Register(userID, uuid.New(), func() { dropped++ })
	release()
	release()

	hub.EndSessions(userID)

	if dropped != 0 {
		t.Fatalf("dropped a socket that had already gone away")
	}
	if len(hub.sockets) != 0 {
		t.Fatalf("sockets index = %d entries, want it emptied", len(hub.sockets))
	}
}
