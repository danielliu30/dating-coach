package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestIssueCarriesScopeAndTTL(t *testing.T) {
	issuer := NewTokenIssuer("test-secret")

	raw, expires, err := issuer.Issue(uuid.New(), "user@example.com", RoleUser, ScopeVerify, 30*time.Minute)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if until := time.Until(expires); until > 30*time.Minute || until < 29*time.Minute {
		t.Fatalf("expiry %s is not ~30m out", until)
	}
	claims, err := issuer.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if claims.Scope != ScopeVerify {
		t.Fatalf("scope = %q, want %q", claims.Scope, ScopeVerify)
	}
}

func TestRequireScope(t *testing.T) {
	issuer := NewTokenIssuer("test-secret")
	handler := Middleware(issuer)(RequireScope(ScopeSession)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))

	for _, tc := range []struct {
		name  string
		scope string
		want  int
	}{
		{name: "session token allowed", scope: ScopeSession, want: http.StatusNoContent},
		{name: "verify token forbidden", scope: ScopeVerify, want: http.StatusForbidden},
		{name: "scopeless token allowed", scope: "", want: http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _, err := issuer.Issue(uuid.New(), "user@example.com", RoleUser, tc.scope, time.Hour)
			if err != nil {
				t.Fatalf("issue: %v", err)
			}
			req := httptest.NewRequest(http.MethodGet, "/api/v1/coaching/coaches", nil)
			req.Header.Set("Authorization", "Bearer "+raw)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}
