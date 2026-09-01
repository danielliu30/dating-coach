package account

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// failingRevoker stands in for a denylist whose Redis is unreachable.
type failingRevoker struct{}

// Revoke always fails.
func (failingRevoker) Revoke(context.Context, uuid.UUID) error { return errors.New("redis is down") }

// TestHandleKeepsRowsWhenRevocationFails pins the order the deletion is applied
// in: rows outliving their revocation is recoverable, whereas a live token whose
// user row is gone reaches handlers as an account that no longer exists. The
// failure has to come from the revocation, so an unreachable database is not
// enough to pass.
func TestHandleKeepsRowsWhenRevocationFails(t *testing.T) {
	// A pool pointed at a closed port: reading the row cannot confirm the
	// deletion has already been applied, so the job stays retryable.
	pool, err := pgxpool.New(context.Background(), "postgres://user:pass@127.0.0.1:1/db")
	if err != nil {
		t.Fatalf("build pool: %v", err)
	}
	t.Cleanup(pool.Close)

	err = NewWorker(db.New(pool), failingRevoker{}).Handle(
		context.Background(),
		Job{UserID: uuid.NewString()},
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "revoke sessions") {
		t.Fatalf("Handle error = %v, want the revocation to have failed it", err)
	}
}

// TestHandleKeepsMarkedRowsWhenRevocationFails covers the state every accepted
// deletion passes through: the row is marked deleted, so it is invisible to the
// queries the rest of the app uses, while its data is still there. Treating that
// as an already-applied deletion would acknowledge the job and keep the data
// forever.
func TestHandleKeepsMarkedRowsWhenRevocationFails(t *testing.T) {
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

	var userID uuid.UUID
	if err := pool.QueryRow(
		ctx,
		`INSERT INTO users (email, password_hash, display_name, deleted_at)
		 VALUES ($1, 'hash', 'Marked', now())
		 RETURNING id`,
		uuid.NewString()+"@example.test",
	).Scan(&userID); err != nil {
		t.Fatalf("insert marked user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	})

	queries := db.New(pool)
	err = NewWorker(queries, failingRevoker{}).Handle(ctx, Job{UserID: userID.String()}, false)
	if err == nil {
		t.Fatal("Handle succeeded although the account's sessions were never revoked")
	}
	exists, err := queries.UserRowExists(ctx, userID)
	if err != nil {
		t.Fatalf("read user row: %v", err)
	}
	if !exists {
		t.Fatal("row was deleted before its sessions were revoked")
	}
}
