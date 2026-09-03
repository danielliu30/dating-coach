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
	"math/big"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
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
	ErrInvalidCode        = errors.New("invalid or expired verification code")
	ErrEmailNotVerified   = errors.New("email address is not verified")
)

const (
	// VerificationCodeTTL is how long an emailed verification code stays valid.
	VerificationCodeTTL = 3 * time.Minute
	// maxVerificationAttempts is the number of wrong codes accepted before the
	// code is discarded and a new one has to be requested.
	maxVerificationAttempts = 3
	codeSpace               = 1000000 // 6-digit numeric codes
)

// Service holds the account business rules: it validates credentials, hashes
// passwords, mints sessions through the TokenIssuer and drives the email
// verification flow. Handler is its only caller.
type Service struct {
	pool           *pgxpool.Pool
	queries        *db.Queries
	issuer         *TokenIssuer
	notifier       notify.Notifier
	bcryptCost     int
	appURL         string
	codes          *VerificationCache
	denylist       *Denylist
	deletions      DeletionPublisher
	sessionTTL     time.Duration
	verifyTokenTTL time.Duration
	refreshTTL     time.Duration
}

// DeletionPublisher queues the row removal that follows a revoked account.
// Service depends on the interface so cmd/api owns the broker connection.
type DeletionPublisher interface {
	Publish(ctx context.Context, job account.Job) error
}

// NewService wires the service dependencies; called once from cmd/api. pool is
// needed for the one operation that spans statements, refresh-token rotation.
// sessionTTL is the lifetime of the access token issued at sign-in and is kept
// short, verifyTokenTTL that of the verify-scoped token issued at sign-up, and
// refreshTTL that of the refresh token clients trade in for new access tokens.
// codes holds the emailed verification codes and their failed-attempt counters.
// denylist is the record open chat sockets re-check, which deletion writes to so
// a live socket is closed at once rather than at its access token's expiry.
func NewService(
	pool *pgxpool.Pool,
	queries *db.Queries,
	issuer *TokenIssuer,
	notifier notify.Notifier,
	bcryptCost int,
	appURL string,
	codes *VerificationCache,
	denylist *Denylist,
	deletions DeletionPublisher,
	sessionTTL time.Duration,
	verifyTokenTTL time.Duration,
	refreshTTL time.Duration,
) *Service {
	return &Service{
		pool:           pool,
		queries:        queries,
		issuer:         issuer,
		notifier:       notifier,
		bcryptCost:     bcryptCost,
		appURL:         appURL,
		codes:          codes,
		denylist:       denylist,
		deletions:      deletions,
		sessionTTL:     sessionTTL,
		verifyTokenTTL: verifyTokenTTL,
		refreshTTL:     refreshTTL,
	}
}

