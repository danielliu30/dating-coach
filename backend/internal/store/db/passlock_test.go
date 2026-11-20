package db

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestExclusivePassReleasesAfterErrorAndCancellation(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	q := New(pool)
	want := errors.New("pass failed")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := q.WithExclusivePass(ctx, t.Name(), func(*Queries) error {
		cancel()
		return want
	}); !errors.Is(err, want) {
		t.Fatalf("failed pass = %v", err)
	}
	other, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	called := false
	if err := New(other).WithExclusivePass(context.Background(), t.Name(), func(*Queries) error {
		called = true
		return nil
	}); err != nil || !called {
		t.Fatalf("next pass called=%v err=%v", called, err)
	}
	if err := q.WithExclusivePass(ctx, t.Name(), func(*Queries) error {
		t.Fatal("cancelled pass ran")
		return nil
	}); err == nil {
		t.Fatal("cancelled pass succeeded")
	}
}

func TestExclusivePassKeepsCompletedWritesAfterDisconnect(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	q := New(pool)
	row, err := q.EnqueueEmail(ctx, EnqueueEmailParams{ToEmail: t.Name() + "@example.test", Subject: "s", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, "DELETE FROM email_outbox WHERE id = $1", row.ID) }()
	err = q.WithExclusivePass(ctx, t.Name(), func(locked *Queries) error {
		if _, err := locked.MarkEmailSent(ctx, row.ID); err != nil {
			return err
		}
		var terminated bool
		return locked.db.QueryRow(ctx, "SELECT pg_terminate_backend(pg_backend_pid())").Scan(&terminated)
	})
	if err == nil {
		t.Fatal("expected a disconnected database session")
	}
	var sent bool
	if err := pool.QueryRow(ctx, "SELECT sent_at IS NOT NULL FROM email_outbox WHERE id = $1", row.ID).Scan(&sent); err != nil || !sent {
		t.Fatalf("completed row sent=%v err=%v", sent, err)
	}
	called := false
	if err := q.WithExclusivePass(ctx, t.Name(), func(*Queries) error { called = true; return nil }); err != nil || !called {
		t.Fatalf("lock after disconnect: called=%v err=%v", called, err)
	}
}
