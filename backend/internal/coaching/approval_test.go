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
