package notify

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// TestPublishReachesHubSubscribers drives an event from Publish through Redis
// into a hub socket, which is the path the worker and the API share.
func TestPublishReachesHubSubscribers(t *testing.T) {
	hub, rdb, _ := newTestHub(t)
	userID := uuid.New()

	events, ready, unsubscribe := hub.Subscribe(userID)
	defer unsubscribe()
	waitReady(t, ready)

	want := Event{Type: EventAnalysisFailed, AnalysisID: uuid.NewString(), ConversationID: uuid.NewString()}
	if err := Publish(context.Background(), rdb, userID, want); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if got := receive(t, events); got != want {
		t.Fatalf("delivered %+v, want %+v", got, want)
	}
}

// TestPublishReportsRedisFailure checks that an unreachable Redis is returned
// to the caller, which decides whether a lost push matters.
func TestPublishReportsRedisFailure(t *testing.T) {
	_, rdb, mr := newTestHub(t)
	mr.Close()

	if err := Publish(context.Background(), rdb, uuid.New(), Event{Type: EventAnalysisReady}); err == nil {
		t.Fatal("Publish to a stopped Redis returned nil")
	}
}
