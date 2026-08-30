// Package account implements account deletion: the caller's tokens are revoked
// synchronously and the cascading row delete runs asynchronously on the worker.
package account

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// Revoker denies the outstanding tokens of an account and can undo that denial.
// Implemented by auth.Denylist; an interface here keeps the packages decoupled
// and testable.
type Revoker interface {
	Revoke(ctx context.Context, userID uuid.UUID) error
	Restore(ctx context.Context, userID uuid.UUID) error
}

// Publisher queues the row deletion. Implemented by JobPublisher.
type Publisher interface {
	Publish(ctx context.Context, job Job) error
}

// Service owns the deletion sequence shared by every entry point (currently the
// HTTP handler).
type Service struct {
	revoker   Revoker
	publisher Publisher
}

// NewService wires the service dependencies; called once from cmd/api.
func NewService(revoker Revoker, publisher Publisher) *Service {
	return &Service{revoker: revoker, publisher: publisher}
}

// Delete revokes the account's tokens and then queues the cascading row delete.
// The order matters: the revocation is durable before the caller is told the
// deletion was accepted, so access ends immediately even though the rows go
// away later. When queueing fails the revocation is undone, because a denylisted
// account cannot authenticate to retry - not even with a freshly issued token -
// so keeping it would lock a live account out for the token lifetime.
func (s *Service) Delete(ctx context.Context, userID uuid.UUID) error {
	if err := s.revoker.Revoke(ctx, userID); err != nil {
		return fmt.Errorf("revoke tokens: %w", err)
	}
	if err := s.publisher.Publish(ctx, Job{UserID: userID.String()}); err != nil {
		if restoreErr := s.revoker.Restore(ctx, userID); restoreErr != nil {
			slog.Error("account stays revoked after a failed deletion publish",
				"error", restoreErr, "user_id", userID)
		}
		return fmt.Errorf("queue account deletion: %w", err)
	}
	return nil
}
