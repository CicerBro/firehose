package server

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ClientIP returns the client address per §5.7: RemoteAddr unless it is a
// trusted proxy, in which case X-Forwarded-For is walked right to left and the
// first untrusted entry wins (or the leftmost valid entry if all are trusted).
// IPv4-mapped IPv6 addresses are unmapped. Invalid entries are ignored.
func ClientIP(r *http.Request, trusted []netip.Prefix) netip.Addr {
	remote := parseIP(r.RemoteAddr)
	if !remote.IsValid() || !isTrusted(remote, trusted) {
		return remote
	}
	var chain []netip.Addr
	for _, h := range r.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(h, ",") {
			if a := parseIP(strings.TrimSpace(part)); a.IsValid() {
				chain = append(chain, a)
			}
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		if !isTrusted(chain[i], trusted) {
			return chain[i]
		}
	}
	if len(chain) > 0 {
		return chain[0]
	}
	return remote
}

// parseIP accepts "ip", "ip:port", "[ipv6]:port" and "[ipv6]"; zones are dropped.
func parseIP(s string) netip.Addr {
	if s == "" {
		return netip.Addr{}
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return a.WithZone("").Unmap()
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().WithZone("").Unmap()
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		if a, err := netip.ParseAddr(host); err == nil {
			return a.WithZone("").Unmap()
		}
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		if a, err := netip.ParseAddr(s[1 : len(s)-1]); err == nil {
			return a.WithZone("").Unmap()
		}
	}
	return netip.Addr{}
}

func isTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ipFamily returns "ipv4" or "ipv6".
func ipFamily(a netip.Addr) string {
	if a.Is4() {
		return "ipv4"
	}
	return "ipv6"
}
