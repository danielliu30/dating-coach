package auth

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	codeKeyPrefix     = "verify:code:%s"
	attemptsKeyPrefix = "verify:attempts:%s"
)

// VerificationCache stores short-lived email verification codes and their
// failed-attempt counters in Redis.
type VerificationCache struct {
	rdb *redis.Client
	ttl time.Duration
}

func NewVerificationCache(rdb *redis.Client, ttl time.Duration) *VerificationCache {
	return &VerificationCache{rdb: rdb, ttl: ttl}
}

func codeKey(userID string) string {
	return fmt.Sprintf(codeKeyPrefix, userID)
}

func attemptsKey(userID string) string {
	return fmt.Sprintf(attemptsKeyPrefix, userID)
}

// Store saves a verification code and resets the attempt counter.
func (c *VerificationCache) Store(ctx context.Context, userID, code string) error {
	pipe := c.rdb.Pipeline()
	pipe.Set(ctx, codeKey(userID), code, c.ttl)
	pipe.Set(ctx, attemptsKey(userID), 0, c.ttl)
	_, err := pipe.Exec(ctx)
	return err
}

// Get returns the stored code and current failed-attempt count. If either key
// is missing, the code is treated as expired/invalid.
func (c *VerificationCache) Get(ctx context.Context, userID string) (code string, attempts int64, err error) {
	pipe := c.rdb.Pipeline()
	codeCmd := pipe.Get(ctx, codeKey(userID))
	attemptsCmd := pipe.Get(ctx, attemptsKey(userID))
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return "", 0, err
	}

	code, _ = codeCmd.Result()
	attemptsStr, _ := attemptsCmd.Result()
	if attemptsStr != "" {
		if n, err := strconv.ParseInt(attemptsStr, 10, 64); err == nil {
			attempts = n
		}
	}
	return code, attempts, nil
}

// incrementAttemptsScript bumps the counter (KEYS[2]) and pins it to the code's
// (KEYS[1]) remaining lifetime in one atomic step, so a concurrent Store cannot
// slip in between and leave a fresh counter with a stale expiry. A missing code
// removes the counter and yields 0.
var incrementAttemptsScript = redis.NewScript(`
local ttl = redis.call('PTTL', KEYS[1])
if ttl <= 0 then
  redis.call('DEL', KEYS[2])
  return 0
end
local n = redis.call('INCR', KEYS[2])
redis.call('PEXPIRE', KEYS[2], ttl)
return n
`)

// IncrementAttempts increments the failed attempt counter. The counter is
// pinned to the remaining lifetime of the code so both expire together; once
// the code is gone the counter is removed and 0 is returned.
func (c *VerificationCache) IncrementAttempts(ctx context.Context, userID string) (int64, error) {
	return incrementAttemptsScript.Run(ctx, c.rdb, []string{codeKey(userID), attemptsKey(userID)}).Int64()
}

// Invalidate removes the code and attempt counter.
func (c *VerificationCache) Invalidate(ctx context.Context, userID string) error {
	return c.rdb.Del(ctx, codeKey(userID), attemptsKey(userID)).Err()
}
