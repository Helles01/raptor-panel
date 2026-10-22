package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/auth/csrf"
	"github.com/Helles01/raptor-panel/internal/auth/session"
)

const sessionContextKey = "raptor.session"

// LoadSession loads the session named by the session cookie, if there is a valid one, and makes it available through
// CurrentSession. A missing or invalid cookie is not an error: the request simply has no session. An invalid cookie is
// cleared so the browser stops sending it.
func LoadSession(m *session.Manager, secure bool) echo.MiddlewareFunc {
	name := session.CookieName(secure)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			cookie, err := c.Cookie(name)
			if err != nil || cookie.Value == "" {
				return next(c)
			}

			s, err := m.Load(c.Request().Context(), cookie.Value)
			switch {
			case err == nil:
				c.Set(sessionContextKey, s)
			case errors.Is(err, session.ErrNotFound):
				http.SetCookie(c.Response(), m.ClearCookie())
			default:
				return apierr.Internal(err)
			}
			return next(c)
		}
	}
}

// CurrentSession returns the session of the request, or nil.
func CurrentSession(c echo.Context) *session.Session {
	s, _ := c.Get(sessionContextKey).(*session.Session)
	return s
}

// SetCurrentSession makes s the session of the request, for example right after a login created a new one.
func SetCurrentSession(c echo.Context, s *session.Session) { c.Set(sessionContextKey, s) }

// CSRFCookieName is the cookie the frontend's HTTP client reads its token from; it sends the value back in
// CSRFHeaderName.
const (
	CSRFCookieName = "XSRF-TOKEN"
	CSRFHeaderName = "X-XSRF-TOKEN"
)

// SetCSRFCookie sends the anti-CSRF token. The cookie must be readable by the frontend's JavaScript, so it is not
// HttpOnly; it is worthless without the session it is bound to.
func SetCSRFCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: CSRFCookieName, Value: token, Path: "/", HttpOnly: false, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

// CSRFOptions configure the CSRF middleware.
type CSRFOptions struct {
	Protector *csrf.Protector
	// AllowedOrigins are the origins ("https://panel.example.com") that may send state-changing requests. When
	// empty the Origin header is not checked; the token still is.
	AllowedOrigins []string
}

// CSRF refuses state-changing requests (anything but GET, HEAD, OPTIONS and TRACE) that do not carry the anti-CSRF
// token of their session, or that come from an origin that is not allowed. Requests that authenticate with an
// "Authorization: Bearer" header are exempt: a browser cannot be made to add that header to a cross-site request, and
// the authenticator must then ignore the cookie.
func CSRF(opts CSRFOptions) echo.MiddlewareFunc {
	origins := map[string]bool{}
	for _, o := range opts.AllowedOrigins {
		origins[o] = true
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			r := c.Request()
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
				return next(c)
			}
			if hasBearer(r) {
				return next(c)
			}

			if len(origins) > 0 {
				if origin := r.Header.Get("Origin"); origin != "" && !origins[origin] {
					return apierr.Forbidden("Cross-origin request refused.")
				}
			}

			s := CurrentSession(c)
			if s == nil || !opts.Protector.Valid(s.ID, r.Header.Get(CSRFHeaderName)) {
				return apierr.Forbidden("CSRF token mismatch.")
			}
			return next(c)
		}
	}
}

func hasBearer(r *http.Request) bool {
	h := r.Header.Get("Authorization")
	return len(h) > 7 && strings.EqualFold(h[:7], "bearer ")
}
