// Package store owns the PostgreSQL connection pool and the generated queries.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// Store is the database handle passed to the services: Queries for the generated
// statements, and Pool for the cases that need an explicit transaction.
type Store struct {
	Pool    *pgxpool.Pool
	Queries *db.Queries
}

// OpenOption customizes the pool config before it connects.
type OpenOption func(*pgxpool.Config)

// WithRelaxedIterativeScan sets hnsw.iterative_scan = relaxed_order on every
// pooled connection, so the worker's filtered pgvector similarity search keeps
// scanning the hnsw index until enough rows pass the owner/kind filters instead
// of stopping at the first batch. On a pgvector too old to know the GUC the
// SET is a harmless placeholder, so this is safe to keep unconditional.
func WithRelaxedIterativeScan() OpenOption {
	return func(cfg *pgxpool.Config) {
		cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
			_, err := conn.Exec(ctx, "SET hnsw.iterative_scan = relaxed_order")
			return err
		}
	}
}

// Open creates a pgx pool and pings it, so a misconfigured database fails at
// startup rather than on the first request.
func Open(ctx context.Context, dsn string, opts ...OpenOption) (*Store, error) {
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	poolCfg.MaxConns = 10
	poolCfg.MaxConnLifetime = time.Hour
	for _, opt := range opts {
		opt(poolCfg)
	}

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

// Close drains the pool; deferred by cmd/api and cmd/worker.
func (s *Store) Close() {
	s.Pool.Close()
}
