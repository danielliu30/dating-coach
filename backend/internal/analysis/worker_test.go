package analysis

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/danielliu30/dating-coach/backend/internal/notify"
	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// silentNotifier accepts every delivery, standing in for the email/push stub.
type silentNotifier struct{}

// Email always succeeds.
func (silentNotifier) Email(context.Context, string, string, string) error { return nil }

// Send always succeeds.
func (silentNotifier) Send(context.Context, notify.Message) error { return nil }

// Push always succeeds.
func (silentNotifier) Push(context.Context, string, string, string) error { return nil }

// subscribeUser opens a Redis subscription on userID's notification channel
// and returns a function receiving the next event published there.
func subscribeUser(t *testing.T, rdb *redis.Client, userID uuid.UUID) func() notify.Event {
	t.Helper()
	messages := subscribeChannel(t, rdb, userID)
	return func() notify.Event {
		t.Helper()
		event, ok := receiveWithin(t, messages, 5*time.Second)
		if !ok {
			t.Fatal("no event published")
		}
		return event
	}
}

// subscribeChannel opens a live Redis subscription on userID's notification
// channel, closed when the test ends, and returns its message stream.
func subscribeChannel(t *testing.T, rdb *redis.Client, userID uuid.UUID) <-chan *redis.Message {
	t.Helper()
	ctx := context.Background()
	sub := rdb.Subscribe(ctx, notify.UserChannel(userID))
	t.Cleanup(func() { _ = sub.Close() })
	if _, err := sub.Receive(ctx); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	return sub.Channel()
}

// receiveWithin returns the next event on messages, or false when none arrives
// within wait.
func receiveWithin(t *testing.T, messages <-chan *redis.Message, wait time.Duration) (notify.Event, bool) {
	t.Helper()
	select {
	case msg := <-messages:
		var event notify.Event
		if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
			t.Fatalf("decode %s: %v", msg.Payload, err)
		}
		return event, true
	case <-time.After(wait):
		return notify.Event{}, false
	}
}

// TestPublishIsBestEffort pins that an unreachable Redis is only logged: the
// analysis is already durable by the time an event is published, so a failed
// publish must neither panic nor block the job.
func TestPublishIsBestEffort(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	w := NewWorker(nil, nil, silentNotifier{}, rdb)

	userID := uuid.New()
	next := subscribeUser(t, rdb, userID)
	want := notify.Event{Type: notify.EventAnalysisReady, AnalysisID: uuid.NewString(), ConversationID: uuid.NewString()}
	w.publish(context.Background(), userID, want)
	if got := next(); got != want {
		t.Fatalf("published %+v, want %+v", got, want)
	}

	mr.Close()
	w.publish(context.Background(), userID, want)
}

// workerFixture is one queued analysis owned by a fresh user, in the database
// at TEST_DATABASE_URL.
type workerFixture struct {
	queries *db.Queries
	userID  uuid.UUID
	job     Job
}

// newWorkerFixture inserts a user, a two-message conversation and a pending
// analysis of it, skipping the test when TEST_DATABASE_URL is unset. The rows
// are deleted when the test ends.
func newWorkerFixture(t *testing.T) workerFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	queries := db.New(pool)

	var userID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, display_name) VALUES ($1, 'hash', 'Worker') RETURNING id`,
		uuid.NewString()+"@example.test",
	).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	})
	conversation, err := queries.CreateConversation(ctx, db.CreateConversationParams{UserID: userID, Title: "t", Platform: "hinge", MatchName: "Sam"})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	for i, sender := range []string{"self", "match"} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO messages (conversation_id, position, sender, body) VALUES ($1, $2, $3, 'hi')`,
			conversation.ID, i, sender,
		); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}
	analysis, err := queries.CreateAnalysisResult(ctx, conversation.ID)
	if err != nil {
		t.Fatalf("create analysis: %v", err)
	}
	return workerFixture{
		queries: queries,
		userID:  userID,
		job: Job{
			AnalysisID:     analysis.ID.String(),
			ConversationID: conversation.ID.String(),
			UserID:         userID.String(),
		},
	}
}

// mlServer answers /analyze with status and, on success, a minimal result.
func mlServer(t *testing.T, status int) *MLClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"model_version":"test","segments":[],"overall":{}}`))
		}
	}))
	t.Cleanup(server.Close)
	return NewMLClient(server.URL, 5*time.Second)
}

// TestHandlePublishesAnalysisReady checks the event the app's result screen
// waits on is published once the analysis is complete and names it.
func TestHandlePublishesAnalysisReady(t *testing.T) {
	f := newWorkerFixture(t)
	rdb := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	next := subscribeUser(t, rdb, f.userID)

	w := NewWorker(f.queries, mlServer(t, http.StatusOK), silentNotifier{}, rdb)
	if err := w.Handle(context.Background(), f.job, false); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	want := notify.Event{Type: notify.EventAnalysisReady, AnalysisID: f.job.AnalysisID, ConversationID: f.job.ConversationID}
	if got := next(); got != want {
		t.Fatalf("published %+v, want %+v", got, want)
	}
	row, err := f.queries.GetAnalysisResult(context.Background(), uuid.MustParse(f.job.AnalysisID))
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != "succeeded" {
		t.Fatalf("status = %q, want succeeded before the event announces it", row.Status)
	}
}

// TestHandlePublishesAnalysisFailedOnlyOnLastAttempt checks the result screen
// hears about a failure once it is final, and not while a redelivery may still
// succeed.
func TestHandlePublishesAnalysisFailedOnlyOnLastAttempt(t *testing.T) {
	f := newWorkerFixture(t)
	rdb := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	sub := subscribeChannel(t, rdb, f.userID)
	w := NewWorker(f.queries, mlServer(t, http.StatusInternalServerError), silentNotifier{}, rdb)

	if err := w.Handle(context.Background(), f.job, false); err == nil {
		t.Fatal("Handle succeeded although the analyzer failed")
	}
	if event, ok := receiveWithin(t, sub, 200*time.Millisecond); ok {
		t.Fatalf("published %+v while attempts were left", event)
	}

	if err := w.Handle(context.Background(), f.job, true); err != nil {
		t.Fatalf("last attempt: %v", err)
	}
	want := notify.Event{Type: notify.EventAnalysisFailed, AnalysisID: f.job.AnalysisID, ConversationID: f.job.ConversationID}
	got, ok := receiveWithin(t, sub, 5*time.Second)
	if !ok || got != want {
		t.Fatalf("published %+v (%v), want %+v", got, ok, want)
	}
	row, err := f.queries.GetAnalysisResult(context.Background(), uuid.MustParse(f.job.AnalysisID))
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != "failed" {
		t.Fatalf("status = %q, want failed before the event announces it", row.Status)
	}
}
