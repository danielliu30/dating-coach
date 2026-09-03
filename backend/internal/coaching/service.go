// Package coaching implements the human-coach features: coach directory,
// availability, and session scheduling for both sides of the relationship.
package coaching

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danielliu30/dating-coach/backend/internal/payments"
	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// Sentinel errors respondErr maps onto HTTP status codes.
var (
	ErrNotFound     = errors.New("not found")
	ErrForbidden    = errors.New("not allowed")
	ErrInvalidInput = errors.New("invalid input")
	ErrSlotTaken    = errors.New("slot is no longer available")
	ErrUnavailable  = errors.New("coach is not available then")
	ErrPayment      = errors.New("payment could not be started")
)

const (
	defaultSessionMinutes = 45
	slotStepMinutes       = 30
	minSessionMinutes     = 15
	maxSessionMinutes     = 240
	maxAvailabilityDays   = 30
)

// sessionStatuses are the states a booking may be moved to.
var sessionStatuses = map[string]bool{
	"scheduled": true,
	"completed": true,
	"cancelled": true,
	"no_show":   true,
}

// Service owns the coaching business rules: the coach directory and profiles,
// each coach's weekly availability, the bookable slots derived from it, and the
// lifecycle of a booked session (book, reschedule, status, notes).
type Service struct {
	pool     *pgxpool.Pool
	queries  *db.Queries
	payments payments.Provider
	// holdTTL is how long an unpaid booking keeps its slot.
	holdTTL time.Duration
	// appURL is where the payment provider sends the client back to.
	appURL string
}

// NewService wires the service dependencies; called once from cmd/api. provider
// decides whether bookings are paid: pass payments.Disabled{} to confirm
// sessions immediately.
func NewService(pool *pgxpool.Pool, queries *db.Queries, provider payments.Provider, holdTTL time.Duration, appURL string) *Service {
	return &Service{pool: pool, queries: queries, payments: provider, holdTTL: holdTTL, appURL: appURL}
}

// PaymentsEnabled reports whether booking a session requires paying for it.
func (s *Service) PaymentsEnabled() bool {
	return s.payments.Enabled()
}

// Coach is the public directory view of a coach profile.
type Coach struct {
	ID               string   `json:"id"`
	DisplayName      string   `json:"display_name"`
	Headline         string   `json:"headline"`
	Bio              string   `json:"bio"`
	Specialties      []string `json:"specialties"`
	HourlyRateCents  int32    `json:"hourly_rate_cents"`
	Timezone         string   `json:"timezone"`
	YearsExperience  int32    `json:"years_experience"`
	AcceptingClients bool     `json:"accepting_clients"`
}

// Session is the API view of a booking. CounterpartName is the other party's
// name and is only filled in by the list endpoints.
type Session struct {
	ID              string `json:"id"`
	UserID          string `json:"user_id"`
	CoachID         string `json:"coach_id"`
	CounterpartName string `json:"counterpart_name,omitempty"`
	ScheduledTime   string `json:"scheduled_time"`
	DurationMinutes int32  `json:"duration_minutes"`
	Status          string `json:"status"`
	Topic           string `json:"topic"`
	CoachNotes      string `json:"coach_notes,omitempty"`
	PaymentStatus   string `json:"payment_status"`
	AmountCents     int32  `json:"amount_cents"`
	Currency        string `json:"currency"`
	HoldExpiresAt   string `json:"hold_expires_at,omitempty"`
	// CheckoutURL is only set on the response to a booking that must be paid
	// for; it is where the client completes the payment.
	CheckoutURL string `json:"checkout_url,omitempty"`
}

// Slot is one bookable start time offered to clients.
type Slot struct {
	Start           string `json:"start"`
	DurationMinutes int32  `json:"duration_minutes"`
}

// AvailabilityWindow is a recurring weekly window in the coach's own timezone,
// expressed as minutes from midnight.
type AvailabilityWindow struct {
	Weekday     int16 `json:"weekday"`
	StartMinute int32 `json:"start_minute"`
	EndMinute   int32 `json:"end_minute"`
}

