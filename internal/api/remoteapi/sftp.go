package remoteapi

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/activity"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/validate"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/auth/lockout"
	"github.com/Helles01/raptor-panel/internal/remote"
	"github.com/Helles01/raptor-panel/internal/servers"
	"github.com/Helles01/raptor-panel/internal/sftp"
)

// maxSFTPBody holds the largest key (16 KiB) with room for the rest of the request.
const maxSFTPBody = 64 << 10

// invalidAccount stands for a user name that cannot be one, in the counters.
const invalidAccount = "-"

// SFTPAuth is POST /api/remote/sftp/auth, the question a node asks for every SFTP sign-in, in the order of docs/modules/07b-sftp.md.
func (h *Handlers) SFTPAuth(c echo.Context) error {
	node, err := scope(c)
	if err != nil {
		return err
	}
	var req sftp.Request
	if err := validate.DecodeJSON(c.Request().Body, &req, maxSFTPBody); err != nil {
		return err
	}
	method, err := req.Method()
	if err != nil {
		return apierr.BadRequest(err.Error())
	}
	ctx := c.Request().Context()
	client := clientHost(req.IP, c.RealIP())

	login, parseErr := sftp.ParseUsername(req.Username)
	if errors.Is(parseErr, sftp.ErrBadServerID) {
		return apierr.BadRequest(parseErr.Error())
	}
	// A name that cannot be one is a failed sign-in like any other, counted under a name nobody has.
	name := login.Username
	if parseErr != nil {
		name = invalidAccount
	}
	counters := sftp.Throttles(name, req.IP, node.NodeID)
	if blocked, retry := h.d.Failures.Blocked(counters); blocked {
		return apierr.TooManyRequests(retry)
	}
	if parseErr != nil {
		return h.rejected(counters)
	}

	user, server, err := h.lookup(c, node.NodeID, login)
	if err != nil {
		return err
	}
	if user.ID == 0 || server.ID == 0 {
		// The time of a password check is spent either way, so that it does not show which of the two was missing.
		auth.VerifyUnknownUser(req.Password)
		return h.rejected(counters)
	}

	if method == sftp.TypePassword {
		if !auth.VerifyPassword(user.PasswordHash, req.Password) {
			if props, ok := h.properties(activity.AuthSFTPFail, map[string]any{"method": "password"}); ok {
				h.logEvent(c, Event{Name: activity.AuthSFTPFail, UserSubject: user.ID, IP: client, Properties: props})
			}
			return h.rejected(counters)
		}
	} else {
		prints, err := h.d.Users.Fingerprints(ctx, user.ID)
		if err != nil {
			return apierr.Internal(fmt.Errorf("read keys: %w", err))
		}
		switch sftp.CheckKey(req.Password, prints) {
		case sftp.KeyMatch:
		case sftp.KeyUnknown:
			// Clients offer every key they have; the node's SSH library asks for the proof of owning it afterwards.
			return apierr.Forbidden(sftp.ErrRejected.Error())
		default:
			return h.rejected(counters)
		}
	}

	grant, err := sftp.Authorize(
		sftp.User{ID: user.ID, UUID: user.UUID, Admin: user.Admin},
		sftp.Access{ServerUUID: server.UUID, OwnerID: server.OwnerID, State: server.State, Subuser: server.Subuser},
	)
	var stateErr *servers.StateError
	switch {
	case errors.Is(err, sftp.ErrDenied):
		h.logEvent(c, Event{Name: activity.ServerSFTPDenied, ServerSubject: server.ID, Actor: user.ID, IP: client, Properties: "{}"})
		return apierr.Forbidden(sftp.ErrDenied.Error())
	case errors.As(err, &stateErr):
		return apierr.Conflict(stateErr.Message)
	case err != nil:
		return apierr.Internal(err)
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, grant)
}

// lookup finds the user and the server of the node. One that is missing comes back as the zero value.
func (h *Handlers) lookup(c echo.Context, nodeID uint64, login sftp.Login) (SFTPUser, SFTPServer, error) {
	ctx := c.Request().Context()
	user, err := h.d.Users.ByUsername(ctx, login.Username)
	switch {
	case errors.Is(err, ErrNotFound):
		return SFTPUser{}, SFTPServer{}, nil
	case err != nil:
		return SFTPUser{}, SFTPServer{}, apierr.Internal(fmt.Errorf("find user: %w", err))
	}
	server, err := h.d.Servers.SFTP(ctx, nodeID, login.Server, user.ID)
	switch {
	case errors.Is(err, ErrNotFound):
		return user, SFTPServer{}, nil
	case err != nil:
		return SFTPUser{}, SFTPServer{}, apierr.Internal(fmt.Errorf("find server: %w", err))
	}
	// The store finds the servers of the node only; a server of another node is as good as none even if it did not.
	if (remote.Scope{NodeID: nodeID}).Owns(server.NodeID) != nil {
		return user, SFTPServer{}, nil
	}
	return user, server, nil
}

// rejected counts a failed sign-in and answers it, the same way whatever was wrong.
func (h *Handlers) rejected(counters []lockout.Counter) error {
	h.d.Failures.Fail(counters)
	return apierr.Forbidden(sftp.ErrRejected.Error())
}
