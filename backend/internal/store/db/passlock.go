package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// WithExclusivePass runs a pass in a transaction, or skips it if another
// instance holds the named lock. Completed writes commit even on callback errors.
func (q *Queries) WithExclusivePass(ctx context.Context, name string, run func(*Queries) error) error {
	pool, ok := q.db.(*pgxpool.Pool)
	if !ok {
		return fmt.Errorf("exclusive pass %s requires a connection pool", name)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	var locked bool
	if err := tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(hashtextextended($1, 0))", "dating-coach:"+name).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return nil
	}
	runErr := run(New(tx))
	commitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return errors.Join(runErr, tx.Commit(commitCtx))
}
