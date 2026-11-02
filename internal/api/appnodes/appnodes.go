// Package appnodes serves the nodes of the Application API: listing and looking them up, making one, changing its settings, deleting it,
// the configuration file its Wings needs, and the nodes a new server could be put on. The rules are in internal/nodes and
// internal/servers; this package puts them in order and talks to the storage and to the nodes through the interfaces below.
package appnodes

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/nodes"
	"github.com/Helles01/raptor-panel/internal/servers"
	"github.com/Helles01/raptor-panel/internal/wings"
)

// ErrNoLocation is what the store answers when the location of a node is not there: a 422 for location_id.
var ErrNoLocation = errors.New("appnodes: no such location")

// CreateRequest is a node to store. The token is stored the way the panel keeps secrets; the store never returns it in a record.
type CreateRequest struct {
	UUID        string
	Settings    nodes.Settings
	Credentials nodes.Credentials
}

// Candidate is a node with what its servers have been given, for finding a place for a new server.
type Candidate struct {
	Usage servers.NodeUsage
	Node  appkit.Node
}

// Store is the part of the storage the routes need.
type Store interface {
	// List returns a page and the total. load names the relations to fill (see appkit.Includes).
	List(ctx context.Context, q listquery.Query, page, perPage int, load appkit.Includes) ([]appkit.Node, int, error)
	// Find returns a node, or appkit.ErrNotFound.
	Find(ctx context.Context, id uint64, load appkit.Includes) (appkit.Node, error)
	// Create stores the node, or answers ErrNoLocation.
	Create(ctx context.Context, r CreateRequest) (appkit.Node, error)
	// Update stores the new settings, and the new credentials if there are some. It answers ErrNoLocation or appkit.ErrNotFound.
	Update(ctx context.Context, id uint64, s nodes.Settings, creds *nodes.Credentials) (appkit.Node, error)
	// Delete removes the node. check is called with the number of servers on it, inside the transaction in which they are counted; its
	// error ends the request and is returned as it is.
	Delete(ctx context.Context, id uint64, check func(servers int64) error) error
	// Config gathers what the configuration file of the node needs: its settings, the paths mounted into its servers, and its
	// credentials opened.
	Config(ctx context.Context, id uint64) (nodes.WingsNode, nodes.Credentials, error)
	// Connection is how to reach the node as it is stored now, with its token opened.
	Connection(ctx context.Context, id uint64) (wings.Node, error)
	// Candidates lists every node with the memory and disk its servers hold.
	Candidates(ctx context.Context) ([]Candidate, error)
}

// Daemon is the calls to a node that these routes make. NewDaemon is the one that talks to a real Wings.
type Daemon interface {
	UpdateConfiguration(ctx context.Context, n wings.Node, config []byte) error
}

// Deps are everything the endpoints need.
type Deps struct {
	Store  Store
	Daemon Daemon
	// BaseURL is the address of the panel: the links in answers, and the "remote" of the configuration of a node.
	BaseURL string
	Logger  *slog.Logger
	// Random defaults to crypto/rand.
	Random io.Reader
}

// Handlers are the endpoints.
type Handlers struct {
	d Deps
}

// New checks the dependencies and returns the endpoints.
func New(d Deps) (*Handlers, error) {
	if d.Store == nil || d.Daemon == nil || d.BaseURL == "" || d.Logger == nil {
		return nil, errors.New("appnodes: the store, the daemon, the panel's address and a logger are required")
	}
	if d.Random == nil {
		d.Random = rand.Reader
	}
	return &Handlers{d: d}, nil
}

// Register adds the routes below /api/application/nodes. The configuration holds the node's token, so it needs the right to write.
func (h *Handlers) Register(g api.Group) {
	r := g.Sub("/api/application/nodes")
	read, write := api.ApplicationKey("nodes", auth.AccessRead), api.ApplicationKey("nodes", auth.AccessReadWrite)
	r.GET("", h.List, read)
	r.GET("/deployable", h.Deployable, read)
	r.GET("/:node", h.View, read)
	r.GET("/:node/configuration", h.Configuration, write)
	r.POST("", h.Create, write)
	r.PATCH("/:node", h.Update, write)
	r.DELETE("/:node", h.Delete, write)
}

func noStore(c echo.Context) { c.Response().Header().Set("Cache-Control", "no-store") }
