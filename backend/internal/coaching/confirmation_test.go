package coaching

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

func TestRespondDeadline(t *testing.T) {
	now := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		start time.Time
		want  time.Time
	}{
		{"far away: full window", now.Add(72 * time.Hour), now.Add(24 * time.Hour)},
		{"tomorrow: capped at 2h before", now.Add(20 * time.Hour), now.Add(18 * time.Hour)},
		{"within lead time: up to the start", now.Add(90 * time.Minute), now.Add(90 * time.Minute)},
	}
	for _, tc := range cases {
		if got := respondDeadline(now, tc.start); !got.Equal(tc.want) {
			t.Errorf("%s: respondDeadline = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestHashTokenIsStableAndOpaque(t *testing.T) {
	tok, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 64 {
		t.Fatalf("token length = %d, want 64 hex chars", len(tok))
	}
	if hashToken(tok) != hashToken(tok) || hashToken(tok) == tok {
		t.Fatal("hashToken must be deterministic and differ from the token")
	}
}

func TestInviteMethodFollowsStatus(t *testing.T) {
	row := db.GetSessionPartiesRow{
		ID:              uuid.New(),
		ScheduledTime:   time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
		DurationMinutes: 45,
		Topic:           "first dates; nerves, etc.",
		UserName:        "Ana",
		UserEmail:       "ana@example.test",
		CoachName:       "Coach",
		CoachEmail:      "coach@example.test",
	}
	for status, want := range map[string]string{
		StatusPending:   "METHOD:REQUEST\r\n",
		StatusScheduled: "METHOD:REQUEST\r\n",
		StatusDeclined:  "METHOD:CANCEL\r\n",
		StatusCancelled: "METHOD:CANCEL\r\n",
	} {
		row.Status = status
		ics := invite(row, "no-reply@example.test")
		if !strings.Contains(ics, want) {
			t.Errorf("%s: invite lacks %q", status, want)
		}
		if !strings.Contains(ics, "DTSTART:20260601T100000Z\r\n") || !strings.Contains(ics, "DTEND:20260601T104500Z\r\n") {
			t.Errorf("%s: wrong DTSTART/DTEND in %q", status, ics)
		}
		if !strings.Contains(ics, `DESCRIPTION:Topic: first dates\; nerves\, etc.`) {
			t.Errorf("%s: TEXT value not escaped", status)
		}
	}
}

// testService connects to TEST_DATABASE_URL, skipping the test when it is unset
// so the suite still runs without Postgres.
func testService(t *testing.T) (*Service, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	return NewService(pool, db.New(pool), "http://app.test", "no-reply@example.test"), pool
}

// insertUser creates a user with the given role and registers its removal,
// which cascades to the coach profile, availability and sessions.
func insertUser(t *testing.T, pool *pgxpool.Pool, role string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email, password_hash, display_name, role) VALUES ($1, 'hash', $2, $3) RETURNING id`,
		uuid.NewString()+"@example.test", role, role).Scan(&id); err != nil {
		t.Fatalf("insert %s: %v", role, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", id)
	})
	return id
}

// insertCoach creates a coach available all day every day in UTC.
func insertCoach(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id := insertUser(t, pool, "coach")
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO coaches (user_id) VALUES ($1)`, id); err != nil {
		t.Fatalf("insert coach: %v", err)
	}
	for wd := 0; wd < 7; wd++ {
		if _, err := pool.Exec(ctx,
			`INSERT INTO coach_availability (coach_id, weekday, start_minute, end_minute) VALUES ($1, $2, 0, 1440)`,
			id, wd); err != nil {
			t.Fatalf("insert availability: %v", err)
		}
	}
	return id
}

// outboxFor returns the subjects of the emails queued for one address, and
// registers their cleanup.
func outboxFor(t *testing.T, pool *pgxpool.Pool, to string) []string {
	t.Helper()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM email_outbox WHERE to_email = $1", to)
	})
	rows, err := pool.Query(context.Background(),
		"SELECT subject FROM email_outbox WHERE to_email = $1 ORDER BY created_at", to)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	defer rows.Close()
	var subjects []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		subjects = append(subjects, s)
	}
	return subjects
}

// emailOf reads a user's address.
func emailOf(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) string {
	t.Helper()
	var email string
	if err := pool.QueryRow(context.Background(), "SELECT email FROM users WHERE id = $1", id).Scan(&email); err != nil {
		t.Fatal(err)
	}
	return email
}

