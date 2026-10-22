// Package lockout holds what the counters that limit guessing have in common: how a client is named in a counter key, and the
// description of a counter.
package lockout

import (
	"net/netip"
	"time"
)

// Counter is one counter that limits failures: at most Limit failures per Window for Key.
type Counter struct {
	Key    string
	Limit  int
	Window time.Duration
}

// Client names a client in a counter key: its address without the port, and for IPv6 only the /64 network, since one subscriber
// usually holds a whole /64 and could otherwise change address at will. An address that cannot be read is named fallback.
func Client(addr, fallback string) string {
	a, ok := parse(addr)
	if !ok {
		return fallback
	}

	a = a.Unmap()
	if a.Is6() {
		p, _ := a.Prefix(64)
		return p.String()
	}
	return a.String()
}

func parse(addr string) (netip.Addr, bool) {
	if ap, err := netip.ParseAddrPort(addr); err == nil {
		return ap.Addr(), true
	}
	a, err := netip.ParseAddr(addr)
	return a, err == nil
}
