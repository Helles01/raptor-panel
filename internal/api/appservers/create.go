package appservers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/controlapi"
	"github.com/Helles01/raptor-panel/internal/api/field"
	"github.com/Helles01/raptor-panel/internal/api/validate"
	"github.com/Helles01/raptor-panel/internal/fielderr"
	"github.com/Helles01/raptor-panel/internal/ids"
	"github.com/Helles01/raptor-panel/internal/servers"
	"github.com/Helles01/raptor-panel/internal/wings"
)

// target finds the node of a server: one that is not there is a 404.
func (h *Handlers) target(ctx context.Context, id uint64) (controlapi.Target, error) {
	t, err := h.d.Targets.For(ctx, id)
	switch {
	case errors.Is(err, controlapi.ErrNotFound):
		return controlapi.Target{}, apierr.NotFound()
	case err != nil:
		return controlapi.Target{}, apierr.Internal(err)
	}
	return t, nil
}

type allocationBody struct {
	Default    field.Int       `json:"default"`
	Additional json.RawMessage `json:"additional"`
}

type deployBody struct {
	Locations   json.RawMessage `json:"locations"`
	DedicatedIP field.Bool      `json:"dedicated_ip"`
	PortRange   json.RawMessage `json:"port_range"`
}

type createBody struct {
	ExternalID        field.String    `json:"external_id"`
	Name              field.String    `json:"name"`
	Description       field.String    `json:"description"`
	User              field.Int       `json:"user"`
	Egg               field.Int       `json:"egg"`
	DockerImage       field.String    `json:"docker_image"`
	Startup           field.String    `json:"startup"`
	Environment       json.RawMessage `json:"environment"`
	SkipScripts       field.Bool      `json:"skip_scripts"`
	OOMDisabled       field.Bool      `json:"oom_disabled"`
	Limits            *limitsBody     `json:"limits"`
	FeatureLimits     *featureBody    `json:"feature_limits"`
	Allocation        *allocationBody `json:"allocation"`
	Deploy            *deployBody     `json:"deploy"`
	StartOnCompletion field.Bool      `json:"start_on_completion"`
}

// sentence makes the message of an allocation refusal into a sentence for the person.
func sentence(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	out := string(r)
	if !strings.HasSuffix(out, ".") {
		out += "."
	}
	return out
}

// allocationError is the 422 for an allocation the request names that cannot be used. The rules of internal/servers word these as
// "allocation 5 is already in use", which is a whole message already.
func allocationError(err error) error {
	fe, ok := fielderr.Of(err)
	if !ok || (fe.Field != "allocation" && fe.Field != "add_allocations" && fe.Field != "remove_allocations") {
		return appkit.FieldError(err)
	}
	v := validate.New()
	v.Custom(fe.Field, "invalid", sentence(fe.Message))
	return v.Err()
}

func needed(v *validate.Validator, name string, i field.Int) {
	v.Sent(name, i.State)
	v.Type(name, i.State, validate.KindNumber, false)
}

