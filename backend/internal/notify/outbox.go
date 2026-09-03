package notify

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// RelayInterval is how often the relay looks for emails that still have to be
// delivered; it is the delay a recipient sees on a normal send.
const RelayInterval = 5 * time.Second

// MaxAttempts is how many times one email is tried before the relay leaves it
// in the outbox for an operator: an address the SMTP server keeps rejecting must
// not be retried forever. With retryDelay's backoff the attempts span more than
// a day, so an SMTP outage of hours loses nothing.
const MaxAttempts = 32

// Retry backoff bounds: the first retry comes after minRetryDelay, each further
// one doubles, capped at maxRetryDelay.
const (
	minRetryDelay = 10 * time.Second
	maxRetryDelay = time.Hour
)

// relayBatch bounds one pass so a backlog after an SMTP outage is drained in
// chunks instead of one long-running transaction-free loop.
const relayBatch = 100

// Relay delivers the emails recorded in email_outbox. Services write the row in
// the same transaction as the change it announces, so once that commit succeeds
// the email is certain to be attempted even when SMTP is down at the time.
type Relay struct {
	queries  *db.Queries
	notifier Notifier
}

// NewRelay builds the outbox relay; called once from cmd/worker.
func NewRelay(queries *db.Queries, notifier Notifier) *Relay {
	return &Relay{queries: queries, notifier: notifier}
}

// Run drains the outbox every RelayInterval until ctx is cancelled, returning
// nil on cancellation. A pass that fails is logged rather than returned: the
// next tick retries it.
func (r *Relay) Run(ctx context.Context) error {
	ticker := time.NewTicker(RelayInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := r.Drain(ctx); err != nil {
				slog.Error("deliver queued emails", "error", err)
			}
		}
	}
}

// Drain attempts up to relayBatch queued emails and reports how many were
// delivered. A failed send is recorded on its row and retried on a later pass,
// up to MaxAttempts; only a database error stops the pass.
func (r *Relay) Drain(ctx context.Context) (int, error) {
	pending, err := r.queries.PendingEmails(ctx, db.PendingEmailsParams{Limit: relayBatch, Attempts: MaxAttempts})
	if err != nil {
		return 0, fmt.Errorf("read email outbox: %w", err)
	}
	sent := 0
	for _, row := range pending {
		msg := Message{To: row.ToEmail, Subject: row.Subject, Body: row.Body}
		if row.Ics != nil {
			msg.ICS = *row.Ics
		}
		if err := r.notifier.Send(ctx, msg); err != nil {
			slog.Error("send queued email", "id", row.ID, "to", row.ToEmail, "attempt", row.Attempts+1, "error", err)
			if _, err := r.queries.MarkEmailFailed(ctx, db.MarkEmailFailedParams{
				ID:         row.ID,
				LastError:  err.Error(),
				RetryAfter: interval(retryDelay(row.Attempts)),
			}); err != nil {
				return sent, fmt.Errorf("record failed email %s: %w", row.ID, err)
			}
			continue
		}
		if _, err := r.queries.MarkEmailSent(ctx, row.ID); err != nil {
			return sent, fmt.Errorf("mark email %s sent: %w", row.ID, err)
		}
		sent++
	}
	return sent, nil
}

// retryDelay is how long to wait before the next attempt after the given number
// of failed ones: minRetryDelay doubled per failure, capped at maxRetryDelay.
func retryDelay(failed int32) time.Duration {
	d := minRetryDelay
	for i := int32(0); i < failed && d < maxRetryDelay; i++ {
		d *= 2
	}
	return min(d, maxRetryDelay)
}

// interval converts d to the Postgres interval type sqlc expects.
func interval(d time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: d.Microseconds(), Valid: true}
}
