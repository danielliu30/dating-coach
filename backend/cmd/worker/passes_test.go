package main

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danielliu30/dating-coach/backend/internal/account"
	"github.com/danielliu30/dating-coach/backend/internal/coaching"
	"github.com/danielliu30/dating-coach/backend/internal/notify"
	"github.com/danielliu30/dating-coach/backend/internal/payments"
	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

func TestSinglePassTasksRespectCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pool, err := pgxpool.New(context.Background(), "postgres://user:pass@127.0.0.1:1/db")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	q := db.New(pool)
	if _, err := account.NewWorker(q, nil).PurgeRefreshTokens(ctx); err == nil {
		t.Error("purge ignored cancellation")
	}
	if _, err := notify.NewRelay(q, nil).CheckExhausted(ctx); err == nil {
		t.Error("email check ignored cancellation")
	}
	provider, err := payments.New(false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	svc := coaching.NewService(pool, q, provider, time.Minute, "", "", nil)
	if _, err := expireBookingsPass(ctx, svc); err == nil {
		t.Error("expiry ignored cancellation")
	}
	if _, err := sweepPaymentsPass(ctx, svc); err == nil {
		t.Error("payment sweep ignored cancellation")
	}
}
