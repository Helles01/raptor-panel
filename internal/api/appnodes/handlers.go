package appnodes

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/field"
	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/api/resp"
	"github.com/Helles01/raptor-panel/internal/api/validate"
	"github.com/Helles01/raptor-panel/internal/ids"
	"github.com/Helles01/raptor-panel/internal/nodes"
	"github.com/Helles01/raptor-panel/internal/wings"
)

// maxBody is the largest body these routes read.
const maxBody = 64 << 10

// daemonOff is the reference's text for a change that is stored but could not be sent to the node.
const daemonOff = "The daemon configuration has been updated, however there was an error encountered while attempting to automatically update the configuration file on the Daemon. You will need to manually update the configuration file (config.yml) for the daemon to apply these changes."

// List is GET /nodes.
func (h *Handlers) List(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	q, err := listquery.Parse(c.QueryParams(), listquery.Nodes)
	if err != nil {
		return err
	}
	page, perPage, err := resp.PageParams(c.QueryParams())
	if err != nil {
		return err
	}
	v := appkit.NewView(p)
	inc := appkit.Wanted(c.QueryParams(), listquery.Nodes)
	list, total, err := h.d.Store.List(c.Request().Context(), q, page, perPage, inc.Load(v))
	if err != nil {
		return appkit.StoreError(err)
	}
	items := make([]resp.Item, len(list))
	for i, n := range list {
		items[i] = v.Node(n, inc)
	}
	return appkit.List(c, items, total, perPage, page)
}

// View is GET /nodes/{node}.
func (h *Handlers) View(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	id, err := appkit.ID(c, "node")
	if err != nil {
		return err
	}
	v := appkit.NewView(p)
	inc := appkit.Wanted(c.QueryParams(), listquery.Nodes)
	n, err := h.d.Store.Find(c.Request().Context(), id, inc.Load(v))
	if err != nil {
		return appkit.StoreError(err)
	}
	return c.JSON(http.StatusOK, v.Node(n, inc))
}

// Configuration is GET /nodes/{node}/configuration: what the node's Wings is configured with, token included.
func (h *Handlers) Configuration(c echo.Context) error {
	if _, err := appkit.Caller(c); err != nil {
		return err
	}
	id, err := appkit.ID(c, "node")
	if err != nil {
		return err
	}
	wn, creds, err := h.d.Store.Config(c.Request().Context(), id)
	if err != nil {
		return appkit.StoreError(err)
	}
	cfg, err := nodes.WingsConfigJSON(wn, creds, h.d.BaseURL)
	if err != nil {
		return apierr.Internal(err)
	}
	noStore(c)
	return c.JSONBlob(http.StatusOK, cfg)
}

type body struct {
	Name               field.String `json:"name"`
	Description        field.String `json:"description"`
	LocationID         field.Int    `json:"location_id"`
	Public             field.Bool   `json:"public"`
	FQDN               field.String `json:"fqdn"`
	Scheme             field.String `json:"scheme"`
	BehindProxy        field.Bool   `json:"behind_proxy"`
	MaintenanceMode    field.Bool   `json:"maintenance_mode"`
	Memory             field.Int    `json:"memory"`
	MemoryOverallocate field.Int    `json:"memory_overallocate"`
	Disk               field.Int    `json:"disk"`
	DiskOverallocate   field.Int    `json:"disk_overallocate"`
	UploadSize         field.Int    `json:"upload_size"`
	DaemonBase         field.String `json:"daemon_base"`
	DaemonSFTP         field.Int    `json:"daemon_sftp"`
	DaemonListen       field.Int    `json:"daemon_listen"`
	// ResetSecret is read as it was sent: only the JSON value true resets the token, as in the reference.
	ResetSecret json.RawMessage `json:"reset_secret"`
}

