// Package api assembles the HTTP server: the router with its mandatory permission descriptors, the error handler and
// the middleware chain. The endpoint handlers live in the sub-packages (client, application, remote, ...).
package api

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/auth"
)

// Kind says who may call a route.
type Kind string

const (
	// KindGuest is for pages that make no sense once logged in, such as the login form.
	KindGuest Kind = "guest"
	// KindPublic needs no login at all.
	KindPublic Kind = "public"
	// KindAuthenticated needs a logged-in user, by session or by client API key.
	KindAuthenticated Kind = "authenticated"
	// KindAdmin needs a logged-in root admin.
	KindAdmin Kind = "admin"
	// KindApplicationKey needs an application API key with access to one resource.
	KindApplicationKey Kind = "application-key"
	// KindServer needs access to the server in the route, optionally with one subuser permission.
	KindServer Kind = "server"
	// KindDaemon needs the token of a Wings node.
	KindDaemon Kind = "daemon"
)

// ServerState says in which states of a server a route may be used. A route that does not say is for a server in its normal state.
type ServerState int

const (
	// StateNormal: only a server that is installed, not suspended, not under maintenance, not restoring and not transferring.
	StateNormal ServerState = iota
	// StateAny: whatever the state, for the route that shows the server itself, so that an installing server can still be looked at.
	StateAny
	// StateAdminConsole: as StateNormal, except that an administrator may also connect to a server that is installing, failed,
	// restoring or transferring (not suspended, and not on a node under maintenance).
	StateAdminConsole
	// StateReinstall: as StateNormal, except that a server whose first installation failed may be reinstalled by its owner (C-048).
	StateReinstall
)

// Permission is the descriptor every route must be registered with. Registering a route without one is a programming
// error that stops the server from starting, so no route can be forgotten.
type Permission struct {
	Kind Kind
	// Resource is the application API resource (KindApplicationKey) or the subuser permission such as
	// "control.start" (KindServer; empty means access to the server is enough).
	Resource string
	// Access is the level an application key needs on Resource.
	Access auth.Access
	// State is for KindServer: the states of the server the route may be used in.
	State ServerState
	// NoTwoFactor exempts a route from the requirement of two-factor authentication, so that a user who must switch it on can reach
	// what it takes to do so.
	NoTwoFactor bool
}

func Guest() Permission         { return Permission{Kind: KindGuest} }
func Public() Permission        { return Permission{Kind: KindPublic} }
func Authenticated() Permission { return Permission{Kind: KindAuthenticated} }
func Admin() Permission         { return Permission{Kind: KindAdmin} }
func Daemon() Permission        { return Permission{Kind: KindDaemon} }

// ApplicationKey is a route that needs an application API key with the given access to a resource.
func ApplicationKey(resource string, access auth.Access) Permission {
	return Permission{Kind: KindApplicationKey, Resource: resource, Access: access}
}

// ServerPermission is a route below /servers/{server}. permission is the subuser permission needed, or "" if any access
// to the server is enough.
func ServerPermission(permission string) Permission {
	return Permission{Kind: KindServer, Resource: permission}
}

// ServerPermissionIn is ServerPermission for a route that may be used in other states of the server than the normal one.
func ServerPermissionIn(permission string, state ServerState) Permission {
	return Permission{Kind: KindServer, Resource: permission, State: state}
}

// WithoutTwoFactor returns the permission of a route that is open to a user who has not switched two-factor authentication on
// although the panel requires it: the account's own details and the setting up of two-factor authentication.
func (p Permission) WithoutTwoFactor() Permission {
	p.NoTwoFactor = true
	return p
}

var serverPermissionPattern = regexp.MustCompile(`^[a-z][a-z-]*\.[a-z][a-z-]*$`)

func (p Permission) String() string {
	s := string(p.Kind)
	switch {
	case p.Resource == "":
	case p.Kind == KindApplicationKey:
		s = fmt.Sprintf("%s:%s:%d", p.Kind, p.Resource, p.Access)
	default:
		s = fmt.Sprintf("%s:%s", p.Kind, p.Resource)
	}
	if p.State != StateNormal {
		s += fmt.Sprintf("[state %d]", p.State)
	}
	if p.NoTwoFactor {
		s += "[no 2fa]"
	}
	return s
}

func (p Permission) validate() error {
	switch p.Kind {
	case KindGuest, KindPublic, KindAuthenticated, KindAdmin, KindDaemon:
		if p.Resource != "" || p.Access != auth.AccessNone {
			return fmt.Errorf("a %s permission takes no resource or access", p.Kind)
		}
		if p.State != StateNormal {
			return fmt.Errorf("a %s permission takes no server state", p.Kind)
		}
		if p.NoTwoFactor && p.Kind != KindAuthenticated {
			return fmt.Errorf("only a route for the signed in may be open without two-factor authentication")
		}
	case KindApplicationKey:
		if p.State != StateNormal || p.NoTwoFactor {
			return fmt.Errorf("an application key permission takes no server state or exemption")
		}
		known := false
		for _, r := range auth.ApplicationResources() {
			known = known || r == p.Resource
		}
		if !known {
			return fmt.Errorf("unknown application resource %q", p.Resource)
		}
		if p.Access != auth.AccessRead && p.Access != auth.AccessReadWrite {
			return fmt.Errorf("an application key permission needs read or read-write access")
		}
	case KindServer:
		if p.Resource != "" && !serverPermissionPattern.MatchString(p.Resource) {
			return fmt.Errorf("%q is not a subuser permission name", p.Resource)
		}
		if p.State < StateNormal || p.State > StateReinstall {
			return fmt.Errorf("unknown server state %d", p.State)
		}
		if p.NoTwoFactor {
			return fmt.Errorf("a server route cannot be open without two-factor authentication")
		}
	case "":
		return fmt.Errorf("no permission given")
	default:
		return fmt.Errorf("unknown permission kind %q", p.Kind)
	}
	return nil
}

