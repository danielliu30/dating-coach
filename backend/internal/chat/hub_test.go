package chat

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// numSub polls Redis until the thread channel's subscriber count reaches want
// or the deadline passes, and returns what it last saw.
func numSub(t *testing.T, rdb *redis.Client, threadID uuid.UUID, want int) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	last := -1
	for time.Now().Before(deadline) {
		res, err := rdb.PubSubNumSub(context.Background(), channelFor(threadID)).Result()
		if err == nil {
			last = int(res[channelFor(threadID)])
			if last == want {
				return last
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return last
}

// TestUnsubscribeReleasesPumpSubscription pins the pump's shutdown: once the
// last socket leaves a thread the Redis subscription must be released too. The
// blocking ReceiveMessage does not unblock on context cancel alone, so without
// an explicit pubsub close the subscription (and the goroutine holding it)
// would leak, and every later subscriber would receive relayed events once per
// leaked pump.
func TestUnsubscribeReleasesPumpSubscription(t *testing.T) {
	mr := miniredis.RunT(t)
	hub := NewHub(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	threadID := uuid.New()

	_, unsub := hub.Subscribe(threadID)
	if got := numSub(t, hub.rdb, threadID, 1); got != 1 {
		t.Fatalf("numsub after subscribe = %d, want 1", got)
	}

	unsub()
	if got := numSub(t, hub.rdb, threadID, 0); got != 0 {
		t.Fatalf("numsub after unsubscribe = %d, want 0 (pump subscription leaked)", got)
	}
}

// TestResubscribeDeliversOnce covers the leak's user-visible blast radius: a
// thread that is re-subscribed must relay each published event exactly once to
// the new socket, not once per generation of dead pump.
func TestResubscribeDeliversOnce(t *testing.T) {
	mr := miniredis.RunT(t)
	hub := NewHub(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	threadID := uuid.New()

	_, unsub := hub.Subscribe(threadID)
	numSub(t, hub.rdb, threadID, 1)
	unsub()
	numSub(t, hub.rdb, threadID, 0)

	events, unsub2 := hub.Subscribe(threadID)
	defer unsub2()
	numSub(t, hub.rdb, threadID, 1)

	if err := hub.Publish(context.Background(), threadID, Event{Type: EventMessage}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case <-events:
	case <-time.After(2 * time.Second):
		t.Fatalf("event never delivered")
	}
	select {
	case <-events:
		t.Fatalf("second copy of the same event delivered")
	case <-time.After(300 * time.Millisecond):
	}
}

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
