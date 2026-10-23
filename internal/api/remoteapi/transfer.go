package remoteapi

import (
	"errors"
	"fmt"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/remote"
)

// TransferFailure is POST /api/remote/servers/{uuid}/transfer/failure: either node says the transfer failed, and the allocations of
// the new node are released.
func (h *Handlers) TransferFailure(c echo.Context) error {
	return h.endTransfer(c, func(t *remote.Transfer, s remote.Scope) (remote.TransferResult, error) { return t.Fail(s) })
}

// TransferSuccess is POST /api/remote/servers/{uuid}/transfer/success: only the new node says it worked. The server moves, the old
// allocations are released, and the old node is told to delete its copy.
func (h *Handlers) TransferSuccess(c echo.Context) error {
	return h.endTransfer(c, func(t *remote.Transfer, s remote.Scope) (remote.TransferResult, error) { return t.Succeed(s) })
}

func (h *Handlers) endTransfer(c echo.Context, end func(*remote.Transfer, remote.Scope) (remote.TransferResult, error)) error {
	node, err := scope(c)
	if err != nil {
		return err
	}
	uuid, err := serverUUID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	info, err := h.d.Servers.Transfer(ctx, node.NodeID, uuid)
	if err != nil {
		return found(err)
	}

	result, err := end(info.Transfer, node)
	switch {
	case errors.Is(err, remote.ErrNotTransferring):
		return apierr.Conflict(err.Error())
	case errors.Is(err, remote.ErrForbidden):
		return forbidden()
	case err != nil:
		return apierr.Internal(err)
	}

	applied, err := h.d.Servers.CompleteTransfer(ctx, info.ServerID, result)
	switch {
	case err != nil:
		return apierr.Internal(fmt.Errorf("end transfer: %w", err))
	case !applied:
		// The other node, or the same one a moment ago, ended it first.
		return apierr.Conflict(remote.ErrNotTransferring.Error())
	}

	if result.DeleteFromOldNode != 0 {
		// The server now lives on the new node; a copy that stays on the old one is only a waste of room, so a failure is logged.
		if err := h.d.OldNodes.DeleteServer(ctx, result.DeleteFromOldNode, uuid); err != nil {
			h.d.Logger.Warn("the old node did not delete the server it handed over", "server", uuid, "node", result.DeleteFromOldNode, "error", err)
		}
	}
	return noContent(c)
}
