package subuserapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/activity"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/field"
	"github.com/Helles01/raptor-panel/internal/api/resp"
	"github.com/Helles01/raptor-panel/internal/api/validate"
	"github.com/Helles01/raptor-panel/internal/auth/passwordreset"
	"github.com/Helles01/raptor-panel/internal/mail"
	"github.com/Helles01/raptor-panel/internal/permissions"
	"github.com/Helles01/raptor-panel/internal/servers"
	"github.com/Helles01/raptor-panel/internal/users"
)

// maxBody holds a full set of permissions with room to spare.
const maxBody = 16 << 10

// maxAttempts bounds the tries to make an account for an address: a user name that is taken is tried again with another, and an
// address that got an account meanwhile is looked up again.
const maxAttempts = 5

var userUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// target reads the {user} of the route, the UUID of the subuser. What cannot be one is not looked up.
func target(c echo.Context) (string, error) {
	u := strings.ToLower(c.Param("user"))
	if !userUUID.MatchString(u) {
		return "", apierr.NotFound()
	}
	return u, nil
}

// List is GET /users: the subusers of the server.
func (h *Handlers) List(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	subusers, err := h.d.Store.List(c.Request().Context(), p.Server.ID)
	if err != nil {
		return storeError(err)
	}
	items := make([]resp.Item, len(subusers))
	for i, s := range subusers {
		it, err := h.item(s)
		if err != nil {
			return err
		}
		items[i] = it
	}
	return c.JSON(200, resp.NewList(items))
}

// View is GET /users/{user}.
func (h *Handlers) View(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	uuid, err := target(c)
	if err != nil {
		return err
	}
	s, err := h.d.Store.Find(c.Request().Context(), p.Server.ID, uuid)
	if err != nil {
		return storeError(err)
	}
	it, err := h.item(s)
	if err != nil {
		return err
	}
	return c.JSON(200, it)
}

// permissionList is the "permissions" of a body, which has to be a list of texts.
type permissionList struct {
	// Items are the texts of the list; an element that is not a text is left out and noted in BadIndex.
	Items []string
	// Problem is the message of the first rule it breaks, empty if none.
	Problem string
}

// parsePermissions reads the field leniently, so that the authorization can run on what was asked before the validation tells what
// is wrong with it, in the order the reference works in.
func parsePermissions(raw json.RawMessage) permissionList {
	switch {
	case len(raw) == 0 || string(raw) == "null":
		return permissionList{Problem: "The permissions field is required."}
	case raw[0] != '[':
		return permissionList{Problem: "The permissions must be an array."}
	}
	var elements []json.RawMessage
	if err := json.Unmarshal(raw, &elements); err != nil {
		return permissionList{Problem: "The permissions must be an array."}
	}
	var out permissionList
	for i, e := range elements {
		var s string
		if json.Unmarshal(e, &s) != nil {
			if out.Problem == "" {
				out.Problem = fmt.Sprintf("The permissions.%d must be a string.", i)
			}
			continue
		}
		out.Items = append(out.Items, s)
	}
	if len(elements) == 0 {
		out.Problem = "The permissions field is required."
	}
	return out
}

type changeBody struct {
	Email       field.String    `json:"email"`
	Permissions json.RawMessage `json:"permissions"`
}

// authorize applies the rules of who may change whom, which come before the validation of the request.
func authorize(p authz.Principal, targetUUID string, targetHeld, requested []string) error {
	err := servers.AuthorizeSubuserChange(servers.SubuserChange{
		ActorUUID: p.User.UUID, Held: p.Permissions, TargetUUID: targetUUID, TargetHeld: targetHeld, Requested: requested,
	})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, servers.ErrSelfEdit):
		return apierr.Forbidden("This action is unauthorized.")
	}
	return apierr.Forbidden(err.Error())
}

