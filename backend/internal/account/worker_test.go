package account

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// failingRevoker stands in for a denylist whose Redis is unreachable.
type failingRevoker struct{}

// Revoke always fails.
func (failingRevoker) Revoke(context.Context, uuid.UUID) error { return errors.New("redis is down") }

// TestHandleKeepsRowsWhenRevocationFails pins the order the deletion is applied
// in: rows outliving their revocation is recoverable, whereas a live token whose
// user row is gone reaches handlers as an account that no longer exists. The
// worker holds a nil *db.Queries, so deleting anything would panic rather than
// quietly pass.
func TestHandleKeepsRowsWhenRevocationFails(t *testing.T) {
	err := NewWorker(nil, failingRevoker{}).Handle(context.Background(), Job{UserID: uuid.NewString()}, false)
	if err == nil {
		t.Fatal("Handle succeeded without revoking the account's sessions")
	}
}
