package coaching

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

type nudgeTx struct {
	pgx.Tx
	committed bool
	err       error
}

func (tx *nudgeTx) Commit(context.Context) error { tx.committed = tx.err == nil; return tx.err }

func TestEmailNudgeAfterCommit(t *testing.T) {
	for _, fail := range []bool{false, true} {
		tx := &nudgeTx{}
		if fail {
			tx.err = errors.New("commit failed")
		}
		called := false
		svc := &Service{}
		svc.SetEmailOutboxNudge(func(context.Context) {
			if !tx.committed {
				t.Fatal("nudge before commit")
			}
			called = true
		})
		err := svc.commit(context.Background(), tx)
		if (err != nil) != fail || called == fail {
			t.Fatalf("fail=%v called=%v err=%v", fail, called, err)
		}
	}
}
