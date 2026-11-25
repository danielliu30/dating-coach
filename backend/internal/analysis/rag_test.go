package analysis

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	pgvector "github.com/pgvector/pgvector-go"

	"github.com/danielliu30/dating-coach/backend/internal/notify"
	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// stubNotifier satisfies notify.Notifier without delivering anything.
type stubNotifier struct{}

func (stubNotifier) Email(context.Context, string, string, string) error { return nil }
func (stubNotifier) Send(context.Context, notify.Message) error          { return nil }
func (stubNotifier) Push(context.Context, string, string, string) error  { return nil }

func TestTranscriptDigestTagsSpeakersInOrder(t *testing.T) {
	messages := []db.Message{
		{Sender: "self", Body: "hey there"},
		{Sender: "match", Body: "hi!"},
		{Sender: "self", Body: "how was your weekend?"},
	}
	got := transcriptDigest(messages, 10000)
	want := "self: hey there\nmatch: hi!\nself: how was your weekend?"
	if got != want {
		t.Fatalf("digest = %q, want %q", got, want)
	}
}

func TestTranscriptDigestTruncatesAtLineBoundary(t *testing.T) {
	messages := []db.Message{
		{Sender: "self", Body: "aaa"},   // "self: aaa" = 9 chars
		{Sender: "match", Body: "bb"},   // "match: bb" = 9 chars
		{Sender: "self", Body: "ccccc"}, // "self: ccccc" = 11 chars
	}
	for _, tc := range []struct {
		maxChars int
		want     string
	}{
		{19, "self: aaa\nmatch: bb"}, // second line lands exactly on the budget
		{18, "self: aaa"},            // second line would overflow: dropped whole
		{9, "self: aaa"},
		{8, ""}, // even the first line does not fit: empty digest
	} {
		if got := transcriptDigest(messages, tc.maxChars); got != tc.want {
			t.Fatalf("digest(maxChars=%d) = %q, want %q", tc.maxChars, got, tc.want)
		}
	}
	if got := transcriptDigest(nil, 100); got != "" {
		t.Fatalf("empty digest = %q, want \"\"", got)
	}
}

func TestSimilarConversationsDisabledSkipsEmbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("ml call to %s while rag disabled", r.URL.Path)
	}))
	defer srv.Close()

	w := NewWorker(nil, NewMLClient(srv.URL, time.Second), nil, RAGConfig{Enabled: false})
	got := w.similarConversations(context.Background(), db.Conversation{}, []db.Message{{Sender: "self", Body: "hi"}})
	if got != nil {
		t.Fatalf("got %+v, want nil", got)
	}
}

// unitVector returns a 1536-dim basis vector (matching rag_chunks' vector(1536)).
func unitVector(dim int) []float32 {
	v := make([]float32, 1536)
	v[dim] = 1
	return v
}

func unitVector64(dim int) []float64 {
	v := make([]float64, 1536)
	v[dim] = 1
	return v
}

