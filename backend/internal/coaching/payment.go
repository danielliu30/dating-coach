package coaching

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/danielliu30/dating-coach/backend/internal/payments"
	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// priceCents is what a session of duration minutes costs at the coach's hourly
// rate, rounded to the nearest cent.
func priceCents(hourlyRateCents, duration int32) int64 {
	return (int64(hourlyRateCents)*int64(duration) + 30) / 60
}

// bookPaid inserts the pending_payment hold and starts the provider checkout
// for it. The hold is written first so the slot is taken before the client is
// sent to authorise their card; the coach is not asked until that succeeded
// (see ApplyPaymentEvent). If the checkout cannot be started, or its reference
// cannot be stored, the hold is released again so no dangling checkout that is
// still payable is left behind; the checkout's own ExpiresAt bounds the damage
// if that clean-up fails too.
func (s *Service) bookPaid(ctx context.Context, userID, coachID uuid.UUID, scheduled time.Time, duration int32, topic string) (Session, error) {
	coach, err := s.queries.GetCoach(ctx, coachID)
	if err != nil {
		return Session{}, fmt.Errorf("get coach: %w", err)
	}
	user, err := s.queries.GetUserByID(ctx, userID)
	if err != nil {
		return Session{}, fmt.Errorf("get user: %w", err)
	}
	amount := priceCents(coach.HourlyRateCents, duration)
	if amount < 0 || amount > math.MaxInt32 {
		return Session{}, fmt.Errorf("%w: coach rate yields an unpayable amount", ErrInvalidInput)
	}
	holdUntil := time.Now().Add(s.holdTTL)

	session, err := s.queries.CreatePendingPaymentSession(ctx, db.CreatePendingPaymentSessionParams{
		UserID:          userID,
		CoachID:         coachID,
		ScheduledTime:   scheduled,
		DurationMinutes: duration,
		Topic:           topic,
		AmountCents:     int32(amount),
		Currency:        "usd",
		HoldExpiresAt:   &holdUntil,
	})
	if err != nil {
		if isSlotConflict(err) {
			return Session{}, ErrSlotTaken
		}
		return Session{}, fmt.Errorf("create pending session: %w", err)
	}

	checkout, err := s.payments.CreateCheckout(ctx, payments.CheckoutInput{
		SessionID:     session.ID.String(),
		AmountCents:   amount,
		Currency:      "usd",
		Description:   fmt.Sprintf("%d min coaching session with %s", duration, coach.DisplayName),
		CustomerEmail: user.Email,
		ExpiresAt:     holdUntil,
		SuccessURL:    s.appURL + "/?payment=success&session=" + session.ID.String(),
		CancelURL:     s.appURL + "/?payment=cancelled&session=" + session.ID.String(),
	})
	if err != nil {
		if _, cancelErr := s.queries.CancelPendingPaymentSession(ctx, session.ID); cancelErr != nil {
			return Session{}, fmt.Errorf("release hold after checkout failure (%v): %w", err, cancelErr)
		}
		return Session{}, fmt.Errorf("%w: %v", ErrPayment, err)
	}
	session, err = s.queries.AttachCheckout(ctx, db.AttachCheckoutParams{ID: session.ID, PaymentRef: &checkout.Ref})
	if err != nil {
		err = fmt.Errorf("store payment ref: %w", err)
		if expireErr := s.payments.ExpireCheckout(ctx, checkout.Ref); expireErr != nil {
			err = fmt.Errorf("%w; expire orphaned checkout %s: %v", err, checkout.Ref, expireErr)
		}
		if _, cancelErr := s.queries.CancelPendingPaymentSession(ctx, session.ID); cancelErr != nil {
			err = fmt.Errorf("%w; release hold: %v", err, cancelErr)
		}
		return Session{}, err
	}
	if session.Status != StatusPendingPayment {
		// The hold lapsed (or was cancelled) while the checkout was being
		// created; it is now recorded as 'expiring', so the sweeper will close
		// it even if this immediate attempt fails.
		if err := s.payments.ExpireCheckout(ctx, checkout.Ref); err == nil {
			_, _ = s.queries.MarkCheckoutExpired(ctx, session.ID)
		}
		return Session{}, fmt.Errorf("%w: hold lapsed before checkout was ready", ErrPayment)
	}
	out := sessionOf(session, "")
	out.CheckoutURL = checkout.URL
	return out, nil
}

