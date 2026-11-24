package httpx

import (
	"net/http"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// Stats is a lightweight in-process request/liveness counter surfaced at
// GET /stats for the self-hosted metrics dashboard. Counters are atomics —
// it adds no allocation to the request path.
type Stats struct {
	started     time.Time
	total       atomic.Int64
	status2xx   atomic.Int64
	status4xx   atomic.Int64
	status5xx   atomic.Int64
	socketCount func() int
}

// NewStats starts the uptime clock; socketCount may be nil.
func NewStats(socketCount func() int) *Stats {
	return &Stats{started: time.Now(), socketCount: socketCount}
}

// Middleware tallies every request by response class.
func (s *Stats) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		s.total.Add(1)
		switch status := ww.Status(); {
		case status < 400:
			s.status2xx.Add(1)
		case status < 500:
			s.status4xx.Add(1)
		default:
			s.status5xx.Add(1)
		}
	})
}

// Handler serves the JSON snapshot consumed by the metrics dashboard.
func (s *Stats) Handler(w http.ResponseWriter, _ *http.Request) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	body := map[string]any{
		"uptime_seconds": int64(time.Since(s.started).Seconds()),
		"goroutines":     runtime.NumGoroutine(),
		"mem_alloc_mb":   mem.Alloc >> 20,
		"requests_total": s.total.Load(),
		"responses_2xx":  s.status2xx.Load(),
		"responses_4xx":  s.status4xx.Load(),
		"responses_5xx":  s.status5xx.Load(),
	}
	if s.socketCount != nil {
		body["ws_connections"] = s.socketCount()
	}
	JSON(w, http.StatusOK, body)
}