// Create is POST /servers: on the node of the allocations the administrator names, or on one found for the server when the request has a
// deploy object. The server is stored first, in the installing state, together with its allocations and the values of its variables;
// then the node is told to set it up, and if it refuses, the server is taken back.
func (h *Handlers) Create(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	var b createBody
	if err := validate.DecodeJSON(c.Request().Body, &b, maxBody); err != nil {
		return err
	}
	v := validate.New()
	v.Sent("name", b.Name.State)
	v.Type("name", b.Name.State, validate.KindString, false)
	v.Type("external_id", b.ExternalID.State, validate.KindString, true)
	v.Type("description", b.Description.State, validate.KindString, true)
	v.Sent("user", b.User.State)
	v.Type("user", b.User.State, validate.KindInteger, false)
	v.Sent("egg", b.Egg.State)
	v.Type("egg", b.Egg.State, validate.KindInteger, false)
	v.Sent("docker_image", b.DockerImage.State)
	v.Type("docker_image", b.DockerImage.State, validate.KindString, false)
	v.Sent("startup", b.Startup.State)
	v.Type("startup", b.Startup.State, validate.KindString, false)
	env := appkit.Environment(b.Environment, v)
	v.Type("skip_scripts", b.SkipScripts.State, validate.KindBoolean, false)
	v.Type("oom_disabled", b.OOMDisabled.State, validate.KindBoolean, false)
	v.Type("start_on_completion", b.StartOnCompletion.State, validate.KindBoolean, false)

	var limits servers.Limits
	if b.Limits == nil {
		v.Custom("limits", "required", "The limits field is required.")
	} else {
		l := b.Limits
		needed(v, "limits.memory", l.Memory)
		needed(v, "limits.swap", l.Swap)
		needed(v, "limits.disk", l.Disk)
		needed(v, "limits.io", l.IO)
		needed(v, "limits.cpu", l.CPU)
		v.Type("limits.threads", l.Threads.State, validate.KindString, true)
		limits = servers.Limits{Memory: l.Memory.V, Swap: l.Swap.V, Disk: l.Disk.V, IO: l.IO.V, CPU: l.CPU.V}
		if l.Threads.State == field.Valid {
			limits.Threads = &l.Threads.V
		}
	}
	var features servers.FeatureLimits
	if b.FeatureLimits == nil {
		v.Custom("feature_limits", "required", "The feature limits field is required.")
	} else {
		f := b.FeatureLimits
		for _, x := range []struct {
			name string
			i    field.Int
		}{{"databases", f.Databases}, {"allocations", f.Allocations}, {"backups", f.Backups}} {
			v.Type("feature_limits."+x.name, x.i.State, validate.KindInteger, true)
		}
		features = servers.FeatureLimits{Databases: number(f.Databases), Allocations: number(f.Allocations), Backups: number(f.Backups)}
	}

	var primary uint64
	var additional, locations []uint64
	var portRange []string
	var dedicated bool
	switch {
	case b.Deploy != nil:
		for _, f := range []struct {
			name string
			raw  json.RawMessage
		}{{"deploy.locations", b.Deploy.Locations}, {"deploy.port_range", b.Deploy.PortRange}} {
			if t := strings.TrimSpace(string(f.raw)); t == "" || t == "null" {
				v.Custom(f.name, "present", "The "+strings.ReplaceAll(f.name, "_", " ")+" field must be present.")
			}
		}
		locations = appkit.IDs(b.Deploy.Locations, "deploy.locations", v)
		portRange = appkit.Strings(b.Deploy.PortRange, "deploy.port_range", v)
		v.Sent("deploy.dedicated_ip", b.Deploy.DedicatedIP.State)
		v.Type("deploy.dedicated_ip", b.Deploy.DedicatedIP.State, validate.KindBoolean, false)
		dedicated = b.Deploy.DedicatedIP.V
		for i, l := range locations {
			if l == 0 {
				v.Custom("deploy.locations", "min", "The deploy.locations."+strconv.Itoa(i)+" must be at least 1.")
			}
		}
	case b.Allocation == nil:
		v.Custom("allocation.default", "required", "The allocation.default field is required.")
	default:
		v.Sent("allocation.default", b.Allocation.Default.State)
		v.Type("allocation.default", b.Allocation.Default.State, validate.KindInteger, false)
		primary = positive(b.Allocation.Default.V)
		additional = appkit.IDs(b.Allocation.Additional, "allocation.additional", v)
	}
	if err := v.Err(); err != nil {
		return err
	}

	var externalID *string
	if b.ExternalID.State == field.Valid {
		externalID = &b.ExternalID.V
	}
	ns := servers.NewServer{
		ExternalID: externalID, Name: b.Name.V, Description: b.Description.V, OwnerID: positive(b.User.V), EggID: positive(b.Egg.V),
		Image: b.DockerImage.V, Startup: b.Startup.V, Limits: limits, Features: features, Allocation: primary, Additional: additional,
	}
	check := ns
	if b.Deploy != nil {
		check.Allocation = 1 // not chosen yet
	}
	if err := check.Validate(); err != nil {
		return allocationError(renamed(err, "allocation.default", "allocation"))
	}

	ctx := c.Request().Context()
	egg, err := h.d.Store.Egg(ctx, ns.EggID)
	switch {
	case errors.Is(err, ErrNoEgg):
		return noEgg()
	case err != nil:
		return appkit.StoreError(err)
	}
	values, err := servers.ValidateEnvironment(egg.Variables, env, true)
	if err != nil {
		if e := appkit.EnvironmentError(err); e != nil {
			return e
		}
		return apierr.Internal(err)
	}

	if b.Deploy != nil {
		if primary, err = h.deploy(ctx, ns, locations, portRange, dedicated); err != nil {
			return err
		}
		ns.Allocation = primary
	}
	return h.make(c, p, ns, egg.NestID, values, b, primary, additional)
}

