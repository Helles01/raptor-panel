package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/api/resp"
	"github.com/Helles01/raptor-panel/internal/api/validate"
	"github.com/Helles01/raptor-panel/internal/auth"
)

// recordingAuthorizer allows everything except what deny says, and remembers every decision.
type recordingAuthorizer struct {
	seen []Permission
	deny func(p Permission) error
}

func (a *recordingAuthorizer) Authorize(_ echo.Context, p Permission) error {
	a.seen = append(a.seen, p)
	if a.deny != nil {
		return a.deny(p)
	}
	return nil
}

func newTestServer(t *testing.T, authz Authorizer, trusted ...string) (*Server, *bytes.Buffer) {
	t.Helper()
	var prefixes []netip.Prefix
	for _, s := range trusted {
		prefixes = append(prefixes, netip.MustParsePrefix(s))
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))

	s, err := New(Options{Logger: log, Trust: middleware.NewProxyTrust(prefixes, log), Authorizer: authz})
	if err != nil {
		t.Fatal(err)
	}
	return s, &logs
}

func serve(t *testing.T, s *Server, method, target string, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	h, err := s.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	r := httptest.NewRequest(method, target, nil)
	r.RemoteAddr = "198.51.100.9:4000"
	for _, m := range mutate {
		m(r)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func errorsOf(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var body struct {
		Errors []map[string]any `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not the API error format: %v", rec.Body.String(), err)
	}
	return body.Errors
}

func TestNewRequiresItsDependencies(t *testing.T) {
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	if _, err := New(Options{Authorizer: &recordingAuthorizer{}}); err == nil {
		t.Error("New accepted a missing logger")
	}
	if _, err := New(Options{Logger: log}); err == nil {
		t.Error("New accepted a missing authorizer")
	}
}

func TestUnknownRouteAndMethodUseTheAPIErrorFormat(t *testing.T) {
	s, _ := newTestServer(t, &recordingAuthorizer{})
	s.Router.GET("/api/thing", func(c echo.Context) error { return c.NoContent(http.StatusNoContent) }, Public())

	notFound := serve(t, s, http.MethodGet, "/api/nothing")
	if notFound.Code != 404 || errorsOf(t, notFound)[0]["code"] != "NotFoundHttpException" || notFound.Header().Get("Content-Type") != "application/json" {
		t.Errorf("unknown route: %d %s", notFound.Code, notFound.Body)
	}

	wrongMethod := serve(t, s, http.MethodDelete, "/api/thing")
	if wrongMethod.Code != 405 || errorsOf(t, wrongMethod)[0]["code"] != "MethodNotAllowedHttpException" {
		t.Errorf("wrong method: %d %s", wrongMethod.Code, wrongMethod.Body)
	}
	if allow := wrongMethod.Header().Get("Allow"); !strings.Contains(allow, "GET") {
		t.Errorf("Allow header = %q", allow)
	}

	for _, rec := range []*httptest.ResponseRecorder{notFound, wrongMethod} {
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("X-Request-ID") == "" {
			t.Errorf("the chain did not run for an error response: %v", rec.Header())
		}
	}
}

func TestHeadErrorsHaveNoBody(t *testing.T) {
	s, _ := newTestServer(t, &recordingAuthorizer{})
	rec := serve(t, s, http.MethodHead, "/missing")
	if rec.Code != 404 || rec.Body.Len() != 0 {
		t.Errorf("HEAD error: %d with %d body bytes", rec.Code, rec.Body.Len())
	}
}

func TestAuthorizerSeesTheRoutePermission(t *testing.T) {
	authz := &recordingAuthorizer{}
	s, _ := newTestServer(t, authz)
	var fromContext Permission
	s.Router.POST("/servers/:server/power", func(c echo.Context) error {
		fromContext, _ = c.Get(PermissionKey).(Permission)
		return c.NoContent(http.StatusNoContent)
	}, ServerPermission("control.start"))

	rec := serve(t, s, http.MethodPost, "/servers/abc/power")

	want := ServerPermission("control.start")
	if rec.Code != http.StatusNoContent || len(authz.seen) != 1 || authz.seen[0] != want || fromContext != want {
		t.Errorf("status %d, authorizer saw %v, handler saw %v", rec.Code, authz.seen, fromContext)
	}
}

func TestDeniedRequestsNeverReachTheHandler(t *testing.T) {
	tests := []struct {
		name     string
		deny     error
		wantCode int
	}{
		{"not logged in", apierr.Unauthenticated(), 401},
		{"not allowed", apierr.Forbidden("This action is unauthorized."), 403},
		{"authorizer failure", errors.New("session store is down"), 500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newTestServer(t, &recordingAuthorizer{deny: func(Permission) error { return tt.deny }})
			reached := false
			s.Router.GET("/secret", func(c echo.Context) error { reached = true; return c.NoContent(204) }, Authenticated())

			rec := serve(t, s, http.MethodGet, "/secret")

			if rec.Code != tt.wantCode || reached {
				t.Errorf("status %d, handler reached: %v", rec.Code, reached)
			}
			if strings.Contains(rec.Body.String(), "session store") {
				t.Errorf("an internal error reached the client: %s", rec.Body)
			}
		})
	}
}

func TestRouteMiddlewareRunsBeforeTheAuthorizer(t *testing.T) {
	// A throttle must count unauthenticated attempts too, so it has to run before authorization.
	var order []string
	authz := &recordingAuthorizer{deny: func(Permission) error { order = append(order, "authorize"); return nil }}
	s, _ := newTestServer(t, authz)
	mw := func(name string) echo.MiddlewareFunc {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error { order = append(order, name); return next(c) }
		}
	}

	api := s.Router.Sub("/api", mw("group"))
	api.GET("/x", func(c echo.Context) error { order = append(order, "handler"); return c.NoContent(204) }, Public(), mw("route"))

	serve(t, s, http.MethodGet, "/api/x")

	if got := strings.Join(order, ","); got != "group,route,authorize,handler" {
		t.Errorf("order = %s", got)
	}
}

func TestRegistrationPanicsOnMistakes(t *testing.T) {
	ok := func(c echo.Context) error { return nil }
	tests := []struct {
		name string
		do   func(r *Router)
		want string
	}{
		{"no permission", func(r *Router) { r.GET("/x", ok, Permission{}) }, "no permission given"},
		{"unknown kind", func(r *Router) { r.GET("/x", ok, Permission{Kind: "everyone"}) }, "unknown permission kind"},
		{"public with a resource", func(r *Router) { r.GET("/x", ok, Permission{Kind: KindPublic, Resource: "servers"}) }, "takes no resource"},
		{"unknown application resource", func(r *Router) { r.GET("/x", ok, ApplicationKey("admins", auth.AccessRead)) }, "unknown application resource"},
		{"application key without access", func(r *Router) { r.GET("/x", ok, ApplicationKey("servers", auth.AccessNone)) }, "read or read-write"},
		{"bad subuser permission", func(r *Router) { r.GET("/x", ok, ServerPermission("start")) }, "not a subuser permission"},
		{"subuser permission with a path", func(r *Router) { r.GET("/x", ok, ServerPermission("../etc.passwd")) }, "not a subuser permission"},
		{"no handler", func(r *Router) { r.GET("/x", nil, Public()) }, "no handler"},
		{"duplicate", func(r *Router) { r.GET("/x", ok, Public()); r.GET("/x", ok, Public()) }, "registered twice"},
		{"duplicate through a group", func(r *Router) { r.GET("/a/x", ok, Public()); r.Sub("/a").GET("/x", ok, Public()) }, "registered twice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newTestServer(t, &recordingAuthorizer{})

			defer func() {
				msg, _ := recover().(string)
				if !strings.Contains(msg, tt.want) {
					t.Errorf("panic = %q, want it to contain %q", msg, tt.want)
				}
			}()
			tt.do(s.Router)
		})
	}
}

func TestValidPermissionsAreAccepted(t *testing.T) {
	ok := func(c echo.Context) error { return nil }
	s, _ := newTestServer(t, &recordingAuthorizer{})
	r := s.Router

	r.GET("/guest", ok, Guest())
	r.GET("/public", ok, Public())
	r.GET("/authed", ok, Authenticated())
	r.GET("/admin", ok, Admin())
	r.GET("/daemon", ok, Daemon())
	r.GET("/app-read", ok, ApplicationKey("servers", auth.AccessRead))
	r.POST("/app-write", ok, ApplicationKey("nodes", auth.AccessReadWrite))
	r.GET("/server", ok, ServerPermission(""))
	r.POST("/server-perm", ok, ServerPermission("file.read-content"))
	r.PUT("/put", ok, Public())
	r.PATCH("/patch", ok, Public())
	r.DELETE("/delete", ok, Public())

	if n := len(r.Routes()); n != 12 {
		t.Errorf("routes = %d, want 12", n)
	}
	if err := r.Verify(); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

func TestVerifyCatchesRoutesAddedBehindTheRouter(t *testing.T) {
	s, _ := newTestServer(t, &recordingAuthorizer{})
	s.Router.GET("/ok", func(c echo.Context) error { return nil }, Public())
	if _, err := s.Handler(); err != nil {
		t.Fatalf("Handler with only registered routes: %v", err)
	}

	s.Echo.GET("/sneaky", func(c echo.Context) error { return c.String(200, "no permission check") })

	h, err := s.Handler()
	if err == nil || h != nil || !strings.Contains(err.Error(), "GET /sneaky") {
		t.Errorf("Handler = %v, %v; want an error naming GET /sneaky", h, err)
	}
}

func TestRoutesAreSortedAndCopied(t *testing.T) {
	s, _ := newTestServer(t, &recordingAuthorizer{})
	ok := func(c echo.Context) error { return nil }
	s.Router.POST("/b", ok, Public())
	s.Router.GET("/b", ok, Public())
	s.Router.GET("/a", ok, Public())

	routes := s.Router.Routes()

	got := []string{}
	for _, rt := range routes {
		got = append(got, rt.Method+" "+rt.Path)
	}
	if strings.Join(got, ",") != "GET /a,GET /b,POST /b" {
		t.Errorf("routes = %v", got)
	}
	routes[0].Path = "/changed"
	if s.Router.Routes()[0].Path != "/a" {
		t.Error("Routes must return a copy")
	}
}

func TestNewRouterPanicsWithoutAnAuthorizer(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewRouter without an authorizer did not panic")
		}
	}()
	NewRouter(echo.New(), nil)
}

func TestPermissionString(t *testing.T) {
	tests := map[Permission]string{
		Public():                                      "public",
		ServerPermission("control.start"):             "server:control.start",
		ServerPermission(""):                          "server",
		ApplicationKey("servers", auth.AccessRead):    "application-key:servers:1",
		ApplicationKey("nodes", auth.AccessReadWrite): "application-key:nodes:3",
	}
	for p, want := range tests {
		if got := p.String(); got != want {
			t.Errorf("%+v.String() = %q, want %q", p, got, want)
		}
	}
}

func TestHandlersUseValidationAndEnvelopes(t *testing.T) {
	s, _ := newTestServer(t, &recordingAuthorizer{})
	s.Router.POST("/things", func(c echo.Context) error {
		var in struct {
			Name string `json:"name"`
		}
		if err := validate.DecodeJSON(c.Request().Body, &in, 1024); err != nil {
			return err
		}
		v := validate.New()
		v.Required("name", in.Name)
		v.Length("name", in.Name, 1, 10)
		if err := v.Err(); err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, resp.NewItem("thing", map[string]any{"name": in.Name}))
	}, Authenticated())
	s.Router.GET("/things", func(c echo.Context) error {
		page, perPage, err := resp.PageParams(c.QueryParams())
		if err != nil {
			return err
		}
		items := []resp.Item{resp.NewItem("thing", map[string]any{"id": 1})}
		return c.JSON(http.StatusOK, resp.NewList(items).WithPagination(resp.NewPagination(c.Request().URL, 120, perPage, page, len(items))))
	}, Authenticated())

	post := func(body string) *httptest.ResponseRecorder {
		return serve(t, s, http.MethodPost, "/things", func(r *http.Request) {
			r.Body = http.NoBody
			if body != "" {
				r.Body = nopCloser{strings.NewReader(body)}
			}
		})
	}

	t.Run("created", func(t *testing.T) {
		rec := post(`{"name":"anvil"}`)
		if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"object":"thing"`) {
			t.Errorf("%d %s", rec.Code, rec.Body)
		}
	})
	t.Run("validation failure is a 422 with one entry per field", func(t *testing.T) {
		rec := post(`{"name":""}`)
		errs := errorsOf(t, rec)
		if rec.Code != 422 || len(errs) != 1 || errs[0]["meta"].(map[string]any)["source_field"] != "name" {
			t.Errorf("%d %s", rec.Code, rec.Body)
		}
	})
	t.Run("malformed body is a 400", func(t *testing.T) {
		if rec := post(`{"name":`); rec.Code != 400 {
			t.Errorf("%d %s", rec.Code, rec.Body)
		}
	})
	t.Run("list with pagination", func(t *testing.T) {
		rec := serve(t, s, http.MethodGet, "/things?page=2&per_page=50")
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != 200 || got["object"] != "list" {
			t.Fatalf("%d %s (%v)", rec.Code, rec.Body, err)
		}
		pagination := got["meta"].(map[string]any)["pagination"].(map[string]any)
		links := pagination["links"].(map[string]any)
		next, _ := url.Parse(links["next"].(string))
		if pagination["total"] != float64(120) || pagination["total_pages"] != float64(3) || next.Query().Get("page") != "3" || next.Query().Get("per_page") != "50" {
			t.Errorf("pagination = %v", pagination)
		}
	})
	t.Run("bad page parameter is a 422", func(t *testing.T) {
		if rec := serve(t, s, http.MethodGet, "/things?per_page=500"); rec.Code != 422 {
			t.Errorf("%d %s", rec.Code, rec.Body)
		}
	})
}

