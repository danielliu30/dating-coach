package analysis

import (
	"encoding/json"
	"strings"
	"testing"
)

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
