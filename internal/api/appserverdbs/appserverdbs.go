// Package appserverdbs serves the databases of a server in the Application API: listing them, making one on a database host the
// administrator names, changing its password and deleting it. The rules and the SQL are in internal/dbhosts; this package puts them in
// order and talks to the storage and the database hosts through the interfaces below. The databases a server's owner manages are in
// internal/api/databaseapi.
package appserverdbs

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/field"
	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/api/resp"
	"github.com/Helles01/raptor-panel/internal/api/validate"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/dbhosts"
)

// The store answers with these.
var (
	// ErrNameTaken: the server has a database of this name already.
	ErrNameTaken = errors.New("appserverdbs: database name taken")
	// ErrNoHost: the database host is not there.
	ErrNoHost = errors.New("appserverdbs: no such database host")
)

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
	// Exists answers appkit.ErrNotFound unless the server is there.
	Exists(ctx context.Context, serverID uint64) error
	// List and Find fill the relations named in load (see appkit.Includes): password opens the sealed password, host adds the host.
	List(ctx context.Context, serverID uint64, load appkit.Includes) ([]appkit.ServerDatabase, error)
	// Find returns one of the server's own databases, or appkit.ErrNotFound.
	Find(ctx context.Context, serverID, databaseID uint64, load appkit.Includes) (appkit.ServerDatabase, error)
	// Create stores the database, or answers ErrNameTaken or ErrNoHost.
	Create(ctx context.Context, r CreateRequest) (appkit.ServerDatabase, error)
	// SetPassword stores a new password (sealed).
	SetPassword(ctx context.Context, databaseID uint64, password string) error
	Delete(ctx context.Context, serverID, databaseID uint64) error
}

// Hosts runs statements on a database host.
type Hosts interface {
	// Run executes the statements in order and stops at the first that fails.
	Run(ctx context.Context, hostID uint64, statements []dbhosts.Statement) error
}

// Deps are everything the endpoints need.
type Deps struct {
	Store Store
	Hosts Hosts
	// Enabled is the panel's switch for creating databases: it applies to the Application API as well, as in the reference.
	Enabled bool
	// BaseURL is the address of the panel, for the links in answers.
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
	if d.Store == nil || d.Hosts == nil || d.BaseURL == "" || d.Logger == nil {
		return nil, errors.New("appserverdbs: the store, the database hosts, the panel's address and a logger are required")
	}
	if d.Random == nil {
		d.Random = rand.Reader
	}
	return &Handlers{d: d}, nil
}

// Register adds the routes below /api/application/servers/{server}/databases.
func (h *Handlers) Register(g api.Group) {
	r := g.Sub("/api/application/servers/:server/databases")
	read, write := api.ApplicationKey("server_databases", auth.AccessRead), api.ApplicationKey("server_databases", auth.AccessReadWrite)
	r.GET("", h.List, read)
	r.GET("/:database", h.View, read)
	r.POST("", h.Create, write)
	r.POST("/:database/reset-password", h.ResetPassword, write)
	r.DELETE("/:database", h.Delete, write)
}

// ids reads the {server} and, when the route has one, the {database} of a route, and checks that the server is there.
func (h *Handlers) ids(c echo.Context, withDatabase bool) (server, database uint64, err error) {
	if server, err = appkit.ID(c, "server"); err != nil {
		return 0, 0, err
	}
	if withDatabase {
		if database, err = appkit.ID(c, "database"); err != nil {
			return 0, 0, err
		}
	}
	if err := h.d.Store.Exists(c.Request().Context(), server); err != nil {
		return 0, 0, appkit.StoreError(err)
	}
	return server, database, nil
}

func noStore(c echo.Context) { c.Response().Header().Set("Cache-Control", "no-store") }

// List is GET /servers/{server}/databases: all of them, without paging, as in the reference.
func (h *Handlers) List(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	server, _, err := h.ids(c, false)
	if err != nil {
		return err
	}
	v := appkit.NewView(p)
	inc := appkit.Wanted(c.QueryParams(), listquery.ServerDatabases)
	list, err := h.d.Store.List(c.Request().Context(), server, inc.Load(v, listquery.ServerDatabases))
	if err != nil {
		return appkit.StoreError(err)
	}
	items := make([]resp.Item, len(list))
	for i, d := range list {
		items[i] = v.ServerDatabase(d, inc)
	}
	if inc["password"] {
		noStore(c)
	}
	return c.JSON(http.StatusOK, resp.NewList(items))
}

