package middleware

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/auth/csrf"
	"github.com/Helles01/raptor-panel/internal/auth/session"
)

func newSessionManager(t *testing.T, secure bool) (*session.Manager, *session.MemoryStore) {
	t.Helper()
	store := session.NewMemoryStore()
	m, err := session.NewManager(store, session.Options{IdleTimeout: time.Hour, MaxLifetime: 24 * time.Hour, Secure: secure})
	if err != nil {
		t.Fatal(err)
	}
	return m, store
}

func TestLoadSession(t *testing.T) {
	m, _ := newSessionManager(t, true)
	s, err := m.Create(context.Background(), "198.51.100.1", "UA")
	if err != nil {
		t.Fatal(err)
	}

	var seen *session.Session
	e := echoWith(t)
	e.Use(LoadSession(m, true))
	e.GET("/", func(c echo.Context) error { seen = CurrentSession(c); return c.NoContent(http.StatusNoContent) })

	send := func(cookies ...*http.Cookie) *httptest.ResponseRecorder {
		seen = nil
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		for _, c := range cookies {
			r.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, r)
		return rec
	}

	t.Run("valid cookie", func(t *testing.T) {
		rec := send(&http.Cookie{Name: "__Host-raptor_session", Value: s.ID})
		if rec.Code != 204 || seen == nil || seen.ID != s.ID {
			t.Errorf("status %d, session %+v", rec.Code, seen)
		}
		if len(rec.Result().Cookies()) != 0 {
			t.Errorf("a valid session must not touch the cookie: %v", rec.Result().Cookies())
		}
	})
	t.Run("no cookie", func(t *testing.T) {
		if rec := send(); rec.Code != 204 || seen != nil {
			t.Errorf("status %d, session %+v", rec.Code, seen)
		}
	})
	t.Run("cookie with the wrong name is ignored", func(t *testing.T) {
		if send(&http.Cookie{Name: "raptor_session", Value: s.ID}); seen != nil {
			t.Error("the non-__Host cookie was used on a secure setup")
		}
	})
	t.Run("unknown session is cleared", func(t *testing.T) {
		rec := send(&http.Cookie{Name: "__Host-raptor_session", Value: strings.Repeat("a", 43)})
		cookies := rec.Result().Cookies()
		if rec.Code != 204 || seen != nil || len(cookies) != 1 || cookies[0].MaxAge >= 0 {
			t.Errorf("status %d, session %+v, cookies %v", rec.Code, seen, cookies)
		}
	})
	t.Run("malformed cookie is cleared", func(t *testing.T) {
		rec := send(&http.Cookie{Name: "__Host-raptor_session", Value: "../../x"})
		if seen != nil || len(rec.Result().Cookies()) != 1 {
			t.Errorf("session %+v, cookies %v", seen, rec.Result().Cookies())
		}
	})
}

func TestSetCSRFCookie(t *testing.T) {
	rec := httptest.NewRecorder()
	SetCSRFCookie(rec, "token-value", true)

	c := rec.Result().Cookies()[0]
	if c.Name != "XSRF-TOKEN" || c.Value != "token-value" || c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Errorf("cookie = %+v", c)
	}
}

type csrfFixture struct {
	e       *echo.Echo
	session *session.Session
	other   *session.Session
	token   string
	p       *csrf.Protector
	reached *int
}

func newCSRFFixture(t *testing.T, origins ...string) csrfFixture {
	t.Helper()
	m, _ := newSessionManager(t, false)
	s, _ := m.Create(context.Background(), "", "")
	other, _ := m.Create(context.Background(), "", "")
	p, err := csrf.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	token, _ := p.Issue(s.ID)

	reached := 0
	e := echoWith(t)
	e.Use(LoadSession(m, false), CSRF(CSRFOptions{Protector: p, AllowedOrigins: origins}))
	handler := func(c echo.Context) error { reached++; return c.NoContent(http.StatusNoContent) }
	for _, method := range []string{"GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE"} {
		e.Add(method, "/x", handler)
	}
	return csrfFixture{e: e, session: s, other: other, token: token, p: p, reached: &reached}
}

func (f csrfFixture) do(method string, mutate func(*http.Request)) int {
	r := httptest.NewRequest(method, "/x", nil)
	if mutate != nil {
		mutate(r)
	}
	rec := httptest.NewRecorder()
	f.e.ServeHTTP(rec, r)
	return rec.Code
}

