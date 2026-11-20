package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestClientIP(t *testing.T) {
	for _, tt := range []struct {
		name      string
		forwarded []string
		hops      int
		want      string
	}{
		{"nginx", []string{"198.51.100.7"}, 1, "198.51.100.7"},
		{"spoofed prefix", []string{"192.0.2.1, 198.51.100.7"}, 1, "198.51.100.7"},
		{"multiple headers", []string{"192.0.2.1", "198.51.100.7"}, 1, "198.51.100.7"},
		{"two trusted hops", []string{"192.0.2.1, 198.51.100.7, 10.0.0.1"}, 2, "198.51.100.7"},
		{"ipv6", []string{"192.0.2.1, 2001:db8::7 "}, 1, "2001:db8::7"},
		{"no header", nil, 1, "203.0.113.4"},
		{"untrusted proxy", []string{"198.51.100.7"}, 0, "203.0.113.4"},
		{"short chain", []string{"192.0.2.1"}, 2, "203.0.113.4"},
		{"malformed selected address", []string{"192.0.2.1, invalid"}, 1, "203.0.113.4"},
		{"empty selected address", []string{"192.0.2.1,"}, 1, "203.0.113.4"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = "203.0.113.4:1234"
			r.Header["X-Forwarded-For"] = tt.forwarded
			r.Header.Set("X-Real-IP", "192.0.2.99")
			h := ClientIPMiddleware(tt.hops)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := ClientIP(r); got != tt.want {
					t.Fatalf("ClientIP = %q, want %q", got, tt.want)
				}
				if r.RemoteAddr != tt.want {
					t.Fatalf("RemoteAddr = %q, want %q", r.RemoteAddr, tt.want)
				}
			}))
			h.ServeHTTP(httptest.NewRecorder(), r)
		})
	}
}

func TestClientIPWithoutMiddleware(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-For", "192.0.2.1, 198.51.100.7")
	if got := ClientIP(r); got != "198.51.100.7" {
		t.Fatal(got)
	}
	r.Header.Del("X-Forwarded-For")
	r.Header.Set("X-Real-IP", "192.0.2.1")
	for _, peer := range []string{"203.0.113.4:1234", "203.0.113.4", "[2001:db8::7]:1234"} {
		r.RemoteAddr = peer
		want := "203.0.113.4"
		if peer == "[2001:db8::7]:1234" {
			want = "2001:db8::7"
		}
		if got := ClientIP(r); got != want {
			t.Fatalf("ClientIP(%q) = %q, want %q", peer, got, want)
		}
	}
}

func TestRateLimiterRejectsRotatingForwardedPrefix(t *testing.T) {
	cache := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: cache.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	limiter := NewRateLimiter(rdb, 2, time.Hour)
	h := ClientIPMiddleware(1)(limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	for i, want := range []int{http.StatusNoContent, http.StatusNoContent, http.StatusTooManyRequests} {
		r := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
		r.Header.Set("X-Forwarded-For", fmt.Sprintf("192.0.2.%d, 198.51.100.7", i+1))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("request %d: status = %d, want %d", i, w.Code, want)
		}
	}
}
