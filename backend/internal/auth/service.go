// Package auth implements sign-up, sign-in, email verification, JWT issuing and
// the request middleware used by the rest of the API.
package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"

	"github.com/danielliu30/dating-coach/backend/internal/notify"
	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

var (
	ErrEmailTaken         = errors.New("email already registered")
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrInvalidInput       = errors.New("invalid input")
	ErrInvalidCode        = errors.New("invalid or expired verification code")
)

const (
	verificationTTL         = 3 * time.Minute
	maxVerificationAttempts = 3
	codeSpace               = 1000000 // 6-digit numeric codes
)

type Service struct {
	queries    *db.Queries
	issuer     *TokenIssuer
	notifier   notify.Notifier
	cache      *VerificationCache
	bcryptCost int
}

func NewService(queries *db.Queries, issuer *TokenIssuer, notifier notify.Notifier, cache *VerificationCache, bcryptCost int) *Service {
	return &Service{queries: queries, issuer: issuer, notifier: notifier, cache: cache, bcryptCost: bcryptCost}
}

type SignUpInput struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

type Session struct {
	Token     string  `json:"token"`
	ExpiresAt string  `json:"expires_at"`
	User      Profile `json:"user"`
}

type Profile struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	DisplayName   string `json:"display_name"`
	Role          string `json:"role"`
	EmailVerified bool   `json:"email_verified"`
}

func profileOf(u db.User) Profile {
	return Profile{
		ID:            u.ID.String(),
		Email:         u.Email,
		DisplayName:   u.DisplayName,
		Role:          u.Role,
		EmailVerified: u.EmailVerified,
	}
}

// SignUp creates the account and returns a session, so a new account is signed
// in immediately; email verification is tracked separately on the profile.
func (s *Service) SignUp(ctx context.Context, in SignUpInput) (Session, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if _, err := mail.ParseAddress(email); err != nil {
		return Session{}, fmt.Errorf("%w: email is not valid", ErrInvalidInput)
	}
	if len(in.Password) < 8 {
		return Session{}, fmt.Errorf("%w: password must be at least 8 characters", ErrInvalidInput)
	}
	displayName := strings.TrimSpace(in.DisplayName)
	if displayName == "" {
		return Session{}, fmt.Errorf("%w: display_name is required", ErrInvalidInput)
	}
	role := in.Role
	if role == "" {
		role = RoleUser
	}
	if role != RoleUser && role != RoleCoach {
		return Session{}, fmt.Errorf("%w: role must be user or coach", ErrInvalidInput)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), s.bcryptCost)
	if err != nil {
		return Session{}, fmt.Errorf("hash password: %w", err)
	}

	code, err := randomCode()
	if err != nil {
		return Session{}, err
	}

	user, err := s.queries.CreateUser(ctx, db.CreateUserParams{
		Email:        email,
		PasswordHash: string(hash),
		DisplayName:  displayName,
		Role:         role,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Session{}, ErrEmailTaken
		}
		return Session{}, fmt.Errorf("create user: %w", err)
	}

	if err := s.cache.Store(ctx, user.ID.String(), code); err != nil {
		return Session{}, fmt.Errorf("store verification code: %w", err)
	}
	s.sendVerificationEmail(ctx, user.Email, code)

	jwtToken, expiresAt, err := s.issuer.Issue(user.ID, user.Email, user.Role)
	if err != nil {
		return Session{}, err
	}
	return Session{
		Token:     jwtToken,
		ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
		User:      profileOf(user),
	}, nil
}

func (s *Service) SignIn(ctx context.Context, email, password string) (Session, error) {
	user, err := s.queries.GetUserByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Session{}, ErrInvalidCredentials
		}
		return Session{}, fmt.Errorf("lookup user: %w", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return Session{}, ErrInvalidCredentials
	}

	token, expires, err := s.issuer.Issue(user.ID, user.Email, user.Role)
	if err != nil {
		return Session{}, err
	}
	return Session{
		Token:     token,
		ExpiresAt: expires.UTC().Format(time.RFC3339),
		User:      profileOf(user),
	}, nil
}

func (s *Service) VerifyEmail(ctx context.Context, email, code string) (Profile, error) {
	user, err := s.queries.GetUserByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Profile{}, ErrInvalidCode
		}
		return Profile{}, fmt.Errorf("lookup user: %w", err)
	}
	if user.EmailVerified {
		return profileOf(user), nil
	}

	storedCode, _, err := s.cache.Get(ctx, user.ID.String())
	if err != nil {
		return Profile{}, fmt.Errorf("read verification code: %w", err)
	}
	if storedCode == "" || storedCode != code {
		if err := s.handleFailedAttempt(ctx, user.ID.String()); err != nil {
			return Profile{}, err
		}
		return Profile{}, ErrInvalidCode
	}

	// Consume the code immediately so it cannot be reused.
	if err := s.cache.Invalidate(ctx, user.ID.String()); err != nil {
		return Profile{}, fmt.Errorf("invalidate verification code: %w", err)
	}

	verifiedUser, err := s.queries.VerifyUserEmail(ctx, user.ID)
	if err != nil {
		return Profile{}, fmt.Errorf("verify email: %w", err)
	}

	// Ignore failed-attempt cleanup errors; the code is already consumed.
	_ = s.cache.Invalidate(ctx, user.ID.String())

	return profileOf(verifiedUser), nil
}

func (s *Service) handleFailedAttempt(ctx context.Context, userID string) error {
	count, err := s.cache.IncrementAttempts(ctx, userID)
	if err != nil {
		return fmt.Errorf("increment attempts: %w", err)
	}
	if count >= maxVerificationAttempts {
		if err := s.cache.Invalidate(ctx, userID); err != nil {
			return fmt.Errorf("invalidate after max attempts: %w", err)
		}
	}
	return nil
}

func (s *Service) ResendVerification(ctx context.Context, email string) error {
	user, err := s.queries.GetUserByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // Do not leak which emails exist.
		}
		return fmt.Errorf("lookup user: %w", err)
	}
	if user.EmailVerified {
		return nil
	}
	code, err := randomCode()
	if err != nil {
		return err
	}
	if err := s.cache.Store(ctx, user.ID.String(), code); err != nil {
		return fmt.Errorf("store verification code: %w", err)
	}
	s.sendVerificationEmail(ctx, user.Email, code)
	return nil
}

func (s *Service) Profile(ctx context.Context, principal Principal) (Profile, error) {
	user, err := s.queries.GetUserByID(ctx, principal.UserID)
	if err != nil {
		return Profile{}, fmt.Errorf("get user: %w", err)
	}
	return profileOf(user), nil
}

func (s *Service) sendVerificationEmail(ctx context.Context, email, code string) {
	body := fmt.Sprintf("Welcome to Dating Coach!\n\nYour verification code is: %s\n\nThis code expires in 3 minutes and will be invalidated after 3 failed attempts.", code)
	if err := s.notifier.Email(ctx, email, "Verify your Dating Coach email", body); err != nil {
		slog.Error("send verification email", "error", err, "email", email)
	}
}

func randomCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(codeSpace))
	if err != nil {
		return "", fmt.Errorf("generate code: %w", err)
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}
