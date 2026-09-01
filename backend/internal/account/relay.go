package account

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// RelayInterval is how often the relay looks for deletions that were recorded
// but never queued. It is the worst-case delay added to a deletion the API could
// not publish itself, which is only the case while the broker is unreachable.
const RelayInterval = 5 * time.Second

// relayBatch bounds one pass. Deletions are rare, so the only way to accumulate
// this many is a broker outage, and a bounded pass keeps the recovery from
// holding a connection for an unbounded time.
const relayBatch = 100

// Publisher queues a deletion job. Relay depends on the interface so its tests
// can run without a broker.
type Publisher interface {
	Publish(ctx context.Context, job Job) error
}

// Relay publishes the deletions in the outbox that no request managed to queue,
// which is what makes a recorded deletion certain to happen: DELETE /me commits
// the mark and the outbox row together, and everything after that is either the
// API's own publish or this.
type Relay struct {
	queries   *db.Queries
	publisher Publisher
}

// NewRelay builds the outbox relay; called once from cmd/worker. publisher is
// the same deletion queue the API publishes to.
func NewRelay(queries *db.Queries, publisher Publisher) *Relay {
	return &Relay{queries: queries, publisher: publisher}
}

// Run drains the outbox every RelayInterval until ctx is cancelled, returning
// nil on cancellation. A pass that fails is logged rather than returned: the
// next tick retries it, and the deletions stay in the outbox meanwhile.
func (r *Relay) Run(ctx context.Context) error {
	ticker := time.NewTicker(RelayInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := r.Drain(ctx); err != nil {
				slog.Error("publish recorded account deletions", "error", err)
			}
		}
	}
}

// Drain queues up to relayBatch recorded deletions and reports how many were
// published. A deletion is marked published only after the broker has confirmed
// it, so a crash in between republishes it later; the worker's handler is
// idempotent, which is what makes the duplicate harmless. It stops at the first
// publish that fails, since the cause is the broker rather than the job.
func (r *Relay) Drain(ctx context.Context) (int, error) {
	pending, err := r.queries.PendingDeletions(ctx, relayBatch)
	if err != nil {
		return 0, fmt.Errorf("read deletion outbox: %w", err)
	}
	for i, userID := range pending {
		if err := r.publisher.Publish(ctx, Job{UserID: userID.String()}); err != nil {
			return i, fmt.Errorf("queue recorded deletion of %s: %w", userID, err)
		}
		if _, err := r.queries.MarkDeletionPublished(ctx, userID); err != nil {
			return i, fmt.Errorf("mark deletion of %s published: %w", userID, err)
		}
		slog.Info("queued a recorded account deletion", "user_id", userID)
	}
	return len(pending), nil
}
