package notify

import (
	"context"
	"errors"
	"testing"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

type nudgeSaver struct {
	saved bool
	err   error
}

func (s *nudgeSaver) EnqueueEmail(context.Context, db.EnqueueEmailParams) (db.EmailOutbox, error) {
	s.saved = s.err == nil
	return db.EmailOutbox{}, s.err
}

type nudgePublisher struct {
	t      *testing.T
	saver  *nudgeSaver
	called bool
	err    error
}

func (p *nudgePublisher) Publish(context.Context, struct{}) error {
	if !p.saver.saved {
		p.t.Fatal("nudge before committed outbox save")
	}
	p.called = true
	return p.err
}

func TestOutboxNudgeAfterSave(t *testing.T) {
	for _, tt := range []struct {
		name                string
		saveErr, publishErr error
	}{
		{"success", nil, nil}, {"save failure", errors.New("database unavailable"), nil}, {"publish failure", nil, errors.New("broker unavailable")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &nudgeSaver{err: tt.saveErr}
			p := &nudgePublisher{t: t, saver: s, err: tt.publishErr}
			n := &OutboxNotifier{Queries: s, Publisher: p}
			err := n.Email(context.Background(), "a@b.test", "subject", "body")
			if (err != nil) != (tt.saveErr != nil) || p.called != (tt.saveErr == nil) {
				t.Fatalf("err=%v called=%v", err, p.called)
			}
		})
	}
}
