// Package middleware holds the HTTP middleware of the API: client address detection, throttling, security headers,
// request IDs, logging with secret redaction and panic recovery.
package middleware

import (
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
)

// ProxyTrust works out the address of the client of a request. X-Forwarded-For is believed only when the request came
// from one of the trusted proxies; for everybody else it is ignored, so a client cannot choose the address it is seen
// as. Getting this wrong makes rate limits meaningless: either everybody behind one proxy shares one address, or an
// attacker picks a fresh one for every request.
type ProxyTrust struct {
	prefixes []netip.Prefix
	log      *slog.Logger
	warned   atomic.Bool
}

// NewProxyTrust returns a ProxyTrust for the given proxy ranges. With none, X-Forwarded-For is never believed.
func NewProxyTrust(prefixes []netip.Prefix, log *slog.Logger) *ProxyTrust {
	return &ProxyTrust{prefixes: prefixes, log: log}
}

func (t *ProxyTrust) trusted(a netip.Addr) bool {
	for _, p := range t.prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ClientIP returns the client address of r as text.
func (t *ProxyTrust) ClientIP(r *http.Request) string {
	remote, ok := parseHop(r.RemoteAddr)
	if !ok {
		// Not possible for a request that arrived over a real connection; keep the raw value rather than guess.
		return r.RemoteAddr
	}

	forwarded := r.Header.Values("X-Forwarded-For")
	if len(forwarded) == 0 {
		return remote.String()
	}

	if !t.trusted(remote) {
		if len(t.prefixes) == 0 && t.warned.CompareAndSwap(false, true) && t.log != nil {
			t.log.Warn("X-Forwarded-For is ignored because no trusted proxies are configured; " +
				"if Raptor runs behind a reverse proxy, set RAPTOR_TRUSTED_PROXIES to its address")
		}
		return remote.String()
	}

	// The request came through a trusted proxy. Walk the chain from the nearest hop outwards; the first address that is
	// not itself a trusted proxy is the client.
	hops := strings.Split(strings.Join(forwarded, ","), ",")
	var leftmost netip.Addr
	for i := len(hops) - 1; i >= 0; i-- {
		addr, ok := parseHop(hops[i])
		if !ok {
			// A malformed entry from a trusted proxy: do not guess, the proxy's own address is the safest answer.
			return remote.String()
		}
		if !t.trusted(addr) {
			return addr.String()
		}
		leftmost = addr
	}
	return leftmost.String()
}

// parseHop reads an address from "ip", "ip:port", "[ip]" or "[ip]:port".
func parseHop(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap(), true
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	s = strings.Trim(s, "[]")
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}
