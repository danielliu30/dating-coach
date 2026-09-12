// Package coaching implements the human-coach features: coach directory,
// availability, and session scheduling for both sides of the relationship.
package coaching

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danielliu30/dating-coach/backend/internal/auth"
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
	ErrExpired      = errors.New("request has expired")
	ErrPayment      = errors.New("payment could not be processed")
)

// maxHourlyRateCents keeps priceCents inside amount_cents (int32) for the
// longest bookable session.
const maxHourlyRateCents = math.MaxInt32 / (maxSessionMinutes / 60)

const (
	defaultSessionMinutes = 45
	slotStepMinutes       = 30
	minSessionMinutes     = 15
	maxSessionMinutes     = 240
	maxAvailabilityDays   = 30

	// A coach has respondWindow to answer a request, but never later than
	// respondLeadTime before the session starts.
	respondWindow   = 24 * time.Hour
	respondLeadTime = 2 * time.Hour
)

// Session statuses. A booking starts pending and holds its slot; the coach
// confirms it to scheduled or declines it, or it expires unanswered. Scheduled
// sessions end completed, cancelled or no_show. With payments on, a booking
// starts one step earlier as pending_payment: it holds its slot while the
// client authorises their card, and only becomes pending once that succeeded.
const (
	StatusPendingPayment = "pending_payment"
	StatusPending        = "pending"
	StatusScheduled      = "scheduled"
	StatusDeclined       = "declined"
	StatusExpired        = "expired"
	StatusCompleted      = "completed"
	StatusCancelled      = "cancelled"
	StatusNoShow         = "no_show"
)

// statusTransitions lists, per current status, the statuses SetStatus may move
// a session to. Confirmation and decline go through Respond instead, and
// expiry through ExpirePending, so neither is reachable from here.
var statusTransitions = map[string]map[string]bool{
	StatusPendingPayment: {StatusCancelled: true},
	StatusPending:        {StatusCancelled: true},
	StatusScheduled:      {StatusCompleted: true, StatusCancelled: true, StatusNoShow: true},
}

// coachOnlyStatuses are the outcomes only the coach may record.
var coachOnlyStatuses = map[string]bool{StatusCompleted: true, StatusNoShow: true}

// Service owns the coaching business rules: the coach directory and profiles,
// each coach's weekly availability, the bookable slots derived from it, and the
// lifecycle of a booked session (request, confirm, reschedule, status, notes).
type Service struct {
	pool     *pgxpool.Pool
	queries  *db.Queries
	mail     mailer
	payments payments.Provider
	// holdTTL is how long a booking keeps its slot while the client is at the
	// payment page.
	holdTTL time.Duration
	// appURL is where the payment provider sends the client back to.
	appURL string
	// reviews condenses written reviews into strengths; nil means only the
	// deterministic recommendation line is produced.
	reviews ReviewSummarizer
}

// ReviewSummarizer is the ml-analyzer's review-summary capability as the
// coaching service needs it; analysis.MLClient satisfies it.
type ReviewSummarizer interface {
	SummarizeReviews(ctx context.Context, in ReviewSummaryRequest) (ReviewSummaryResult, error)
}

// ReviewSummaryRequest is what the summariser is given: the coach's name and
// each review's private rating plus written comment.
type ReviewSummaryRequest struct {
	CoachName string              `json:"coach_name"`
	Reviews   []ReviewSummaryItem `json:"reviews"`
}

// ReviewSummaryItem is one review as the summariser sees it.
type ReviewSummaryItem struct {
	Rating  int16  `json:"rating"`
	Comment string `json:"comment"`
}

// ReviewSummaryResult is the summariser's answer, exposed as-is by
// Service.ReviewSummary.
type ReviewSummaryResult struct {
	ModelVersion string   `json:"model_version"`
	Recommended  int32    `json:"recommended"`
	Total        int32    `json:"total"`
	Summary      string   `json:"summary"`
	Strengths    []string `json:"strengths"`
}

// NewService wires the service dependencies; called once from cmd/api and
// cmd/worker. provider decides whether bookings are paid: pass
// payments.Disabled{} to skip the card authorisation step. holdTTL is how long
// a pending_payment booking keeps its slot. appURL is the public app origin
// the confirmation links in coach emails and the payment return URLs point at
// and mailFrom the organizer address on calendar invites.
func NewService(pool *pgxpool.Pool, queries *db.Queries, provider payments.Provider, holdTTL time.Duration, appURL, mailFrom string, reviews ReviewSummarizer) *Service {
	return &Service{
		pool:     pool,
		queries:  queries,
		mail:     mailer{appURL: appURL, mailFrom: mailFrom},
		payments: provider,
		holdTTL:  holdTTL,
		appURL:   appURL,
		reviews:  reviews,
	}
}