// TestSimilarConversations exercises the retrieval path against a real
// database: an httptest analyzer serves /embed, rag_chunks serves candidates.
func TestSimilarConversations(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := db.New(pool)

	userID, inflightID := insertConversation(t, pool)
	old, err := q.CreateConversation(ctx, db.CreateConversationParams{
		UserID: userID, Title: "old", Platform: "hinge", MatchName: "match",
	})
	if err != nil {
		t.Fatalf("create old conversation: %v", err)
	}

	embedding := pgvector.NewVector(unitVector(0))
	upsert := func(convID uuid.UUID, content string) {
		t.Helper()
		if _, err := q.UpsertRagChunk(ctx, db.UpsertRagChunkParams{
			OwnerID:        &userID,
			ConversationID: &convID,
			Content:        content,
			Embedding:      embedding,
			Model:          "test-embedding-model",
			DocVersion:     1,
		}); err != nil {
			t.Fatalf("upsert chunk: %v", err)
		}
	}
	upsert(old.ID, "self: baked bread\nmatch: that smells amazing")
	// A chunk on the in-flight conversation itself must be excluded.
	upsert(inflightID, "self: this very conversation\nmatch: still talking")

	embedStatus := http.StatusOK
	embedVec := unitVector64(0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embed" {
			t.Errorf("unexpected ml call to %s", r.URL.Path)
			return
		}
		if embedStatus != http.StatusOK {
			http.Error(w, `{"detail":"embed down"}`, embedStatus)
			return
		}
		_ = json.NewEncoder(w).Encode(MLEmbedResponse{
			Model:      "test-embedding-model",
			Dims:       1536,
			Embeddings: [][]float64{embedVec},
		})
	}))
	defer srv.Close()

	rag := RAGConfig{Enabled: true, TopK: 4, MaxDistance: 0.35, MaxDocChars: 12000, MaxContextChars: 8000}
	w := NewWorker(q, NewMLClient(srv.URL, 5*time.Second), nil, rag)
	inflight := db.Conversation{ID: inflightID, UserID: userID}
	messages := []db.Message{{Sender: "self", Body: "hi"}, {Sender: "match", Body: "hey"}}

	t.Run("retrieves the old conversation and excludes the in-flight one", func(t *testing.T) {
		got := w.similarConversations(ctx, inflight, messages)
		if len(got) != 1 {
			t.Fatalf("got %d results, want 1: %+v", len(got), got)
		}
		if got[0].ConversationID != old.ID.String() {
			t.Fatalf("conversation_id = %s, want %s", got[0].ConversationID, old.ID)
		}
		if got[0].Content != "self: baked bread\nmatch: that smells amazing" {
			t.Fatalf("content = %q", got[0].Content)
		}
	})

	t.Run("drops matches beyond MaxDistance", func(t *testing.T) {
		embedVec = unitVector64(1) // orthogonal to the indexed vector: distance 1.0
		defer func() { embedVec = unitVector64(0) }()
		if got := w.similarConversations(ctx, inflight, messages); len(got) != 0 {
			t.Fatalf("got %+v, want no results beyond MaxDistance", got)
		}
	})

	t.Run("returns nil when embed fails", func(t *testing.T) {
		embedStatus = http.StatusInternalServerError
		defer func() { embedStatus = http.StatusOK }()
		if got := w.similarConversations(ctx, inflight, messages); got != nil {
			t.Fatalf("got %+v, want nil on embed failure", got)
		}
	})

	t.Run("truncates context to MaxContextChars", func(t *testing.T) {
		tight := NewWorker(q, NewMLClient(srv.URL, 5*time.Second), nil, RAGConfig{
			Enabled: true, TopK: 4, MaxDistance: 0.35, MaxDocChars: 12000, MaxContextChars: 10,
		})
		got := tight.similarConversations(ctx, inflight, messages)
		if len(got) != 1 || got[0].Content != "self: bake" {
			t.Fatalf("got %+v, want content truncated to 10 chars", got)
		}
	})
}

var analyzeResponse = MLResponse{
	ModelVersion: "v1",
	Segments:     []MLSegment{{StartPosition: 0, EndPosition: 0, EngagementScore: 0.6, Label: "warm", Comment: "nice opener"}},
	Overall:      MLOverall{EngagementScore: 0.6, Summary: "promising"},
}

// seedJob creates the user, conversation + message and pending analysis row a
// job needs, and returns it.
func seedJob(t *testing.T, pool *pgxpool.Pool, q *db.Queries) Job {
	t.Helper()
	ctx := context.Background()
	userID, conversationID := insertConversation(t, pool)
	if _, err := q.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conversationID,
		Position:       0,
		Sender:         "self",
		Body:           "hi, loved your hiking photos",
		SentAt:         time.Now(),
	}); err != nil {
		t.Fatalf("create message: %v", err)
	}
	analysis, err := q.CreateAnalysisResult(ctx, conversationID)
	if err != nil {
		t.Fatalf("create analysis: %v", err)
	}
	return Job{
		AnalysisID:     analysis.ID.String(),
		ConversationID: conversationID.String(),
		UserID:         userID.String(),
	}
}

