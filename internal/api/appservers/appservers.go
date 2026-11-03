// Package appservers serves the servers of the Application API: reading them, making one (on a node the administrator names or one that
// is found for it), changing its details, build and startup, suspending it, reinstalling it and deleting it. The rules are in
// internal/servers, internal/dbhosts and internal/eggrules; this package puts them in order and talks to the storage, the nodes and
// the database hosts through the interfaces below.
package appservers

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/controlapi"
	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/dbhosts"
	"github.com/Helles01/raptor-panel/internal/servers"
	"github.com/Helles01/raptor-panel/internal/wings"
)

// The store answers with these.
var (
	// ErrNoOwner: the user a server is given to is not there (a 422 on user).
	ErrNoOwner = errors.New("appservers: no such user")
	// ErrNoEgg: the egg is not there (a 422 on egg).
	ErrNoEgg = errors.New("appservers: no such egg")
	// ErrBusy: the server was not in the state the change was made from; another request got there first.
	ErrBusy = errors.New("appservers: the server changed meanwhile")
)

// EggInfo is what changing or making a server needs to know about an egg.
type EggInfo struct {
	NestID    uint64
	Variables []servers.EggVariable
}

// BuildChange is the build of a server after a change, decided while its allocations are locked.
type BuildChange struct {
	Limits      servers.Limits
	Features    servers.FeatureLimits
	OOMDisabled bool
	Plan        servers.AllocationPlan
}

// BuildRequest asks the store to change the build of a server in one transaction. The store locks the server and the allocations named
// in Rows, reads them, and calls Change with what it read; an error of Change ends the request and is returned as it is. Otherwise the
// store stores the limits and the default allocation, assigns the allocations of the plan only if they are still free (and refuses the
// whole change if one was taken meanwhile), and releases the others, clearing their notes.
type BuildRequest struct {
	Rows   []uint64
	Change func(cur appkit.Server, rows map[uint64]servers.AllocationRow) (BuildChange, error)
}

// CreateRequest asks the store to add a server in one transaction. The store locks and reads the allocations named in Allocations and
// calls Check with them; its error ends the request. Then it inserts the server (in the installing state, on the node of the primary
// allocation), assigns the allocations only if they are still free and refuses the creation if one is not, and stores the values of the
// variables.
type CreateRequest struct {
	UUID, ShortID string
	Server        servers.NewServer
	NestID        uint64
	OOMDisabled   bool
	SkipScripts   bool
	Values        []servers.VariableValue
	Allocations   []uint64
	Check         func(rows map[uint64]servers.AllocationRow) error
}

// Store is the part of the storage the routes need.
type Store interface {
	// List returns a page and the total. load names the relations to fill (see appkit.Includes).
	List(ctx context.Context, q listquery.Query, page, perPage int, load appkit.Includes) ([]appkit.Server, int, error)
	// Find returns a server, or appkit.ErrNotFound.
	Find(ctx context.Context, id uint64, load appkit.Includes) (appkit.Server, error)
	FindExternal(ctx context.Context, externalID string, load appkit.Includes) (appkit.Server, error)

	// UpdateDetails stores the new details. revoke is the previous owner when the owner changed (else 0): the store records, in the
	// same transaction, that their tokens and SFTP access on the node are to be revoked. It answers ErrNoOwner or an *appkit.TakenError.
	UpdateDetails(ctx context.Context, id uint64, d servers.Details, revoke uint64) (appkit.Server, error)
	UpdateBuild(ctx context.Context, id uint64, r BuildRequest) (appkit.Server, error)
	// Egg returns the nest and the variables of an egg, or ErrNoEgg.
	Egg(ctx context.Context, id uint64) (EggInfo, error)
	// UpdateStartup stores the startup settings (and the nest of the egg) and the values of the variables, in one transaction.
	UpdateStartup(ctx context.Context, id uint64, s servers.StartupSettings, nestID uint64, values []servers.VariableValue) (appkit.Server, error)

	// Usage lists every node with what its servers hold, and Allocations the allocations of nodes, for finding a place for a server.
	Usage(ctx context.Context) ([]servers.NodeUsage, error)
	Allocations(ctx context.Context, nodeIDs []uint64) ([]servers.Candidate, error)
	// Create stores the server, or answers ErrNoOwner, ErrNoEgg or an *appkit.TakenError.
	Create(ctx context.Context, r CreateRequest) (appkit.Server, error)

	// Transferring says whether the server is being moved to another node.
	Transferring(ctx context.Context, id uint64) (bool, error)
	// SetStatus changes the status from one value to another in one statement: false means the server did not have the status.
	SetStatus(ctx context.Context, id uint64, from, to string) (bool, error)
	// Databases lists the databases of a server, DeleteDatabase removes the row of one with its sealed password, and Delete the server, which has
	// none left: its allocations are released with their notes cleared, and its rows go.
	Databases(ctx context.Context, serverID uint64) ([]appkit.ServerDatabase, error)
	DeleteDatabase(ctx context.Context, id uint64) error
	Delete(ctx context.Context, id uint64) error
}

// Daemon is the calls to a node that these routes make. NewDaemon is the one that talks to a real Wings.
type Daemon interface {
	Create(ctx context.Context, n wings.Node, serverUUID string, startOnCompletion bool) error
	Sync(ctx context.Context, n wings.Node, serverUUID string) error
	Reinstall(ctx context.Context, n wings.Node, serverUUID string) error
	Delete(ctx context.Context, n wings.Node, serverUUID string) error
}

// Hosts runs statements on a database host.
type Hosts interface {
	Run(ctx context.Context, hostID uint64, statements []dbhosts.Statement) error
}

// Deps are everything the endpoints need.
type Deps struct {
	Store   Store
	Targets controlapi.Targets
	Daemon  Daemon
	Hosts   Hosts
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
	if d.Store == nil || d.Targets == nil || d.Daemon == nil || d.Hosts == nil || d.Logger == nil {
		return nil, errors.New("appservers: the store, the target store, the daemon, the database hosts and a logger are required")
	}
	if d.Random == nil {
		d.Random = rand.Reader
	}
	return &Handlers{d: d}, nil
}

// Register adds the routes below /api/application/servers.
func (h *Handlers) Register(g api.Group) {
	r := g.Sub("/api/application/servers")
	read, write := api.ApplicationKey("servers", auth.AccessRead), api.ApplicationKey("servers", auth.AccessReadWrite)
	r.GET("", h.List, read)
	r.GET("/external/:external", h.External, read)
	r.GET("/:server", h.View, read)
	r.POST("", h.Create, write)
	r.PATCH("/:server/details", h.Details, write)
	r.PATCH("/:server/build", h.Build, write)
	r.PATCH("/:server/startup", h.Startup, write)
	r.POST("/:server/suspend", h.Suspend, write)
	r.POST("/:server/unsuspend", h.Unsuspend, write)
	r.POST("/:server/reinstall", h.Reinstall, write)
	r.DELETE("/:server", h.Delete, write)
	r.DELETE("/:server/:force", h.Delete, write)
}
