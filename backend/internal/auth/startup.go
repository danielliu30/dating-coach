package auth

import (
	"context"
	"log/slog"
)

// ReplayRevocationsOnStartup prepares the denylist for request-driven services.
// Failed replays leave it fail-closed; the scheduled replay task retries later.
func ReplayRevocationsOnStartup(ctx context.Context, backend string, denylist *Denylist, lister SessionRevocationLister) {
	if backend != "pubsub" {
		return
	}
	if err := denylist.ReplayRevocations(ctx, lister); err != nil {
		slog.Error("startup revocation replay failed; scheduled replay will retry", "error", err)
	}
}
