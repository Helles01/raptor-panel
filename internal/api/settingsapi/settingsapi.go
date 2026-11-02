// Package settingsapi serves the routes with which a user changes their own server: the startup variables, the Docker image, the name
// and the description, and the reinstallation. The rules are in internal/servers (settings.go) and internal/eggrules; this package puts
// them in order and talks to the storage, the node and the activity log through the interfaces below.
package settingsapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/clientapi"
	"github.com/Helles01/raptor-panel/internal/permissions"
)

// ErrNotFound is what the store answers when the server is gone.
var ErrNotFound = errors.New("settingsapi: not found")

// Image is a Docker image an egg offers, with the name it is shown under.
type Image struct{ Label, Image string }

// Images are the images of an egg. Their JSON form is an object from the label to the image, in the order of the egg, as the
// frontend shows them.
type Images []Image

// MarshalJSON writes the object, keeping the order.
func (im Images) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, x := range im {
		if i > 0 {
			b.WriteByte(',')
		}
		label, _ := json.Marshal(x.Label)
		image, _ := json.Marshal(x.Image)
		b.Write(label)
		b.WriteByte(':')
		b.Write(image)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func (im Images) values() []string {
	out := make([]string, len(im))
	for i, x := range im {
		out[i] = x.Image
	}
	return out
}

// Server is a server with what these routes need to know about it: the server as the client API sees it (its limits, startup
// command, image, variables, status) and the images of its egg.
type Server struct {
	clientapi.Server
	EggImages Images
}

// Store is the part of the storage the routes need.
type Store interface {
	// Server returns a server with its variables, or ErrNotFound.
	Server(ctx context.Context, id uint64) (Server, error)
	// SetVariable stores the value of a variable of the server, an empty string for none.
	SetVariable(ctx context.Context, serverID uint64, envName, value string) error
	SetImage(ctx context.Context, serverID uint64, image string) error
	Rename(ctx context.Context, serverID uint64, name, description string) error
	// SetStatus changes the status of the server, only if it is still from, and says whether it did. Empty is the normal state.
	SetStatus(ctx context.Context, serverID uint64, from, to string) (bool, error)
}

// Reinstaller tells the node of a server to run its install script again.
type Reinstaller interface {
	Reinstall(ctx context.Context, serverID uint64, serverUUID string) error
}

// Entry is an entry of the activity log about a server.
type Entry struct {
	Event string
	// ServerID is the server the entry is about; ActorID the user who did it. APIKeyID is set when the call came with an API key.
	ServerID   uint64
	ActorID    uint64
	APIKeyID   *uint64
	IP         string
	Properties string
}

// ActivityLog stores activity entries.
type ActivityLog interface {
	Log(ctx context.Context, e Entry) error
}

// Deps are everything the endpoints need.
type Deps struct {
	Store       Store
	Reinstaller Reinstaller
	Activity    ActivityLog
	Logger      *slog.Logger
}

// Handlers are the endpoints.
type Handlers struct {
	d Deps
}

// New checks the dependencies and returns the endpoints.
func New(d Deps) (*Handlers, error) {
	if d.Store == nil || d.Reinstaller == nil || d.Activity == nil || d.Logger == nil {
		return nil, errors.New("settingsapi: the store, the reinstaller, the activity log and a logger are required")
	}
	return &Handlers{d: d}, nil
}

// Register adds the routes below /api/client/servers/{server}.
func (h *Handlers) Register(g api.Group) {
	s := g.Sub("/api/client/servers/:server")
	s.GET("/startup", h.Startup, api.ServerPermission(permissions.StartupRead))
	s.PUT("/startup/variable", h.SetVariable, api.ServerPermission(permissions.StartupUpdate))
	s.POST("/settings/rename", h.Rename, api.ServerPermission(permissions.SettingsRename))
	s.PUT("/settings/docker-image", h.SetImage, api.ServerPermission(permissions.StartupDockerImage))
	// A server whose first installation failed can be installed again by its owner.
	s.POST("/settings/reinstall", h.Reinstall, api.ServerPermissionIn(permissions.SettingsReinstall, api.StateReinstall))
}

// maxBody is the largest body these routes read: a variable value is short, an image and a name shorter.
const maxBody = 64 << 10

// caller returns who is calling and the server of the route; a route wired without them is refused.
func caller(c echo.Context) (authz.Principal, error) {
	p, ok := authz.From(c)
	if !ok || p.Server == nil {
		return authz.Principal{}, apierr.Forbidden("This action is unauthorized.")
	}
	return p, nil
}

func storeError(err error) error {
	if errors.Is(err, ErrNotFound) {
		return apierr.NotFound()
	}
	return apierr.Internal(err)
}

// log writes an entry about the server of the route. A failure is logged and never reaches the person, whose action has happened.
func (h *Handlers) log(c echo.Context, p authz.Principal, event, properties string) {
	e := Entry{Event: event, ServerID: p.Server.ID, ActorID: p.User.ID, IP: c.RealIP(), Properties: properties}
	if p.Key != nil {
		id := p.Key.ID
		e.APIKeyID = &id
	}
	if err := h.d.Activity.Log(c.Request().Context(), e); err != nil {
		h.d.Logger.Error("activity entry not stored", "event", event, "server", p.Server.ID, "error", err)
	}
}

func noContent(c echo.Context) error { return c.NoContent(http.StatusNoContent) }
