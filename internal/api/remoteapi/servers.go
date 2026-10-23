package remoteapi

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/field"
	"github.com/Helles01/raptor-panel/internal/api/validate"
	"github.com/Helles01/raptor-panel/internal/remote"
	"github.com/Helles01/raptor-panel/internal/servers"
)

// serverConfiguration is one server as the node reads it.
type serverConfiguration struct {
	UUID                 string              `json:"uuid"`
	Settings             servers.WingsConfig `json:"settings"`
	ProcessConfiguration servers.EggProcess  `json:"process_configuration"`
}

type listAnswer struct {
	Data []serverConfiguration `json:"data"`
	Meta remote.ListMeta       `json:"meta"`
}

// List is GET /api/remote/servers: the servers of the calling node, a page at a time, as the node reads them when it starts.
func (h *Handlers) List(c echo.Context) error {
	node, err := scope(c)
	if err != nil {
		return err
	}
	page, perPage := remote.ParsePaging(c.QueryParam("page"), c.QueryParam("per_page"))
	offset, _ := remote.Window(0, page, perPage)
	result, err := h.d.Servers.List(c.Request().Context(), node.NodeID, offset, perPage)
	if err != nil {
		return apierr.Internal(fmt.Errorf("list servers: %w", err))
	}

	data := make([]serverConfiguration, 0, len(result.Servers))
	for _, s := range result.Servers {
		process, err := servers.EggProcessConfiguration(s)
		if err != nil {
			// One egg with a broken configuration must not keep the node from starting its other servers. The server is left out and
			// the fault is logged for whoever looks after the egg.
			h.d.Logger.Error("a server is left out of the list of its node", "server", s.UUID, "error", err)
			continue
		}
		data = append(data, serverConfiguration{UUID: s.UUID, Settings: servers.Config(s), ProcessConfiguration: process})
	}
	_, meta := remote.Window(result.Total, page, perPage)
	return c.JSON(http.StatusOK, listAnswer{Data: data, Meta: meta})
}

// Details is GET /api/remote/servers/{uuid}: the configuration of one server. While the server is being transferred, both nodes may ask.
func (h *Handlers) Details(c echo.Context) error {
	node, err := scope(c)
	if err != nil {
		return err
	}
	uuid, err := serverUUID(c)
	if err != nil {
		return err
	}
	d, err := h.d.Servers.Reachable(c.Request().Context(), node.NodeID, uuid)
	if err != nil {
		return found(err)
	}
	if err := node.CanReadDetails(d.NodeID, d.Transfer); err != nil {
		return forbidden()
	}
	process, err := servers.EggProcessConfiguration(d.Server)
	if err != nil {
		return apierr.Internal(fmt.Errorf("server %s: %w", d.Server.UUID, err))
	}
	return c.JSON(http.StatusOK, serverConfiguration{UUID: d.Server.UUID, Settings: servers.Config(d.Server), ProcessConfiguration: process})
}

// InstallScript is GET /api/remote/servers/{uuid}/install: the script a node runs to install a server of its own.
func (h *Handlers) InstallScript(c echo.Context) error {
	node, err := scope(c)
	if err != nil {
		return err
	}
	uuid, err := serverUUID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	held, err := h.d.Servers.Own(ctx, node.NodeID, uuid)
	if err != nil {
		return found(err)
	}
	if node.Owns(held.NodeID) != nil {
		return forbidden()
	}
	script, err := h.d.Servers.InstallScript(ctx, held.ID)
	if err != nil {
		return apierr.Internal(fmt.Errorf("install script: %w", err))
	}
	return c.JSON(http.StatusOK, script)
}

type installBody struct {
	Successful field.Bool `json:"successful"`
	Reinstall  field.Bool `json:"reinstall"`
}

// InstallReport is POST /api/remote/servers/{uuid}/install: the result of an installation or reinstallation. Only a server that is
// waiting for one takes it (C-033), and the status is stored only if it is still what it was read as, so that two reports cannot both win.
func (h *Handlers) InstallReport(c echo.Context) error {
	node, err := scope(c)
	if err != nil {
		return err
	}
	uuid, err := serverUUID(c)
	if err != nil {
		return err
	}
	var body installBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxSmallBody); err != nil {
		return err
	}
	v := validate.New()
	v.Sent("successful", body.Successful.State)
	v.Type("successful", body.Successful.State, validate.KindBoolean, false)
	v.Type("reinstall", body.Reinstall.State, validate.KindBoolean, true)
	if err := v.Err(); err != nil {
		return err
	}

	ctx := c.Request().Context()
	held, err := h.d.Servers.Own(ctx, node.NodeID, uuid)
	if err != nil {
		return found(err)
	}
	if node.Owns(held.NodeID) != nil {
		return forbidden()
	}
	outcome, err := servers.ApplyInstallReport(held.Status, held.InstalledBefore, servers.InstallReport{
		Successful: body.Successful.V, Reinstall: body.Reinstall.V,
	}, h.d.Notify)
	var stateErr *servers.StateError
	if errors.As(err, &stateErr) {
		return apierr.Conflict(stateErr.Message)
	} else if err != nil {
		return apierr.Internal(err)
	}

	stored, err := h.d.Servers.Installed(ctx, held.ID, held.Status, outcome, h.d.Now())
	switch {
	case err != nil:
		return apierr.Internal(fmt.Errorf("store installation: %w", err))
	case !stored:
		return apierr.Conflict("This server is not waiting for the result of an installation.")
	}

	if outcome.Notify != servers.NotifyNone {
		h.notifyInstalled(c, held)
	}
	return noContent(c)
}

// notifyInstalled tells the owner. The report is already stored, so a mail that cannot be made or queued is only logged.
func (h *Handlers) notifyInstalled(c echo.Context, held Held) {
	m, err := h.d.Site.ServerInstalled(held.OwnerEmail, held.OwnerUsername, held.Name)
	if err == nil {
		err = h.d.Outbox.Send(c.Request().Context(), m)
	}
	if err != nil {
		h.d.Logger.Error("the mail about an installation was not queued", "server", held.UUID, "error", err)
	}
}

// Reset is POST /api/remote/servers/reset, called by a node after it has booted its servers: whatever was installing or restoring a
// backup when it stopped did not finish. A restore is over and the server normal again, with an entry that it failed; an installation
// is marked failed (C-036).
func (h *Handlers) Reset(c echo.Context) error {
	node, err := scope(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	candidates, err := h.d.Servers.Resettable(ctx, node.NodeID)
	if err != nil {
		return apierr.Internal(fmt.Errorf("list servers to reset: %w", err))
	}

	var changes []ResetChange
	for _, s := range candidates {
		to, changed := remote.ResetStatus(s.Status, s.InstalledBefore)
		if !changed {
			continue
		}
		change := ResetChange{ServerID: s.ID, From: s.Status, To: to}
		if s.Status == servers.StatusRestoringBackup {
			change.RestoreFailed = s.Restoring
		}
		changes = append(changes, change)
	}
	if len(changes) > 0 {
		if err := h.d.Servers.Reset(ctx, node.NodeID, changes); err != nil {
			return apierr.Internal(fmt.Errorf("reset servers: %w", err))
		}
	}
	return noContent(c)
}

// maxSmallBody is the largest body of the small requests: an installation report.
const maxSmallBody = 4 << 10