// tokenHashOf reads the stored confirmation token hash of a session.
func tokenHashOf(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var h *string
	if err := pool.QueryRow(context.Background(),
		"SELECT confirmation_token FROM coaching_sessions WHERE id = $1", id).Scan(&h); err != nil {
		t.Fatal(err)
	}
	if h == nil {
		return ""
	}
	return *h
}

// nextSlot is a start time far enough away for the full response window.
func nextSlot() string {
	return time.Now().Add(72 * time.Hour).Truncate(30 * time.Minute).UTC().Format(time.RFC3339)
}

func TestBookingIsPendingHoldsSlotAndEmailsBothParties(t *testing.T) {
	svc, pool := testService(t)
	ctx := context.Background()
	coach, client, other := insertCoach(t, pool), insertUser(t, pool, "user"), insertUser(t, pool, "user")

	start := nextSlot()
	s, err := svc.BookSession(ctx, client, BookInput{CoachID: coach.String(), ScheduledTime: start, Topic: "nerves"})
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	if s.Status != StatusPending || s.RespondBy == "" {
		t.Fatalf("new booking = %+v, want pending with a deadline", s)
	}
	if _, err := svc.BookSession(ctx, other, BookInput{CoachID: coach.String(), ScheduledTime: start}); !errors.Is(err, ErrSlotTaken) {
		t.Fatalf("second booking of a pending slot: err = %v, want ErrSlotTaken", err)
	}
	if got := outboxFor(t, pool, emailOf(t, pool, coach)); len(got) != 1 || !strings.Contains(got[0], "request") {
		t.Fatalf("coach emails = %v, want one request", got)
	}
	if got := outboxFor(t, pool, emailOf(t, pool, client)); len(got) != 1 {
		t.Fatalf("client emails = %v, want one receipt", got)
	}
	if h := tokenHashOf(t, pool, s.ID); h == "" || len(h) != 64 {
		t.Fatalf("stored token hash = %q, want a sha256 hex digest", h)
	}
}

func TestDeclineReleasesSlotAndTokenIsSingleUse(t *testing.T) {
	svc, pool := testService(t)
	ctx := context.Background()
	coach, client, other := insertCoach(t, pool), insertUser(t, pool, "user"), insertUser(t, pool, "user")

	start := nextSlot()
	s, err := svc.BookSession(ctx, client, BookInput{CoachID: coach.String(), ScheduledTime: start})
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	// The raw token only exists in the email; recover it from the link.
	var body string
	if err := pool.QueryRow(ctx, "SELECT body FROM email_outbox WHERE to_email = $1", emailOf(t, pool, coach)).Scan(&body); err != nil {
		t.Fatal(err)
	}
	i := strings.Index(body, "token=")
	if i < 0 {
		t.Fatalf("coach email has no token link: %q", body)
	}
	token := body[i+len("token="):][:64]
	sid := uuid.MustParse(s.ID)

	if _, err := svc.RespondWithToken(ctx, sid, "not-the-token", "decline"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong token: err = %v, want ErrNotFound", err)
	}
	declined, err := svc.RespondWithToken(ctx, sid, token, "decline")
	if err != nil {
		t.Fatalf("decline: %v", err)
	}
	if declined.Status != StatusDeclined {
		t.Fatalf("status = %s, want declined", declined.Status)
	}
	if _, err := svc.RespondWithToken(ctx, sid, token, "confirm"); err == nil {
		t.Fatal("reused token must not confirm a declined request")
	}
	if _, err := svc.BookSession(ctx, other, BookInput{CoachID: coach.String(), ScheduledTime: start}); err != nil {
		t.Fatalf("slot should be free after decline: %v", err)
	}
	if got := outboxFor(t, pool, emailOf(t, pool, client)); len(got) != 2 || !strings.Contains(got[1], "declined") {
		t.Fatalf("client emails = %v, want receipt then decline", got)
	}
	outboxFor(t, pool, emailOf(t, pool, other))
}

