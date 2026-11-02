// Package subuserapi serves the routes that list, add, change and remove the subusers of a server. The rules are in internal/servers
// (who may change whom), internal/permissions (what is stored) and internal/users (the account made for an address that has none);
// this package puts them in order and talks to the storage, the mail and the activity log through the interfaces below.
package subuserapi

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/clientapi"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/api/resp"
	"github.com/Helles01/raptor-panel/internal/mail"
	"github.com/Helles01/raptor-panel/internal/permissions"
	"github.com/Helles01/raptor-panel/internal/users"
)

// The store answers with these.
var (
	// ErrNotFound: there is no such subuser or user.
	ErrNotFound = errors.New("subuserapi: not found")
	// ErrExists: the user is a subuser of the server already (another request was first).
	ErrExists = errors.New("subuserapi: already a subuser")
	// ErrUsernameTaken: the generated user name is in use; another one is tried.
	ErrUsernameTaken = errors.New("subuserapi: user name taken")
	// ErrEmailTaken: an account was made for the address meanwhile; the address is looked up again.
	ErrEmailTaken = errors.New("subuserapi: e-mail address taken")
)

// UserRef is an existing user, as far as adding them goes.
type UserRef struct {
	ID       uint64
	UUID     string
	Username string
}

// Subuser is a user with access to a server and the permissions they were given.
type Subuser struct {
	UserID      uint64
	User        clientapi.Person
	Permissions []string
}

// AddRequest adds a subuser. With NewUser set, the account is made in the same transaction, with a token to set its password (the
// hash of SetupTokenHash) stored for its address.
type AddRequest struct {
	ServerID       uint64
	UserID         uint64
	NewUser        *users.Record
	SetupTokenHash string
	Permissions    []string
}

// Store is the part of the storage the routes need.
type Store interface {
	List(ctx context.Context, serverID uint64) ([]Subuser, error)
	// Find returns the subuser of the server with the user UUID, or ErrNotFound.
	Find(ctx context.Context, serverID uint64, userUUID string) (Subuser, error)
	// UserByEmail finds a user by address, which the database compares without regard to case, or ErrNotFound.
	UserByEmail(ctx context.Context, email string) (UserRef, error)
	IsSubuser(ctx context.Context, serverID, userID uint64) (bool, error)
	// Add stores the subuser (and the account) in one transaction. It answers ErrExists, ErrUsernameTaken or ErrEmailTaken.
	Add(ctx context.Context, r AddRequest) (Subuser, error)
	// SetPermissions stores new permissions. With revoke the store records, in the same transaction, that the tokens and SFTP access
	// of the user on the node are to be revoked (the outbox), so that the change takes effect at once.
	SetPermissions(ctx context.Context, serverID, userID uint64, permissions []string, revoke bool) error
	// Remove deletes the subuser entry, with the same revocation.
	Remove(ctx context.Context, serverID, userID uint64, revoke bool) error
	// ServerName is the name of the server, for the mails.
	ServerName(ctx context.Context, serverID uint64) (string, error)
}

// Entry is an entry of the activity log about a server and a user.
type Entry struct {
	Event string
	// ServerID is the server and SubjectUserID the subuser the entry is about; ActorID is the user who did it.
	ServerID      uint64
	SubjectUserID uint64
	ActorID       uint64
	APIKeyID      *uint64
	IP            string
	Properties    string
}

// ActivityLog stores activity entries.
type ActivityLog interface {
	Log(ctx context.Context, e Entry) error
}

// Outbox takes mail to be sent later.
type Outbox interface {
	Send(ctx context.Context, m mail.Message) error
}

// The throttle of adding subusers: ten in fifteen minutes for each server, whoever adds, as in the reference.
const (
	CreateLimit  = 10
	CreateWindow = 15 * time.Minute
)

// Deps are everything the endpoints need.
type Deps struct {
	Store    Store
	Activity ActivityLog
	Outbox   Outbox
	// CreateLimiter allows CreateLimit additions in CreateWindow for a server.
	CreateLimiter *middleware.Limiter
	Site          mail.Site
	// Location is the time zone of the panel, UTC when nil.
	Location *time.Location
	Logger   *slog.Logger
	// Random defaults to crypto/rand.
	Random io.Reader
}

// Handlers are the endpoints.
type Handlers struct {
	d Deps
}

// New checks the dependencies and returns the endpoints.
func New(d Deps) (*Handlers, error) {
	if d.Store == nil || d.Activity == nil || d.Outbox == nil || d.CreateLimiter == nil || d.Logger == nil {
		return nil, errors.New("subuserapi: the store, the activity log, the outbox, the limiter and a logger are required")
	}
	if d.Location == nil {
		d.Location = time.UTC
	}
	if d.Random == nil {
		d.Random = rand.Reader
	}
	return &Handlers{d: d}, nil
}

// Register adds the routes below /api/client/servers/{server}/users.
func (h *Handlers) Register(g api.Group) {
	u := g.Sub("/api/client/servers/:server/users")
	u.GET("", h.List, api.ServerPermission(permissions.UserRead))
	u.POST("", h.Create, api.ServerPermission(permissions.UserCreate))
	u.GET("/:user", h.View, api.ServerPermission(permissions.UserRead))
	u.POST("/:user", h.Update, api.ServerPermission(permissions.UserUpdate))
	u.DELETE("/:user", h.Delete, api.ServerPermission(permissions.UserDelete))
}

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

func (h *Handlers) item(s Subuser) (resp.Item, error) {
	return clientapi.SubuserItem(s.User, s.Permissions, h.d.Location)
}

// log writes an entry about a subuser. A failure is logged and never reaches the person, whose action has happened.
func (h *Handlers) log(c echo.Context, p authz.Principal, subject uint64, event string, props string) {
	e := Entry{Event: event, ServerID: p.Server.ID, SubjectUserID: subject, ActorID: p.User.ID, IP: c.RealIP(), Properties: props}
	if p.Key != nil {
		id := p.Key.ID
		e.APIKeyID = &id
	}
	if err := h.d.Activity.Log(c.Request().Context(), e); err != nil {
		h.d.Logger.Error("activity entry not stored", "event", event, "server", p.Server.ID, "error", err)
	}
}

// send queues a mail. The change is already made, so a mail that cannot be made or queued is only logged.
func (h *Handlers) send(c echo.Context, what string, m mail.Message, err error) {
	if err == nil {
		err = h.d.Outbox.Send(c.Request().Context(), m)
	}
	if err != nil {
		h.d.Logger.Error("a mail about a subuser was not queued", "mail", what, "error", err)
	}
}

// sorted returns a sorted copy.
func sorted(list []string) []string {
	out := slices.Clone(list)
	slices.Sort(out)
	return out
}

func noContent(c echo.Context) error { return c.NoContent(http.StatusNoContent) }
