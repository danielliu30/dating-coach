package notify

import (
	"context"
	"log/slog"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// NudgePublisher wakes the email outbox relay.
type NudgePublisher interface {
	Publish(context.Context, struct{}) error
}

// Nudge requests immediate delivery, logging failures for the scheduled fallback.
func Nudge(ctx context.Context, p NudgePublisher) {
	if err := p.Publish(ctx, struct{}{}); err != nil {
		slog.Error("publish email outbox nudge; scheduled relay will retry", "error", err)
	}
}

// EmailSaver persists an email in an autocommitted outbox write.
type EmailSaver interface {
	EnqueueEmail(context.Context, db.EnqueueEmailParams) (db.EmailOutbox, error)
}

// OutboxNotifier queues email and nudges the relay after the save commits.
type OutboxNotifier struct {
	Notifier
	Queries   EmailSaver
	Publisher NudgePublisher
}

// Email queues a plain-text message for immediate relay.
func (n *OutboxNotifier) Email(ctx context.Context, to, subject, body string) error {
	return n.Send(ctx, Message{To: to, Subject: subject, Body: body})
}

// Send persists a message before publishing its best-effort delivery nudge.
func (n *OutboxNotifier) Send(ctx context.Context, m Message) error {
	var ics *string
	if m.ICS != "" {
		ics = &m.ICS
	}
	if _, err := n.Queries.EnqueueEmail(ctx, db.EnqueueEmailParams{ToEmail: m.To, Subject: m.Subject, Body: m.Body, Ics: ics}); err != nil {
		return err
	}
	Nudge(ctx, n.Publisher)
	return nil
}
