package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// fakeRevocationStore is an in-memory RevocationStore: it keeps the expiry per
// account the way the table does, so the denylist can be exercised without a
// database. Zero value accepts writes and reports nothing revoked.
type fakeRevocationStore struct {
	expiries  map[uuid.UUID]time.Time
	revokeErr error
	lookupErr error
}

// RevokeUserSessions records the expiry, keeping the later one on a repeat call.
func (f *fakeRevocationStore) RevokeUserSessions(_ context.Context, arg db.RevokeUserSessionsParams) error {
	if f.revokeErr != nil {
		return f.revokeErr
	}
	if f.expiries == nil {
		f.expiries = make(map[uuid.UUID]time.Time)
	}
	if existing, ok := f.expiries[arg.UserID]; !ok || arg.ExpiresAt.After(existing) {
		f.expiries[arg.UserID] = arg.ExpiresAt
	}
	return nil
}

// IsSessionRevoked reports whether an unexpired entry exists for userID.
func (f *fakeRevocationStore) IsSessionRevoked(_ context.Context, userID uuid.UUID) (bool, error) {
	if f.lookupErr != nil {
		return false, f.lookupErr
	}
	expires, ok := f.expiries[userID]
	return ok && expires.After(time.Now()), nil
}

// TestDenylistOutlivesProcessState covers the durability the middleware relies
// on: a revocation written by one Denylist is seen by another built over the
// same store, and it stops mattering only once the tokens it refuses expire.
func TestDenylistOutlivesProcessState(t *testing.T) {
	store := &fakeRevocationStore{}
	userID := uuid.New()

	if err := NewDenylist(store, time.Hour).Revoke(context.Background(), userID); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}

	revoked, err := NewDenylist(store, time.Hour).Revoked(context.Background(), userID)
	if err != nil {
		t.Fatalf("Revoked() error = %v", err)
	}
	if !revoked {
		t.Fatal("Revoked() = false for an account revoked through another denylist")
	}

	store.expiries[userID] = time.Now().Add(-time.Second)
	revoked, err = NewDenylist(store, time.Hour).Revoked(context.Background(), userID)
	if err != nil {
		t.Fatalf("Revoked() error = %v", err)
	}
	if revoked {
		t.Fatal("Revoked() = true for an entry whose tokens have all expired")
	}
}

// TestDenylistRevokedPropagatesFailures checks the middleware gets an error, not
// a false "active", when the revocation state cannot be read.
func TestDenylistRevokedPropagatesFailures(t *testing.T) {
	store := &fakeRevocationStore{lookupErr: errors.New("database unavailable")}
	if _, err := NewDenylist(store, time.Hour).Revoked(context.Background(), uuid.New()); err == nil {
		t.Fatal("Revoked() error = nil, want the store failure")
	}
}

// stubRevocations answers Revoked from fixed values, standing in for a store.
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
			name:          "unreachable store fails closed",
			withPrincipal: true,
			revocations:   stubRevocations{err: errors.New("query revoked_sessions: connection refused")},
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
