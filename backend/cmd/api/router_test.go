package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/danielliu30/dating-coach/backend/internal/analysis"
	"github.com/danielliu30/dating-coach/backend/internal/auth"
	"github.com/danielliu30/dating-coach/backend/internal/chat"
	"github.com/danielliu30/dating-coach/backend/internal/coaching"
	"github.com/danielliu30/dating-coach/backend/internal/config"
	"github.com/danielliu30/dating-coach/backend/internal/payments"
)

// allowAll stands in for the revocation middleware, which needs Redis and has
// nothing to say about scopes.
func allowAll(next http.Handler) http.Handler { return next }

// testRouter builds the real routing tree over handlers with no backing
// services: every case below is decided by the auth middleware, so a request
// that reaches a handler fails loudly instead of returning a plausible status.
func testRouter(t *testing.T, issuer *auth.TokenIssuer) http.Handler {
	t.Helper()

	hub := chat.NewHub(nil)
	return newRouter(
		&config.Config{Env: "test", CORSOrigins: []string{"*"}},
		auth.Middleware(issuer),
		allowAll,
		handlers{
			auth:     auth.NewHandler(auth.NewService(nil, issuer, nil, 4, "", nil, nil, time.Hour, 30*time.Minute), auth.NewRateLimiter(nil, 100, time.Minute)),
			coaching: coaching.NewHandler(coaching.NewService(nil, nil, payments.Disabled{}, 15*time.Minute, "")),
			chat:     chat.NewHandler(chat.NewService(nil, hub), hub, nil, []string{"*"}),
			analysis: analysis.NewHandler(analysis.NewService(nil, nil, nil)),
		},
	)
}

// TestPrivateRoutesRejectVerifyScope pins the wiring in newRouter: the token
// minted by sign-up must reach the auth endpoints it needs to verify an address
// and nothing else, whatever feature is mounted under the private group.
func TestPrivateRoutesRejectVerifyScope(t *testing.T) {
	issuer := auth.NewTokenIssuer("test-secret")
	router := testRouter(t, issuer)

	verifyToken, _, err := issuer.Issue(uuid.New(), "user@example.com", auth.RoleCoach, auth.ScopeVerify, time.Hour)
	if err != nil {
		t.Fatalf("issue verify token: %v", err)
	}
	sessionToken, _, err := issuer.Issue(uuid.New(), "user@example.com", auth.RoleCoach, auth.ScopeSession, time.Hour)
	if err != nil {
		t.Fatalf("issue session token: %v", err)
	}

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/coaching/coaches"},
		{http.MethodGet, "/api/v1/coaching/sessions"},
		{http.MethodPost, "/api/v1/coaching/sessions"},
		{http.MethodGet, "/api/v1/chat/threads"},
		{http.MethodPost, "/api/v1/chat/threads"},
		{http.MethodGet, "/api/v1/chat/coach/threads"},
		{http.MethodGet, "/api/v1/analysis/conversations"},
		{http.MethodPost, "/api/v1/analysis/conversations"},
		{http.MethodGet, "/api/v1/coach/sessions"},
		{http.MethodPut, "/api/v1/coach/profile"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			if got := status(router, tc.method, tc.path, verifyToken); got != http.StatusForbidden {
				t.Fatalf("verify-scoped token: status = %d, want %d", got, http.StatusForbidden)
			}
			// The same request with a session token gets past the middleware and
			// into the (unbacked) handler, so only the scope check can 403 here.
			if got := status(router, tc.method, tc.path, sessionToken); got == http.StatusForbidden || got == http.StatusUnauthorized {
				t.Fatalf("session-scoped token: status = %d, want it past authentication", got)
			}
		})
	}

	t.Run("GET /api/v1/auth/me stays reachable", func(t *testing.T) {
		if got := status(router, http.MethodGet, "/api/v1/auth/me", verifyToken); got == http.StatusForbidden {
			t.Fatal("verify-scoped token cannot reach /auth/me, so it cannot drive the verify screen")
		}
	})

	t.Run("no token is unauthorized", func(t *testing.T) {
		if got := status(router, http.MethodGet, "/api/v1/coaching/coaches", ""); got != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", got, http.StatusUnauthorized)
		}
	})
}

// status serves one request against router and returns its status code, sending
// the bearer token when it is not empty.
func status(router http.Handler, method, path, token string) int {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec.Code
}
