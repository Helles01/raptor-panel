package config

import (
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

func TestHTTPDefaultsAndEnvironment(t *testing.T) {
	cfg, err := Load("", env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Listen != "127.0.0.1:8080" || len(cfg.HTTP.TrustedProxies) != 0 {
		t.Errorf("defaults = %+v", cfg.HTTP)
	}

	cfg, err = Load("", env(map[string]string{
		"RAPTOR_HTTP_LISTEN":     ":9000",
		"RAPTOR_TRUSTED_PROXIES": " 10.0.0.0/8, 192.0.2.7 ,, ::1 ",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Listen != ":9000" {
		t.Errorf("Listen = %q", cfg.HTTP.Listen)
	}
	if want := []string{"10.0.0.0/8", "192.0.2.7", "::1"}; !reflect.DeepEqual(cfg.HTTP.TrustedProxies, want) {
		t.Errorf("TrustedProxies = %q, want %q", cfg.HTTP.TrustedProxies, want)
	}
}

func TestHTTPFromFile(t *testing.T) {
	path := writeFile(t, "http:\n  listen: 0.0.0.0:8443\n  trusted_proxies: [\"172.16.0.0/12\"]\n")

	cfg, err := Load(path, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Listen != "0.0.0.0:8443" || !reflect.DeepEqual(cfg.HTTP.TrustedProxies, []string{"172.16.0.0/12"}) {
		t.Errorf("HTTP = %+v", cfg.HTTP)
	}
}

func TestHTTPValidate(t *testing.T) {
	tests := []struct {
		name    string
		http    HTTP
		wantErr string
	}{
		{"default", HTTP{Listen: "127.0.0.1:8080"}, ""},
		{"all interfaces", HTTP{Listen: ":8080"}, ""},
		{"ipv6", HTTP{Listen: "[::1]:8080"}, ""},
		{"with proxies", HTTP{Listen: ":8080", TrustedProxies: []string{"10.0.0.0/8", "203.0.113.9", "fd00::/8"}}, ""},
		{"no port", HTTP{Listen: "127.0.0.1"}, "host:port"},
		{"empty", HTTP{}, "host:port"},
		{"port zero", HTTP{Listen: "127.0.0.1:0"}, "port"},
		{"port too large", HTTP{Listen: "127.0.0.1:70000"}, "port"},
		{"port not a number", HTTP{Listen: "127.0.0.1:http"}, "port"},
		{"host with a slash", HTTP{Listen: "a/b:80"}, "host"},
		{"proxy is not an address", HTTP{Listen: ":80", TrustedProxies: []string{"proxy.example.com"}}, "not a valid IP"},
		{"proxy range is broken", HTTP{Listen: ":80", TrustedProxies: []string{"10.0.0.0/33"}}, "not a valid CIDR"},
		{"trusting every ipv4 address", HTTP{Listen: ":80", TrustedProxies: []string{"0.0.0.0/0"}}, "every address"},
		{"trusting every ipv6 address", HTTP{Listen: ":80", TrustedProxies: []string{"::/0"}}, "every address"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.http.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestTrustedPrefixes(t *testing.T) {
	h := HTTP{TrustedProxies: []string{"10.1.2.3/8", "192.0.2.7", "::ffff:198.51.100.4", "2001:db8::1"}}

	got, err := h.TrustedPrefixes()
	if err != nil {
		t.Fatal(err)
	}

	want := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"), // host bits are cleared
		netip.MustParsePrefix("192.0.2.7/32"),
		netip.MustParsePrefix("198.51.100.4/32"), // an IPv4-mapped address becomes a plain IPv4 prefix
		netip.MustParsePrefix("2001:db8::1/128"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("prefixes = %v, want %v", got, want)
	}

	if none, err := (HTTP{}).TrustedPrefixes(); err != nil || len(none) != 0 {
		t.Errorf("no proxies: %v, %v", none, err)
	}
}
