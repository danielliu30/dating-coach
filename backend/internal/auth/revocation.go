package auth

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/danielliu30/dating-coach/backend/internal/httpx"
)

// revokedKeyPrefix namespaces the per-account revocation keys in Redis.
const revokedKeyPrefix = "auth:revoked:"

// revocationGrace pads an entry beyond the token lifetime it is given, covering
// a sign-in that read the account row just before deletion marked it and minted
// its token just after the revocation was written.
const revocationGrace = 5 * time.Minute

// Revocations reports whether an account's outstanding tokens must be refused.
// Middleware depends on the interface rather than Denylist so it can be
// exercised without a live Redis.
type Revocations interface {
	Revoked(ctx context.Context, userID uuid.UUID) (bool, error)
}

// Denylist is the Redis-backed record of accounts whose tokens were revoked
// before their own expiry, which is what deleting an account does: JWTs are
// self-contained, so nothing else stops an already-issued token.
type Denylist struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewDenylist returns a denylist whose entries live for ttl plus a short grace
// period. Pass the session token lifetime: deletion marks the account row
// before it revokes, so no later token exists and the entry can no longer make
// a difference once the tokens that predate it have expired.
func NewDenylist(rdb *redis.Client, ttl time.Duration) *Denylist {
	return &Denylist{rdb: rdb, ttl: ttl + revocationGrace}
}

// Revoke marks every outstanding token for userID as unusable. It returns an
// error when Redis is unreachable. Account deletion calls it on a best-effort
// basis and only logs a failure: once the deletion is recorded, the worker
// revokes before it removes any rows, so this call only shortens the window in
// which a still-valid token works from the job's latency to milliseconds.
func (d *Denylist) Revoke(ctx context.Context, userID uuid.UUID) error {
	if err := d.rdb.Set(ctx, revokedKeyPrefix+userID.String(), "1", d.ttl).Err(); err != nil {
		return fmt.Errorf("revoke sessions for %s: %w", userID, err)
	}
	return nil
}

// Revoked reports whether userID is on the denylist. A Redis failure is
// returned rather than treated as "not revoked", so callers can fail closed.
func (d *Denylist) Revoked(ctx context.Context, userID uuid.UUID) (bool, error) {
	n, err := d.rdb.Exists(ctx, revokedKeyPrefix+userID.String()).Result()
	if err != nil {
		return false, fmt.Errorf("check revocation for %s: %w", userID, err)
	}
	return n > 0, nil
}

// RequireActive rejects callers whose account has been revoked, which a valid
// signature alone cannot detect. It must be mounted after Middleware, which
// supplies the Principal it reads.
//
// It fails closed: when Redis cannot be reached the request is answered 503
// instead of being allowed through, because "unknown" here means "possibly a
// deleted account".
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
