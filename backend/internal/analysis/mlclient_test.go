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

// TestMLOverallRoundTripsReflectionFields pins that the analyzer's
// reflection_questions, patterns and goal_scores survive decode -> re-encode, which is the
// path the worker uses to persist analysis_results.overall.
func TestMLOverallRoundTripsReflectionFields(t *testing.T) {
	wire := `{"engagement_score":0.4,"summary":"s","strengths":[],"improvements":["Message 2 got no reply."],` +
		`"reflection_questions":["Did you actually enjoy this conversation?"],` +
		`"patterns":["You sent about twice as many messages as they did in this conversation (4 to 2)."],` +
		`"goal_scores":[{"goal":"feedback","score":0.75,"comment":"Message 1 drew a long reply."}]}`
	var overall MLOverall
	if err := json.Unmarshal([]byte(wire), &overall); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(overall.ReflectionQuestions) != 1 || len(overall.Patterns) != 1 || len(overall.GoalScores) != 1 {
		t.Fatalf("decoded %+v, want one reflection question, one pattern and one goal score", overall)
	}
	stored, err := json.Marshal(overall)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{
		`"reflection_questions":["Did you actually enjoy this conversation?"]`,
		`"patterns":["You sent about twice as many messages as they did in this conversation (4 to 2)."]`,
		`"goal_scores":[{"goal":"feedback","score":0.75,"comment":"Message 1 drew a long reply."}]`,
	} {
		if !strings.Contains(string(stored), want) {
			t.Fatalf("persisted overall %s is missing %s", stored, want)
		}
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

// TestMLRequestFocusAreasWireFormat pins the analyzer contract for coaching
// goals: they travel as the top-level "focus_areas" list and are omitted when
// the user has chosen none, so the analyzer returns no goal_scores.
func TestMLRequestFocusAreasWireFormat(t *testing.T) {
	with, err := json.Marshal(MLRequest{ConversationID: "c", Platform: "hinge", FocusAreas: []string{"agency", "feedback"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(with), `"focus_areas":["agency","feedback"]`) {
		t.Fatalf("focus_areas missing from %s", with)
	}
	for _, areas := range [][]string{nil, {}} {
		without, err := json.Marshal(MLRequest{ConversationID: "c", Platform: "hinge", FocusAreas: areas})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(without), "focus_areas") {
			t.Fatalf("empty focus_areas should be omitted, got %s", without)
		}
	}
}
