package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// WithExclusivePass runs a pass on one connection, or skips it if another
// instance holds the named lock. Each write commits independently. The database
// connection must support session locks (not transaction-mode PgBouncer).
func (q *Queries) WithExclusivePass(ctx context.Context, name string, run func(*Queries) error) error {
	pool, ok := q.db.(*pgxpool.Pool)
	if !ok {
		return fmt.Errorf("exclusive pass %s requires a connection pool", name)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1, 0))", "dating-coach:"+name).Scan(&locked); err != nil {
		// Cancellation may race with the server acquiring the session lock.
		_ = conn.Conn().Close(context.Background())
		return err
	}
	if !locked {
		return nil
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var unlocked bool
		err := conn.QueryRow(unlockCtx, "SELECT pg_advisory_unlock(hashtextextended($1, 0))", "dating-coach:"+name).Scan(&unlocked)
		if err != nil || !unlocked {
			_ = conn.Conn().Close(unlockCtx)
		}
	}()
	return run(New(conn))
}
