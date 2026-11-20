package auth

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// TrustedProxyMiddleware resolves RemoteAddr once, before logging and limiting.
// A positive hop count selects that address from the right instead of walking
// the CIDR chain. Forwarded headers are only accepted from a trusted peer.
func TrustedProxyMiddleware(prefixes []netip.Prefix, trustedHops ...int) func(http.Handler) http.Handler {
	hops := 0
	if len(trustedHops) > 0 {
		hops = trustedHops[0]
	}
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
				if hops > 0 {
					if len(chain) < hops {
						next.ServeHTTP(w, r)
						return
					}
					chain = chain[len(chain)-hops : len(chain)-hops+1]
				}
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
			} else if real := r.Header.Get("X-Real-IP"); hops == 0 && real != "" {
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
