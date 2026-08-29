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

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrForbidden    = errors.New("not allowed")
	ErrInvalidInput = errors.New("invalid input")
	ErrSlotTaken    = errors.New("slot is no longer available")
	ErrUnavailable  = errors.New("coach is not available then")
)

const (
	defaultSessionMinutes = 45
	slotStepMinutes       = 30
	minSessionMinutes     = 15
	maxSessionMinutes     = 240
	maxAvailabilityDays   = 30
)

var sessionStatuses = map[string]bool{
	"scheduled": true,
	"completed": true,
	"cancelled": true,
	"no_show":   true,
}

type Service struct {
	pool    *pgxpool.Pool
	queries *db.Queries
}

func NewService(pool *pgxpool.Pool, queries *db.Queries) *Service {
	return &Service{pool: pool, queries: queries}
}

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
}

type Slot struct {
	Start           string `json:"start"`
	DurationMinutes int32  `json:"duration_minutes"`
}

type AvailabilityWindow struct {
	Weekday     int16 `json:"weekday"`
	StartMinute int32 `json:"start_minute"`
	EndMinute   int32 `json:"end_minute"`
}

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
	}
}

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

type UpsertProfileInput struct {
	Headline         string   `json:"headline"`
	Bio              string   `json:"bio"`
	Specialties      []string `json:"specialties"`
	HourlyRateCents  int32    `json:"hourly_rate_cents"`
	Timezone         string   `json:"timezone"`
	YearsExperience  int32    `json:"years_experience"`
	AcceptingClients bool     `json:"accepting_clients"`
}

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

type BookInput struct {
	CoachID         string `json:"coach_id"`
	ScheduledTime   string `json:"scheduled_time"`
	DurationMinutes int32  `json:"duration_minutes"`
	Topic           string `json:"topic"`
}

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
		})
	}
	return out, nil
}

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

func (s *Service) SetStatus(ctx context.Context, sessionID, actorID uuid.UUID, status string) (Session, error) {
	if !sessionStatuses[status] {
		return Session{}, fmt.Errorf("%w: unknown status %q", ErrInvalidInput, status)
	}
	if _, err := s.participant(ctx, sessionID, actorID); err != nil {
		return Session{}, err
	}
	updated, err := s.queries.UpdateSessionStatus(ctx, db.UpdateSessionStatusParams{ID: sessionID, Status: status})
	if err != nil {
		return Session{}, fmt.Errorf("update session status: %w", err)
	}
	return sessionOf(updated, ""), nil
}

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
