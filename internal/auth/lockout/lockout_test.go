package lockout

import "testing"

func TestClient(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want string
	}{
		{"IPv4 with a port", "203.0.113.9:51234", "203.0.113.9"},
		{"IPv4 without a port", "203.0.113.9", "203.0.113.9"},
		{"IPv6 with a port", "[2001:db8:1:2:3:4:5:6]:22", "2001:db8:1:2::/64"},
		{"IPv6 without a port", "2001:db8:1:2:3:4:5:6", "2001:db8:1:2::/64"},
		{"another address in the same /64", "[2001:db8:1:2:ffff::1]:99", "2001:db8:1:2::/64"},
		{"another /64", "2001:db8:1:3::1", "2001:db8:1:3::/64"},
		{"IPv4 inside IPv6", "[::ffff:203.0.113.9]:22", "203.0.113.9"},
		{"a zone is dropped", "[fe80::1%eth0]:22", "fe80::/64"},
		{"garbage", "not an address", "unknown"},
		{"empty", "", "unknown"},
		{"a port only", ":22", "unknown"},
		{"a host name", "client.example.com:22", "unknown"},
		{"an address with a bad port", "203.0.113.9:99999", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Client(tt.addr, "unknown"); got != tt.want {
				t.Errorf("Client(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}

func TestClientFallbackIsWhatTheCallerSays(t *testing.T) {
	if Client("", "node-7") != "node-7" || Client("", "node-8") != "node-8" {
		t.Error("the fallback must be returned as given")
	}
}
