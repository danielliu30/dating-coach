package account

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// recorder captures the deletion sequence, optionally failing the publish and
// cancelling the request context the way an abandoned request would.
type recorder struct {
	steps        []string
	publishErr   error
	cancelOnPub  context.CancelFunc
	restoreCtxOK bool
}

func (r *recorder) Revoke(_ context.Context, userID uuid.UUID) error {
	r.steps = append(r.steps, "revoke:"+userID.String())
	return nil
}

func (r *recorder) Restore(ctx context.Context, userID uuid.UUID) error {
	r.steps = append(r.steps, "restore:"+userID.String())
	r.restoreCtxOK = ctx.Err() == nil
	return nil
}

func (r *recorder) Publish(_ context.Context, job Job) error {
	r.steps = append(r.steps, "publish:"+job.UserID)
	if r.cancelOnPub != nil {
		r.cancelOnPub()
	}
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

func TestDeleteUndoesRevocationAfterRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := &recorder{publishErr: context.Canceled, cancelOnPub: cancel}
	userID := uuid.New()
	if err := NewService(rec, rec).Delete(ctx, userID); err == nil {
		t.Fatal("expected a publish failure to be reported")
	}
	if len(rec.steps) != 3 || rec.steps[2] != "restore:"+userID.String() {
		t.Fatalf("steps = %v, want the revocation to be undone", rec.steps)
	}
	if !rec.restoreCtxOK {
		t.Fatal("Restore got an already-cancelled context, so the rollback cannot reach Redis")
	}
}