// read decodes a body and checks that what every request needs is there and that each field is of its kind. What a value may be is up
// to internal/nodes.
func read(c echo.Context) (body, error) {
	var b body
	if err := validate.DecodeJSON(c.Request().Body, &b, maxBody); err != nil {
		return body{}, err
	}
	v := validate.New()
	for _, f := range []struct {
		name string
		s    field.State
		kind validate.Kind
	}{
		{"name", b.Name.State, validate.KindString}, {"fqdn", b.FQDN.State, validate.KindString}, {"scheme", b.Scheme.State, validate.KindString},
		{"location_id", b.LocationID.State, validate.KindInteger},
		{"memory", b.Memory.State, validate.KindNumber}, {"memory_overallocate", b.MemoryOverallocate.State, validate.KindNumber},
		{"disk", b.Disk.State, validate.KindNumber}, {"disk_overallocate", b.DiskOverallocate.State, validate.KindNumber},
		{"daemon_sftp", b.DaemonSFTP.State, validate.KindNumber}, {"daemon_listen", b.DaemonListen.State, validate.KindNumber},
	} {
		v.Sent(f.name, f.s)
		v.Type(f.name, f.s, f.kind, false)
	}
	v.Type("description", b.Description.State, validate.KindString, true)
	v.Type("public", b.Public.State, validate.KindBoolean, false)
	v.Type("behind_proxy", b.BehindProxy.State, validate.KindBoolean, false)
	v.Type("maintenance_mode", b.MaintenanceMode.State, validate.KindBoolean, false)
	v.Type("upload_size", b.UploadSize.State, validate.KindInteger, false)
	// The base path is optional, but not blank when it is given.
	if b.DaemonBase.State == field.Null {
		v.Sent("daemon_base", field.Null)
	} else {
		v.Type("daemon_base", b.DaemonBase.State, validate.KindString, false)
	}
	return b, v.Err()
}

func (b body) resets() bool { return bytes.Equal(bytes.TrimSpace(b.ResetSecret), []byte("true")) }

// positive turns a number into an identifier; what is below one is no identifier, which the rules then refuse.
func positive(n int64) uint64 {
	if n < 1 {
		return 0
	}
	return uint64(n)
}

// settings is the node a create asks for: the defaults of the node form, and what was sent over them.
func (b body) settings() nodes.Settings {
	s := nodes.DefaultNewNode()
	s.Name, s.Description, s.LocationID, s.FQDN, s.Scheme = b.Name.V, b.Description.V, positive(b.LocationID.V), b.FQDN.V, b.Scheme.V
	s.Memory, s.MemoryOverallocate, s.Disk, s.DiskOverallocate = b.Memory.V, b.MemoryOverallocate.V, b.Disk.V, b.DiskOverallocate.V
	s.DaemonSFTP, s.DaemonListen = int(b.DaemonSFTP.V), int(b.DaemonListen.V)
	if b.Public.State == field.Valid {
		s.Public = b.Public.V
	}
	if b.BehindProxy.State == field.Valid {
		s.BehindProxy = b.BehindProxy.V
	}
	if b.MaintenanceMode.State == field.Valid {
		s.MaintenanceMode = b.MaintenanceMode.V
	}
	if b.UploadSize.State == field.Valid {
		s.UploadSize = b.UploadSize.V
	}
	if b.DaemonBase.State == field.Valid {
		s.DaemonBase = b.DaemonBase.V
	}
	return s
}

// change is what an update asks for: what was not sent stays as it is.
func (b body) change() nodes.Change {
	ch := nodes.Change{
		Name: b.Name.V, LocationID: positive(b.LocationID.V), FQDN: b.FQDN.V, Scheme: b.Scheme.V,
		Memory: b.Memory.V, MemoryOverallocate: b.MemoryOverallocate.V, Disk: b.Disk.V, DiskOverallocate: b.DiskOverallocate.V,
		DaemonSFTP: int(b.DaemonSFTP.V), DaemonListen: int(b.DaemonListen.V),
	}
	switch b.Description.State {
	case field.Valid:
		ch.Description = &b.Description.V
	case field.Null:
		empty := ""
		ch.Description = &empty
	}
	if b.Public.State == field.Valid {
		ch.Public = &b.Public.V
	}
	if b.BehindProxy.State == field.Valid {
		ch.BehindProxy = &b.BehindProxy.V
	}
	if b.MaintenanceMode.State == field.Valid {
		ch.MaintenanceMode = &b.MaintenanceMode.V
	}
	if b.UploadSize.State == field.Valid {
		ch.UploadSize = &b.UploadSize.V
	}
	if b.DaemonBase.State == field.Valid {
		ch.DaemonBase = &b.DaemonBase.V
	}
	return ch
}

