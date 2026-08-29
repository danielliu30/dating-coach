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

type Claims struct {
	jwt.RegisteredClaims
	Role  string `json:"role"`
	Email string `json:"email"`
}

func (c Claims) UserID() (uuid.UUID, error) {
	return uuid.Parse(c.Subject)
}

type TokenIssuer struct {
	secret []byte
	ttl    time.Duration
}

func NewTokenIssuer(secret string, ttl time.Duration) *TokenIssuer {
	return &TokenIssuer{secret: []byte(secret), ttl: ttl}
}

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
