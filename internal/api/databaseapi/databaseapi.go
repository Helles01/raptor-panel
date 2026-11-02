// Package databaseapi serves the routes with which a user manages the databases of their server on the database hosts the panel is
// connected to: listing them, creating one, changing its password and deleting it. The rules and the SQL are in internal/dbhosts; this
// package puts them in order and talks to the storage, the hosts and the activity log through the interfaces below.
package databaseapi

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/dbhosts"
	"github.com/Helles01/raptor-panel/internal/permissions"
)

// The store answers with these.
var (
	// ErrNotFound: there is no such database on the server.
	ErrNotFound = errors.New("databaseapi: not found")
	// ErrNameTaken: the server has a database of this name already.
	ErrNameTaken = errors.New("databaseapi: database name taken")
)

// Record is a database of a server as stored, with the address of its host.
type Record struct {
	ID          uint64
	HostID      uint64
	HostAddress string
	HostPort    int
	// Name, Username and Remote are the database, the user of it and the address the user may connect from.
	Name           string
	Username       string
	Remote         string
	MaxConnections *int
}

// Account is the part of a record that the SQL builders need.
func (r Record) account(serverID uint64) dbhosts.Database {
	max := 0
	if r.MaxConnections != nil {
		max = *r.MaxConnections
	}
	return dbhosts.Database{ServerID: serverID, Name: r.Name, Username: r.Username, Remote: r.Remote, MaxConnections: max}
}

// NewDatabase is a database to store, with its password in the clear: the store seals it.
type NewDatabase struct {
	ServerID uint64
	HostID   uint64
	Name     string
	Username string
	Password string
	Remote   string
}

// CreateRequest asks the store to add a database in one transaction in which the server's databases are locked.
type CreateRequest struct {
	Database NewDatabase
	// Check is called with the server's database limit and how many databases it has; an error ends the request and is returned as it
	// is.
	Check func(limit *int64, count int64) error
}

// Store is the part of the storage the routes need. Every method works on the databases of the one server.
type Store interface {
	List(ctx context.Context, serverID uint64) ([]Record, error)
	// Find returns one of the server's own databases, or ErrNotFound.
	Find(ctx context.Context, serverID, databaseID uint64) (Record, error)
	// Password opens the sealed password of a database.
	Password(ctx context.Context, databaseID uint64) (string, error)
	// Hosts lists the database hosts a database may be made on.
	Hosts(ctx context.Context) ([]dbhosts.HostChoice, error)
	// Create stores the database, or answers ErrNameTaken.
	Create(ctx context.Context, r CreateRequest) (Record, error)
	// SetPassword stores a new password (sealed).
	SetPassword(ctx context.Context, databaseID uint64, password string) error
	Delete(ctx context.Context, serverID, databaseID uint64) error
}

// Hosts runs statements on a database host.
type Hosts interface {
	// Run executes the statements in order and stops at the first that fails.
	Run(ctx context.Context, hostID uint64, statements []dbhosts.Statement) error
}

// Entry is an entry of the activity log about a server and a database.
type Entry struct {
	Event string
	// ServerID is the server and DatabaseID the database the entry is about; ActorID is the user who did it.
	ServerID   uint64
	DatabaseID uint64
	ActorID    uint64
	APIKeyID   *uint64
	IP         string
	Properties string
}

// ActivityLog stores activity entries.
type ActivityLog interface {
	Log(ctx context.Context, e Entry) error
}

// The throttle of creating databases: two a minute for each server, whoever creates, as in the reference.
const (
	CreateLimit  = 2
	CreateWindow = time.Minute
)

// Deps are everything the endpoints need.
type Deps struct {
	Store    Store
	Hosts    Hosts
	Activity ActivityLog
	// CreateLimiter allows CreateLimit databases in CreateWindow for a server.
	CreateLimiter *middleware.Limiter
	// Enabled is the panel's switch for users making databases; AllowRandom lets a server get a database on a host that is not linked
	// to its node when none is.
	Enabled     bool
	AllowRandom bool
	Logger      *slog.Logger
	// Random defaults to crypto/rand.
	Random io.Reader
}

// Handlers are the endpoints.
type Handlers struct {
	d Deps
}

// New checks the dependencies and returns the endpoints.
func New(d Deps) (*Handlers, error) {
	if d.Store == nil || d.Hosts == nil || d.Activity == nil || d.CreateLimiter == nil || d.Logger == nil {
		return nil, errors.New("databaseapi: the store, the hosts, the activity log, the limiter and a logger are required")
	}
	if d.Random == nil {
		d.Random = rand.Reader
	}
	return &Handlers{d: d}, nil
}

// Register adds the routes below /api/client/servers/{server}/databases.
func (h *Handlers) Register(g api.Group) {
	r := g.Sub("/api/client/servers/:server/databases")
	r.GET("", h.List, api.ServerPermission(permissions.DatabaseRead))
	r.POST("", h.Create, api.ServerPermission(permissions.DatabaseCreate))
	r.POST("/:database/rotate-password", h.RotatePassword, api.ServerPermission(permissions.DatabaseUpdate))
	r.DELETE("/:database", h.Delete, api.ServerPermission(permissions.DatabaseDelete))
}

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

// databaseID reads the {database} of the route. The reference shows an obscured form of the row number (C-061); Raptor shows the number
// itself, which is no secret since a database is always looked up among the server's own.
func databaseID(c echo.Context) (uint64, error) {
	id, err := strconv.ParseUint(c.Param("database"), 10, 64)
	if err != nil || id == 0 {
		return 0, apierr.NotFound()
	}
	return id, nil
}
