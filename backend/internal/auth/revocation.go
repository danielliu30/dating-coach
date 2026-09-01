package auth

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/danielliu30/dating-coach/backend/internal/store/db"
)

// revokedChannel carries freshly revoked account IDs to every replica, so
// connections already authenticated elsewhere can be dropped at once rather
// than at their next durable re-check.
const revokedChannel = "auth:revoked"

// Revocations reports whether an account's outstanding tokens must be refused.
// Long-lived connections depend on the interface rather than the concrete
// store so they can be exercised without a database.
type Revocations interface {
	Revoked(ctx context.Context, userID uuid.UUID) (bool, error)
}

// AccountStatus answers revocation questions from the accounts table, which is
// where a deletion is recorded before anything else happens. Access tokens are
// checked by signature alone and kept short-lived, so this is consulted only by
// connections that outlive a single request.
type AccountStatus struct {
	queries *db.Queries
}

// NewAccountStatus returns a Revocations backed by queries. It reads committed
// rows, so a revocation it reports cannot be undone by a cache restart.
func NewAccountStatus(queries *db.Queries) *AccountStatus {
	return &AccountStatus{queries: queries}
}

// Revoked reports whether userID no longer has an account a session may act
// for: the row is either marked deleted or already gone. A database failure is
// returned rather than treated as "not revoked", so callers can fail closed.
func (a *AccountStatus) Revoked(ctx context.Context, userID uuid.UUID) (bool, error) {
	active, err := a.queries.ActiveUserExists(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("check revocation for %s: %w", userID, err)
	}
	return !active, nil
}

// Announcer broadcasts revocations to every replica over Redis pub/sub.
type Announcer struct {
	rdb *redis.Client
}

// NewAnnouncer returns an announcer publishing on rdb.
func NewAnnouncer(rdb *redis.Client) *Announcer {
	return &Announcer{rdb: rdb}
}

// Announce tells the fleet that userID's sessions have ended, returning an
// error when Redis is unreachable. It is a latency optimisation and nothing
// more: the durable record is the account row, and a listener that misses the
// announcement still notices at its own next re-check, so callers treat a
// failure as recoverable.
func (a *Announcer) Announce(ctx context.Context, userID uuid.UUID) error {
	if err := a.rdb.Publish(ctx, revokedChannel, userID.String()).Err(); err != nil {
		return fmt.Errorf("announce revocation for %s: %w", userID, err)
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
