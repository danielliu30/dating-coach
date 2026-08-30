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

func TestDeleteKeepsRevocationWhenQueueingFails(t *testing.T) {
	rec := &recorder{publishErr: errors.New("broker down")}
	err := NewService(rec, rec).Delete(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("expected a publish failure to be reported")
	}
	if len(rec.steps) != 2 {
		t.Fatalf("steps = %v, want the revocation to have happened first", rec.steps)
	}
}
