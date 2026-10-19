package notify

import (
	"testing"

	"github.com/google/uuid"
)

// TestEndSessionsDropsOnlyLiveSocketsOfThatUser pins what a revocation
// announcement may touch: every socket the account holds here, once each, and
// nobody else's, and never one that already closed.
func TestEndSessionsDropsOnlyLiveSocketsOfThatUser(t *testing.T) {
	hub := NewHub(nil)
	revoked, other := uuid.New(), uuid.New()

	var live, closed, spared int
	hub.Register(revoked, uuid.New(), func() { live++ })
	release := hub.Register(revoked, uuid.New(), func() { closed++ })
	hub.Register(other, uuid.New(), func() { spared++ })
	release()

	hub.EndSessions(revoked)
	hub.EndSessions(revoked)

	if live != 1 {
		t.Fatalf("live socket dropped %d times, want 1", live)
	}
	if closed != 0 {
		t.Fatal("dropped a socket that had already gone away")
	}
	if spared != 0 {
		t.Fatalf("dropped %d sockets of another account", spared)
	}
}
