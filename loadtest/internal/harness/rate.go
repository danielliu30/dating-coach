package harness

import (
	"context"
	"time"
)

// Rate is a shared open-loop pacer: one ticker feeds every worker in a
// stage, so the aggregate action rate is fixed regardless of how many VUs
// are listening. Ticks a slow stage cannot consume are dropped — the Rate
// never queues bursts, which keeps measured rps honest instead of letting a
// backlog masquerade as throughput.
type Rate struct {
	ticker *time.Ticker
}

// NewRate returns a pacer emitting rps ticks per second, or nil when rps <= 0
// (callers treat nil as "unpaced" and skip Wait).
func NewRate(rps float64) *Rate {
	if rps <= 0 {
		return nil
	}
	interval := time.Duration(float64(time.Second) / rps)
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	return &Rate{ticker: time.NewTicker(interval)}
}

// Wait blocks until the next tick or ctx ends; the bool reports which. A nil
// *Rate always returns true immediately so callers can use it unconditionally.
func (r *Rate) Wait(ctx context.Context) bool {
	if r == nil {
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case <-r.ticker.C:
		return true
	}
}

// Close stops the ticker. Safe on nil.
func (r *Rate) Close() {
	if r != nil {
		r.ticker.Stop()
	}
}
