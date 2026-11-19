package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

var ErrInvalidResetCode = errors.New("invalid or expired reset code")

func (s *Service) ForgotPassword(ctx context.Context, email string) error {
	user, err := s.queries.GetUserByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lookup reset account: %w", err)
	}
	code, err := randomCode()
	if err != nil {
		return err
	}
	if err := s.resetCodes.Store(ctx, user.ID.String(), code); err != nil {
		return fmt.Errorf("store reset code: %w", err)
	}
	body := fmt.Sprintf("Your password reset code is: %s\n\nThis code expires in %g minutes and is discarded after %d wrong attempts.", code, s.resetCodes.ttl.Minutes(), maxVerificationAttempts)
	if err := s.notifier.Email(ctx, user.Email, "Reset your Dating Coach password", body); err != nil {
		slog.Error("send password reset email", "error", err, "user_id", user.ID)
	}
	return nil
}

func (s *Service) ResetPassword(ctx context.Context, email, code, password string) error {
	if err := validatePassword(password); err != nil {
		return err
	}
	user, err := s.queries.GetUserByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidResetCode
	}
	if err != nil {
		return fmt.Errorf("lookup reset account: %w", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.bcryptCost)
	if err != nil {
		return fmt.Errorf("hash reset password: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)
	if _, err := q.LockUserForRefresh(ctx, user.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidResetCode
		}
		return err
	}
	userID := user.ID.String()
	consumed, err := s.resetCodes.Consume(ctx, userID, strings.TrimSpace(code))
	if err != nil {
		return err
	}
	if !consumed {
		count, err := s.resetCodes.IncrementAttempts(ctx, userID)
		if err != nil {
			return err
		}
		if count >= maxVerificationAttempts {
			if err := s.resetCodes.Invalidate(ctx, userID); err != nil {
				return err
			}
		}
		return ErrInvalidResetCode
	}
	if err := q.SetUserPassword(ctx, db.SetUserPasswordParams{ID: user.ID, PasswordHash: string(hash)}); err != nil {
		return err
	}
	if _, err := q.VerifyUserEmail(ctx, user.ID); err != nil {
		return err
	}
	if _, err := q.RevokeUserRefreshTokens(ctx, user.ID); err != nil {
		return err
	}
	at := time.Now().Truncate(time.Microsecond)
	if err := q.RecordSessionRevocation(ctx, db.RecordSessionRevocationParams{ID: user.ID, RevokedAt: at}); err != nil {
		return err
	}
	if s.denylist != nil {
		if err := s.denylist.RevokeSessionsAt(ctx, user.ID, at); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}
