package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func verifiedRequest(t *testing.T, lookup VerifiedLookup, withPrincipal bool) *httptest.ResponseRecorder {
	t.Helper()

	reached := false
	handler := RequireVerified(lookup)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/coaching/coaches", nil)
	if withPrincipal {
		req = req.WithContext(WithPrincipal(req.Context(), Principal{UserID: uuid.New(), Role: RoleUser}))
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if reached != (rec.Code == http.StatusNoContent) {
		t.Fatalf("handler reached = %v with status %d", reached, rec.Code)
	}
	return rec
}

func TestRequireVerified(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		withPrincipal bool
		lookup        VerifiedLookup
		want          int
	}{
		{
			name:          "verified account passes",
			withPrincipal: true,
			lookup:        func(context.Context, uuid.UUID) (bool, error) { return true, nil },
			want:          http.StatusNoContent,
		},
		{
			name:          "unverified account is forbidden",
			withPrincipal: true,
			lookup:        func(context.Context, uuid.UUID) (bool, error) { return false, nil },
			want:          http.StatusForbidden,
		},
		{
			name:          "deleted account is unauthorized",
			withPrincipal: true,
			lookup:        func(context.Context, uuid.UUID) (bool, error) { return false, pgx.ErrNoRows },
			want:          http.StatusUnauthorized,
		},
		{
			name:          "lookup failure does not fail open",
			withPrincipal: true,
			lookup:        func(context.Context, uuid.UUID) (bool, error) { return true, errors.New("boom") },
			want:          http.StatusInternalServerError,
		},
		{
			name:          "missing principal is unauthorized",
			withPrincipal: false,
			lookup:        func(context.Context, uuid.UUID) (bool, error) { return true, nil },
			want:          http.StatusUnauthorized,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := verifiedRequest(t, tc.lookup, tc.withPrincipal).Code; got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
		})
	}
}
