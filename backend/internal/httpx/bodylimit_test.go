package httpx

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestBodyLimit(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"empty", "", http.StatusNoContent},
		{"at limit", `"` + strings.Repeat("a", 62) + `"`, http.StatusNoContent},
		{"over limit", `"` + strings.Repeat("a", 63) + `"`, http.StatusRequestEntityTooLarge},
		{"oversized trailing whitespace", `{}` + strings.Repeat(" ", 63), http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			h := LimitRequestBody(64)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				data, err := io.ReadAll(r.Body)
				if err != nil || string(data) != tc.body {
					t.Fatalf("body changed: %q err=%v", data, err)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			req.ContentLength = -1
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want || called != (tc.want == http.StatusNoContent) {
				t.Fatalf("status=%d called=%v", rec.Code, called)
			}
			if tc.want == http.StatusRequestEntityTooLarge && rec.Body.String() != "{\"error\":\"request body too large\"}\n" {
				t.Fatalf("unexpected error body: %s", rec.Body)
			}
		})
	}
}

type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (brokenBody) Close() error             { return nil }

func TestRequestBodyReadError(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Body = brokenBody{}
	rec := httptest.NewRecorder()
	LimitRequestBody(MaxJSONBodyBytes)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler reached after read error")
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatal(rec.Code)
	}
}
