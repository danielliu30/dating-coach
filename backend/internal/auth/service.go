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

	"github.com/google/uuid"
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
	queries        *db.Queries
	issuer         *TokenIssuer
	notifier       notify.Notifier
	bcryptCost     int
	appURL         string
	sessionTTL     time.Duration
	verifyTokenTTL time.Duration
}

func NewService(queries *db.Queries, issuer *TokenIssuer, notifier notify.Notifier, bcryptCost int, appURL string, sessionTTL, verifyTokenTTL time.Duration) *Service {
	return &Service{
		queries:        queries,
		issuer:         issuer,
		notifier:       notifier,
		bcryptCost:     bcryptCost,
		appURL:         appURL,
		sessionTTL:     sessionTTL,
		verifyTokenTTL: verifyTokenTTL,
	}
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

	token, err := randomToken()
	if err != nil {
		return Session{}, err
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
			return Session{}, ErrEmailTaken
		}
		return Session{}, fmt.Errorf("create user: %w", err)
	}

	s.sendVerificationEmail(ctx, user.Email, token)

	// Sign-up mints a short-lived verify-scoped token: it can drive the verify
	// screen and reach /auth, but not the rest of the API.
	jwtToken, expiresAt, err := s.issuer.Issue(user.ID, user.Email, user.Role, ScopeVerify, s.verifyTokenTTL)
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

	token, expires, err := s.issuer.Issue(user.ID, user.Email, user.Role, ScopeSession, s.sessionTTL)
	if err != nil {
		return Session{}, err
	}
	return Session{
		Token:     token,
		ExpiresAt: expires.UTC().Format(time.RFC3339),
		User:      profileOf(user),
	}, nil
}

// VerifyEmail confirms the address behind a verification token and returns the
// updated profile.
//
// bearer is the caller's current JWT, or empty when the request is
// unauthenticated. When it is the verify-scoped token this very account was
// given at sign-up, the returned Session also carries a full session token, so
// the caller leaves verification with credentials that reach the private API
// instead of a token every private route rejects. A bearer for a different
// account, an expired one, or none at all only yields the profile: verifying
// never hands out a session to whoever merely holds the emailed token.
func (s *Service) VerifyEmail(ctx context.Context, token, bearer string) (Session, error) {
	user, err := s.queries.VerifyUserEmail(ctx, &token)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Session{}, ErrInvalidToken
		}
		return Session{}, fmt.Errorf("verify email: %w", err)
	}
	profile := profileOf(user)
	if !s.ownsBearer(user.ID, bearer) {
		return Session{User: profile}, nil
	}
	sessionToken, expires, err := s.issuer.Issue(user.ID, user.Email, user.Role, ScopeSession, s.sessionTTL)
	if err != nil {
		return Session{}, fmt.Errorf("issue session: %w", err)
	}
	return Session{
		Token:     sessionToken,
		ExpiresAt: expires.UTC().Format(time.RFC3339),
		User:      profile,
	}, nil
}

// ownsBearer reports whether bearer is a currently valid token for userID.
func (s *Service) ownsBearer(userID uuid.UUID, bearer string) bool {
	if bearer == "" {
		return false
	}
	claims, err := s.issuer.Parse(bearer)
	if err != nil {
		return false
	}
	subject, err := claims.UserID()
	return err == nil && subject == userID
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
