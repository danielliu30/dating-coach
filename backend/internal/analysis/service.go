// Package analysis accepts dating-app conversations, queues them for scoring and
// exposes the results produced by the ML analyzer.
package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// Sentinel errors respondErr maps onto HTTP status codes.
var (
	ErrNotFound     = errors.New("not found")
	ErrForbidden    = errors.New("not allowed")
	ErrInvalidInput = errors.New("invalid input")
)

const (
	maxMessages       = 500
	maxMessageLength  = 4000
	defaultListLimit  = 25
	analysisQueueKind = "analysis_ready"
)

// Publisher enqueues analysis jobs for the worker. JobPublisher is the RabbitMQ
// implementation; tests substitute a fake.
type Publisher interface {
	Publish(ctx context.Context, job Job) error
}

// Service owns the conversation-analysis business rules: it validates and
// stores submitted transcripts, enqueues the scoring job, reads results back
// with an ownership check, and records outcome labels for model training.
type Service struct {
	pool      *pgxpool.Pool
	queries   *db.Queries
	publisher Publisher
}

// NewService wires the service dependencies; called once from cmd/api.
func NewService(pool *pgxpool.Pool, queries *db.Queries, publisher Publisher) *Service {
	return &Service{pool: pool, queries: queries, publisher: publisher}
}

// SubmitInput is the decoded POST /analysis/conversations body: the transcript
// the user pasted in, plus where it came from.
type SubmitInput struct {
	Title     string          `json:"title"`
	Platform  string          `json:"platform"`
	MatchName string          `json:"match_name"`
	Messages  []SubmitMessage `json:"messages"`
}

// SubmitMessage is one message of a submitted transcript. Sender is "self" or
// "match"; SentAt is optional RFC3339 and defaults to now.
type SubmitMessage struct {
	Sender string `json:"sender"`
	Body   string `json:"body"`
	SentAt string `json:"sent_at"`
}

// Result is the API view of an analysis row. Segments and Overall stay raw JSON
// so the stored scoring payload reaches clients unchanged.
type Result struct {
	ID             string          `json:"id"`
	ConversationID string          `json:"conversation_id"`
	Status         string          `json:"status"`
	ModelVersion   string          `json:"model_version"`
	Segments       json.RawMessage `json:"segments"`
	Overall        json.RawMessage `json:"overall"`
	Error          string          `json:"error,omitempty"`
	CreatedAt      string          `json:"created_at"`
	CompletedAt    string          `json:"completed_at,omitempty"`
}

// Conversation is the list-view summary of a stored transcript.
type Conversation struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Platform  string `json:"platform"`
	MatchName string `json:"match_name"`
	CreatedAt string `json:"created_at"`
}

