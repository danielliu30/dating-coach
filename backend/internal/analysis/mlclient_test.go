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

// TestMLRequestPreferencesWireFormat pins the analyzer contract: preferences
// travel as the top-level "preferences" key and are omitted entirely when the
// user has not written any, so the analyzer takes its untailored path.
func TestMLRequestPreferencesWireFormat(t *testing.T) {
	with, err := json.Marshal(MLRequest{ConversationID: "c", Platform: "hinge", Preferences: "someone who hikes"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(with), `"preferences":"someone who hikes"`) {
		t.Fatalf("preferences missing from %s", with)
	}
	without, err := json.Marshal(MLRequest{ConversationID: "c", Platform: "hinge"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(without), "preferences") {
		t.Fatalf("blank preferences should be omitted, got %s", without)
	}
}
