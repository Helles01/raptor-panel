package appservers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/field"
	"github.com/Helles01/raptor-panel/internal/api/validate"
	"github.com/Helles01/raptor-panel/internal/fielderr"
	"github.com/Helles01/raptor-panel/internal/servers"
)

// maxBody is the largest body these routes read.
const maxBody = 256 << 10

// positive turns a number into an identifier; what is below one is no identifier, which the rules then refuse.
func positive(n int64) uint64 {
	if n < 1 {
		return 0
	}
	return uint64(n)
}

// optional is the pointer a rule package takes for a field that may be left alone: nil when it was not sent, a pointer to the empty
// text when it was sent as null, which clears it.
func optional(s field.String) *string {
	switch s.State {
	case field.Valid:
		return &s.V
	case field.Null:
		empty := ""
		return &empty
	}
	return nil
}

// renamed gives a refusal that names one field the name another has in this request.
func renamed(err error, from, to string) error {
	if fe, ok := fielderr.Of(err); ok && fe.Field == from {
		return &fielderr.Error{Field: to, Message: fe.Message}
	}
	return err
}

// noUser is the 422 for an owner that is not there, as the framework words it.
func noUser() error {
	v := validate.New()
	v.Custom("user", "exists", "The selected user is invalid.")
	return v.Err()
}

func noEgg() error {
	v := validate.New()
	v.Custom("egg", "exists", "The selected egg is invalid.")
	return v.Err()
}

type detailsBody struct {
	ExternalID  field.String `json:"external_id"`
	Name        field.String `json:"name"`
	User        field.Int    `json:"user"`
	Description field.String `json:"description"`
}

// Details is PATCH /servers/{server}/details: the name and the owner are needed every time; the external identifier and the description
// stay as they are unless they are sent, and null or an empty text clears them (C-050). When the owner changes, the node is told to
// forget the previous owner's tokens and SFTP sessions: the store records that in the same transaction (the outbox).
func (h *Handlers) Details(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	id, err := appkit.ID(c, "server")
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	cur, err := h.d.Store.Find(ctx, id, nil)
	if err != nil {
		return appkit.StoreError(err)
	}
	var b detailsBody
	if err := validate.DecodeJSON(c.Request().Body, &b, maxBody); err != nil {
		return err
	}
	v := validate.New()
	v.Sent("name", b.Name.State)
	v.Type("name", b.Name.State, validate.KindString, false)
	v.Sent("user", b.User.State)
	v.Type("user", b.User.State, validate.KindInteger, false)
	v.Type("external_id", b.ExternalID.State, validate.KindString, true)
	v.Type("description", b.Description.State, validate.KindString, true)
	if err := v.Err(); err != nil {
		return err
	}

	plan, err := servers.PlanDetails(
		servers.Details{ExternalID: cur.ExternalID, OwnerID: cur.OwnerID, Name: cur.Config.Name, Description: cur.Config.Description},
		servers.DetailsChange{ExternalID: optional(b.ExternalID), OwnerID: positive(b.User.V), Name: b.Name.V, Description: optional(b.Description)},
	)
	if err != nil {
		return appkit.FieldError(err)
	}
	var revoke uint64
	if plan.OwnerChanged {
		revoke = plan.PreviousOwner
	}
	s, err := h.d.Store.UpdateDetails(ctx, id, plan.Next, revoke)
	switch {
	case errors.Is(err, ErrNoOwner):
		return noUser()
	case err != nil:
		return appkit.StoreError(err)
	}
	return c.JSON(http.StatusOK, appkit.NewView(p).Server(s, nil))
}

type limitsBody struct {
	Memory  field.Int    `json:"memory"`
	Swap    field.Int    `json:"swap"`
	IO      field.Int    `json:"io"`
	CPU     field.Int    `json:"cpu"`
	Disk    field.Int    `json:"disk"`
	Threads field.String `json:"threads"`
}

type featureBody struct {
	Databases   field.Int `json:"databases"`
	Allocations field.Int `json:"allocations"`
	Backups     field.Int `json:"backups"`
}

type buildBody struct {
	Allocation        field.Int       `json:"allocation"`
	OOMDisabled       field.Bool      `json:"oom_disabled"`
	Limits            *limitsBody     `json:"limits"`
	AddAllocations    json.RawMessage `json:"add_allocations"`
	RemoveAllocations json.RawMessage `json:"remove_allocations"`
	FeatureLimits     *featureBody    `json:"feature_limits"`
	// The limits may also be sent at the top level, as older clients do; the ones in limits win.
	limitsBody
}

