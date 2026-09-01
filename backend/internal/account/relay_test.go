package account

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// recordingPublisher collects the jobs a relay pass queued.
type recordingPublisher struct{ jobs []Job }

// Publish appends the job, always succeeding.
func (p *recordingPublisher) Publish(_ context.Context, job Job) error {
	p.jobs = append(p.jobs, job)
	return nil
}

// failingPublisher stands in for a broker the relay cannot reach.
type failingPublisher struct{}

// Publish always fails.
func (failingPublisher) Publish(context.Context, Job) error { return errors.New("broker is down") }

// recordDeletion inserts a user, records its deletion the way DELETE /me does
// and returns the id, registering the cleanup of both rows.
func recordDeletion(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()

	ctx := context.Background()
	var userID uuid.UUID
	if err := pool.QueryRow(
		ctx,
		`INSERT INTO users (email, password_hash, display_name)
		 VALUES ($1, 'hash', 'Outbox') RETURNING id`,
		uuid.NewString()+"@example.test",
	).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM account_deletions WHERE user_id = $1", userID)
	})
	if _, err := db.New(pool).RequestUserDeletion(ctx, userID); err != nil {
		t.Fatalf("record deletion: %v", err)
	}
	return userID
}

// testPool connects to TEST_DATABASE_URL, skipping the test when it is unset so
// the suite still runs without Postgres.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// publishedAt reads the outbox row's published_at, failing the test when the row
// is gone.
func publishedAt(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) *time.Time {
	t.Helper()

	var at *time.Time
	if err := pool.QueryRow(
		context.Background(),
		"SELECT published_at FROM account_deletions WHERE user_id = $1",
		userID,
	).Scan(&at); err != nil {
		t.Fatalf("read outbox row: %v", err)
	}
	return at
}

// TestDrainQueuesRecordedDeletions covers the guarantee the outbox exists for: a
// deletion the API committed but never published is queued by the relay, and is
// not queued a second time once it has been.
func TestDrainQueuesRecordedDeletions(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	userID := recordDeletion(t, pool)

	publisher := &recordingPublisher{}
	relay := NewRelay(db.New(pool), publisher)
	if _, err := relay.Drain(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(publisher.jobs) != 1 || publisher.jobs[0].UserID != userID.String() {
		t.Fatalf("queued jobs = %v, want the deletion of %s", publisher.jobs, userID)
	}
	if publishedAt(t, pool, userID) == nil {
		t.Fatal("a queued deletion was left unpublished, so it will be queued again")
	}

	if _, err := relay.Drain(ctx); err != nil {
		t.Fatalf("second drain: %v", err)
	}
	if len(publisher.jobs) != 1 {
		t.Fatalf("queued jobs = %v, want the deletion queued once", publisher.jobs)
	}
}

// TestDrainKeepsDeletionsTheBrokerRefused pins what happens when the relay
// cannot publish: the outbox row stays unpublished, so the next pass retries it
// rather than dropping a deletion its owner was told had been accepted.
func TestDrainKeepsDeletionsTheBrokerRefused(t *testing.T) {
	pool := testPool(t)
	userID := recordDeletion(t, pool)

	if _, err := NewRelay(db.New(pool), failingPublisher{}).Drain(context.Background()); err == nil {
		t.Fatal("Drain succeeded although the broker refused the deletion")
	}
	if at := publishedAt(t, pool, userID); at != nil {
		t.Fatalf("deletion marked published at %s although it was never queued", at)
	}
}

// TestHandleClearsTheOutbox covers the end of a deletion's life: once the rows
// are gone the outbox row has nothing left to ask for, and leaving it behind
// would keep an unpublished retry alive for an account that no longer exists.
func TestHandleClearsTheOutbox(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	userID := recordDeletion(t, pool)

	queries := db.New(pool)
	if err := NewWorker(queries, silentRevoker{}).Handle(ctx, Job{UserID: userID.String()}, false); err != nil {
		t.Fatalf("handle: %v", err)
	}
	var rows int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM account_deletions WHERE user_id = $1", userID).Scan(&rows); err != nil {
		t.Fatalf("count outbox rows: %v", err)
	}
	if rows != 0 {
		t.Fatalf("outbox rows for %s = %d, want the applied deletion cleared", userID, rows)
	}
}
