package settingsapi

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"unicode/utf8"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/activity"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/clientapi"
	"github.com/Helles01/raptor-panel/internal/api/controlapi"
	"github.com/Helles01/raptor-panel/internal/api/field"
	"github.com/Helles01/raptor-panel/internal/api/resp"
	"github.com/Helles01/raptor-panel/internal/api/validate"
	"github.com/Helles01/raptor-panel/internal/servers"
)

// Startup is GET /startup: the startup command and the variables a user may see, with the images the egg offers.
func (h *Handlers) Startup(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	s, err := h.d.Store.Server(c.Request().Context(), p.Server.ID)
	if err != nil {
		return storeError(err)
	}
	var items []resp.Item
	for _, v := range s.Variables {
		if !v.UserViewable {
			continue
		}
		it, err := clientapi.VariableItem(v)
		if err != nil {
			return apierr.Internal(err)
		}
		items = append(items, it)
	}
	list := resp.NewList(items).
		WithMeta("startup_command", clientapi.StartupCommand(s.Server, false)).
		WithMeta("docker_images", s.EggImages).
		WithMeta("raw_startup_command", s.Startup)
	return c.JSON(http.StatusOK, list)
}

type variableBody struct {
	Key   field.String `json:"key"`
	Value field.Text   `json:"value"`
}

// SetVariable is PUT /startup/variable: sets one variable of the server, checked against the rules of the egg.
func (h *Handlers) SetVariable(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	var body variableBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	v := validate.New()
	v.Sent("key", body.Key.State)
	v.Type("key", body.Key.State, validate.KindString, false)
	switch body.Value.State {
	case field.Absent:
		v.Custom("value", "present", "The value field must be present.")
	case field.Invalid:
		v.Custom("value", "invalid", "The value must be a string or a number.")
	}
	if err := v.Err(); err != nil {
		return err
	}
	var value *string
	if body.Value.State == field.Valid {
		value = &body.Value.V
	}

	ctx := c.Request().Context()
	s, err := h.d.Store.Server(ctx, p.Server.ID)
	if err != nil {
		return storeError(err)
	}
	vars := make([]servers.StartupVariable, len(s.Variables))
	for i, sv := range s.Variables {
		vars[i] = servers.StartupVariable{EnvName: sv.EnvName, UserViewable: sv.UserViewable, UserEditable: sv.UserEditable, Rules: sv.Rules, Value: sv.ServerValue}
	}
	edit, err := servers.PlanStartupEdit(vars, body.Key.V, value)
	var valueErr *servers.ValueError
	switch {
	case errors.Is(err, servers.ErrNoSuchVariable), errors.Is(err, servers.ErrReadOnlyVariable):
		return apierr.BadRequest(err.Error())
	case errors.As(err, &valueErr):
		fields := make([]apierr.FieldError, len(valueErr.Messages))
		for i, m := range valueErr.Messages {
			fields[i] = apierr.FieldError{Field: "value", Rule: "invalid", Detail: m}
		}
		return apierr.Validation(fields)
	case err != nil:
		return apierr.Internal(err)
	}

	if err := h.d.Store.SetVariable(ctx, p.Server.ID, body.Key.V, edit.Store); err != nil {
		return storeError(err)
	}
	if edit.Changed {
		props, perr := activity.Properties(activity.ServerStartupEdit, map[string]any{"variable": body.Key.V, "old": edit.Old, "new": edit.New})
		if perr != nil {
			h.d.Logger.Error("activity entry refused", "event", activity.ServerStartupEdit, "error", perr)
		} else {
			h.log(c, p, activity.ServerStartupEdit, props)
		}
	}

	// The answer shows the variable and the command as they are now.
	var shown clientapi.Variable
	for i, sv := range s.Variables {
		if sv.EnvName == body.Key.V {
			var now *string
			if edit.Store != "" {
				now = &edit.Store
			}
			s.Variables[i].ServerValue = now
			shown = s.Variables[i]
		}
	}
	it, err := clientapi.VariableItem(shown)
	if err != nil {
		return apierr.Internal(err)
	}
	return c.JSON(http.StatusOK, it.WithMeta("startup_command", clientapi.StartupCommand(s.Server, false)).WithMeta("raw_startup_command", s.Startup))
}

type renameBody struct {
	Name        field.String `json:"name"`
	Description field.String `json:"description"`
}