// PermissionKey is the echo context key under which the permission of the matched route is available to handlers.
const PermissionKey = "raptor.permission"

// Authorizer decides whether the request may use a route with the given permission. It returns nil to allow, or an
// error (normally an *apierr.Error: 401, 403) to deny. It runs after the route's own middleware, such as a throttle.
type Authorizer interface {
	Authorize(c echo.Context, p Permission) error
}

// Route is one registered route and its permission.
type Route struct {
	Method     string
	Path       string
	Permission Permission
}

// Router registers routes on an Echo instance and keeps the list of what was registered. Handlers cannot be added any
// other way: the Echo instance is not exposed to the endpoint packages.
type Router struct {
	e      *echo.Echo
	authz  Authorizer
	routes *[]Route
	Group
}

// Group is a set of routes under a common prefix and middleware.
type Group struct {
	r      *Router
	prefix string
	mw     []echo.MiddlewareFunc
	// after runs after the route has been authorized, so that it can tell who is asking.
	after []echo.MiddlewareFunc
}

// NewRouter returns a Router. It panics if there is no Authorizer: a router that does not authorize is never wanted.
func NewRouter(e *echo.Echo, authz Authorizer) *Router {
	if e == nil || authz == nil {
		panic("api: NewRouter needs an Echo instance and an Authorizer")
	}
	r := &Router{e: e, authz: authz, routes: &[]Route{}}
	r.Group = Group{r: r}
	return r
}

// Sub returns a sub-group below prefix. Its middleware runs before the middleware of the routes in it.
func (g Group) Sub(prefix string, mw ...echo.MiddlewareFunc) Group {
	return Group{r: g.r, prefix: g.prefix + prefix, mw: append(append([]echo.MiddlewareFunc(nil), g.mw...), mw...), after: g.after}
}

// AfterAuth returns a group whose routes run the middleware after the route has been authorized, where the middleware of Sub runs before:
// it is for what depends on who is asking, such as a limit on the requests of a user. A request that is refused by the authorizer never
// reaches it.
func (g Group) AfterAuth(mw ...echo.MiddlewareFunc) Group {
	return Group{r: g.r, prefix: g.prefix, mw: g.mw, after: append(append([]echo.MiddlewareFunc(nil), g.after...), mw...)}
}

func (g Group) GET(path string, h echo.HandlerFunc, p Permission, mw ...echo.MiddlewareFunc) {
	g.Handle(http.MethodGet, path, h, p, mw...)
}

func (g Group) POST(path string, h echo.HandlerFunc, p Permission, mw ...echo.MiddlewareFunc) {
	g.Handle(http.MethodPost, path, h, p, mw...)
}

func (g Group) PUT(path string, h echo.HandlerFunc, p Permission, mw ...echo.MiddlewareFunc) {
	g.Handle(http.MethodPut, path, h, p, mw...)
}

func (g Group) PATCH(path string, h echo.HandlerFunc, p Permission, mw ...echo.MiddlewareFunc) {
	g.Handle(http.MethodPatch, path, h, p, mw...)
}

func (g Group) DELETE(path string, h echo.HandlerFunc, p Permission, mw ...echo.MiddlewareFunc) {
	g.Handle(http.MethodDelete, path, h, p, mw...)
}

// Handle registers a route. It panics on an invalid or missing permission and on a duplicate route.
func (g Group) Handle(method, path string, h echo.HandlerFunc, p Permission, mw ...echo.MiddlewareFunc) {
	full := g.prefix + path
	if err := p.validate(); err != nil {
		panic(fmt.Sprintf("api: route %s %s: %v", method, full, err))
	}
	if h == nil {
		panic(fmt.Sprintf("api: route %s %s has no handler", method, full))
	}
	for _, existing := range *g.r.routes {
		if existing.Method == method && existing.Path == full {
			panic(fmt.Sprintf("api: route %s %s is registered twice", method, full))
		}
	}

	chain := append(append([]echo.MiddlewareFunc(nil), g.mw...), mw...)
	chain = append(chain, g.r.authorize(p))
	chain = append(chain, g.after...)

	g.r.e.Add(method, full, h, chain...)
	*g.r.routes = append(*g.r.routes, Route{Method: method, Path: full, Permission: p})
}

func (r *Router) authorize(p Permission) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(PermissionKey, p)
			if err := r.authz.Authorize(c, p); err != nil {
				return err
			}
			return next(c)
		}
	}
}

// Routes returns every registered route, sorted by path and method.
func (r *Router) Routes() []Route {
	out := append([]Route(nil), *r.routes...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out
}

// Verify checks that every route known to Echo was registered through the Router, that is, has a permission. Call it
// once all routes are set up; the server refuses to start if it fails.
func (r *Router) Verify() error {
	registered := map[string]bool{}
	for _, rt := range *r.routes {
		registered[rt.Method+" "+rt.Path] = true
	}

	var missing []string
	for _, er := range r.e.Routes() {
		if !registered[er.Method+" "+er.Path] {
			missing = append(missing, er.Method+" "+er.Path)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("routes without a permission descriptor: %s", strings.Join(missing, ", "))
	}
	return nil
}
