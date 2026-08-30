package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

// stubRevocations answers Revoked from fixed values, standing in for Redis.
type stubRevocations struct {
	revoked bool
	err     error
}

// Revoked satisfies Revocations.
func (s stubRevocations) Revoked(context.Context, uuid.UUID) (bool, error) {
	return s.revoked, s.err
}

func TestRequireActive(t *testing.T) {
	cases := []struct {
		name          string
		withPrincipal bool
		revocations   stubRevocations
		want          int
	}{
		{
			name:          "active account passes",
			withPrincipal: true,
			want:          http.StatusNoContent,
		},
		{
			name:          "revoked account is unauthorized",
			withPrincipal: true,
			revocations:   stubRevocations{revoked: true},
			want:          http.StatusUnauthorized,
		},
		{
			name:          "unreachable redis fails closed",
			withPrincipal: true,
			revocations:   stubRevocations{err: errors.New("dial redis: connection refused")},
			want:          http.StatusServiceUnavailable,
		},
		{
			name: "missing principal is unauthorized",
			want: http.StatusUnauthorized,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := RequireActive(tc.revocations)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))

			req := httptest.NewRequest(http.MethodGet, "/api/v1/coaching/sessions", nil)
			if tc.withPrincipal {
				req = req.WithContext(WithPrincipal(req.Context(), Principal{UserID: uuid.New()}))
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}
