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

// NewRateLimiter returns a limiter allowing limit requests per window per key.
func NewRateLimiter(rdb *redis.Client, limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{rdb: rdb, limit: limit, window: window}
}

// Allow counts one request against key and reports whether it stays within the
// current window's budget.
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

type clientIPKey struct{}

// ClientIPMiddleware selects the client address from the trusted end of the
// proxy chain. Zero hops ignores forwarded headers and uses the socket peer.
func ClientIPMiddleware(trustedHops int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := forwardedClientIP(r, trustedHops)
			r = r.WithContext(context.WithValue(r.Context(), clientIPKey{}, ip))
			r.RemoteAddr = ip
			next.ServeHTTP(w, r)
		})
	}
}

// ClientIP is the rate-limit key selected by ClientIPMiddleware. Without the
// middleware, it trusts only the rightmost forwarded address.
func ClientIP(r *http.Request) string {
	if ip, ok := r.Context().Value(clientIPKey{}).(string); ok {
		return ip
	}
	return forwardedClientIP(r, 1)
}

func forwardedClientIP(r *http.Request, trustedHops int) string {
	if trustedHops > 0 {
		var chain []string
		for _, value := range r.Header.Values("X-Forwarded-For") {
			chain = append(chain, strings.Split(value, ",")...)
		}
		if len(chain) >= trustedHops {
			if ip := net.ParseIP(strings.TrimSpace(chain[len(chain)-trustedHops])); ip != nil {
				return ip.String()
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
