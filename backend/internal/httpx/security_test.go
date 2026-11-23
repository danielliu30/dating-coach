package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecurityHeaders(t *testing.T) {
	for _, path := range []string{"/api/v1/auth", "/api/v1/auth/", "/api/v1/auth/login", "/api/v1/auth/missing", "/api/v1/authentic", "/api/v1/coaching", "/healthz"} {
		for _, status := range []int{http.StatusOK, http.StatusNoContent, http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound, http.StatusInternalServerError} {
			t.Run(path+http.StatusText(status), func(t *testing.T) {
				rec := httptest.NewRecorder()
				SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
				if rec.Code != status || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
					t.Fatalf("status=%d headers=%v", rec.Code, rec.Header())
				}
				want := "no-store"
				if path == "/api/v1/authentic" || path == "/api/v1/coaching" || path == "/healthz" {
					want = ""
				}
				if got := rec.Header().Get("Cache-Control"); got != want {
					t.Fatalf("Cache-Control=%q want %q", got, want)
				}
			})
		}
	}
}
