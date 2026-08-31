package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// ErrInvalidRefreshToken is returned for a refresh token that is unknown,
// expired, or already spent. The cases are deliberately indistinguishable to
// the client, which can only react by signing in again.
var ErrInvalidRefreshToken = errors.New("invalid or expired refresh token")

// openSession issues the pair a signed-in caller needs: a short-lived
// session-scoped access token, and a refresh token to mint the next one with.
// The expiry it reports is the access token's, so clients renew before it runs
// out rather than after a request has already failed.
//
// q is the handle the refresh token is written through, so a caller rotating
// one can pass a transaction and have the new token share its fate.
func (s *Service) openSession(ctx context.Context, q *db.Queries, user db.User) (Session, error) {
	token, expires, err := s.issuer.Issue(user.ID, user.Email, user.Role, ScopeSession, s.sessionTTL)
	if err != nil {
		return Session{}, err
	}
	refresh, err := s.issueRefreshToken(ctx, q, user)
	if err != nil {
		return Session{}, err
	}
	return Session{
		Token:        token,
		RefreshToken: refresh,
		ExpiresAt:    expires.UTC().Format(time.RFC3339),
		User:         profileOf(user),
	}, nil
}

// issueRefreshToken stores a new refresh token for user and returns its plain
// text, which is the only moment that value exists outside the client: the row
// holds nothing but its hash, so the table cannot be replayed against the API.
func (s *Service) issueRefreshToken(ctx context.Context, q *db.Queries, user db.User) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	if _, err := q.CreateRefreshToken(ctx, db.CreateRefreshTokenParams{
		UserID:    user.ID,
		TokenHash: hashRefreshToken(token),
		ExpiresAt: time.Now().Add(s.refreshTTL),
	}); err != nil {
		return "", fmt.Errorf("create refresh token: %w", err)
	}
	return token, nil
}

// Refresh exchanges a refresh token for a new session, rotating the token: the
// presented one is spent and can never be exchanged again.
//
// Presenting a spent token is treated as theft rather than as a mistake, since
// the legitimate client has already moved on to the token it was given back:
// every refresh token of that account is deleted, which ends the sessions of
// both the attacker and the victim, and the account has to sign in again. The
// access tokens already minted survive until they expire, which is minutes.
//
// Spending the presented token and storing its replacement share a
// transaction, so a failure half way through cannot leave the caller holding a
// token that is spent but was never exchanged for another.
//
// It returns ErrInvalidRefreshToken for a token that is unknown, expired or
// spent, including the reuse case.
func (s *Service) Refresh(ctx context.Context, raw string) (Session, error) {
	if raw == "" {
		return Session{}, ErrInvalidRefreshToken
	}
	row, err := s.queries.GetRefreshToken(ctx, hashRefreshToken(raw))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Session{}, ErrInvalidRefreshToken
		}
		return Session{}, fmt.Errorf("lookup refresh token: %w", err)
	}
	if row.UsedAt != nil {
		slog.Warn("refresh token reused", "user_id", row.UserID)
		if _, err := s.queries.RevokeUserRefreshTokens(ctx, row.UserID); err != nil {
			return Session{}, fmt.Errorf("revoke reused refresh tokens: %w", err)
		}
		return Session{}, ErrInvalidRefreshToken
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Session{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	// The update is the rotation: it only matches a token that is still unspent
	// and unexpired, so two concurrent requests cannot both be served.
	rows, err := q.UseRefreshToken(ctx, row.ID)
	if err != nil {
		return Session{}, fmt.Errorf("spend refresh token: %w", err)
	}
	if rows == 0 {
		return Session{}, ErrInvalidRefreshToken
	}

	user, err := q.GetUserByID(ctx, row.UserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Session{}, ErrInvalidRefreshToken
		}
		return Session{}, fmt.Errorf("get user: %w", err)
	}
	session, err := s.openSession(ctx, q, user)
	if err != nil {
		return Session{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Session{}, fmt.Errorf("commit rotation: %w", err)
	}
	// Logged after the commit, so a rotation that rolled back leaves no record
	// of a session that never became usable. The token values stay out of the
	// log, which would otherwise be a credential store.
	slog.Info("refresh token rotated",
		"user_id", row.UserID,
		"spent_token_id", row.ID,
		"expires_at", session.ExpiresAt,
	)
	return session, nil
}

// hashRefreshToken returns the hex SHA-256 of a refresh token, which is what
// the database stores and looks rows up by. A plain hash is enough where a
// password needs bcrypt: the token is 256 bits of entropy, not a guessable
// secret.
func hashRefreshToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
