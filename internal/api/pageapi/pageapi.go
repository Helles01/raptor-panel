// Package pageapi serves what a browser asks for first: the HTML page that starts the React frontend (one for the signed in, one for
// the sign-in pages), the files of the frontend build and the translations it fetches. The page carries the account's own data, so
// it is never cached, and nobody gets it without the cookie of a session.
package pageapi

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/api/web"
)

// ErrNotFound is what Users answers for a user that does not exist.
var ErrNotFound = errors.New("pageapi: not found")

// Users gives the page what the frontend learns about the person who is signed in.
type Users interface {
	Find(ctx context.Context, id uint64) (web.User, error)
}

// Settings gives the page the name and the language of the panel.
type Settings interface {
	Site(ctx context.Context) (web.Site, error)
}

// Deps are everything the pages need.
type Deps struct {
	Shell    *web.Shell
	Locales  *web.Locales
	Assets   fs.FS
	Users    Users
	Settings Settings
	// LocaleLimiter limits the requests for translations of one address, which anybody may make.
	LocaleLimiter *middleware.Limiter
	Logger        *slog.Logger
}

// Handlers are the pages.
type Handlers struct {
	d      Deps
	assets http.Handler
}

// New checks the dependencies and returns the pages.
func New(d Deps) (*Handlers, error) {
	if d.Shell == nil || d.Locales == nil || d.Assets == nil || d.Users == nil || d.Settings == nil || d.LocaleLimiter == nil || d.Logger == nil {
		return nil, errors.New("pageapi: the shell, the locales, the assets, the stores, a limiter and a logger are required")
	}
	return &Handlers{d: d, assets: web.AssetsHandler(d.Assets)}, nil
}

// Register adds the pages. Every one of them is open to anybody: whether a page is for the signed in is decided here, because a
// browser that asks for a page must be sent to the sign-in page, where the authorizer would answer with an error in JSON.
func (h *Handlers) Register(g api.Group) {
	g.GET("/", h.App, api.Public())
	g.GET("/account", h.App, api.Public())
	g.GET("/auth/login", h.Auth, api.Public())
	g.GET("/auth/password", h.Auth, api.Public())
	g.GET("/auth/password/reset/:token", h.Auth, api.Public())
	g.GET("/auth/*", h.Auth, api.Public())
	g.GET("/locales/locale.json", h.d.Locales.Handler(), api.Public(), middleware.Throttle(h.d.LocaleLimiter, middleware.KeyByIP))
	g.GET("/assets/*", echo.WrapHandler(h.assets), api.Public())
	g.GET("/*", h.Fallback, api.Public())
}

// signedIn returns the person behind the session of the request, or nil. A session whose account is gone belongs to nobody, so the
// person is sent to sign in again and the sign-in page does not send them back.
func (h *Handlers) signedIn(c echo.Context) (*web.User, error) {
	s := middleware.CurrentSession(c)
	if s == nil || s.UserID == 0 {
		return nil, nil
	}
	u, err := h.d.Users.Find(c.Request().Context(), s.UserID)
	switch {
	case errors.Is(err, ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, apierr.Internal(err)
	}
	return &u, nil
}

func (h *Handlers) redirect(c echo.Context, to string) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.Redirect(http.StatusFound, to)
}

func (h *Handlers) render(c echo.Context, page web.Page, user *web.User) error {
	site, err := h.d.Settings.Site(c.Request().Context())
	if err != nil {
		return apierr.Internal(err)
	}
	if err := h.d.Shell.Render(c.Response(), page, user, site); err != nil {
		return apierr.Internal(err)
	}
	return nil
}

// App is the page of the signed in; whoever is not goes to sign in.
func (h *Handlers) App(c echo.Context) error {
	user, err := h.signedIn(c)
	if err != nil {
		return err
	}
	if user == nil {
		return h.redirect(c, "/auth/login")
	}
	return h.render(c, web.PageApp, user)
}

// Auth is the page of the sign-in, the forgotten password and the reset; whoever is signed in already goes to the panel.
func (h *Handlers) Auth(c echo.Context) error {
	user, err := h.signedIn(c)
	if err != nil {
		return err
	}
	if user != nil {
		return h.redirect(c, "/")
	}
	return h.render(c, web.PageAuth, nil)
}

// reserved are the places the frontend does not answer for: they are the API's, the sign-in pages' or the old admin area's, and a page
// there would be a lie. The reference leaves exactly these out of its catch-all.
var reserved = []string{"api", "auth", "admin", "daemon"}

// Fallback answers every other path the way the reference does: the application, whose router knows its own pages and shows a "not
// found" of its own.
func (h *Handlers) Fallback(c echo.Context) error {
	first, _, _ := strings.Cut(strings.TrimPrefix(c.Request().URL.Path, "/"), "/")
	for _, r := range reserved {
		if first == r {
			return apierr.NotFound()
		}
	}
	return h.App(c)
}
