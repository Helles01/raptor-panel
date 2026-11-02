// Package appusers serves the users of the Application API: listing and looking them up, making an account, changing it and deleting it.
// The rules are in internal/users and internal/auth/passwordreset; this package puts them in order and talks to the storage, the mail
// and the activity log through the interfaces below.
package appusers

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/mail"
	"github.com/Helles01/raptor-panel/internal/users"
)

// CreateRequest asks the store to add an account in one transaction. With a SetupTokenHash the token with which its owner chooses a
// password is stored for the address as well.
type CreateRequest struct {
	User           users.Record
	SetupTokenHash string
}

// Store is the part of the storage the routes need. The unique keys of the users (email, username, external_id) answer
// *appkit.TakenError with the field, which is a 422.
type Store interface {
	List(ctx context.Context, q listquery.Query, page, perPage int, load appkit.Includes) ([]appkit.User, int, error)
	// List, Find and FindExternal fill the relations named in load (see appkit.Includes).
	// Find returns a user, or appkit.ErrNotFound.
	Find(ctx context.Context, id uint64, load appkit.Includes) (appkit.User, error)
	FindExternal(ctx context.Context, externalID string, load appkit.Includes) (appkit.User, error)
	Create(ctx context.Context, r CreateRequest) (appkit.User, error)
	// Update stores the new account. The effects of the change that the database holds (the user's sessions and tokens, the pending
	// reset tokens, the application keys of someone who is no longer an administrator) are carried out in the same transaction.
	Update(ctx context.Context, id uint64, u users.Update, fx users.Effects) (appkit.User, error)
	// Delete removes the user. check is called with the number of servers the user owns, inside the transaction in which they are
	// counted; its error ends the request and is returned as it is.
	Delete(ctx context.Context, id uint64, check func(ownedServers int64) error) error
}

// Entry is an entry of the activity log about a user.
type Entry struct {
	Event string
	// SubjectUserID is the user the entry is about; ActorID the user whose key made the call.
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

// Deps are everything the endpoints need.
type Deps struct {
	Store    Store
	Activity ActivityLog
	Outbox   Outbox
	Site     mail.Site
	// BaseURL is the address of the panel, for the links in answers.
	BaseURL string
	// Languages are the languages an account may use.
	Languages []string
	Logger    *slog.Logger
	// Random defaults to crypto/rand.
	Random io.Reader
}

// Handlers are the endpoints.
type Handlers struct {
	d Deps
}

// New checks the dependencies and returns the endpoints.
func New(d Deps) (*Handlers, error) {
	if d.Store == nil || d.Activity == nil || d.Outbox == nil || d.Logger == nil || d.BaseURL == "" || len(d.Languages) == 0 {
		return nil, errors.New("appusers: the store, the activity log, the outbox, the panel's address, the languages and a logger are required")
	}
	if d.Random == nil {
		d.Random = rand.Reader
	}
	return &Handlers{d: d}, nil
}

// Register adds the routes below /api/application/users.
func (h *Handlers) Register(g api.Group) {
	r := g.Sub("/api/application/users")
	read, write := api.ApplicationKey("users", auth.AccessRead), api.ApplicationKey("users", auth.AccessReadWrite)
	r.GET("", h.List, read)
	r.GET("/external/:external", h.External, read)
	r.GET("/:user", h.View, read)
	r.POST("", h.Create, write)
	r.PATCH("/:user", h.Update, write)
	r.DELETE("/:user", h.Delete, write)
}

// log writes an entry about a user. A failure is logged and never reaches the person, whose action has happened.
func (h *Handlers) log(c echo.Context, p authz.Principal, subject uint64, event, props string) {
	e := Entry{Event: event, SubjectUserID: subject, ActorID: p.User.ID, IP: c.RealIP(), Properties: props}
	if p.Key != nil {
		id := p.Key.ID
		e.APIKeyID = &id
	}
	if err := h.d.Activity.Log(c.Request().Context(), e); err != nil {
		h.d.Logger.Error("activity entry not stored", "event", event, "user", subject, "error", err)
	}
}
