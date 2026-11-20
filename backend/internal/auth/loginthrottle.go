package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

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
local count = redis.call('INCR', KEYS[1])
if count == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return count
`)

func (l *LoginThrottle) reserve(ctx context.Context, email string) error {
	count, err := reserveLoginAttempt.Run(context.WithoutCancel(ctx), l.rdb, []string{loginFailureKey(email)}, l.window.Milliseconds()).Int64()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrLoginThrottleUnavailable, err)
	}
	if count > int64(l.limit) {
		return ErrLoginThrottled
	}
	return nil
}

func (l *LoginThrottle) clear(ctx context.Context, email string) error {
	if err := l.rdb.Del(context.WithoutCancel(ctx), loginFailureKey(email)).Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrLoginThrottleUnavailable, err)
	}
	return nil
}
