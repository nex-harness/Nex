package httpapi

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
)

// trustedProxies is the set of proxy networks whose X-Forwarded-For is believed.
// It is process-wide configuration set once at startup by SetTrustedProxies.
var trustedProxies atomic.Pointer[[]netip.Prefix]

// SetTrustedProxies configures the reverse proxies whose X-Forwarded-For header
// is trusted for the login rate limiter. Entries are IPs or CIDRs; invalid
// entries are returned so the caller can report them. An empty list keeps the
// default (TCP peer only).
func SetTrustedProxies(entries []string) (invalid []string) {
	prefixes := make([]netip.Prefix, 0, len(entries))
	for _, raw := range entries {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(entry); err == nil {
			prefixes = append(prefixes, prefix.Masked())
			continue
		}
		if addr, err := netip.ParseAddr(entry); err == nil {
			prefixes = append(prefixes, netip.PrefixFrom(addr.Unmap(), addr.Unmap().BitLen()))
			continue
		}
		invalid = append(invalid, entry)
	}
	trustedProxies.Store(&prefixes)
	return invalid
}

func isTrustedProxy(addr netip.Addr) bool {
	list := trustedProxies.Load()
	if list == nil {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range *list {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// limiterKey returns the address the login rate limiter keys on.
//
// The TCP peer is used unless it is a configured trusted proxy. In that case
// the X-Forwarded-For chain is walked from the right (the hop the trusted proxy
// appended) past any further trusted proxies, and the first untrusted address
// is the client. A client cannot spoof its key this way: everything left of the
// first untrusted hop is ignored, and an untrusted peer's header is never read.
func limiterKey(r *http.Request) string {
	peer := peerHost(r)
	addr, err := netip.ParseAddr(peer)
	if err != nil || !isTrustedProxy(addr) {
		return peer
	}
	hops := forwardedHops(r.Header.Values("X-Forwarded-For"))
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(hops[i])
		if err != nil {
			// A malformed hop means the chain cannot be trusted past this point.
			return peer
		}
		if !isTrustedProxy(hop) {
			return hop.Unmap().String()
		}
	}
	return peer
}

func peerHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		if r.RemoteAddr != "" {
			return r.RemoteAddr
		}
		return "unknown"
	}
	return host
}

func forwardedHops(values []string) []string {
	var hops []string
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if hop := strings.TrimSpace(part); hop != "" {
				hops = append(hops, hop)
			}
		}
	}
	return hops
}
