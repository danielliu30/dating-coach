package harness

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Metrics aggregates every observation a component makes: per-endpoint call
// counts and latencies, free-form counters, and finding notes. Safe for
// concurrent use by every VU sharing the collector.
type Metrics struct {
	mu    sync.Mutex
	eps   map[string]*endpoint
	count map[string]int64
	notes []string
	start      time.Time
	stage      string
	vus        int
	stageIdx   int
	stageTotal int
	stageStart time.Time
	stageDur   time.Duration
}

type endpoint struct {
	statuses map[int]int
	lats     []float64 // seconds
}

// EndpointStats is the exported snapshot of one endpoint.
type EndpointStats struct {
	Count    int         `json:"count"`
	Statuses map[int]int `json:"statuses"`
	P50      float64     `json:"p50_ms"`
	P95      float64     `json:"p95_ms"`
	P99      float64     `json:"p99_ms"`
	Max      float64     `json:"max_ms"`
}

// NewMetrics starts an empty collector.
func NewMetrics() *Metrics {
	return &Metrics{
		eps:   map[string]*endpoint{},
		count: map[string]int64{},
		start: time.Now(),
	}
}

// Observe records one call: status is the HTTP status, or 0 for a transport
// error. lat is wire latency.
func (m *Metrics) Observe(label string, status int, lat time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.eps[label]
	if e == nil {
		e = &endpoint{statuses: map[int]int{}}
		m.eps[label] = e
	}
	e.statuses[status]++
	e.lats = append(e.lats, lat.Seconds())
}

// Inc adds one to a named counter (sends, echoes received, violations...).
func (m *Metrics) Inc(name string) { m.Add(name, 1) }

// Add adds n to a named counter.
func (m *Metrics) Add(name string, n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.count[name] += n
}

// Count reads a named counter.
func (m *Metrics) Count(name string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.count[name]
}

// SetStage records the stage currently running so live views can show the
// active concurrency level (how many virtual users are driving the load),
// where in the ramp it is (idx/total), and how far into the stage duration.
func (m *Metrics) SetStage(idx, total int, name string, vus int, dur time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stage, m.vus = name, vus
	m.stageIdx, m.stageTotal = idx, total
	m.stageStart, m.stageDur = time.Now(), dur
}

// SetPhase labels a non-ramped phase (fan-out check, race probe, ...) so live
// views show what the component is doing between stages. VUs is set to 0.
func (m *Metrics) SetPhase(name string) {
	m.SetStage(0, 0, name, 0, 0)
}

// StageInfo returns the running stage's label, position (idx/total), VU
// count, and seconds elapsed / allotted (0 when outside a stage).
func (m *Metrics) StageInfo() (name string, idx, total, vus int, elapsed, dur float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	name, idx, total, vus = m.stage, m.stageIdx, m.stageTotal, m.vus
	dur = m.stageDur.Seconds()
	if !m.stageStart.IsZero() {
		elapsed = time.Since(m.stageStart).Seconds()
	}
	return
}

// Note appends a free-form finding line to the report.
func (m *Metrics) Note(format string, args ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.notes = append(m.notes, fmt.Sprintf(format, args...))
}

// ErrorRate is the fraction of observations that were transport failures or
// 5xx responses — the default tripwire signal.
func (m *Metrics) ErrorRate() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var total, bad int
	for _, e := range m.eps {
		for status, n := range e.statuses {
			if status < 0 {
				continue // canceled at stage end: neither success nor failure
			}
			total += n
			if status == 0 || status >= 500 {
				bad += n
			}
		}
	}
	if total == 0 {
		return 0
	}
	return float64(bad) / float64(total)
}

// P99 returns the p99 latency (ms) across an endpoint's calls, or one label of
// "" for everything.
func (m *Metrics) P99(label string) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var lats []float64
	for l, e := range m.eps {
		if label != "" && l != label {
			continue
		}
		lats = append(lats, e.lats...)
	}
	if len(lats) == 0 {
		return 0
	}
	return percentile(lats, 0.99) * 1000
}

func percentile(sorted []float64, p float64) float64 {
	cp := append([]float64(nil), sorted...)
	sort.Float64s(cp)
	if len(cp) == 0 {
		return 0
	}
	idx := int(math.Ceil(p*float64(len(cp)))) - 1
	if idx < 0 {
		idx = 0
	}
	return cp[idx]
}

// Snapshot copies the current per-endpoint stats for reporting.
func (m *Metrics) Snapshot() map[string]EndpointStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]EndpointStats, len(m.eps))
	for label, e := range m.eps {
		out[label] = EndpointStats{
			Count:    len(e.lats),
			Statuses: appendStatusCopy(e.statuses),
			P50:      percentile(e.lats, 0.50) * 1000,
			P95:      percentile(e.lats, 0.95) * 1000,
			P99:      percentile(e.lats, 0.99) * 1000,
			Max:      maxLat(e.lats) * 1000,
		}
	}
	return out
}

func copyCounts(in map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func appendStatusCopy(in map[int]int) map[int]int {
	out := make(map[int]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func maxLat(l []float64) float64 {
	var mx float64
	for _, v := range l {
		if v > mx {
			mx = v
		}
	}
	return mx
}

// Report is the JSON document each component writes to loadtest/out/.
type Report struct {
	Component  string                   `json:"component"`
	StartedAt  time.Time                `json:"started_at"`
	FinishedAt time.Time                `json:"finished_at"`
	ElapsedS   float64                  `json:"elapsed_s"`
	Endpoints  map[string]EndpointStats `json:"endpoints"`
	Counters   map[string]int64         `json:"counters"`
	Notes      []string                 `json:"notes"`
	Extra      map[string]any           `json:"extra,omitempty"`
}

// WriteReport serializes the collector plus component-supplied extras to
// out/<name>-<unix>.json and returns the path.
func (m *Metrics) WriteReport(dir, component string, extra map[string]any) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rep := Report{
		Component:  component,
		StartedAt:  m.start,
		FinishedAt: time.Now(),
		ElapsedS:   time.Since(m.start).Seconds(),
		Endpoints:  map[string]EndpointStats{},
		Counters:   copyCounts(m.count),
		Notes:      append([]string{}, m.notes...),
		Extra:      extra,
	}
	for label, e := range m.eps {
		rep.Endpoints[label] = EndpointStats{
			Count:    len(e.lats),
			Statuses: appendStatusCopy(e.statuses),
			P50:      percentile(e.lats, 0.50) * 1000,
			P95:      percentile(e.lats, 0.95) * 1000,
			P99:      percentile(e.lats, 0.99) * 1000,
			Max:      maxLat(e.lats) * 1000,
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%d.json", component, time.Now().Unix()))
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, data, 0o644)
}
