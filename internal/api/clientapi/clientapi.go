// Package clientapi serves the read endpoints of the client API, the one the panel's own frontend uses: the list of the
// signed-in user's servers, the page of one server, the permission tree, the user's own account and the two activity lists.
// The rules are in internal/servers, internal/permissions, internal/activity and internal/api/listquery; this package puts them
// in order and reads the data through the interfaces below, which the database layer fills.
package clientapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/activity"
	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/permissions"
	"github.com/Helles01/raptor-panel/internal/servers"
)

// ErrNotFound is what a store answers when the record is gone, for instance deleted since the authorizer found it.
var ErrNotFound = errors.New("clientapi: not found")

// Person is a user as the client API shows one: the actor of an activity entry, a subuser.
type Person struct {
	UUID      string
	Username  string
	Email     string
	UsesTOTP  bool
	CreatedAt time.Time
}

// Account is the signed-in user's own details.
type Account struct {
	ID        uint64
	RootAdmin bool
	Username  string
	Email     string
	FirstName string
	LastName  string
	Language  string
}

// Node is the node a server runs on.
type Node struct {
	Name        string
	FQDN        string
	SFTPPort    int
	Maintenance bool
}

// Allocation is an address and port of a server. Notes and IPAlias are nil when not set.
type Allocation struct {
	ID      uint64
	IP      string
	IPAlias *string
	Port    int
	Notes   *string
}

// Variable is an egg variable together with the value of one server.
type Variable struct {
	Name        string
	Description string
	EnvName     string
	Default     string
	// ServerValue is the server's own value, nil when it uses the default.
	ServerValue  *string
	UserViewable bool
	UserEditable bool
	Rules        string
}

// Egg is the egg of a server, as far as the client API shows it. Features is what the egg or, if it has none of its own, its
// parent egg lists; nil when neither does.
type Egg struct {
	UUID     string
	Name     string
	Features []string
}

// Server is a server with everything its page shows.
type Server struct {
	ID          uint64
	UUID        string
	ShortID     string
	OwnerID     uint64
	Name        string
	Description string
	// Status is empty in the normal state, otherwise one of the servers.Status* values.
	Status       string
	Transferring bool
	Node         Node

	Memory      int64
	Swap        int64
	Disk        int64
	IO          int64
	CPU         int64
	Threads     *string
	OOMDisabled bool
	SkipScripts bool

	Startup string
	Image   string
	Egg     Egg

	// The limits of the features; nil means no limit.
	DatabaseLimit   *int64
	AllocationLimit *int64
	BackupLimit     *int64

	// Primary is the allocation of the server; Allocations are all of them, the primary one included.
	Primary     Allocation
	Allocations []Allocation
	// Variables are all the egg variables of the server, also those a user may not see: the startup command needs them to hide
	// them.
	Variables []Variable

	// SubuserPermissions are what the viewer of a list holds as a subuser of this server, nil when they are not one. The store
	// fills it in a list; the page of one server takes the permissions from the authorizer.
	SubuserPermissions []string
}

// Subuser is a user who was given access to a server.
type Subuser struct {
	User        Person
	Permissions []string
}

// ServerList is a request for one page of the servers of a user.
type ServerList struct {
	// Viewer is the user whose list it is.
	Viewer uint64
	Scope  servers.ListScope
	// Query holds the filters and sorts the request asked for. The store joins its condition to the scope's.
	Query         listquery.Query
	Page, PerPage int
}

// ServerPage is one page of servers and the number of all that match.
type ServerPage struct {
	Total   int
	Servers []Server
}

// ActivityList is a request for one page of an activity log.
type ActivityList struct {
	Query         listquery.Query
	Page, PerPage int
	// Exclude are the events that no list shows.
	Exclude []string
	// WithActors asks for the user behind each entry.
	WithActors bool
}

// ActivityEntry is one entry and, if asked for, who did it.
type ActivityEntry struct {
	activity.Stored
	// Actor is nil when no user did it, or when the actors were not asked for.
	Actor *Person
}

