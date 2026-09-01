package account

import (
	"context"
	"errors"
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