// TestHandleCompletesWhenRetrievalFails is the load-bearing guarantee: a dead
// /embed degrades the analyze request to no-context, it never fails the job.
func TestHandleCompletesWhenRetrievalFails(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := db.New(pool)
	job := seedJob(t, pool, q)

	var analyzeReq MLRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/embed":
			http.Error(w, `{"detail":"embed down"}`, http.StatusInternalServerError)
		case "/analyze":
			if err := json.NewDecoder(r.Body).Decode(&analyzeReq); err != nil {
				t.Errorf("decode analyze request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(analyzeResponse)
		default:
			t.Errorf("unexpected ml call to %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	w := NewWorker(q, NewMLClient(srv.URL, 5*time.Second), stubNotifier{}, RAGConfig{
		Enabled: true, TopK: 4, MaxDistance: 0.35, MaxDocChars: 12000, MaxContextChars: 8000,
	})
	if err := w.Handle(ctx, job, true); err != nil {
		t.Fatalf("Handle with dead /embed: %v", err)
	}
	if len(analyzeReq.SimilarConversations) != 0 {
		t.Fatalf("analyze carried %+v similar conversations despite embed failure", analyzeReq.SimilarConversations)
	}
	row, err := q.GetAnalysisResult(ctx, mustUUID(t, job.AnalysisID))
	if err != nil {
		t.Fatalf("read analysis: %v", err)
	}
	if row.Status != "succeeded" {
		t.Fatalf("analysis status = %q, want succeeded", row.Status)
	}
}

// TestHandleInjectsRetrievedContext proves the wiring end to end: the chunk
// indexed for the owner's older conversation lands on the /analyze request.
func TestHandleInjectsRetrievedContext(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := db.New(pool)
	job := seedJob(t, pool, q)

	userID := mustUUID(t, job.UserID)
	old, err := q.CreateConversation(ctx, db.CreateConversationParams{
		UserID: userID, Title: "old", Platform: "hinge", MatchName: "match",
	})
	if err != nil {
		t.Fatalf("create old conversation: %v", err)
	}
	if _, err := q.UpsertRagChunk(ctx, db.UpsertRagChunkParams{
		OwnerID:        &userID,
		ConversationID: &old.ID,
		Content:        "self: baked bread\nmatch: that smells amazing",
		Embedding:      pgvector.NewVector(unitVector(0)),
		Model:          "test-embedding-model",
		DocVersion:     1,
	}); err != nil {
		t.Fatalf("upsert chunk: %v", err)
	}

	var analyzeReq MLRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/embed":
			_ = json.NewEncoder(w).Encode(MLEmbedResponse{
				Model: "test-embedding-model", Dims: 1536,
				Embeddings: [][]float64{unitVector64(0)},
			})
		case "/analyze":
			if err := json.NewDecoder(r.Body).Decode(&analyzeReq); err != nil {
				t.Errorf("decode analyze request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(analyzeResponse)
		default:
			t.Errorf("unexpected ml call to %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	w := NewWorker(q, NewMLClient(srv.URL, 5*time.Second), stubNotifier{}, RAGConfig{
		Enabled: true, TopK: 4, MaxDistance: 0.35, MaxDocChars: 12000, MaxContextChars: 8000,
	})
	if err := w.Handle(ctx, job, true); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(analyzeReq.SimilarConversations) != 1 ||
		analyzeReq.SimilarConversations[0].ConversationID != old.ID.String() ||
		analyzeReq.SimilarConversations[0].Content != "self: baked bread\nmatch: that smells amazing" {
		t.Fatalf("similar_conversations on the wire = %+v", analyzeReq.SimilarConversations)
	}
	row, err := q.GetAnalysisResult(ctx, mustUUID(t, job.AnalysisID))
	if err != nil {
		t.Fatalf("read analysis: %v", err)
	}
	if row.Status != "succeeded" {
		t.Fatalf("analysis status = %q, want succeeded", row.Status)
	}
}

func TestRagDocumentAppendsOutcomeLine(t *testing.T) {
	messages := []db.Message{
		{Sender: "self", Body: "hey there"},
		{Sender: "match", Body: "hi!"},
	}
	got := ragDocument(messages, "they hit it off", 10000)
	want := "self: hey there\nmatch: hi!\nOutcome: they hit it off"
	if got != want {
		t.Fatalf("doc = %q, want %q", got, want)
	}
}

func TestRagDocumentOmitsOutcomeWhenSummaryEmpty(t *testing.T) {
	messages := []db.Message{{Sender: "self", Body: "hi"}}
	got := ragDocument(messages, "", 10000)
	if got != "self: hi" {
		t.Fatalf("doc = %q, want %q", got, "self: hi")
	}
}

func TestRagDocumentCapsDigestAtMaxChars(t *testing.T) {
	messages := []db.Message{
		{Sender: "self", Body: "aaa"}, // "self: aaa" = 9 chars
		{Sender: "match", Body: "bb"}, // would overflow a 9-char digest budget
	}
	// maxChars applies to the digest portion only; the outcome line is
	// appended past the cap.
	got := ragDocument(messages, "went well", 9)
	want := "self: aaa\nOutcome: went well"
	if got != want {
		t.Fatalf("doc = %q, want %q", got, want)
	}
}

// TestIndexConversationDisabledWritesNothing: disabled indexing makes no ml
// call and leaves no rag_chunks row.
func TestIndexConversationDisabledWritesNothing(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := db.New(pool)
	userID, conversationID := insertConversation(t, pool)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("ml call to %s while rag disabled", r.URL.Path)
	}))
	defer srv.Close()

	w := NewWorker(q, NewMLClient(srv.URL, time.Second), nil, RAGConfig{Enabled: false})
	w.indexConversation(ctx, db.Conversation{ID: conversationID, UserID: userID},
		[]db.Message{{Sender: "self", Body: "hi"}}, "went well")

	if n := chunkCount(t, pool, conversationID); n != 0 {
		t.Fatalf("rag_chunks has %d rows, want 0", n)
	}
}

