package auth

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/danielliu30/dating-coach/backend/internal/httpx"
)

// RateLimiter is a fixed-window per-IP limiter backed by Redis so the limit is
// shared across API replicas.
type RateLimiter struct {
	rdb    *redis.Client
	limit  int
	window time.Duration
}

func NewRateLimiter(rdb *redis.Client, limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{rdb: rdb, limit: limit, window: window}
}

func (l *RateLimiter) Allow(ctx context.Context, key string) (bool, error) {
	redisKey := fmt.Sprintf("ratelimit:auth:%s:%d", key, time.Now().UnixNano()/int64(l.window))
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
			httpx.Error(w, http.StatusTooManyRequests, "too many requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func ClientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	if realIP := r.Header.Get("X-Real-IP"); realIP != "" {
		return strings.TrimSpace(realIP)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
