package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// RefreshTokenStore is the subset of the generated queries the refresh flow
// needs. Service depends on the interface so the flow can be exercised without
// a database.
type RefreshTokenStore interface {
	CreateRefreshToken(ctx context.Context, arg db.CreateRefreshTokenParams) error
	GetActiveRefreshToken(ctx context.Context, tokenHash string) (db.RefreshToken, error)
	GetRefreshToken(ctx context.Context, tokenHash string) (db.RefreshToken, error)
	RevokeRefreshToken(ctx context.Context, tokenHash string) (int64, error)
	RevokeRefreshTokenFamily(ctx context.Context, familyID uuid.UUID) (int64, error)
	RevokeUserRefreshTokens(ctx context.Context, userID uuid.UUID) (int64, error)
}

// RefreshTokens issues, rotates and revokes the long-lived halves of a session.
// Access tokens are verified by signature alone and cannot be withdrawn, so
// this is the only place a session can be ended before it expires.
type RefreshTokens struct {
	store RefreshTokenStore
	ttl   time.Duration
}

// NewRefreshTokens returns a refresh-token issuer whose tokens last for ttl,
// which bounds how long a signed-out or deleted account could still obtain new
// access tokens if a revocation were ever missed.
func NewRefreshTokens(store RefreshTokenStore, ttl time.Duration) *RefreshTokens {
	return &RefreshTokens{store: store, ttl: ttl}
}

// Issue mints a refresh token for userID and stores only its hash, returning
// the secret the client must keep. The caller is responsible for handing it out
// over the same response as the access token it pairs with.
//
// The token starts a new family, so this opens a session; rotating an existing
// one goes through Rotate, which keeps the family intact.
func (r *RefreshTokens) Issue(ctx context.Context, userID uuid.UUID) (string, time.Time, error) {
	return r.issue(ctx, userID, uuid.New())
}

// issue stores one token in familyID and returns the secret half of it.
func (r *RefreshTokens) issue(ctx context.Context, userID, familyID uuid.UUID) (string, time.Time, error) {
	token, err := randomToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expires := time.Now().Add(r.ttl)
	if err := r.store.CreateRefreshToken(ctx, db.CreateRefreshTokenParams{
		TokenHash: hashToken(token),
		UserID:    userID,
		FamilyID:  familyID,
		ExpiresAt: expires,
	}); err != nil {
		return "", time.Time{}, fmt.Errorf("create refresh token for %s: %w", userID, err)
	}
	return token, expires, nil
}

// Owner returns the user a refresh token belongs to without spending it, so a
// caller can do the work that must succeed before the exchange is committed.
//
// It returns ErrInvalidToken when the token is unknown, already used, revoked
// or expired.
func (r *RefreshTokens) Owner(ctx context.Context, token string) (uuid.UUID, error) {
	hash := hashToken(token)
	row, err := r.store.GetActiveRefreshToken(ctx, hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, r.reject(ctx, hash)
		}
		return uuid.Nil, fmt.Errorf("lookup refresh token: %w", err)
	}
	return row.UserID, nil
}

// reject turns a token that cannot be exchanged into the error to return to the
// client, first revoking the rest of its family when the token was already
// spent.
//
// A spent token coming back means it survived somewhere it should not have, and
// whoever holds it may hold its replacement too, so the entire chain of
// rotations is ended: the legitimate client is signed out alongside the
// attacker, which is the intended trade. Unknown and merely expired tokens
// carry no such signal and only fail the exchange, and families of the same
// user's other sessions are left alone.
//
// It returns ErrInvalidToken unless the store itself failed, so the client
// cannot tell the cases apart. A failed family revocation is returned instead,
// since leaving a compromised chain alive is worse than a failed refresh.
func (r *RefreshTokens) reject(ctx context.Context, hash string) error {
	row, err := r.store.GetRefreshToken(ctx, hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidToken
		}
		return fmt.Errorf("lookup refresh token: %w", err)
	}
	if row.RevokedAt == nil {
		return ErrInvalidToken
	}
	rows, err := r.store.RevokeRefreshTokenFamily(ctx, row.FamilyID)
	if err != nil {
		return fmt.Errorf("revoke refresh token family %s: %w", row.FamilyID, err)
	}
	slog.WarnContext(ctx, "spent refresh token presented, revoked its family",
		"user_id", row.UserID, "family_id", row.FamilyID, "revoked", rows)
	return ErrInvalidToken
}

// Rotate consumes a refresh token and returns its owner along with the
// replacement token, which stays in the presented token's family so a later
// replay of any link in the chain can end all of it.
//
// It returns ErrInvalidToken when the token is unknown, already used, revoked
// or expired; any other error means the exchange could not be completed and the
// caller should retry with the same token. Presenting a spent token also ends
// the rest of its family — see reject.
func (r *RefreshTokens) Rotate(ctx context.Context, token string) (uuid.UUID, string, time.Time, error) {
	hash := hashToken(token)
	row, err := r.store.GetActiveRefreshToken(ctx, hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, "", time.Time{}, r.reject(ctx, hash)
		}
		return uuid.Nil, "", time.Time{}, fmt.Errorf("lookup refresh token: %w", err)
	}
	// The replacement is minted before the presented token is spent, so a
	// failure here leaves the client with a token it can retry rather than no
	// session at all. The cost is an unreachable row when the revocation below
	// fails or loses the race, which the expiry sweep collects.
	replacement, expires, err := r.issue(ctx, row.UserID, row.FamilyID)
	if err != nil {
		return uuid.Nil, "", time.Time{}, err
	}
	rows, err := r.store.RevokeRefreshToken(ctx, hash)
	if err != nil {
		return uuid.Nil, "", time.Time{}, fmt.Errorf("revoke refresh token: %w", err)
	}
	// Losing the race to a concurrent refresh means the token was already spent.
	if rows == 0 {
		return uuid.Nil, "", time.Time{}, r.reject(ctx, hash)
	}
	return row.UserID, replacement, expires, nil
}

// RevokeAll ends every session of userID by revoking the tokens that could mint
// new access tokens. Access tokens already in flight keep working until they
// expire, which is what keeps them cheap to verify.
func (r *RefreshTokens) RevokeAll(ctx context.Context, userID uuid.UUID) error {
	if _, err := r.store.RevokeUserRefreshTokens(ctx, userID); err != nil {
		return fmt.Errorf("revoke refresh tokens for %s: %w", userID, err)
	}
	return nil
}

// hashToken returns the stored form of a refresh token. SHA-256 without a salt
// is sufficient because the input is 256 bits of entropy, not a password.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
