package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStatsHandlerRuntimeFields(t *testing.T) {
	s := NewStats(func() int { return 3 })
	w := httptest.NewRecorder()
	s.Handler(w, httptest.NewRequest(http.MethodGet, "/stats", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("stats = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("stats body: %v", err)
	}
	for _, key := range []string{"uptime_seconds", "goroutines", "mem_alloc_mb"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("stats missing %s: %v", key, body)
		}
	}
	if body["ws_connections"] != float64(3) {
		t.Fatalf("ws_connections = %v, want 3", body["ws_connections"])
	}
}

func TestStatsOmitsWSWithoutSocketSource(t *testing.T) {
	s := NewStats(nil)
	w := httptest.NewRecorder()
	s.Handler(w, httptest.NewRequest(http.MethodGet, "/stats", nil))
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("stats body: %v", err)
	}
	if _, ok := body["ws_connections"]; ok {
		t.Fatalf("ws_connections present without a source: %v", body)
	}
}
