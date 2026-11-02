package mail

import (
	"errors"
	"testing"
)

func settingsOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestOptionsFromSettings(t *testing.T) {
	base := map[string]string{
		"mail:mailers:smtp:host": "mail.example.com", "mail:mailers:smtp:port": "587", "mail:mailers:smtp:encryption": "tls",
		"mail:mailers:smtp:username": "panel", "mail:from:address": "panel@example.com",
	}
	with := func(k, v string) map[string]string {
		m := map[string]string{}
		for a, b := range base {
			m[a] = b
		}
		m[k] = v
		return m
	}
	o, err := OptionsFromSettings(settingsOf(base), "s3cret", "panel.example.com")
	if err != nil || o.Addr != "mail.example.com:587" || o.Security != StartTLS || o.Username != "panel" || o.Password != "s3cret" || o.From != "panel@example.com" || o.Hello != "panel.example.com" {
		t.Fatalf("options = %+v, %v", o, err)
	}
	for enc, want := range map[string]Security{"tls": StartTLS, "TLS": StartTLS, " ssl ": ImplicitTLS, "": NoTLS} {
		o, err := OptionsFromSettings(settingsOf(with("mail:mailers:smtp:encryption", enc)), "", "")
		if err != nil || o.Security != want {
			t.Errorf("encryption %q: %+v %v", enc, o.Security, err)
		}
	}
	// An address of IPv6 is written in brackets.
	if o, _ := OptionsFromSettings(settingsOf(with("mail:mailers:smtp:host", "::1")), "", ""); o.Addr != "[::1]:587" {
		t.Errorf("IPv6 host: %q", o.Addr)
	}

	if _, err := OptionsFromSettings(settingsOf(with("mail:mailers:smtp:host", "  ")), "", ""); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("no host: %v", err)
	}
	for name, m := range map[string]map[string]string{
		"no port":                 {"mail:mailers:smtp:host": "h", "mail:mailers:smtp:port": ""},
		"a port that is no port":  with("mail:mailers:smtp:port", "smtp"),
		"a port of zero":          with("mail:mailers:smtp:port", "0"),
		"a port too high":         with("mail:mailers:smtp:port", "70000"),
		"the first port too high": with("mail:mailers:smtp:port", "65536"),
		"encryption unknown":      with("mail:mailers:smtp:encryption", "starttls"),
	} {
		if _, err := OptionsFromSettings(settingsOf(m), "", ""); err == nil || errors.Is(err, ErrNotConfigured) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSenderFromSettings(t *testing.T) {
	get := settingsOf(map[string]string{"mail:mailers:smtp:host": "mail.example.com", "mail:mailers:smtp:port": "465", "mail:mailers:smtp:encryption": "ssl", "mail:from:address": "panel@example.com"})
	s, err := SenderFromSettings(get, "", "panel.example.com")
	if err != nil || s.o.Security != ImplicitTLS || s.host != "mail.example.com" {
		t.Fatalf("sender = %+v, %v", s, err)
	}
	if _, err := SenderFromSettings(settingsOf(nil), "", ""); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("not configured: %v", err)
	}
	// A password to another machine without protection is refused, here as anywhere.
	bad := settingsOf(map[string]string{"mail:mailers:smtp:host": "mail.example.com", "mail:mailers:smtp:port": "25", "mail:mailers:smtp:username": "panel", "mail:from:address": "panel@example.com"})
	if _, err := SenderFromSettings(bad, "s3cret", ""); err == nil {
		t.Error("a password in the clear")
	}
	// A sender without a sender address is refused too.
	if _, err := SenderFromSettings(settingsOf(map[string]string{"mail:mailers:smtp:host": "h", "mail:mailers:smtp:port": "25"}), "", ""); err == nil {
		t.Error("no from address")
	}
}

func TestThePortsAtTheEdges(t *testing.T) {
	for _, port := range []string{"1", "65535"} {
		o, err := OptionsFromSettings(settingsOf(map[string]string{"mail:mailers:smtp:host": "h", "mail:mailers:smtp:port": port}), "", "")
		if err != nil || o.Addr != "h:"+port {
			t.Errorf("port %s: %+v %v", port, o, err)
		}
	}
}
