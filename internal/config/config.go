// Package config loads the Raptor configuration from an optional YAML file and RAPTOR_* environment variables.
//
// The environment wins over the file. Secrets are accepted from either place (the file must be kept out of version
// control, see .gitignore) and never appear in String(), %v or log output.
package config

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strconv"

	"go.yaml.in/yaml/v3"

	"github.com/Helles01/raptor-panel/internal/secrets"
)

// Config is the process configuration.
type Config struct {
	DB      DB      `yaml:"db"`
	Secrets Secrets `yaml:"secrets"`
}

// DB describes the MariaDB/MySQL connection.
type DB struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Name     string `yaml:"name"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	// TLS is one of "false", "true", "skip-verify" or "preferred".
	TLS string `yaml:"tls"`
}

// Secrets holds the key for Raptor's own secret format.
type Secrets struct {
	// Key is the base64 encoding of 32 random bytes, see `raptor keygen`.
	Key string `yaml:"key"`
}

var (
	hostPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,255}$`)
	namePattern = regexp.MustCompile(`^[A-Za-z0-9_$.-]{1,64}$`)
)

// Load reads the file at path (or at $RAPTOR_CONFIG when path is empty; no file is read when both are empty), then
// applies the RAPTOR_* environment variables. It does not validate: commands validate the parts they need.
func Load(path string, getenv func(string) string) (Config, error) {
	cfg := Config{DB: DB{Host: "127.0.0.1", Port: 3306, TLS: "false"}}

	if path == "" {
		path = getenv("RAPTOR_CONFIG")
	}
	if path != "" {
		if err := readFile(path, &cfg); err != nil {
			return Config{}, err
		}
	}
	if err := applyEnv(&cfg, getenv); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func readFile(path string, cfg *Config) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open config file: %w", err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("parse config file %s: %w", path, err)
	}
	return nil
}

func applyEnv(cfg *Config, getenv func(string) string) error {
	override := func(dst *string, name string) {
		if v := getenv(name); v != "" {
			*dst = v
		}
	}
	override(&cfg.DB.Host, "RAPTOR_DB_HOST")
	override(&cfg.DB.Name, "RAPTOR_DB_NAME")
	override(&cfg.DB.User, "RAPTOR_DB_USER")
	override(&cfg.DB.Password, "RAPTOR_DB_PASSWORD")
	override(&cfg.DB.TLS, "RAPTOR_DB_TLS")
	override(&cfg.Secrets.Key, "RAPTOR_SECRET_KEY")

	if v := getenv("RAPTOR_DB_PORT"); v != "" {
		port, err := strconv.Atoi(v)
		if err != nil {
			return errors.New("RAPTOR_DB_PORT must be a number")
		}
		cfg.DB.Port = port
	}
	return nil
}

// Validate checks the database settings against simple allow-lists.
func (d DB) Validate() error {
	switch {
	case !hostPattern.MatchString(d.Host):
		return errors.New("db.host must be 1-255 characters from A-Z a-z 0-9 . _ : -")
	case d.Port < 1 || d.Port > 65535:
		return errors.New("db.port must be between 1 and 65535")
	case !namePattern.MatchString(d.Name):
		return errors.New("db.name must be set, at most 64 characters from A-Z a-z 0-9 _ $ . -")
	case !namePattern.MatchString(d.User):
		return errors.New("db.user must be set, at most 64 characters from A-Z a-z 0-9 _ $ . -")
	}

	switch d.TLS {
	case "false", "true", "skip-verify", "preferred":
		return nil
	default:
		return errors.New(`db.tls must be one of "false", "true", "skip-verify", "preferred"`)
	}
}

// KeyBytes decodes the secret key.
func (s Secrets) KeyBytes() ([]byte, error) {
	if s.Key == "" {
		return nil, errors.New("secrets.key is not set (generate one with `raptor keygen` and pass it as RAPTOR_SECRET_KEY)")
	}
	return secrets.ParseKey(s.Key)
}

func redacted(set bool) string {
	if set {
		return "[redacted]"
	}
	return "[empty]"
}

// String never includes the password.
func (d DB) String() string {
	return fmt.Sprintf("db{host=%s port=%d name=%s user=%s password=%s tls=%s}",
		d.Host, d.Port, d.Name, d.User, redacted(d.Password != ""), d.TLS)
}

// GoString keeps %#v from printing the password.
func (d DB) GoString() string { return d.String() }

// LogValue keeps slog from printing the password.
func (d DB) LogValue() slog.Value { return slog.StringValue(d.String()) }

// String never includes the key.
func (s Secrets) String() string { return fmt.Sprintf("secrets{key=%s}", redacted(s.Key != "")) }

// GoString keeps %#v from printing the key.
func (s Secrets) GoString() string { return s.String() }

// LogValue keeps slog from printing the key.
func (s Secrets) LogValue() slog.Value { return slog.StringValue(s.String()) }
