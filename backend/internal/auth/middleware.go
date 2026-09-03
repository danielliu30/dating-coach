package auth

import (
	"context"
	"net/http"
	"strings"
	"time"

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
	// ExpiresAt is when the token this caller presented stops being valid. It
	// is zero for a token that carries no expiry, which HTTP handlers can
	// ignore, since every request is authenticated again; long-lived
	// connections cannot, and read it to stop outliving their own token.
	ExpiresAt time.Time
}

// IsCoach reports whether the caller may use the coach-only endpoints.
func (p Principal) IsCoach() bool { return p.Role == RoleCoach || p.Role == RoleAdmin }

// bearerToken returns the request's JWT from the Authorization header, falling
// back to the token query parameter that WebSocket clients must use because
// they cannot set headers. It returns "" when the request carries neither.
func bearerToken(r *http.Request) string {
	raw := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer"))
	if raw == "" {
		raw = r.URL.Query().Get("token")
	}
	return raw
}

// Middleware rejects requests without a valid bearer token.
func Middleware(issuer *TokenIssuer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := bearerToken(r)
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
			if claims.ExpiresAt != nil {
				principal.ExpiresAt = claims.ExpiresAt.Time
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	}
}

// RequireScope rejects callers whose token was not issued for the given scope.
// It must be mounted after Middleware, which supplies the Principal it reads.
// The verify-scoped token handed out at sign-up is thereby kept off the private
// API while it is still cryptographically valid, and answers 403.
//
// Tokens minted before scopes existed carry no scope at all. Those are not
// grandfathered in: they answer 401 so clients drop the session and sign in
// again, rather than 403, which would leave them signed in holding a token that
// can never satisfy any route.
func RequireScope(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFrom(r.Context())
			if !ok {
				httpx.Error(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			switch {
			case principal.Scope == "":
				httpx.Error(w, http.StatusUnauthorized, "token predates scopes; sign in again")
				return
			case principal.Scope != scope:
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
