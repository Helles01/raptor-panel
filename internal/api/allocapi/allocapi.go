// Package allocapi serves the routes with which a user manages the extra network allocations of their server: listing them, adding one
// at random, writing a note on one, making one the primary and giving one up. The rules are in internal/nodes (allocations.go); this
// package puts them in order and talks to the storage and the activity log through the interfaces below.
package allocapi

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/activity"
	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/clientapi"
	"github.com/Helles01/raptor-panel/internal/api/field"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/api/resp"
	"github.com/Helles01/raptor-panel/internal/api/validate"
	"github.com/Helles01/raptor-panel/internal/nodes"
	"github.com/Helles01/raptor-panel/internal/permissions"
)

// ErrNotFound is what the store answers when the allocation is not one of the server's.
var ErrNotFound = errors.New("allocapi: not found")

// AssignRequest asks the store to give a server one more allocation, in one transaction in which the server's allocations are locked.
type AssignRequest struct {
	ServerID uint64
	// Check is called with the server's allocation limit and the number of allocations it has; an error ends the request and is
	// returned as it is.
	Check func(limit *int64, count int64) error
	// Port is called when the node has no free allocation on the server's address, with the ports of the range that are in use on
	// it, to choose the port of a new one; an error ends the request and is returned as it is.
	Port func(used []int) (int, error)
}

// Store is the part of the storage the routes need. Every method works on the allocations of the one server.
type Store interface {
	// List returns the allocations of the server and the id of its primary one.
	List(ctx context.Context, serverID uint64) (allocs []clientapi.Allocation, primaryID uint64, err error)
	// Limit returns the server's allocation limit (nil for none) and the id of its primary allocation.
	Limit(ctx context.Context, serverID uint64) (limit *int64, primaryID uint64, err error)
	// Find returns one of the server's own allocations, or ErrNotFound.
	Find(ctx context.Context, serverID, allocationID uint64) (clientapi.Allocation, error)
	Assign(ctx context.Context, r AssignRequest) (clientapi.Allocation, error)
	// SetNotes stores the note of an allocation of the server; nil clears it.
	SetNotes(ctx context.Context, serverID, allocationID uint64, notes *string) error
	SetPrimary(ctx context.Context, serverID, allocationID uint64) error
	// Release gives an allocation up (its note cleared, no server), only if it is the server's and not its primary, and says whether it did.
	Release(ctx context.Context, serverID, allocationID uint64) (bool, error)
}

// Entry is an entry of the activity log about a server and an allocation.
type Entry struct {
	Event string
	// ServerID is the server and AllocationID the allocation the entry is about; ActorID is the user who did it.
	ServerID     uint64
	AllocationID uint64
	ActorID      uint64
	APIKeyID     *uint64
	IP           string
	Properties   string
}

// ActivityLog stores activity entries.
type ActivityLog interface {
	Log(ctx context.Context, e Entry) error
}

// The throttle of adding allocations: two a minute for each server, whoever adds, as in the reference.
const (
	AddLimit  = 2
	AddWindow = time.Minute
)

// Deps are everything the endpoints need.
type Deps struct {
	Store    Store
	Activity ActivityLog
	// AddLimiter allows AddLimit additions in AddWindow for a server.
	AddLimiter *middleware.Limiter
	// Policy answers what the administrator has set about users adding allocations. It is asked at each request, so that a setting changed from the
	// command line applies to the panel that is running.
	Policy func(ctx context.Context) (Policy, error)
	Logger *slog.Logger
	// Random defaults to crypto/rand.
	Random io.Reader
}

// Policy is the administrator's say on allocations that users add for themselves.
type Policy struct {
	// Enabled is the panel's switch for users adding allocations.
	Enabled bool
	// RangeStart and RangeEnd are where a new port is chosen.
	RangeStart, RangeEnd int
}

// Handlers are the endpoints.
type Handlers struct {
	d Deps
}

