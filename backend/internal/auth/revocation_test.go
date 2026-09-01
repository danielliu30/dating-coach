package auth

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// TestAccountStatusFollowsTheDeletionMark covers what a long-lived connection
// asks between requests: an account is revoked from the moment its deletion is
// recorded, not once the worker gets round to removing its rows, and an account
// that never existed counts as revoked too.
func TestAccountStatusFollowsTheDeletionMark(t *testing.T) {
	svc, _, pool := newTestService(t)
	ctx := context.Background()
	queries := db.New(pool)
	status := NewAccountStatus(queries)

	if revoked, err := status.Revoked(ctx, uuid.New()); err != nil || !revoked {
		t.Fatalf("Revoked(unknown account) = %v, %v, want true, nil", revoked, err)
	}

	email := "socket-status-" + uuid.NewString() + "@example.com"
	t.Cleanup(func() { deleteTestUser(t, pool, email) })
	if _, err := svc.SignUp(ctx, SignUpInput{
		Email:       email,
		Password:    "correct-horse",
		DisplayName: "Socket Status",
	}); err != nil {
		t.Fatalf("sign up: %v", err)
	}

	var userID uuid.UUID
	if err := pool.QueryRow(ctx, "SELECT id FROM users WHERE email = $1", email).Scan(&userID); err != nil {
		t.Fatalf("read user id: %v", err)
	}

	if revoked, err := status.Revoked(ctx, userID); err != nil || revoked {
		t.Fatalf("Revoked(live account) = %v, %v, want false, nil", revoked, err)
	}

	if _, err := queries.RequestUserDeletion(ctx, userID); err != nil {
		t.Fatalf("record deletion: %v", err)
	}

	if revoked, err := status.Revoked(ctx, userID); err != nil || !revoked {
		t.Fatalf("Revoked(deleted account) = %v, %v, want true, nil", revoked, err)
	}
}