// sessionOf projects a session row onto the API shape.
func sessionOf(s db.CoachingSession, counterpart string) Session {
	out := Session{
		ID:              s.ID.String(),
		UserID:          s.UserID.String(),
		CoachID:         s.CoachID.String(),
		CounterpartName: counterpart,
		ScheduledTime:   s.ScheduledTime.UTC().Format(time.RFC3339),
		DurationMinutes: s.DurationMinutes,
		Status:          s.Status,
		Topic:           s.Topic,
		CoachNotes:      s.CoachNotes,
		PaymentStatus:   s.PaymentStatus,
		AmountCents:     s.AmountCents,
		Currency:        s.Currency,
	}
	out.HoldExpiresAt = formatHold(s.HoldExpiresAt)
	return out
}

// formatHold renders a nullable hold deadline as RFC3339, or "" when unset.
func formatHold(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// priceCents is what a session of duration minutes costs at the coach's hourly
// rate, rounded to the nearest cent.
func priceCents(hourlyRateCents, duration int32) int64 {
	return (int64(hourlyRateCents)*int64(duration) + 30) / 60
}

// ListCoaches returns a page of the coach directory.
func (s *Service) ListCoaches(ctx context.Context, limit, offset int32, acceptingOnly bool) ([]Coach, error) {
	rows, err := s.queries.ListCoaches(ctx, db.ListCoachesParams{
		Limit:         limit,
		Offset:        offset,
		AcceptingOnly: &acceptingOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("list coaches: %w", err)
	}
	out := make([]Coach, 0, len(rows))
	for _, row := range rows {
		out = append(out, Coach{
			ID:               row.UserID.String(),
			DisplayName:      row.DisplayName,
			Headline:         row.Headline,
			Bio:              row.Bio,
			Specialties:      row.Specialties,
			HourlyRateCents:  row.HourlyRateCents,
			Timezone:         row.Timezone,
			YearsExperience:  row.YearsExperience,
			AcceptingClients: row.AcceptingClients,
		})
	}
	return out, nil
}

// GetCoach returns one coach profile, or ErrNotFound when the user has none.
func (s *Service) GetCoach(ctx context.Context, coachID uuid.UUID) (Coach, error) {
	row, err := s.queries.GetCoach(ctx, coachID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Coach{}, ErrNotFound
		}
		return Coach{}, fmt.Errorf("get coach: %w", err)
	}
	return Coach{
		ID:               row.UserID.String(),
		DisplayName:      row.DisplayName,
		Headline:         row.Headline,
		Bio:              row.Bio,
		Specialties:      row.Specialties,
		HourlyRateCents:  row.HourlyRateCents,
		Timezone:         row.Timezone,
		YearsExperience:  row.YearsExperience,
		AcceptingClients: row.AcceptingClients,
	}, nil
}

// UpsertProfileInput is the decoded PUT /coach/profile body.
type UpsertProfileInput struct {
	Headline         string   `json:"headline"`
	Bio              string   `json:"bio"`
	Specialties      []string `json:"specialties"`
	HourlyRateCents  int32    `json:"hourly_rate_cents"`
	Timezone         string   `json:"timezone"`
	YearsExperience  int32    `json:"years_experience"`
	AcceptingClients bool     `json:"accepting_clients"`
}

// UpsertProfile creates or replaces the caller's coach profile. The timezone is
// validated here because every slot calculation is done in it.
func (s *Service) UpsertProfile(ctx context.Context, coachID uuid.UUID, in UpsertProfileInput) (Coach, error) {
	if in.Timezone == "" {
		in.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil {
		return Coach{}, fmt.Errorf("%w: unknown timezone %q", ErrInvalidInput, in.Timezone)
	}
	if in.Specialties == nil {
		in.Specialties = []string{}
	}
	row, err := s.queries.UpsertCoachProfile(ctx, db.UpsertCoachProfileParams{
		UserID:           coachID,
		Headline:         in.Headline,
		Bio:              in.Bio,
		Specialties:      in.Specialties,
		HourlyRateCents:  in.HourlyRateCents,
		Timezone:         in.Timezone,
		YearsExperience:  in.YearsExperience,
		AcceptingClients: in.AcceptingClients,
	})
	if err != nil {
		return Coach{}, fmt.Errorf("upsert coach profile: %w", err)
	}
	return s.GetCoach(ctx, row.UserID)
}

// SetAvailability replaces the coach's whole weekly schedule with windows.
func (s *Service) SetAvailability(ctx context.Context, coachID uuid.UUID, windows []AvailabilityWindow) ([]AvailabilityWindow, error) {
	for _, w := range windows {
		if w.Weekday < 0 || w.Weekday > 6 || w.StartMinute < 0 || w.EndMinute <= w.StartMinute || w.EndMinute > 1440 {
			return nil, fmt.Errorf("%w: availability window out of range", ErrInvalidInput)
		}
	}
	// Delete-then-insert in one transaction, so a failing insert cannot leave the
	// coach with partial or empty availability.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := s.queries.WithTx(tx)
	if err := q.ReplaceCoachAvailability(ctx, coachID); err != nil {
		return nil, fmt.Errorf("clear availability: %w", err)
	}
	for _, w := range windows {
		if _, err := q.AddCoachAvailability(ctx, db.AddCoachAvailabilityParams{
			CoachID:     coachID,
			Weekday:     w.Weekday,
			StartMinute: w.StartMinute,
			EndMinute:   w.EndMinute,
		}); err != nil {
			return nil, fmt.Errorf("add availability: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit availability: %w", err)
	}
	return s.ListAvailability(ctx, coachID)
}

// ListAvailability returns the coach's published weekly windows.
func (s *Service) ListAvailability(ctx context.Context, coachID uuid.UUID) ([]AvailabilityWindow, error) {
	rows, err := s.queries.ListCoachAvailability(ctx, coachID)
	if err != nil {
		return nil, fmt.Errorf("list availability: %w", err)
	}
	out := make([]AvailabilityWindow, 0, len(rows))
	for _, row := range rows {
		out = append(out, AvailabilityWindow{Weekday: row.Weekday, StartMinute: row.StartMinute, EndMinute: row.EndMinute})
	}
	return out, nil
}

// OpenSlots expands the coach's weekly availability into concrete slots between
// from and to, dropping anything that overlaps an already booked session.
// excludeSessionID ignores one of the actor's own sessions, so a reschedule can
// offer times that overlap the slot being moved.
func (s *Service) OpenSlots(ctx context.Context, coachID, actorID uuid.UUID, from, to time.Time, durationMinutes int32, excludeSessionID *uuid.UUID) ([]Slot, error) {
	if excludeSessionID != nil {
		session, err := s.participant(ctx, *excludeSessionID, actorID)
		if err != nil {
			return nil, err
		}
		if session.CoachID != coachID {
			return nil, fmt.Errorf("%w: session belongs to another coach", ErrInvalidInput)
		}
	}
	if durationMinutes <= 0 {
		durationMinutes = defaultSessionMinutes
	}
	if !to.After(from) {
		return nil, fmt.Errorf("%w: 'to' must be after 'from'", ErrInvalidInput)
	}
	if to.Sub(from) > maxAvailabilityDays*24*time.Hour {
		return nil, fmt.Errorf("%w: range cannot exceed %d days", ErrInvalidInput, maxAvailabilityDays)
	}

	coach, err := s.GetCoach(ctx, coachID)
	if err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(coach.Timezone)
	if err != nil {
		loc = time.UTC
	}

	windows, err := s.ListAvailability(ctx, coachID)
	if err != nil {
		return nil, err
	}
	booked, err := s.queries.ListBookedSlots(ctx, db.ListBookedSlotsParams{
		// A session starting before the range can still run into it.
		CoachID:         coachID,
		ScheduledTime:   from.Add(-maxSessionMinutes * time.Minute),
		ScheduledTime_2: to,
	})
	if err != nil {
		return nil, fmt.Errorf("list booked slots: %w", err)
	}

	byWeekday := map[int16][]AvailabilityWindow{}
	for _, w := range windows {
		byWeekday[w.Weekday] = append(byWeekday[w.Weekday], w)
	}

	slots := []Slot{}
	day := time.Date(from.In(loc).Year(), from.In(loc).Month(), from.In(loc).Day(), 0, 0, 0, 0, loc)
	for !day.After(to.In(loc)) {
		for _, w := range byWeekday[int16(day.Weekday())] {
			for minute := w.StartMinute; minute+durationMinutes <= w.EndMinute; minute += slotStepMinutes {
				// Wall-clock construction, so a DST transition inside the day
				// does not shift slots out of the published window.
				start := time.Date(day.Year(), day.Month(), day.Day(), int(minute/60), int(minute%60), 0, 0, loc)
				if wallMinute(start, loc) != minute {
					// Nonexistent local time (spring forward); the window
					// resumes after the gap.
					continue
				}
				if start.Before(from) || !start.Before(to) {
					continue
				}
				if overlapsBooked(start, durationMinutes, booked, excludeSessionID) {
					continue
				}
				slots = append(slots, Slot{Start: start.UTC().Format(time.RFC3339), DurationMinutes: durationMinutes})
			}
		}
		day = day.AddDate(0, 0, 1)
	}
	return slots, nil
}

// wallMinute is the local minute-of-day t actually lands on, which differs from
// the requested one when the time does not exist in loc (spring forward).
func wallMinute(t time.Time, loc *time.Location) int32 {
	local := t.In(loc)
	return int32(local.Hour()*60 + local.Minute())
}

// overlapsBooked reports whether the interval collides with an existing session,
// optionally ignoring the one being rescheduled.
func overlapsBooked(start time.Time, durationMinutes int32, booked []db.ListBookedSlotsRow, exclude *uuid.UUID) bool {
	end := start.Add(time.Duration(durationMinutes) * time.Minute)
	for _, b := range booked {
		if exclude != nil && b.ID == *exclude {
			continue
		}
		bookedEnd := b.ScheduledTime.Add(time.Duration(b.DurationMinutes) * time.Minute)
		if start.Before(bookedEnd) && b.ScheduledTime.Before(end) {
			return true
		}
	}
	return false
}

// BookInput is the decoded POST /coaching/sessions body.
type BookInput struct {
	CoachID         string `json:"coach_id"`
	ScheduledTime   string `json:"scheduled_time"`
	DurationMinutes int32  `json:"duration_minutes"`
	Topic           string `json:"topic"`
}

// BookSession books a slot with a coach after checking that it is in the future,
// inside the published availability and still free. With payments enabled the
// session is created as pending_payment, holding the slot until the client pays
// or the hold lapses, and the returned Session carries the CheckoutURL; a
// checkout that cannot be started releases the hold and returns ErrPayment.
func (s *Service) BookSession(ctx context.Context, userID uuid.UUID, in BookInput) (Session, error) {
	coachID, err := uuid.Parse(in.CoachID)
	if err != nil {
		return Session{}, fmt.Errorf("%w: coach_id must be a uuid", ErrInvalidInput)
	}
	scheduled, err := time.Parse(time.RFC3339, in.ScheduledTime)
	if err != nil {
		return Session{}, fmt.Errorf("%w: scheduled_time must be RFC3339", ErrInvalidInput)
	}
	if scheduled.Before(time.Now()) {
		return Session{}, fmt.Errorf("%w: scheduled_time is in the past", ErrInvalidInput)
	}
	duration := in.DurationMinutes
	if duration <= 0 {
		duration = defaultSessionMinutes
	}
	if err := s.assertBookable(ctx, coachID, scheduled, duration, true, nil); err != nil {
		return Session{}, err
	}
	if s.payments.Enabled() {
		return s.bookPaid(ctx, userID, coachID, scheduled, duration, in.Topic)
	}

	session, err := s.queries.CreateCoachingSession(ctx, db.CreateCoachingSessionParams{
		UserID:          userID,
		CoachID:         coachID,
		ScheduledTime:   scheduled,
		DurationMinutes: duration,
		Topic:           in.Topic,
	})
	if err != nil {
		if isSlotConflict(err) {
			return Session{}, ErrSlotTaken
		}
		return Session{}, fmt.Errorf("create session: %w", err)
	}
	return sessionOf(session, ""), nil
}

// bookPaid inserts the pending_payment hold and starts the provider checkout
// for it. The hold is written first so the slot is taken before the client is
// sent to pay. If the checkout cannot be started, or its reference cannot be
// stored, the hold is cancelled and the checkout expired again so nothing
// payable is left behind; the checkout's own ExpiresAt bounds the damage if
// that clean-up fails too.
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
	session, err = s.queries.SetSessionPaymentRef(ctx, db.SetSessionPaymentRefParams{ID: session.ID, PaymentRef: &checkout.Ref})
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
	out := sessionOf(session, "")
	out.CheckoutURL = checkout.URL
	return out, nil
}

// ApplyPaymentEvent records a provider webhook event and moves the matching
// session accordingly, all in one transaction with the session row locked so
// it cannot race the hold sweeper. EventPaid confirms a pending_payment hold;
// if the hold was already released it reinstates the session when the slot is
// still free and otherwise marks the payment refund_due for SweepPayments to
// refund, so a charged customer is never silently dropped. EventExpired
// cancels a pending hold. A redelivered event (same ID) is a no-op, so the
// provider may retry freely.
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
	case payments.EventPaid:
		if session.Status == "pending_payment" {
			_, err = q.MarkSessionPaid(ctx, session.ID)
		} else {
			err = reinstateOrRefund(ctx, tx, q, session.ID)
		}
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

// reinstateOrRefund handles a payment that arrived after the session's hold was
// released. It tries to schedule the session again; if the slot has since been
// taken (exclusion constraint) it marks the payment refund_due instead. The
// reinstate runs in a savepoint so the conflict does not abort tx. A session
// that is not cancelled-unpaid (already paid, refunded, ...) is left alone.
func reinstateOrRefund(ctx context.Context, tx pgx.Tx, q *db.Queries, sessionID uuid.UUID) error {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("savepoint: %w", err)
	}
	if _, err := q.WithTx(sp).ReinstatePaidSession(ctx, sessionID); err != nil {
		_ = sp.Rollback(ctx)
		if !isSlotConflict(err) {
			return fmt.Errorf("reinstate session: %w", err)
		}
		if _, err := q.MarkPaymentRefundDue(ctx, sessionID); err != nil {
			return fmt.Errorf("mark refund due: %w", err)
		}
		return nil
	}
	return sp.Commit(ctx)
}

// SweepResult counts what one SweepPayments pass did.
type SweepResult struct {
	HoldsReleased    int
	CheckoutsExpired int
	RefundsIssued    int
}

// SweepPayments is the periodic reconciliation pass for paid bookings. It
// releases pending_payment holds that lapsed before now (freeing their slots),
// then works through all provider clean-up still owed: expiring the checkout
// of every released hold so a late payment is refused, and refunding every
// payment marked refund_due. Provider calls are retried on the next pass if
// they fail, because a row only leaves 'expiring'/'refund_due' once the
// provider call succeeded. It returns the counts and the first provider error.
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

	refunds, err := s.queries.ListRefundsDue(ctx)
	if err != nil {
		return res, fmt.Errorf("list refunds due: %w", err)
	}
	for _, session := range refunds {
		if err := s.payments.Refund(ctx, *session.PaymentRef); err != nil {
			return res, fmt.Errorf("refund %s: %w", *session.PaymentRef, err)
		}
		if _, err := s.queries.MarkSessionRefunded(ctx, session.ID); err != nil {
			return res, fmt.Errorf("mark refunded: %w", err)
		}
		res.RefundsIssued++
	}
	return res, nil
}

