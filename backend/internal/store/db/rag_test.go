package db

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	pgvector "github.com/pgvector/pgvector-go"
)

// TestRagChunkUpsertAndFindSimilar exercises the rag_chunks queries end to end:
// a chunk matches for its owner, another conversation is excluded, and a
// different owner sees nothing. CI applies the migrations before running tests.
func TestRagChunkUpsertAndFindSimilar(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	q := New(pool)

	user, err := q.CreateUser(ctx, CreateUserParams{
		Email:        uuid.NewString() + "@example.test",
		PasswordHash: "hash",
		DisplayName:  "rag tester",
		Role:         "user",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	defer func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", user.ID) }()

	newConversation := func(title string) Conversation {
		conv, err := q.CreateConversation(ctx, CreateConversationParams{
			UserID:    user.ID,
			Title:     title,
			Platform:  "hinge",
			MatchName: "match",
		})
		if err != nil {
			t.Fatalf("create conversation: %v", err)
		}
		return conv
	}
	matched := newConversation("matched")
	excluded := newConversation("excluded")

	vec := make([]float32, 1536)
	vec[0] = 1
	embedding := pgvector.NewVector(vec)

	if _, err := q.UpsertRagChunk(ctx, UpsertRagChunkParams{
		OwnerID:        &user.ID,
		ConversationID: &matched.ID,
		Content:        "summary of the matched conversation",
		Embedding:      embedding,
		Model:          "test-embedding-model",
		DocVersion:     1,
	}); err != nil {
		t.Fatalf("upsert chunk: %v", err)
	}
	if _, err := q.UpsertRagChunk(ctx, UpsertRagChunkParams{
		OwnerID:        &user.ID,
		ConversationID: &excluded.ID,
		Content:        "summary of the excluded conversation",
		Embedding:      embedding,
		Model:          "test-embedding-model",
		DocVersion:     1,
	}); err != nil {
		t.Fatalf("upsert excluded chunk: %v", err)
	}

	rows, err := q.FindSimilarConversations(ctx, FindSimilarConversationsParams{
		QueryEmbedding:        embedding,
		OwnerID:               &user.ID,
		ExcludeConversationID: &excluded.ID,
		MaxResults:            10,
	})
	if err != nil {
		t.Fatalf("find similar: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("find similar rows = %d, want 1", len(rows))
	}
	if rows[0].ConversationID == nil || *rows[0].ConversationID != matched.ID {
		t.Fatalf("matched conversation = %v, want %v", rows[0].ConversationID, matched.ID)
	}
	if rows[0].Distance < 0 || rows[0].Distance > 1e-9 {
		t.Fatalf("distance = %v, want ~0", rows[0].Distance)
	}

	otherUser, err := q.CreateUser(ctx, CreateUserParams{
		Email:        uuid.NewString() + "@example.test",
		PasswordHash: "hash",
		DisplayName:  "other owner",
		Role:         "user",
	})
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}
	defer func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", otherUser.ID) }()

	rows, err = q.FindSimilarConversations(ctx, FindSimilarConversationsParams{
		QueryEmbedding:        embedding,
		OwnerID:               &otherUser.ID,
		ExcludeConversationID: &uuid.Nil,
		MaxResults:            10,
	})
	if err != nil {
		t.Fatalf("find similar for other owner: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("other owner rows = %d, want 0", len(rows))
	}
}
