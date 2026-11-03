// Package logging makes the panel's logger from the configuration. Every log line of the panel goes through the redacting handler, so a
// password, a token or an API key that gets into a message or an attribute is replaced before it is written.
package logging

import (
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/config"
)

// New returns the logger for a configuration, writing to w (the standard error of the process in the program). Times are in UTC.
func New(cfg config.Log, w io.Writer) (*slog.Logger, error) {
	if w == nil {
		return nil, errors.New("logging: a logger needs somewhere to write")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
		return nil, err
	}
	opts := &slog.HandlerOptions{Level: level, ReplaceAttr: utc}

	var h slog.Handler
	if cfg.Format == "json" {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(middleware.NewRedactingHandler(h)), nil
}

// utc writes the time of an entry in UTC, so that logs of machines in different zones can be read together.
func utc(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey && a.Value.Kind() == slog.KindTime {
		return slog.Time(a.Key, a.Value.Time().UTC().Truncate(time.Millisecond))
	}
	return a
}
