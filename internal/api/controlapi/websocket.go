package controlapi

import (
	"errors"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/permissions"
	"github.com/Helles01/raptor-panel/internal/wings"
)

// subject is the caller as the token maker sees them.
type subject struct {
	id   uint64
	uuid string
	held []string
}

func (s subject) ID() uint64                 { return s.id }
func (s subject) UUID() string               { return s.uuid }
func (s subject) Can(permission string) bool { return permissions.Has(s.held, permission) }
func (s subject) Permissions() []string      { return s.held }

type websocketData struct {
	Token  string `json:"token"`
	Socket string `json:"socket"`
}

// Websocket is GET /api/client/servers/{server}/websocket: the address of the console on the node and a token to open it with. The
// route holds the permission websocket.connect, and the token carries the caller's permissions on this server, which the node
// enforces on every message.
func (h *Handlers) Websocket(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	// Five tokens a minute for a server, however many people ask. Only calls that have passed the authorizer count, so that
	// a stranger cannot use the allowance up.
	if allowed, retry := h.d.WebsocketLimiter.Allow(p.Server.UUID); !allowed {
		return apierr.TooManyRequests(retry)
	}
	t, err := h.target(c, p)
	if err != nil {
		return err
	}
	node, err := consoleNode(t, p.Permissions)
	if err != nil {
		return err
	}

	token, err := h.d.Tokens.Websocket(node, subject{id: p.User.ID, uuid: p.User.UUID, held: p.Permissions}, t.UUID)
	switch {
	case errors.Is(err, wings.ErrMissingPermission):
		return apierr.Forbidden("You do not have permission to connect to this server's websocket.")
	case err != nil:
		return apierr.Internal(err)
	}
	noStore(c)
	return ok(c, map[string]any{"data": websocketData{Token: token, Socket: node.WebsocketURL(t.UUID)}})
}

// consoleNode is the node whose console a user connects to. While a server is being moved only someone who may read the transfer
// log gets in, and once the files have left the old node the console is on the new one.
func consoleNode(t Target, held []string) (wings.Node, error) {
	if t.Transfer == nil {
		return t.Node, nil
	}
	if !permissions.Has(held, permissions.AdminWebsocketTransfer) {
		return wings.Node{}, apierr.Forbidden("You do not have permission to view server transfer logs.")
	}
	if t.Transfer.Archived {
		return t.Transfer.NewNode, nil
	}
	return t.Node, nil
}
