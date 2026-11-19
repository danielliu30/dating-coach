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
