package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// stubRefreshStore is an in-memory RefreshTokenStore keyed by token hash, so
// tests can assert on what was persisted without a database.
type stubRefreshStore struct {
	rows         map[string]db.RefreshToken
	revokedUsers []uuid.UUID
	createErr    error
	lookupErr    error
}

// CreateRefreshToken stores the row, or returns the configured failure.
func (s *stubRefreshStore) CreateRefreshToken(_ context.Context, arg db.CreateRefreshTokenParams) error {
	if s.createErr != nil {
		return s.createErr
	}
	if s.rows == nil {
		s.rows = map[string]db.RefreshToken{}
	}
	s.rows[arg.TokenHash] = db.RefreshToken{
		TokenHash: arg.TokenHash,
		UserID:    arg.UserID,
		ExpiresAt: arg.ExpiresAt,
	}
	return nil
}

// GetActiveRefreshToken returns the unrevoked, unexpired row for tokenHash, or
// pgx.ErrNoRows the way the real query does.
func (s *stubRefreshStore) GetActiveRefreshToken(_ context.Context, tokenHash string) (db.RefreshToken, error) {
	if s.lookupErr != nil {
		return db.RefreshToken{}, s.lookupErr
	}
	row, ok := s.rows[tokenHash]
	if !ok || row.RevokedAt != nil || !row.ExpiresAt.After(time.Now()) {
		return db.RefreshToken{}, pgx.ErrNoRows
	}
	return row, nil
}

// RevokeRefreshToken marks the row revoked, reporting how many rows it changed.
func (s *stubRefreshStore) RevokeRefreshToken(_ context.Context, tokenHash string) (int64, error) {
	row, ok := s.rows[tokenHash]
	if !ok || row.RevokedAt != nil {
		return 0, nil
	}
	now := time.Now()
	row.RevokedAt = &now
	s.rows[tokenHash] = row
	return 1, nil
}

// RevokeUserRefreshTokens revokes every live row of userID and records the call.
func (s *stubRefreshStore) RevokeUserRefreshTokens(_ context.Context, userID uuid.UUID) (int64, error) {
	s.revokedUsers = append(s.revokedUsers, userID)
	var revoked int64
	for hash, row := range s.rows {
		if row.UserID != userID || row.RevokedAt != nil {
			continue
		}
		now := time.Now()
		row.RevokedAt = &now
		s.rows[hash] = row
		revoked++
	}
	return revoked, nil
}

// TestIssueStoresOnlyTheHash pins the property that makes a table dump
// unusable: the secret handed to the client is never written down.
func TestIssueStoresOnlyTheHash(t *testing.T) {
	store := &stubRefreshStore{}
	userID := uuid.New()

	token, expires, err := NewRefreshTokens(store, time.Hour).Issue(context.Background(), userID)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if token == "" {
		t.Fatal("issue returned an empty token")
	}
	if _, ok := store.rows[token]; ok {
		t.Fatal("the plaintext token was stored")
	}
	row, ok := store.rows[hashToken(token)]
	if !ok {
		t.Fatal("no row stored for the token hash")
	}
	if row.UserID != userID {
		t.Fatalf("stored user = %s, want %s", row.UserID, userID)
	}
	if !row.ExpiresAt.Equal(expires) {
		t.Fatalf("stored expiry = %s, want %s", row.ExpiresAt, expires)
	}
}

// TestRotateInvalidatesTheSpentToken covers the replay case: the token used for
// an exchange must not work a second time, while its replacement does.
func TestRotateInvalidatesTheSpentToken(t *testing.T) {
	store := &stubRefreshStore{}
	refresh := NewRefreshTokens(store, time.Hour)
	userID := uuid.New()
	token, _, err := refresh.Issue(context.Background(), userID)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	owner, replacement, _, err := refresh.Rotate(context.Background(), token)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if owner != userID {
		t.Fatalf("owner = %s, want %s", owner, userID)
	}
	if replacement == token {
		t.Fatal("rotate returned the same token")
	}
	if _, _, _, err := refresh.Rotate(context.Background(), token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("reusing the spent token: err = %v, want ErrInvalidToken", err)
	}
	if _, _, _, err := refresh.Rotate(context.Background(), replacement); err != nil {
		t.Fatalf("rotating the replacement: %v", err)
	}
}

// TestRotateRejectsUnusableTokens checks the three ways a token stops being
// exchangeable, all of which must look identical to the client.
func TestRotateRejectsUnusableTokens(t *testing.T) {
	userID := uuid.New()
	for name, seed := range map[string]func(*RefreshTokens, *stubRefreshStore) string{
		"unknown": func(*RefreshTokens, *stubRefreshStore) string { return "never-issued" },
		"expired": func(r *RefreshTokens, s *stubRefreshStore) string {
			token, _, err := r.Issue(context.Background(), userID)
			if err != nil {
				panic(err)
			}
			row := s.rows[hashToken(token)]
			row.ExpiresAt = time.Now().Add(-time.Minute)
			s.rows[hashToken(token)] = row
			return token
		},
		"revoked with the account": func(r *RefreshTokens, s *stubRefreshStore) string {
			token, _, err := r.Issue(context.Background(), userID)
			if err != nil {
				panic(err)
			}
			if err := r.RevokeAll(context.Background(), userID); err != nil {
				panic(err)
			}
			return token
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := &stubRefreshStore{}
			refresh := NewRefreshTokens(store, time.Hour)
			token := seed(refresh, store)
			if _, _, _, err := refresh.Rotate(context.Background(), token); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("err = %v, want ErrInvalidToken", err)
			}
		})
	}
}

// TestRotateSurfacesStoreFailures keeps an unreachable database distinct from a
// bad token, so a client is not signed out over an outage.
func TestRotateSurfacesStoreFailures(t *testing.T) {
	store := &stubRefreshStore{lookupErr: errors.New("connection refused")}
	_, _, _, err := NewRefreshTokens(store, time.Hour).Rotate(context.Background(), "whatever")
	if err == nil || errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err = %v, want a store failure", err)
	}
}
