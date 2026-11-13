package analysis

import (
	"context"
	"encoding/json"
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

// insertConversation creates a user owning one empty conversation; deleting the
// user cascades to the conversation and its training_examples rows.
func insertConversation(t *testing.T, pool *pgxpool.Pool) (userID, conversationID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, display_name) VALUES ($1, 'hash', 'labeller') RETURNING id`,
		uuid.NewString()+"@example.test").Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	})
	if err := pool.QueryRow(ctx,
		`INSERT INTO conversations (user_id, platform) VALUES ($1, 'hinge') RETURNING id`, userID).Scan(&conversationID); err != nil {
		t.Fatalf("insert conversation: %v", err)
	}
	return userID, conversationID
}

// TestLabelStoresAppOutcomeWithEmptySegments posts the exact body the app's
// "What happened next?" card sends and checks the weak-label row it leaves.
func TestLabelStoresAppOutcomeWithEmptySegments(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	svc := NewService(pool, db.New(pool), nil)

	for _, body := range []string{
		`{"outcome":"ghosted","reply_received":false,"engagement_score":null,"segment_labels":null,"notes":"","consented":true,"label_source":"user"}`,
		`{"outcome":"date_set","reply_received":true,"consented":true}`,
	} {
		userID, conversationID := insertConversation(t, pool)
		var in LabelInput
		if err := json.Unmarshal([]byte(body), &in); err != nil {
			t.Fatalf("decode %s: %v", body, err)
		}
		if _, err := svc.Label(ctx, conversationID, userID, in); err != nil {
			t.Fatalf("Label(%s): %v", body, err)
		}

		var outcome, labelSource, segmentsType string
		var replyReceived, consented bool
		if err := pool.QueryRow(ctx,
			`SELECT outcome, label_source, reply_received, consented, jsonb_typeof(segment_labels)
			 FROM training_examples WHERE conversation_id = $1`, conversationID,
		).Scan(&outcome, &labelSource, &replyReceived, &consented, &segmentsType); err != nil {
			t.Fatalf("read training example: %v", err)
		}
		if outcome != *in.Outcome || replyReceived != *in.ReplyReceived || !consented || labelSource != "user" {
			t.Errorf("row = (%s, %v, %v, %s), want (%s, %v, true, user)",
				outcome, replyReceived, consented, labelSource, *in.Outcome, *in.ReplyReceived)
		}
		if segmentsType != "array" {
			t.Errorf("segment_labels stored as jsonb %s for %s, want array", segmentsType, body)
		}
	}
}
