package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

var (
	ErrLoginThrottled           = errors.New("too many requests")
	ErrLoginThrottleUnavailable = errors.New("login throttle unavailable")
)

type LoginThrottle struct {
	rdb    *redis.Client
	limit  int
	window time.Duration
}

func NewLoginThrottle(rdb *redis.Client, limit int, window time.Duration) *LoginThrottle {
	return &LoginThrottle{rdb: rdb, limit: limit, window: window}
}

func loginFailureKey(email string) string {
	return fmt.Sprintf("auth:login-failures:%x", sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email)))))
}

var reserveLoginAttempt = redis.NewScript(`
local count = redis.call('SCARD', KEYS[1])
if count >= tonumber(ARGV[2]) then
  return 0
end
redis.call('SADD', KEYS[1], ARGV[3])
if count == 0 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return 1
`)

func (l *LoginThrottle) reserve(ctx context.Context, email string) (string, error) {
	id := uuid.NewString()
	allowed, err := reserveLoginAttempt.Run(context.WithoutCancel(ctx), l.rdb, []string{loginFailureKey(email)}, l.window.Milliseconds(), l.limit, id).Int64()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrLoginThrottleUnavailable, err)
	}
	if allowed == 0 {
		return "", ErrLoginThrottled
	}
	return id, nil
}

func (l *LoginThrottle) release(ctx context.Context, email, id string) error {
	if err := l.rdb.SRem(context.WithoutCancel(ctx), loginFailureKey(email), id).Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrLoginThrottleUnavailable, err)
	}
	return nil
}

func (l *LoginThrottle) clear(ctx context.Context, email string) error {
	if err := l.rdb.Del(context.WithoutCancel(ctx), loginFailureKey(email)).Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrLoginThrottleUnavailable, err)
	}
	return nil
}