// Rename is POST /settings/rename: the name and, if sent, the description. Each change is logged on its own.
func (h *Handlers) Rename(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	var body renameBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	v := validate.New()
	v.Sent("name", body.Name.State)
	v.Type("name", body.Name.State, validate.KindString, false)
	v.Type("description", body.Description.State, validate.KindString, true)
	if err := v.Err(); err != nil {
		return err
	}
	// A description that is not sent is kept; one sent as null or blank is cleared.
	var description *string
	switch body.Description.State {
	case field.Valid:
		description = &body.Description.V
	case field.Null:
		empty := ""
		description = &empty
	}

	ctx := c.Request().Context()
	s, err := h.d.Store.Server(ctx, p.Server.ID)
	if err != nil {
		return storeError(err)
	}
	renamed, err := servers.PlanRename(servers.Naming{Name: s.Name, Description: s.Description}, body.Name.V, description)
	if err != nil {
		return validate.FromField(err)
	}
	if err := h.d.Store.Rename(ctx, p.Server.ID, renamed.Name, renamed.Description); err != nil {
		return storeError(err)
	}
	if renamed.NameChanged {
		h.logChange(c, p, activity.ServerSettingsRename, s.Name, renamed.Name)
	}
	if renamed.DescriptionChanged {
		h.logChange(c, p, activity.ServerSettingsDescription, s.Description, renamed.Description)
	}
	return noContent(c)
}

func (h *Handlers) logChange(c echo.Context, p authz.Principal, event, old, new string) {
	props, err := activity.Properties(event, map[string]any{"old": old, "new": new})
	if err != nil {
		h.d.Logger.Error("activity entry refused", "event", event, "error", err)
		return
	}
	h.log(c, p, event, props)
}

type imageBody struct {
	DockerImage field.String `json:"docker_image"`
}

// imagePattern is the reference's rule for the text of an image.
var imagePattern = regexp.MustCompile(`^[\w#\./\- ]*\|?~?[\w\./\-:@ ]*$`)

// SetImage is PUT /settings/docker-image: one of the images of the egg, for a server that still uses one of them.
func (h *Handlers) SetImage(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	var body imageBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	v := validate.New()
	v.Sent("docker_image", body.DockerImage.State)
	v.Type("docker_image", body.DockerImage.State, validate.KindString, false)
	if body.DockerImage.State == field.Valid {
		if utf8.RuneCountInString(body.DockerImage.V) > 191 {
			v.Custom("docker_image", "max", "The docker image must not be greater than 191 characters.")
		}
		v.Matches("docker_image", body.DockerImage.V, imagePattern)
	}
	if err := v.Err(); err != nil {
		return err
	}

	ctx := c.Request().Context()
	s, err := h.d.Store.Server(ctx, p.Server.ID)
	if err != nil {
		return storeError(err)
	}
	changed, err := servers.PlanImageChange(s.Image, s.EggImages.values(), body.DockerImage.V)
	switch {
	case errors.Is(err, servers.ErrImageLocked):
		return apierr.BadRequest(err.Error())
	case err != nil:
		return validate.FromField(err)
	}
	if changed {
		if err := h.d.Store.SetImage(ctx, p.Server.ID, body.DockerImage.V); err != nil {
			return storeError(err)
		}
		h.logChange(c, p, activity.ServerStartupImage, s.Image, body.DockerImage.V)
	}
	return noContent(c)
}

// Reinstall is POST /settings/reinstall: the server is marked as installing and its node is told to run the install script. If the
// node cannot be told, the mark is taken back.
func (h *Handlers) Reinstall(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	s, err := h.d.Store.Server(ctx, p.Server.ID)
	if err != nil {
		return storeError(err)
	}
	if err := servers.CheckCanReinstall(s.Status, s.SkipScripts); err != nil {
		return apierr.Display(err.Error())
	}

	marked, err := h.d.Store.SetStatus(ctx, p.Server.ID, s.Status, servers.StatusInstalling)
	switch {
	case err != nil:
		return storeError(err)
	case !marked:
		return apierr.Conflict("This server is changing state, please try again.")
	}
	if err := h.d.Reinstaller.Reinstall(ctx, p.Server.ID, s.UUID); err != nil {
		if _, undoErr := h.d.Store.SetStatus(ctx, p.Server.ID, servers.StatusInstalling, s.Status); undoErr != nil {
			h.d.Logger.Error("the status of a server was not put back after a reinstall failed", "server", p.Server.ID, "error", undoErr)
		}
		return controlapi.DaemonError(fmt.Errorf("reinstall: %w", err))
	}
	h.log(c, p, activity.ServerReinstall, "{}")
	return c.JSON(http.StatusAccepted, []string{})
}
