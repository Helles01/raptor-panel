package accountapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/activity"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/field"
	"github.com/Helles01/raptor-panel/internal/api/resp"
	"github.com/Helles01/raptor-panel/internal/api/validate"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/sshkeys"
)

type keyAttributes struct {
	Identifier  string   `json:"identifier"`
	Description string   `json:"description"`
	AllowedIPs  []string `json:"allowed_ips"`
	LastUsedAt  *string  `json:"last_used_at"`
	CreatedAt   string   `json:"created_at"`
}

func (h *Handlers) keyItem(k KeyRecord) resp.Item {
	ips := k.AllowedIPs
	if ips == nil {
		ips = []string{}
	}
	var last *string
	if k.LastUsedAt != nil {
		s := k.LastUsedAt.In(h.d.Location).Format(atom)
		last = &s
	}
	return resp.NewItem("api_key", keyAttributes{
		Identifier: k.Identifier, Description: k.Description, AllowedIPs: ips, LastUsedAt: last, CreatedAt: k.CreatedAt.In(h.d.Location).Format(atom),
	})
}

// ListKeys is GET /account/api-keys.
func (h *Handlers) ListKeys(c echo.Context) error {
	p, _, err := h.caller(c)
	if err != nil {
		return err
	}
	keys, err := h.d.Store.APIKeys(c.Request().Context(), p.User.ID)
	if err != nil {
		return storeError(err)
	}
	items := make([]resp.Item, len(keys))
	for i, k := range keys {
		items[i] = h.keyItem(k)
	}
	return c.JSON(http.StatusOK, resp.NewList(items))
}

type keyBody struct {
	Description field.String    `json:"description"`
	AllowedIPs  json.RawMessage `json:"allowed_ips"`
}

// allowedIPs reads the list of addresses of a new key: absent or null is none, otherwise a list of texts.
func allowedIPs(raw json.RawMessage, v *validate.Validator) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var elements []json.RawMessage
	if raw[0] != '[' || json.Unmarshal(raw, &elements) != nil {
		v.Custom("allowed_ips", "array", "The allowed ips must be an array.")
		return nil
	}
	out := make([]string, 0, len(elements))
	for i, e := range elements {
		var s string
		if json.Unmarshal(e, &s) != nil {
			v.Custom("allowed_ips", "string", fmt.Sprintf("The allowed_ips.%d must be a string.", i))
			return nil
		}
		out = append(out, s)
	}
	return out
}

// CreateKey is POST /account/api-keys: a key for the user's account. The secret is shown once, in the answer, and only its hash is kept.
func (h *Handlers) CreateKey(c echo.Context) error {
	p, _, err := h.caller(c)
	if err != nil {
		return err
	}
	var body keyBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	v := validate.New()
	v.Sent("description", body.Description.State)
	v.Type("description", body.Description.State, validate.KindString, false)
	if body.Description.State == field.Valid {
		if err := auth.ValidateKeyDescription(body.Description.V); err != nil {
			v.Custom("description", "invalid", err.Error())
		}
	}
	ips := allowedIPs(body.AllowedIPs, v)
	if v.OK() && len(ips) > 0 {
		if _, err := auth.ParseAllowedIPs(ips); err != nil {
			v.Custom("allowed_ips", "invalid", err.Error())
		}
	}
	if err := v.Err(); err != nil {
		return err
	}

	identifier, secret, err := auth.NewAPIKey(h.d.Random, auth.KeyAccount)
	if err != nil {
		return apierr.Internal(err)
	}
	rec, err := h.d.Store.CreateKey(c.Request().Context(), p.User.ID, NewKey{
		Identifier: identifier, SecretHash: auth.HashKeySecret(secret), Description: body.Description.V, AllowedIPs: ips,
	}, auth.CheckCanCreateKey)
	switch {
	case errors.Is(err, auth.ErrTooManyKeys):
		return apierr.Display(err.Error())
	case err != nil:
		return storeError(err)
	}
	if props, perr := activity.Properties(activity.UserAPIKeyCreate, map[string]any{"identifier": identifier}); perr != nil {
		h.d.Logger.Error("activity entry refused", "event", activity.UserAPIKeyCreate, "error", perr)
	} else {
		h.log(c, p, activity.UserAPIKeyCreate, rec.ID, props)
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, h.keyItem(rec).WithMeta("secret_token", identifier+secret))
}

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

// DeleteKey is DELETE /account/api-keys/{identifier}: removes an account key of the user.
func (h *Handlers) DeleteKey(c echo.Context) error {
	p, _, err := h.caller(c)
	if err != nil {
		return err
	}
	identifier := c.Param("identifier")
	if !identifierPattern.MatchString(identifier) {
		return apierr.NotFound()
	}
	rec, err := h.d.Store.DeleteKey(c.Request().Context(), p.User.ID, identifier)
	if err != nil {
		return storeError(err)
	}
	if props, perr := activity.Properties(activity.UserAPIKeyDelete, map[string]any{"identifier": rec.Identifier}); perr != nil {
		h.d.Logger.Error("activity entry refused", "event", activity.UserAPIKeyDelete, "error", perr)
	} else {
		h.log(c, p, activity.UserAPIKeyDelete, rec.ID, props)
	}
	return c.NoContent(http.StatusNoContent)
}

