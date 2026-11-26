package auth

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/danielliu30/dating-coach/backend/internal/httpx"
)

// RateLimiter is a fixed-window limiter backed by Redis so limits are shared
// across API replicas. prefix namespaces the bucket ("ratelimit:auth" for
// per-IP auth limits, "ratelimit:user"/"ratelimit:analysis" for per-user ones).
type RateLimiter struct {
	rdb    *redis.Client
	prefix string
	limit  int
	window time.Duration
}

// NewRateLimiter returns a limiter allowing limit requests per window per key,
// in the per-IP auth namespace.
func NewRateLimiter(rdb *redis.Client, limit int, window time.Duration) *RateLimiter {
	return NewKeyedRateLimiter(rdb, "ratelimit:auth", limit, window)
}

// NewKeyedRateLimiter returns a limiter under a caller-chosen Redis prefix so
// different surfaces (per-IP auth, per-user API, cost-bounded analysis) keep
// separate budgets.
func NewKeyedRateLimiter(rdb *redis.Client, prefix string, limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{rdb: rdb, prefix: prefix, limit: limit, window: window}
}

// Allow counts one request against key and reports whether it stays within the
// current window's budget.
func (l *RateLimiter) Allow(ctx context.Context, key string) (bool, error) {
	redisKey := fmt.Sprintf("%s:%s:%d", l.prefix, key, time.Now().UnixNano()/int64(l.window))
	count, err := l.rdb.Incr(ctx, redisKey).Result()
	if err != nil {
		return false, fmt.Errorf("incr rate limit key: %w", err)
	}
	if count == 1 {
		if err := l.rdb.Expire(ctx, redisKey, l.window).Err(); err != nil {
			return false, fmt.Errorf("expire rate limit key: %w", err)
		}
	}
	return count <= int64(l.limit), nil
}

// Middleware limits requests per client IP. Redis outages fail open: auth stays
// available and the failure is logged.
func (l *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed, err := l.Allow(r.Context(), ClientIP(r))
		if err != nil {
			slog.Error("auth rate limiter unavailable", "error", err)
			next.ServeHTTP(w, r)
			return
		}
		if !allowed {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", int(l.window.Seconds())))
			l.tooMany(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// tooMany answers a rejected request.
func (l *RateLimiter) tooMany(w http.ResponseWriter) {
	httpx.Error(w, http.StatusTooManyRequests, "too many requests")
}

// UserMiddleware limits requests per authenticated principal. It runs after
// the auth middleware: requests without a principal pass through (the auth
// middleware rejects them itself), and like Middleware it fails open on Redis
// outages so a cache failure never takes the API down.
func (l *RateLimiter) UserMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFrom(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		allowed, err := l.Allow(r.Context(), "user:"+principal.UserID.String())
		if err != nil {
			slog.Error("user rate limiter unavailable", "error", err)
			next.ServeHTTP(w, r)
			return
		}
		if !allowed {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", int(l.window.Seconds())))
			l.tooMany(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ClientIP reads the peer resolved by TrustedProxyMiddleware.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
