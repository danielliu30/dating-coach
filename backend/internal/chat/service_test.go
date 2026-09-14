package chat

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// testPool connects to TEST_DATABASE_URL, skipping the test when it is unset.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// insertUser creates a user with the given role and registers its removal,
// which cascades to coach profiles and threads.
func insertUser(t *testing.T, pool *pgxpool.Pool, role string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email, password_hash, display_name, role) VALUES ($1, 'hash', $2, $3) RETURNING id`,
		uuid.NewString()+"@example.test", role, role).Scan(&id); err != nil {
		t.Fatalf("insert %s: %v", role, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", id)
	})
	return id
}

func TestStartThreadRequiresApprovedCoach(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	svc := NewService(db.New(pool), nil)
	client, coach := insertUser(t, pool, "user"), insertUser(t, pool, "coach")

	// A coach account with no profile row is unavailable too.
	if _, err := svc.StartThread(ctx, client, coach, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("thread with profile-less coach: err = %v, want ErrNotFound", err)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO coaches (user_id) VALUES ($1)`, coach); err != nil {
		t.Fatalf("insert coach: %v", err)
	}
	for _, status := range []string{"pending", "rejected"} {
		if _, err := pool.Exec(ctx, `UPDATE coaches SET approval_status = $2 WHERE user_id = $1`, coach, status); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.StartThread(ctx, client, coach, nil); !errors.Is(err, ErrNotFound) {
			t.Fatalf("thread with %s coach: err = %v, want ErrNotFound", status, err)
		}
	}

	if _, err := pool.Exec(ctx, `UPDATE coaches SET approval_status = 'approved' WHERE user_id = $1`, coach); err != nil {
		t.Fatal(err)
	}
	thread, err := svc.StartThread(ctx, client, coach, nil)
	if err != nil {
		t.Fatalf("thread with approved coach: %v", err)
	}
	// An existing thread stays reachable even if the coach is later un-approved.
	if _, err := pool.Exec(ctx, `UPDATE coaches SET approval_status = 'rejected' WHERE user_id = $1`, coach); err != nil {
		t.Fatal(err)
	}
	again, err := svc.StartThread(ctx, client, coach, nil)
	if err != nil || again.ID != thread.ID {
		t.Fatalf("re-open existing thread = %+v, %v; want %s", again, err, thread.ID)
	}
}