// TestHandleIndexesConversation proves the write path end to end: a
// successful Handle leaves one rag_chunks row holding the transcript digest
// plus the outcome line, stamped with the embed model and doc version.
func TestHandleIndexesConversation(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := db.New(pool)
	job := seedJob(t, pool, q)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/embed":
			_ = json.NewEncoder(w).Encode(MLEmbedResponse{
				Model: "test-embedding-model", Dims: 1536,
				Embeddings: [][]float64{unitVector64(0)},
			})
		case "/analyze":
			_ = json.NewEncoder(w).Encode(analyzeResponse)
		default:
			t.Errorf("unexpected ml call to %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	w := NewWorker(q, NewMLClient(srv.URL, 5*time.Second), stubNotifier{}, RAGConfig{
		Enabled: true, TopK: 4, MaxDistance: 0.35, MaxDocChars: 12000, MaxContextChars: 8000,
	})
	if err := w.Handle(ctx, job, true); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	conversationID := mustUUID(t, job.ConversationID)
	if n := chunkCount(t, pool, conversationID); n != 1 {
		t.Fatalf("rag_chunks has %d rows, want 1", n)
	}
	var content, model string
	var docVersion int16
	if err := pool.QueryRow(ctx,
		`SELECT content, model, doc_version FROM rag_chunks WHERE conversation_id = $1`,
		conversationID).Scan(&content, &model, &docVersion); err != nil {
		t.Fatalf("read rag chunk: %v", err)
	}
	if !strings.Contains(content, "self: hi, loved your hiking photos") {
		t.Fatalf("content = %q, want the transcript digest", content)
	}
	if !strings.Contains(content, "\nOutcome: promising") {
		t.Fatalf("content = %q, want a trailing Outcome line with the summary", content)
	}
	if model != "test-embedding-model" {
		t.Fatalf("model = %q, want test-embedding-model", model)
	}
	if docVersion != ragDocVersion {
		t.Fatalf("doc_version = %d, want %d", docVersion, ragDocVersion)
	}
}

