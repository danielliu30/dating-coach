package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/danielliu30/dating-coach/backend/internal/httpx"
	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

type contextKey string

const principalKey contextKey = "auth.principal"

// Principal is the authenticated caller.
type Principal struct {
	UserID uuid.UUID
	Email  string
	Role   string
}

func (p Principal) IsCoach() bool { return p.Role == RoleCoach || p.Role == RoleAdmin }

// Middleware rejects requests without a valid bearer token.
func Middleware(issuer *TokenIssuer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer"))
			if raw == "" {
				raw = r.URL.Query().Get("token") // WebSocket clients cannot set headers.
			}
			if raw == "" {
				httpx.Error(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			claims, err := issuer.Parse(raw)
			if err != nil {
				httpx.Error(w, http.StatusUnauthorized, "invalid token")
				return
			}
			userID, err := claims.UserID()
			if err != nil {
				httpx.Error(w, http.StatusUnauthorized, "invalid token subject")
				return
			}
			principal := Principal{UserID: userID, Email: claims.Email, Role: claims.Role}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	}
}

// VerifiedLookup reports whether the account has confirmed its email address.
type VerifiedLookup func(ctx context.Context, userID uuid.UUID) (bool, error)

// EmailVerifiedLookup reads the verification flag straight from the users table.
func EmailVerifiedLookup(queries *db.Queries) VerifiedLookup {
	return func(ctx context.Context, userID uuid.UUID) (bool, error) {
		user, err := queries.GetUserByID(ctx, userID)
		if err != nil {
			return false, err
		}
		return user.EmailVerified, nil
	}
}

// RequireVerified rejects callers whose email address is still unconfirmed. The
// flag is read per request rather than taken from the token so that verifying
// (or an account being deleted) takes effect immediately on tokens already out
// in the wild.
func RequireVerified(lookup VerifiedLookup) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFrom(r.Context())
			if !ok {
				httpx.Error(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			verified, err := lookup(r.Context(), principal.UserID)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				httpx.Error(w, http.StatusUnauthorized, "account no longer exists")
				return
			case err != nil:
				slog.Error("email verification lookup", "error", err, "user_id", principal.UserID)
				httpx.Error(w, http.StatusInternalServerError, "could not verify account")
				return
			case !verified:
				httpx.Error(w, http.StatusForbidden, "email not verified")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireCoach rejects callers that are not coaches.
func RequireCoach(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFrom(r.Context())
		if !ok || !principal.IsCoach() {
			httpx.Error(w, http.StatusForbidden, "coach role required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey).(Principal)
	return p, ok
}
