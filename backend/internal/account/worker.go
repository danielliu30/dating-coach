package account

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// Revoker ends every outstanding session of an account. Worker depends on the
// interface because the Redis-backed implementation lives in internal/auth,
// which already imports this package to publish the jobs.
type Revoker interface {
	Revoke(ctx context.Context, userID uuid.UUID) error
}

// Worker applies queued deletions to the database.
type Worker struct {
	queries *db.Queries
	revoker Revoker
}

// NewWorker builds the deletion worker; called once from cmd/worker. revoker is
// the session denylist, which Handle writes to before it removes any rows.
func NewWorker(queries *db.Queries, revoker Revoker) *Worker {
	return &Worker{queries: queries, revoker: revoker}
}

// Handle revokes the account's sessions and then deletes its row, which
// cascades to its coach profile, sessions, chat threads and messages,
// conversations, analyses and notifications. It satisfies JobHandler.
//
// Revoking here is what lets the API queue a deletion before revoking anything,
// and so retry a deletion it could not queue instead of locking the caller out
// of an account that is still alive. A failed revocation leaves the rows in
// place and is retried with the job: a token that outlives the row it
// authenticates reaches handlers as a user that no longer exists.
//
// A job whose user is already gone succeeds, including when the revocation it
// no longer needs is what failed: deliveries are at-least-once, so a redelivery
// after a successful pass must not be treated as a failure and dead-lettered.
// lastAttempt only annotates the log line, since every error here (unparseable
// id, database down) is retried by the queue before it is dead-lettered.
func (w *Worker) Handle(ctx context.Context, job Job, lastAttempt bool) error {
	userID, err := uuid.Parse(job.UserID)
	if err != nil {
		return fmt.Errorf("parse user id %q: %w", job.UserID, err)
	}
	if err := w.revoker.Revoke(ctx, userID); err != nil {
		if w.deleted(ctx, userID) {
			slog.Info("account already deleted", "user_id", userID, "last_attempt", lastAttempt)
			return nil
		}
		return fmt.Errorf("revoke sessions of %s: %w", userID, err)
	}
	rows, err := w.queries.DeleteUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("delete user %s: %w", userID, err)
	}
	if rows == 0 {
		slog.Info("account already deleted", "user_id", userID, "last_attempt", lastAttempt)
		return nil
	}
	slog.Info("account deleted", "user_id", userID)
	return nil
}

// deleted reports that userID has no row left at all, which makes the rest of
// the job a no-op. It asks whether the row is physically there rather than
// whether it is readable, since a deletion this worker has yet to apply has
// been marked deleted and so is hidden from every other query. It answers false
// when the question cannot be answered, so a database that is unreachable at
// the same time as Redis still fails the job rather than declaring an
// outstanding deletion done.
func (w *Worker) deleted(ctx context.Context, userID uuid.UUID) bool {
	exists, err := w.queries.UserRowExists(ctx, userID)
	return err == nil && !exists
}
