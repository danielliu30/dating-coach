package analysis

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/danielliu30/dating-coach/backend/internal/coaching"
)

// MLRequest is the payload sent to the Python analyzer. It is intentionally
// model-agnostic: the LLM-prompt backend and a future fine-tuned model share it.
type MLRequest struct {
	ConversationID string `json:"conversation_id"`
	Platform       string `json:"platform"`
	MatchName      string `json:"match_name,omitempty"`
	// Preferences is the owner's users.dating_preferences text; omitted when blank
	// so the analyzer's default (untailored) path is taken.
	Preferences string      `json:"preferences,omitempty"`
	Messages    []MLMessage `json:"messages"`
}

// MLMessage is one transcript message as the analyzer expects it.
type MLMessage struct {
	Position int32  `json:"position"`
	Sender   string `json:"sender"`
	Body     string `json:"body"`
	SentAt   string `json:"sent_at"`
}

// MLResponse mirrors ml-analyzer's AnalyzeResponse schema.
type MLResponse struct {
	ModelVersion string      `json:"model_version"`
	Segments     []MLSegment `json:"segments"`
	Overall      MLOverall   `json:"overall"`
}

// MLSegment scores a contiguous run of messages, which is what the app
// highlights inline in the transcript.
type MLSegment struct {
	StartPosition   int32   `json:"start_position"`
	EndPosition     int32   `json:"end_position"`
	EngagementScore float64 `json:"engagement_score"`
	Label           string  `json:"label"`
	Comment         string  `json:"comment"`
}

// MLOverall is the conversation-level verdict shown at the top of the report.
// ReflectionQuestions and Patterns are the analyzer's inward-facing feedback
// (open questions for the customer and recurring behaviour named as an
// observation). Results stored before the analyzer emitted them, or from an
// analyzer that omits them, carry null for these keys, like any absent slice.
type MLOverall struct {
	EngagementScore     float64  `json:"engagement_score"`
	Summary             string   `json:"summary"`
	Strengths           []string `json:"strengths"`
	Improvements        []string `json:"improvements"`
	ReflectionQuestions []string `json:"reflection_questions"`
	Patterns            []string `json:"patterns"`
}

// SummarizeReviews asks ml-analyzer's /summarize/reviews to condense a coach's
// reviews; it satisfies coaching.ReviewSummarizer. Errors carry the response
// snippet like Analyze.
func (c *MLClient) SummarizeReviews(ctx context.Context, in coaching.ReviewSummaryRequest) (coaching.ReviewSummaryResult, error) {
	var out coaching.ReviewSummaryResult
	if err := c.post(ctx, "/summarize/reviews", in, &out); err != nil {
		return coaching.ReviewSummaryResult{}, err
	}
	return out, nil
}

// MLClient talks to the ml-analyzer service over HTTP only, so the ML component
// can be deployed, scaled and replaced independently.
type MLClient struct {
	baseURL string
	http    *http.Client
}

// NewMLClient returns a client for the analyzer at baseURL. The timeout bounds
// a whole scoring call, which can be slow when it goes through an LLM.
func NewMLClient(baseURL string, timeout time.Duration) *MLClient {
	return &MLClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: timeout},
	}
}

// Analyze scores one conversation by POSTing it to /analyze. Non-200 responses
// become errors carrying a snippet of the body, so the worker can record why a
// job failed. Called by Worker.Handle.
func (c *MLClient) Analyze(ctx context.Context, in MLRequest) (MLResponse, error) {
	var out MLResponse
	if err := c.post(ctx, "/analyze", in, &out); err != nil {
		return MLResponse{}, err
	}
	return out, nil
}

// post sends in as JSON to path and decodes a 200 response into out. Non-200
// responses become errors carrying a snippet of the body.
func (c *MLClient) post(ctx context.Context, path string, in any, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("encode ml request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build ml request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call ml service: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("ml service returned %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode ml response: %w", err)
	}
	return nil
}
