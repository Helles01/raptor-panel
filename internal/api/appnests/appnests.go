// Package appnests serves the nests and eggs of the Application API: reading them, with the includes of the reference (the eggs of a nest,
// the nest, variables, and the configuration and script an egg borrows from another). Nests and eggs are made by importing an egg file with
// the command line, not through this API, so every route here only reads.
package appnests

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/api/resp"
	"github.com/Helles01/raptor-panel/internal/auth"
)

// Store is the part of the storage the routes need. An egg is looked for among the eggs of its nest.
type Store interface {
	// ListNests returns a page of nests and the total. load names the relations to fill (see appkit.Includes).
	ListNests(ctx context.Context, page, perPage int, load appkit.Includes) ([]appkit.Nest, int, error)
	// FindNest returns a nest, or appkit.ErrNotFound.
	FindNest(ctx context.Context, id uint64, load appkit.Includes) (appkit.Nest, error)
	// ListEggs returns the eggs of a nest, or appkit.ErrNotFound if there is no such nest.
	ListEggs(ctx context.Context, nestID uint64, load appkit.Includes) ([]appkit.Egg, error)
	// FindEgg returns an egg of the nest, or appkit.ErrNotFound.
	FindEgg(ctx context.Context, nestID, eggID uint64, load appkit.Includes) (appkit.Egg, error)
}

// Deps are everything the endpoints need.
type Deps struct {
	Store  Store
	Logger *slog.Logger
}

// Handlers are the endpoints.
type Handlers struct {
	d Deps
}

// New checks the dependencies and returns the endpoints.
func New(d Deps) (*Handlers, error) {
	if d.Store == nil || d.Logger == nil {
		return nil, errors.New("appnests: the store and a logger are required")
	}
	return &Handlers{d: d}, nil
}

// Register adds the routes below /api/application/nests.
func (h *Handlers) Register(g api.Group) {
	r := g.Sub("/api/application/nests")
	nests, eggs := api.ApplicationKey("nests", auth.AccessRead), api.ApplicationKey("eggs", auth.AccessRead)
	r.GET("", h.ListNests, nests)
	r.GET("/:nest", h.ViewNest, nests)
	r.GET("/:nest/eggs", h.ListEggs, eggs)
	r.GET("/:nest/eggs/:egg", h.ViewEgg, eggs)
}

// ListNests is GET /nests. The reference takes no filters or sorts here and ignores any that are sent, and so does this.
func (h *Handlers) ListNests(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	page, perPage, err := resp.PageParams(c.QueryParams())
	if err != nil {
		return err
	}
	v := appkit.NewView(p)
	inc := appkit.Wanted(c.QueryParams(), listquery.Nests)
	list, total, err := h.d.Store.ListNests(c.Request().Context(), page, perPage, inc.Load(v))
	if err != nil {
		return appkit.StoreError(err)
	}
	items := make([]resp.Item, len(list))
	for i, n := range list {
		items[i] = v.Nest(n, inc)
	}
	return appkit.List(c, items, total, perPage, page)
}

// ViewNest is GET /nests/{nest}.
func (h *Handlers) ViewNest(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	id, err := appkit.ID(c, "nest")
	if err != nil {
		return err
	}
	v := appkit.NewView(p)
	inc := appkit.Wanted(c.QueryParams(), listquery.Nests)
	n, err := h.d.Store.FindNest(c.Request().Context(), id, inc.Load(v))
	if err != nil {
		return appkit.StoreError(err)
	}
	return c.JSON(http.StatusOK, v.Nest(n, inc))
}

// ListEggs is GET /nests/{nest}/eggs: all the eggs of the nest, without paging, as in the reference.
func (h *Handlers) ListEggs(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	id, err := appkit.ID(c, "nest")
	if err != nil {
		return err
	}
	v := appkit.NewView(p)
	inc := appkit.Wanted(c.QueryParams(), listquery.Eggs)
	list, err := h.d.Store.ListEggs(c.Request().Context(), id, inc.Load(v))
	if err != nil {
		return appkit.StoreError(err)
	}
	items := make([]resp.Item, len(list))
	for i, e := range list {
		items[i] = v.Egg(e, inc)
	}
	return c.JSON(http.StatusOK, resp.NewList(items))
}

// ViewEgg is GET /nests/{nest}/eggs/{egg}.
func (h *Handlers) ViewEgg(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	nestID, err := appkit.ID(c, "nest")
	if err != nil {
		return err
	}
	id, err := appkit.ID(c, "egg")
	if err != nil {
		return err
	}
	v := appkit.NewView(p)
	inc := appkit.Wanted(c.QueryParams(), listquery.Eggs)
	e, err := h.d.Store.FindEgg(c.Request().Context(), nestID, id, inc.Load(v))
	if err != nil {
		return appkit.StoreError(err)
	}
	return c.JSON(http.StatusOK, v.Egg(e, inc))
}
