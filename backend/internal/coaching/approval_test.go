package coaching

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// setApproval writes a coach's approval_status directly, bypassing the service.
func setApproval(t *testing.T, pool *pgxpool.Pool, coach uuid.UUID, status string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		"UPDATE coaches SET approval_status = $2 WHERE user_id = $1", coach, status); err != nil {
		t.Fatalf("set approval: %v", err)
	}
}

// listedIDs returns the ids of every coach in the directory.
func listedIDs(t *testing.T, svc *Service) map[string]bool {
	t.Helper()
	ids := map[string]bool{}
	for offset := int32(0); ; offset += 100 {
		page, err := svc.ListCoaches(context.Background(), 100, offset, false, nil)
		if err != nil {
			t.Fatalf("list coaches: %v", err)
		}
		for _, c := range page {
			ids[c.ID] = true
		}
		if len(page) < 100 {
			return ids
		}
	}
}

func TestNewCoachIsPendingAndHiddenUntilApproved(t *testing.T) {
	svc, pool := testService(t)
	ctx := context.Background()
	coachID := insertUser(t, pool, "coach")

	coach, err := svc.UpsertProfile(ctx, coachID, UpsertProfileInput{Headline: "new", AcceptingClients: true})
	if err != nil {
		t.Fatalf("upsert profile: %v", err)
	}
	if coach.ApprovalStatus != ApprovalPending {
		t.Fatalf("fresh profile approval_status = %q, want %q", coach.ApprovalStatus, ApprovalPending)
	}
	if listedIDs(t, svc)[coach.ID] {
		t.Fatal("pending coach must not be listed")
	}

	setApproval(t, pool, coachID, ApprovalApproved)
	if !listedIDs(t, svc)[coach.ID] {
		t.Fatal("approved coach must be listed")
	}
	// Saving the profile again must not reset the approval.
	coach, err = svc.UpsertProfile(ctx, coachID, UpsertProfileInput{Headline: "edited", AcceptingClients: true})
	if err != nil {
		t.Fatalf("re-upsert profile: %v", err)
	}
	if coach.ApprovalStatus != ApprovalApproved {
		t.Fatalf("approval_status after edit = %q, want %q", coach.ApprovalStatus, ApprovalApproved)
	}

	setApproval(t, pool, coachID, ApprovalRejected)
	if listedIDs(t, svc)[coach.ID] {
		t.Fatal("rejected coach must not be listed")
	}
	if got, err := svc.GetCoach(ctx, coachID); err != nil || got.ApprovalStatus != ApprovalRejected {
		t.Fatalf("GetCoach = %+v, %v; want approval_status rejected", got, err)
	}
}

// queue returns one admin approval queue keyed by coach id.
func queue(t *testing.T, svc *Service, status string) map[string]AdminCoach {
	t.Helper()
	out := map[string]AdminCoach{}
	for offset := int32(0); ; offset += 100 {
		page, err := svc.ListCoachesByApproval(context.Background(), status, 100, offset)
		if err != nil {
			t.Fatalf("list %s coaches: %v", status, err)
		}
		for _, c := range page {
			out[c.ID] = c
		}
		if len(page) < 100 {
			return out
		}
	}
}

func TestAdminApprovalQueueAndDecision(t *testing.T) {
	svc, pool := testService(t)
	ctx := context.Background()
	coachID, client := insertUser(t, pool, "coach"), insertUser(t, pool, "user")
	if _, err := svc.UpsertProfile(ctx, coachID, UpsertProfileInput{Headline: "applicant", AcceptingClients: true}); err != nil {
		t.Fatalf("upsert profile: %v", err)
	}
	if _, err := svc.SetAvailability(ctx, coachID, []AvailabilityWindow{
		{Weekday: 0, StartMinute: 0, EndMinute: 1440}, {Weekday: 1, StartMinute: 0, EndMinute: 1440},
		{Weekday: 2, StartMinute: 0, EndMinute: 1440}, {Weekday: 3, StartMinute: 0, EndMinute: 1440},
		{Weekday: 4, StartMinute: 0, EndMinute: 1440}, {Weekday: 5, StartMinute: 0, EndMinute: 1440},
		{Weekday: 6, StartMinute: 0, EndMinute: 1440},
	}); err != nil {
		t.Fatalf("set availability: %v", err)
	}
	id := coachID.String()

	if _, err := svc.ListCoachesByApproval(ctx, "banned", 10, 0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("list with bogus status: err = %v, want ErrInvalidInput", err)
	}
	if _, err := svc.SetCoachApproval(ctx, coachID, "banned"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("set bogus status: err = %v, want ErrInvalidInput", err)
	}
	if _, err := svc.SetCoachApproval(ctx, client, ApprovalApproved); !errors.Is(err, ErrNotFound) {
		t.Fatalf("approve a non-coach: err = %v, want ErrNotFound", err)
	}

	if applicant, ok := queue(t, svc, ApprovalPending)[id]; !ok || applicant.Email == "" || applicant.DisplayName == "" {
		t.Fatalf("new coach missing from pending queue or lacks contact details: %+v", applicant)
	}
	if listedIDs(t, svc)[id] {
		t.Fatal("pending coach must not be in the directory")
	}
	if _, err := svc.BookSession(ctx, client, BookInput{CoachID: id, ScheduledTime: nextSlot()}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("booking a pending coach: err = %v, want ErrUnavailable", err)
	}

	coach, err := svc.SetCoachApproval(ctx, coachID, ApprovalApproved)
	if err != nil || coach.ApprovalStatus != ApprovalApproved {
		t.Fatalf("approve = %+v, %v", coach, err)
	}
	if _, pending := queue(t, svc, ApprovalPending)[id]; pending {
		t.Fatal("approved coach must leave the pending queue")
	}
	if _, approved := queue(t, svc, ApprovalApproved)[id]; !approved {
		t.Fatal("approved coach should move from the pending to the approved queue")
	}
	if !listedIDs(t, svc)[id] {
		t.Fatal("approved coach must be in the directory")
	}
	if _, err := svc.BookSession(ctx, client, BookInput{CoachID: id, ScheduledTime: nextSlot()}); err != nil {
		t.Fatalf("booking an approved coach: %v", err)
	}

	coach, err = svc.SetCoachApproval(ctx, coachID, ApprovalRejected)
	if err != nil || coach.ApprovalStatus != ApprovalRejected {
		t.Fatalf("reject = %+v, %v", coach, err)
	}
	if _, rejected := queue(t, svc, ApprovalRejected)[id]; listedIDs(t, svc)[id] || !rejected {
		t.Fatal("rejected coach must be hidden and sit in the rejected queue")
	}
}

func TestOnlyApprovedCoachesAreBookable(t *testing.T) {
	svc, pool := testService(t)
	ctx := context.Background()
	coach, client := insertCoach(t, pool), insertUser(t, pool, "user")
	start := nextSlot()

	for _, status := range []string{ApprovalPending, ApprovalRejected} {
		setApproval(t, pool, coach, status)
		if _, err := svc.BookSession(ctx, client, BookInput{CoachID: coach.String(), ScheduledTime: start}); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("booking a %s coach: err = %v, want ErrUnavailable", status, err)
		}
	}

	setApproval(t, pool, coach, ApprovalApproved)
	if _, err := svc.BookSession(ctx, client, BookInput{CoachID: coach.String(), ScheduledTime: start}); err != nil {
		t.Fatalf("booking an approved coach: %v", err)
	}
}
