package analysis

import "encoding/json"

type profileBand string

const (
	profileCollecting  profileBand = "collecting_evidence"
	profileStrong      profileBand = "strong"
	profileDeveloping  profileBand = "developing"
	profileNeedsCoach  profileBand = "needs_more_coaching"
	profileRubric                  = "profile-v1"
	profileMinEvidence             = 3
	profileMaxEvidence             = 5
)

type profileObservation struct {
	Level         string `json:"level"`
	SelfPositions []int  `json:"self_positions"`
	Issue         string `json:"issue"`
}

func (o profileObservation) assessed() bool {
	if len(o.SelfPositions) == 0 || len(o.SelfPositions) > 6 {
		return false
	}
	last := -1
	for _, p := range o.SelfPositions {
		if p <= last {
			return false
		}
		last = p
	}
	switch o.Level {
	case "strong":
		return o.Issue == ""
	case "needs_coaching":
		return o.Issue == "repeated_ignored_constraint" && len(o.SelfPositions) >= 2
	default:
		return false
	}
}

type storedProfileEvidence struct {
	RubricVersion     string             `json:"rubric_version"`
	Coverage          string             `json:"coverage"`
	FeedbackNeeded    profileObservation `json:"feedback_needed"`
	SelfCorrection    profileObservation `json:"self_correction"`
	Engagement        profileObservation `json:"engagement"`
	ClarityOfApproach profileObservation `json:"clarity_of_approach"`
}

// profileBandForEvidence takes one payload per distinct transcript, newest
// first and already scoped to an owner and the recent submission window.
func profileBandForEvidence(payloads []json.RawMessage) (profileBand, int) {
	var count, strong int
	var uncertainRepair, recurringIssue bool
	issues := map[string]int{}
	for _, payload := range payloads {
		var row storedProfileEvidence
		if json.Unmarshal(payload, &row) != nil || row.RubricVersion != profileRubric || row.Coverage != "eligible" {
			continue
		}
		feedback, engagement, clarity := row.FeedbackNeeded.assessed(), row.Engagement.assessed(), row.ClarityOfApproach.assessed()
		if !feedback && !engagement && !clarity {
			continue
		}
		count++

		if feedback && row.FeedbackNeeded.Level == "needs_coaching" {
			issues[row.FeedbackNeeded.Issue]++
			recurringIssue = recurringIssue || issues[row.FeedbackNeeded.Issue] >= 2
		}
		if feedback && row.FeedbackNeeded.Level == "strong" &&
			engagement && row.Engagement.Level == "strong" && clarity && row.ClarityOfApproach.Level == "strong" {
			repair := row.SelfCorrection
			switch {
			case repair.Level == "strong" && repair.assessed():
				strong++
			case repair.Level == "not_applicable" && len(repair.SelfPositions) == 0 && repair.Issue == "":
				strong++
			default:
				uncertainRepair = true
			}
		}
		if count == profileMaxEvidence {
			break
		}
	}
	switch {
	case count < profileMinEvidence:
		return profileCollecting, count
	case recurringIssue:
		return profileNeedsCoach, count
	case !uncertainRepair && strong > count/2:
		return profileStrong, count
	default:
		return profileDeveloping, count
	}
}
