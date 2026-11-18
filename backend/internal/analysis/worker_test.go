package analysis

import (
	"slices"
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
	if untailored.Preferences != "" || len(untailored.FocusAreas) != 0 {
		t.Fatalf("zero tailoring leaked into %+v", untailored)
	}
}