// ApplyPaymentEvent records a provider webhook event and moves the matching
// session accordingly, all in one transaction with the session row locked so
// it cannot race the hold sweeper or a participant's cancel. EventAuthorized
// turns a pending_payment hold into a pending request: the coach is emailed
// the confirm/decline links and the client told the request was sent, exactly
// as an unpaid booking would have been at BookSession. If the hold had already
// lapsed the request is revived when the slot is still free; otherwise, or if
// the participant had cancelled, the authorisation is marked for release so
// the client is never charged. EventExpired cancels a pending hold. A
// redelivered event (same ID) is a no-op, so the provider may retry freely.
func (s *Service) ApplyPaymentEvent(ctx context.Context, ev payments.Event) error {
	if ev.Kind == payments.EventIgnored || ev.CheckoutRef == "" {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	session, err := q.GetSessionByPaymentRefForUpdate(ctx, &ev.CheckoutRef)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lookup session by payment ref: %w", err)
	}
	inserted, err := q.RecordPaymentEvent(ctx, db.RecordPaymentEventParams{
		ProviderEventID: ev.ID,
		SessionID:       &session.ID,
		EventType:       string(ev.Kind),
	})
	if err != nil {
		return fmt.Errorf("record payment event: %w", err)
	}
	if inserted == 0 {
		return nil
	}

	switch ev.Kind {
	case payments.EventAuthorized:
		err = s.authorized(ctx, tx, q, session)
	case payments.EventExpired:
		_, err = q.CancelPendingPaymentSession(ctx, session.ID)
	}
	if err != nil {
		return fmt.Errorf("apply %s: %w", ev.Kind, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit payment event: %w", err)
	}
	return nil
}

// authorized moves a session whose card was just held on to the coach: a
// pending_payment hold becomes a pending request, a hold the sweeper timed out
// is revived if its slot is still free (the reinstate runs in a savepoint so a
// slot conflict does not abort tx), and anything else, including a slot that
// has since been taken or a deliberately cancelled hold, has its
// authorisation marked for release. It queues the request emails when the
// coach is asked.
func (s *Service) authorized(ctx context.Context, tx pgx.Tx, q *db.Queries, session db.CoachingSession) error {
	token, err := randomToken()
	if err != nil {
		return err
	}
	respondBy := respondDeadline(time.Now(), session.ScheduledTime)
	tokenHash := hashToken(token)

	var requested bool
	switch session.Status {
	case StatusPendingPayment:
		if _, err := q.AuthorizeSession(ctx, db.AuthorizeSessionParams{ID: session.ID, ConfirmationToken: &tokenHash, RespondBy: &respondBy}); err != nil {
			return fmt.Errorf("authorize session: %w", err)
		}
		requested = true
	case StatusExpired:
		sp, err := tx.Begin(ctx)
		if err != nil {
			return fmt.Errorf("savepoint: %w", err)
		}
		_, err = q.WithTx(sp).ReinstateAuthorizedSession(ctx, db.ReinstateAuthorizedSessionParams{ID: session.ID, ConfirmationToken: &tokenHash, RespondBy: &respondBy})
		switch {
		case err == nil:
			if err := sp.Commit(ctx); err != nil {
				return fmt.Errorf("commit savepoint: %w", err)
			}
			requested = true
		case errors.Is(err, pgx.ErrNoRows), isSlotConflict(err):
			_ = sp.Rollback(ctx)
		default:
			_ = sp.Rollback(ctx)
			return fmt.Errorf("reinstate session: %w", err)
		}
	}
	if !requested {
		if _, err := q.MarkAuthorizationToRelease(ctx, session.ID); err != nil {
			return fmt.Errorf("mark authorization to release: %w", err)
		}
		return nil
	}
	parties, err := q.GetSessionParties(ctx, session.ID)
	if err != nil {
		return fmt.Errorf("load session parties: %w", err)
	}
	if err := s.mail.coachRequest(ctx, q, parties, token, false); err != nil {
		return err
	}
	return s.mail.clientRequested(ctx, q, parties)
}

// capture charges the card held for session, which the caller has just moved
// to scheduled inside q's transaction, and records the payment as paid. The
// session ID is the idempotency key, so a retry after a failed commit does not
// charge twice. A provider failure surfaces as ErrPayment and the caller rolls
// the confirmation back, leaving the request pending for the coach to retry.
func (s *Service) capture(ctx context.Context, q *db.Queries, session db.CoachingSession) error {
	if session.PaymentRef == nil {
		return fmt.Errorf("%w: authorised session %s has no payment reference", ErrPayment, session.ID)
	}
	if err := s.payments.Capture(ctx, *session.PaymentRef, session.ID.String()); err != nil {
		return fmt.Errorf("%w: capture %s: %v", ErrPayment, *session.PaymentRef, err)
	}
	if _, err := q.MarkSessionCaptured(ctx, session.ID); err != nil {
		return fmt.Errorf("mark captured: %w", err)
	}
	return nil
}

// SweepResult counts what one SweepPayments pass did.
type SweepResult struct {
	HoldsReleased       int
	CheckoutsExpired    int
	AuthorizationsFreed int
	RefundsIssued       int
}

// SweepPayments is the periodic reconciliation pass for paid bookings. It
// expires pending_payment holds that lapsed before now (freeing their slots),
// then works through all provider clean-up still owed: expiring the checkout
// of every lapsed hold so a late authorisation is refused, releasing the card
// hold of every declined, expired or cancelled request, and refunding every
// payment marked refund_due. Provider calls are retried on the next pass if
// they fail, because a row only leaves 'expiring'/'releasing'/'refund_due'
// once the provider call succeeded. It returns the counts and the first
// provider error.
func (s *Service) SweepPayments(ctx context.Context, now time.Time) (SweepResult, error) {
	var res SweepResult
	released, err := s.queries.ExpirePaymentHolds(ctx, &now)
	if err != nil {
		return res, fmt.Errorf("expire holds: %w", err)
	}
	res.HoldsReleased = int(released)

	toExpire, err := s.queries.ListCheckoutsToExpire(ctx)
	if err != nil {
		return res, fmt.Errorf("list checkouts to expire: %w", err)
	}
	for _, session := range toExpire {
		if err := s.payments.ExpireCheckout(ctx, *session.PaymentRef); err != nil {
			return res, fmt.Errorf("expire checkout %s: %w", *session.PaymentRef, err)
		}
		if _, err := s.queries.MarkCheckoutExpired(ctx, session.ID); err != nil {
			return res, fmt.Errorf("mark checkout expired: %w", err)
		}
		res.CheckoutsExpired++
	}

	toRelease, err := s.queries.ListAuthorizationsToRelease(ctx)
	if err != nil {
		return res, fmt.Errorf("list authorizations to release: %w", err)
	}
	for _, session := range toRelease {
		if err := s.payments.Release(ctx, *session.PaymentRef, session.ID.String()); err != nil {
			return res, fmt.Errorf("release authorization %s: %w", *session.PaymentRef, err)
		}
		if _, err := s.queries.MarkAuthorizationReleased(ctx, session.ID); err != nil {
			return res, fmt.Errorf("mark authorization released: %w", err)
		}
		res.AuthorizationsFreed++
	}

	refunds, err := s.queries.ListRefundsDue(ctx)
	if err != nil {
		return res, fmt.Errorf("list refunds due: %w", err)
	}
	for _, session := range refunds {
		if err := s.payments.Refund(ctx, *session.PaymentRef, session.ID.String()); err != nil {
			return res, fmt.Errorf("refund %s: %w", *session.PaymentRef, err)
		}
		if _, err := s.queries.MarkSessionRefunded(ctx, session.ID); err != nil {
			return res, fmt.Errorf("mark refunded: %w", err)
		}
		res.RefundsIssued++
	}
	return res, nil
}
