package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

// scopedRequest drives one request through RequireScope(want), with a principal
// carrying tokenScope in context unless withPrincipal is false, and returns the
// recorded response. It fails the test when the wrapped handler runs without
// answering 204, i.e. when rejecting the request did not stop the chain.
func scopedRequest(t *testing.T, want, tokenScope string, withPrincipal bool) *httptest.ResponseRecorder {
	t.Helper()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/private", nil)
	if withPrincipal {
		principal := Principal{UserID: uuid.New(), Email: "a@example.com", Role: RoleUser, Scope: tokenScope}
		req = req.WithContext(WithPrincipal(req.Context(), principal))
	}
	rec := httptest.NewRecorder()
	RequireScope(want)(next).ServeHTTP(rec, req)
	return rec
}

func TestRequireScope(t *testing.T) {
	tests := []struct {
		name          string
		withPrincipal bool
		tokenScope    string
		want          int
	}{
		{"matching scope passes", true, ScopeSession, http.StatusNoContent},
		{"verify scope is forbidden on session routes", true, ScopeVerify, http.StatusForbidden},
		{"token predating scopes is unauthorized so clients sign in again", true, "", http.StatusUnauthorized},
		{"missing principal is unauthorized", false, ScopeSession, http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := scopedRequest(t, ScopeSession, tc.tokenScope, tc.withPrincipal)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

// roleRequest drives one request through mw with a session principal of the
// given role (no principal at all when role is empty) and returns the status.
func roleRequest(t *testing.T, mw func(http.Handler) http.Handler, role string) int {
	t.Helper()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/admin/coaches", nil)
	if role != "" {
		principal := Principal{UserID: uuid.New(), Email: "a@example.com", Role: role, Scope: ScopeSession}
		req = req.WithContext(WithPrincipal(req.Context(), principal))
	}
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req)
	return rec.Code
}

func TestRequireAdmin(t *testing.T) {
	for role, want := range map[string]int{
		RoleAdmin: http.StatusNoContent,
		RoleCoach: http.StatusForbidden,
		RoleUser:  http.StatusForbidden,
		"":        http.StatusForbidden,
	} {
		if got := roleRequest(t, RequireAdmin, role); got != want {
			t.Errorf("RequireAdmin with role %q = %d, want %d", role, got, want)
		}
	}
	// RequireCoach keeps admitting admins, so the two middlewares differ only
	// in whether coaches pass.
	if got := roleRequest(t, RequireCoach, RoleAdmin); got != http.StatusNoContent {
		t.Errorf("RequireCoach with admin = %d, want 204", got)
	}
}

// TestMiddlewareQueryTokenOnlyOnWebSocketUpgrade checks that ?token= is
// accepted only on WebSocket upgrade requests, while the Authorization header
// works for any request.
func TestMiddlewareQueryTokenOnlyOnWebSocketUpgrade(t *testing.T) {
	issuer := NewTokenIssuer("test-secret")
	token, _, err := issuer.Issue(uuid.New(), "a@example.com", RoleUser, ScopeSession, time.Minute)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	tests := []struct {
		name    string
		query   bool
		header  bool
		upgrade string
		want    int
	}{
		{"query token on plain HTTP is rejected", true, false, "", http.StatusUnauthorized},
		{"query token on non-websocket upgrade is rejected", true, false, "h2c", http.StatusUnauthorized},
		{"query token on websocket upgrade is accepted", true, false, "websocket", http.StatusNoContent},
		{"upgrade header is matched case-insensitively", true, false, "WebSocket", http.StatusNoContent},
		{"upgrade header list containing websocket is accepted", true, false, "foo, websocket", http.StatusNoContent},
		{"authorization header on plain HTTP is accepted", false, true, "", http.StatusNoContent},
		{"no token on websocket upgrade is rejected", false, false, "websocket", http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})
			target := "/chat/threads/t1/ws"
			if tc.query {
				target += "?token=" + token
			}
			req := httptest.NewRequest(http.MethodGet, target, nil)
			if tc.header {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			if tc.upgrade != "" {
				req.Header.Set("Connection", "Upgrade")
				req.Header.Set("Upgrade", tc.upgrade)
			}
			rec := httptest.NewRecorder()
			Middleware(issuer, nil)(next).ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}
