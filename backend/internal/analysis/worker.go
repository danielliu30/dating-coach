package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/danielliu30/dating-coach/backend/internal/notify"
	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// Worker consumes analysis jobs, calls the ML service and writes the results back.
type Worker struct {
	queries  *db.Queries
	ml       *MLClient
	notifier notify.Notifier
}

func NewWorker(queries *db.Queries, ml *MLClient, notifier notify.Notifier) *Worker {
	return &Worker{queries: queries, ml: ml, notifier: notifier}
}

// Handle processes a single job. It returns an error only for failures worth
// another delivery; on the last attempt the failure is recorded on the analysis
// row and the job is acked.
func (w *Worker) Handle(ctx context.Context, job Job, lastAttempt bool) error {
	analysisID, err := uuid.Parse(job.AnalysisID)
	if err != nil {
		return fmt.Errorf("parse analysis id: %w", err)
	}
	conversationID, err := uuid.Parse(job.ConversationID)
	if err != nil {
		return fmt.Errorf("parse conversation id: %w", err)
	}

	// Redeliveries of an already finished analysis are acked without rescoring.
	if _, err := w.queries.ClaimAnalysis(ctx, analysisID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			slog.Info("skipping analysis in terminal state", "analysis_id", analysisID)
			return nil
		}
		return fmt.Errorf("claim analysis: %w", err)
	}

	conversation, err := w.queries.GetConversation(ctx, conversationID)
	if err != nil {
		return w.fail(ctx, analysisID, lastAttempt, fmt.Errorf("load conversation: %w", err))
	}
	messages, err := w.queries.ListMessages(ctx, conversationID)
	if err != nil {
		return w.fail(ctx, analysisID, lastAttempt, fmt.Errorf("load messages: %w", err))
	}

	req := MLRequest{
		ConversationID: conversationID.String(),
		Platform:       conversation.Platform,
		MatchName:      conversation.MatchName,
		Messages:       make([]MLMessage, 0, len(messages)),
	}
	for _, m := range messages {
		req.Messages = append(req.Messages, MLMessage{
			Position: m.Position,
			Sender:   m.Sender,
			Body:     m.Body,
			SentAt:   m.SentAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
	}

	resp, err := w.ml.Analyze(ctx, req)
	if err != nil {
		return w.fail(ctx, analysisID, lastAttempt, err)
	}

	segments, err := json.Marshal(resp.Segments)
	if err != nil {
		return w.fail(ctx, analysisID, lastAttempt, fmt.Errorf("encode segments: %w", err))
	}
	overall, err := json.Marshal(resp.Overall)
	if err != nil {
		return w.fail(ctx, analysisID, lastAttempt, fmt.Errorf("encode overall: %w", err))
	}

	if _, err := w.queries.CompleteAnalysis(ctx, db.CompleteAnalysisParams{
		ID:           analysisID,
		Segments:     segments,
		Overall:      overall,
		ModelVersion: resp.ModelVersion,
	}); err != nil {
		return fmt.Errorf("complete analysis: %w", err)
	}

	w.notifyReady(ctx, job, analysisID)
	return nil
}

// fail records a permanent failure on the analysis row, or returns cause so the
// job is redelivered when there are attempts left.
func (w *Worker) fail(ctx context.Context, analysisID uuid.UUID, lastAttempt bool, cause error) error {
	if !lastAttempt {
		return cause
	}
	slog.Error("analysis failed", "error", cause, "analysis_id", analysisID)
	if _, err := w.queries.FailAnalysis(ctx, db.FailAnalysisParams{ID: analysisID, Error: cause.Error()}); err != nil {
		return fmt.Errorf("mark analysis failed: %w", err)
	}
	return nil
}

// notifyReady records and delivers the "analysis ready" notification. Email/push
// delivery itself is a stub until the mobile clients register device tokens.
func (w *Worker) notifyReady(ctx context.Context, job Job, analysisID uuid.UUID) {
	userID, err := uuid.Parse(job.UserID)
	if err != nil {
		slog.Error("parse user id", "error", err, "analysis_id", analysisID)
		return
	}
	payload, err := json.Marshal(map[string]string{
		"analysis_id":     analysisID.String(),
		"conversation_id": job.ConversationID,
	})
	if err != nil {
		slog.Error("encode notification payload", "error", err)
		return
	}
	notification, err := w.queries.CreateNotification(ctx, db.CreateNotificationParams{
		UserID:  userID,
		Kind:    analysisQueueKind,
		Payload: payload,
	})
	if err != nil {
		slog.Error("create notification", "error", err)
		return
	}
	if err := w.notifier.Push(ctx, userID.String(), "Your conversation analysis is ready",
		"Open the app to see where the conversation was engaging."); err != nil {
		slog.Error("push notification", "error", err)
		return
	}
	if err := w.queries.MarkNotificationSent(ctx, notification.ID); err != nil {
		slog.Error("mark notification sent", "error", err)
	}
}
