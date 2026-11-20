package httpx

import (
	"errors"
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
			h := LimitRequestBody(64)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.body != "" {
					var data any
					if err := Decode(r, &data); err != nil {
						DecodeError(w, err)
						return
					}
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			req.ContentLength = -1
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d", rec.Code, tc.want)
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
	LimitRequestBody(MaxJSONBodyBytes)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data any
		DecodeError(w, Decode(r, &data))
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatal(rec.Code)
	}
}

func TestBodyLimitDoesNotReadBeforeHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Body = brokenBody{}
	req.ContentLength = MaxJSONBodyBytes + 1
	rec := httptest.NewRecorder()
	LimitRequestBody(MaxJSONBodyBytes)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("middleware read body before authentication: %d", rec.Code)
	}
}
