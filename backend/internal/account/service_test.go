package account

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// recorder captures the deletion sequence, optionally failing the publish.
type recorder struct {
	steps      []string
	publishErr error
}

func (r *recorder) Revoke(_ context.Context, userID uuid.UUID) error {
	r.steps = append(r.steps, "revoke:"+userID.String())
	return nil
}

func (r *recorder) Restore(_ context.Context, userID uuid.UUID) error {
	r.steps = append(r.steps, "restore:"+userID.String())
	return nil
}

func (r *recorder) Publish(_ context.Context, job Job) error {
	r.steps = append(r.steps, "publish:"+job.UserID)
	return r.publishErr
}

func TestDeleteRevokesBeforeQueueing(t *testing.T) {
	rec := &recorder{}
	userID := uuid.New()
	if err := NewService(rec, rec).Delete(context.Background(), userID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	want := []string{"revoke:" + userID.String(), "publish:" + userID.String()}
	if len(rec.steps) != len(want) || rec.steps[0] != want[0] || rec.steps[1] != want[1] {
		t.Fatalf("steps = %v, want %v", rec.steps, want)
	}
}

func TestDeleteUndoesRevocationWhenQueueingFails(t *testing.T) {
	rec := &recorder{publishErr: errors.New("broker down")}
	userID := uuid.New()
	err := NewService(rec, rec).Delete(context.Background(), userID)
	if err == nil {
		t.Fatal("expected a publish failure to be reported")
	}
	// Without the rollback the account could never retry: its tokens, including
	// freshly issued ones, would stay denied while its rows still exist.
	if len(rec.steps) != 3 || rec.steps[2] != "restore:"+userID.String() {
		t.Fatalf("steps = %v, want the revocation to be undone", rec.steps)
	}
}
