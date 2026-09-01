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

	"github.com/danielliu30/dating-coach/backend/internal/account"
	"github.com/danielliu30/dating-coach/backend/internal/notify"
	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// Sentinel errors the handler maps onto HTTP status codes.
var (
	ErrEmailTaken         = errors.New("email already registered")
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrInvalidInput       = errors.New("invalid input")
	ErrInvalidToken       = errors.New("invalid or expired verification token")
	ErrEmailNotVerified   = errors.New("email address is not verified")
)

const verificationTTL = 48 * time.Hour

// Service holds the account business rules: it validates credentials, hashes
// passwords, mints sessions through the TokenIssuer and drives the email
// verification flow. Handler is its only caller.
type Service struct {
	queries        *db.Queries
	issuer         *TokenIssuer
	notifier       notify.Notifier
	bcryptCost     int
	appURL         string
	denylist       *Denylist
	deletions      DeletionPublisher
	sessionTTL     time.Duration
	verifyTokenTTL time.Duration
}

// DeletionPublisher queues the row removal that follows a revoked account.
// Service depends on the interface so cmd/api owns the broker connection.
type DeletionPublisher interface {
	Publish(ctx context.Context, job account.Job) error
}

// NewService wires the service dependencies; called once from cmd/api. denylist
// and deletions are the two halves of account deletion: revoke now, delete rows
// later. sessionTTL is the lifetime of the session token issued at sign-in;
// verifyTokenTTL that of the verify-scoped token issued at sign-up.
func NewService(
	queries *db.Queries,
	issuer *TokenIssuer,
	notifier notify.Notifier,
	bcryptCost int,
	appURL string,
	denylist *Denylist,
	deletions DeletionPublisher,
	sessionTTL time.Duration,
	verifyTokenTTL time.Duration,
) *Service {
	return &Service{
		queries:        queries,
		issuer:         issuer,
		notifier:       notifier,
		bcryptCost:     bcryptCost,
		appURL:         appURL,
		denylist:       denylist,
		deletions:      deletions,
		sessionTTL:     sessionTTL,
		verifyTokenTTL: verifyTokenTTL,
	}
}

// DeleteAccount queues the removal of userID's rows, marks the account deleted
// so no new session can be handed out for it, and ends the sessions it already
// has. It returns once the broker has confirmed the deletion job and the row is
// marked, which is the point from which the account is certain to be deleted and
// certain not to hand out another token.
//
// The job is queued before anything else, so a failure there leaves the account
// exactly as it was and the caller can retry: marking or revoking first would,
// on a broker that is down, leave the caller unable to use an account that then
// stays alive until an operator intervenes. Marking before revoking is what
// bounds the denylist entry: every token the entry has to outlive was minted
// before the mark, so it may expire once the token lifetime has passed even if
// the worker never removes the row. Revocation is best-effort last, since the
// worker revokes before it deletes anything and the queue retries it; a Redis
// failure here only delays the sessions dying from milliseconds to however long
// the job waits, which is not worth telling a caller their deletion failed when
// it will go through.
func (s *Service) DeleteAccount(ctx context.Context, userID uuid.UUID) error {
	if err := s.deletions.Publish(ctx, account.Job{UserID: userID.String()}); err != nil {
		return fmt.Errorf("queue account deletion: %w", err)
	}
	if _, err := s.queries.MarkUserDeleted(ctx, userID); err != nil {
		return fmt.Errorf("mark account deleted: %w", err)
	}
	if err := s.denylist.Revoke(ctx, userID); err != nil {
		slog.Error("revoke sessions of a deleted account", "error", err, "user_id", userID)
	}
	return nil
}

// SignUpInput is the decoded POST /auth/signup body.
type SignUpInput struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

// Session is the sign-up/sign-in response: a bearer token plus the profile the
// clients render right away.
type Session struct {
	Token     string  `json:"token"`
	ExpiresAt string  `json:"expires_at"`
	User      Profile `json:"user"`
}

// Profile is the public view of a user row, also returned by GET /auth/me.
type Profile struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	DisplayName   string `json:"display_name"`
	Role          string `json:"role"`
	EmailVerified bool   `json:"email_verified"`
}

// profileOf projects a database row onto the API shape, keeping the password
// hash and verification token out of responses.
func profileOf(u db.User) Profile {
	return Profile{
		ID:            u.ID.String(),
		Email:         u.Email,
		DisplayName:   u.DisplayName,
		Role:          u.Role,
		EmailVerified: u.EmailVerified,
	}
}

// SignUp creates the account and returns a verify-scoped session: the token it
// carries reaches the /auth endpoints only, so a brand new account can finish
// verification but cannot touch coaching, chat or analysis until it trades the
// token in for a session-scoped one.
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

// SignIn verifies the password and issues a session-scoped token. Unknown
// emails and wrong passwords both return ErrInvalidCredentials; an unverified
// account returns ErrEmailNotVerified, only after the password has been
// checked.
//
// An account awaiting the deletion worker is indistinguishable from an unknown
// one: the lookup skips rows marked deleted, so no session is ever minted for
// an account whose data is on its way out, however long the removal takes.
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
	if !user.EmailVerified {
		return Session{}, ErrEmailNotVerified
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

// VerifyEmail consumes a verification token, marks the address confirmed and
// returns the updated profile.
//
// bearer is the caller's current JWT, or "" when the request is
// unauthenticated. When it is the verify-scoped token this very account was
// given at sign-up, the returned Session also carries a session-scoped token,
// so the caller leaves verification with credentials that reach the private API
// instead of one every private route rejects. A bearer belonging to another
// account, an expired one, or none at all yields the profile only: verifying
// never hands a session to whoever merely holds the emailed token.
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

// ResendVerification issues a fresh token and re-sends the email. It succeeds
// for unknown or already verified addresses so callers cannot enumerate users.
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

// Profile reloads the authenticated caller from the database, so clients see
// changes made since the token was issued.
func (s *Service) Profile(ctx context.Context, principal Principal) (Profile, error) {
	user, err := s.queries.GetUserByID(ctx, principal.UserID)
	if err != nil {
		return Profile{}, fmt.Errorf("get user: %w", err)
	}
	return profileOf(user), nil
}

// sendVerificationEmail builds the deep link into the app and delivers it. A
// delivery failure is logged, not returned: sign-up itself already succeeded.
func (s *Service) sendVerificationEmail(ctx context.Context, email, token string) {
	link := fmt.Sprintf("%s/verify?token=%s", strings.TrimRight(s.appURL, "/"), token)
	body := fmt.Sprintf("Welcome to Dating Coach!\n\nVerify your email address: %s\n\nThis link expires in 48 hours.", link)
	if err := s.notifier.Email(ctx, email, "Verify your Dating Coach email", body); err != nil {
		slog.Error("send verification email", "error", err, "email", email)
	}
}

// randomToken returns a 256-bit hex string used as an email verification token.
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
