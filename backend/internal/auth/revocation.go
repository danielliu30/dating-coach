package auth

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

const (
	// revokedKeyPrefix namespaces the per-account revocation keys in Redis.
	revokedKeyPrefix = "auth:revoked:"
	// revokedChannel carries freshly revoked account IDs to every replica, so
	// connections already authenticated elsewhere can be dropped at once rather
	// than at their next durable re-check.
	revokedChannel       = "auth:revoked"
	revocationsReadyKey  = "auth:revocations:restored"
	revocationsReplayKey = "auth:revocations:replaying"
)

// revocationGrace pads an entry beyond the token lifetime it is given, covering
// a sign-in that read the account row just before deletion marked it and minted
// its token just after the revocation was written.
const revocationGrace = 5 * time.Minute

// Revocations reports since when an account's tokens must be refused: every
// token issued at or before the returned time is revoked, and a zero time means
// none are.
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

// NewDenylist takes the longest access or refresh token lifetime a cutoff must outlive.
func NewDenylist(rdb *redis.Client, ttl time.Duration) *Denylist {
	return &Denylist{rdb: rdb, ttl: ttl + revocationGrace}
}

// Revoke marks every token userID will ever hold as unusable, including one
// minted after this call by a sign-in that read the account just before it
// was marked deleted. Account deletion calls it on a best-effort basis; the
// worker revokes before removing rows.
//
// It also announces the revocation on revokedChannel. That publish is best
// effort: listeners that miss it pick up the cutoff on their periodic re-check.
func (d *Denylist) Revoke(ctx context.Context, userID uuid.UUID) error {
	return d.write(ctx, userID, revokedAll, true)
}

// RevokeSessionsAt revokes tokens issued at or before at without weakening an account-wide Revoke.
func (d *Denylist) RevokeSessionsAt(ctx context.Context, userID uuid.UUID, at time.Time) error {
	return d.write(ctx, userID, strconv.FormatInt(at.UnixNano(), 10), true)
}

// SessionRevocationLister reads the durable cutoffs recorded in Postgres.
type SessionRevocationLister interface {
	ListSessionRevocationsSince(ctx context.Context, since time.Time) ([]db.ListSessionRevocationsSinceRow, error)
}

// ReplayRevocations restores recent durable cutoffs without announcements:
// announcing would also drop sockets opened by a sign-in after the cutoff.
// Older sockets close on their heartbeat check instead.
func (d *Denylist) ReplayRevocations(ctx context.Context, lister SessionRevocationLister) error {
	// A flush during replay must not let a partially restored cache become ready.
	replayID := uuid.NewString()
	if err := d.rdb.Set(ctx, revocationsReplayKey, replayID, 0).Err(); err != nil {
		return fmt.Errorf("start revocation replay: %w", err)
	}
	rows, err := lister.ListSessionRevocationsSince(ctx, time.Now().Add(-d.ttl))
	if err != nil {
		return fmt.Errorf("list session revocations: %w", err)
	}
	for _, row := range rows {
		if row.SessionsRevokedAt == nil {
			continue
		}
		cutoff := strconv.FormatInt(row.SessionsRevokedAt.UnixNano(), 10)
		if err := d.write(ctx, row.ID, cutoff, false); err != nil {
			return err
		}
	}
	ready, err := finishRevocationReplay.Run(ctx, d.rdb, []string{revocationsReplayKey, revocationsReadyKey}, replayID).Int()
	if err != nil {
		return fmt.Errorf("finish revocation replay: %w", err)
	}
	if ready == 0 {
		return ErrRevocationUnavailable
	}
	return nil
}

var finishRevocationReplay = redis.NewScript(`
if redis.call("GET", KEYS[1]) ~= ARGV[1] then return 0 end
redis.call("SET", KEYS[2], "1")
return 1
`)

const RevocationReplayPeriod = 15 * time.Second

// WatchDurableRevocations runs ReplayRevocations every period until ctx is
// done, returning the first failure so the caller can back off and retry.
func (d *Denylist) WatchDurableRevocations(ctx context.Context, lister SessionRevocationLister, period time.Duration) error {
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		if err := d.ReplayRevocations(ctx, lister); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// revokedAll is the entry Revoke writes: every token is refused.
const revokedAll = "all"

// writeRevocation sets the entry unless it already revokes at least as much:
// a sign-out cannot turn a deleted account's ban into a time-bounded one, and a
// delayed write cannot move a cutoff back. Cutoffs are equal-length decimal
// nanoseconds, so comparing them as strings orders them.
var writeRevocation = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if current == ARGV[3] then return 0 end
if current and ARGV[1] ~= ARGV[3] and #current == #ARGV[1] and current >= ARGV[1] then return 0 end
redis.call("SET", KEYS[1], ARGV[1], "PX", ARGV[2])
return 1
`)

func (d *Denylist) write(ctx context.Context, userID uuid.UUID, value string, announce bool) error {
	key := revokedKeyPrefix + userID.String()
	changed, err := writeRevocation.Run(ctx, d.rdb, []string{key}, value, d.ttl.Milliseconds(), revokedAll).Int()
	if err != nil {
		return fmt.Errorf("revoke sessions for %s: %w", userID, err)
	}
	if changed == 0 || !announce {
		return nil
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
	values, err := d.rdb.MGet(ctx, revocationsReadyKey, revokedKeyPrefix+userID.String()).Result()
	if err != nil {
		return time.Time{}, fmt.Errorf("check revocation for %s: %w", userID, err)
	}
	if values[0] != "1" {
		return time.Time{}, ErrRevocationUnavailable
	}
	if values[1] == nil {
		return time.Time{}, nil
	}
	val, ok := values[1].(string)
	if !ok {
		return revokeEverything, nil
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