// View is GET /servers/{server}/databases/{database}. The database is looked for among the server's own.
func (h *Handlers) View(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	server, id, err := h.ids(c, true)
	if err != nil {
		return err
	}
	v := appkit.NewView(p)
	inc := appkit.Wanted(c.QueryParams(), listquery.ServerDatabases)
	d, err := h.d.Store.Find(c.Request().Context(), server, id, inc.Load(v, listquery.ServerDatabases))
	if err != nil {
		return appkit.StoreError(err)
	}
	if inc["password"] {
		noStore(c)
	}
	return c.JSON(http.StatusOK, v.ServerDatabase(d, inc))
}

type createBody struct {
	Database field.String `json:"database"`
	Remote   field.String `json:"remote"`
	Host     field.Int    `json:"host"`
}

var (
	alphaDash  = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	remoteForm = regexp.MustCompile(`^[0-9%.]{1,15}$`)
)

// isRule is true for the refusals of the rules of making a database, which the reference shows as a message to the person.
func isRule(err error) bool {
	return errors.Is(err, dbhosts.ErrFeatureDisabled) || errors.Is(err, dbhosts.ErrTooMany)
}

// Create is POST /servers/{server}/databases: the database, its user and its rights are made on the host the request names. The
// answer holds the password only if it asks for it with ?include=password; it is the one way to learn the password of a new database.
// If the host cannot be told, the row is taken back and what was made on the host is dropped.
func (h *Handlers) Create(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	server, _, err := h.ids(c, false)
	if err != nil {
		return err
	}
	var body createBody
	if err := validate.DecodeJSON(c.Request().Body, &body, 16<<10); err != nil {
		return err
	}
	v := validate.New()
	v.Sent("database", body.Database.State)
	v.Type("database", body.Database.State, validate.KindString, false)
	if body.Database.State == field.Valid {
		v.Length("database", body.Database.V, 3, 48)
		if !alphaDash.MatchString(body.Database.V) {
			v.Custom("database", "alpha_dash", "The database may only contain letters, numbers, dashes and underscores.")
		}
	}
	v.Sent("remote", body.Remote.State)
	v.Type("remote", body.Remote.State, validate.KindString, false)
	if body.Remote.State == field.Valid && !remoteForm.MatchString(body.Remote.V) {
		v.Custom("remote", "regex", "The remote format is invalid.")
	}
	v.Sent("host", body.Host.State)
	v.Type("host", body.Host.State, validate.KindInteger, false)
	if err := v.Err(); err != nil {
		return err
	}

	ctx := c.Request().Context()
	name, err := dbhosts.UniqueName(body.Database.V, server)
	if err != nil {
		return invalid("database", "The database is not valid.")
	}
	username, err := dbhosts.Username(server, h.d.Random)
	if err != nil {
		return apierr.Internal(err)
	}
	password, err := dbhosts.Password(h.d.Random)
	if err != nil {
		return apierr.Internal(err)
	}
	var hostID uint64
	if body.Host.V > 0 {
		hostID = uint64(body.Host.V)
	}
	rec, err := h.d.Store.Create(ctx, CreateRequest{
		Database: NewDatabase{ServerID: server, HostID: hostID, Name: name, Username: username, Password: password, Remote: body.Remote.V},
		Check:    func(limit *int64, count int64) error { return dbhosts.CheckCanCreate(h.d.Enabled, limit, count) },
	})
	switch {
	case errors.Is(err, ErrNameTaken):
		return invalid("database", "The database has already been taken.")
	case errors.Is(err, ErrNoHost):
		return invalid("host", "The selected host is invalid.")
	case isRule(err):
		return apierr.Display(err.Error())
	case err != nil:
		return appkit.StoreError(err)
	}

	account := account(server, rec)
	statements, err := dbhosts.Create(account, password)
	if err == nil {
		err = h.d.Hosts.Run(ctx, rec.HostID, statements)
	}
	if err != nil {
		h.undo(c, server, rec)
		return apierr.Internal(fmt.Errorf("create database on host: %w", err))
	}

	view := appkit.NewView(p)
	inc := appkit.Wanted(c.QueryParams(), listquery.ServerDatabases)
	if inc["password"] {
		rec.Password = &password
		noStore(c)
	}
	return appkit.Created(c, view.ServerDatabase(rec, inc), h.d.BaseURL, fmt.Sprintf("/api/application/servers/%d/databases/%d", server, rec.ID))
}

