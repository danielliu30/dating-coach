package analysis

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

func TestNewMLRequestCarriesOwnerTailoring(t *testing.T) {
	conversation := db.Conversation{ID: uuid.New(), Platform: "hinge", MatchName: "Sam"}
	sent := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("PST", -8*3600))
	messages := []db.Message{{Position: 0, Sender: "self", Body: "Hi Sam", SentAt: sent}}

	req := newMLRequest(conversation, messages, db.User{DatingPreferences: "someone who hikes", CoachingGoals: []string{"agency", "support"}})
	if req.ConversationID != conversation.ID.String() || req.Platform != "hinge" || req.MatchName != "Sam" {
		t.Fatalf("request %+v", req)
	}
	if req.Preferences != "someone who hikes" || !slices.Equal(req.FocusAreas, []string{"agency", "support"}) {
		t.Fatalf("tailoring not forwarded: %+v", req)
	}
	if len(req.Messages) != 1 || req.Messages[0].SentAt != "2026-01-02T11:04:05Z" {
		t.Fatalf("messages %+v", req.Messages)
	}

	untailored := newMLRequest(conversation, messages, db.User{})
	if untailored.Preferences != "" || len(untailored.FocusAreas) != 0 || untailored.Profile != nil {
		t.Fatalf("zero tailoring leaked into %+v", untailored)
	}
}

func TestNewMLRequestCarriesOwnerProfile(t *testing.T) {
	conversation := db.Conversation{ID: uuid.New(), Platform: "hinge"}
	messages := []db.Message{{Position: 0, Sender: "self", Body: "Hi", SentAt: time.Now()}}
	owner := db.User{
		DatingStyles:    []string{"hinge", "in_person"},
		PhasesWorkingOn: []string{"building_rapport"},
		PhasesStrong:    []string{},
	}

	req := newMLRequest(conversation, messages, owner)
	if req.Profile == nil ||
		!slices.Equal(req.Profile.DatingStyles, []string{"hinge", "in_person"}) ||
		!slices.Equal(req.Profile.PhasesWorkingOn, []string{"building_rapport"}) ||
		len(req.Profile.PhasesStrong) != 0 {
		t.Fatalf("profile not forwarded: %+v", req.Profile)
	}

	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := string(wire["profile"]); got != `{"dating_styles":["hinge","in_person"],"phases_working_on":["building_rapport"]}` {
		t.Fatalf("profile on the wire = %s", got)
	}

	empty := newMLRequest(conversation, messages, db.User{DatingStyles: []string{}, PhasesStrong: []string{}, PhasesWorkingOn: []string{}})
	if empty.Profile != nil {
		t.Fatalf("empty profile sent: %+v", empty.Profile)
	}
	body, err = json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), `"profile"`) {
		t.Fatalf("empty profile key on the wire: %s", body)
	}
}
