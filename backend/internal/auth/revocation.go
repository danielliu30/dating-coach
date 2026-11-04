package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
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

// Revocations reports since when an account's tokens must be refused: every
// token issued at or before the returned time is revoked, and a zero time means
// none are. Middleware depends on the interface rather than Denylist so it can
// be exercised without a live Redis.
type Revocations interface {
	RevokedSince(ctx context.Context, userID uuid.UUID) (time.Time, error)
}

// Denylist is the Redis-backed record of accounts whose tokens were revoked
// before their own expiry, which is what deleting an account does: JWTs are
// self-contained, so nothing else stops an already-issued token.
type Denylist struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewDenylist returns a denylist whose entries live for ttl plus a short grace
// period. Pass the longest lifetime of any access token, session- or
// verify-scoped: deletion marks the account row
// before it revokes, so no later token exists and the entry can no longer make
// a difference once the tokens that predate it have expired.
func NewDenylist(rdb *redis.Client, ttl time.Duration) *Denylist {
	return &Denylist{rdb: rdb, ttl: ttl + revocationGrace}
}

// Revoke marks every token userID will ever hold as unusable, including one
// minted after this call by a sign-in that read the account just before it
// was marked deleted. Account deletion calls it. It returns an error when Redis
// is unreachable; deletion calls it on a best-effort basis and only logs a
// failure: once the deletion is recorded, the worker revokes before it removes
// any rows, so this call only shortens the window in which a still-valid token
// works from the job's latency to milliseconds.
//
// It also announces the revocation on revokedChannel. That publish is best
// effort and its failure is logged, not returned: the durable key above is what
// makes the revocation true, and listeners that miss the announcement still
// pick it up from their own periodic re-check.
func (d *Denylist) Revoke(ctx context.Context, userID uuid.UUID) error {
	return d.write(ctx, userID, revokedAll)
}

// RevokeSessions marks every token for userID issued up to now as unusable,
// while tokens minted afterwards (a fresh sign-in) keep working. Sign-out and
// refresh-token reuse call it. It never weakens an account-wide Revoke.
func (d *Denylist) RevokeSessions(ctx context.Context, userID uuid.UUID) error {
	return d.write(ctx, userID, strconv.FormatInt(time.Now().UnixNano(), 10))
}

// revokedAll is the entry Revoke writes: every token is refused.
const revokedAll = "all"

// writeRevocation sets the entry unless it already revokes everything, so a
// sign-out cannot turn a deleted account's ban into a time-bounded one.
var writeRevocation = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[3] then return 0 end
redis.call("SET", KEYS[1], ARGV[1], "PX", ARGV[2])
return 1
`)

func (d *Denylist) write(ctx context.Context, userID uuid.UUID, value string) error {
	key := revokedKeyPrefix + userID.String()
	if err := writeRevocation.Run(ctx, d.rdb, []string{key}, value, d.ttl.Milliseconds(), revokedAll).Err(); err != nil {
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

// RevokedSince returns when userID's tokens were last revoked, or the zero
// time if they never were. A Redis failure is returned rather than treated as
// "not revoked", so callers can fail closed. An entry that does not hold a
// timestamp (Revoke's, or one written before entries carried one) revokes
// every token.
func (d *Denylist) RevokedSince(ctx context.Context, userID uuid.UUID) (time.Time, error) {
	val, err := d.rdb.Get(ctx, revokedKeyPrefix+userID.String()).Result()
	if errors.Is(err, redis.Nil) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("check revocation for %s: %w", userID, err)
	}
	nanos, err := strconv.ParseInt(val, 10, 64)
	if err != nil || nanos < minRevocationNanos {
		return revokeEverything, nil
	}
	return time.Unix(0, nanos), nil
}

// minRevocationNanos (2001-09-09) separates real timestamps from the "1" that
// entries held before they carried one.
const minRevocationNanos = 1_000_000_000_000_000_000

// revokeEverything is a revocation time no token's issue time can reach.
var revokeEverything = time.Unix(1<<40, 0)
