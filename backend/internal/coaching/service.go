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
	ErrSlotTaken    = errors.New("slot is no longer available")
	ErrUnavailable  = errors.New("coach is not available then")
	ErrExpired      = errors.New("request has expired")
)

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
// sessions end completed, cancelled or no_show.
const (
	StatusPending   = "pending"
	StatusScheduled = "scheduled"
	StatusDeclined  = "declined"
	StatusExpired   = "expired"
	StatusCompleted = "completed"
	StatusCancelled = "cancelled"
	StatusNoShow    = "no_show"
)

// statusTransitions lists, per current status, the statuses SetStatus may move
// a session to. Confirmation and decline go through Respond instead, and
// expiry through ExpirePending, so neither is reachable from here.
var statusTransitions = map[string]map[string]bool{
	StatusPending:   {StatusCancelled: true},
	StatusScheduled: {StatusCompleted: true, StatusCancelled: true, StatusNoShow: true},
}

// coachOnlyStatuses are the outcomes only the coach may record.
var coachOnlyStatuses = map[string]bool{StatusCompleted: true, StatusNoShow: true}

// Service owns the coaching business rules: the coach directory and profiles,
// each coach's weekly availability, the bookable slots derived from it, and the
// lifecycle of a booked session (request, confirm, reschedule, status, notes).
type Service struct {
	pool    *pgxpool.Pool
	queries *db.Queries
	mail    mailer
}

// NewService wires the service dependencies; called once from cmd/api and
// cmd/worker. appURL is the public app origin the confirmation links in coach
// emails point at and mailFrom the organizer address on calendar invites.
func NewService(pool *pgxpool.Pool, queries *db.Queries, appURL, mailFrom string) *Service {
	return &Service{pool: pool, queries: queries, mail: mailer{appURL: appURL, mailFrom: mailFrom}}
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
	RespondBy       string `json:"respond_by,omitempty"`
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
		RespondBy:       rfc3339(s.RespondBy),
	}
}

// rfc3339 formats an optional instant, empty when nil.
func rfc3339(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
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
// client's notification in the same transaction. The UPDATE is conditional on
// the session still being pending, so two concurrent answers cannot both win.
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
			return Session{}, fmt.Errorf("%w: request is no longer pending", ErrInvalidInput)
		}
		return Session{}, fmt.Errorf("%s session: %w", action, err)
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
	if err := tx.Commit(ctx); err != nil {
		return Session{}, fmt.Errorf("commit response: %w", err)
	}
	return sessionOf(updated, ""), nil
}

// ExpirePending moves every pending request whose deadline has passed to
// expired, releasing its slot, and queues an email to each client. It returns
// how many expired; cmd/worker calls it periodically.
func (s *Service) ExpirePending(ctx context.Context) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	expired, err := q.ExpirePendingSessions(ctx)
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
			RespondBy:       rfc3339(row.RespondBy),
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
			RespondBy:       rfc3339(row.RespondBy),
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

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Session{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	updated, err := q.UpdateSessionStatus(ctx, db.UpdateSessionStatusParams{ID: sessionID, Status: status})
	if err != nil {
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
			return Session{}, ErrNotFound
		}
		return Session{}, fmt.Errorf("reschedule session: %w", err)
	}
	parties, err := q.GetSessionParties(ctx, sessionID)
	if err != nil {
		return Session{}, fmt.Errorf("load session parties: %w", err)
	}
	if byCoach {
		err = s.mail.coachRescheduled(ctx, q, parties)
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