// deploy finds an allocation for a server on a node that has room for it. What cannot be found is a message to the person.
func (h *Handlers) deploy(ctx context.Context, ns servers.NewServer, locations []uint64, ports []string, dedicated bool) (uint64, error) {
	usage, err := h.d.Store.Usage(ctx)
	if err != nil {
		return 0, appkit.StoreError(err)
	}
	viable, err := servers.FindViableNodes(usage, locations, ns.Limits.Memory, ns.Limits.Disk)
	if err != nil {
		return 0, apierr.Display(err.Error())
	}
	specs, err := servers.ParsePortSpecs(ports)
	if err != nil {
		return 0, apierr.Display(err.Error())
	}
	nodeIDs := make([]uint64, len(viable))
	for i, n := range viable {
		nodeIDs[i] = n.ID
	}
	cands, err := h.d.Store.Allocations(ctx, nodeIDs)
	if err != nil {
		return 0, appkit.StoreError(err)
	}
	chosen, err := servers.SelectAllocation(cands, nodeIDs, specs, dedicated, h.d.Random)
	switch {
	case errors.Is(err, servers.ErrNoViableAllocation):
		return 0, apierr.Display(err.Error())
	case err != nil:
		return 0, apierr.Internal(err)
	}
	return chosen.ID, nil
}

// maxUUIDTries is how often a name for a server is made again when the UUID or its short form is taken.
const maxUUIDTries = 5

// make stores the server and has the node set it up.
func (h *Handlers) make(c echo.Context, p authz.Principal, ns servers.NewServer, nestID uint64, values []servers.VariableValue, b createBody, primary uint64, additional []uint64) error {
	ctx := c.Request().Context()
	req := CreateRequest{
		Server: ns, NestID: nestID, Values: values, OOMDisabled: true, SkipScripts: b.SkipScripts.V,
		Allocations: append([]uint64{primary}, additional...),
		Check: func(rows map[uint64]servers.AllocationRow) error {
			row, ok := rows[primary]
			if !ok {
				return fielderr.New("allocation", "allocation %d does not exist", primary)
			}
			return servers.CheckNewServerAllocations(row.NodeID, primary, additional, rows)
		},
	}
	if b.OOMDisabled.State == field.Valid {
		req.OOMDisabled = b.OOMDisabled.V
	}

	var s appkit.Server
	for try := 0; ; try++ {
		uuid, err := ids.UUIDv4(h.d.Random)
		if err != nil {
			return apierr.Internal(err)
		}
		req.UUID, req.ShortID = uuid, uuid[:8]
		s, err = h.d.Store.Create(ctx, req)
		var taken *appkit.TakenError
		if errors.As(err, &taken) && taken.Field == "uuid" && try < maxUUIDTries {
			continue
		}
		switch {
		case errors.Is(err, ErrNoOwner):
			return noUser()
		case errors.Is(err, ErrNoEgg):
			return noEgg()
		case err != nil:
			if _, ok := fielderr.Of(err); ok {
				return allocationError(err)
			}
			return appkit.StoreError(err)
		}
		break
	}

	t, err := h.target(ctx, s.ID)
	if err == nil {
		err = h.d.Daemon.Create(ctx, t.Node, s.Config.UUID, b.StartOnCompletion.V)
	}
	if err != nil {
		h.takeBack(c, s, t)
		var ae *apierr.Error
		if errors.As(err, &ae) {
			return err
		}
		return controlapi.DaemonError(err)
	}
	return c.JSON(http.StatusCreated, appkit.NewView(p).Server(s, nil))
}

// takeBack removes a server the node would not set up, from the node (in case it half did) and from the panel. The first failure is
// what the person is told, so what goes wrong here is only logged.
func (h *Handlers) takeBack(c echo.Context, s appkit.Server, t controlapi.Target) {
	ctx := c.Request().Context()
	if t.UUID != "" {
		var we *wings.Error
		if err := h.d.Daemon.Delete(ctx, t.Node, t.UUID); err != nil && !(errors.As(err, &we) && we.NodeStatus == http.StatusNotFound) {
			h.d.Logger.Warn("a node was not asked to delete a server that could not be created", "server", s.ID, "error", err)
		}
	}
	if err := h.d.Store.Delete(ctx, s.ID); err != nil {
		h.d.Logger.Error("a server that could not be created was not taken back", "server", s.ID, "error", err)
	}
}
