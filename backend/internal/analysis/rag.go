package analysis

import (
	"context"
	"log/slog"
	"strings"
	"time"

	pgvector "github.com/pgvector/pgvector-go"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// RAGConfig carries the worker's retrieval knobs (from config.Config).
type RAGConfig struct {
	Enabled         bool
	TopK            int
	MaxDistance     float64
	MaxDocChars     int
	MaxContextChars int
}

// ragDocVersion stamps every row this binary writes to rag_chunks; bump it
// whenever ragDocument's format changes so stale rows stay distinguishable.
const ragDocVersion int16 = 1

// transcriptDigest renders messages as speaker-tagged lines, the same
// "self:"/"match:" vocabulary ml-analyzer's transcript renderer uses, joined by
// newlines and truncated to maxChars at a line boundary: the line that would
// overflow is dropped rather than cut mid-line.
func transcriptDigest(messages []db.Message, maxChars int) string {
	var b strings.Builder
	for _, m := range messages {
		line := m.Sender + ": " + m.Body
		need := len(line)
		if b.Len() > 0 {
			need += 1 // newline separator
		}
		if b.Len()+need > maxChars {
			break
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	return b.String()
}

// ragDocument is the text embedded and stored for a completed conversation:
// the transcript digest plus a trailing "Outcome:" line carrying the
// analyzer's summary, which biases retrieval toward conversations that ended
// similarly. The outcome line is appended beyond maxChars — the cap applies
// to the digest portion only.
func ragDocument(messages []db.Message, summary string, maxChars int) string {
	doc := transcriptDigest(messages, maxChars)
	if summary != "" {
		doc += "\nOutcome: " + summary
	}
	return doc
}

// indexConversation is the worker's best-effort write path: once a job has
// completed it embeds the conversation's ragDocument and upserts it into
// rag_chunks so future analyses can retrieve it. Like similarConversations,
// every failure is logged and swallowed — indexing must never fail or block
// a job that already succeeded.
func (w *Worker) indexConversation(ctx context.Context, conversation db.Conversation, messages []db.Message, summary string) {
	if !w.rag.Enabled {
		return
	}
	start := time.Now()

	doc := ragDocument(messages, summary, w.rag.MaxDocChars)
	if doc == "" {
		return
	}

	resp, err := w.ml.Embed(ctx, MLEmbedRequest{Inputs: []string{doc}})
	if err != nil {
		slog.Warn("rag index embed unavailable", "error", err, "conversation_id", conversation.ID)
		return
	}
	if len(resp.Embeddings) == 0 || len(resp.Embeddings[0]) == 0 {
		slog.Warn("rag index embed unavailable", "reason", "empty embedding", "conversation_id", conversation.ID)
		return
	}

	f32 := make([]float32, len(resp.Embeddings[0]))
	for i, f := range resp.Embeddings[0] {
		f32[i] = float32(f)
	}

	if _, err := w.queries.UpsertRagChunk(ctx, db.UpsertRagChunkParams{
		OwnerID:        &conversation.UserID,
		ConversationID: &conversation.ID,
		Content:        doc,
		Embedding:      pgvector.NewVector(f32),
		Model:          resp.Model,
		DocVersion:     ragDocVersion,
	}); err != nil {
		slog.Warn("rag chunk upsert failed", "error", err, "conversation_id", conversation.ID)
		return
	}
	slog.Debug("rag chunk written", "rag_write_ms", time.Since(start).Milliseconds(), "conversation_id", conversation.ID)
}

// similarConversations is the worker's best-effort retrieval step: it embeds
// the in-flight transcript, finds the owner's nearest past conversations in
// rag_chunks and returns them as scoring context for the analyzer. Every
// failure path logs a warning and returns nil — retrieval must never fail a
// job, it only degrades the context the scorer sees.
//
// The worker's pool sets hnsw.iterative_scan = relaxed_order
// (store.WithRelaxedIterativeScan in cmd/worker) so this filtered similarity
// search keeps scanning until TopK rows match.
func (w *Worker) similarConversations(ctx context.Context, conversation db.Conversation, messages []db.Message) []MLSimilarConversation {
	start := time.Now()
	var results []MLSimilarConversation
	var embedMS, queryMS int64
	var top1 float64
	skip := "disabled"
	// One cheap line per call: counts, durations and distances only — never
	// content or vectors.
	defer func() {
		attrs := []any{
			"rag_retrieved", len(results),
			"rag_embed_ms", embedMS,
			"rag_query_ms", queryMS,
			"rag_total_ms", time.Since(start).Milliseconds(),
		}
		if len(results) > 0 {
			attrs = append(attrs, "rag_top1_distance", top1)
		} else {
			attrs = append(attrs, "rag_skip_reason", skip)
		}
		slog.Debug("rag retrieval", attrs...)
	}()

	if !w.rag.Enabled {
		return nil
	}
	skip = "no_match"

	digest := transcriptDigest(messages, w.rag.MaxDocChars)
	if digest == "" {
		return nil
	}

	embedStart := time.Now()
	resp, err := w.ml.Embed(ctx, MLEmbedRequest{Inputs: []string{digest}})
	embedMS = time.Since(embedStart).Milliseconds()
	if err != nil {
		slog.Warn("rag embed unavailable", "error", err, "conversation_id", conversation.ID)
		skip = "embed_unavailable"
		return nil
	}
	if len(resp.Embeddings) == 0 || len(resp.Embeddings[0]) == 0 {
		slog.Warn("rag embed unavailable", "reason", "empty embedding", "conversation_id", conversation.ID)
		skip = "embed_unavailable"
		return nil
	}

	f32 := make([]float32, len(resp.Embeddings[0]))
	for i, f := range resp.Embeddings[0] {
		f32[i] = float32(f)
	}

	queryStart := time.Now()
	rows, err := w.queries.FindSimilarConversations(ctx, db.FindSimilarConversationsParams{
		QueryEmbedding:        pgvector.NewVector(f32),
		OwnerID:               &conversation.UserID,
		ExcludeConversationID: &conversation.ID,
		MaxResults:            int32(w.rag.TopK),
	})
	queryMS = time.Since(queryStart).Milliseconds()
	if err != nil {
		slog.Warn("rag similarity query failed", "error", err, "conversation_id", conversation.ID)
		skip = "query_error"
		return nil
	}

	budget := w.rag.MaxContextChars
	for _, row := range rows {
		if row.ConversationID == nil || row.Distance > w.rag.MaxDistance {
			continue
		}
		content := row.Content
		if len(content) > budget {
			content = content[:budget]
		}
		if content == "" {
			break // aggregate context budget is spent
		}
		if len(results) == 0 {
			top1 = row.Distance
		}
		results = append(results, MLSimilarConversation{
			ConversationID: row.ConversationID.String(),
			Content:        content,
		})
		budget -= len(content)
	}
	return results
}