// Create is POST /users: adds a user to the server, making an account first for an address that has none.
func (h *Handlers) Create(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	// Ten additions in fifteen minutes for a server, whoever adds. Only calls that passed the authorizer count.
	if allowed, retry := h.d.CreateLimiter.Allow(p.Server.UUID); !allowed {
		return apierr.TooManyRequests(retry)
	}
	var body changeBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	list := parsePermissions(body.Permissions)
	if err := authorize(p, "", nil, list.Items); err != nil {
		return err
	}

	v := validate.New()
	v.Sent("email", body.Email.State)
	v.Type("email", body.Email.State, validate.KindString, false)
	if body.Email.State == field.Valid {
		v.Email("email", body.Email.V)
	}
	if list.Problem != "" {
		v.Custom("permissions", "invalid", list.Problem)
	}
	if err := v.Err(); err != nil {
		return err
	}

	stored := permissions.Normalize(list.Items)
	sub, newAccount, err := h.add(c, p, body.Email.V, stored)
	if err != nil {
		return err
	}

	name, err := h.d.Store.ServerName(c.Request().Context(), p.Server.ID)
	if err != nil {
		h.d.Logger.Error("the name of a server was not read for a mail", "server", p.Server.ID, "error", err)
	} else {
		m, merr := h.d.Site.AddedToServer(sub.User.Email, sub.User.Username, name, p.Server.ShortID)
		h.send(c, "added", m, merr)
	}
	if newAccount != nil {
		m, merr := h.d.Site.AccountCreated(mail.User{Name: "Server Subuser", Username: sub.User.Username, Email: sub.User.Email}, newAccount.token)
		h.send(c, "account", m, merr)
	}
	if props, perr := activity.Properties(activity.ServerSubuserCreate, map[string]any{"email": body.Email.V, "permissions": stored}); perr != nil {
		h.d.Logger.Error("activity entry refused", "event", activity.ServerSubuserCreate, "error", perr)
	} else {
		h.log(c, p, sub.UserID, activity.ServerSubuserCreate, props)
	}

	it, err := h.item(sub)
	if err != nil {
		return err
	}
	return c.JSON(200, it)
}

type createdAccount struct{ token string }

// add stores the subuser. An address that has an account is used as it is; one that has none gets one, with a token to choose its
// password that goes out in the mail.
func (h *Handlers) add(c echo.Context, p authz.Principal, email string, stored []string) (Subuser, *createdAccount, error) {
	ctx := c.Request().Context()
	for attempt := 0; attempt < maxAttempts; attempt++ {
		ref, err := h.d.Store.UserByEmail(ctx, email)
		switch {
		case err == nil:
			already, err := h.d.Store.IsSubuser(ctx, p.Server.ID, ref.ID)
			if err != nil {
				return Subuser{}, nil, apierr.Internal(fmt.Errorf("check subuser: %w", err))
			}
			if err := servers.CheckNewSubuser(p.Server.OwnerID, ref.ID, already); err != nil {
				return Subuser{}, nil, apierr.Display(err.Error())
			}
			sub, err := h.d.Store.Add(ctx, AddRequest{ServerID: p.Server.ID, UserID: ref.ID, Permissions: stored})
			switch {
			case errors.Is(err, ErrExists):
				return Subuser{}, nil, apierr.Display(servers.ErrSubuserExists.Error())
			case err != nil:
				return Subuser{}, nil, apierr.Internal(fmt.Errorf("add subuser: %w", err))
			}
			return sub, nil, nil

		case !errors.Is(err, ErrNotFound):
			return Subuser{}, nil, apierr.Internal(fmt.Errorf("find user: %w", err))
		}

		rec, token, err := h.newAccount(email)
		if err != nil {
			return Subuser{}, nil, err
		}
		sub, err := h.d.Store.Add(ctx, AddRequest{ServerID: p.Server.ID, NewUser: &rec, SetupTokenHash: token.Hash, Permissions: stored})
		switch {
		case errors.Is(err, ErrUsernameTaken), errors.Is(err, ErrEmailTaken):
			continue
		case err != nil:
			return Subuser{}, nil, apierr.Internal(fmt.Errorf("add subuser: %w", err))
		}
		return sub, &createdAccount{token: token.Plain}, nil
	}
	return Subuser{}, nil, apierr.Internal(errors.New("no account could be made for the address in several tries"))
}