func TestCoachConfirmsFromDashboardThenExpiryLeavesItAlone(t *testing.T) {
	svc, pool := testService(t)
	ctx := context.Background()
	coach, client, stranger := insertCoach(t, pool), insertUser(t, pool, "user"), insertCoach(t, pool)

	s, err := svc.BookSession(ctx, client, BookInput{CoachID: coach.String(), ScheduledTime: nextSlot()})
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	sid := uuid.MustParse(s.ID)
	if _, err := svc.RespondAsCoach(ctx, sid, stranger, "confirm"); !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrNotFound) {
		t.Fatalf("another coach confirming: err = %v", err)
	}
	if _, err := svc.RespondAsCoach(ctx, sid, coach, "maybe"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad action: err = %v, want ErrInvalidInput", err)
	}
	confirmed, err := svc.RespondAsCoach(ctx, sid, coach, "confirm")
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if confirmed.Status != StatusScheduled {
		t.Fatalf("status = %s, want scheduled", confirmed.Status)
	}
	if _, err := svc.RespondAsCoach(ctx, sid, coach, "decline"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("answering twice: err = %v, want ErrInvalidInput", err)
	}
	// Back-date the deadline: a confirmed session must survive the sweep.
	if _, err := pool.Exec(ctx, "UPDATE coaching_sessions SET respond_by = now() - interval '1 hour' WHERE id = $1", sid); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ExpirePending(ctx); err != nil {
		t.Fatalf("expire: %v", err)
	}
	var status string
	if err := pool.QueryRow(ctx, "SELECT status FROM coaching_sessions WHERE id = $1", sid).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != StatusScheduled {
		t.Fatalf("status after sweep = %s, want scheduled", status)
	}
	if got := outboxFor(t, pool, emailOf(t, pool, client)); len(got) != 2 || !strings.Contains(got[1], "confirmed") {
		t.Fatalf("client emails = %v, want receipt then confirmation", got)
	}
	outboxFor(t, pool, emailOf(t, pool, coach))
}

func TestExpiredRequestReleasesSlotAndRejectsLateAnswer(t *testing.T) {
	svc, pool := testService(t)
	ctx := context.Background()
	coach, client, other := insertCoach(t, pool), insertUser(t, pool, "user"), insertUser(t, pool, "user")

	start := nextSlot()
	s, err := svc.BookSession(ctx, client, BookInput{CoachID: coach.String(), ScheduledTime: start})
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	sid := uuid.MustParse(s.ID)
	if _, err := pool.Exec(ctx, "UPDATE coaching_sessions SET respond_by = now() - interval '1 minute' WHERE id = $1", sid); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RespondAsCoach(ctx, sid, coach, "confirm"); !errors.Is(err, ErrExpired) {
		t.Fatalf("late answer: err = %v, want ErrExpired", err)
	}
	n, err := svc.ExpirePending(ctx)
	if err != nil || n < 1 {
		t.Fatalf("ExpirePending = %d, %v; want >= 1", n, err)
	}
	if _, err := svc.BookSession(ctx, other, BookInput{CoachID: coach.String(), ScheduledTime: start}); err != nil {
		t.Fatalf("slot should be free after expiry: %v", err)
	}
	if got := outboxFor(t, pool, emailOf(t, pool, client)); len(got) != 2 || !strings.Contains(got[1], "expired") {
		t.Fatalf("client emails = %v, want receipt then expiry", got)
	}
	outboxFor(t, pool, emailOf(t, pool, coach))
	outboxFor(t, pool, emailOf(t, pool, other))
}

func TestClientRescheduleNeedsReconfirmation(t *testing.T) {
	svc, pool := testService(t)
	ctx := context.Background()
	coach, client := insertCoach(t, pool), insertUser(t, pool, "user")

	s, err := svc.BookSession(ctx, client, BookInput{CoachID: coach.String(), ScheduledTime: nextSlot()})
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	sid := uuid.MustParse(s.ID)
	if _, err := svc.RespondAsCoach(ctx, sid, coach, "confirm"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	firstHash := tokenHashOf(t, pool, s.ID)

	later := time.Now().Add(96 * time.Hour).Truncate(30 * time.Minute).UTC().Format(time.RFC3339)
	moved, err := svc.Reschedule(ctx, sid, coach, later)
	if err != nil {
		t.Fatalf("coach reschedule: %v", err)
	}
	if moved.Status != StatusScheduled {
		t.Fatalf("coach-moved status = %s, want scheduled", moved.Status)
	}

	later2 := time.Now().Add(120 * time.Hour).Truncate(30 * time.Minute).UTC().Format(time.RFC3339)
	moved, err = svc.Reschedule(ctx, sid, client, later2)
	if err != nil {
		t.Fatalf("client reschedule: %v", err)
	}
	if moved.Status != StatusPending || moved.RespondBy == "" {
		t.Fatalf("client-moved = %+v, want pending with a deadline", moved)
	}
	if h := tokenHashOf(t, pool, s.ID); h == firstHash {
		t.Fatal("client reschedule must issue a fresh token")
	}
	if got := outboxFor(t, pool, emailOf(t, pool, coach)); len(got) != 2 {
		t.Fatalf("coach emails = %v, want original request and re-request", got)
	}
	outboxFor(t, pool, emailOf(t, pool, client))
}