// storeError is appkit.StoreError with the one answer of this store: a location that is not there.
func storeError(err error) error {
	if errors.Is(err, ErrNoLocation) {
		v := validate.New()
		v.Custom("location_id", "exists", "The selected location id is invalid.")
		return v.Err()
	}
	return appkit.StoreError(err)
}

// Create is POST /nodes: the node gets a token of its own, which only Wings and the configuration route ever see.
func (h *Handlers) Create(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	b, err := read(c)
	if err != nil {
		return err
	}
	s := b.settings()
	if err := s.Validate(); err != nil {
		return appkit.FieldError(err)
	}
	uuid, err := ids.UUIDv4(h.d.Random)
	if err != nil {
		return apierr.Internal(err)
	}
	creds, err := nodes.NewCredentials(h.d.Random)
	if err != nil {
		return apierr.Internal(err)
	}
	n, err := h.d.Store.Create(c.Request().Context(), CreateRequest{UUID: uuid, Settings: s, Credentials: creds})
	if err != nil {
		return storeError(err)
	}
	return appkit.Created(c, appkit.NewView(p).Node(n, nil), h.d.BaseURL, "/api/application/nodes/"+strconv.FormatUint(n.ID, 10))
}

// Update is PATCH /nodes/{node}. The settings are stored first; then the node, if it is running, is sent the new configuration. When it
// cannot be reached the change stays and the answer says that the configuration file has to be updated by hand.
func (h *Handlers) Update(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	id, err := appkit.ID(c, "node")
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
	next, err := cur.Settings.Apply(b.change())
	if err != nil {
		return appkit.FieldError(err)
	}
	var creds *nodes.Credentials
	if b.resets() {
		fresh, err := nodes.NewCredentials(h.d.Random)
		if err != nil {
			return apierr.Internal(err)
		}
		creds = &fresh
	}

	// The node is reached where it is now, with the token it has now; both are read before they change.
	old, connErr := h.d.Store.Connection(ctx, id)
	n, err := h.d.Store.Update(ctx, id, next, creds)
	if err != nil {
		return storeError(err)
	}
	if err := h.push(c, id, old, connErr); err != nil {
		h.d.Logger.Warn("the new configuration of a node was not delivered", "node", id, "error", err)
		return apierr.Display(daemonOff)
	}
	return c.JSON(http.StatusOK, appkit.NewView(p).Node(n, nil))
}

// push sends the node its configuration as it is stored now.
func (h *Handlers) push(c echo.Context, id uint64, old wings.Node, connErr error) error {
	if connErr != nil {
		return connErr
	}
	ctx := c.Request().Context()
	wn, creds, err := h.d.Store.Config(ctx, id)
	if err != nil {
		return err
	}
	cfg, err := nodes.WingsConfigJSON(wn, creds, h.d.BaseURL)
	if err != nil {
		return err
	}
	return h.d.Daemon.UpdateConfiguration(ctx, old, cfg)
}

// Delete is DELETE /nodes/{node}: a node with servers is not deleted.
func (h *Handlers) Delete(c echo.Context) error {
	if _, err := appkit.Caller(c); err != nil {
		return err
	}
	id, err := appkit.ID(c, "node")
	if err != nil {
		return err
	}
	err = h.d.Store.Delete(c.Request().Context(), id, nodes.CheckCanDeleteNode)
	switch {
	case errors.Is(err, nodes.ErrNodeHasServers):
		return apierr.Display(nodes.ErrNodeHasServers.Error())
	case err != nil:
		return appkit.StoreError(err)
	}
	return c.NoContent(http.StatusNoContent)
}
