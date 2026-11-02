// Package controlapi serves the routes of a server that act on its node: power signals, console commands, the live resource use and
// the token for the console of the browser. The rules are in internal/permissions and internal/wings; this package puts them in
// order, and talks to the storage, the node and the activity log through the interfaces below.
package controlapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/permissions"
	"github.com/Helles01/raptor-panel/internal/wings"
)

// ErrNotFound is what a store answers when the record is gone, for instance deleted since the authorizer found it.
var ErrNotFound = errors.New("controlapi: not found")

// Transfer is the move of a server to another node, as far as the console is concerned.
type Transfer struct {
	// Archived is true once the files have been taken from the old node; the console is then on the new one.
	Archived bool
	NewNode  wings.Node
}

// Target is how to reach a server: its UUID and the node it runs on, with the node's token opened for use.
type Target struct {
	UUID string
	Node wings.Node
	// Transfer is set while the server is being moved.
	Transfer *Transfer
}

// Targets finds the node of a server.
type Targets interface {
	// For returns the target of the server with the row number, or ErrNotFound.
	For(ctx context.Context, serverID uint64) (Target, error)
}

// Daemon is the calls to a node that these routes make. NewDaemon is the one that talks to a real Wings.
type Daemon interface {
	Power(ctx context.Context, n wings.Node, serverUUID, action string) error
	Command(ctx context.Context, n wings.Node, serverUUID, command string) error
	Details(ctx context.Context, n wings.Node, serverUUID string) (wings.Details, error)
}

// Tokens makes the token a browser connects to the console of a node with. *wings.Issuer is one.
type Tokens interface {
	Websocket(n wings.Node, who wings.Subject, serverUUID string) (string, error)
}

// Entry is an entry of the activity log about a server.
type Entry struct {
	Event string
	// ServerID is the server the entry is about; ActorID the user who did it. APIKeyID is set when the call came with an API key.
	ServerID uint64
	ActorID  uint64
	APIKeyID *uint64
	IP       string
	// Properties is the JSON object from activity.Properties.
	Properties string
}

// ActivityLog stores activity entries.
type ActivityLog interface {
	Log(ctx context.Context, e Entry) error
}

// The throttle of the console token: five a minute for each server, whoever asks, as in the reference.
const (
	WebsocketLimit  = 5
	WebsocketWindow = time.Minute
)

// resourcesTTL is how long the resource use of a server is kept, so that a page that asks again and again does not make the node
// answer again and again.
const resourcesTTL = 20 * time.Second

// Deps are everything the endpoints need.
type Deps struct {
	Targets  Targets
	Daemon   Daemon
	Tokens   Tokens
	Activity ActivityLog
	// WebsocketLimiter allows WebsocketLimit calls in WebsocketWindow for a server.
	WebsocketLimiter *middleware.Limiter
	Logger           *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
}

// Handlers are the endpoints.
type Handlers struct {
	d     Deps
	cache *detailsCache
}

// New checks the dependencies and returns the endpoints.
func New(d Deps) (*Handlers, error) {
	if d.Targets == nil || d.Daemon == nil || d.Tokens == nil || d.Activity == nil {
		return nil, errors.New("controlapi: the target store, the daemon, the token maker and the activity log are required")
	}
	if d.WebsocketLimiter == nil || d.Logger == nil {
		return nil, errors.New("controlapi: the websocket limiter and a logger are required")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Handlers{d: d, cache: newDetailsCache(resourcesTTL, d.Now)}, nil
}

// Register adds the routes below /api/client/servers/{server}. The group must already have the session loaded.
func (h *Handlers) Register(g api.Group) {
	s := g.Sub("/api/client/servers/:server")
	// The console may also be opened by an administrator on a server that is installing, restoring or being moved.
	s.GET("/websocket", h.Websocket, api.ServerPermissionIn(permissions.WebsocketConnect, api.StateAdminConsole))
	s.GET("/resources", h.Resources, api.ServerPermission(""))
	s.POST("/command", h.Command, api.ServerPermission(permissions.ControlConsole))
	// The permission of a power signal is that of the signal in the body, so the handler asks for it once it has read the body.
	s.POST("/power", h.Power, api.ServerPermission(""))
}

// maxBody is the largest request body these routes read: a command is at most 4096 bytes, which can be six times that in JSON.
const maxBody = 64 << 10

// caller returns who is calling and the server of the route. A route wired without the authorizer, or without a server in its path,
// has neither; it is refused rather than served.
func caller(c echo.Context) (authz.Principal, error) {
	p, ok := authz.From(c)
	if !ok || p.Server == nil {
		return authz.Principal{}, apierr.Forbidden("This action is unauthorized.")
	}
	return p, nil
}

// target finds the node of the server of the route.
func (h *Handlers) target(c echo.Context, p authz.Principal) (Target, error) {
	t, err := h.d.Targets.For(c.Request().Context(), p.Server.ID)
	switch {
	case errors.Is(err, ErrNotFound):
		return Target{}, apierr.NotFound()
	case err != nil:
		return Target{}, apierr.Internal(err)
	}
	return t, nil
}

// DaemonError turns a failed call to a node into the API's answer: the status and the text the node's client worked out, which are
// safe to show, with the cause kept for the log. Anything else is an unexpected failure.
func DaemonError(err error) error {
	var we *wings.Error
	if errors.As(err, &we) {
		return apierr.Daemon(we.Status, we.Message, err)
	}
	return apierr.Internal(err)
}

// record writes an entry about the server of the route. A failure is logged and never reaches the person, whose action has happened.
func (h *Handlers) record(c echo.Context, p authz.Principal, event, properties string) {
	e := Entry{Event: event, ServerID: p.Server.ID, ActorID: p.User.ID, IP: c.RealIP(), Properties: properties}
	if p.Key != nil {
		id := p.Key.ID
		e.APIKeyID = &id
	}
	if err := h.d.Activity.Log(c.Request().Context(), e); err != nil {
		h.d.Logger.Error("activity entry not stored", "event", event, "server", p.Server.ID, "error", err)
	}
}

func noStore(c echo.Context) { c.Response().Header().Set("Cache-Control", "no-store") }

func ok(c echo.Context, body any) error { return c.JSON(http.StatusOK, body) }