func invalid(name, detail string) error {
	v := validate.New()
	v.Custom(name, "invalid", detail)
	return v.Err()
}

// account is the part of a database that the SQL builders need.
func account(server uint64, d appkit.ServerDatabase) dbhosts.Database {
	return dbhosts.Database{ServerID: server, Name: d.Name, Username: d.Username, Remote: d.Remote, MaxConnections: d.MaxConnections}
}

// undo drops what a failed creation may have left on the host and takes the row back. The first failure is what the person is told, so
// a failure here is only logged.
func (h *Handlers) undo(c echo.Context, server uint64, rec appkit.ServerDatabase) {
	ctx := c.Request().Context()
	if drop, err := dbhosts.Drop(account(server, rec)); err == nil {
		if err := h.d.Hosts.Run(ctx, rec.HostID, drop); err != nil {
			h.d.Logger.Error("a database that could not be made was not dropped on the host", "server", server, "database", rec.Name, "error", err)
		}
	}
	if err := h.d.Store.Delete(ctx, server, rec.ID); err != nil {
		h.d.Logger.Error("a database that could not be made was not taken back", "server", server, "database", rec.Name, "error", err)
	}
}

// ResetPassword is POST /servers/{server}/databases/{database}/reset-password: a new password, which is not part of the answer; ask for it
// with ?include=password. It is stored first and set on the host after; if the host refuses, the old one is put back.
func (h *Handlers) ResetPassword(c echo.Context) error {
	if _, err := appkit.Caller(c); err != nil {
		return err
	}
	server, id, err := h.ids(c, true)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	rec, err := h.d.Store.Find(ctx, server, id, appkit.Includes{"password": true})
	if err != nil {
		return appkit.StoreError(err)
	}
	if rec.Password == nil {
		return apierr.Internal(errors.New("the store did not open the password of a database"))
	}
	old := *rec.Password
	password, err := dbhosts.Password(h.d.Random)
	if err != nil {
		return apierr.Internal(err)
	}
	statements, err := dbhosts.ChangePassword(account(server, rec), password)
	if err != nil {
		return apierr.Internal(err)
	}
	if err := h.d.Store.SetPassword(ctx, id, password); err != nil {
		return appkit.StoreError(err)
	}
	if err := h.d.Hosts.Run(ctx, rec.HostID, statements); err != nil {
		if undo := h.d.Store.SetPassword(ctx, id, old); undo != nil {
			h.d.Logger.Error("the old password of a database was not put back after the host refused the new one", "server", server, "database", rec.Name, "error", undo)
		}
		return apierr.Internal(fmt.Errorf("change password on host: %w", err))
	}
	return c.NoContent(http.StatusNoContent)
}

// Delete is DELETE /servers/{server}/databases/{database}: dropped on the host first, then the row; if the host refuses the row stays and
// the request can be made again.
func (h *Handlers) Delete(c echo.Context) error {
	if _, err := appkit.Caller(c); err != nil {
		return err
	}
	server, id, err := h.ids(c, true)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	rec, err := h.d.Store.Find(ctx, server, id, nil)
	if err != nil {
		return appkit.StoreError(err)
	}
	statements, err := dbhosts.Drop(account(server, rec))
	if err != nil {
		return apierr.Internal(err)
	}
	if err := h.d.Hosts.Run(ctx, rec.HostID, statements); err != nil {
		return apierr.Internal(fmt.Errorf("drop database on host: %w", err))
	}
	if err := h.d.Store.Delete(ctx, server, id); err != nil {
		return appkit.StoreError(err)
	}
	return c.NoContent(http.StatusNoContent)
}
