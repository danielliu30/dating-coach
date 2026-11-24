package harness

import (
	"context"
	"testing"
	"time"
)

// A nil *Rate must not outlive its context: unpaced (-rps 0) loops that use
// Wait as their only termination check would otherwise never exit, so the run
// never reaches reporting or fixture cleanup.
func TestNilRateWaitStopsOnCancel(t *testing.T) {
	var r *Rate
	if !r.Wait(context.Background()) {
		t.Fatal("nil rate should return true while ctx is live")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r.Wait(ctx) {
		t.Fatal("nil rate must return false once ctx is done")
	}
}

// A paced Wait must also report ctx expiry rather than blocking forever on
// the ticker channel.
func TestPacedRateWaitStopsOnCancel(t *testing.T) {
	r := NewRate(1000)
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	deadline := time.After(2 * time.Second)
	for r.Wait(ctx) {
		select {
		case <-deadline:
			t.Fatal("paced Wait did not stop after ctx expiry")
		default:
		}
	}
}

// NewRate(<=0) returns nil so callers can treat "unpaced" uniformly.
func TestNewRateNonPositive(t *testing.T) {
	if r := NewRate(0); r != nil {
		t.Fatal("rps=0 must return nil pacer")
	}
	if r := NewRate(-1); r != nil {
		t.Fatal("negative rps must return nil pacer")
	}
}
