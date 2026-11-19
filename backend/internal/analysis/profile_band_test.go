package analysis

import (
	"encoding/json"
	"testing"
)

func TestProfileBandForEvidence(t *testing.T) {
	strong := profileObservation{Level: "strong", SelfPositions: []int{2}}
	missed := profileObservation{Level: "needs_coaching", SelfPositions: []int{2, 4}, Issue: "repeated_ignored_constraint"}
	clear := storedProfileEvidence{
		RubricVersion:     profileRubric,
		Coverage:          "eligible",
		FeedbackNeeded:    strong,
		Engagement:        strong,
		ClarityOfApproach: strong,
		SelfCorrection:    profileObservation{Level: "not_applicable"},
	}
	repaired := clear
	repaired.SelfCorrection = profileObservation{Level: "strong", SelfPositions: []int{2, 3}}
	unclearRepair := clear
	unclearRepair.SelfCorrection = profileObservation{Level: "unknown"}
	repeatedIssue := storedProfileEvidence{
		RubricVersion: profileRubric, Coverage: "eligible",
		FeedbackNeeded: missed, Engagement: missed, SelfCorrection: missed,
	}
	partial := storedProfileEvidence{
		RubricVersion: profileRubric, Coverage: "eligible", ClarityOfApproach: strong,
	}
	unsupported := storedProfileEvidence{RubricVersion: profileRubric, Coverage: "eligible"}
	short := storedProfileEvidence{RubricVersion: profileRubric, Coverage: "insufficient"}
	old := clear
	old.RubricVersion = "old"
	uncited := unsupported
	uncited.FeedbackNeeded = profileObservation{Level: "strong"}

	for _, tc := range []struct {
		name  string
		rows  []storedProfileEvidence
		want  profileBand
		count int
	}{
		{"a strong single chat is not a profile", []storedProfileEvidence{clear}, profileCollecting, 1},
		{"unknown and incompatible results do not count", []storedProfileEvidence{clear, unsupported, short, old, uncited}, profileCollecting, 1},
		{"clean exchanges need not contain a mistake", []storedProfileEvidence{clear, clear, clear}, profileStrong, 3},
		{"independent repair can support strong", []storedProfileEvidence{clear, repaired, partial}, profileStrong, 3},
		{"uncertain repair does not earn strong", []storedProfileEvidence{clear, unclearRepair, repaired}, profileDeveloping, 3},
		{"mixed evidence", []storedProfileEvidence{clear, repeatedIssue, partial}, profileDeveloping, 3},
		{"recurring material issue", []storedProfileEvidence{repeatedIssue, clear, repeatedIssue}, profileNeedsCoach, 3},
		{"two issues still need a third supported sample", []storedProfileEvidence{repeatedIssue, repeatedIssue, unsupported}, profileCollecting, 2},
		{"no older issue outside five usable chats", []storedProfileEvidence{unsupported, clear, repaired, clear, partial, repeatedIssue, repeatedIssue}, profileStrong, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payloads := []json.RawMessage{nil, json.RawMessage("null"), json.RawMessage("false")}
			for _, row := range tc.rows {
				payload, err := json.Marshal(row)
				if err != nil {
					t.Fatal(err)
				}
				payloads = append(payloads, payload)
			}
			if band, count := profileBandForEvidence(payloads); band != tc.want || count != tc.count {
				t.Fatalf("got %s from %d usable chats, want %s from %d", band, count, tc.want, tc.count)
			}
		})
	}
}
