package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ProgressEvery is the standard live-status tick for all components.
const ProgressEvery = 2 * time.Second

// flagWords are the counter-name fragments that indicate correctness
// problems; Progress highlights them live and they drive the summary view.
var flagWords = []string{
	"fail", "desync", "cross", "leak", "missing", "mismatch",
	"conflict", "unexpected", "violation", "foreign", "dup",
}

// Flagged returns the counters whose names carry a correctness signal and
// whose value is nonzero.
func Flagged(counts map[string]int64) map[string]int64 {
	out := map[string]int64{}
	for k, v := range counts {
		if v == 0 {
			continue
		}
		for _, w := range flagWords {
			if strings.Contains(k, w) {
				out[k] = v
				break
			}
		}
	}
	return out
}

// Counters returns a copy of every counter set so far.
func (m *Metrics) Counters() map[string]int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return copyCounts(m.count)
}

// LiveState is the per-tick view written to live-<component>.json for the
// dashboard and printed to the console.
type LiveState struct {
	Component string           `json:"component"`
	ElapsedS  float64          `json:"elapsed_s"`
	ObsTotal  int              `json:"obs_total"`
	ObsRate   float64          `json:"obs_rate"`
	Counters  map[string]int64 `json:"counters"`
	Flags     map[string]int64 `json:"flags"`
	UpdatedAt string           `json:"updated_at"`
}

func (m *Metrics) live(name string, obsTotal int, obsRate float64) LiveState {
	counts := m.Counters()
	return LiveState{
		Component: name,
		ElapsedS:  time.Since(m.start).Seconds(),
		ObsTotal:  obsTotal,
		ObsRate:   obsRate,
		Counters:  counts,
		Flags:     Flagged(counts),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

// Progress prints a one-line rolling status to stderr every `every` until ctx
// ends: elapsed, total observations, instantaneous observation rate, and any
// flagged counters. When liveDir is non-empty it also atomically rewrites
// <liveDir>/live-<name>.json each tick for lt-dash to serve.
func (m *Metrics) Progress(ctx context.Context, name string, every time.Duration, liveDir string) {
	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		var lastObs int
		var lastAt time.Time = m.start
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				obs := 0
				for _, e := range m.Snapshot() {
					obs += e.Count
				}
				dt := now.Sub(lastAt).Seconds()
				rate := 0.0
				if dt > 0 {
					rate = float64(obs-lastObs) / dt
				}
				lastObs, lastAt = obs, now

				st := m.live(name, obs, rate)
				flags := ""
				if len(st.Flags) > 0 {
					keys := make([]string, 0, len(st.Flags))
					for k := range st.Flags {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					parts := make([]string, 0, len(keys))
					for _, k := range keys {
						parts = append(parts, fmt.Sprintf("%s=%d", k, st.Flags[k]))
					}
					flags = " flags=" + strings.Join(parts, ",")
				}
				fmt.Fprintf(os.Stderr, "[%s] t=%5.1fs obs=%-7d rate=%6.1f/s%s\n",
					name, st.ElapsedS, obs, rate, flags)

				if liveDir != "" {
					if data, err := json.Marshal(st); err == nil {
						tmp := filepath.Join(liveDir, ".live-"+name+".tmp")
						if err := os.MkdirAll(liveDir, 0o755); err == nil &&
							os.WriteFile(tmp, data, 0o644) == nil {
							_ = os.Rename(tmp, filepath.Join(liveDir, "live-"+name+".json"))
						}
					}
				}
			}
		}
	}()
}

// ReadReport parses a component JSON report written by WriteReport.
func ReadReport(path string) (*Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rep Report
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, err
	}
	return &rep, nil
}

// LatestReports returns the newest report path per component in dir,
// ignoring live-* snapshots and any non-report JSON.
func LatestReports(dir string) (map[string]string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	latest := map[string]string{}
	latestMT := map[string]int64{}
	for _, p := range paths {
		base := strings.TrimSuffix(filepath.Base(p), ".json")
		if strings.HasPrefix(base, "live-") || strings.HasPrefix(base, ".") {
			continue
		}
		i := strings.LastIndexByte(base, '-')
		if i < 0 {
			continue
		}
		comp := base[:i]
		if st, err := os.Stat(p); err == nil && st.ModTime().UnixNano() > latestMT[comp] {
			latestMT[comp] = st.ModTime().UnixNano()
			latest[comp] = p
		}
	}
	return latest, nil
}
