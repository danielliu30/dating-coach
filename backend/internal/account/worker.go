package account

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// Worker performs the row deletion queued by Service.
type Worker struct {
	queries *db.Queries
}

// NewWorker wires the worker dependencies; called once from cmd/worker, whose
// consumer loop invokes Handle for every delivery.
func NewWorker(queries *db.Queries) *Worker {
	return &Worker{queries: queries}
}

// Handle deletes the account's row, which cascades to its conversations,
// analyses, chats and coaching sessions. A job for an already deleted account
// succeeds, so redeliveries are harmless. lastAttempt only affects logging: a
// returned error is retried once and then dead-lettered by Consume, since the
// rows must never be silently left behind.
func (w *Worker) Handle(ctx context.Context, job Job, lastAttempt bool) error {
	userID, err := uuid.Parse(job.UserID)
	if err != nil {
		return fmt.Errorf("parse user id: %w", err)
	}
	deleted, err := w.queries.DeleteUser(ctx, userID)
	if err != nil {
		if lastAttempt {
			slog.Error("account deletion will be dead-lettered", "error", err, "user_id", userID)
		}
		return fmt.Errorf("delete user %s: %w", userID, err)
	}
	if deleted == 0 {
		slog.Info("account already deleted", "user_id", userID)
		return nil
	}
	slog.Info("account deleted", "user_id", userID)
	return nil
}