// number is the pointer a limit takes: nil when it was not sent.
func number(i field.Int) *int64 {
	if i.State != field.Valid {
		return nil
	}
	return &i.V
}

// limit picks the value of a limit: from limits if it is there, otherwise from the top level.
func limit(inner, outer field.Int) field.Int {
	if inner.State != field.Absent {
		return inner
	}
	return outer
}

// feature changes a feature limit: not sent keeps it, null clears it.
func feature(cur *int64, sent field.Int) *int64 {
	switch sent.State {
	case field.Valid:
		return &sent.V
	case field.Null:
		return nil
	}
	return cur
}

// Build is PATCH /servers/{server}/build: limits, feature limits, allocations to add and remove and the default one. The change is
// decided inside the store's transaction, while the allocations are locked. Afterwards the node is told to sync; when it cannot be
// reached the change stays, and that is only logged, as in the reference.
func (h *Handlers) Build(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	id, err := appkit.ID(c, "server")
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if _, err := h.d.Store.Find(ctx, id, nil); err != nil {
		return appkit.StoreError(err)
	}
	var b buildBody
	if err := validate.DecodeJSON(c.Request().Body, &b, maxBody); err != nil {
		return err
	}
	in := b.limitsBody
	if b.Limits != nil {
		in = limitsBody{
			Memory: limit(b.Limits.Memory, b.Memory), Swap: limit(b.Limits.Swap, b.Swap), IO: limit(b.Limits.IO, b.IO), CPU: limit(b.Limits.CPU, b.CPU),
			Disk: limit(b.Limits.Disk, b.Disk), Threads: b.Limits.Threads,
		}
		if b.Limits.Threads.State == field.Absent {
			in.Threads = b.Threads
		}
	}

	v := validate.New()
	for _, f := range []struct {
		name string
		i    field.Int
	}{{"memory", in.Memory}, {"swap", in.Swap}, {"io", in.IO}, {"cpu", in.CPU}, {"disk", in.Disk}} {
		v.Type("limits."+f.name, f.i.State, validate.KindNumber, false)
	}
	v.Type("limits.threads", in.Threads.State, validate.KindString, true)
	v.Type("allocation", b.Allocation.State, validate.KindInteger, false)
	v.Type("oom_disabled", b.OOMDisabled.State, validate.KindBoolean, false)
	add := appkit.IDs(b.AddAllocations, "add_allocations", v)
	remove := appkit.IDs(b.RemoveAllocations, "remove_allocations", v)
	if b.FeatureLimits == nil {
		v.Custom("feature_limits", "required", "The feature limits field is required.")
	} else {
		for _, f := range []struct {
			name string
			i    field.Int
		}{{"databases", b.FeatureLimits.Databases}, {"allocations", b.FeatureLimits.Allocations}, {"backups", b.FeatureLimits.Backups}} {
			v.Type("feature_limits."+f.name, f.i.State, validate.KindInteger, true)
		}
	}
	if err := v.Err(); err != nil {
		return err
	}

	update := servers.LimitsUpdate{Memory: number(in.Memory), Swap: number(in.Swap), IO: number(in.IO), CPU: number(in.CPU), Disk: number(in.Disk)}
	switch in.Threads.State {
	case field.Valid:
		update.Threads = &in.Threads.V
	case field.Null:
		update.ClearThreads = true
	}
	newDefault := positive(b.Allocation.V)
	rows := append(append([]uint64{}, add...), remove...)
	if newDefault != 0 {
		rows = append(rows, newDefault)
	}

	s, err := h.d.Store.UpdateBuild(ctx, id, BuildRequest{Rows: rows, Change: func(cur appkit.Server, rows map[uint64]servers.AllocationRow) (BuildChange, error) {
		c := cur.Config
		limits, err := update.Apply(servers.Limits{Memory: c.Memory, Swap: c.Swap, Disk: c.Disk, IO: c.IO, CPU: c.CPU, Threads: c.Threads})
		if err != nil {
			return BuildChange{}, err
		}
		features := servers.FeatureLimits{
			Databases:   feature(cur.DatabaseLimit, b.FeatureLimits.Databases),
			Allocations: feature(c.AllocationLimit, b.FeatureLimits.Allocations),
			Backups:     feature(cur.BackupLimit, b.FeatureLimits.Backups),
		}
		if err := features.Validate(); err != nil {
			return BuildChange{}, err
		}
		plan, err := servers.PlanAllocationChange(cur.ID, cur.NodeID, cur.AllocationID, rows, add, remove, newDefault)
		if err != nil {
			return BuildChange{}, err
		}
		oom := c.OOMDisabled
		if b.OOMDisabled.State == field.Valid {
			oom = b.OOMDisabled.V
		}
		return BuildChange{Limits: limits, Features: features, OOMDisabled: oom, Plan: plan}, nil
	}})
	switch {
	case errors.Is(err, servers.ErrNoFallback), errors.Is(err, servers.ErrDefaultNotAssigned):
		return apierr.Display(err.Error())
	case err != nil:
		if _, ok := fielderr.Of(err); ok {
			return allocationError(err)
		}
		return appkit.StoreError(err)
	}

	h.sync(c, id)
	return c.JSON(http.StatusOK, appkit.NewView(p).Server(s, nil))
}

