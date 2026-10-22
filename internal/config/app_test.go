package config

import (
	"strings"
	"testing"
)

func TestAppValidateAndOrigin(t *testing.T) {
	tests := []struct {
		url        string
		wantErr    bool
		wantOrigin string
	}{
		{"https://panel.example.com", false, "https://panel.example.com"},
		{"https://panel.example.com/", false, "https://panel.example.com"},
		{"http://localhost:8080", false, "http://localhost:8080"},
		{"https://[2001:db8::1]:8443", false, "https://[2001:db8::1]:8443"},
		{"", true, ""},
		{"panel.example.com", true, ""},
		{"ftp://panel.example.com", true, ""},
		{"https://", true, ""},
		{"https://user:pw@panel.example.com", true, ""},
		{"https://panel.example.com/admin", true, ""},
		{"https://panel.example.com?x=1", true, ""},
		{"https://panel.example.com/#frag", true, ""},
		{"javascript:alert(1)", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			a := App{URL: tt.url}

			err := a.Validate()

			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && a.Origin() != tt.wantOrigin {
				t.Errorf("Origin = %q, want %q", a.Origin(), tt.wantOrigin)
			}
		})
	}
}

func TestWingsDefaultsAndValidate(t *testing.T) {
	cfg, err := Load("", env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Wings != (Wings{TimeoutSeconds: 15, ConnectTimeoutSeconds: 5}) || cfg.Wings.Validate() != nil {
		t.Errorf("defaults = %+v", cfg.Wings)
	}

	for name, w := range map[string]Wings{
		"zero timeout":       {TimeoutSeconds: 0, ConnectTimeoutSeconds: 1},
		"huge timeout":       {TimeoutSeconds: 301, ConnectTimeoutSeconds: 5},
		"connect over total": {TimeoutSeconds: 5, ConnectTimeoutSeconds: 6},
		"zero connect":       {TimeoutSeconds: 5, ConnectTimeoutSeconds: 0},
		"negative":           {TimeoutSeconds: -1, ConnectTimeoutSeconds: -1},
	} {
		if err := w.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAppAndWingsFromTheEnvironment(t *testing.T) {
	cfg, err := Load("", env(map[string]string{"RAPTOR_APP_URL": "https://panel.example.com", "RAPTOR_WINGS_INSECURE_TLS": "true"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.App.URL != "https://panel.example.com" || !cfg.Wings.InsecureTLS {
		t.Errorf("config = %+v %+v", cfg.App, cfg.Wings)
	}

	if _, err := Load("", env(map[string]string{"RAPTOR_WINGS_INSECURE_TLS": "maybe"})); err == nil || !strings.Contains(err.Error(), "RAPTOR_WINGS_INSECURE_TLS") {
		t.Errorf("error = %v", err)
	}

	path := writeFile(t, "app:\n  url: https://from-file.example.com\nwings:\n  timeout_seconds: 30\n  connect_timeout_seconds: 10\n")
	cfg, err = Load(path, env(nil))
	if err != nil || cfg.App.URL != "https://from-file.example.com" || cfg.Wings.TimeoutSeconds != 30 || cfg.Wings.ConnectTimeoutSeconds != 10 {
		t.Errorf("from file: %+v %+v %v", cfg.App, cfg.Wings, err)
	}
}
