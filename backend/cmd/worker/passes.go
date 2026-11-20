package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/danielliu30/dating-coach/backend/internal/coaching"
)

func expireBookingsPass(ctx context.Context, svc *coaching.Service) (int, error) {
	n, err := svc.ExpirePending(ctx)
	if err != nil {
		slog.Error("expire session requests", "error", err)
	} else if n > 0 {
		slog.Info("expired unanswered session requests", "count", n)
	}
	return n, err
}

func sweepPaymentsPass(ctx context.Context, svc *coaching.Service) (coaching.SweepResult, error) {
	res, err := svc.SweepPayments(ctx, time.Now())
	if err != nil {
		slog.Error("sweep payments", "error", err)
	}
	if res != (coaching.SweepResult{}) {
		slog.Info("swept payments", "holds_released", res.HoldsReleased, "checkouts_expired", res.CheckoutsExpired, "authorizations_released", res.AuthorizationsFreed, "refunds_issued", res.RefundsIssued)
	}
	return res, err
}
