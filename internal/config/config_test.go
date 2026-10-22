package config

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	file := writeFile(t, `
db:
  host: db.internal
  port: 3307
  name: panel
  user: raptor
  password: from-file
  tls: preferred
`)

	tests := []struct {
		name    string
		path    string
		vars    map[string]string
		want    DB
		wantKey string
	}{
		{
			name: "defaults without file or environment",
			want: DB{Host: "127.0.0.1", Port: 3306, TLS: "false"},
		},
		{
			name: "file only",
			path: file,
			want: DB{Host: "db.internal", Port: 3307, Name: "panel", User: "raptor", Password: "from-file", TLS: "preferred"},
		},
		{
			name: "environment overrides file",
			path: file,
			vars: map[string]string{"RAPTOR_DB_HOST": "10.0.0.5", "RAPTOR_DB_PORT": "3308", "RAPTOR_DB_PASSWORD": "from-env", "RAPTOR_SECRET_KEY": "k"},
			want: DB{Host: "10.0.0.5", Port: 3308, Name: "panel", User: "raptor", Password: "from-env", TLS: "preferred"},

			wantKey: "k",
		},
		{
			name: "file path from RAPTOR_CONFIG",
			vars: map[string]string{"RAPTOR_CONFIG": file},
			want: DB{Host: "db.internal", Port: 3307, Name: "panel", User: "raptor", Password: "from-file", TLS: "preferred"},
		},
		{
			name: "environment only",
			vars: map[string]string{"RAPTOR_DB_NAME": "p", "RAPTOR_DB_USER": "u", "RAPTOR_DB_TLS": "true"},
			want: DB{Host: "127.0.0.1", Port: 3306, Name: "p", User: "u", TLS: "true"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(tt.path, env(tt.vars))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.DB != tt.want {
				t.Errorf("DB = %+v, want %+v", cfg.DB, tt.want)
			}
			if cfg.Secrets.Key != tt.wantKey {
				t.Errorf("Secrets.Key = %q, want %q", cfg.Secrets.Key, tt.wantKey)
			}
		})
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name string
		path string
		vars map[string]string
		want string
	}{
		{"missing file", filepath.Join(t.TempDir(), "nope.yml"), nil, "open config file"},
		{"unknown field", writeFile(t, "db:\n  pasword: x\n"), nil, "pasword"},
		{"not yaml", writeFile(t, "db: [unclosed"), nil, "parse config file"},
		{"port is not a number", "", map[string]string{"RAPTOR_DB_PORT": "abc"}, "RAPTOR_DB_PORT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(tt.path, env(tt.vars))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Load error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestLoadEmptyFile(t *testing.T) {
	cfg, err := Load(writeFile(t, ""), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DB.Port != 3306 {
		t.Errorf("an empty file must leave the defaults, got port %d", cfg.DB.Port)
	}
}

func TestDBValidate(t *testing.T) {
	valid := DB{Host: "db-1.example.com", Port: 3306, Name: "panel", User: "raptor", TLS: "false"}

	tests := []struct {
		name    string
		mutate  func(*DB)
		wantErr string
	}{
		{"valid", func(*DB) {}, ""},
		{"ipv6 host", func(d *DB) { d.Host = "::1" }, ""},
		{"tls true", func(d *DB) { d.TLS = "true" }, ""},
		{"tls skip-verify", func(d *DB) { d.TLS = "skip-verify" }, ""},
		{"empty host", func(d *DB) { d.Host = "" }, "db.host"},
		{"host with space", func(d *DB) { d.Host = "db host" }, "db.host"},
		{"host with slash", func(d *DB) { d.Host = "db/host" }, "db.host"},
		{"port zero", func(d *DB) { d.Port = 0 }, "db.port"},
		{"port too large", func(d *DB) { d.Port = 65536 }, "db.port"},
		{"empty name", func(d *DB) { d.Name = "" }, "db.name"},
		{"name with quote", func(d *DB) { d.Name = "pa`nel" }, "db.name"},
		{"name too long", func(d *DB) { d.Name = strings.Repeat("a", 65) }, "db.name"},
		{"empty user", func(d *DB) { d.User = "" }, "db.user"},
		{"user with semicolon", func(d *DB) { d.User = "a;b" }, "db.user"},
		{"unknown tls", func(d *DB) { d.TLS = "maybe" }, "db.tls"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := valid
			tt.mutate(&d)
			err := d.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("Validate: unexpected error %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("Validate error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestSecretsKeyBytes(t *testing.T) {
	good := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))

	if key, err := (Secrets{Key: good}).KeyBytes(); err != nil || len(key) != 32 {
		t.Errorf("KeyBytes(valid) = %d bytes, %v", len(key), err)
	}
	for _, bad := range []string{"", "not base64!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if _, err := (Secrets{Key: bad}).KeyBytes(); err == nil {
			t.Errorf("KeyBytes(%q) succeeded, want an error", bad)
		}
	}
}

func TestSecretsNeverPrinted(t *testing.T) {
	const password, key = "hunter2-db-password", "c2VjcmV0LWtleS1tYXRlcmlhbC1ub3QtZm9yLWxvZ3M="
	cfg := Config{
		DB:      DB{Host: "h", Port: 1, Name: "n", User: "u", Password: password, TLS: "false"},
		Secrets: Secrets{Key: key},
	}

	var logged bytes.Buffer
	slog.New(slog.NewTextHandler(&logged, nil)).Info("config loaded", "db", cfg.DB, "secrets", cfg.Secrets, "config", cfg)

	outputs := map[string]string{
		"%v":   fmt.Sprintf("%v", cfg),
		"%+v":  fmt.Sprintf("%+v", cfg),
		"%#v":  fmt.Sprintf("%#v", cfg),
		"%s":   fmt.Sprintf("%s %s", cfg.DB, cfg.Secrets),
		"slog": logged.String(),
	}
	for verb, out := range outputs {
		if strings.Contains(out, password) || strings.Contains(out, key) {
			t.Errorf("%s output leaks a secret: %s", verb, out)
		}
	}
}
