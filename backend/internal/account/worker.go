package account

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// Worker applies queued deletions to the database.
type Worker struct {
	queries *db.Queries
}

// NewWorker builds the deletion worker; called once from cmd/worker.
func NewWorker(queries *db.Queries) *Worker {
	return &Worker{queries: queries}
}

// Handle deletes the account's row, which cascades to its coach profile,
// sessions, chat threads and messages, conversations, analyses and
// notifications. It satisfies JobHandler.
//
// A job whose user is already gone succeeds: deliveries are at-least-once, so a
// redelivery after a successful pass must not be treated as a failure and
// dead-lettered. lastAttempt only annotates the log line, since every error
// here (unparseable id, database down) is retried once and then dead-lettered
// by the queue.
func (w *Worker) Handle(ctx context.Context, job Job, lastAttempt bool) error {
	userID, err := uuid.Parse(job.UserID)
	if err != nil {
		return fmt.Errorf("parse user id %q: %w", job.UserID, err)
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

// PurgeExpiredRefreshTokens deletes refresh tokens that can no longer be
// exchanged, every interval until ctx is cancelled, so the table stays bounded
// whether or not accounts are being deleted. It blocks, and is meant to run in
// its own goroutine.
//
// Failures are logged and retried on the next tick rather than returned: the
// leftover rows are inert, and nothing else depends on the sweep.
func (w *Worker) PurgeExpiredRefreshTokens(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		rows, err := w.queries.DeleteExpiredRefreshTokens(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			slog.Warn("purge expired refresh tokens", "error", err)
		case err == nil && rows > 0:
			slog.Info("purged expired refresh tokens", "rows", rows)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
