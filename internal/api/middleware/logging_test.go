package middleware

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
)

const (
	apiKey = "ptla_AbCdEfGhIjK" + "0123456789abcdefghijklmnopqrstuv" // identifier (16) + secret (32)
	jwt    = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.c2lnbmF0dXJl"
)

func newRedactingLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(NewRedactingHandler(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
}

func TestRedactionByAttributeName(t *testing.T) {
	for _, key := range []string{"password", "Password", "new_password", "token", "access_token", "authorization", "Authorization",
		"cookie", "set-cookie", "client_secret", "api_key", "x-api-key", "totp_code", "recovery_token"} {
		var buf bytes.Buffer

		newRedactingLogger(&buf).Info("event", key, "hunter2")

		if strings.Contains(buf.String(), "hunter2") || !strings.Contains(buf.String(), redacted) {
			t.Errorf("attribute %q was not redacted: %s", key, buf.String())
		}
	}
}

func TestRedactionKeepsHarmlessAttributes(t *testing.T) {
	var buf bytes.Buffer

	newRedactingLogger(&buf).Info("event", "user", "ada", "status", 200, "key_type", 2)

	for _, want := range []string{"user=ada", "status=200", "key_type=2"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("%q is missing from %s", want, buf.String())
		}
	}
}

func TestRedactionInsideText(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		leaks []string
	}{
		{"api key", "key " + apiKey + " rejected", []string{apiKey[16:]}},
		{"bearer", "Authorization: Bearer abc123.DEF456", []string{"abc123", "DEF456"}},
		{"lower case bearer", "bearer abc123", []string{"abc123"}},
		{"jwt", "token " + jwt, []string{"c2lnbmF0dXJl", "eyJzdWIiOiJ4In0"}},
		{"sealed value", "stored rp1:AAAAAAAAAAAAAAAAAAAAAA_-Zz9 ok", []string{"AAAAAAAAAAAAAAAAAAAAAA"}},
		{"url parameter", "GET /download?token=s3cr3tvalue&file=a.txt", []string{"s3cr3tvalue"}},
		{"password parameter", "retry password=hunter2&x=1", []string{"hunter2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := newRedactingLogger(&buf)

			log.Info(tt.text)
			log.Info("event", "detail", tt.text)
			log.Error("event", "error", errors.New(tt.text))
			log.With("ctx", tt.text).Info("event")

			for _, leak := range tt.leaks {
				if strings.Contains(buf.String(), leak) {
					t.Errorf("%q leaked:\n%s", leak, buf.String())
				}
			}
		})
	}
}

func TestRedactionInGroupsAndWithAttrs(t *testing.T) {
	var buf bytes.Buffer
	log := newRedactingLogger(&buf)

	log.With("password", "hunter2").WithGroup("req").Info("event", slog.Group("auth", "token", "abc"), "ok", 1)

	if strings.Contains(buf.String(), "hunter2") || strings.Contains(buf.String(), "abc") {
		t.Errorf("a secret leaked through With or a group: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "ok=1") {
		t.Errorf("harmless attribute lost: %s", buf.String())
	}
}

func TestRedactingHandlerRespectsLevels(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})

	if NewRedactingHandler(inner).Enabled(context.Background(), slog.LevelInfo) {
		t.Error("Info must be disabled when the inner handler is at Warn")
	}
}

func newServer(t *testing.T, log *slog.Logger, handler echo.HandlerFunc) *echo.Echo {
	t.Helper()
	e := echoWith(t)
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		if c.Response().Committed {
			return
		}
		var ae *apierr.Error
		if !errors.As(err, &ae) {
			ae = apierr.Internal(err)
		}
		apierr.Write(c.Response(), ae)
	}
	e.Use(Recover(log), RequestID(), RequestLogger(log), SecurityHeaders())
	e.GET("/things/:id", handler)
	return e
}

