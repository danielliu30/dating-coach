package coaching

import (
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
