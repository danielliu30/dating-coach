package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	RoleUser  = "user"
	RoleCoach = "coach"
	RoleAdmin = "admin"
)

const (
	// ScopeVerify tokens are issued at sign-up and may only reach the /auth
	// endpoints; ScopeSession tokens are issued at sign-in and reach the rest of
	// the API. The scope is frozen for the token's lifetime.
	ScopeVerify  = "verify"
	ScopeSession = "session"
)

type Claims struct {
	jwt.RegisteredClaims
	Role  string `json:"role"`
	Email string `json:"email"`
	Scope string `json:"scope"`
}

func (c Claims) UserID() (uuid.UUID, error) {
	return uuid.Parse(c.Subject)
}

type TokenIssuer struct {
	secret []byte
}

func NewTokenIssuer(secret string) *TokenIssuer {
	return &TokenIssuer{secret: []byte(secret)}
}

// Issue signs a token whose lifetime and scope are chosen per call, so sign-up
// and sign-in can mint short-lived verify tokens and full session tokens from
// the same issuer.
func (t *TokenIssuer) Issue(userID uuid.UUID, email, role, scope string, ttl time.Duration) (string, time.Time, error) {
	expires := time.Now().Add(ttl)
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			Issuer:    "dating-coach-api",
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(expires),
		},
		Role:  role,
		Email: email,
		Scope: scope,
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign token: %w", err)
	}
	return signed, expires, nil
}

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