func TestRequestLoggerNeverLogsQueriesHeadersOrBodies(t *testing.T) {
	var buf bytes.Buffer
	e := newServer(t, newRedactingLogger(&buf), func(c echo.Context) error { return c.String(http.StatusOK, "ok") })

	r := httptest.NewRequest(http.MethodGet, "/things/7?token=querysecret&session=querysecret2", nil)
	r.Header.Set("Authorization", "Bearer headersecret")
	r.Header.Set("Cookie", "raptor_session=cookiesecret")
	r.RemoteAddr = "198.51.100.9:5555"
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, r)

	out := buf.String()
	for _, leak := range []string{"querysecret", "querysecret2", "headersecret", "cookiesecret"} {
		if strings.Contains(out, leak) {
			t.Errorf("%q was logged: %s", leak, out)
		}
	}
	for _, want := range []string{"method=GET", "path=/things/7", "status=200", "ip=198.51.100.9", "request_id=" + rec.Header().Get("X-Request-ID")} {
		if !strings.Contains(out, want) {
			t.Errorf("%q is missing from the log line: %s", want, out)
		}
	}
}

func TestRequestLoggerReportsTheFinalStatus(t *testing.T) {
	var buf bytes.Buffer
	e := newServer(t, newRedactingLogger(&buf), func(c echo.Context) error { return apierr.NotFound() })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/things/1", nil))

	if rec.Code != http.StatusNotFound || !strings.Contains(buf.String(), "status=404") {
		t.Errorf("status %d, log: %s", rec.Code, buf.String())
	}
	if strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("a 404 is not a server error: %s", buf.String())
	}
}

func TestInternalErrorsGoToTheLogOnly(t *testing.T) {
	var buf bytes.Buffer
	e := newServer(t, newRedactingLogger(&buf), func(c echo.Context) error {
		return fmt.Errorf("dial db: password=hunter2 refused")
	})

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/things/1", nil))

	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), apierr.InternalDetail) {
		t.Errorf("status %d body %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "dial db") || strings.Contains(rec.Body.String(), "hunter2") {
		t.Errorf("the cause reached the client: %s", rec.Body)
	}
	if !strings.Contains(buf.String(), "level=ERROR") || strings.Contains(buf.String(), "hunter2") {
		t.Errorf("the failure must be logged at error level, redacted: %s", buf.String())
	}
}

func TestRecoverTurnsAPanicIntoAGeneric500(t *testing.T) {
	var buf bytes.Buffer
	e := newServer(t, newRedactingLogger(&buf), func(c echo.Context) error { panic("boom token=abc123") })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/things/1", nil))

	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), apierr.InternalDetail) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "boom") || strings.Contains(rec.Body.String(), "goroutine") {
		t.Errorf("panic details reached the client: %s", rec.Body)
	}
	if !strings.Contains(buf.String(), "panic in handler") || strings.Contains(buf.String(), "abc123") {
		t.Errorf("the panic must be logged, with secrets redacted: %s", buf.String())
	}
}

func TestSecurityHeadersAndRequestID(t *testing.T) {
	e := newServer(t, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), func(c echo.Context) error {
		c.Response().Header().Set("X-Frame-Options", "SAMEORIGIN") // a handler may override a default
		return c.String(http.StatusOK, "ok")
	})

	send := func(clientID string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/things/1", nil)
		if clientID != "" {
			r.Header.Set("X-Request-ID", clientID)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, r)
		return rec
	}

	rec := send("forged\nid")

	want := map[string]string{
		"X-Frame-Options":        "SAMEORIGIN",
		"X-Content-Type-Options": "nosniff",
		"X-Xss-Protection":       "1; mode=block",
		"Referrer-Policy":        "no-referrer-when-downgrade",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}

	id := rec.Header().Get("X-Request-ID")
	if len(id) != 16 || strings.Contains(id, "forged") {
		t.Errorf("request id = %q, want a fresh 16 character id (the client's value must not be used)", id)
	}
	if other := send("").Header().Get("X-Request-ID"); other == id {
		t.Error("two requests got the same request id")
	}
}

func TestSecurityHeadersAreAlsoOnErrors(t *testing.T) {
	e := newServer(t, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), func(c echo.Context) error { return apierr.Forbidden("no") })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/things/1", nil))

	if rec.Code != http.StatusForbidden || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("status %d, headers %v", rec.Code, rec.Header())
	}
}