// DeleteAccount marks userID's account deleted, so no new session can be handed
// out for it, and records the removal of its rows in the deletion outbox. Both
// happen in one statement, and it is the only failure a caller is told about:
// once it has committed, the deletion is certain, because the relay queues every
// recorded deletion whether or not this request manages to.
//
// The mark and the outbox row are what have to be atomic. A mark without the
// record is an account its owner can no longer use and nothing is going to
// delete; a record without the mark is an account that can mint one more token
// while it waits. Neither is reachable through a single statement: a broker or
// Redis that is down cannot separate them, and a database that is down leaves
// the account exactly as it was for the caller to retry.
//
// The steps that follow are optimisations, so they only log. Deleting the
// refresh tokens and revoking the sessions end the account's access in
// milliseconds rather than whenever the worker gets to the job; the mark alone
// already stops both, since a rotation reads the account row and a socket
// re-checks it, and the worker revokes before it deletes anything. The access
// token in the caller's hand keeps working until it expires, which is minutes
// away.
func (s *Service) DeleteAccount(ctx context.Context, userID uuid.UUID) error {
	rows, err := s.queries.RequestUserDeletion(ctx, userID)
	if err != nil {
		return fmt.Errorf("record account deletion: %w", err)
	}
	if rows == 0 {
		// The row is already gone, so there is nothing left to delete or revoke.
		return nil
	}
	if _, err := s.queries.RevokeUserRefreshTokens(ctx, userID); err != nil {
		slog.Error("revoke refresh tokens of a deleted account", "error", err, "user_id", userID)
	}
	if err := s.denylist.Revoke(ctx, userID); err != nil {
		slog.Error("revoke sessions of a deleted account", "error", err, "user_id", userID)
	}
	if err := s.deletions.Publish(ctx, account.Job{UserID: userID.String()}); err != nil {
		slog.Warn("leaving a recorded deletion to the relay", "error", err, "user_id", userID)
		return nil
	}
	if _, err := s.queries.MarkDeletionPublished(ctx, userID); err != nil {
		slog.Error("mark a queued deletion published", "error", err, "user_id", userID)
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
//
// RefreshToken is empty on responses that do not open a full session (sign-up,
// and verifying an address the caller does not hold a token for): only a caller
// given a session-scoped access token also gets the means to renew it.
type Session struct {
	Token        string  `json:"token"`
	RefreshToken string  `json:"refresh_token,omitempty"`
	ExpiresAt    string  `json:"expires_at"`
	User         Profile `json:"user"`
}

// Profile is the public view of a user row, also returned by GET /auth/me.
type Profile struct {
	ID              string   `json:"id"`
	Email           string   `json:"email"`
	DisplayName     string   `json:"display_name"`
	Role            string   `json:"role"`
	EmailVerified   bool     `json:"email_verified"`
	DatingStyles    []string `json:"dating_styles"`
	PhasesStrong    []string `json:"phases_strong"`
	PhasesWorkingOn []string `json:"phases_working_on"`
}

// DatingStyles is the vocabulary of where a user meets people, as stored in
// users.dating_styles and accepted by PATCH /auth/me.
var DatingStyles = []string{
	"in_person", "tinder", "hinge", "bumble", "coffee_meets_bagel",
	"match", "okcupid", "feeld", "speed_dating", "friends_intro",
}

// DatingPhases is the vocabulary of stages in a dating flow, in the order they
// happen; each may be listed as a strength or as being worked on, not both.
var DatingPhases = []string{
	"opening", "first_messages", "building_rapport", "flirting",
	"asking_out", "first_date", "follow_up", "defining_relationship",
}

// DatingProfileInput is the decoded PATCH /auth/me body. Every list replaces the
// stored one wholesale; a nil list is treated as empty.
type DatingProfileInput struct {
	DatingStyles    []string `json:"dating_styles"`
	PhasesStrong    []string `json:"phases_strong"`
	PhasesWorkingOn []string `json:"phases_working_on"`
}

// profileOf projects a database row onto the API shape, keeping the password
// hash and verification token out of responses.
func profileOf(u db.User) Profile {
	return Profile{
		ID:              u.ID.String(),
		Email:           u.Email,
		DisplayName:     u.DisplayName,
		Role:            u.Role,
		EmailVerified:   u.EmailVerified,
		DatingStyles:    nonNil(u.DatingStyles),
		PhasesStrong:    nonNil(u.PhasesStrong),
		PhasesWorkingOn: nonNil(u.PhasesWorkingOn),
	}
}

// nonNil returns an empty slice in place of nil so lists serialise as [] rather
// than null.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// normaliseChoices lowercases and trims values, drops blanks and duplicates,
// and returns them in vocabulary order. It fails with ErrInvalidInput, naming
// field, when a value is not in the vocabulary.
func normaliseChoices(field string, values, vocabulary []string) ([]string, error) {
	chosen := make(map[string]bool, len(values))
	for _, v := range values {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" {
			continue
		}
		if !slices.Contains(vocabulary, v) {
			return nil, fmt.Errorf("%w: %s contains unknown value %q", ErrInvalidInput, field, v)
		}
		chosen[v] = true
	}
	out := make([]string, 0, len(chosen))
	for _, v := range vocabulary {
		if chosen[v] {
			out = append(out, v)
		}
	}
	return out, nil
}

// UpdateDatingProfile validates in against the vocabularies and replaces the
// caller's dating styles and phases, returning the updated profile. A phase
// listed both as a strength and as being worked on is rejected with
// ErrInvalidInput rather than resolved silently.
func (s *Service) UpdateDatingProfile(ctx context.Context, principal Principal, in DatingProfileInput) (Profile, error) {
	styles, err := normaliseChoices("dating_styles", in.DatingStyles, DatingStyles)
	if err != nil {
		return Profile{}, err
	}
	strong, err := normaliseChoices("phases_strong", in.PhasesStrong, DatingPhases)
	if err != nil {
		return Profile{}, err
	}
	working, err := normaliseChoices("phases_working_on", in.PhasesWorkingOn, DatingPhases)
	if err != nil {
		return Profile{}, err
	}
	for _, p := range strong {
		if slices.Contains(working, p) {
			return Profile{}, fmt.Errorf("%w: phase %q cannot be both a strength and being worked on", ErrInvalidInput, p)
		}
	}
	user, err := s.queries.UpdateUserDatingProfile(ctx, db.UpdateUserDatingProfileParams{
		ID:              principal.UserID,
		DatingStyles:    styles,
		PhasesStrong:    strong,
		PhasesWorkingOn: working,
	})
	if err != nil {
		return Profile{}, fmt.Errorf("update dating profile: %w", err)
	}
	return profileOf(user), nil
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

	// The row and the code have to exist together: a registered address with no
	// code cannot be verified and blocks every retry with ErrEmailTaken, so a
	// failed store takes the row back out and lets the caller sign up again.
	if err := s.codes.Store(ctx, user.ID.String(), code); err != nil {
		if _, delErr := s.queries.DeleteUser(ctx, user.ID); delErr != nil {
			slog.Error("roll back sign-up after failed code store", "error", delErr, "user_id", user.ID)
		}
		return Session{}, fmt.Errorf("store verification code: %w", err)
	}
	s.sendVerificationEmail(ctx, user.Email, code)

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

// SignIn verifies the password and opens a session. Unknown emails and wrong
// passwords both return ErrInvalidCredentials; an unverified account returns
// ErrEmailNotVerified, only after the password has been checked.
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

	session, err := s.openSession(ctx, s.queries, user)
	if err != nil {
		return Session{}, err
	}
	slog.Info("session issued", "user_id", user.ID, "reason", "sign in",
		"expires_at", session.ExpiresAt)
	return session, nil
}

// VerifyEmail checks the emailed code for email, marks the address confirmed
// and returns the updated profile. A wrong code counts as a failed attempt and
// the code is discarded after maxVerificationAttempts of them; an unknown
// address, a missing or expired code and a wrong code all return
// ErrInvalidCode. An already verified address succeeds without a code.
//
// The code is only removed once the account row has been updated, so a database
// failure leaves it in place for the caller to retry with the same code.
//
// bearer is the caller's current JWT, or "" when the request is
// unauthenticated. When it is the verify-scoped token this very account was
// given at sign-up, the returned Session also carries a session-scoped access
// token and a refresh token, so the caller leaves verification with credentials
// that reach the private API instead of one every private route rejects. A
// bearer belonging to another account, an expired one, or none at all yields
// the profile only: verifying never hands a session to whoever merely holds the
// emailed code.
func (s *Service) VerifyEmail(ctx context.Context, email, code, bearer string) (Session, error) {
	user, err := s.queries.GetUserByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Session{}, ErrInvalidCode
		}
		return Session{}, fmt.Errorf("lookup user: %w", err)
	}
	if !user.EmailVerified {
		userID := user.ID.String()
		stored, _, err := s.codes.Get(ctx, userID)
		if err != nil {
			return Session{}, fmt.Errorf("read verification code: %w", err)
		}
		if stored == "" || stored != strings.TrimSpace(code) {
			if err := s.recordFailedAttempt(ctx, userID); err != nil {
				return Session{}, err
			}
			return Session{}, ErrInvalidCode
		}
		user, err = s.queries.VerifyUserEmail(ctx, user.ID)
		if err != nil {
			return Session{}, fmt.Errorf("verify email: %w", err)
		}
		if err := s.codes.Invalidate(ctx, userID); err != nil {
			slog.Error("discard consumed verification code", "error", err, "user_id", user.ID)
		}
	}
	profile := profileOf(user)
	if !s.ownsBearer(user.ID, bearer) {
		return Session{User: profile}, nil
	}
	session, err := s.openSession(ctx, s.queries, user)
	if err != nil {
		return Session{}, err
	}
	slog.Info("session issued", "user_id", user.ID, "reason", "verify email",
		"expires_at", session.ExpiresAt)
	return session, nil
}

