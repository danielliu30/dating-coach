package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Roles carried in the token; RoleAdmin is treated as a superset of RoleCoach.
const (
	RoleUser  = "user"
	RoleCoach = "coach"
	RoleAdmin = "admin"
)

// Claims is the JWT payload: the standard registered claims plus the role and
// email the middleware needs to build a Principal without a database round trip.
type Claims struct {
	jwt.RegisteredClaims
	Role  string `json:"role"`
	Email string `json:"email"`
}

// UserID parses the subject claim, which holds the user's UUID.
func (c Claims) UserID() (uuid.UUID, error) {
	return uuid.Parse(c.Subject)
}

// TokenIssuer signs and verifies the HS256 session tokens. One instance is
// shared by Service (issuing) and Middleware (verifying).
type TokenIssuer struct {
	secret []byte
	ttl    time.Duration
}

// NewTokenIssuer returns an issuer for the configured secret and token lifetime.
func NewTokenIssuer(secret string, ttl time.Duration) *TokenIssuer {
	return &TokenIssuer{secret: []byte(secret), ttl: ttl}
}

// Issue mints a signed token for a user and reports when it expires, which the
// sign-up and sign-in responses hand to clients as the session expiry.
func (t *TokenIssuer) Issue(userID uuid.UUID, email, role string) (string, time.Time, error) {
	expires := time.Now().Add(t.ttl)
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			Issuer:    "dating-coach-api",
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(expires),
		},
		Role:  role,
		Email: email,
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign token: %w", err)
	}
	return signed, expires, nil
}

// Parse validates a token's signature, algorithm and expiry, returning its
// claims. Used by Middleware on every authenticated request.
func (t *TokenIssuer) Parse(raw string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) {
		return t.secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil {
		return nil, fmt.Errorf("parse token: %w", err)
	}
	if !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	return claims, nil
}
