package harness

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Stage is one level of a ramp: VUs concurrent workers for Duration.
type Stage struct {
	Name     string
	VUs      int
	Duration time.Duration
}

// Result describes how a ramp ended.
type Result struct {
	LastGood Stage  `json:"last_good_stage"`
	Tripped  bool   `json:"tripped"`
	Reason   string `json:"reason"`
}

// Tripwire is evaluated after each stage; returning true stops the ramp.
// reason names what broke so the report can say which wall was hit.
type Tripwire func(m *Metrics) (tripped bool, reason string)

// ErrorRateTripwire trips when transport errors + 5xx exceed max of all
// observations.
func ErrorRateTripwire(max float64) Tripwire {
	return func(m *Metrics) (bool, string) {
		if r := m.ErrorRate(); r > max {
			return true, fmt.Sprintf("error rate %.1f%% exceeded %.1f%%", r*100, max*100)
		}
		return false, ""
	}
}

// Run executes each stage in order: VUs workers call fn in a loop until the
// stage duration elapses, then the tripwire is checked. It returns the last
// stage that stayed healthy, or the stage that tripped.
func Run(ctx context.Context, m *Metrics, stages []Stage, fn func(ctx context.Context, vu int), trip Tripwire) Result {
	if len(stages) == 0 {
		return Result{Reason: "no stages configured"}
	}
	var lastGood Stage
	for _, s := range stages {
		stageCtx, cancel := context.WithTimeout(ctx, s.Duration)
		var wg sync.WaitGroup
		for i := 0; i < s.VUs; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				fn(stageCtx, id)
			}(i)
		}
		wg.Wait()
		cancel()
		if tripped, reason := trip(m); tripped {
			m.Note("tripwire at stage %s (%d VUs): %s", s.Name, s.VUs, reason)
			return Result{LastGood: lastGood, Tripped: true, Reason: fmt.Sprintf("stage %s (%d VUs): %s", s.Name, s.VUs, reason)}
		}
		lastGood = s
		m.Note("stage %s (%d VUs) clean", s.Name, s.VUs)
	}
	return Result{LastGood: lastGood, Reason: "completed all stages"}
}

// Workers runs fn per VU until ctx ends — the building block inside one stage
// for components that don't use Run.
func Workers(ctx context.Context, vus int, fn func(ctx context.Context, vu int)) {
	var wg sync.WaitGroup
	for i := 0; i < vus; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			fn(ctx, id)
		}(i)
	}
	wg.Wait()
}

// Pace sleeps until the next tick of interval; it is the simplest fixed-rate
// loop a VU can use inside a stage.
func Pace(ctx context.Context, interval time.Duration) {
	t := time.NewTimer(interval)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
