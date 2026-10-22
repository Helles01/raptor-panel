package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
)

const redacted = "[REDACTED]"

// sensitiveKeys are fragments of attribute names whose values are never logged.
var sensitiveKeys = []string{
	"password", "passwd", "secret", "token", "authorization", "cookie", "apikey", "api_key", "api-key",
	"private_key", "totp", "recovery",
}

// secretPatterns find secrets inside free text: API keys, bearer credentials, JWTs, Raptor's sealed values and
// secret-looking URL parameters.
var secretPatterns = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`ptl[ac]_[A-Za-z0-9]{43}`), redacted},
	{regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`), "${1}" + redacted},
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`), redacted},
	{regexp.MustCompile(`rp1:[A-Za-z0-9_-]+`), redacted},
	{regexp.MustCompile(`(?i)((?:token|password|secret|apikey|api_key)=)[^&\s"']+`), "${1}" + redacted},
}

func redactString(s string) string {
	for _, p := range secretPatterns {
		s = p.re.ReplaceAllString(s, p.with)
	}
	return s
}

func sensitiveKey(key string) bool {
	k := strings.ToLower(key)
	for _, frag := range sensitiveKeys {
		if strings.Contains(k, frag) {
			return true
		}
	}
	return false
}

func redactAttr(a slog.Attr) slog.Attr {
	if sensitiveKey(a.Key) {
		return slog.String(a.Key, redacted)
	}

	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindGroup:
		group := v.Group()
		out := make([]any, 0, len(group))
		for _, g := range group {
			out = append(out, redactAttr(g))
		}
		return slog.Group(a.Key, out...)
	case slog.KindString:
		return slog.String(a.Key, redactString(v.String()))
	case slog.KindAny:
		// Errors and arbitrary values may contain anything; log their text, redacted.
		return slog.String(a.Key, redactString(fmt.Sprint(v.Any())))
	default:
		return slog.Attr{Key: a.Key, Value: v}
	}
}

type redactingHandler struct{ inner slog.Handler }

// NewRedactingHandler wraps a slog handler so that secrets never reach the log: values of attributes with sensitive
// names (password, token, secret, authorization, cookie, ...) are replaced, and secret-looking text inside any message,
// string or error is masked.
func NewRedactingHandler(inner slog.Handler) slog.Handler { return &redactingHandler{inner: inner} }

func (h *redactingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, redactString(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, out)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		clean = append(clean, redactAttr(a))
	}
	return &redactingHandler{inner: h.inner.WithAttrs(clean)}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{inner: h.inner.WithGroup(name)}
}

// RequestLogger logs one line per request: method, path, status, size, duration, client address and request ID. It
// never logs the query string, headers or bodies, because tokens travel in all three.
func RequestLogger(log *slog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()

			err := next(c)
			if err != nil {
				// Let the error handler write the response now, so the status logged below is the real one.
				c.Error(err)
			}

			res := c.Response()
			level := slog.LevelInfo
			if res.Status >= 500 {
				level = slog.LevelError
			}
			attrs := []slog.Attr{
				slog.String("request_id", ID(c)),
				slog.String("method", c.Request().Method),
				slog.String("path", c.Request().URL.Path),
				slog.Int("status", res.Status),
				slog.Int64("bytes", res.Size),
				slog.Duration("duration", time.Since(start)),
				slog.String("ip", c.RealIP()),
			}
			if err != nil && res.Status >= 500 {
				attrs = append(attrs, slog.String("error", err.Error()))
			}
			log.LogAttrs(c.Request().Context(), level, "request", attrs...)

			return err
		}
	}
}

// Recover turns a panic in a handler into a generic 500. The panic value and stack go to the log (redacted), never to
// the client.
func Recover(log *slog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) (err error) {
			defer func() {
				if r := recover(); r != nil {
					log.LogAttrs(c.Request().Context(), slog.LevelError, "panic in handler",
						slog.String("request_id", ID(c)),
						slog.String("panic", fmt.Sprint(r)),
						slog.String("stack", string(debug.Stack())),
					)
					err = apierr.Internal(fmt.Errorf("panic: %v", r))
				}
			}()
			return next(c)
		}
	}
}
