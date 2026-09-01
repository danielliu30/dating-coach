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

// stubPublisher records the jobs handed to it and can be told to fail. It
// stops the sweep once it has seen the job the test is waiting for, or on the
// first failure, so no test waits out a tick. Other rows left in the shared
// test database may be swept alongside it, hence waiting for a specific job
// rather than the first one.
type stubPublisher struct {
	jobs  []Job
	err   error
	until uuid.UUID
	stop  context.CancelFunc
}

// Publish records the job, or returns the configured failure without recording
// it.
func (s *stubPublisher) Publish(_ context.Context, job Job) error {
	if s.err != nil {
		s.stop()
		return s.err
	}
	s.jobs = append(s.jobs, job)
	if job.UserID == s.until.String() {
		s.stop()
	}
	return nil
}

// queued reports whether userID was published for deletion.
func (s *stubPublisher) queued(userID uuid.UUID) bool {
	for _, job := range s.jobs {
		if job.UserID == userID.String() {
			return true
		}
	}
	return false
}

// TestRequeuePendingDeletionsPicksUpAnUnqueuedDeletion covers the recovery the
// sweep exists for: the account was marked deleted but the job never reached
// the broker, and its owner can no longer sign in to ask again.
func TestRequeuePendingDeletionsPicksUpAnUnqueuedDeletion(t *testing.T) {
	pool := testPool(t)
	worker := NewWorker(db.New(pool))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pending := insertUser(t, pool, true)
	live := insertUser(t, pool, false)

	publisher := &stubPublisher{until: pending, stop: cancel}
	worker.RequeuePendingDeletions(ctx, time.Hour, publisher)

	if !publisher.queued(pending) {
		t.Fatalf("the account marked deleted (%s) was never queued: %v", pending, publisher.jobs)
	}
	if publisher.queued(live) {
		t.Fatalf("a live account (%s) was queued for deletion", live)
	}
}

// TestRequeuePendingDeletionsSurvivesABrokerOutage pins that a publish failure
// leaves the mark in place to be retried rather than taking the worker down.
func TestRequeuePendingDeletionsSurvivesABrokerOutage(t *testing.T) {
	pool := testPool(t)
	worker := NewWorker(db.New(pool))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	userID := insertUser(t, pool, true)

	worker.RequeuePendingDeletions(ctx, time.Hour, &stubPublisher{err: errors.New("broker down"), stop: cancel})

	var deletedAt *time.Time
	if err := pool.QueryRow(context.Background(),
		"SELECT deleted_at FROM users WHERE id = $1", userID).Scan(&deletedAt); err != nil {
		t.Fatalf("read deleted_at: %v", err)
	}
	if deletedAt == nil {
		t.Fatal("the deletion mark was lost, so nothing would retry the deletion")
	}
}

// insertUser adds a user, optionally already marked deleted, and removes it
// when the test ends. It returns the new user's id.
func insertUser(t *testing.T, pool *pgxpool.Pool, deleted bool) uuid.UUID {
	t.Helper()
	email := "sweep-" + uuid.NewString() + "@example.com"
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DELETE FROM users WHERE email = $1", email); err != nil {
			t.Errorf("delete test user: %v", err)
		}
	})
	var id uuid.UUID
	err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email, password_hash, display_name, deleted_at)
		 VALUES ($1, 'x', 'Sweep Test', CASE WHEN $2 THEN now() END) RETURNING id`,
		email, deleted).Scan(&id)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

// testPool opens the test database, skipping the test when TEST_DATABASE_URL
// is unset so the suite still runs without one.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