// PaymentsEnabled reports whether booking a session requires authorising a
// card payment first.
func (s *Service) PaymentsEnabled() bool {
	return s.payments.Enabled()
}

// Coach is the public directory view of a coach profile.
type Coach struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name"`
	Headline    string   `json:"headline"`
	Bio         string   `json:"bio"`
	Specialties []string `json:"specialties"`
	// Phases are the dating-cycle stages the coach specialises in, drawn from
	// auth.DatingPhases.
	Phases           []string `json:"phases"`
	HourlyRateCents  int32    `json:"hourly_rate_cents"`
	Timezone         string   `json:"timezone"`
	YearsExperience  int32    `json:"years_experience"`
	AcceptingClients bool     `json:"accepting_clients"`
	// AvgRating is the mean of client ratings (1-5); 0 when ReviewCount is 0.
	AvgRating   float64 `json:"avg_rating"`
	ReviewCount int32   `json:"review_count"`
	// RecommendCount is how many reviews meet recommendThreshold; the client
	// shows this instead of the numeric rating.
	RecommendCount int32 `json:"recommend_count"`
}

// Review is a client's rating of a coach, written after a completed session.
type Review struct {
	ID           string `json:"id"`
	CoachID      string `json:"coach_id"`
	UserID       string `json:"user_id"`
	ReviewerName string `json:"reviewer_name"`
	SessionID    string `json:"session_id,omitempty"`
	Rating       int16  `json:"rating"`
	// Recommended is Rating >= recommendThreshold; what clients see.
	Recommended bool      `json:"recommended"`
	Comment     string    `json:"comment"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// CreateReviewInput is the client-supplied body of a review. SessionID is
// optional; when set it must name a completed session between the two parties.
type CreateReviewInput struct {
	SessionID *uuid.UUID `json:"session_id"`
	Rating    int16      `json:"rating"`
	Comment   string     `json:"comment"`
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
	// MeetingURL is the join link the coach set for the session, if any.
	MeetingURL    string `json:"meeting_url,omitempty"`
	RespondBy     string `json:"respond_by,omitempty"`
	PaymentStatus string `json:"payment_status"`
	AmountCents   int32  `json:"amount_cents"`
	Currency      string `json:"currency"`
	HoldExpiresAt string `json:"hold_expires_at,omitempty"`
	// CheckoutURL is only set on the response to a booking that must be paid
	// for; it is where the client authorises the payment.
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
	return Session{
		ID:              s.ID.String(),
		UserID:          s.UserID.String(),
		CoachID:         s.CoachID.String(),
		CounterpartName: counterpart,
		ScheduledTime:   s.ScheduledTime.UTC().Format(time.RFC3339),
		DurationMinutes: s.DurationMinutes,
		Status:          s.Status,
		Topic:           s.Topic,
		CoachNotes:      s.CoachNotes,
		MeetingURL:      s.MeetingUrl,
		RespondBy:       rfc3339(s.RespondBy),
		PaymentStatus:   s.PaymentStatus,
		AmountCents:     s.AmountCents,
		Currency:        s.Currency,
		HoldExpiresAt:   rfc3339(s.HoldExpiresAt),
	}
}

// rfc3339 formats an optional instant, empty when nil.
func rfc3339(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ListCoaches returns a page of the coach directory. A non-nil phase keeps
// only coaches who list that dating phase; it is trimmed and lowercased like
// stored phases and must then be one of auth.DatingPhases, else ErrInvalidInput.
func (s *Service) ListCoaches(ctx context.Context, limit, offset int32, acceptingOnly bool, phase *string) ([]Coach, error) {
	if phase != nil {
		p := strings.ToLower(strings.TrimSpace(*phase))
		if !slices.Contains(auth.DatingPhases, p) {
			return nil, fmt.Errorf("%w: unknown phase %q", ErrInvalidInput, *phase)
		}
		phase = &p
	}
	rows, err := s.queries.ListCoaches(ctx, db.ListCoachesParams{
		Limit:         limit,
		Offset:        offset,
		AcceptingOnly: &acceptingOnly,
		Phase:         phase,
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
			Phases:           row.Phases,
			HourlyRateCents:  row.HourlyRateCents,
			Timezone:         row.Timezone,
			YearsExperience:  row.YearsExperience,
			AcceptingClients: row.AcceptingClients,
			AvgRating:        row.AvgRating,
			ReviewCount:      row.ReviewCount,
			RecommendCount:   row.RecommendCount,
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
		Phases:           row.Phases,
		HourlyRateCents:  row.HourlyRateCents,
		Timezone:         row.Timezone,
		YearsExperience:  row.YearsExperience,
		AcceptingClients: row.AcceptingClients,
		AvgRating:        row.AvgRating,
		ReviewCount:      row.ReviewCount,
		RecommendCount:   row.RecommendCount,
	}, nil
}

// UpsertProfileInput is the decoded PUT /coach/profile body.
type UpsertProfileInput struct {
	Headline         string   `json:"headline"`
	Bio              string   `json:"bio"`
	Specialties      []string `json:"specialties"`
	Phases           []string `json:"phases"`
	HourlyRateCents  int32    `json:"hourly_rate_cents"`
	Timezone         string   `json:"timezone"`
	YearsExperience  int32    `json:"years_experience"`
	AcceptingClients bool     `json:"accepting_clients"`
}

// normalisePhases validates phases against auth.DatingPhases and returns them
// deduplicated in vocabulary order; blanks are dropped and a nil list yields an
// empty (never nil) slice. An unknown value yields ErrInvalidInput.
func normalisePhases(phases []string) ([]string, error) {
	chosen := make(map[string]bool, len(phases))
	for _, p := range phases {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if !slices.Contains(auth.DatingPhases, p) {
			return nil, fmt.Errorf("%w: phases contains unknown value %q", ErrInvalidInput, p)
		}
		chosen[p] = true
	}
	out := make([]string, 0, len(chosen))
	for _, p := range auth.DatingPhases {
		if chosen[p] {
			out = append(out, p)
		}
	}
	return out, nil
}

// UpsertProfile creates or replaces the caller's coach profile. The timezone is
// validated here because every slot calculation is done in it, and phases
// against auth.DatingPhases so the directory filter has a fixed vocabulary.
func (s *Service) UpsertProfile(ctx context.Context, coachID uuid.UUID, in UpsertProfileInput) (Coach, error) {
	if in.Timezone == "" {
		in.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil {
		return Coach{}, fmt.Errorf("%w: unknown timezone %q", ErrInvalidInput, in.Timezone)
	}
	if in.HourlyRateCents < 0 || (s.payments.Enabled() && in.HourlyRateCents > maxHourlyRateCents) {
		return Coach{}, fmt.Errorf("%w: hourly_rate_cents must be between 0 and %d", ErrInvalidInput, maxHourlyRateCents)
	}
	if in.Specialties == nil {
		in.Specialties = []string{}
	}
	phases, err := normalisePhases(in.Phases)
	if err != nil {
		return Coach{}, err
	}
	row, err := s.queries.UpsertCoachProfile(ctx, db.UpsertCoachProfileParams{
		UserID:           coachID,
		Headline:         in.Headline,
		Bio:              in.Bio,
		Specialties:      in.Specialties,
		Phases:           phases,
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
// offer times that overlap the slot being moved. With payments on, slots too
// close to start for the client to complete checkout (see minCheckoutWindow)
// are not offered either; a reschedule takes no payment, so it keeps those.
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

	if s.payments.Enabled() && excludeSessionID == nil {
		if earliest := time.Now().Add(minCheckoutWindow); earliest.After(from) {
			from = earliest
		}
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

// BookSession requests a slot with a coach after checking that it is in the
// future, inside the published availability and still free. The session is
// created pending and holds the slot; the coach is emailed a confirm/decline
// link, and the client a receipt, both recorded in the outbox in the same
// transaction as the session.
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
	token, err := randomToken()
	if err != nil {
		return Session{}, err
	}
	respondBy := respondDeadline(time.Now(), scheduled)
	tokenHash := hashToken(token)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Session{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	session, err := q.CreateCoachingSession(ctx, db.CreateCoachingSessionParams{
		UserID:            userID,
		CoachID:           coachID,
		ScheduledTime:     scheduled,
		DurationMinutes:   duration,
		Topic:             in.Topic,
		ConfirmationToken: &tokenHash,
		RespondBy:         &respondBy,
	})
	if err != nil {
		if isSlotConflict(err) {
			return Session{}, ErrSlotTaken
		}
		return Session{}, fmt.Errorf("create session: %w", err)
	}
	parties, err := q.GetSessionParties(ctx, session.ID)
	if err != nil {
		return Session{}, fmt.Errorf("load session parties: %w", err)
	}
	if err := s.mail.coachRequest(ctx, q, parties, token, false); err != nil {
		return Session{}, err
	}
	if err := s.mail.clientRequested(ctx, q, parties); err != nil {
		return Session{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Session{}, fmt.Errorf("commit booking: %w", err)
	}
	return sessionOf(session, ""), nil
}

// respondDeadline is when a request made at now for a session at start expires
// unanswered: respondWindow from now, but no later than respondLeadTime before
// the session. A request made inside that lead time may be answered right up
// to the start.
func respondDeadline(now, start time.Time) time.Time {
	deadline := now.Add(respondWindow)
	if latest := start.Add(-respondLeadTime); latest.Before(deadline) {
		deadline = latest
	}
	if deadline.Before(now) {
		deadline = start
	}
	return deadline
}

// randomToken returns a 256-bit hex string used as a confirmation token. Only
// its hashToken digest is stored, so a database read cannot answer requests.
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// hashToken is the at-rest form of a confirmation token: hex SHA-256, the same
// scheme refresh tokens use.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// RespondWithToken answers a pending request using the token from the coach's
// email, so no sign-in is needed. action is "confirm" or "decline". A token
// that matches no session yields ErrNotFound; one whose deadline has passed
// ErrExpired; a request already answered ErrInvalidInput.
func (s *Service) RespondWithToken(ctx context.Context, sessionID uuid.UUID, token, action string) (Session, error) {
	if token == "" {
		return Session{}, fmt.Errorf("%w: token is required", ErrInvalidInput)
	}
	tokenHash := hashToken(token)
	session, err := s.queries.GetSessionByConfirmationToken(ctx, db.GetSessionByConfirmationTokenParams{ID: sessionID, ConfirmationToken: &tokenHash})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Session{}, ErrNotFound
		}
		return Session{}, fmt.Errorf("get session by token: %w", err)
	}
	return s.respond(ctx, session, action)
}

// RespondAsCoach answers a pending request from the coach dashboard; only the
// session's coach may. action is "confirm" or "decline".
func (s *Service) RespondAsCoach(ctx context.Context, sessionID, coachID uuid.UUID, action string) (Session, error) {
	session, err := s.participant(ctx, sessionID, coachID)
	if err != nil {
		return Session{}, err
	}
	if session.CoachID != coachID {
		return Session{}, ErrForbidden
	}
	return s.respond(ctx, session, action)
}

// respond applies a confirm or decline to a pending session and queues the
// client's notification and the coach's calendar update in the same
// transaction. The UPDATE is conditional on the session still being pending and
// inside its deadline (by database time), so two concurrent answers cannot both
// win and an answer racing the expiry sweep cannot land after it.
func (s *Service) respond(ctx context.Context, session db.CoachingSession, action string) (Session, error) {
	if action != "confirm" && action != "decline" {
		return Session{}, fmt.Errorf("%w: action must be confirm or decline", ErrInvalidInput)
	}
	if session.Status != StatusPending {
		return Session{}, fmt.Errorf("%w: request is already %s", ErrInvalidInput, session.Status)
	}
	if session.RespondBy != nil && session.RespondBy.Before(time.Now()) {
		return Session{}, ErrExpired
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Session{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	var updated db.CoachingSession
	if action == "confirm" {
		updated, err = q.ConfirmSession(ctx, session.ID)
	} else {
		updated, err = q.DeclineSession(ctx, session.ID)
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Session{}, s.whyNotPending(ctx, session.ID)
		}
		return Session{}, fmt.Errorf("%s session: %w", action, err)
	}
	if action == "confirm" && updated.PaymentStatus == payments.StatusAuthorized {
		if err := s.capture(ctx, q, updated); err != nil {
			return Session{}, err
		}
		updated.PaymentStatus = payments.StatusPaid
	}
	parties, err := q.GetSessionParties(ctx, session.ID)
	if err != nil {
		return Session{}, fmt.Errorf("load session parties: %w", err)
	}
	if action == "confirm" {
		err = s.mail.clientConfirmed(ctx, q, parties)
	} else {
		err = s.mail.clientDeclined(ctx, q, parties)
	}
	if err != nil {
		return Session{}, err
	}
	if err := s.mail.coachCalendarUpdate(ctx, q, parties); err != nil {
		return Session{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Session{}, fmt.Errorf("commit response: %w", err)
	}
	return sessionOf(updated, ""), nil
}

// whyNotPending explains a conditional confirm/decline UPDATE that matched no
// row: ErrExpired when the request is still pending but past its deadline,
// otherwise ErrInvalidInput naming the status it has reached.
func (s *Service) whyNotPending(ctx context.Context, sessionID uuid.UUID) error {
	current, err := s.queries.GetCoachingSession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("%w: request is no longer pending", ErrInvalidInput)
	}
	if current.Status == StatusPending {
		return ErrExpired
	}
	return fmt.Errorf("%w: request is already %s", ErrInvalidInput, current.Status)
}

// expireBatchSize bounds how many overdue requests one transaction expires, so
// a backlog is worked through in short transactions instead of one long one.
const expireBatchSize = 100

// ExpirePending moves every pending request whose deadline has passed to
// expired, releasing its slot, and queues an email to each client and a
// calendar cancellation to each coach. Work is committed in batches of
// expireBatchSize; rows another sweep already holds are skipped. It returns how
// many expired in total; cmd/worker calls it periodically.
func (s *Service) ExpirePending(ctx context.Context) (int, error) {
	total := 0
	for {
		n, err := s.expireBatch(ctx)
		total += n
		if err != nil {
			return total, err
		}
		if n < expireBatchSize {
			return total, nil
		}
	}
}

// expireBatch expires up to expireBatchSize overdue requests in one transaction
// together with their notification emails, returning how many it expired.
func (s *Service) expireBatch(ctx context.Context) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	expired, err := q.ExpirePendingSessions(ctx, expireBatchSize)
	if err != nil {
		return 0, fmt.Errorf("expire pending sessions: %w", err)
	}
	for _, session := range expired {
		parties, err := q.GetSessionParties(ctx, session.ID)
		if err != nil {
			return 0, fmt.Errorf("load session parties: %w", err)
		}
		if err := s.mail.clientExpired(ctx, q, parties); err != nil {
			return 0, err
		}
		if err := s.mail.coachCalendarUpdate(ctx, q, parties); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit expiry: %w", err)
	}
	return len(expired), nil
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
			MeetingURL:      row.MeetingUrl,
			RespondBy:       rfc3339(row.RespondBy),
			PaymentStatus:   row.PaymentStatus,
			AmountCents:     row.AmountCents,
			Currency:        row.Currency,
			HoldExpiresAt:   rfc3339(row.HoldExpiresAt),
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
			MeetingURL:      row.MeetingUrl,
			RespondBy:       rfc3339(row.RespondBy),
			PaymentStatus:   row.PaymentStatus,
			AmountCents:     row.AmountCents,
			Currency:        row.Currency,
			HoldExpiresAt:   rfc3339(row.HoldExpiresAt),
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

// SetStatus moves a session along statusTransitions; either participant may
// cancel, which is how client-side cancellation is implemented, while completed
// and no_show are the coach's to record. A cancellation queues an email to the
// other party in the same transaction.
func (s *Service) SetStatus(ctx context.Context, sessionID, actorID uuid.UUID, status string) (Session, error) {
	session, err := s.participant(ctx, sessionID, actorID)
	if err != nil {
		return Session{}, err
	}
	if !statusTransitions[session.Status][status] {
		return Session{}, fmt.Errorf("%w: a %s session cannot be marked %s", ErrInvalidInput, session.Status, status)
	}
	byCoach := actorID == session.CoachID
	if coachOnlyStatuses[status] && !byCoach {
		return Session{}, ErrForbidden
	}
	if session.Status == StatusPendingPayment {
		// Nobody has been emailed yet, so there is nothing to notify; the open
		// checkout becomes owed clean-up for SweepPayments. If the card was
		// authorised in the meantime the row is now pending and the ordinary
		// cancel below releases the authorisation instead.
		updated, err := s.queries.ReleasePendingPaymentSession(ctx, sessionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return s.SetStatus(ctx, sessionID, actorID, status)
		}
		if err != nil {
			return Session{}, fmt.Errorf("release payment hold: %w", err)
		}
		return sessionOf(updated, ""), nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Session{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	updated, err := q.UpdateSessionStatus(ctx, db.UpdateSessionStatusParams{ID: sessionID, Status: status, ExpectedStatus: session.Status})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Session{}, fmt.Errorf("%w: session is no longer %s", ErrInvalidInput, session.Status)
		}
		return Session{}, fmt.Errorf("update session status: %w", err)
	}
	if status == StatusCancelled {
		parties, err := q.GetSessionParties(ctx, sessionID)
		if err != nil {
			return Session{}, fmt.Errorf("load session parties: %w", err)
		}
		if err := s.mail.cancelled(ctx, q, parties, byCoach); err != nil {
			return Session{}, err
		}
		// The coach holds an invite from the request email; the client only
		// once the session was confirmed.
		if byCoach || session.Status == StatusScheduled {
			if err := s.mail.selfCalendarUpdate(ctx, q, parties, byCoach); err != nil {
				return Session{}, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Session{}, fmt.Errorf("commit status: %w", err)
	}
	return sessionOf(updated, ""), nil
}

// Reschedule moves a pending or scheduled session to a new start time, keeping
// its duration. The session being moved is excluded from the conflict check,
// and the coach need not still be accepting new clients. A client-initiated
// move returns the session to pending with a fresh token and asks the coach to
// reconfirm; a coach-initiated move keeps the status and sends the client an
// updated invite.
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
	if session.Status != StatusPending && session.Status != StatusScheduled {
		return Session{}, fmt.Errorf("%w: a %s session cannot be rescheduled", ErrInvalidInput, session.Status)
	}
	if err := s.assertBookable(ctx, session.CoachID, scheduled, session.DurationMinutes, false, &sessionID); err != nil {
		return Session{}, err
	}

	byCoach := actorID == session.CoachID
	params := db.RescheduleSessionParams{
		ID:                sessionID,
		ScheduledTime:     scheduled,
		ExpectedStatus:    session.Status,
		Status:            session.Status,
		ConfirmationToken: session.ConfirmationToken,
		RespondBy:         session.RespondBy,
	}
	var token string
	if !byCoach {
		token, err = randomToken()
		if err != nil {
			return Session{}, err
		}
		respondBy := respondDeadline(time.Now(), scheduled)
		tokenHash := hashToken(token)
		params.Status = StatusPending
		params.ConfirmationToken = &tokenHash
		params.RespondBy = &respondBy
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Session{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	updated, err := q.RescheduleSession(ctx, params)
	if err != nil {
		if isSlotConflict(err) {
			return Session{}, ErrSlotTaken
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return Session{}, fmt.Errorf("%w: session is no longer %s", ErrInvalidInput, session.Status)
		}
		return Session{}, fmt.Errorf("reschedule session: %w", err)
	}
	parties, err := q.GetSessionParties(ctx, sessionID)
	if err != nil {
		return Session{}, fmt.Errorf("load session parties: %w", err)
	}
	if byCoach {
		if err := s.mail.coachRescheduled(ctx, q, parties); err != nil {
			return Session{}, err
		}
		err = s.mail.selfCalendarUpdate(ctx, q, parties, true)
	} else {
		err = s.mail.coachRequest(ctx, q, parties, token, true)
	}
	if err != nil {
		return Session{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Session{}, fmt.Errorf("commit reschedule: %w", err)
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

// SetMeetingURL stores the join link clients use to attend the session; only
// the session's coach may, and only while the session is pending or scheduled
// (a finished or cancelled session yields ErrInvalidInput). The URL must be
// empty (clearing it) or an absolute http(s) URL with a host, otherwise
// ErrInvalidInput. Changing the link on a scheduled session bumps the calendar
// sequence and emails both parties an updated invite so their calendars carry
// the new location; a pending session is simply updated, since its confirmed
// invite has not been sent yet. Setting the value it already holds is a no-op.
func (s *Service) SetMeetingURL(ctx context.Context, sessionID, coachID uuid.UUID, meetingURL string) (Session, error) {
	meetingURL, err := normaliseMeetingURL(meetingURL)
	if err != nil {
		return Session{}, err
	}
	session, err := s.participant(ctx, sessionID, coachID)
	if err != nil {
		return Session{}, err
	}
	if session.CoachID != coachID {
		return Session{}, ErrForbidden
	}
	if err := meetingLinkAllowed(session); err != nil {
		return Session{}, err
	}
	if session.MeetingUrl == meetingURL {
		return sessionOf(session, ""), nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Session{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	updated, err := q.UpdateSessionMeetingURL(ctx, db.UpdateSessionMeetingURLParams{ID: sessionID, MeetingUrl: meetingURL})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return Session{}, fmt.Errorf("update meeting url: %w", err)
		}
		// The conditional UPDATE matched nothing: a concurrent change moved the
		// session to a terminal status or already stored this value. Re-read
		// and report whichever it was.
		current, err := q.GetCoachingSession(ctx, sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Session{}, ErrNotFound
			}
			return Session{}, fmt.Errorf("reload session: %w", err)
		}
		if err := meetingLinkAllowed(current); err != nil {
			return Session{}, err
		}
		return sessionOf(current, ""), nil
	}
	if updated.Status == StatusScheduled {
		parties, err := q.GetSessionParties(ctx, sessionID)
		if err != nil {
			return Session{}, fmt.Errorf("load session parties: %w", err)
		}
		if err := s.mail.meetingLinkChanged(ctx, q, parties, false); err != nil {
			return Session{}, err
		}
		if err := s.mail.meetingLinkChanged(ctx, q, parties, true); err != nil {
			return Session{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Session{}, fmt.Errorf("commit meeting url: %w", err)
	}
	return sessionOf(updated, ""), nil
}

// meetingLinkAllowed reports ErrInvalidInput when the session's status no
// longer admits a meeting link (only pending and scheduled sessions do).
func meetingLinkAllowed(session db.CoachingSession) error {
	if session.Status != StatusPending && session.Status != StatusScheduled {
		return fmt.Errorf("%w: a %s session cannot take a meeting link", ErrInvalidInput, session.Status)
	}
	return nil
}

// normaliseMeetingURL trims raw and returns it unchanged when empty or when it
// parses as an absolute http or https URL with a host; anything else is
// ErrInvalidInput so a bare "zoom.us/j/1" or a javascript: link is never
// stored and later rendered as a tappable link.
func normaliseMeetingURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("%w: meeting_url must be an http(s) URL", ErrInvalidInput)
	}
	return raw, nil
}

// maxReviewComment bounds the free-text comment on a review.
const maxReviewComment = 2000

// recommendThreshold is the lowest rating that counts as recommending the
// coach; the app never shows the rating itself.
const recommendThreshold = 4

// maxSummaryReviews caps how many (most recently written) reviews are sent to
// the summariser.
const maxSummaryReviews = 200

// summaryTimeout bounds the wait for ml-analyzer before the aggregate fallback
// is used; the page already has everything it needs locally.
const summaryTimeout = 8 * time.Second

// CreateReview records (or replaces) the caller's review of a coach. It is
// gated: the caller must have at least one completed session with the coach,
// or, when in.SessionID is set, that session must be theirs, with that coach,
// and completed — otherwise ErrForbidden. Rating must be 1-5 and the comment at
// most maxReviewComment characters, otherwise ErrInvalidInput. A second review
// for the same coach overwrites the first (one review per client per coach).
func (s *Service) CreateReview(ctx context.Context, coachID, userID uuid.UUID, in CreateReviewInput) (Review, error) {
	if in.Rating < 1 || in.Rating > 5 {
		return Review{}, fmt.Errorf("%w: rating must be between 1 and 5", ErrInvalidInput)
	}
	comment := strings.TrimSpace(in.Comment)
	if utf8.RuneCountInString(comment) > maxReviewComment {
		return Review{}, fmt.Errorf("%w: comment exceeds %d characters", ErrInvalidInput, maxReviewComment)
	}
	if _, err := s.queries.GetCoach(ctx, coachID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Review{}, ErrNotFound
		}
		return Review{}, fmt.Errorf("load coach: %w", err)
	}
	completed, err := s.queries.HasCompletedSession(ctx, db.HasCompletedSessionParams{
		CoachID:   coachID,
		UserID:    userID,
		SessionID: in.SessionID,
	})
	if err != nil {
		return Review{}, fmt.Errorf("check completed session: %w", err)
	}
	if !completed {
		return Review{}, ErrForbidden
	}
	row, err := s.queries.UpsertCoachReview(ctx, db.UpsertCoachReviewParams{
		CoachID:   coachID,
		UserID:    userID,
		SessionID: in.SessionID,
		Rating:    in.Rating,
		Comment:   comment,
	})
	if err != nil {
		return Review{}, fmt.Errorf("upsert review: %w", err)
	}
	reviewer, err := s.queries.GetUserByID(ctx, userID)
	if err != nil {
		return Review{}, fmt.Errorf("load reviewer: %w", err)
	}
	return reviewOf(row, reviewer.DisplayName), nil
}

// ListReviews returns the coach's reviews, newest first. It is public to any
// authenticated caller and returns ErrNotFound for an unknown coach.
func (s *Service) ListReviews(ctx context.Context, coachID uuid.UUID, limit, offset int32) ([]Review, error) {
	if _, err := s.queries.GetCoach(ctx, coachID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("load coach: %w", err)
	}
	rows, err := s.queries.ListCoachReviews(ctx, db.ListCoachReviewsParams{CoachID: coachID, Limit: limit, Offset: offset})
	if err != nil {
		return nil, fmt.Errorf("list reviews: %w", err)
	}
	out := make([]Review, 0, len(rows))
	for _, r := range rows {
		out = append(out, reviewOf(db.CoachReview{
			ID: r.ID, CoachID: r.CoachID, UserID: r.UserID, SessionID: r.SessionID,
			Rating: r.Rating, Comment: r.Comment, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		}, r.ReviewerName))
	}
	return out, nil
}

// reviewOf converts a stored review plus its reviewer's display name into the
// API shape; a nil session id becomes an omitted field.
func reviewOf(r db.CoachReview, reviewerName string) Review {
	out := Review{
		ID:           r.ID.String(),
		CoachID:      r.CoachID.String(),
		UserID:       r.UserID.String(),
		ReviewerName: reviewerName,
		Rating:       r.Rating,
		Recommended:  r.Rating >= recommendThreshold,
		Comment:      r.Comment,
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
	}
	if r.SessionID != nil {
		out.SessionID = r.SessionID.String()
	}
	return out
}

// ReviewSummary condenses the coach's reviews into a recommendation line and
// named strengths for the coach page. Unknown coach → ErrNotFound. With no
// summariser configured, or when it fails, it degrades to the deterministic
// recommendation line (from the stored aggregates) with no strengths, so the
// page never breaks because ml-analyzer is down.
func (s *Service) ReviewSummary(ctx context.Context, coachID uuid.UUID) (ReviewSummaryResult, error) {
	coach, err := s.GetCoach(ctx, coachID)
	if err != nil {
		return ReviewSummaryResult{}, err
	}
	fallback := ReviewSummaryResult{
		ModelVersion: "aggregate",
		Recommended:  coach.RecommendCount,
		Total:        coach.ReviewCount,
		Summary:      recommendationLine(coach.RecommendCount, coach.ReviewCount, coach.DisplayName),
		Strengths:    []string{},
	}
	if s.reviews == nil || coach.ReviewCount == 0 {
		return fallback, nil
	}
	rows, err := s.queries.ListCoachReviewTexts(ctx, db.ListCoachReviewTextsParams{CoachID: coachID, Limit: maxSummaryReviews})
	if err != nil {
		return ReviewSummaryResult{}, fmt.Errorf("load reviews: %w", err)
	}
	req := ReviewSummaryRequest{CoachName: coach.DisplayName, Reviews: make([]ReviewSummaryItem, 0, len(rows))}
	for _, r := range rows {
		req.Reviews = append(req.Reviews, ReviewSummaryItem{Rating: r.Rating, Comment: r.Comment})
	}
	mlCtx, cancel := context.WithTimeout(ctx, summaryTimeout)
	defer cancel()
	out, err := s.reviews.SummarizeReviews(mlCtx, req)
	if err != nil {
		slog.WarnContext(ctx, "review summary unavailable, using aggregates", "coach_id", coachID, "err", err)
		return fallback, nil
	}
	if out.Strengths == nil {
		out.Strengths = []string{}
	}
	// The summariser only saw a sample; the stored aggregates own the counts and
	// the headline built from them.
	sampleLine := recommendationLine(out.Recommended, out.Total, coach.DisplayName)
	out.Summary = strings.TrimSpace(fallback.Summary + " " + strings.TrimSpace(strings.TrimPrefix(out.Summary, sampleLine)))
	out.Recommended, out.Total = fallback.Recommended, fallback.Total
	return out, nil
}

// recommendationLine phrases the recommend/total counts without numbers of
// stars; empty when there are no reviews.
func recommendationLine(recommended, total int32, coachName string) string {
	if total == 0 {
		return ""
	}
	if recommended == total {
		return fmt.Sprintf("Every client so far recommends %s.", coachName)
	}
	return fmt.Sprintf("%d of %d clients recommend %s.", recommended, total, coachName)
}
