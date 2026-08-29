// Package auth implements sign-up, sign-in, email verification, JWT issuing and
// the request middleware used by the rest of the API.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
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
	ErrInvalidToken       = errors.New("invalid or expired verification token")
)

const verificationTTL = 48 * time.Hour

type Service struct {
	queries    *db.Queries
	issuer     *TokenIssuer
	notifier   notify.Notifier
	bcryptCost int
	appURL     string
}

func NewService(queries *db.Queries, issuer *TokenIssuer, notifier notify.Notifier, bcryptCost int, appURL string) *Service {
	return &Service{queries: queries, issuer: issuer, notifier: notifier, bcryptCost: bcryptCost, appURL: appURL}
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

func (s *Service) SignUp(ctx context.Context, in SignUpInput) (Profile, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if _, err := mail.ParseAddress(email); err != nil {
		return Profile{}, fmt.Errorf("%w: email is not valid", ErrInvalidInput)
	}
	if len(in.Password) < 8 {
		return Profile{}, fmt.Errorf("%w: password must be at least 8 characters", ErrInvalidInput)
	}
	displayName := strings.TrimSpace(in.DisplayName)
	if displayName == "" {
		return Profile{}, fmt.Errorf("%w: display_name is required", ErrInvalidInput)
	}
	role := in.Role
	if role == "" {
		role = RoleUser
	}
	if role != RoleUser && role != RoleCoach {
		return Profile{}, fmt.Errorf("%w: role must be user or coach", ErrInvalidInput)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), s.bcryptCost)
	if err != nil {
		return Profile{}, fmt.Errorf("hash password: %w", err)
	}

	token, err := randomToken()
	if err != nil {
		return Profile{}, err
	}
	expires := time.Now().Add(verificationTTL)

	user, err := s.queries.CreateUser(ctx, db.CreateUserParams{
		Email:                 email,
		PasswordHash:          string(hash),
		DisplayName:           displayName,
		Role:                  role,
		VerificationToken:     &token,
		VerificationExpiresAt: &expires,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Profile{}, ErrEmailTaken
		}
		return Profile{}, fmt.Errorf("create user: %w", err)
	}

	s.sendVerificationEmail(ctx, user.Email, token)
	return profileOf(user), nil
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

func (s *Service) VerifyEmail(ctx context.Context, token string) (Profile, error) {
	user, err := s.queries.VerifyUserEmail(ctx, &token)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Profile{}, ErrInvalidToken
		}
		return Profile{}, fmt.Errorf("verify email: %w", err)
	}
	return profileOf(user), nil
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
	token, err := randomToken()
	if err != nil {
		return err
	}
	expires := time.Now().Add(verificationTTL)
	if err := s.queries.SetVerificationToken(ctx, db.SetVerificationTokenParams{
		ID:                    user.ID,
		VerificationToken:     &token,
		VerificationExpiresAt: &expires,
	}); err != nil {
		return fmt.Errorf("set verification token: %w", err)
	}
	s.sendVerificationEmail(ctx, user.Email, token)
	return nil
}

func (s *Service) Profile(ctx context.Context, principal Principal) (Profile, error) {
	user, err := s.queries.GetUserByID(ctx, principal.UserID)
	if err != nil {
		return Profile{}, fmt.Errorf("get user: %w", err)
	}
	return profileOf(user), nil
}

func (s *Service) sendVerificationEmail(ctx context.Context, email, token string) {
	link := fmt.Sprintf("%s/verify?token=%s", strings.TrimRight(s.appURL, "/"), token)
	body := fmt.Sprintf("Welcome to Dating Coach!\n\nVerify your email address: %s\n\nThis link expires in 48 hours.", link)
	if err := s.notifier.Email(ctx, email, "Verify your Dating Coach email", body); err != nil {
		slog.Error("send verification email", "error", err, "email", email)
	}
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
