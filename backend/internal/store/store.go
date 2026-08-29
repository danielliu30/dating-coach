// Package store owns the PostgreSQL connection pool and the generated queries.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

type Store struct {
	Pool    *pgxpool.Pool
	Queries *db.Queries
}

// Open creates a pgx pool and pings it.
func Open(ctx context.Context, dsn string) (*Store, error) {
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	poolCfg.MaxConns = 10
	poolCfg.MaxConnLifetime = time.Hour

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return &Store{Pool: pool, Queries: db.New(pool)}, nil
}

func (s *Store) Close() {
	s.Pool.Close()
}