// ActivityPage is one page of entries and the number of all that match.
type ActivityPage struct {
	Total   int
	Entries []ActivityEntry
}

// Accounts is the part of the user store the endpoints need.
type Accounts interface {
	ByID(ctx context.Context, id uint64) (Account, error)
}

// Servers is the part of the server store the endpoints need.
type Servers interface {
	// List returns one page, ordered as the request says and then by id. A scope of ScopeNone has no servers.
	List(ctx context.Context, l ServerList) (ServerPage, error)
	// One returns a server by its row number, or ErrNotFound.
	One(ctx context.Context, id uint64) (Server, error)
	// Subusers returns the subusers of the servers, by server.
	Subusers(ctx context.Context, serverIDs []uint64) (map[uint64][]Subuser, error)
}

// ActivityLogs is the store of the activity log.
type ActivityLogs interface {
	// ForUser returns the entries whose subject is the user.
	ForUser(ctx context.Context, userID uint64, l ActivityList) (ActivityPage, error)
	// ForServer returns the entries whose subject is the server. With hideAdmins the entries of administrators are left out,
	// unless the administrator is the owner or a subuser of the server.
	ForServer(ctx context.Context, serverID uint64, hideAdmins bool, l ActivityList) (ActivityPage, error)
}

// Deps are everything the endpoints need.
type Deps struct {
	Accounts Accounts
	Servers  Servers
	Activity ActivityLogs

	// Texts says, for every event, which properties its text on the screen shows (activity.ParseTexts of the frontend's
	// activity.json). Without it every property of an entry counts as one that is not shown.
	Texts map[string][]string
	// HideAdminActivity leaves the entries of administrators out of a server's log.
	HideAdminActivity bool
	// Location is the time zone of the panel, UTC when nil.
	Location *time.Location
	Logger   *slog.Logger
}

// Handlers are the endpoints.
type Handlers struct {
	d Deps
}

// New checks the dependencies and returns the endpoints.
func New(d Deps) (*Handlers, error) {
	if d.Accounts == nil || d.Servers == nil || d.Activity == nil {
		return nil, errors.New("clientapi: every store is required")
	}
	if d.Logger == nil {
		return nil, errors.New("clientapi: a logger is required")
	}
	if d.Location == nil {
		d.Location = time.UTC
	}
	return &Handlers{d: d}, nil
}

// Register adds the routes below /api/client. The group must already have the session loaded.
func (h *Handlers) Register(g api.Group) {
	c := g.Sub("/api/client")
	c.GET("", h.Servers, api.Authenticated())
	c.GET("/permissions", h.Permissions, api.Authenticated())
	// The account page is the one a user reaches to switch two-factor authentication on, so it is open without it.
	c.GET("/account", h.Account, api.Authenticated().WithoutTwoFactor())
	c.GET("/account/activity", h.AccountActivity, api.Authenticated())

	// The page of a server is shown whatever the state of the server, so that one that is installing can still be looked at.
	c.GET("/servers/:server", h.Server, api.ServerPermissionIn("", api.StateAny))
	c.GET("/servers/:server/activity", h.ServerActivity, api.ServerPermission(permissions.ActivityRead))
}

// caller returns who is calling. A route wired without the authorizer would have no principal; it is refused rather than served.
func caller(c echo.Context) (authz.Principal, error) {
	p, ok := authz.From(c)
	if !ok {
		return authz.Principal{}, apierr.Forbidden("This action is unauthorized.")
	}
	return p, nil
}

// storeError turns what a store answered into the API's answer: a record that is gone is 404, anything else 500 with the cause
// kept for the log.
func storeError(err error) error {
	if errors.Is(err, ErrNotFound) {
		return apierr.NotFound()
	}
	return apierr.Internal(err)
}

func (h *Handlers) holders(event string) []string { return h.d.Texts[event] }

func ok(c echo.Context, body any) error { return c.JSON(http.StatusOK, body) }
