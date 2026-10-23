package controlapi

import (
	"errors"
	"net/http"
	"regexp"
	"slices"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/activity"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/field"
	"github.com/Helles01/raptor-panel/internal/api/validate"
	"github.com/Helles01/raptor-panel/internal/permissions"
	"github.com/Helles01/raptor-panel/internal/schedules"
	"github.com/Helles01/raptor-panel/internal/wings"
)

// powerEvents are the signals a server takes, with the event each writes.
var powerEvents = map[string]string{
	"start":   activity.ServerPowerStart,
	"stop":    activity.ServerPowerStop,
	"restart": activity.ServerPowerRestart,
	"kill":    activity.ServerPowerKill,
}

type powerBody struct {
	Signal field.String `json:"signal"`
}

// Power is POST /api/client/servers/{server}/power.
//
// The permission needed depends on the signal, as the reference works it out (the one that schedules use for a power task). It is
// asked for once the body is read and before it is validated, so that a user who may do nothing here learns nothing about the
// signals. A signal that is not one has no permission, which only an owner or an administrator (who hold everything) can pass, and
// they are then told by the validation.
func (h *Handlers) Power(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	var body powerBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	signal := body.Signal.V // empty unless the value is a string
	if !mayPower(p.Permissions, signal) {
		return apierr.Forbidden("This action is unauthorized.")
	}

	v := validate.New()
	v.Sent("signal", body.Signal.State)
	v.Type("signal", body.Signal.State, validate.KindString, false)
	if body.Signal.State == field.Valid {
		v.OneOf("signal", signal, "start", "stop", "restart", "kill")
	}
	if err := v.Err(); err != nil {
		return err
	}

	t, err := h.target(c, p)
	if err != nil {
		return err
	}
	if err := h.d.Daemon.Power(c.Request().Context(), t.Node, t.UUID, signal); err != nil {
		return daemonError(err)
	}
	h.log(c, p, powerEvents[signal], nil)
	return c.NoContent(http.StatusNoContent)
}

// mayPower reports whether someone holding the permissions may send the signal.
func mayPower(held []string, signal string) bool {
	needed, err := schedules.Task{Action: schedules.ActionPower, Payload: signal}.Permission()
	if err != nil {
		return slices.Contains(held, permissions.Wildcard)
	}
	return permissions.Has(held, needed)
}

type commandBody struct {
	Command field.String `json:"command"`
}

// noNUL is the shape of a command the node takes: a NUL would cut it short.
var noNUL = regexp.MustCompile(`^[^\x00]*$`)

// Command is POST /api/client/servers/{server}/command. The route holds the permission control.console.
func (h *Handlers) Command(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	var body commandBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	v := validate.New()
	v.Sent("command", body.Command.State)
	v.Type("command", body.Command.State, validate.KindString, false)
	if body.Command.State == field.Valid {
		v.Bytes("command", body.Command.V, 1, wings.MaxCommandLength)
		v.Matches("command", body.Command.V, noNUL)
	}
	if err := v.Err(); err != nil {
		return err
	}

	t, err := h.target(c, p)
	if err != nil {
		return err
	}
	if err := h.d.Daemon.Command(c.Request().Context(), t.Node, t.UUID, body.Command.V); err != nil {
		// A node that answers 502 has no running server to give the command to.
		var we *wings.Error
		if errors.As(err, &we) && we.NodeStatus == http.StatusBadGateway {
			return apierr.Http(http.StatusBadGateway, "Server must be online in order to send commands.")
		}
		return daemonError(err)
	}
	h.log(c, p, activity.ServerConsoleCommand, map[string]any{"command": body.Command.V})
	return c.NoContent(http.StatusNoContent)
}

// log writes an entry with the properties of its event. The reference adds no address or browser to these entries; the address of
// the request is kept in the entry itself.
func (h *Handlers) log(c echo.Context, p authz.Principal, event string, values map[string]any) {
	props, err := activity.Properties(event, values)
	if err != nil {
		h.d.Logger.Error("activity entry refused", "event", event, "error", err)
		return
	}
	h.record(c, p, event, props)
}
