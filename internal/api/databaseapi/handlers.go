package databaseapi

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/activity"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/field"
	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/api/resp"
	"github.com/Helles01/raptor-panel/internal/api/validate"
	"github.com/Helles01/raptor-panel/internal/dbhosts"
	"github.com/Helles01/raptor-panel/internal/permissions"
)

type hostAttributes struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}

type databaseAttributes struct {
	ID              string         `json:"id"`
	Host            hostAttributes `json:"host"`
	Name            string         `json:"name"`
	Username        string         `json:"username"`
	ConnectionsFrom string         `json:"connections_from"`
	MaxConnections  *int           `json:"max_connections"`
}

type passwordAttributes struct {
	Password string `json:"password"`
}

// item is the object of a database. With withPassword the password is attached, for someone who may see it (the others get a null
// resource, as the reference's serializer writes one).
func (h *Handlers) item(c echo.Context, p authz.Principal, r Record, withPassword bool) (resp.Item, error) {
	it := resp.NewItem("server_database", databaseAttributes{
		ID: strconv.FormatUint(r.ID, 10), Host: hostAttributes{Address: r.HostAddress, Port: r.HostPort},
		Name: r.Name, Username: r.Username, ConnectionsFrom: r.Remote, MaxConnections: r.MaxConnections,
	})
	if !withPassword {
		return it, nil
	}
	if !permissions.Has(p.Permissions, permissions.DatabaseViewPassword) {
		return it.With("password", resp.NewItem("null_resource", nil)), nil
	}
	pw, err := h.d.Store.Password(c.Request().Context(), r.ID)
	if err != nil {
		return resp.Item{}, storeError(err)
	}
	return it.With("password", resp.NewItem("database_password", passwordAttributes{Password: pw})), nil
}

// log writes an entry about a database. A failure is logged and never reaches the person, whose action has happened.
func (h *Handlers) log(c echo.Context, p authz.Principal, event string, r Record) {
	props, err := activity.Properties(event, map[string]any{"name": r.Name})
	if err != nil {
		h.d.Logger.Error("activity entry refused", "event", event, "error", err)
		return
	}
	e := Entry{Event: event, ServerID: p.Server.ID, DatabaseID: r.ID, ActorID: p.User.ID, IP: c.RealIP(), Properties: props}
	if p.Key != nil {
		id := p.Key.ID
		e.APIKeyID = &id
	}
	if err := h.d.Activity.Log(c.Request().Context(), e); err != nil {
		h.d.Logger.Error("activity entry not stored", "event", event, "server", p.Server.ID, "error", err)
	}
}

// List is GET /databases, with the passwords for those who may see them when ?include=password asks for them.
func (h *Handlers) List(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	withPassword := false
	for _, in := range listquery.ParseIncludes(c.QueryParams(), []string{"password"}) {
		withPassword = withPassword || in.Name == "password"
	}
	records, err := h.d.Store.List(c.Request().Context(), p.Server.ID)
	if err != nil {
		return storeError(err)
	}
	items := make([]resp.Item, len(records))
	for i, r := range records {
		if items[i], err = h.item(c, p, r, withPassword); err != nil {
			return err
		}
	}
	if withPassword {
		c.Response().Header().Set("Cache-Control", "no-store")
	}
	return c.JSON(http.StatusOK, resp.NewList(items))
}

type createBody struct {
	Database field.String `json:"database"`
	Remote   field.String `json:"remote"`
}

var (
	alphaDash  = regexp.MustCompile(`^[\p{L}\p{M}\p{N}_-]+$`)
	remoteForm = regexp.MustCompile(`^[\w\-/.%:]+$`)
)