func (f csrfFixture) withSession(s *session.Session, token string) func(*http.Request) {
	return func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "raptor_session", Value: s.ID})
		if token != "" {
			r.Header.Set("X-XSRF-TOKEN", token)
		}
	}
}

func TestCSRFSafeMethodsNeedNoToken(t *testing.T) {
	f := newCSRFFixture(t)
	for _, method := range []string{"GET", "HEAD", "OPTIONS"} {
		if code := f.do(method, nil); code != http.StatusNoContent {
			t.Errorf("%s without a session or token: status %d", method, code)
		}
	}
}

func TestCSRFUnsafeMethods(t *testing.T) {
	f := newCSRFFixture(t)
	otherToken, _ := f.p.Issue(f.other.ID)

	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			tests := []struct {
				name   string
				mutate func(*http.Request)
				want   int
			}{
				{"valid token", f.withSession(f.session, f.token), 204},
				{"no session", func(r *http.Request) { r.Header.Set("X-XSRF-TOKEN", f.token) }, 403},
				{"no token", f.withSession(f.session, ""), 403},
				{"wrong token", f.withSession(f.session, "a.b"), 403},
				{"token of another session", f.withSession(f.session, otherToken), 403},
				{"token in the cookie only", func(r *http.Request) {
					f.withSession(f.session, "")(r)
					r.AddCookie(&http.Cookie{Name: "XSRF-TOKEN", Value: f.token})
				}, 403},
				{"token in the query only", func(r *http.Request) {
					f.withSession(f.session, "")(r)
					r.URL.RawQuery = "X-XSRF-TOKEN=" + f.token
				}, 403},
			}
			for _, tt := range tests {
				if code := f.do(method, tt.mutate); code != tt.want {
					t.Errorf("%s: status %d, want %d", tt.name, code, tt.want)
				}
			}
		})
	}
}

func TestCSRFBearerRequestsAreExempt(t *testing.T) {
	f := newCSRFFixture(t)

	if code := f.do("POST", func(r *http.Request) { r.Header.Set("Authorization", "Bearer ptlc_abc") }); code != 204 {
		t.Errorf("bearer request: status %d", code)
	}
	if code := f.do("POST", func(r *http.Request) { r.Header.Set("Authorization", "bearer abc.def") }); code != 204 {
		t.Errorf("lower case scheme: status %d", code)
	}
	for _, header := range []string{"Basic abc", "Bearer", "Bearer ", "bearerabc", "Token abc"} {
		if code := f.do("POST", func(r *http.Request) { r.Header.Set("Authorization", header) }); code != 403 {
			t.Errorf("Authorization %q must not exempt the request: status %d", header, code)
		}
	}
}

func TestCSRFOriginCheck(t *testing.T) {
	f := newCSRFFixture(t, "https://panel.example.com")
	withOrigin := func(origin string) func(*http.Request) {
		valid := f.withSession(f.session, f.token)
		return func(r *http.Request) {
			valid(r)
			if origin != "" {
				r.Header.Set("Origin", origin)
			}
		}
	}

	tests := []struct {
		name   string
		origin string
		want   int
	}{
		{"allowed origin", "https://panel.example.com", 204},
		{"no origin header", "", 204},
		{"another site", "https://evil.example.com", 403},
		{"the null origin", "null", 403},
		{"same host other scheme", "http://panel.example.com", 403},
		{"same host other port", "https://panel.example.com:8443", 403},
		{"subdomain", "https://x.panel.example.com", 403},
		{"suffix trick", "https://panel.example.com.evil.com", 403},
	}
	for _, tt := range tests {
		if code := f.do("POST", withOrigin(tt.origin)); code != tt.want {
			t.Errorf("%s: status %d, want %d", tt.name, code, tt.want)
		}
	}

	// Even an allowed origin needs the token.
	if code := f.do("POST", func(r *http.Request) {
		f.withSession(f.session, "")(r)
		r.Header.Set("Origin", "https://panel.example.com")
	}); code != 403 {
		t.Errorf("allowed origin without a token: status %d", code)
	}
}

func TestCSRFRefusedRequestsNeverReachTheHandler(t *testing.T) {
	f := newCSRFFixture(t)

	f.do("POST", nil)
	f.do("POST", f.withSession(f.session, "bad"))

	if *f.reached != 0 {
		t.Errorf("the handler ran %d times for refused requests", *f.reached)
	}
}