// New checks the dependencies and returns the endpoints.
func New(d Deps) (*Handlers, error) {
	if d.Store == nil || d.Activity == nil || d.AddLimiter == nil || d.Policy == nil || d.Logger == nil {
		return nil, errors.New("allocapi: the store, the activity log, the limiter, the policy and a logger are required")
	}
	if d.Random == nil {
		d.Random = rand.Reader
	}
	return &Handlers{d: d}, nil
}

// Register adds the routes below /api/client/servers/{server}/network.
func (h *Handlers) Register(g api.Group) {
	n := g.Sub("/api/client/servers/:server/network/allocations")
	n.GET("", h.List, api.ServerPermission(permissions.AllocationRead))
	n.POST("", h.Add, api.ServerPermission(permissions.AllocationCreate))
	n.POST("/:allocation", h.SetNotes, api.ServerPermission(permissions.AllocationUpdate))
	n.POST("/:allocation/primary", h.SetPrimary, api.ServerPermission(permissions.AllocationUpdate))
	n.DELETE("/:allocation", h.Delete, api.ServerPermission(permissions.AllocationDelete))
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

// allocationID reads the {allocation} of the route, a number. What cannot be one is not looked up.
func allocationID(c echo.Context) (uint64, error) {
	id, err := strconv.ParseUint(c.Param("allocation"), 10, 64)
	if err != nil || id == 0 {
		return 0, apierr.NotFound()
	}
	return id, nil
}

// log writes an entry about an allocation. A failure is logged and never reaches the person, whose action has happened.
func (h *Handlers) log(c echo.Context, p authz.Principal, event string, a clientapi.Allocation, values map[string]any) {
	props, err := activity.Properties(event, values)
	if err != nil {
		h.d.Logger.Error("activity entry refused", "event", event, "error", err)
		return
	}
	e := Entry{Event: event, ServerID: p.Server.ID, AllocationID: a.ID, ActorID: p.User.ID, IP: c.RealIP(), Properties: props}
	if p.Key != nil {
		id := p.Key.ID
		e.APIKeyID = &id
	}
	if err := h.d.Activity.Log(c.Request().Context(), e); err != nil {
		h.d.Logger.Error("activity entry not stored", "event", event, "server", p.Server.ID, "error", err)
	}
}

func text(a clientapi.Allocation) string { return fmt.Sprintf("%s:%d", a.IP, a.Port) }

// isRule is true for the refusals of the allocation rules, which the reference shows as a message to the person (400).
func isRule(err error) bool {
	for _, rule := range []error{nodes.ErrAutoDisabled, nodes.ErrAllocLimit, nodes.ErrNoLimitSet, nodes.ErrPrimary, nodes.ErrNoSpace} {
		if errors.Is(err, rule) {
			return true
		}
	}
	return false
}

// List is GET /network/allocations.
func (h *Handlers) List(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	allocs, primary, err := h.d.Store.List(c.Request().Context(), p.Server.ID)
	if err != nil {
		return storeError(err)
	}
	items := make([]resp.Item, len(allocs))
	for i, a := range allocs {
		items[i] = clientapi.AllocationItem(a, a.ID == primary)
	}
	return c.JSON(http.StatusOK, resp.NewList(items))
}

// Add is POST /network/allocations: one more allocation, taken from the free ones on the server's address or, if none is free, made
// with a random port of the range the administrator set.
func (h *Handlers) Add(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	// Two a minute for a server, whoever asks. Only calls that passed the authorizer count.
	if allowed, retry := h.d.AddLimiter.Allow(p.Server.UUID); !allowed {
		return apierr.TooManyRequests(retry)
	}
	policy, err := h.d.Policy(c.Request().Context())
	if err != nil {
		return apierr.Internal(fmt.Errorf("read the settings of the allocations: %w", err))
	}
	a, err := h.d.Store.Assign(c.Request().Context(), AssignRequest{
		ServerID: p.Server.ID,
		Check: func(limit *int64, count int64) error {
			return nodes.CheckCanAddAllocation(policy.Enabled, limit, count)
		},
		Port: func(used []int) (int, error) {
			return nodes.PickPort(policy.RangeStart, policy.RangeEnd, used, h.d.Random)
		},
	})
	switch {
	case isRule(err):
		return apierr.Display(err.Error())
	case err != nil:
		return storeError(err)
	}
	h.log(c, p, activity.ServerAllocationCreate, a, map[string]any{"allocation": text(a)})
	return c.JSON(http.StatusOK, clientapi.AllocationItem(a, false))
}

type notesBody struct {
	Notes field.String `json:"notes"`
}

// SetNotes is POST /network/allocations/{allocation}: the note on an allocation, which must be sent (null clears it).
func (h *Handlers) SetNotes(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	id, err := allocationID(c)
	if err != nil {
		return err
	}
	var body notesBody
	if err := validate.DecodeJSON(c.Request().Body, &body, 16<<10); err != nil {
		return err
	}
	v := validate.New()
	if body.Notes.State == field.Absent {
		v.Custom("notes", "present", "The notes field must be present.")
	}
	v.Type("notes", body.Notes.State, validate.KindString, true)
	var notes *string
	if body.Notes.State == field.Valid {
		if err := nodes.ValidateNotes(body.Notes.V); err != nil {
			v.Custom("notes", "invalid", err.Error())
		}
		notes = &body.Notes.V
	}
	if err := v.Err(); err != nil {
		return err
	}

	ctx := c.Request().Context()
	a, err := h.d.Store.Find(ctx, p.Server.ID, id)
	if err != nil {
		return storeError(err)
	}
	if err := h.d.Store.SetNotes(ctx, p.Server.ID, id, notes); err != nil {
		return storeError(err)
	}
	old := a.Notes
	a.Notes = notes
	if !samePtr(old, notes) {
		h.log(c, p, activity.ServerAllocationNotes, a, map[string]any{"allocation": text(a), "old": old, "new": notes})
	}
	_, primary, err := h.d.Store.Limit(ctx, p.Server.ID)
	if err != nil {
		return storeError(err)
	}
	return c.JSON(http.StatusOK, clientapi.AllocationItem(a, a.ID == primary))
}

func samePtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// SetPrimary is POST /network/allocations/{allocation}/primary.
func (h *Handlers) SetPrimary(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	id, err := allocationID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	a, err := h.d.Store.Find(ctx, p.Server.ID, id)
	if err != nil {
		return storeError(err)
	}
	if err := h.d.Store.SetPrimary(ctx, p.Server.ID, id); err != nil {
		return storeError(err)
	}
	h.log(c, p, activity.ServerAllocationPrimary, a, map[string]any{"allocation": text(a)})
	return c.JSON(http.StatusOK, clientapi.AllocationItem(a, true))
}

// Delete is DELETE /network/allocations/{allocation}: gives an allocation up. A server with no limit set cannot give any up, and
// the primary one cannot be given up.
func (h *Handlers) Delete(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	id, err := allocationID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	a, err := h.d.Store.Find(ctx, p.Server.ID, id)
	if err != nil {
		return storeError(err)
	}
	limit, primary, err := h.d.Store.Limit(ctx, p.Server.ID)
	if err != nil {
		return storeError(err)
	}
	if err := nodes.CheckCanDeleteAllocation(limit, id, primary); err != nil {
		return apierr.Display(err.Error())
	}
	released, err := h.d.Store.Release(ctx, p.Server.ID, id)
	switch {
	case err != nil:
		return storeError(err)
	case !released:
		// The primary allocation was changed to this one, or the allocation was taken away, since it was read.
		return apierr.Conflict("This allocation can no longer be given up, please try again.")
	}
	h.log(c, p, activity.ServerAllocationDelete, a, map[string]any{"allocation": text(a)})
	return c.NoContent(http.StatusNoContent)
}
