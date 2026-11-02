package appusers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/activity"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/field"
	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/api/resp"
	"github.com/Helles01/raptor-panel/internal/api/validate"
	"github.com/Helles01/raptor-panel/internal/auth/passwordreset"
	"github.com/Helles01/raptor-panel/internal/mail"
	"github.com/Helles01/raptor-panel/internal/users"
)

// maxBody is the largest body these routes read.
const maxBody = 64 << 10

// List is GET /users, with the filters, sorts and page of the Application API.
func (h *Handlers) List(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	q, err := listquery.Parse(c.QueryParams(), listquery.Users)
	if err != nil {
		return err
	}
	page, perPage, err := resp.PageParams(c.QueryParams())
	if err != nil {
		return err
	}
	v := appkit.NewView(p)
	inc := appkit.Wanted(c.QueryParams(), listquery.Users)
	list, total, err := h.d.Store.List(c.Request().Context(), q, page, perPage, inc.Load(v, listquery.Users))
	if err != nil {
		return appkit.StoreError(err)
	}
	items := make([]resp.Item, len(list))
	for i, u := range list {
		items[i] = v.User(u, inc)
	}
	return appkit.List(c, items, total, perPage, page)
}

// View is GET /users/{user}.
func (h *Handlers) View(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	id, err := appkit.ID(c, "user")
	if err != nil {
		return err
	}
	v := appkit.NewView(p)
	inc := appkit.Wanted(c.QueryParams(), listquery.Users)
	u, err := h.d.Store.Find(c.Request().Context(), id, inc.Load(v, listquery.Users))
	if err != nil {
		return appkit.StoreError(err)
	}
	return c.JSON(http.StatusOK, v.User(u, inc))
}

// External is GET /users/external/{external}: the user an outside system calls by its own identifier.
func (h *Handlers) External(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	v := appkit.NewView(p)
	inc := appkit.Wanted(c.QueryParams(), listquery.Users)
	u, err := h.d.Store.FindExternal(c.Request().Context(), c.Param("external"), inc.Load(v, listquery.Users))
	if err != nil {
		return appkit.StoreError(err)
	}
	return c.JSON(http.StatusOK, v.User(u, inc))
}

type body struct {
	ExternalID field.String `json:"external_id"`
	Email      field.String `json:"email"`
	Username   field.String `json:"username"`
	FirstName  field.String `json:"first_name"`
	LastName   field.String `json:"last_name"`
	Password   field.Secret `json:"password"`
	Language   field.String `json:"language"`
	RootAdmin  field.Bool   `json:"root_admin"`
}

// read decodes a body and checks that the four fields every request needs are there and that each field is of its kind. What a value
// may be is up to internal/users.
func read(c echo.Context) (body, error) {
	var b body
	if err := validate.DecodeJSON(c.Request().Body, &b, maxBody); err != nil {
		return body{}, err
	}
	v := validate.New()
	for _, f := range []struct {
		name string
		s    field.String
	}{{"email", b.Email}, {"username", b.Username}, {"first_name", b.FirstName}, {"last_name", b.LastName}} {
		v.Sent(f.name, f.s.State)
		v.Type(f.name, f.s.State, validate.KindString, false)
	}
	v.Type("external_id", b.ExternalID.State, validate.KindString, true)
	v.Type("password", b.Password.State, validate.KindString, true)
	v.Type("language", b.Language.State, validate.KindString, true)
	v.Type("root_admin", b.RootAdmin.State, validate.KindBoolean, false)
	return b, v.Err()
}

// Create is POST /users. A password that is not given makes an account that its owner finishes through a link in the mail.
func (h *Handlers) Create(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	b, err := read(c)
	if err != nil {
		return err
	}
	in := users.Input{
		ExternalID: b.ExternalID.V, Email: b.Email.V, Username: b.Username.V, FirstName: b.FirstName.V, LastName: b.LastName.V,
		Password: b.Password.V, Language: b.Language.V, Admin: b.RootAdmin.V,
	}
	rec, err := users.PrepareAccount(in, h.d.Languages, h.d.Random)
	if err != nil {
		return appkit.FieldError(err)
	}
	req := CreateRequest{User: rec}
	var setup string
	if rec.NeedsSetup {
		token, err := passwordreset.NewToken(h.d.Random)
		if err != nil {
			return apierr.Internal(err)
		}
		req.SetupTokenHash, setup = token.Hash, token.Plain
	}
	u, err := h.d.Store.Create(c.Request().Context(), req)
	if err != nil {
		return appkit.StoreError(err)
	}

	// The account is made; a mail that cannot be made or queued, or an entry that is not stored, is only logged.
	m, merr := h.d.Site.AccountCreated(mail.User{Name: u.FirstName, Username: u.Username, Email: u.Email}, setup)
	if merr == nil {
		merr = h.d.Outbox.Send(c.Request().Context(), m)
	}
	if merr != nil {
		h.d.Logger.Error("the mail about a new account was not queued", "user", u.ID, "error", merr)
	}
	props, perr := activity.Properties(activity.UserCreate, map[string]any{"email": u.Email, "username": u.Username, "admin": u.Admin})
	if perr != nil {
		h.d.Logger.Error("activity entry refused", "event", activity.UserCreate, "error", perr)
	} else {
		h.log(c, p, u.ID, activity.UserCreate, props)
	}
	return appkit.Created(c, appkit.NewView(p).User(u, nil), h.d.BaseURL, "/api/application/users/"+strconv.FormatUint(u.ID, 10))
}

// Update is PATCH /users/{user}. As in the reference the address, user name and both names are needed every time.
func (h *Handlers) Update(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	id, err := appkit.ID(c, "user")
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

	ch := users.Change{Email: b.Email.V, Username: b.Username.V, FirstName: b.FirstName.V, LastName: b.LastName.V, Password: b.Password.V}
	switch b.ExternalID.State {
	case field.Valid:
		ch.ExternalID = &b.ExternalID.V
	case field.Null:
		empty := ""
		ch.ExternalID = &empty
	}
	if b.Language.State == field.Valid {
		ch.Language = &b.Language.V
	}
	if b.RootAdmin.State == field.Valid {
		ch.Admin = &b.RootAdmin.V
	}
	account := users.Account{ExternalID: cur.ExternalID, Email: cur.Email, Username: cur.Username, FirstName: cur.FirstName, LastName: cur.LastName, Language: cur.Language, Admin: cur.Admin}
	up, fx, err := users.PlanUpdate(account, ch, h.d.Languages)
	if err != nil {
		return appkit.FieldError(err)
	}
	u, err := h.d.Store.Update(ctx, id, up, fx)
	if err != nil {
		return appkit.StoreError(err)
	}
	return c.JSON(http.StatusOK, appkit.NewView(p).User(u, nil))
}

// Delete is DELETE /users/{user}: a user who owns servers is not deleted.
func (h *Handlers) Delete(c echo.Context) error {
	if _, err := appkit.Caller(c); err != nil {
		return err
	}
	id, err := appkit.ID(c, "user")
	if err != nil {
		return err
	}
	err = h.d.Store.Delete(c.Request().Context(), id, users.CheckCanDelete)
	switch {
	case errors.Is(err, users.ErrHasServers):
		return apierr.Display(users.ErrHasServers.Error())
	case err != nil:
		return appkit.StoreError(err)
	}
	return c.NoContent(http.StatusNoContent)
}
