package analysis

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/danielliu30/dating-coach/backend/internal/auth"
	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

func postImages(t *testing.T, h *Handler, body string, signedIn bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/images", strings.NewReader(body))
	if signedIn {
		req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{UserID: uuid.New()}))
	}
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	return rec
}

func TestAnalyzeImagesEndpoint(t *testing.T) {
	ml := &fakeImageAnalyzer{resp: MLImageResponse{ModelVersion: "v", Overall: MLImageOverall{Summary: "ok"}}}
	h := NewHandler(nil, NewImageService(fakeUsers{user: db.User{DatingPreferences: "hikers"}}, ml))

	rec := postImages(t, h, `{"images":[{"url":"https://cdn.example/a.jpg"}]}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out MLImageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Overall.Summary != "ok" {
		t.Fatalf("body %s (%v)", rec.Body, err)
	}
	if ml.got.Preferences != "hikers" {
		t.Fatalf("preferences %q", ml.got.Preferences)
	}

	if rec := postImages(t, h, `{"images":[{"url":"https://cdn.example/a.jpg"}]}`, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status %d", rec.Code)
	}
	if rec := postImages(t, h, `{"images":[]}`, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty images status %d", rec.Code)
	}
	if rec := postImages(t, h, `{"images":[{"url":"https://x/a.jpg","extra":1}]}`, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status %d", rec.Code)
	}
}

func TestAnalyzeImagesEndpointRejectsOversizedBody(t *testing.T) {
	valid := `{"images":[{"url":"https://cdn.example/a.jpg"}]}`
	cases := map[string]struct {
		body string
		want int
	}{
		"oversized value":           {`{"images":[{"base64":"` + strings.Repeat("A", int(maxImageBodyBytes)) + `"}]}`, http.StatusRequestEntityTooLarge},
		"oversized trailing junk":   {valid + strings.Repeat(" ", int(maxImageBodyBytes)), http.StatusRequestEntityTooLarge},
		"second value":              {valid + `{}`, http.StatusBadRequest},
		"trailing non-whitespace":   {valid + `x`, http.StatusBadRequest},
		"trailing whitespace is ok": {valid + "\n  \n", http.StatusOK},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ml := &fakeImageAnalyzer{}
			h := NewHandler(nil, NewImageService(fakeUsers{}, ml))
			rec := postImages(t, h, tc.body, true)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
			if called := len(ml.got.Images) != 0; called != (tc.want == http.StatusOK) {
				t.Fatalf("analyzer called=%v", called)
			}
		})
	}
}
