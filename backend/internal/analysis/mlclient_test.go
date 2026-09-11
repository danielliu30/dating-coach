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
