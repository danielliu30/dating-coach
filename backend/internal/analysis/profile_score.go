package analysis

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/danielliu30/dating-coach/backend/internal/auth"
	"github.com/danielliu30/dating-coach/backend/internal/httpx"
)

type ProfileScore struct {
	Band                 profileBand          `json:"band"`
	RubricVersion        string               `json:"rubric_version"`
	ConversationsCounted int                  `json:"conversations_counted"`
	MinimumConversations int                  `json:"minimum_conversations"`
	Evidence             []ProfileScoreSource `json:"evidence"`
}

type ProfileScoreSource struct {
	AnalysisID     uuid.UUID `json:"analysis_id"`
	ConversationID uuid.UUID `json:"conversation_id"`
	SubmittedAt    time.Time `json:"submitted_at"`
}

func (s *Service) ProfileScore(ctx context.Context, owner uuid.UUID) (ProfileScore, error) {
	selected, err := s.selectProfileEvidence(ctx, owner, time.Now())
	if err != nil {
		return ProfileScore{}, err
	}
	payloads := make([]json.RawMessage, 0, len(selected))
	sources := make([]ProfileScoreSource, 0, len(selected))
	for _, candidate := range selected {
		payloads = append(payloads, candidate.Evidence)
		sources = append(sources, ProfileScoreSource{
			AnalysisID: candidate.AnalysisID, ConversationID: candidate.ConversationID, SubmittedAt: candidate.SubmittedAt.UTC(),
		})
	}
	band, count := profileBandForEvidence(payloads)
	return ProfileScore{
		Band: band, RubricVersion: profileRubric, ConversationsCounted: count,
		MinimumConversations: profileMinEvidence, Evidence: sources,
	}, nil
}

func (h *Handler) profileScore(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	principal, ok := principalOf(w, r)
	if !ok {
		return
	}
	// Disabled and non-client callers have no profile score surface.
	if !h.profileScoreEnabled || principal.Role != auth.RoleUser {
		httpx.Error(w, http.StatusNotFound, "profile score unavailable")
		return
	}
	score, err := h.svc.ProfileScore(r.Context(), principal.UserID)
	if err != nil {
		respondErr(w, err, "could not load profile score")
		return
	}
	httpx.JSON(w, http.StatusOK, score)
}
