package auth

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestTrustedProxyClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("::1/128")}
	for _, tc := range []struct{ name, peer, xff, real, want string }{
		{"direct", "198.51.100.1:1234", "", "", "198.51.100.1"},
		{"spoofed headers", "198.51.100.1:1234", "203.0.113.1", "203.0.113.2", "198.51.100.1"},
		{"trusted gateway", "172.18.0.2:1234", "203.0.113.1", "203.0.113.2", "203.0.113.1"},
		{"host nginx", "127.0.0.1:1234", "", "203.0.113.2", "203.0.113.2"},
		{"multihop", "172.18.0.2:1234", "192.0.2.1, 203.0.113.1, 172.18.0.3, 127.0.0.1", "", "203.0.113.1"},
		{"untrusted intermediate", "172.18.0.2:1234", "192.0.2.1, 203.0.113.1", "", "203.0.113.1"},
		{"ipv6", "[::1]:1234", "2001:db8::1", "", "2001:db8::1"},
		{"mapped peer", "[::ffff:172.18.0.2]:1234", "::ffff:203.0.113.1", "", "203.0.113.1"},
		{"invalid xff", "172.18.0.2:1234", "203.0.113.1, invalid", "203.0.113.2", "172.18.0.2"},
		{"invalid real ip", "127.0.0.1:1234", "", "invalid", "127.0.0.1"},
		{"no forwarding headers", "172.18.0.2:1234", "", "", "172.18.0.2"},
		{"all trusted", "172.18.0.2:1234", "127.0.0.1, 172.18.0.3", "", "127.0.0.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.peer
			req.Header.Set("X-Forwarded-For", tc.xff)
			req.Header.Set("X-Real-IP", tc.real)
			TrustedProxyMiddleware(trusted)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				if got := ClientIP(r); got != tc.want {
					t.Errorf("ClientIP=%q, want %q", got, tc.want)
				}
			})).ServeHTTP(httptest.NewRecorder(), req)
		})
	}
}

func TestNoTrustedProxies(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.1")
	TrustedProxyMiddleware(nil)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if got := ClientIP(r); got != "127.0.0.1" {
			t.Fatal(got)
		}
	})).ServeHTTP(httptest.NewRecorder(), req)
}

func TestRateLimiterUsesResolvedClientIP(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		t.Run(map[bool]string{false: "untrusted", true: "trusted"}[trusted], func(t *testing.T) {
			cache, _ := newTestCache(t)
			var prefixes []netip.Prefix
			if trusted {
				prefixes = []netip.Prefix{netip.MustParsePrefix("172.18.0.0/16")}
			}
			handler := TrustedProxyMiddleware(prefixes)(NewRateLimiter(cache.rdb, 1, time.Minute).Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})))
			for i, forwarded := range []string{"198.51.100.1", "198.51.100.2"} {
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.RemoteAddr = "172.18.0.2:1234"
				req.Header.Set("X-Forwarded-For", forwarded)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				want := http.StatusNoContent
				if !trusted && i == 1 {
					want = http.StatusTooManyRequests
				}
				if rec.Code != want {
					t.Fatalf("status=%d, want %d", rec.Code, want)
				}
			}
		})
	}
}
