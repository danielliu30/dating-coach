package auth

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/danielliu30/dating-coach/backend/internal/httpx"
)

const (
	// revokedKeyPrefix namespaces the per-account revocation keys in Redis.
	revokedKeyPrefix = "auth:revoked:"
	// revokedChannel carries freshly revoked account IDs to every replica, so
	// connections already authenticated elsewhere can be dropped at once rather
	// than at their next durable re-check.
	revokedChannel = "auth:revoked"
)

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
//
// It also announces the revocation on revokedChannel. That publish is best
// effort and its failure is logged, not returned: the durable key above is what
// makes the revocation true, and listeners that miss the announcement still
// pick it up from their own periodic re-check.
func (d *Denylist) Revoke(ctx context.Context, userID uuid.UUID) error {
	if err := d.rdb.Set(ctx, revokedKeyPrefix+userID.String(), "1", d.ttl).Err(); err != nil {
		return fmt.Errorf("revoke sessions for %s: %w", userID, err)
	}
	if err := d.rdb.Publish(ctx, revokedChannel, userID.String()).Err(); err != nil {
		slog.Error("announce revocation", "error", err, "user_id", userID)
	}
	return nil
}

// WatchRevocations calls onRevoked with every account revoked anywhere in the
// fleet, until ctx is done. It blocks, so callers run it in its own goroutine.
//
// Delivery is not guaranteed: Redis pub/sub drops messages published while this
// replica is disconnected, and a reconnect does not replay them. Callers must
// therefore treat it as a latency optimisation over their own durable check,
// never as the only thing standing between a revoked account and its
// still-open connections.
func WatchRevocations(ctx context.Context, rdb *redis.Client, onRevoked func(uuid.UUID)) {
	pubsub := rdb.Subscribe(ctx, revokedChannel)
	defer func() {
		if err := pubsub.Close(); err != nil && ctx.Err() == nil {
			slog.Error("close revocation subscription", "error", err)
		}
	}()

	for {
		msg, err := pubsub.ReceiveMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Error("receive revocation", "error", err)
			time.Sleep(time.Second)
			continue
		}
		userID, err := uuid.Parse(msg.Payload)
		if err != nil {
			slog.Error("decode revocation", "error", err, "payload", msg.Payload)
			continue
		}
		slog.Info("revocation announced", "user_id", userID)
		onRevoked(userID)
	}
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