// Create is POST /databases: the database, its user and its rights are made on a host, and the user's password is shown in the answer.
// If the host cannot be told, the row is taken back and what was made on the host is dropped.
func (h *Handlers) Create(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	// Two a minute for a server, whoever creates. Only calls that passed the authorizer count.
	if allowed, retry := h.d.CreateLimiter.Allow(p.Server.UUID); !allowed {
		return apierr.TooManyRequests(retry)
	}
	var body createBody
	if err := validate.DecodeJSON(c.Request().Body, &body, 16<<10); err != nil {
		return err
	}
	v := validate.New()
	v.Sent("database", body.Database.State)
	v.Type("database", body.Database.State, validate.KindString, false)
	if body.Database.State == field.Valid {
		v.Length("database", body.Database.V, 3, 48)
		if !alphaDash.MatchString(body.Database.V) {
			v.Custom("database", "alpha_dash", "The database may only contain letters, numbers, dashes and underscores.")
		}
	}
	v.Sent("remote", body.Remote.State)
	v.Type("remote", body.Remote.State, validate.KindString, false)
	if body.Remote.State == field.Valid {
		if !remoteForm.MatchString(body.Remote.V) {
			v.Custom("remote", "regex", "The remote format is invalid.")
		} else if err := dbhosts.ValidateRemote(body.Remote.V); err != nil {
			v.Custom("remote", "invalid", "The remote format is invalid.")
		}
	}
	if err := v.Err(); err != nil {
		return err
	}

	ctx := c.Request().Context()
	name, err := dbhosts.UniqueName(body.Database.V, p.Server.ID)
	if err != nil {
		w := validate.New()
		w.Custom("database", "invalid", "The database is not valid.")
		return w.Err()
	}
	hosts, err := h.d.Store.Hosts(ctx)
	if err != nil {
		return apierr.Internal(fmt.Errorf("list hosts: %w", err))
	}
	host, err := dbhosts.ChooseHost(hosts, p.Server.NodeID, h.d.AllowRandom, h.d.Random)
	if err != nil {
		return apierr.Display(err.Error())
	}
	username, err := dbhosts.Username(p.Server.ID, h.d.Random)
	if err != nil {
		return apierr.Internal(err)
	}
	password, err := dbhosts.Password(h.d.Random)
	if err != nil {
		return apierr.Internal(err)
	}

	rec, err := h.d.Store.Create(ctx, CreateRequest{
		Database: NewDatabase{ServerID: p.Server.ID, HostID: host.ID, Name: name, Username: username, Password: password, Remote: body.Remote.V},
		Check:    func(limit *int64, count int64) error { return dbhosts.CheckCanCreate(h.d.Enabled, limit, count) },
	})
	switch {
	case errors.Is(err, ErrNameTaken):
		w := validate.New()
		w.Custom("database", "unique", "The database name you have selected is already in use by this server.")
		return w.Err()
	case isRule(err):
		return apierr.Display(err.Error())
	case err != nil:
		return storeError(err)
	}

	account := rec.account(p.Server.ID)
	statements, err := dbhosts.Create(account, password)
	if err == nil {
		err = h.d.Hosts.Run(ctx, rec.HostID, statements)
	}
	if err != nil {
		h.undoCreate(c, p, rec)
		return apierr.Internal(fmt.Errorf("create database on host: %w", err))
	}

	h.log(c, p, activity.ServerDatabaseCreate, rec)
	it, err := h.item(c, p, rec, true)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, it)
}

// isRule is true for the refusals of the rules of making a database, which the reference shows as a message to the person.
func isRule(err error) bool {
	for _, rule := range []error{dbhosts.ErrFeatureDisabled, dbhosts.ErrTooMany, dbhosts.ErrNoSuitableHost} {
		if errors.Is(err, rule) {
			return true
		}
	}
	return false
}

// undoCreate drops what a failed creation may have left on the host and takes the row back. The first failure is what the person is
// told, so a failure here is only logged.
func (h *Handlers) undoCreate(c echo.Context, p authz.Principal, rec Record) {
	ctx := c.Request().Context()
	if drop, err := dbhosts.Drop(rec.account(p.Server.ID)); err == nil {
		if err := h.d.Hosts.Run(ctx, rec.HostID, drop); err != nil {
			h.d.Logger.Error("a database that could not be made was not dropped on the host", "server", p.Server.ID, "database", rec.Name, "error", err)
		}
	}
	if err := h.d.Store.Delete(ctx, p.Server.ID, rec.ID); err != nil {
		h.d.Logger.Error("a database that could not be made was not taken back", "server", p.Server.ID, "database", rec.Name, "error", err)
	}
}

// RotatePassword is POST /databases/{database}/rotate-password: a new password, shown in the answer. It is stored first and set on the
// host after; if the host refuses, the old one is put back, so the panel never shows a password the host does not have.
func (h *Handlers) RotatePassword(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	id, err := databaseID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	rec, err := h.d.Store.Find(ctx, p.Server.ID, id)
	if err != nil {
		return storeError(err)
	}
	old, err := h.d.Store.Password(ctx, id)
	if err != nil {
		return storeError(err)
	}
	password, err := dbhosts.Password(h.d.Random)
	if err != nil {
		return apierr.Internal(err)
	}
	statements, err := dbhosts.ChangePassword(rec.account(p.Server.ID), password)
	if err != nil {
		return apierr.Internal(err)
	}

	if err := h.d.Store.SetPassword(ctx, id, password); err != nil {
		return storeError(err)
	}
	if err := h.d.Hosts.Run(ctx, rec.HostID, statements); err != nil {
		if undo := h.d.Store.SetPassword(ctx, id, old); undo != nil {
			h.d.Logger.Error("the old password of a database was not put back after the host refused the new one", "server", p.Server.ID, "database", rec.Name, "error", undo)
		}
		return apierr.Internal(fmt.Errorf("change password on host: %w", err))
	}
	h.log(c, p, activity.ServerDatabaseRotatePassword, rec)
	it, err := h.item(c, p, rec, true)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, it)
}

// Delete is DELETE /databases/{database}: dropped on the host first, then the row; if the host refuses the row stays and the request can
// be made again.
func (h *Handlers) Delete(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	id, err := databaseID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	rec, err := h.d.Store.Find(ctx, p.Server.ID, id)
	if err != nil {
		return storeError(err)
	}
	statements, err := dbhosts.Drop(rec.account(p.Server.ID))
	if err != nil {
		return apierr.Internal(err)
	}
	if err := h.d.Hosts.Run(ctx, rec.HostID, statements); err != nil {
		return apierr.Internal(fmt.Errorf("drop database on host: %w", err))
	}
	if err := h.d.Store.Delete(ctx, p.Server.ID, id); err != nil {
		return storeError(err)
	}
	h.log(c, p, activity.ServerDatabaseDelete, rec)
	return c.NoContent(http.StatusNoContent)
}