type sshAttributes struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"public_key"`
	CreatedAt   string `json:"created_at"`
}

func (h *Handlers) sshItem(k SSHKeyRecord) resp.Item {
	return resp.NewItem("ssh_key", sshAttributes{Name: k.Name, Fingerprint: k.Fingerprint, PublicKey: k.PublicKey, CreatedAt: k.CreatedAt.In(h.d.Location).Format(atom)})
}

// ListSSHKeys is GET /account/ssh-keys.
func (h *Handlers) ListSSHKeys(c echo.Context) error {
	p, _, err := h.caller(c)
	if err != nil {
		return err
	}
	keys, err := h.d.Store.SSHKeys(c.Request().Context(), p.User.ID)
	if err != nil {
		return storeError(err)
	}
	items := make([]resp.Item, len(keys))
	for i, k := range keys {
		items[i] = h.sshItem(k)
	}
	return c.JSON(http.StatusOK, resp.NewList(items))
}

type sshBody struct {
	Name      field.String `json:"name"`
	PublicKey field.String `json:"public_key"`
}

// maxSSHName is the longest name a key may have: the column holds 191 characters.
const maxSSHName = 191

// AddSSHKey is POST /account/ssh-keys: a public key the user may sign in to SFTP with.
func (h *Handlers) AddSSHKey(c echo.Context) error {
	p, _, err := h.caller(c)
	if err != nil {
		return err
	}
	var body sshBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	v := validate.New()
	v.Sent("name", body.Name.State)
	v.Type("name", body.Name.State, validate.KindString, false)
	if body.Name.State == field.Valid {
		v.Length("name", body.Name.V, 1, maxSSHName)
	}
	v.Sent("public_key", body.PublicKey.State)
	v.Type("public_key", body.PublicKey.State, validate.KindString, false)
	var key sshkeys.Key
	if body.PublicKey.State == field.Valid {
		var perr error
		if key, perr = sshkeys.Parse(body.PublicKey.V); perr != nil {
			v.Custom("public_key", "invalid", perr.Error())
		}
	}
	if err := v.Err(); err != nil {
		return err
	}

	ctx := c.Request().Context()
	exists, err := h.d.Store.HasFingerprint(ctx, p.User.ID, key.Fingerprint)
	if err != nil {
		return storeError(err)
	}
	if exists {
		return existsError()
	}
	pem, err := key.PEM()
	if err != nil {
		return apierr.Internal(err)
	}
	rec, err := h.d.Store.AddSSHKey(ctx, p.User.ID, SSHKeyRecord{Name: body.Name.V, Fingerprint: key.Fingerprint, PublicKey: pem, CreatedAt: h.d.Now().UTC().Truncate(time.Second)})
	switch {
	case errors.Is(err, ErrKeyExists):
		return existsError()
	case err != nil:
		return storeError(err)
	}
	if props, perr := activity.Properties(activity.UserSSHKeyCreate, map[string]any{"fingerprint": key.Fingerprint}); perr != nil {
		h.d.Logger.Error("activity entry refused", "event", activity.UserSSHKeyCreate, "error", perr)
	} else {
		h.log(c, p, activity.UserSSHKeyCreate, rec.ID, props)
	}
	return c.JSON(http.StatusOK, h.sshItem(rec))
}

func existsError() error {
	v := validate.New()
	v.Custom("public_key", "unique", "The public key provided already exists on your account.")
	return v.Err()
}

type removeBody struct {
	Fingerprint field.String `json:"fingerprint"`
}

// RemoveSSHKey is POST /account/ssh-keys/remove: removes the key with the fingerprint. A fingerprint the user has no key for is not
// an error.
func (h *Handlers) RemoveSSHKey(c echo.Context) error {
	p, _, err := h.caller(c)
	if err != nil {
		return err
	}
	var body removeBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	v := validate.New()
	v.Sent("fingerprint", body.Fingerprint.State)
	v.Type("fingerprint", body.Fingerprint.State, validate.KindString, false)
	if err := v.Err(); err != nil {
		return err
	}

	rec, found, err := h.d.Store.RemoveSSHKey(c.Request().Context(), p.User.ID, body.Fingerprint.V)
	if err != nil {
		return storeError(err)
	}
	if found {
		if props, perr := activity.Properties(activity.UserSSHKeyDelete, map[string]any{"fingerprint": rec.Fingerprint}); perr != nil {
			h.d.Logger.Error("activity entry refused", "event", activity.UserSSHKeyDelete, "error", perr)
		} else {
			h.log(c, p, activity.UserSSHKeyDelete, rec.ID, props)
		}
	}
	return c.NoContent(http.StatusNoContent)
}
