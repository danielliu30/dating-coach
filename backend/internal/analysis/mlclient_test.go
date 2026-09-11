package analysis

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMLClientAnalyzePostsJSONAndDecodes(t *testing.T) {
	var gotPath, gotContentType string
	var got MLRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(MLResponse{ModelVersion: "v1", Overall: MLOverall{Summary: "ok"}})
	}))
	defer srv.Close()

	out, err := NewMLClient(srv.URL+"/", time.Second).Analyze(context.Background(), MLRequest{ConversationID: "c", Platform: "hinge"})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if gotPath != "/analyze" || gotContentType != "application/json" {
		t.Fatalf("request went to %q with %q", gotPath, gotContentType)
	}
	if got.ConversationID != "c" || got.Platform != "hinge" {
		t.Fatalf("request body %+v", got)
	}
	if out.ModelVersion != "v1" || out.Overall.Summary != "ok" {
		t.Fatalf("response %+v", out)
	}
}

func TestMLClientAnalyzeImagesWireFormat(t *testing.T) {
	var gotPath string
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(MLImageResponse{
			ModelVersion: "image-heuristic-v1",
			Images:       []MLImageAssessment{{Index: 0, IsClear: true, Feedback: "sharp"}},
			Overall:      MLImageOverall{Summary: "ok"},
		})
	}))
	defer srv.Close()

	out, err := NewMLClient(srv.URL, time.Second).AnalyzeImages(context.Background(), MLImageRequest{
		Images:      []MLImageRef{{URL: "https://cdn.example/a.jpg"}, {Base64: "AAAA", MediaType: "image/png"}},
		Preferences: "someone who hikes",
	})
	if err != nil {
		t.Fatalf("AnalyzeImages: %v", err)
	}
	if gotPath != "/analyze/images" {
		t.Fatalf("request went to %q", gotPath)
	}
	images, _ := raw["images"].([]any)
	if len(images) != 2 || raw["preferences"] != "someone who hikes" {
		t.Fatalf("request body %v", raw)
	}
	// The analyzer enforces url XOR base64, so an unset side must be omitted, not sent as "".
	first, _ := images[0].(map[string]any)
	if _, has := first["base64"]; has {
		t.Fatalf("empty base64 should be omitted, got %v", first)
	}
	if out.ModelVersion != "image-heuristic-v1" || len(out.Images) != 1 || !out.Images[0].IsClear || out.Overall.Summary != "ok" {
		t.Fatalf("response %+v", out)
	}
}

func TestMLClientAnalyzeSurfacesNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"detail":"bad"}`, http.StatusUnprocessableEntity)
	}))
	defer srv.Close()

	_, err := NewMLClient(srv.URL, time.Second).Analyze(context.Background(), MLRequest{})
	if err == nil || !strings.Contains(err.Error(), "422") || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("got %v, want error carrying status and body snippet", err)
	}
}