// assertBookable rejects a requested interval that the coach has not published
// availability for, or that collides with an existing session. The exclusion
// constraint on coaching_sessions is the authoritative race-safe check; this
// gives callers a precise error instead of a bare conflict.
func (s *Service) assertBookable(ctx context.Context, coachID uuid.UUID, start time.Time, duration int32, requireAccepting bool, excludeSessionID *uuid.UUID) error {
	if duration < minSessionMinutes || duration > maxSessionMinutes {
		return fmt.Errorf("%w: duration must be between %d and %d minutes", ErrInvalidInput, minSessionMinutes, maxSessionMinutes)
	}

	coach, err := s.GetCoach(ctx, coachID)
	if err != nil {
		return err
	}
	if requireAccepting && !coach.AcceptingClients {
		return fmt.Errorf("%w: coach is not accepting clients", ErrUnavailable)
	}

	windows, err := s.ListAvailability(ctx, coachID)
	if err != nil {
		return err
	}
	loc, err := time.LoadLocation(coach.Timezone)
	if err != nil {
		loc = time.UTC
	}
	local := start.In(loc)
	startMinute := int32(local.Hour()*60 + local.Minute())
	insideWindow := false
	for _, w := range windows {
		if w.Weekday == int16(local.Weekday()) && startMinute >= w.StartMinute && startMinute+duration <= w.EndMinute {
			insideWindow = true
			break
		}
	}
	if !insideWindow {
		return fmt.Errorf("%w: %s is outside the coach's published availability", ErrUnavailable, start.UTC().Format(time.RFC3339))
	}

	end := start.Add(time.Duration(duration) * time.Minute)
	booked, err := s.queries.ListBookedSlots(ctx, db.ListBookedSlotsParams{
		CoachID:         coachID,
		ScheduledTime:   start.Add(-maxSessionMinutes * time.Minute),
		ScheduledTime_2: end,
	})
	if err != nil {
		return fmt.Errorf("list booked slots: %w", err)
	}
	if overlapsBooked(start, duration, booked, excludeSessionID) {
		return ErrSlotTaken
	}
	return nil
}