type nopCloser struct{ *strings.Reader }

func (nopCloser) Close() error { return nil }

func TestHandlerFailuresAreGenericAndLogged(t *testing.T) {
	s, logs := newTestServer(t, &recordingAuthorizer{})
	s.Router.GET("/fail", func(c echo.Context) error { return errors.New("connect: password=hunter2 refused") }, Public())
	s.Router.GET("/panic", func(c echo.Context) error { panic("kaboom") }, Public())

	for _, path := range []string{"/fail", "/panic"} {
		rec := serve(t, s, http.MethodGet, path)

		if rec.Code != 500 || errorsOf(t, rec)[0]["detail"] != apierr.InternalDetail {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "kaboom") {
			t.Errorf("%s: internals reached the client: %s", path, rec.Body)
		}
	}
	if strings.Contains(logs.String(), "hunter2") {
		t.Errorf("a secret reached the log: %s", logs)
	}
}

func TestClientAddressFollowsTheTrustedProxyList(t *testing.T) {
	var seen string
	s, _ := newTestServer(t, &recordingAuthorizer{}, "10.0.0.0/8")
	s.Router.GET("/ip", func(c echo.Context) error { seen = c.RealIP(); return c.NoContent(204) }, Public())

	via := func(remote, xff string) string {
		serve(t, s, http.MethodGet, "/ip", func(r *http.Request) {
			r.RemoteAddr = remote
			r.Header.Set("X-Forwarded-For", xff)
			r.Header.Set("X-Real-IP", "6.6.6.6")
		})
		return seen
	}

	if got := via("198.51.100.9:1", "1.2.3.4"); got != "198.51.100.9" {
		t.Errorf("a direct client chose its own address: %q", got)
	}
	if got := via("10.0.0.2:1", "203.0.113.5"); got != "203.0.113.5" {
		t.Errorf("a trusted proxy was not believed: %q", got)
	}
}
