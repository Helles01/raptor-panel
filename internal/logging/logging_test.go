package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/config"
)

func newLogger(t *testing.T, level, format string) (*bytes.Buffer, func(msg string, args ...any), func(msg string, args ...any)) {
	t.Helper()
	var buf bytes.Buffer
	l, err := New(config.Log{Level: level, Format: format}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	return &buf, func(m string, a ...any) { l.Debug(m, a...) }, func(m string, a ...any) { l.Info(m, a...); l.Warn(m, a...); l.Error(m, a...) }
}

func TestLevels(t *testing.T) {
	for level, want := range map[string]int{"debug": 4, "info": 3, "warn": 2, "error": 1} {
		buf, debug, rest := newLogger(t, level, "text")
		debug("x")
		rest("x")
		if got := strings.Count(buf.String(), "\n"); got != want {
			t.Errorf("level %s wrote %d lines, want %d:\n%s", level, got, want, buf.String())
		}
	}
}

func TestJSONAndTextFormats(t *testing.T) {
	buf, _, rest := newLogger(t, "info", "json")
	rest("hello", "user", "ada")
	first := strings.SplitN(buf.String(), "\n", 2)[0]
	var rec map[string]any
	if err := json.Unmarshal([]byte(first), &rec); err != nil || rec["msg"] != "hello" || rec["user"] != "ada" || rec["level"] != "INFO" {
		t.Errorf("json line = %q (%v)", first, err)
	}
	if ts, _ := rec["time"].(string); !strings.HasSuffix(ts, "Z") {
		t.Errorf("the time is not UTC: %q", ts)
	}

	buf, _, rest = newLogger(t, "info", "text")
	rest("hello", "user", "ada")
	if line := buf.String(); !strings.Contains(line, "msg=hello") || !strings.Contains(line, "user=ada") || strings.HasPrefix(line, "{") {
		t.Errorf("text line = %q", line)
	}
}

func TestSecretsNeverReachTheLog(t *testing.T) {
	const key = "ptlc_abcdefghijABCDEFGHIJabcdefghijABCDEFGHIJabc"
	for _, format := range []string{"text", "json"} {
		buf, _, rest := newLogger(t, "info", format)
		rest("login with Bearer abc.def.ghi and "+key, "password", "hunter2", "api_key", key, "err", errors.New("failed: token=sekret123 for ada"))
		out := buf.String()
		for _, leaked := range []string{"hunter2", key, "abc.def.ghi", "sekret123"} {
			if strings.Contains(out, leaked) {
				t.Errorf("%s: %q reached the log:\n%s", format, leaked, out)
			}
		}
		if !strings.Contains(out, "[REDACTED]") || !strings.Contains(out, "ada") {
			t.Errorf("%s: out = %s", format, out)
		}
	}
}

func TestNewRefuses(t *testing.T) {
	if _, err := New(config.Log{Level: "info", Format: "text"}, nil); err == nil {
		t.Error("no writer accepted")
	}
	for _, cfg := range []config.Log{{Level: "loud", Format: "text"}, {Level: "info", Format: "xml"}, {}} {
		if _, err := New(cfg, &bytes.Buffer{}); err == nil {
			t.Errorf("%+v accepted", cfg)
		}
	}
}