// ListForUser returns a client's sessions, optionally filtered by status.
func (s *Service) ListForUser(ctx context.Context, userID uuid.UUID, status *string) ([]Session, error) {
	rows, err := s.queries.ListSessionsForUser(ctx, db.ListSessionsForUserParams{UserID: userID, Status: status})
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	out := make([]Session, 0, len(rows))
	for _, row := range rows {
		out = append(out, Session{
			ID:              row.ID.String(),
			UserID:          row.UserID.String(),
			CoachID:         row.CoachID.String(),
			CounterpartName: row.CoachName,
			ScheduledTime:   row.ScheduledTime.UTC().Format(time.RFC3339),
			DurationMinutes: row.DurationMinutes,
			Status:          row.Status,
			Topic:           row.Topic,
			CoachNotes:      row.CoachNotes,
			PaymentStatus:   row.PaymentStatus,
			AmountCents:     row.AmountCents,
			Currency:        row.Currency,
			HoldExpiresAt:   formatHold(row.HoldExpiresAt),
		})
	}
	return out, nil
}

// ListForCoach returns a coach's sessions for the dashboard, optionally
// filtered by status and start time.
func (s *Service) ListForCoach(ctx context.Context, coachID uuid.UUID, status *string, fromTime *time.Time) ([]Session, error) {
	rows, err := s.queries.ListSessionsForCoach(ctx, db.ListSessionsForCoachParams{
		CoachID:  coachID,
		Status:   status,
		FromTime: fromTime,
	})
	if err != nil {
		return nil, fmt.Errorf("list coach sessions: %w", err)
	}
	out := make([]Session, 0, len(rows))
	for _, row := range rows {
		out = append(out, Session{
			ID:              row.ID.String(),
			UserID:          row.UserID.String(),
			CoachID:         row.CoachID.String(),
			CounterpartName: row.UserName,
			ScheduledTime:   row.ScheduledTime.UTC().Format(time.RFC3339),
			DurationMinutes: row.DurationMinutes,
			Status:          row.Status,
			Topic:           row.Topic,
			CoachNotes:      row.CoachNotes,
			PaymentStatus:   row.PaymentStatus,
			AmountCents:     row.AmountCents,
			Currency:        row.Currency,
			HoldExpiresAt:   formatHold(row.HoldExpiresAt),
		})
	}
	return out, nil
}

