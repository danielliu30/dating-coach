package coaching

import (
	"errors"
	"testing"
	"time"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
	"github.com/google/uuid"
)

func TestWallMinuteSkipsNonexistentLocalTime(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	// 2026-03-08 02:30 does not exist: clocks jump 02:00 -> 03:00.
	gap := time.Date(2026, 3, 8, 2, 30, 0, 0, loc)
	if got := wallMinute(gap, loc); got == 2*60+30 {
		t.Fatalf("expected nonexistent 02:30 to land elsewhere, got minute %d", got)
	}
	ok := time.Date(2026, 3, 8, 9, 15, 0, 0, loc)
	if got, want := wallMinute(ok, loc), int32(9*60+15); got != want {
		t.Fatalf("wallMinute = %d, want %d", got, want)
	}
}

func TestOverlapsBookedHonoursExclusion(t *testing.T) {
	booked := uuid.New()
	start := time.Date(2026, 6, 1, 10, 15, 0, 0, time.UTC)
	rows := []db.ListBookedSlotsRow{{
		ID:              booked,
		ScheduledTime:   start,
		DurationMinutes: 45,
	}}
	probe := start.Add(30 * time.Minute)
	if !overlapsBooked(probe, 45, rows, nil) {
		t.Fatal("expected overlap with the booked session")
	}
	if overlapsBooked(probe, 45, rows, &booked) {
		t.Fatal("expected the excluded session to be ignored")
	}
	after := start.Add(45 * time.Minute)
	if overlapsBooked(after, 45, rows, nil) {
		t.Fatal("a slot starting when the session ends does not overlap")
	}
}

func TestNormalisePhases(t *testing.T) {
	got, err := normalisePhases([]string{" First_Date ", "opening", "first_date", ""})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0] != "opening" || got[1] != "first_date" {
		t.Fatalf("expected deduplicated vocabulary order [opening first_date], got %v", got)
	}

	got, err = normalisePhases(nil)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("expected empty non-nil slice for nil input, got %v, %v", got, err)
	}

	if _, err := normalisePhases([]string{"opening", "ghosting"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for unknown phase, got %v", err)
	}
}
