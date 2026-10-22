package middleware

import (
	"crypto/rand"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/ids"
)

// RequestIDKey is the echo context key of the request ID.
const RequestIDKey = "request_id"

// securityHeaders are the defaults of the reference panel's SetSecurityHeaders middleware. A Content-Security-Policy
// needs the nonce of the page shell and is added together with the shell (M3).
func securityHeaders() map[string]string {
	return map[string]string{
		"X-Frame-Options":        "DENY",
		"X-Content-Type-Options": "nosniff",
		"X-XSS-Protection":       "1; mode=block",
		"Referrer-Policy":        "no-referrer-when-downgrade",
	}
}

// SecurityHeaders sets the default security headers. A handler that wants a different value sets its own afterwards.
func SecurityHeaders() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			h := c.Response().Header()
			for k, v := range securityHeaders() {
				if h.Get(k) == "" {
					h.Set(k, v)
				}
			}
			return next(c)
		}
	}
}

// RequestID gives every request a fresh random ID, sends it back as X-Request-ID and makes it available to logs. An ID
// sent by the client is never used, so it cannot forge or pollute log entries.
func RequestID() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			id, err := ids.RandomAlnum(rand.Reader, 16)
			if err != nil {
				id = "unknown"
			}
			c.Set(RequestIDKey, id)
			c.Response().Header().Set("X-Request-ID", id)
			return next(c)
		}
	}
}

// ID returns the request ID set by RequestID, or "" if there is none.
func ID(c echo.Context) string {
	id, _ := c.Get(RequestIDKey).(string)
	return id
}
