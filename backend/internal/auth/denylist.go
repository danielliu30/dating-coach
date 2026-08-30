package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Revocations reports whether an account's outstanding tokens must be rejected.
// Middleware depends on the interface so tests can supply a fake.
type Revocations interface {
	// IsRevoked reports whether userID has been revoked. An error means the
	// answer is unknown and callers must not treat the token as valid.
	IsRevoked(ctx context.Context, userID uuid.UUID) (bool, error)
}

// Denylist is the Redis-backed set of accounts whose tokens are no longer
// accepted. Entries are written when an account is deleted and expire after the
// token lifetime, by which point every token naming the account has expired too.
type Denylist struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewDenylist returns a denylist whose entries live for ttl, which must be at
// least the JWT lifetime or tokens could outlive their revocation.
func NewDenylist(rdb *redis.Client, ttl time.Duration) *Denylist {
	return &Denylist{rdb: rdb, ttl: ttl}
}

// Revoke denies every token issued for userID for the configured ttl. It is
// idempotent and refreshes the expiry when called repeatedly.
func (d *Denylist) Revoke(ctx context.Context, userID uuid.UUID) error {
	if err := d.rdb.Set(ctx, denylistKey(userID), "1", d.ttl).Err(); err != nil {
		return fmt.Errorf("denylist account %s: %w", userID, err)
	}
	return nil
}

// Restore removes userID from the denylist, undoing a Revoke whose caller could
// not complete the deletion it was part of. Absent entries are not an error.
func (d *Denylist) Restore(ctx context.Context, userID uuid.UUID) error {
	if err := d.rdb.Del(ctx, denylistKey(userID)).Err(); err != nil {
		return fmt.Errorf("undo denylist for account %s: %w", userID, err)
	}
	return nil
}

// IsRevoked reports whether userID is on the denylist. A Redis failure returns
// an error rather than false, so callers fail closed.
func (d *Denylist) IsRevoked(ctx context.Context, userID uuid.UUID) (bool, error) {
	err := d.rdb.Get(ctx, denylistKey(userID)).Err()
	switch {
	case errors.Is(err, redis.Nil):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("read denylist for account %s: %w", userID, err)
	default:
		return true, nil
	}
}

// denylistKey namespaces the per-account denylist entry.
func denylistKey(userID uuid.UUID) string {
	return "auth:denylist:" + userID.String()
}
