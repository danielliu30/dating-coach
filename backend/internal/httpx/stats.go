package httpx

import (
	"net/http"
	"runtime"
	"time"
)

// Stats surfaces liveness/runtime data at GET /stats for the self-hosted
// metrics dashboard. It is deliberately read-only: everything it reports is
// sampled when the endpoint is scraped, so it adds zero work to the request
// path. Request counts and status classes come from the nginx access log —
// the edge is already in-path — not from in-process middleware.
type Stats struct {
	started     time.Time
	socketCount func() int
}

// NewStats starts the uptime clock; socketCount may be nil.
func NewStats(socketCount func() int) *Stats {
	return &Stats{started: time.Now(), socketCount: socketCount}
}

// Handler serves the JSON snapshot consumed by the metrics dashboard.
func (s *Stats) Handler(w http.ResponseWriter, _ *http.Request) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	body := map[string]any{
		"uptime_seconds": int64(time.Since(s.started).Seconds()),
		"goroutines":     runtime.NumGoroutine(),
		"mem_alloc_mb":   mem.Alloc >> 20,
	}
	if s.socketCount != nil {
		body["ws_connections"] = s.socketCount()
	}
	JSON(w, http.StatusOK, body)
}
