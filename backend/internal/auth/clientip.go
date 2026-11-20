package auth

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// TrustedProxyMiddleware resolves RemoteAddr once, before logging and limiting.
func TrustedProxyMiddleware(prefixes []netip.Prefix) func(http.Handler) http.Handler {
	trusted := func(ip netip.Addr) bool {
		for _, prefix := range prefixes {
			if prefix.Contains(ip.Unmap()) {
				return true
			}
		}
		return false
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			peer := ClientIP(r)
			ip, err := netip.ParseAddr(peer)
			if err != nil || !trusted(ip) {
				next.ServeHTTP(w, r)
				return
			}
			candidate := ip
			if forwarded := strings.Join(r.Header.Values("X-Forwarded-For"), ","); forwarded != "" {
				chain := strings.Split(forwarded, ",")
				for i := len(chain) - 1; i >= 0; i-- {
					hop, err := netip.ParseAddr(strings.TrimSpace(chain[i]))
					if err != nil {
						next.ServeHTTP(w, r)
						return
					}
					candidate = hop.Unmap()
					if !trusted(candidate) {
						break
					}
				}
			} else if real := r.Header.Get("X-Real-IP"); real != "" {
				candidate, err = netip.ParseAddr(strings.TrimSpace(real))
				if err != nil {
					next.ServeHTTP(w, r)
					return
				}
			}
			r.RemoteAddr = net.JoinHostPort(candidate.Unmap().String(), "0")
			next.ServeHTTP(w, r)
		})
	}
}