// sync tells the node of a server to fetch its configuration again. The change is made already, so a node that cannot be told is only
// logged: it reads the configuration the next time it starts the server.
func (h *Handlers) sync(c echo.Context, id uint64) {
	ctx := c.Request().Context()
	t, err := h.d.Targets.For(ctx, id)
	if err == nil {
		err = h.d.Daemon.Sync(ctx, t.Node, t.UUID)
	}
	if err != nil {
		h.d.Logger.Warn("a node was not told to sync a server", "server", id, "error", err)
	}
}

type startupBody struct {
	Startup     field.String    `json:"startup"`
	Environment json.RawMessage `json:"environment"`
	Egg         field.Int       `json:"egg"`
	Image       field.String    `json:"image"`
	SkipScripts field.Bool      `json:"skip_scripts"`
}

// Startup is PATCH /servers/{server}/startup: the startup command, the egg, the image and whether to skip the scripts are all needed,
// and the values of the egg's variables (which may be none). The node is not told: it takes the new values when the server starts.
func (h *Handlers) Startup(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	id, err := appkit.ID(c, "server")
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	cur, err := h.d.Store.Find(ctx, id, nil)
	if err != nil {
		return appkit.StoreError(err)
	}
	var b startupBody
	if err := validate.DecodeJSON(c.Request().Body, &b, maxBody); err != nil {
		return err
	}
	v := validate.New()
	v.Sent("startup", b.Startup.State)
	v.Type("startup", b.Startup.State, validate.KindString, false)
	env := appkit.Environment(b.Environment, v)
	v.Sent("egg", b.Egg.State)
	v.Type("egg", b.Egg.State, validate.KindInteger, false)
	v.Sent("image", b.Image.State)
	v.Type("image", b.Image.State, validate.KindString, false)
	v.Sent("skip_scripts", b.SkipScripts.State)
	v.Type("skip_scripts", b.SkipScripts.State, validate.KindBoolean, false)
	if err := v.Err(); err != nil {
		return err
	}

	egg, err := h.d.Store.Egg(ctx, positive(b.Egg.V))
	switch {
	case errors.Is(err, ErrNoEgg):
		return noEgg()
	case err != nil:
		return appkit.StoreError(err)
	}
	plan, err := servers.PlanStartup(
		servers.StartupSettings{Startup: cur.Config.Startup, EggID: cur.EggID, Image: cur.Config.Image, SkipScripts: cur.Config.SkipScripts},
		servers.StartupChange{Startup: b.Startup.V, EggID: positive(b.Egg.V), Image: b.Image.V, SkipScripts: b.SkipScripts.V, Environment: env},
		egg.Variables,
	)
	if err != nil {
		if e := appkit.EnvironmentError(err); e != nil {
			return e
		}
		return appkit.FieldError(renamed(err, "docker_image", "image"))
	}
	s, err := h.d.Store.UpdateStartup(ctx, id, plan.Next, egg.NestID, plan.Values)
	switch {
	case errors.Is(err, ErrNoEgg):
		return noEgg()
	case err != nil:
		return appkit.StoreError(err)
	}
	return c.JSON(http.StatusOK, appkit.NewView(p).Server(s, nil))
}
