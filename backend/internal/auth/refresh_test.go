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
	familyErr    error
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
		FamilyID:  arg.FamilyID,
		ExpiresAt: arg.ExpiresAt,
	}
	return nil
}

// GetRefreshToken returns the row for tokenHash whatever its state, which is
// how the real query lets the flow recognise a token that was already spent.
func (s *stubRefreshStore) GetRefreshToken(_ context.Context, tokenHash string) (db.RefreshToken, error) {
	if s.lookupErr != nil {
		return db.RefreshToken{}, s.lookupErr
	}
	row, ok := s.rows[tokenHash]
	if !ok {
		return db.RefreshToken{}, pgx.ErrNoRows
	}
	return row, nil
}

// RevokeRefreshTokenFamily revokes every live row sharing familyID.
func (s *stubRefreshStore) RevokeRefreshTokenFamily(_ context.Context, familyID uuid.UUID) (int64, error) {
	if s.familyErr != nil {
		return 0, s.familyErr
	}
	var revoked int64
	for hash, row := range s.rows {
		if row.FamilyID != familyID || row.RevokedAt != nil {
			continue
		}
		now := time.Now()
		row.RevokedAt = &now
		s.rows[hash] = row
		revoked++
	}
	return revoked, nil
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
	if _, _, _, err := refresh.Rotate(context.Background(), replacement); err != nil {
		t.Fatalf("rotating the replacement: %v", err)
	}
	if _, _, _, err := refresh.Rotate(context.Background(), token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("reusing the spent token: err = %v, want ErrInvalidToken", err)
	}
}

// TestRotateRevokesTheFamilyOfAReplayedToken covers the response to a leak: a
// spent token coming back means the chain it belongs to is compromised, so the
// live replacement must stop working too, while the same user's other session
// carries on.
func TestRotateRevokesTheFamilyOfAReplayedToken(t *testing.T) {
	store := &stubRefreshStore{}
	refresh := NewRefreshTokens(store, time.Hour)
	ctx := context.Background()
	userID := uuid.New()

	stolen, _, err := refresh.Issue(ctx, userID)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	otherDevice, _, err := refresh.Issue(ctx, userID)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	_, live, _, err := refresh.Rotate(ctx, stolen)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}

	if _, _, _, err := refresh.Rotate(ctx, stolen); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("replaying the spent token: err = %v, want ErrInvalidToken", err)
	}
	if _, _, _, err := refresh.Rotate(ctx, live); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("replacement after the replay: err = %v, want ErrInvalidToken", err)
	}
	if _, _, _, err := refresh.Rotate(ctx, otherDevice); err != nil {
		t.Fatalf("the other session was revoked too: %v", err)
	}
}

// TestRotateFailsWhenTheFamilyCannotBeRevoked keeps a replay from looking like
// an ordinary bad token when the revocation did not happen: the caller must see
// a failure rather than a compromised chain quietly staying live.
func TestRotateFailsWhenTheFamilyCannotBeRevoked(t *testing.T) {
	store := &stubRefreshStore{}
	refresh := NewRefreshTokens(store, time.Hour)
	ctx := context.Background()
	token, _, err := refresh.Issue(ctx, uuid.New())
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, _, _, err := refresh.Rotate(ctx, token); err != nil {
		t.Fatalf("rotate: %v", err)
	}

	store.familyErr = errors.New("connection refused")
	if _, _, _, err := refresh.Rotate(ctx, token); err == nil || errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err = %v, want a store failure", err)
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
			if name == "expired" {
				// Expiry is not a leak, so it must not take the family with it.
				if row := store.rows[hashToken(token)]; row.RevokedAt != nil {
					t.Fatal("an expired token revoked its family")
				}
			}
		})
	}
}

// TestRotateKeepsTheTokenWhenTheReplacementFails guards the ordering inside
// Rotate: a failure to store the replacement must leave the presented token
// exchangeable, or the client is stranded with no way back into its session.
func TestRotateKeepsTheTokenWhenTheReplacementFails(t *testing.T) {
	store := &stubRefreshStore{}
	refresh := NewRefreshTokens(store, time.Hour)
	token, _, err := refresh.Issue(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	store.createErr = errors.New("connection refused")
	if _, _, _, err := refresh.Rotate(context.Background(), token); err == nil || errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err = %v, want a store failure", err)
	}

	store.createErr = nil
	if _, _, _, err := refresh.Rotate(context.Background(), token); err != nil {
		t.Fatalf("retrying the rotation: %v", err)
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