// participant checks that the caller owns the session as user or coach.
func (s *Service) participant(ctx context.Context, sessionID, userID uuid.UUID) (db.CoachingSession, error) {
	session, err := s.queries.GetCoachingSession(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.CoachingSession{}, ErrNotFound
		}
		return db.CoachingSession{}, fmt.Errorf("get session: %w", err)
	}
	if session.UserID != userID && session.CoachID != userID {
		return db.CoachingSession{}, ErrForbidden
	}
	return session, nil
}

// SetStatus moves a session to another status; either participant may call it,
// which is how client-side cancellation is implemented.
func (s *Service) SetStatus(ctx context.Context, sessionID, actorID uuid.UUID, status string) (Session, error) {
	if !sessionStatuses[status] {
		return Session{}, fmt.Errorf("%w: unknown status %q", ErrInvalidInput, status)
	}
	session, err := s.participant(ctx, sessionID, actorID)
	if err != nil {
		return Session{}, err
	}
	// Only a confirmed payment moves a hold forward; participants may just
	// abandon it.
	if session.Status == "pending_payment" && status != "cancelled" {
		return Session{}, fmt.Errorf("%w: session is awaiting payment", ErrInvalidInput)
	}
	updated, err := s.queries.UpdateSessionStatus(ctx, db.UpdateSessionStatusParams{ID: sessionID, Status: status})
	if err != nil {
		return Session{}, fmt.Errorf("update session status: %w", err)
	}
	return sessionOf(updated, ""), nil
}

