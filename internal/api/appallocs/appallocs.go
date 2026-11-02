// Package appallocs serves the allocations of a node in the Application API: listing them, adding addresses and ports in bulk, and
// deleting one that no server uses. The rules are in internal/nodes; this package puts them in order and talks to the storage through the
// interface below. The allocations a server's owner manages are in internal/api/allocapi.
package appallocs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

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

// Store is the part of the storage the routes need. Every method works on the allocations of one node and answers appkit.ErrNotFound if
// the node is not there.
type Store interface {
	// Exists answers appkit.ErrNotFound unless the node is there.
	Exists(ctx context.Context, nodeID uint64) error
	// List returns a page and the total. load names the relations to fill (see appkit.Includes).
	List(ctx context.Context, nodeID uint64, q listquery.Query, page, perPage int, load appkit.Includes) ([]appkit.Allocation, int, error)
	// Add stores the allocations in one transaction; an address and port the node has already are left as they are.
	Add(ctx context.Context, nodeID uint64, list []nodes.Allocation) error
	// Delete removes an allocation of the node. check is called with the server that holds it (nil if free), inside the transaction in
	// which it is read; its error ends the request and is returned as it is. An allocation of another node is not found.
	Delete(ctx context.Context, nodeID, allocationID uint64, check func(serverID *uint64) error) error
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
		return nil, errors.New("appallocs: the store and a logger are required")
	}
	return &Handlers{d: d}, nil
}

// Register adds the routes below /api/application/nodes/{node}/allocations.
func (h *Handlers) Register(g api.Group) {
	r := g.Sub("/api/application/nodes/:node/allocations")
	r.GET("", h.List, api.ApplicationKey("allocations", auth.AccessRead))
	r.POST("", h.Add, api.ApplicationKey("allocations", auth.AccessReadWrite))
	r.DELETE("/:allocation", h.Delete, api.ApplicationKey("allocations", auth.AccessReadWrite))
}

// maxBody is the largest body these routes read: a thousand ports written out is far less.
const maxBody = 256 << 10

// List is GET /nodes/{node}/allocations.
func (h *Handlers) List(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	nodeID, err := appkit.ID(c, "node")
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if err := h.d.Store.Exists(ctx, nodeID); err != nil {
		return appkit.StoreError(err)
	}
	q, err := listquery.Parse(c.QueryParams(), listquery.Allocations)
	if err != nil {
		return err
	}
	page, perPage, err := resp.PageParams(c.QueryParams())
	if err != nil {
		return err
	}
	v := appkit.NewView(p)
	inc := appkit.Wanted(c.QueryParams(), listquery.Allocations)
	list, total, err := h.d.Store.List(ctx, nodeID, q, page, perPage, inc.Load(v, listquery.Allocations))
	if err != nil {
		return appkit.StoreError(err)
	}
	items := make([]resp.Item, len(list))
	for i, a := range list {
		items[i] = v.Allocation(a, inc)
	}
	return appkit.List(c, items, total, perPage, page)
}

type body struct {
	IP    field.String    `json:"ip"`
	Alias field.String    `json:"alias"`
	Ports json.RawMessage `json:"ports"`
}

// ports reads the list of ports, each of them text ("25565" or "25570-25580"), as the reference's rules say.
func ports(raw json.RawMessage, v *validate.Validator) []string {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "[]" {
		v.Custom("ports", "required", "The ports field is required.")
		return nil
	}
	var elements []json.RawMessage
	if raw[0] != '[' || json.Unmarshal(raw, &elements) != nil {
		v.Custom("ports", "array", "The ports must be an array.")
		return nil
	}
	out := make([]string, 0, len(elements))
	for i, e := range elements {
		var s string
		if json.Unmarshal(e, &s) != nil {
			v.Custom("ports", "string", fmt.Sprintf("The ports.%d must be a string.", i))
			return nil
		}
		out = append(out, s)
	}
	return out
}

// Add is POST /nodes/{node}/allocations: an address, or a network of them, with the ports and ranges of ports to open on each.
func (h *Handlers) Add(c echo.Context) error {
	if _, err := appkit.Caller(c); err != nil {
		return err
	}
	nodeID, err := appkit.ID(c, "node")
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if err := h.d.Store.Exists(ctx, nodeID); err != nil {
		return appkit.StoreError(err)
	}
	var b body
	if err := validate.DecodeJSON(c.Request().Body, &b, maxBody); err != nil {
		return err
	}
	v := validate.New()
	v.Sent("ip", b.IP.State)
	v.Type("ip", b.IP.State, validate.KindString, false)
	v.Type("alias", b.Alias.State, validate.KindString, true)
	list := ports(b.Ports, v)
	if b.Alias.State == field.Valid {
		v.Length("alias", b.Alias.V, 1, 191)
	}
	if err := v.Err(); err != nil {
		return err
	}

	// What the rules of the allocations refuse is told as the reference tells it, as a message to the person.
	expanded, err := nodes.ExpandAllocations(nodes.NewAllocations{IP: b.IP.V, Ports: list, Alias: b.Alias.V})
	if err != nil {
		return apierr.Display(err.Error())
	}
	if err := h.d.Store.Add(ctx, nodeID, expanded); err != nil {
		return appkit.StoreError(err)
	}
	return c.NoContent(http.StatusNoContent)
}

// Delete is DELETE /nodes/{node}/allocations/{allocation}: an allocation that a server uses is not deleted.
func (h *Handlers) Delete(c echo.Context) error {
	if _, err := appkit.Caller(c); err != nil {
		return err
	}
	nodeID, err := appkit.ID(c, "node")
	if err != nil {
		return err
	}
	id, err := appkit.ID(c, "allocation")
	if err != nil {
		return err
	}
	err = h.d.Store.Delete(c.Request().Context(), nodeID, id, nodes.CheckCanDeleteAllocationOfNode)
	switch {
	case errors.Is(err, nodes.ErrAllocationInUse):
		return apierr.Display(nodes.ErrAllocationInUse.Error())
	case err != nil:
		return appkit.StoreError(err)
	}
	return c.NoContent(http.StatusNoContent)
}
