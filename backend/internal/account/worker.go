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
// succeeds, so redeliveries are harmless. attempt, the 1-based handler run,
// only affects logging: a returned error is retried by Consume until MaxAttempts
// failures dead-letter it, since the rows must never be silently left behind.
func (w *Worker) Handle(ctx context.Context, job Job, attempt int) error {
	userID, err := uuid.Parse(job.UserID)
	if err != nil {
		return fmt.Errorf("parse user id: %w", err)
	}
	deleted, err := w.queries.DeleteUser(ctx, userID)
	if err != nil {
		if attempt >= MaxAttempts {
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