// TestHandleSucceedsWhenIndexingFails is the write-path mirror of
// TestHandleCompletesWhenRetrievalFails: a dead /embed must never fail a job
// whose analysis already completed.
func TestHandleSucceedsWhenIndexingFails(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := db.New(pool)
	job := seedJob(t, pool, q)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/embed":
			http.Error(w, `{"detail":"embed down"}`, http.StatusInternalServerError)
		case "/analyze":
			_ = json.NewEncoder(w).Encode(analyzeResponse)
		default:
			t.Errorf("unexpected ml call to %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	w := NewWorker(q, NewMLClient(srv.URL, 5*time.Second), stubNotifier{}, RAGConfig{
		Enabled: true, TopK: 4, MaxDistance: 0.35, MaxDocChars: 12000, MaxContextChars: 8000,
	})
	if err := w.Handle(ctx, job, true); err != nil {
		t.Fatalf("Handle with dead /embed: %v", err)
	}
	row, err := q.GetAnalysisResult(ctx, mustUUID(t, job.AnalysisID))
	if err != nil {
		t.Fatalf("read analysis: %v", err)
	}
	if row.Status != "succeeded" {
		t.Fatalf("analysis status = %q, want succeeded", row.Status)
	}
	if n := chunkCount(t, pool, mustUUID(t, job.ConversationID)); n != 0 {
		t.Fatalf("rag_chunks has %d rows, want 0", n)
	}
}

// TestIndexConversationIsIdempotent: re-indexing a conversation overwrites
// its chunk via the (conversation_id, seq) upsert rather than adding rows.
func TestIndexConversationIsIdempotent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := db.New(pool)
	userID, conversationID := insertConversation(t, pool)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embed" {
			t.Errorf("unexpected ml call to %s", r.URL.Path)
			return
		}
		_ = json.NewEncoder(w).Encode(MLEmbedResponse{
			Model: "test-embedding-model", Dims: 1536,
			Embeddings: [][]float64{unitVector64(0)},
		})
	}))
	defer srv.Close()

	w := NewWorker(q, NewMLClient(srv.URL, 5*time.Second), nil, RAGConfig{
		Enabled: true, TopK: 4, MaxDistance: 0.35, MaxDocChars: 12000, MaxContextChars: 8000,
	})
	conversation := db.Conversation{ID: conversationID, UserID: userID}
	messages := []db.Message{{Sender: "self", Body: "hi"}}
	w.indexConversation(ctx, conversation, messages, "first outcome")
	w.indexConversation(ctx, conversation, messages, "second outcome")

	if n := chunkCount(t, pool, conversationID); n != 1 {
		t.Fatalf("rag_chunks has %d rows, want 1 after two indexes", n)
	}
	var content string
	if err := pool.QueryRow(ctx,
		`SELECT content FROM rag_chunks WHERE conversation_id = $1`, conversationID,
	).Scan(&content); err != nil {
		t.Fatalf("read rag chunk: %v", err)
	}
	if !strings.Contains(content, "Outcome: second outcome") {
		t.Fatalf("content = %q, want the second upsert's outcome", content)
	}
}

func chunkCount(t *testing.T, pool *pgxpool.Pool, conversationID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM rag_chunks WHERE conversation_id = $1`, conversationID,
	).Scan(&n); err != nil {
		t.Fatalf("count rag chunks: %v", err)
	}
	return n
}

func mustUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", s, err)
	}
	return id
}