// newAccount prepares the account of an address that has none, and the token with which its owner chooses a password.
func (h *Handlers) newAccount(email string) (users.Record, passwordreset.Token, error) {
	username, err := users.GenerateUsername(email, h.d.Random)
	if err != nil {
		return users.Record{}, passwordreset.Token{}, apierr.Internal(err)
	}
	rec, err := users.PrepareAccount(users.Input{Email: email, Username: username, FirstName: "Server", LastName: "Subuser"}, nil, h.d.Random)
	if err != nil {
		if fieldErr := validate.FromField(err); fieldErr != err {
			return users.Record{}, passwordreset.Token{}, fieldErr
		}
		return users.Record{}, passwordreset.Token{}, apierr.Internal(err)
	}
	token, err := passwordreset.NewToken(h.d.Random)
	if err != nil {
		return users.Record{}, passwordreset.Token{}, apierr.Internal(err)
	}
	return rec, token, nil
}

// Update is POST /users/{user}: changes the permissions of a subuser. The tokens and SFTP access they hold are revoked, but only when
// the permissions really change.
func (h *Handlers) Update(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	uuid, err := target(c)
	if err != nil {
		return err
	}
	var body changeBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	ctx := c.Request().Context()
	sub, err := h.d.Store.Find(ctx, p.Server.ID, uuid)
	if err != nil {
		return storeError(err)
	}
	list := parsePermissions(body.Permissions)
	if err := authorize(p, uuid, sub.Permissions, list.Items); err != nil {
		return err
	}
	if list.Problem != "" {
		v := validate.New()
		v.Custom("permissions", "invalid", list.Problem)
		return v.Err()
	}

	stored := permissions.Normalize(list.Items)
	if !slices.Equal(sorted(stored), sorted(sub.Permissions)) {
		if err := h.d.Store.SetPermissions(ctx, p.Server.ID, sub.UserID, stored, true); err != nil {
			return storeError(err)
		}
		props, perr := activity.Properties(activity.ServerSubuserUpdate, map[string]any{
			"email": sub.User.Email, "old": sorted(sub.Permissions), "new": sorted(stored), "revoked": true,
		})
		if perr != nil {
			h.d.Logger.Error("activity entry refused", "event", activity.ServerSubuserUpdate, "error", perr)
		} else {
			h.log(c, p, sub.UserID, activity.ServerSubuserUpdate, props)
		}
		sub.Permissions = stored
	}
	it, err := h.item(sub)
	if err != nil {
		return err
	}
	return c.JSON(200, it)
}

// Delete is DELETE /users/{user}: takes a subuser off the server and revokes what they hold on the node.
func (h *Handlers) Delete(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	uuid, err := target(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	sub, err := h.d.Store.Find(ctx, p.Server.ID, uuid)
	if err != nil {
		return storeError(err)
	}
	if err := authorize(p, uuid, sub.Permissions, nil); err != nil {
		return err
	}
	if err := h.d.Store.Remove(ctx, p.Server.ID, sub.UserID, true); err != nil {
		return storeError(err)
	}

	if props, perr := activity.Properties(activity.ServerSubuserDelete, map[string]any{"email": sub.User.Email, "revoked": true}); perr != nil {
		h.d.Logger.Error("activity entry refused", "event", activity.ServerSubuserDelete, "error", perr)
	} else {
		h.log(c, p, sub.UserID, activity.ServerSubuserDelete, props)
	}
	if name, err := h.d.Store.ServerName(ctx, p.Server.ID); err != nil {
		h.d.Logger.Error("the name of a server was not read for a mail", "server", p.Server.ID, "error", err)
	} else {
		m, merr := h.d.Site.RemovedFromServer(sub.User.Email, sub.User.Username, name)
		h.send(c, "removed", m, merr)
	}
	return noContent(c)
}
