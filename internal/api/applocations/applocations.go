// Package applocations serves the locations of the Application API: listing and looking them up, making one, changing it and deleting
// it. The rules are in internal/nodes; this package puts them in order and talks to the storage through the interface below.
package applocations

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/field"
	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/api/resp"
	"github.com/Helles01/raptor-panel/internal/api/validate"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/nodes"
)

// Store is the part of the storage the routes need. The short name is unique: a second one answers *appkit.TakenError. A Long that is
// empty is stored as null.
type Store interface {
	// List returns a page and the total. load names the relations to fill (see appkit.Includes).
	List(ctx context.Context, q listquery.Query, page, perPage int, load appkit.Includes) ([]appkit.Location, int, error)
	// Find returns a location, or appkit.ErrNotFound.
	Find(ctx context.Context, id uint64, load appkit.Includes) (appkit.Location, error)
	Create(ctx context.Context, l nodes.Location) (appkit.Location, error)
	Update(ctx context.Context, id uint64, l nodes.Location) (appkit.Location, error)
	// Delete removes the location. check is called with the number of nodes in it, inside the transaction in which they are counted;
	// its error ends the request and is returned as it is.
	Delete(ctx context.Context, id uint64, check func(nodes int64) error) error
}

// Deps are everything the endpoints need.
type Deps struct {
	Store Store
	// BaseURL is the address of the panel, for the links in answers.
	BaseURL string
	Logger  *slog.Logger
}

// Handlers are the endpoints.
type Handlers struct {
	d Deps
}

// New checks the dependencies and returns the endpoints.
func New(d Deps) (*Handlers, error) {
	if d.Store == nil || d.BaseURL == "" || d.Logger == nil {
		return nil, errors.New("applocations: the store, the panel's address and a logger are required")
	}
	return &Handlers{d: d}, nil
}

// Register adds the routes below /api/application/locations.
func (h *Handlers) Register(g api.Group) {
	r := g.Sub("/api/application/locations")
	read, write := api.ApplicationKey("locations", auth.AccessRead), api.ApplicationKey("locations", auth.AccessReadWrite)
	r.GET("", h.List, read)
	r.GET("/:location", h.View, read)
	r.POST("", h.Create, write)
	r.PATCH("/:location", h.Update, write)
	r.DELETE("/:location", h.Delete, write)
}

// maxBody is the largest body these routes read.
const maxBody = 16 << 10

// List is GET /locations.
func (h *Handlers) List(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	q, err := listquery.Parse(c.QueryParams(), listquery.Locations)
	if err != nil {
		return err
	}
	page, perPage, err := resp.PageParams(c.QueryParams())
	if err != nil {
		return err
	}
	v := appkit.NewView(p)
	inc := appkit.Wanted(c.QueryParams(), listquery.Locations)
	list, total, err := h.d.Store.List(c.Request().Context(), q, page, perPage, inc.Load(v, listquery.Locations))
	if err != nil {
		return appkit.StoreError(err)
	}
	items := make([]resp.Item, len(list))
	for i, l := range list {
		items[i] = v.Location(l, inc)
	}
	return appkit.List(c, items, total, perPage, page)
}

// View is GET /locations/{location}.
func (h *Handlers) View(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	id, err := appkit.ID(c, "location")
	if err != nil {
		return err
	}
	v := appkit.NewView(p)
	inc := appkit.Wanted(c.QueryParams(), listquery.Locations)
	l, err := h.d.Store.Find(c.Request().Context(), id, inc.Load(v, listquery.Locations))
	if err != nil {
		return appkit.StoreError(err)
	}
	return c.JSON(http.StatusOK, v.Location(l, inc))
}

type body struct {
	Short field.String `json:"short"`
	Long  field.String `json:"long"`
}

// read decodes a body and checks that the short name is there and that both are text.
func read(c echo.Context) (body, error) {
	var b body
	if err := validate.DecodeJSON(c.Request().Body, &b, maxBody); err != nil {
		return body{}, err
	}
	v := validate.New()
	v.Sent("short", b.Short.State)
	v.Type("short", b.Short.State, validate.KindString, false)
	v.Type("long", b.Long.State, validate.KindString, true)
	return b, v.Err()
}

// Create is POST /locations.
func (h *Handlers) Create(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	b, err := read(c)
	if err != nil {
		return err
	}
	loc := nodes.Location{Short: b.Short.V, Long: b.Long.V}
	if err := nodes.ValidateLocation(loc.Short, loc.Long); err != nil {
		return appkit.FieldError(err)
	}
	l, err := h.d.Store.Create(c.Request().Context(), loc)
	if err != nil {
		return appkit.StoreError(err)
	}
	return appkit.Created(c, appkit.NewView(p).Location(l, nil), h.d.BaseURL, "/api/application/locations/"+strconv.FormatUint(l.ID, 10))
}

// Update is PATCH /locations/{location}: the short name is needed every time, and the long one stays as it is unless it is sent.
func (h *Handlers) Update(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	id, err := appkit.ID(c, "location")
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	cur, err := h.d.Store.Find(ctx, id, nil)
	if err != nil {
		return appkit.StoreError(err)
	}
	b, err := read(c)
	if err != nil {
		return err
	}
	var long *string
	switch b.Long.State {
	case field.Valid:
		long = &b.Long.V
	case field.Null:
		empty := ""
		long = &empty
	}
	current := nodes.Location{Short: cur.Short}
	if cur.Long != nil {
		current.Long = *cur.Long
	}
	next, err := nodes.ChangeLocation(current, b.Short.V, long)
	if err != nil {
		return appkit.FieldError(err)
	}
	l, err := h.d.Store.Update(ctx, id, next)
	if err != nil {
		return appkit.StoreError(err)
	}
	return c.JSON(http.StatusOK, appkit.NewView(p).Location(l, nil))
}

// Delete is DELETE /locations/{location}: a location that still has nodes is not deleted.
func (h *Handlers) Delete(c echo.Context) error {
	if _, err := appkit.Caller(c); err != nil {
		return err
	}
	id, err := appkit.ID(c, "location")
	if err != nil {
		return err
	}
	err = h.d.Store.Delete(c.Request().Context(), id, nodes.CheckCanDeleteLocation)
	switch {
	case errors.Is(err, nodes.ErrLocationHasNodes):
		return apierr.Display(nodes.ErrLocationHasNodes.Error())
	case err != nil:
		return appkit.StoreError(err)
	}
	return c.NoContent(http.StatusNoContent)
}
