package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/danielliu30/dating-coach/backend/internal/httpx"
)

type contextKey string

const principalKey contextKey = "auth.principal"

// Principal is the authenticated caller. Middleware puts one on the request
// context and handlers read it back with PrincipalFrom.
type Principal struct {
	UserID uuid.UUID
	Email  string
	Role   string
	Scope  string
}

// IsCoach reports whether the caller may use the coach-only endpoints.
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
			scope := claims.Scope
			if scope == "" {
				scope = ScopeSession // Tokens issued before scopes existed.
			}
			principal := Principal{UserID: userID, Email: claims.Email, Role: claims.Role, Scope: scope}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	}
}

// RequireScope rejects callers whose token was not issued with scope, using only
// the claim Middleware already parsed. Mount it inside Middleware.
func RequireScope(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFrom(r.Context())
			if !ok || principal.Scope != scope {
				httpx.Error(w, http.StatusForbidden, scope+" scope required")
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

// WithPrincipal stores the caller on a context; tests use it to fake auth.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

// PrincipalFrom returns the caller placed on the context by Middleware. The
// boolean is false on unauthenticated requests.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey).(Principal)
	return p, ok
}
