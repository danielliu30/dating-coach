package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStatsCountsResponseClasses(t *testing.T) {
	s := NewStats(func() int { return 3 })
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/missing", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	mux.HandleFunc("/broken", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	h := s.Middleware(mux)

	for _, path := range []string{"/ok", "/missing", "/broken"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	w := httptest.NewRecorder()
	s.Handler(w, httptest.NewRequest(http.MethodGet, "/stats", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("stats = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("stats body: %v", err)
	}
	if body["requests_total"] != float64(3) {
		t.Fatalf("requests_total = %v, want 3", body["requests_total"])
	}
	if body["responses_2xx"] != float64(1) || body["responses_4xx"] != float64(1) || body["responses_5xx"] != float64(1) {
		t.Fatalf("status counts = %v", body)
	}
	if body["ws_connections"] != float64(3) {
		t.Fatalf("ws_connections = %v, want 3", body["ws_connections"])
	}
	for _, key := range []string{"uptime_seconds", "goroutines", "mem_alloc_mb"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("stats missing %s: %v", key, body)
		}
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
