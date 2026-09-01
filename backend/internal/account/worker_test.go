package account

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// TestHandleDeletesTheAccountAndItsRefreshTokens covers what applying a deletion
// is for: the row is gone, and the cascade takes the account's refresh tokens
// with it, which is what stops any further access token from being minted for
// it.
func TestHandleDeletesTheAccountAndItsRefreshTokens(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	queries := db.New(pool)
	userID := recordDeletion(t, pool)

	if err := queries.CreateRefreshToken(ctx, db.CreateRefreshTokenParams{
		TokenHash: uuid.NewString(),
		UserID:    userID,
		FamilyID:  uuid.New(),
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create refresh token: %v", err)
	}

	if err := NewWorker(queries).Handle(ctx, Job{UserID: userID.String()}, false); err != nil {
		t.Fatalf("handle deletion: %v", err)
	}

	exists, err := queries.UserRowExists(ctx, userID)
	if err != nil {
		t.Fatalf("read user row: %v", err)
	}
	if exists {
		t.Fatal("account row survived its deletion")
	}
	var tokens int
	if err := pool.QueryRow(
		ctx,
		"SELECT count(*) FROM refresh_tokens WHERE user_id = $1",
		userID,
	).Scan(&tokens); err != nil {
		t.Fatalf("count refresh tokens: %v", err)
	}
	if tokens != 0 {
		t.Fatalf("refresh tokens of the deleted account = %d, want 0", tokens)
	}
}

// TestHandleAcceptsARedeliveredDeletion pins the idempotency the at-least-once
// queue needs: a job for an account that is already gone must succeed, or a
// redelivery of a deletion that worked would be retried and dead-lettered.
func TestHandleAcceptsARedeliveredDeletion(t *testing.T) {
	pool := testPool(t)

	err := NewWorker(db.New(pool)).Handle(
		context.Background(),
		Job{UserID: uuid.NewString()},
		true,
	)
	if err != nil {
		t.Fatalf("handle a deletion whose account is gone: %v", err)
	}
}
