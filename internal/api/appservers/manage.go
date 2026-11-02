package appservers

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/controlapi"
	"github.com/Helles01/raptor-panel/internal/dbhosts"
	"github.com/Helles01/raptor-panel/internal/servers"
	"github.com/Helles01/raptor-panel/internal/wings"
)

// changed is the answer to a request that found the server in another state than it read: another request was first.
func changed() error {
	return apierr.Conflict("The state of the server changed while this was being done, please try again.")
}

// Suspend is POST /servers/{server}/suspend.
func (h *Handlers) Suspend(c echo.Context) error { return h.toggle(c, servers.Suspend) }

// Unsuspend is POST /servers/{server}/unsuspend.
func (h *Handlers) Unsuspend(c echo.Context) error { return h.toggle(c, servers.Unsuspend) }

// toggle suspends or releases a server. The status is changed only if it is still the one that was read, the node is told, and if it
// cannot be, the status is put back. A server that is already as asked is left alone.
func (h *Handlers) toggle(c echo.Context, action servers.SuspendAction) error {
	if _, err := appkit.Caller(c); err != nil {
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
	transferring, err := h.d.Store.Transferring(ctx, id)
	if err != nil {
		return appkit.StoreError(err)
	}
	plan, err := servers.PlanSuspension(action, cur.Config.Status, transferring)
	if err != nil {
		return apierr.Conflict(err.Error())
	}
	if plan.Noop {
		return c.NoContent(http.StatusNoContent)
	}
	t, err := h.target(ctx, id)
	if err != nil {
		return err
	}
	from := cur.Config.Status
	if ok, err := h.d.Store.SetStatus(ctx, id, from, plan.Status); err != nil {
		return appkit.StoreError(err)
	} else if !ok {
		return changed()
	}
	if err := h.d.Daemon.Sync(ctx, t.Node, t.UUID); err != nil {
		h.undo(c, id, plan.Status, from)
		return controlapi.DaemonError(err)
	}
	return c.NoContent(http.StatusNoContent)
}

// undo puts a status back after the node refused. A failure to do that is logged: the first failure is what the person is told.
func (h *Handlers) undo(c echo.Context, id uint64, from, to string) {
	ok, err := h.d.Store.SetStatus(c.Request().Context(), id, from, to)
	if err != nil || !ok {
		h.d.Logger.Error("the status of a server was not put back", "server", id, "from", from, "to", to, "stored", ok, "error", err)
	}
}

// Reinstall is POST /servers/{server}/reinstall: the server is marked as installing and the node runs the install script again. If
// the node cannot be told the mark is taken back.
func (h *Handlers) Reinstall(c echo.Context) error {
	if _, err := appkit.Caller(c); err != nil {
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
	if err := servers.CheckCanReinstall(cur.Config.Status, cur.Config.SkipScripts); err != nil {
		return apierr.Display(err.Error())
	}
	t, err := h.target(ctx, id)
	if err != nil {
		return err
	}
	from := cur.Config.Status
	if ok, err := h.d.Store.SetStatus(ctx, id, from, servers.StatusInstalling); err != nil {
		return appkit.StoreError(err)
	} else if !ok {
		return changed()
	}
	if err := h.d.Daemon.Reinstall(ctx, t.Node, t.UUID); err != nil {
		h.undo(c, id, servers.StatusInstalling, from)
		return controlapi.DaemonError(err)
	}
	return c.NoContent(http.StatusNoContent)
}

// Delete is DELETE /servers/{server} and DELETE /servers/{server}/force. The node is told first; if it refuses, the server stays unless
// the deletion is forced, or the node says it does not know the server. Then the databases are dropped on their hosts (one that cannot
// be stops an ordinary deletion and is left behind by a forced one), and the server goes.
func (h *Handlers) Delete(c echo.Context) error {
	if _, err := appkit.Caller(c); err != nil {
		return err
	}
	id, err := appkit.ID(c, "server")
	if err != nil {
		return err
	}
	force, ok := servers.ParseDeleteForce(c.Param("force"))
	if !ok {
		return apierr.NotFound()
	}
	ctx := c.Request().Context()
	if _, err := h.d.Store.Find(ctx, id, nil); err != nil {
		return appkit.StoreError(err)
	}
	t, err := h.target(ctx, id)
	if err != nil {
		return err
	}
	if err := h.d.Daemon.Delete(ctx, t.Node, t.UUID); err != nil {
		var we *wings.Error
		status := 0
		if errors.As(err, &we) {
			status = we.NodeStatus
		}
		if !servers.DeleteMayContinue(force, status) {
			return controlapi.DaemonError(err)
		}
		h.d.Logger.Warn("a node did not delete a server, which is deleted all the same", "server", id, "forced", force, "error", err)
	}

	dbs, err := h.d.Store.Databases(ctx, id)
	if err != nil {
		return appkit.StoreError(err)
	}
	for _, d := range dbs {
		if err := h.drop(c, id, d); err != nil {
			if !force {
				return apierr.Internal(err)
			}
			h.d.Logger.Warn("a database was not dropped on its host, and its row is deleted all the same", "server", id, "database", d.ID, "error", err)
		}
		if err := h.d.Store.DeleteDatabase(ctx, d.ID); err != nil {
			return appkit.StoreError(err)
		}
	}
	if err := h.d.Store.Delete(ctx, id); err != nil {
		return appkit.StoreError(err)
	}
	return c.NoContent(http.StatusNoContent)
}

// drop removes a database and its user from the host.
func (h *Handlers) drop(c echo.Context, serverID uint64, d appkit.ServerDatabase) error {
	st, err := dbhosts.Drop(dbhosts.Database{ServerID: serverID, Name: d.Name, Username: d.Username, Remote: d.Remote, MaxConnections: d.MaxConnections})
	if err != nil {
		return err
	}
	return h.d.Hosts.Run(c.Request().Context(), d.HostID, st)
}
