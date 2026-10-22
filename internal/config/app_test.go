package config

import (
	"strings"
	"testing"
	"time"
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

func TestTimezone(t *testing.T) {
	cfg, err := Load("", env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.App.Timezone != "UTC" {
		t.Errorf("default timezone = %q, want UTC", cfg.App.Timezone)
	}

	cfg, err = Load("", env(map[string]string{"RAPTOR_APP_TIMEZONE": "Europe/Berlin"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.App.Timezone != "Europe/Berlin" {
		t.Errorf("timezone from the environment = %q", cfg.App.Timezone)
	}

	path := writeFile(t, "app:\n  timezone: America/New_York\n")
	cfg, err = Load(path, env(nil))
	if err != nil || cfg.App.Timezone != "America/New_York" {
		t.Errorf("timezone from the file = %q, %v", cfg.App.Timezone, err)
	}
	cfg, err = Load(path, env(map[string]string{"RAPTOR_APP_TIMEZONE": "UTC"}))
	if err != nil || cfg.App.Timezone != "UTC" {
		t.Errorf("the environment must win over the file: %q, %v", cfg.App.Timezone, err)
	}
}

func TestLocation(t *testing.T) {
	winter := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	summer := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		zone         string
		wantErr      bool
		winter, sumr int // offset in seconds
	}{
		{"empty is UTC", "", false, 0, 0},
		{"UTC", "UTC", false, 0, 0},
		{"Berlin", "Europe/Berlin", false, 3600, 7200},
		{"New York", "America/New_York", false, -5 * 3600, -4 * 3600},
		{"a zone with more segments", "America/Argentina/Buenos_Aires", false, -3 * 3600, -3 * 3600},
		{"a zone with a sign", "Etc/GMT+5", false, -5 * 3600, -5 * 3600},

		{"Local depends on the machine", "Local", true, 0, 0},
		{"unknown name", "Mars/Olympus", true, 0, 0},
		{"a path out of the database", "../etc/passwd", true, 0, 0},
		{"an absolute path", "/etc/localtime", true, 0, 0},
		{"a backslash", `Europe\Berlin`, true, 0, 0},
		{"a space", "Europe/ Berlin", true, 0, 0},
		{"a trailing slash", "Europe/", true, 0, 0},
		{"a leading digit", "1UTC", true, 0, 0},
		{"a newline", "UTC\n", true, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loc, err := App{Timezone: tt.zone}.Location()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Location(%q) error = %v, wantErr %v", tt.zone, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if _, off := winter.In(loc).Zone(); off != tt.winter {
				t.Errorf("winter offset = %d, want %d", off, tt.winter)
			}
			if _, off := summer.In(loc).Zone(); off != tt.sumr {
				t.Errorf("summer offset = %d, want %d", off, tt.sumr)
			}
		})
	}
}

func TestAppValidateChecksTheTimezone(t *testing.T) {
	if err := (App{URL: "https://panel.example.com", Timezone: "Europe/Berlin"}).Validate(); err != nil {
		t.Errorf("a valid app: %v", err)
	}
	err := App{URL: "https://panel.example.com", Timezone: "Mars/Olympus"}.Validate()
	if err == nil || !strings.Contains(err.Error(), "app.timezone") {
		t.Errorf("error = %v, want one that names app.timezone", err)
	}
}
