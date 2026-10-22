package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func req(remote string, xff ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	for _, v := range xff {
		r.Header.Add("X-Forwarded-For", v)
	}
	return r
}

func prefixes(t *testing.T, list ...string) []netip.Prefix {
	t.Helper()
	var out []netip.Prefix
	for _, s := range list {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

func TestClientIP(t *testing.T) {
	proxies := prefixes(t, "10.0.0.0/8", "192.0.2.1/32", "fd00::/8")

	tests := []struct {
		name     string
		trusted  []netip.Prefix
		request  *http.Request
		wantAddr string
	}{
		{"direct client, no header", proxies, req("198.51.100.7:4000"), "198.51.100.7"},
		{"direct client may not set the header", proxies, req("198.51.100.7:4000", "1.2.3.4"), "198.51.100.7"},
		{"trusted proxy forwards the client", proxies, req("10.0.0.5:4000", "198.51.100.7"), "198.51.100.7"},
		{"two trusted proxies in a chain", proxies, req("10.0.0.5:4000", "198.51.100.7, 10.0.0.9"), "198.51.100.7"},
		{"a spoofed leftmost entry is skipped", proxies, req("10.0.0.5:4000", "6.6.6.6, 198.51.100.7"), "198.51.100.7"},
		{"entries added by several header lines", proxies, req("10.0.0.5:4000", "6.6.6.6", "198.51.100.7"), "198.51.100.7"},
		{"every hop trusted gives the leftmost", proxies, req("10.0.0.5:4000", "10.1.1.1, 10.0.0.9"), "10.1.1.1"},
		{"a single trusted address", proxies, req("192.0.2.1:4000", "203.0.113.9"), "203.0.113.9"},
		{"ipv6 client behind a trusted proxy", proxies, req("10.0.0.5:4000", "2001:db8::7"), "2001:db8::7"},
		{"ipv6 trusted proxy", proxies, req("[fd00::1]:4000", "203.0.113.9"), "203.0.113.9"},
		{"entry with a port", proxies, req("10.0.0.5:4000", "198.51.100.7:51000"), "198.51.100.7"},
		{"bracketed ipv6 entry", proxies, req("10.0.0.5:4000", "[2001:db8::7]"), "2001:db8::7"},
		{"ipv4 mapped into ipv6", proxies, req("[::ffff:10.0.0.5]:4000", "198.51.100.7"), "198.51.100.7"},
		{"malformed entry falls back to the proxy", proxies, req("10.0.0.5:4000", "not-an-ip"), "10.0.0.5"},
		{"malformed entry among valid ones", proxies, req("10.0.0.5:4000", "198.51.100.7, garbage"), "10.0.0.5"},
		{"empty entry", proxies, req("10.0.0.5:4000", ""), "10.0.0.5"},
		{"no proxies trusted: header ignored", nil, req("10.0.0.5:4000", "198.51.100.7"), "10.0.0.5"},
		{"remote address without a port", proxies, req("198.51.100.7"), "198.51.100.7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewProxyTrust(tt.trusted, nil).ClientIP(tt.request)
			if got != tt.wantAddr {
				t.Errorf("ClientIP = %q, want %q", got, tt.wantAddr)
			}
		})
	}
}

// Two different clients must never end up with the same address, whatever header they send. This is the property whose
// absence caused a panel-wide login lockout in the reference (one shared rate-limit bucket).
func TestClientsBehindNoProxyStayDistinct(t *testing.T) {
	trust := NewProxyTrust(nil, nil)

	a := trust.ClientIP(req("198.51.100.1:1111", "9.9.9.9"))
	b := trust.ClientIP(req("198.51.100.2:2222", "9.9.9.9"))

	if a == b || a != "198.51.100.1" || b != "198.51.100.2" {
		t.Errorf("addresses = %q and %q", a, b)
	}
}

func TestWarnsOnceAboutAnUnconfiguredProxy(t *testing.T) {
	var logs bytes.Buffer
	trust := NewProxyTrust(nil, slog.New(slog.NewTextHandler(&logs, nil)))

	trust.ClientIP(req("10.0.0.5:1", "1.1.1.1"))
	trust.ClientIP(req("10.0.0.5:1", "1.1.1.1"))
	trust.ClientIP(req("10.0.0.5:1")) // no header: nothing to warn about

	if n := strings.Count(logs.String(), "RAPTOR_TRUSTED_PROXIES"); n != 1 {
		t.Errorf("the warning was logged %d times, want once:\n%s", n, logs.String())
	}

	var quiet bytes.Buffer
	configured := NewProxyTrust(prefixes(t, "10.0.0.0/8"), slog.New(slog.NewTextHandler(&quiet, nil)))
	configured.ClientIP(req("198.51.100.7:1", "1.1.1.1"))
	if quiet.Len() != 0 {
		t.Errorf("a configured proxy list must not warn: %s", quiet.String())
	}
}