// Reschedule moves a session to a new start time, keeping its duration. The
// session being moved is excluded from the conflict check, and the coach need
// not still be accepting new clients.
func (s *Service) Reschedule(ctx context.Context, sessionID, actorID uuid.UUID, scheduledTime string) (Session, error) {
	scheduled, err := time.Parse(time.RFC3339, scheduledTime)
	if err != nil {
		return Session{}, fmt.Errorf("%w: scheduled_time must be RFC3339", ErrInvalidInput)
	}
	if scheduled.Before(time.Now()) {
		return Session{}, fmt.Errorf("%w: scheduled_time is in the past", ErrInvalidInput)
	}
	session, err := s.participant(ctx, sessionID, actorID)
	if err != nil {
		return Session{}, err
	}
	if err := s.assertBookable(ctx, session.CoachID, scheduled, session.DurationMinutes, false, &sessionID); err != nil {
		return Session{}, err
	}
	updated, err := s.queries.RescheduleSession(ctx, db.RescheduleSessionParams{ID: sessionID, ScheduledTime: scheduled})
	if err != nil {
		if isSlotConflict(err) {
			return Session{}, ErrSlotTaken
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return Session{}, ErrNotFound
		}
		return Session{}, fmt.Errorf("reschedule session: %w", err)
	}
	return sessionOf(updated, ""), nil
}

// SetNotes stores the coach's private notes; only the session's coach may.
func (s *Service) SetNotes(ctx context.Context, sessionID, coachID uuid.UUID, notes string) (Session, error) {
	session, err := s.participant(ctx, sessionID, coachID)
	if err != nil {
		return Session{}, err
	}
	if session.CoachID != coachID {
		return Session{}, ErrForbidden
	}
	updated, err := s.queries.UpdateSessionNotes(ctx, db.UpdateSessionNotesParams{ID: sessionID, CoachNotes: notes})
	if err != nil {
		return Session{}, fmt.Errorf("update notes: %w", err)
	}
	return sessionOf(updated, ""), nil
}
