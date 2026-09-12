package coaching

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danielliu30/dating-coach/backend/internal/payments"
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

func TestInviteAttendeeNamesAreParameterSafe(t *testing.T) {
	row := db.GetSessionPartiesRow{
		ID:            uuid.New(),
		Status:        StatusScheduled,
		ScheduledTime: time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
		UserName:      "Ana \"Nina\" O'Neil, Jr.",
		UserEmail:     "ana@example.test",
		CoachName:     "Coach: José;\nMüller",
		CoachEmail:    "coach@example.test",
	}
	ics := invite(row, "no-reply@example.test")
	for _, want := range []string{
		"ATTENDEE;CN=\"Ana 'Nina' O'Neil, Jr.\";ROLE=REQ-PARTICIPANT:mailto:ana@example.test\r\n",
		"ATTENDEE;CN=\"Coach: José;Müller\";ROLE=REQ-PARTICIPANT:mailto:coach@example.test\r\n",
		`SUMMARY:Dating Coach session: Ana "Nina" O'Neil\, Jr. with Coach: José\;\nMüller` + "\r\n",
	} {
		if !strings.Contains(ics, want) {
			t.Errorf("invite lacks %q in:\n%s", want, ics)
		}
	}
	for in, want := range map[string]string{
		"Ana":         "Ana",
		"Ana, Jr.":    `"Ana, Jr."`,
		`Say "hi"`:    "Say 'hi'",
		"tab\there":   "tabhere",
		"Élodie Ünal": "Élodie Ünal",
	} {
		if got := paramICS(in); got != want {
			t.Errorf("paramICS(%q) = %q, want %q", in, got, want)
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
	return NewService(pool, db.New(pool), payments.Disabled{}, 15*time.Minute, "http://app.test", "no-reply@example.test"), pool
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
	// Coach: original request, decline cancellation, then the other client's request.
	if got := outboxFor(t, pool, emailOf(t, pool, coach)); len(got) != 3 || !strings.Contains(got[1], "Declined") {
		t.Fatalf("coach emails = %v, want request, calendar cancellation, new request", got)
	}
	if ics := icsFor(t, pool, emailOf(t, pool, coach), "Declined"); !strings.Contains(ics, "METHOD:CANCEL") || !strings.Contains(ics, "SEQUENCE:1") {
		t.Fatalf("coach cancel invite = %q, want CANCEL with SEQUENCE:1", ics)
	}
	outboxFor(t, pool, emailOf(t, pool, other))
}

// icsFor returns the invite attached to the email queued for to whose subject
// contains subject.
func icsFor(t *testing.T, pool *pgxpool.Pool, to, subject string) string {
	t.Helper()
	var ics *string
	if err := pool.QueryRow(context.Background(),
		"SELECT ics FROM email_outbox WHERE to_email = $1 AND subject LIKE '%' || $2 || '%'", to, subject).Scan(&ics); err != nil {
		t.Fatal(err)
	}
	if ics == nil {
		return ""
	}
	return *ics
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
	if got := outboxFor(t, pool, emailOf(t, pool, coach)); len(got) != 2 || !strings.Contains(got[1], "Confirmed") {
		t.Fatalf("coach emails = %v, want request then confirmed invite", got)
	}
	if ics := icsFor(t, pool, emailOf(t, pool, coach), "Confirmed"); !strings.Contains(ics, "STATUS:CONFIRMED") || !strings.Contains(ics, "SEQUENCE:1") {
		t.Fatalf("coach confirmed invite = %q, want CONFIRMED with SEQUENCE:1", ics)
	}
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
	// Coach: original request, expiry cancellation, then the other client's request.
	if got := outboxFor(t, pool, emailOf(t, pool, coach)); len(got) != 3 || !strings.Contains(got[1], "Expired") {
		t.Fatalf("coach emails = %v, want request, calendar cancellation, new request", got)
	}
	outboxFor(t, pool, emailOf(t, pool, other))
}

func TestDeadlineIsEnforcedByDatabaseTime(t *testing.T) {
	svc, pool := testService(t)
	ctx := context.Background()
	coach, client := insertCoach(t, pool), insertUser(t, pool, "user")

	s, err := svc.BookSession(ctx, client, BookInput{CoachID: coach.String(), ScheduledTime: nextSlot()})
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	sid := uuid.MustParse(s.ID)
	// The pre-read saw a live deadline; it lapses before the UPDATE runs.
	session, err := svc.queries.GetCoachingSession(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE coaching_sessions SET respond_by = now() - interval '1 second' WHERE id = $1", sid); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.respond(ctx, session, "confirm"); !errors.Is(err, ErrExpired) {
		t.Fatalf("confirm after deadline lapsed mid-flight: err = %v, want ErrExpired", err)
	}
	var status string
	if err := pool.QueryRow(ctx, "SELECT status FROM coaching_sessions WHERE id = $1", sid).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != StatusPending {
		t.Fatalf("status = %s, want still pending for the sweep", status)
	}
	outboxFor(t, pool, emailOf(t, pool, client))
	outboxFor(t, pool, emailOf(t, pool, coach))
}

func TestStaleStatusAndRescheduleDoNotOverwrite(t *testing.T) {
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
	if _, err := svc.SetStatus(ctx, sid, coach, StatusCompleted); err != nil {
		t.Fatalf("complete: %v", err)
	}
	// A caller that validated against 'scheduled' must not win once the row
	// has moved on; UpdateSessionStatus is keyed on the expected status.
	if _, err := svc.queries.UpdateSessionStatus(ctx, db.UpdateSessionStatusParams{ID: sid, Status: StatusCancelled, ExpectedStatus: StatusScheduled}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale cancel: err = %v, want no rows", err)
	}
	if _, err := svc.Reschedule(ctx, sid, client, nextSlot()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("reschedule completed session: err = %v, want ErrInvalidInput", err)
	}
	var status string
	var seq int32
	if err := pool.QueryRow(ctx, "SELECT status, calendar_sequence FROM coaching_sessions WHERE id = $1", sid).Scan(&status, &seq); err != nil {
		t.Fatal(err)
	}
	if status != StatusCompleted || seq != 2 {
		t.Fatalf("status=%s sequence=%d, want completed with sequence 2 (confirm, complete)", status, seq)
	}
	outboxFor(t, pool, emailOf(t, pool, client))
	outboxFor(t, pool, emailOf(t, pool, coach))
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
	if got := outboxFor(t, pool, emailOf(t, pool, coach)); len(got) != 4 || !strings.HasPrefix(got[2], "Moved:") || !strings.Contains(got[3], "reconfirm") {
		t.Fatalf("coach emails = %v, want request, confirmed invite, own move update, re-request", got)
	}
	if ics := icsFor(t, pool, emailOf(t, pool, coach), "Moved:"); !strings.Contains(ics, "SEQUENCE:2\r\n") || !strings.Contains(ics, "STATUS:CONFIRMED") {
		t.Fatalf("coach's own move update should carry the new time as sequence 2:\n%s", ics)
	}
	outboxFor(t, pool, emailOf(t, pool, client))
}

func TestCancellationUpdatesBothCalendars(t *testing.T) {
	svc, pool := testService(t)
	ctx := context.Background()
	coach, client := insertCoach(t, pool), insertUser(t, pool, "user")

	// Client cancels a pending request: only the coach ever held an invite.
	s, err := svc.BookSession(ctx, client, BookInput{CoachID: coach.String(), ScheduledTime: nextSlot()})
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	if _, err := svc.SetStatus(ctx, uuid.MustParse(s.ID), client, StatusCancelled); err != nil {
		t.Fatalf("client cancel pending: %v", err)
	}
	if got := outboxFor(t, pool, emailOf(t, pool, client)); len(got) != 1 {
		t.Fatalf("client emails after cancelling a pending request = %v, want only the request receipt", got)
	}
	if got := outboxFor(t, pool, emailOf(t, pool, coach)); len(got) != 2 || !strings.Contains(got[1], "cancelled their session") {
		t.Fatalf("coach emails = %v, want request then cancellation", got)
	}

	// Client cancels a confirmed session: both held invites, both get a CANCEL.
	s, err = svc.BookSession(ctx, client, BookInput{CoachID: coach.String(), ScheduledTime: nextSlot()})
	if err != nil {
		t.Fatalf("rebook: %v", err)
	}
	sid := uuid.MustParse(s.ID)
	if _, err := svc.RespondAsCoach(ctx, sid, coach, "confirm"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := svc.SetStatus(ctx, sid, client, StatusCancelled); err != nil {
		t.Fatalf("client cancel confirmed: %v", err)
	}
	if ics := icsFor(t, pool, emailOf(t, pool, client), "Cancelled: session with"); !strings.Contains(ics, "METHOD:CANCEL") || !strings.Contains(ics, "UID:"+s.ID+"@") {
		t.Fatalf("client's own cancel update should cancel the confirmed event:\n%s", ics)
	}

	// Coach cancels a confirmed session: the coach's copy is cancelled too.
	s, err = svc.BookSession(ctx, client, BookInput{CoachID: coach.String(), ScheduledTime: nextSlot()})
	if err != nil {
		t.Fatalf("rebook: %v", err)
	}
	sid = uuid.MustParse(s.ID)
	if _, err := svc.RespondAsCoach(ctx, sid, coach, "confirm"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := svc.SetStatus(ctx, sid, coach, StatusCancelled); err != nil {
		t.Fatalf("coach cancel: %v", err)
	}
	if ics := icsFor(t, pool, emailOf(t, pool, coach), "Cancelled: session with"); !strings.Contains(ics, "METHOD:CANCEL") || !strings.Contains(ics, "UID:"+s.ID+"@") {
		t.Fatalf("coach's own cancel update should cancel their event:\n%s", ics)
	}
}

func TestMeetingLinkFollowsLifecycleAndUpdatesCalendars(t *testing.T) {
	svc, pool := testService(t)
	ctx := context.Background()
	coach, client, stranger := insertCoach(t, pool), insertUser(t, pool, "user"), insertCoach(t, pool)

	s, err := svc.BookSession(ctx, client, BookInput{CoachID: coach.String(), ScheduledTime: nextSlot()})
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	sid := uuid.MustParse(s.ID)
	if _, err := svc.SetMeetingURL(ctx, sid, stranger, "https://meet.test/a"); !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrNotFound) {
		t.Fatalf("another coach setting link: err = %v", err)
	}
	if _, err := svc.SetMeetingURL(ctx, sid, coach, "meet.test/a"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("relative url: err = %v, want ErrInvalidInput", err)
	}
	// While pending only the row changes; the confirmed invite carries the link later.
	if _, err := svc.SetMeetingURL(ctx, sid, coach, "https://meet.test/a"); err != nil {
		t.Fatalf("set while pending: %v", err)
	}
	if got := outboxFor(t, pool, emailOf(t, pool, client)); len(got) != 1 {
		t.Fatalf("client emails while pending = %v, want only the receipt", got)
	}
	if _, err := svc.RespondAsCoach(ctx, sid, coach, "confirm"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if ics := icsFor(t, pool, emailOf(t, pool, client), "confirmed"); !strings.Contains(ics, "LOCATION:https://meet.test/a") {
		t.Fatalf("confirmation invite = %q, want LOCATION with the link", ics)
	}
	// Unchanged value is a no-op: no extra mail, no sequence bump.
	if _, err := svc.SetMeetingURL(ctx, sid, coach, "https://meet.test/a"); err != nil {
		t.Fatalf("set same: %v", err)
	}
	if got := outboxFor(t, pool, emailOf(t, pool, client)); len(got) != 2 {
		t.Fatalf("client emails after no-op = %v, want receipt and confirmation", got)
	}
	updated, err := svc.SetMeetingURL(ctx, sid, coach, "https://meet.test/b")
	if err != nil {
		t.Fatalf("replace while scheduled: %v", err)
	}
	if updated.MeetingURL != "https://meet.test/b" {
		t.Fatalf("meeting url = %q, want the new link", updated.MeetingURL)
	}
	for _, who := range []uuid.UUID{client, coach} {
		got := outboxFor(t, pool, emailOf(t, pool, who))
		if len(got) != 3 || !strings.Contains(got[2], "Join link") {
			t.Fatalf("emails for %s = %v, want a join-link update last", who, got)
		}
		ics := icsFor(t, pool, emailOf(t, pool, who), "Join link")
		// Sequence: 1 pending set, 2 confirmation, 3 this replacement.
		if !strings.Contains(ics, "LOCATION:https://meet.test/b") || !strings.Contains(ics, "SEQUENCE:3") {
			t.Fatalf("join-link invite for %s = %q, want new LOCATION with SEQUENCE:3", who, ics)
		}
	}
	if _, err := svc.SetStatus(ctx, sid, client, StatusCancelled); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := svc.SetMeetingURL(ctx, sid, coach, "https://meet.test/c"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("set on cancelled: err = %v, want ErrInvalidInput", err)
	}
}

func TestInviteCarriesMeetingURL(t *testing.T) {
	row := db.GetSessionPartiesRow{
		ID:              uuid.New(),
		Status:          StatusScheduled,
		ScheduledTime:   time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
		DurationMinutes: 45,
		UserName:        "Ana",
		CoachName:       "Coach",
		MeetingUrl:      "https://meet.example.test/abc,1",
	}
	ics := invite(row, "no-reply@example.test")
	if !strings.Contains(ics, `LOCATION:https://meet.example.test/abc\,1`+"\r\n") {
		t.Errorf("invite lacks escaped LOCATION: %q", ics)
	}
	if !strings.Contains(ics, `Join: https://meet.example.test/abc\,1`) {
		t.Errorf("invite DESCRIPTION lacks join link: %q", ics)
	}
	row.MeetingUrl = ""
	if strings.Contains(invite(row, "no-reply@example.test"), "LOCATION:") {
		t.Error("invite has LOCATION without a meeting url")
	}
}

func TestNormaliseMeetingURL(t *testing.T) {
	for _, ok := range []string{"", "  ", "https://zoom.us/j/1", "http://meet.example.test/x?y=1"} {
		if _, err := normaliseMeetingURL(ok); err != nil {
			t.Errorf("%q: unexpected error %v", ok, err)
		}
	}
	if got, _ := normaliseMeetingURL("  "); got != "" {
		t.Errorf("blank should clear, got %q", got)
	}
	for _, bad := range []string{"zoom.us/j/1", "ftp://x.test", "javascript:alert(1)", "https://", "not a url"} {
		if _, err := normaliseMeetingURL(bad); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%q: expected ErrInvalidInput, got %v", bad, err)
		}
	}
}

func TestReviewsRequireCompletedSession(t *testing.T) {
	svc, pool := testService(t)
	ctx := context.Background()
	coach, client, other := insertCoach(t, pool), insertUser(t, pool, "user"), insertUser(t, pool, "user")

	if _, err := svc.CreateReview(ctx, coach, client, CreateReviewInput{Rating: 5}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("review with no session: err = %v, want ErrForbidden", err)
	}
	if _, err := svc.CreateReview(ctx, coach, coach, CreateReviewInput{Rating: 5}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("coach reviewing themselves: err = %v, want ErrForbidden", err)
	}
	s, err := svc.BookSession(ctx, client, BookInput{CoachID: coach.String(), ScheduledTime: nextSlot()})
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	sid := uuid.MustParse(s.ID)
	if _, err := svc.RespondAsCoach(ctx, sid, coach, "confirm"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := svc.CreateReview(ctx, coach, client, CreateReviewInput{Rating: 5}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("review of scheduled (not completed) session: err = %v, want ErrForbidden", err)
	}
	if _, err := svc.SetStatus(ctx, sid, coach, StatusCompleted); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if _, err := svc.CreateReview(ctx, coach, other, CreateReviewInput{SessionID: &sid, Rating: 5}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stranger citing someone else's session: err = %v, want ErrForbidden", err)
	}
	if _, err := svc.CreateReview(ctx, coach, client, CreateReviewInput{Rating: 6}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("rating 6: err = %v, want ErrInvalidInput", err)
	}
	first, err := svc.CreateReview(ctx, coach, client, CreateReviewInput{SessionID: &sid, Rating: 4, Comment: "  solid  "})
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if first.Rating != 4 || first.Comment != "solid" || first.SessionID != s.ID {
		t.Fatalf("review = %+v", first)
	}
	// A second review by the same client replaces the first rather than adding one.
	second, err := svc.CreateReview(ctx, coach, client, CreateReviewInput{Rating: 2})
	if err != nil {
		t.Fatalf("re-review: %v", err)
	}
	if second.ID != first.ID || second.Rating != 2 {
		t.Fatalf("second review = %+v, want same id with rating 2", second)
	}
	reviews, err := svc.ListReviews(ctx, coach, 10, 0)
	if err != nil || len(reviews) != 1 || reviews[0].Rating != 2 || reviews[0].ReviewerName == "" {
		t.Fatalf("list = %+v, %v", reviews, err)
	}
	c, err := svc.GetCoach(ctx, coach)
	if err != nil || c.ReviewCount != 1 || c.AvgRating != 2 {
		t.Fatalf("coach aggregates = %+v, %v", c, err)
	}
}
