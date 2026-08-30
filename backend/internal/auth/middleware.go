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
	Scope  string
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
			principal := Principal{UserID: userID, Email: claims.Email, Role: claims.Role, Scope: claims.Scope}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	}
}

// VerifiedLookup reports whether the account behind userID has confirmed its
// email address. Implementations return pgx.ErrNoRows when no such account
// exists; any other error means the flag could not be determined.
type VerifiedLookup func(ctx context.Context, userID uuid.UUID) (bool, error)

// EmailVerifiedLookup returns a VerifiedLookup backed by the users table. It
// costs one primary-key read per call, and propagates pgx.ErrNoRows for users
// that no longer exist.
func EmailVerifiedLookup(queries *db.Queries) VerifiedLookup {
	return func(ctx context.Context, userID uuid.UUID) (bool, error) {
		user, err := queries.GetUserByID(ctx, userID)
		if err != nil {
			return false, err
		}
		return user.EmailVerified, nil
	}
}

// RequireVerified rejects callers whose email address is still unconfirmed. It
// must be mounted after Middleware, which supplies the Principal it reads.
//
// The flag comes from lookup on every request rather than from the token, so
// verifying an address (or deleting an account) takes effect immediately on
// tokens already out in the wild, at the cost of one lookup per request.
//
// Callers are answered 403 when unverified, 401 when the principal is missing
// or the account is gone, and 500 when the lookup itself fails: a failing
// lookup denies the request rather than failing open.
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

// RequireScope rejects callers whose token was not issued for the given scope.
// It must be mounted after Middleware, which supplies the Principal it reads.
// A verify-scoped token (issued at sign-up) is thereby kept off the private API
// even while it is still cryptographically valid.
func RequireScope(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFrom(r.Context())
			if !ok {
				httpx.Error(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			if principal.Scope != scope {
				httpx.Error(w, http.StatusForbidden, "token scope not permitted")
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
