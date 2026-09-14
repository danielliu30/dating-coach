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

// testRouter builds the real routing tree over handlers with no backing
// services: every case below is decided by the auth middleware, so a request
// that reaches a handler fails loudly instead of returning a plausible status.
func testRouter(t *testing.T, issuer *auth.TokenIssuer) http.Handler {
	t.Helper()

	hub := chat.NewHub(nil)
	return newRouter(
		&config.Config{Env: "test", CORSOrigins: []string{"*"}},
		auth.Middleware(issuer),
		handlers{
			auth:     auth.NewHandler(auth.NewService(nil, nil, issuer, nil, 4, "", nil, nil, nil, 15*time.Minute, 30*time.Minute, time.Hour), auth.NewRateLimiter(nil, 100, time.Minute)),
			coaching: coaching.NewHandler(coaching.NewService(nil, nil, payments.Disabled{}, 15*time.Minute, "", "", nil)),
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
		// An unparsable id keeps the request in the handler's own validation,
		// so the unbacked service is never called.
		{http.MethodPost, "/api/v1/analysis/conversations/not-a-uuid/reanalyze"},
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

// TestAdminRoutesRequireAdminRole pins the /admin mount: only an admin session
// token gets past the middleware; users and coaches are refused with 403 and
// unauthenticated callers with 401.
func TestAdminRoutesRequireAdminRole(t *testing.T) {
	issuer := auth.NewTokenIssuer("test-secret")
	router := testRouter(t, issuer)
	tokenFor := func(role string) string {
		tok, _, err := issuer.Issue(uuid.New(), role+"@example.com", role, auth.ScopeSession, time.Hour)
		if err != nil {
			t.Fatalf("issue %s token: %v", role, err)
		}
		return tok
	}
	// A malformed id keeps the admin request inside pathUUID, so the unbacked
	// service is never reached and the admin case answers 400, not a panic.
	const path = "/api/v1/admin/coaches/not-a-uuid/approve"
	for role, want := range map[string]int{
		auth.RoleUser:  http.StatusForbidden,
		auth.RoleCoach: http.StatusForbidden,
		auth.RoleAdmin: http.StatusBadRequest,
	} {
		if got := status(router, http.MethodPost, path, tokenFor(role)); got != want {
			t.Errorf("%s POST %s = %d, want %d", role, path, got, want)
		}
	}
	if got := status(router, http.MethodGet, "/api/v1/admin/coaches", tokenFor(auth.RoleCoach)); got != http.StatusForbidden {
		t.Errorf("coach GET /admin/coaches = %d, want 403", got)
	}
	if got := status(router, http.MethodGet, "/api/v1/admin/coaches", ""); got != http.StatusUnauthorized {
		t.Errorf("anonymous GET /admin/coaches = %d, want 401", got)
	}
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