// recordFailedAttempt counts one wrong code for userID and discards the code
// once maxVerificationAttempts is reached, so the caller has to request a new
// one. The returned error is a Redis failure, not a verdict on the code.
func (s *Service) recordFailedAttempt(ctx context.Context, userID string) error {
	count, err := s.codes.IncrementAttempts(ctx, userID)
	if err != nil {
		return fmt.Errorf("increment verification attempts: %w", err)
	}
	if count >= maxVerificationAttempts {
		if err := s.codes.Invalidate(ctx, userID); err != nil {
			return fmt.Errorf("invalidate verification code after %d attempts: %w", count, err)
		}
	}
	return nil
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

// ResendVerification issues a fresh code, replacing any outstanding one, and
// re-sends the email. It succeeds for unknown or already verified addresses so
// callers cannot enumerate users.
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
	if err := s.codes.Store(ctx, user.ID.String(), code); err != nil {
		return fmt.Errorf("store verification code: %w", err)
	}
	s.sendVerificationEmail(ctx, user.Email, code)
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

// sendVerificationEmail delivers the code to the address. A delivery failure is
// logged, not returned: sign-up itself already succeeded.
func (s *Service) sendVerificationEmail(ctx context.Context, email, code string) {
	body := fmt.Sprintf("Welcome to Dating Coach!\n\nYour verification code is: %s\n\nThis code expires in %d minutes and is discarded after %d wrong attempts.",
		code, int(VerificationCodeTTL.Minutes()), maxVerificationAttempts)
	if err := s.notifier.Email(ctx, email, "Verify your Dating Coach email", body); err != nil {
		slog.Error("send verification email", "error", err, "email", email)
	}
}

// randomToken returns a 256-bit hex string used as a refresh token.
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// randomCode returns a uniformly random, zero-padded 6-digit verification code.
func randomCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(codeSpace))
	if err != nil {
		return "", fmt.Errorf("generate code: %w", err)
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}
