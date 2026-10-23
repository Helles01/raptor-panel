package authz

import (
	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/auth/session"
)

const (
	principalKey = "raptor.principal"
	nodeKey      = "raptor.node"
)

// Principal is who is calling, as the authorizer found out.
type Principal struct {
	User User
	// Key is the API key of the request, nil when the user came by session. Session is set when the user came by session. They are
	// never both set.
	Key     *Key
	Session *session.Session
	// Server and Permissions are set on a route below /servers/{server}: the server, and what the user may do with it ("*" for the
	// owner, with the admin.* ones added for an administrator).
	Server      *Server
	Permissions []string
}

func setPrincipal(c echo.Context, p Principal) { c.Set(principalKey, p) }

// From returns the caller of a request that passed a route for the signed in, a server or an application key. The second result is
// false on any other route.
func From(c echo.Context) (Principal, bool) {
	p, ok := c.Get(principalKey).(Principal)
	return p, ok
}

func setNode(c echo.Context, n Node) { c.Set(nodeKey, n) }

// NodeFrom returns the node of a request that passed a route for the daemon.
func NodeFrom(c echo.Context) (Node, bool) {
	n, ok := c.Get(nodeKey).(Node)
	return n, ok
}
