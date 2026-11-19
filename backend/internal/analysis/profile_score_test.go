package analysis

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/danielliu30/dating-coach/backend/internal/auth"
	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

func TestProfileScoreRouteAccess(t *testing.T) {
	issuer := auth.NewTokenIssuer("test-secret")
	for _, tc := range []struct {
		name, role, scope string
		enabled           bool
		want              int
	}{
		{"disabled", auth.RoleUser, auth.ScopeSession, false, http.StatusNotFound},
		{"coach", auth.RoleCoach, auth.ScopeSession, true, http.StatusNotFound},
		{"admin", auth.RoleAdmin, auth.ScopeSession, true, http.StatusNotFound},
		{"anonymous", "", "", true, http.StatusUnauthorized},
		{"verify-only", auth.RoleUser, auth.ScopeVerify, true, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := auth.Middleware(issuer, nil, nil)(auth.RequireScope(auth.ScopeSession)(NewHandler(nil, tc.enabled).Routes()))
			res := requestProfileScore(t, handler, issuer, uuid.New(), tc.role, tc.scope, "")
			if res.Code != tc.want {
				t.Fatalf("status = %d, want %d", res.Code, tc.want)
			}
		})
	}
}

func TestProfileScoreRouteDerivesOwnerFromSession(t *testing.T) {
	pool := testPool(t)
	owner, conversation := insertConversation(t, pool)
	stranger, _ := insertConversation(t, pool)
	mustExecProfileTest(t, pool, "INSERT INTO messages (conversation_id,position,sender,body) VALUES ($1,0,'self','Private message')", conversation)
	var analysisID uuid.UUID
	err := pool.QueryRow(context.Background(), `INSERT INTO analysis_results (conversation_id,status,overall)
		VALUES ($1,'succeeded','{"profile_evidence":{"rubric_version":"profile-v1","coverage":"eligible","feedback_needed":{"level":"strong","self_positions":[0]}}}') RETURNING id`,
		conversation).Scan(&analysisID)
	if err != nil {
		t.Fatal(err)
	}
	issuer := auth.NewTokenIssuer("test-secret")
	handler := auth.Middleware(issuer, nil, nil)(auth.RequireScope(auth.ScopeSession)(NewHandler(NewService(pool, db.New(pool), nil), true).Routes()))
	for _, tc := range []struct {
		user   uuid.UUID
		spoof  uuid.UUID
		count  int
		source uuid.UUID
	}{
		{owner, stranger, 1, analysisID},
		{stranger, owner, 0, uuid.Nil},
	} {
		res := requestProfileScore(t, handler, issuer, tc.user, auth.RoleUser, auth.ScopeSession, "?user_id="+tc.spoof.String())
		if res.Code != http.StatusOK || res.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("status = %d, headers = %v, body = %s", res.Code, res.Header(), res.Body)
		}
		var score ProfileScore
		if err := json.Unmarshal(res.Body.Bytes(), &score); err != nil {
			t.Fatal(err)
		}
		if score.Band != profileCollecting || score.RubricVersion != profileRubric || score.MinimumConversations != profileMinEvidence ||
			score.ConversationsCounted != tc.count || score.Evidence == nil || len(score.Evidence) != tc.count ||
			(tc.count == 1 && (score.Evidence[0].AnalysisID != tc.source || score.Evidence[0].ConversationID != conversation)) {
			t.Fatalf("wrong summary for %s: %+v", tc.user, score)
		}
	}
}

func requestProfileScore(t *testing.T, handler http.Handler, issuer *auth.TokenIssuer, user uuid.UUID, role, scope, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/profile-score"+query, nil)
	if role != "" {
		token, _, err := issuer.Issue(user, "client@example.com", role, scope, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	return res
}
