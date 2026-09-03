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

// IncrementAttempts increments the failed attempt counter. The counter is
// pinned to the remaining lifetime of the code so both expire together; once
// the code is gone the counter is removed rather than left behind.
func (c *VerificationCache) IncrementAttempts(ctx context.Context, userID string) (int64, error) {
	key := attemptsKey(userID)
	pipe := c.rdb.Pipeline()
	incr := pipe.Incr(ctx, key)
	remaining := pipe.PTTL(ctx, codeKey(userID))
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	count := incr.Val()
	if ttl := remaining.Val(); ttl > 0 {
		if err := c.rdb.PExpire(ctx, key, ttl).Err(); err != nil {
			return count, err
		}
		return count, nil
	}
	if err := c.rdb.Del(ctx, key).Err(); err != nil {
		return count, err
	}
	return count, nil
}

// Invalidate removes the code and attempt counter.
func (c *VerificationCache) Invalidate(ctx context.Context, userID string) error {
	return c.rdb.Del(ctx, codeKey(userID), attemptsKey(userID)).Err()
}
