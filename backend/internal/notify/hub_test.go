package notify

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// newTestHub returns a hub over a throwaway in-memory Redis, plus a client for
// publishing to it and the server itself for inspecting subscriptions.
func newTestHub(t *testing.T) (*Hub, *redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewHub(rdb), rdb, mr
}

// waitReady fails the test unless ready closes within a second.
func waitReady(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("subscription never became ready")
	}
}

// receive fails the test unless events yields one event within a second.
func receive(t *testing.T, events <-chan Event) Event {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("event channel closed")
		}
		return event
	case <-time.After(time.Second):
		t.Fatal("no event delivered")
	}
	return Event{}
}

// publishRaw publishes event on userID's channel straight through Redis, the
// way any other process would.
func publishRaw(t *testing.T, rdb *redis.Client, userID uuid.UUID, event Event) {
	t.Helper()
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := rdb.Publish(context.Background(), UserChannel(userID), payload).Err(); err != nil {
		t.Fatalf("publish: %v", err)
	}
}

func TestUserChannel(t *testing.T) {
	id := uuid.MustParse("6f1c1f1e-0000-4000-8000-000000000001")
	if got, want := UserChannel(id), "notify:user:6f1c1f1e-0000-4000-8000-000000000001"; got != want {
		t.Fatalf("UserChannel = %q, want %q", got, want)
	}
}

// TestSubscribeFansOutToEverySocketOfThatUser checks that one Redis
// subscription per user feeds all of that user's local sockets, and that
// another user's channel does not leak into them.
func TestSubscribeFansOutToEverySocketOfThatUser(t *testing.T) {
	hub, rdb, mr := newTestHub(t)
	userID, other := uuid.New(), uuid.New()

	first, ready, unsubFirst := hub.Subscribe(userID)
	defer unsubFirst()
	second, readyAgain, unsubSecond := hub.Subscribe(userID)
	defer unsubSecond()
	waitReady(t, ready)
	waitReady(t, readyAgain)

	if n := mr.PubSubNumSub(UserChannel(userID))[UserChannel(userID)]; n != 1 {
		t.Fatalf("redis subscriptions for the user = %d, want 1 shared by both sockets", n)
	}

	publishRaw(t, rdb, other, Event{Type: EventAnalysisReady, AnalysisID: "not-yours"})
	want := Event{Type: EventAnalysisReady, AnalysisID: uuid.NewString(), ConversationID: uuid.NewString()}
	publishRaw(t, rdb, userID, want)

	for i, events := range []<-chan Event{first, second} {
		if got := receive(t, events); got != want {
			t.Fatalf("socket %d got %+v, want %+v", i, got, want)
		}
	}
}

// TestUnsubscribeLastSocketEndsTheRedisSubscription pins the lazy lifecycle:
// the subscription outlives every socket but the last, and is dropped with it.
func TestUnsubscribeLastSocketEndsTheRedisSubscription(t *testing.T) {
	hub, _, mr := newTestHub(t)
	userID := uuid.New()
	channel := UserChannel(userID)

	first, ready, unsubFirst := hub.Subscribe(userID)
	_, _, unsubSecond := hub.Subscribe(userID)
	waitReady(t, ready)

	unsubFirst()
	unsubFirst()
	if _, open := <-first; open {
		t.Fatal("unsubscribed socket's channel still open")
	}
	if n := mr.PubSubNumSub(channel)[channel]; n != 1 {
		t.Fatalf("subscriptions after the first socket left = %d, want 1", n)
	}

	unsubSecond()
	deadline := time.Now().Add(time.Second)
	for mr.PubSubNumSub(channel)[channel] != 0 {
		if time.Now().After(deadline) {
			t.Fatal("redis subscription outlived the last socket")
		}
		time.Sleep(10 * time.Millisecond)
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.users) != 0 {
		t.Fatalf("users index = %d entries, want it emptied", len(hub.users))
	}
}

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

// TestSlowSocketIsCutOffNotLeftWaiting checks that a socket whose buffer is full
// has its event channel closed, ending it so the client reconnects and
// refetches, while the user's other sockets keep receiving everything.
func TestSlowSocketIsCutOffNotLeftWaiting(t *testing.T) {
	hub, rdb, _ := newTestHub(t)
	userID := uuid.New()

	slow, ready, unsubSlow := hub.Subscribe(userID)
	defer unsubSlow()
	fast, _, unsubFast := hub.Subscribe(userID)
	defer unsubFast()
	waitReady(t, ready)

	const sent = 20
	for i := 0; i < sent; i++ {
		publishRaw(t, rdb, userID, Event{Type: EventAnalysisReady})
		receive(t, fast)
	}

	buffered := 0
	for range slow {
		buffered++
	}
	if buffered == 0 || buffered >= sent {
		t.Fatalf("slow socket got %d of %d events before its channel closed", buffered, sent)
	}
}

// TestCancelledPumpCannotFeedANewerSubscription checks that a message still
// held by the pump of a subscription that has ended is not delivered to the
// sockets of the user's next subscription.
func TestCancelledPumpCannotFeedANewerSubscription(t *testing.T) {
	hub, _, _ := newTestHub(t)
	userID := uuid.New()

	_, ready, unsubOld := hub.Subscribe(userID)
	waitReady(t, ready)
	hub.mu.Lock()
	oldFeed := hub.users[userID]
	hub.mu.Unlock()
	unsubOld()

	events, _, unsub := hub.Subscribe(userID)
	defer unsub()
	hub.broadcastLocal(userID, oldFeed, Event{Type: EventAnalysisReady})

	select {
	case event := <-events:
		t.Fatalf("new socket received %+v from the old subscription", event)
	case <-time.After(100 * time.Millisecond):
	}
}
