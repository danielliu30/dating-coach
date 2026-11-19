package analysis

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

const (
	profileEvidenceWindow = 90 * 24 * time.Hour
	profileEvidenceBatch  = 32
)

func (s *Service) selectProfileEvidence(ctx context.Context, owner uuid.UUID, now time.Time) ([]db.ListProfileEvidenceCandidatesRow, error) {
	selected := make([]db.ListProfileEvidenceCandidatesRow, 0, profileMaxEvidence)
	seen := make(map[string]bool, profileMaxEvidence)
	params := db.ListProfileEvidenceCandidatesParams{
		OwnerID: owner, SubmittedSince: now.Add(-profileEvidenceWindow), Rubric: profileRubric, BatchSize: profileEvidenceBatch,
	}
	for {
		rows, err := s.queries.ListProfileEvidenceCandidates(ctx, params)
		if err != nil {
			return nil, err
		}
		for _, candidate := range rows {
			key := string(candidate.Fingerprint)
			if key == "" || seen[key] {
				continue
			}
			evidence, ok := qualifyingProfileEvidence(candidate.Evidence)
			if !ok || !evidenceCitesSelf(evidence, candidate.SelfPositions) {
				continue
			}
			seen[key] = true
			selected = append(selected, candidate)
			if len(selected) == profileMaxEvidence {
				return selected, nil
			}
		}
		if len(rows) < profileEvidenceBatch {
			return selected, nil
		}
		last := rows[len(rows)-1]
		params.BeforeSubmission = &last.SubmittedAt
		params.BeforeConversation = last.ConversationID
		params.BeforeAnalysisCreated = last.AnalysisCreatedAt
		params.BeforeAnalysis = last.AnalysisID
	}
}

func evidenceCitesSelf(e storedProfileEvidence, selfPositions []int32) bool {
	for _, observation := range []profileObservation{e.FeedbackNeeded, e.SelfCorrection, e.Engagement, e.ClarityOfApproach} {
		for _, position := range observation.SelfPositions {
			if position < 0 || position > 1<<31-1 || !slices.Contains(selfPositions, int32(position)) {
				return false
			}
		}
	}
	return true
}
