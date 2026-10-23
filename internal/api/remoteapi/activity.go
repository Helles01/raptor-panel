package remoteapi

import (
	"fmt"
	"io"
	"slices"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/activity"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
)

// maxActivityBody holds a full batch of events with their metadata.
const maxActivityBody = 2 << 20

// Activity is POST /api/remote/activity: the events a node collected (what was done in the console, by SFTP, with files). An event
// that cannot be used is left out, not the request, because the node sends an unconfirmed batch again from its oldest event, and one
// bad event answered with an error would hold back every later one for good (C-034). An event for a server that is not the node's is
// left out too.
func (h *Handlers) Activity(c echo.Context) error {
	node, err := scope(c)
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request().Body, maxActivityBody+1))
	if err != nil {
		return apierr.BadRequest("The request body could not be read.")
	}
	if len(raw) > maxActivityBody {
		return apierr.TooLarge()
	}
	result, err := activity.Process(raw, h.d.Now())
	if err != nil {
		return apierr.BadRequest(err.Error())
	}

	skipped := result.Skipped
	if len(result.Entries) > 0 {
		ctx := c.Request().Context()
		var serverUUIDs, userUUIDs []string
		for _, e := range result.Entries {
			if !slices.Contains(serverUUIDs, e.ServerUUID) {
				serverUUIDs = append(serverUUIDs, e.ServerUUID)
			}
			if e.UserUUID != "" && !slices.Contains(userUUIDs, e.UserUUID) {
				userUUIDs = append(userUUIDs, e.UserUUID)
			}
		}
		ownServers, err := h.d.Activity.Servers(ctx, node.NodeID, serverUUIDs)
		if err != nil {
			return apierr.Internal(fmt.Errorf("find servers: %w", err))
		}
		users := map[string]uint64{}
		if len(userUUIDs) > 0 {
			if users, err = h.d.Activity.Users(ctx, userUUIDs); err != nil {
				return apierr.Internal(fmt.Errorf("find users: %w", err))
			}
		}

		stored := make([]StoredEntry, 0, len(result.Entries))
		for _, e := range result.Entries {
			serverID, ok := ownServers[e.ServerUUID]
			if !ok {
				skipped++
				continue
			}
			stored = append(stored, StoredEntry{
				ServerID: serverID, UserID: users[e.UserUUID], Event: e.Event, IP: e.IP, Properties: e.Properties, When: e.When,
			})
		}
		if len(stored) > 0 {
			if err := h.d.Activity.Store(ctx, stored); err != nil {
				return apierr.Internal(fmt.Errorf("store activity: %w", err))
			}
		}
	}
	if skipped > 0 {
		h.d.Logger.Warn("events of a node were left out", "node", node.NodeID, "skipped", skipped)
	}
	return noContent(c)
}
