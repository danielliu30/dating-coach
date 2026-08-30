package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeRevocations answers from a fixed set, or fails when err is set.
type fakeRevocations struct {
	revoked map[uuid.UUID]bool
	err     error
}

func (f fakeRevocations) IsRevoked(_ context.Context, userID uuid.UUID) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.revoked[userID], nil
}

func TestMiddlewareRejectsRevokedToken(t *testing.T) {
	issuer := NewTokenIssuer("test-secret", time.Hour)
	userID := uuid.New()
	token, _, err := issuer.Issue(userID, "user@example.com", RoleUser)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	for _, tc := range []struct {
		name        string
		revocations Revocations
		wantStatus  int
	}{
		{"valid", fakeRevocations{}, http.StatusOK},
		{"revoked", fakeRevocations{revoked: map[uuid.UUID]bool{userID: true}}, http.StatusUnauthorized},
		{"denylist unavailable", fakeRevocations{err: errors.New("redis down")}, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := Middleware(issuer, tc.revocations)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}
}
