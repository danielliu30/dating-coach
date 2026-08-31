package auth

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/danielliu30/dating-coach/backend/internal/httpx"
	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// Revocations reports whether an account's outstanding tokens must be refused.
// Middleware depends on the interface rather than Denylist so it can be
// exercised without a live database.
type Revocations interface {
	Revoked(ctx context.Context, userID uuid.UUID) (bool, error)
}

// RevocationStore is the subset of the generated queries a Denylist needs.
type RevocationStore interface {
	RevokeUserSessions(ctx context.Context, arg db.RevokeUserSessionsParams) error
	IsSessionRevoked(ctx context.Context, userID uuid.UUID) (bool, error)
}

// Denylist is the record of accounts whose tokens were revoked before their own
// expiry, which is what deleting an account does: JWTs are self-contained, so
// nothing else stops an already-issued token.
//
// It lives in PostgreSQL rather than in a cache: a revocation has to outlive
// every process that holds it, because a token it refuses stays validly signed
// until its own expiry, and the account's rows are only removed afterwards.
type Denylist struct {
	store RevocationStore
	ttl   time.Duration
}

// NewDenylist returns a denylist whose entries last for ttl. Pass the session
// token lifetime: once every token minted before the revocation has expired the
// entry can no longer make a difference, so it is purged.
func NewDenylist(store RevocationStore, ttl time.Duration) *Denylist {
	return &Denylist{store: store, ttl: ttl}
}

// Revoke marks every outstanding token for userID as unusable. It is
// synchronous on purpose: the caller (account deletion) must not report success
// until the sessions are actually dead, even though the rows are removed later.
// It returns an error when the write did not commit.
func (d *Denylist) Revoke(ctx context.Context, userID uuid.UUID) error {
	params := db.RevokeUserSessionsParams{UserID: userID, ExpiresAt: time.Now().Add(d.ttl)}
	if err := d.store.RevokeUserSessions(ctx, params); err != nil {
		return fmt.Errorf("revoke sessions for %s: %w", userID, err)
	}
	return nil
}

// Revoked reports whether userID is on the denylist. A query failure is
// returned rather than treated as "not revoked", so callers can fail closed.
func (d *Denylist) Revoked(ctx context.Context, userID uuid.UUID) (bool, error) {
	revoked, err := d.store.IsSessionRevoked(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("check revocation for %s: %w", userID, err)
	}
	return revoked, nil
}

// RequireActive rejects callers whose account has been revoked, which a valid
// signature alone cannot detect. It must be mounted after Middleware, which
// supplies the Principal it reads.
//
// It fails closed: when the revocation state cannot be read the request is
// answered 503 instead of being allowed through, because "unknown" here means
// "possibly a deleted account".
func RequireActive(revocations Revocations) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFrom(r.Context())
			if !ok {
				httpx.Error(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			revoked, err := revocations.Revoked(r.Context(), principal.UserID)
			if err != nil {
				httpx.Error(w, http.StatusServiceUnavailable, "could not verify session")
				return
			}
			if revoked {
				httpx.Error(w, http.StatusUnauthorized, "session revoked")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