// resultOf projects an analysis row onto the API shape, formatting timestamps.
func resultOf(a db.AnalysisResult) Result {
	out := Result{
		ID:             a.ID.String(),
		ConversationID: a.ConversationID.String(),
		Status:         a.Status,
		ModelVersion:   a.ModelVersion,
		Segments:       a.Segments,
		Overall:        a.Overall,
		Error:          a.Error,
		CreatedAt:      a.CreatedAt.UTC().Format(time.RFC3339),
	}
	if a.CompletedAt != nil {
		out.CompletedAt = a.CompletedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// Submit stores the conversation and enqueues an analysis job.
func (s *Service) Submit(ctx context.Context, userID uuid.UUID, in SubmitInput) (Result, error) {
	if len(in.Messages) == 0 {
		return Result{}, fmt.Errorf("%w: at least one message is required", ErrInvalidInput)
	}
	if len(in.Messages) > maxMessages {
		return Result{}, fmt.Errorf("%w: at most %d messages are supported", ErrInvalidInput, maxMessages)
	}
	for i, m := range in.Messages {
		if m.Sender != "self" && m.Sender != "match" {
			return Result{}, fmt.Errorf("%w: messages[%d].sender must be self or match", ErrInvalidInput, i)
		}
		if strings.TrimSpace(m.Body) == "" || len(m.Body) > maxMessageLength {
			return Result{}, fmt.Errorf("%w: messages[%d].body is empty or too long", ErrInvalidInput, i)
		}
	}
	if in.Platform == "" {
		in.Platform = "unknown"
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	conversation, err := q.CreateConversation(ctx, db.CreateConversationParams{
		UserID:    userID,
		Title:     in.Title,
		Platform:  in.Platform,
		MatchName: in.MatchName,
	})
	if err != nil {
		return Result{}, fmt.Errorf("create conversation: %w", err)
	}

	for i, m := range in.Messages {
		sentAt := time.Now().UTC()
		if m.SentAt != "" {
			parsed, err := time.Parse(time.RFC3339, m.SentAt)
			if err != nil {
				return Result{}, fmt.Errorf("%w: messages[%d].sent_at must be RFC3339", ErrInvalidInput, i)
			}
			sentAt = parsed
		}
		if _, err := q.CreateMessage(ctx, db.CreateMessageParams{
			ConversationID: conversation.ID,
			Position:       int32(i),
			Sender:         m.Sender,
			Body:           m.Body,
			SentAt:         sentAt,
		}); err != nil {
			return Result{}, fmt.Errorf("create message: %w", err)
		}
	}

	result, err := q.CreateAnalysisResult(ctx, conversation.ID)
	if err != nil {
		return Result{}, fmt.Errorf("create analysis result: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("commit transaction: %w", err)
	}

	job := Job{
		AnalysisID:     result.ID.String(),
		ConversationID: conversation.ID.String(),
		UserID:         userID.String(),
	}
	if err := s.publisher.Publish(ctx, job); err != nil {
		failed, failErr := s.queries.FailAnalysis(ctx, db.FailAnalysisParams{ID: result.ID, Error: "could not enqueue analysis"})
		if failErr != nil {
			return Result{}, fmt.Errorf("publish job: %w (and mark failed: %v)", err, failErr)
		}
		return resultOf(failed), fmt.Errorf("publish job: %w", err)
	}
	return resultOf(result), nil
}

// Reanalyze queues a fresh scoring run for a conversation the user already
// submitted, so a run that failed for a transient reason (the analyzer being
// down, a broker hiccup) can be retried without re-pasting the transcript.
// It returns the pending analysis to poll. A run that is still pending or
// running is returned as-is instead of being duplicated, so repeated calls
// enqueue at most one job. Errors are ErrNotFound for an unknown conversation
// and ErrForbidden when it belongs to someone else.
func (s *Service) Reanalyze(ctx context.Context, conversationID, userID uuid.UUID) (Result, error) {
	if _, err := s.ownedConversation(ctx, conversationID, userID); err != nil {
		return Result{}, err
	}
	latest, err := s.queries.GetLatestAnalysisForConversation(ctx, conversationID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Result{}, fmt.Errorf("get latest analysis: %w", err)
	}
	if err == nil && (latest.Status == "pending" || latest.Status == "running") {
		return resultOf(latest), nil
	}

	result, err := s.queries.CreateAnalysisResult(ctx, conversationID)
	if err != nil {
		return Result{}, fmt.Errorf("create analysis result: %w", err)
	}
	job := Job{
		AnalysisID:     result.ID.String(),
		ConversationID: conversationID.String(),
		UserID:         userID.String(),
	}
	if err := s.publisher.Publish(ctx, job); err != nil {
		failed, failErr := s.queries.FailAnalysis(ctx, db.FailAnalysisParams{ID: result.ID, Error: "could not enqueue analysis"})
		if failErr != nil {
			return Result{}, fmt.Errorf("publish job: %w (and mark failed: %v)", err, failErr)
		}
		return resultOf(failed), fmt.Errorf("publish job: %w", err)
	}
	return resultOf(result), nil
}

// Get returns one analysis by ID, provided userID owns its conversation.
func (s *Service) Get(ctx context.Context, analysisID, userID uuid.UUID) (Result, error) {
	result, err := s.queries.GetAnalysisResult(ctx, analysisID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, ErrNotFound
		}
		return Result{}, fmt.Errorf("get analysis: %w", err)
	}
	if _, err := s.ownedConversation(ctx, result.ConversationID, userID); err != nil {
		return Result{}, err
	}
	return resultOf(result), nil
}

// LatestForConversation returns the newest analysis of a conversation, which is
// what clients poll while a submission is still being scored.
func (s *Service) LatestForConversation(ctx context.Context, conversationID, userID uuid.UUID) (Result, error) {
	if _, err := s.ownedConversation(ctx, conversationID, userID); err != nil {
		return Result{}, err
	}
	result, err := s.queries.GetLatestAnalysisForConversation(ctx, conversationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, ErrNotFound
		}
		return Result{}, fmt.Errorf("get latest analysis: %w", err)
	}
	return resultOf(result), nil
}

// ListConversations returns a page of the user's submitted conversations.
func (s *Service) ListConversations(ctx context.Context, userID uuid.UUID, limit, offset int32) ([]Conversation, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	rows, err := s.queries.ListConversationsForUser(ctx, db.ListConversationsForUserParams{
		UserID: userID,
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	out := make([]Conversation, 0, len(rows))
	for _, row := range rows {
		out = append(out, Conversation{
			ID:        row.ID.String(),
			Title:     row.Title,
			Platform:  row.Platform,
			MatchName: row.MatchName,
			CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return out, nil
}

// validOutcomes mirrors the training_examples outcome constraint and the
// options the app offers; ml-analyzer/training/data_schema.md documents them.
var validOutcomes = map[string]bool{
	"ghosted":          true,
	"kept_talking":     true,
	"number_exchanged": true,
	"date_set":         true,
}

// LabelInput is the decoded label body: how the conversation actually went, as
// reported by the user or a coach.
type LabelInput struct {
	Outcome         *string         `json:"outcome"`
	ReplyReceived   *bool           `json:"reply_received"`
	EngagementScore *float64        `json:"engagement_score"`
	SegmentLabels   json.RawMessage `json:"segment_labels"`
	Notes           string          `json:"notes"`
	Consented       bool            `json:"consented"`
	LabelSource     string          `json:"label_source"`
}

// Label records an outcome label for a conversation. These rows are the training
// set for the self-trained scorer (see ml-analyzer/README.md).
func (s *Service) Label(ctx context.Context, conversationID, userID uuid.UUID, in LabelInput) (db.TrainingExample, error) {
	if _, err := s.ownedConversation(ctx, conversationID, userID); err != nil {
		return db.TrainingExample{}, err
	}
	source := in.LabelSource
	if source == "" {
		source = "user"
	}
	if source != "user" && source != "coach" && source != "heuristic" {
		return db.TrainingExample{}, fmt.Errorf("%w: unknown label_source %q", ErrInvalidInput, source)
	}
	if in.Outcome != nil && !validOutcomes[*in.Outcome] {
		return db.TrainingExample{}, fmt.Errorf("%w: unknown outcome %q", ErrInvalidInput, *in.Outcome)
	}
	if in.EngagementScore != nil && (*in.EngagementScore < 0 || *in.EngagementScore > 1) {
		return db.TrainingExample{}, fmt.Errorf("%w: engagement_score must be between 0 and 1", ErrInvalidInput)
	}
	segmentLabels := in.SegmentLabels
	if len(segmentLabels) == 0 {
		segmentLabels = json.RawMessage(`[]`)
	}

	example, err := s.queries.UpsertTrainingExample(ctx, db.UpsertTrainingExampleParams{
		ConversationID:  conversationID,
		LabelSource:     source,
		Outcome:         in.Outcome,
		ReplyReceived:   in.ReplyReceived,
		EngagementScore: in.EngagementScore,
		SegmentLabels:   segmentLabels,
		Notes:           in.Notes,
		Consented:       in.Consented,
	})
	if err != nil {
		return db.TrainingExample{}, fmt.Errorf("save training example: %w", err)
	}
	return example, nil
}

// ownedConversation loads a conversation and enforces that userID owns it, so
// analyses cannot be read or labelled across accounts.
func (s *Service) ownedConversation(ctx context.Context, conversationID, userID uuid.UUID) (db.Conversation, error) {
	conversation, err := s.queries.GetConversation(ctx, conversationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.Conversation{}, ErrNotFound
		}
		return db.Conversation{}, fmt.Errorf("get conversation: %w", err)
	}
	if conversation.UserID != userID {
		return db.Conversation{}, ErrForbidden
	}
	return conversation, nil
}
