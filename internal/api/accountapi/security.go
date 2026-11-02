package accountapi

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/account"
	"github.com/Helles01/raptor-panel/internal/activity"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/field"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/api/validate"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/auth/lockout"
)

// passwordCheck compares what was sent with the user's password. A wrong one is counted, so that a stolen session or key cannot be
// used to guess the password (C-049). The time of the comparison is spent also when nothing was sent.
func (h *Handlers) passwordCheck(a Account, sent string, counters []lockout.Counter) bool {
	if auth.VerifyPassword(a.PasswordHash, sent) {
		return true
	}
	h.d.Failures.Fail(counters)
	return false
}

type emailBody struct {
	Email    field.String `json:"email"`
	Password field.Secret `json:"password"`
}

// UpdateEmail is PUT /account/email, in the order of docs/modules/04b-account.md. The current password is asked for first, then the
// address; changing it more than three times a day is refused, so that nobody can find out which addresses are in use by changing their
// own again and again.
func (h *Handlers) UpdateEmail(c echo.Context) error {
	p, a, err := h.caller(c)
	if err != nil {
		return err
	}
	counters, err := h.confirmCounters(c, p)
	if err != nil {
		return err
	}
	var body emailBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	if !h.passwordCheck(a, body.Password.V, counters) {
		return apierr.BadRequest(account.ErrInvalidPassword.Error())
	}
	v := validate.New()
	v.Sent("email", body.Email.State)
	v.Type("email", body.Email.State, validate.KindString, false)
	if err := v.Err(); err != nil {
		return err
	}

	changes := account.EmailChangeCounter(a.UUID)
	used, _ := h.d.Failures.Blocked([]lockout.Counter{changes})
	plan, err := account.PlanEmailChange(a.Email, body.Email.V, used)
	switch {
	case errors.Is(err, account.ErrEmailChanges):
		return apierr.TooManyRequests(account.EmailChangeWindow)
	case err != nil:
		return validate.FromField(err)
	}
	if !plan.Change {
		return c.NoContent(http.StatusNoContent)
	}

	h.d.Failures.Fail([]lockout.Counter{changes})
	if err := h.d.Store.SetEmail(c.Request().Context(), p.User.ID, plan.Old, plan.New); err != nil {
		if errors.Is(err, ErrEmailTaken) {
			v := validate.New()
			v.Custom("email", "unique", "The email has already been taken.")
			return v.Err()
		}
		return storeError(err)
	}
	if props, perr := activity.Properties(activity.UserEmailChanged, map[string]any{"old": plan.Old, "new": plan.New}); perr != nil {
		h.d.Logger.Error("activity entry refused", "event", activity.UserEmailChanged, "error", perr)
	} else {
		h.log(c, p, activity.UserEmailChanged, 0, props)
	}
	return c.NoContent(http.StatusNoContent)
}

type passwordBody struct {
	Current      field.Secret `json:"current_password"`
	Password     field.Secret `json:"password"`
	Confirmation field.Secret `json:"password_confirmation"`
}

// UpdatePassword is PUT /account/password. The other sessions of the user end, the nodes drop their tokens and SFTP sessions, and
// reset links asked for before stop working (C-049).
func (h *Handlers) UpdatePassword(c echo.Context) error {
	p, a, err := h.caller(c)
	if err != nil {
		return err
	}
	counters, err := h.confirmCounters(c, p)
	if err != nil {
		return err
	}
	var body passwordBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	currentOK := h.passwordCheck(a, body.Current.V, counters)
	if currentOK {
		v := validate.New()
		v.Sent("password", body.Password.State)
		v.Type("password", body.Password.State, validate.KindString, false)
		if err := v.Err(); err != nil {
			return err
		}
	}

	plan, err := account.PlanPasswordChange(currentOK, body.Password.V, body.Confirmation.V)
	var pwErr *account.PasswordError
	switch {
	case errors.Is(err, account.ErrInvalidPassword):
		return apierr.BadRequest(err.Error())
	case errors.As(err, &pwErr):
		fields := make([]apierr.FieldError, len(pwErr.Messages))
		for i, m := range pwErr.Messages {
			fields[i] = apierr.FieldError{Field: "password", Rule: "invalid", Detail: m}
		}
		return apierr.Validation(fields)
	case err != nil:
		return apierr.Internal(err)
	}

	ctx := c.Request().Context()
	if err := h.d.Store.SetPassword(ctx, p.User.ID, plan.Hash); err != nil {
		return storeError(err)
	}
	// The password is changed whatever happens next, so a failure here is logged and not answered with an error: the person would
	// try again with a password that no longer is the old one.
	if plan.RevokeOtherSessions {
		if err := h.d.Sessions.RevokeOthers(ctx, p.User.ID, middleware.CurrentSession(c)); err != nil {
			h.d.Logger.Error("the other sessions of a user were not ended after a password change", "user", p.User.ID, "error", err)
		}
	}
	if plan.DeauthorizeNodes {
		if err := h.d.Nodes.DeauthorizeUser(ctx, p.User.ID); err != nil {
			h.d.Logger.Error("the nodes were not told to drop the tokens of a user after a password change", "user", p.User.ID, "error", err)
		}
	}
	if props, perr := activity.Properties(activity.UserPasswordChanged, nil); perr != nil {
		h.d.Logger.Error("activity entry refused", "event", activity.UserPasswordChanged, "error", perr)
	} else {
		h.log(c, p, activity.UserPasswordChanged, 0, props)
	}
	return c.NoContent(http.StatusNoContent)
}

// TwoFactorSetup is GET /account/two-factor: a new secret and the address of the QR code. Refused while two-factor authentication is on,
// so that the secret in use cannot be replaced by asking for another.
func (h *Handlers) TwoFactorSetup(c echo.Context) error {
	p, a, err := h.caller(c)
	if err != nil {
		return err
	}
	setup, err := account.PlanTwoFactorSetup(a.UsesTOTP, h.d.Issuer, a.Email, h.d.Random)
	switch {
	case errors.Is(err, account.ErrTwoFactorEnabled):
		return apierr.BadRequest(err.Error())
	case err != nil:
		return apierr.Internal(err)
	}
	if err := h.d.Store.SetTOTPSecret(c.Request().Context(), p.User.ID, setup.Secret); err != nil {
		return storeError(err)
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, map[string]any{"data": map[string]string{"image_url_data": setup.URI, "secret": setup.Secret}})
}

type enableBody struct {
	Code     field.String `json:"code"`
	Password field.Secret `json:"password"`
}

// TwoFactorEnable is POST /account/two-factor: the code from the app and the password switch it on, and ten recovery tokens are shown
// once. The code that does it cannot be used to sign in afterwards.
func (h *Handlers) TwoFactorEnable(c echo.Context) error {
	p, a, err := h.caller(c)
	if err != nil {
		return err
	}
	counters, err := h.confirmCounters(c, p)
	if err != nil {
		return err
	}
	var body enableBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	v := validate.New()
	v.Sent("code", body.Code.State)
	v.Type("code", body.Code.State, validate.KindString, false)
	v.Sent("password", body.Password.State)
	v.Type("password", body.Password.State, validate.KindString, false)
	if err := v.Err(); err != nil {
		return err
	}

	passwordOK := h.passwordCheck(a, body.Password.V, counters)
	plan, err := account.PlanTwoFactorEnable(passwordOK, body.Code.V, a.TOTPSecret, h.d.Now(), h.d.Random)
	switch {
	case errors.Is(err, account.ErrTwoFactorToken):
		h.d.Failures.Fail(counters)
		return apierr.BadRequest(err.Error())
	case errors.Is(err, account.ErrTwoFactorPassword), errors.Is(err, account.ErrNotSetUp):
		return apierr.BadRequest(err.Error())
	case err != nil:
		if fe := validate.FromField(err); fe != err {
			return fe
		}
		return apierr.Internal(err)
	}
	if err := h.d.Store.EnableTOTP(c.Request().Context(), p.User.ID, plan.Hashes, plan.UsedAt); err != nil {
		return storeError(err)
	}
	if props, perr := activity.Properties(activity.UserTwoFactorCreate, nil); perr != nil {
		h.d.Logger.Error("activity entry refused", "event", activity.UserTwoFactorCreate, "error", perr)
	} else {
		h.log(c, p, activity.UserTwoFactorCreate, 0, props)
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, map[string]any{"object": "recovery_tokens", "attributes": map[string]any{"tokens": plan.Tokens}})
}

type disableBody struct {
	Password field.Secret `json:"password"`
}

// TwoFactorDisable is POST /account/two-factor/disable: switched off with the password. The secret and the recovery tokens are
// deleted with it (C-049).
func (h *Handlers) TwoFactorDisable(c echo.Context) error {
	p, a, err := h.caller(c)
	if err != nil {
		return err
	}
	counters, err := h.confirmCounters(c, p)
	if err != nil {
		return err
	}
	var body disableBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	if _, err := account.PlanTwoFactorDisable(h.passwordCheck(a, body.Password.V, counters)); err != nil {
		return apierr.BadRequest(err.Error())
	}
	if err := h.d.Store.DisableTOTP(c.Request().Context(), p.User.ID); err != nil {
		return storeError(err)
	}
	if props, perr := activity.Properties(activity.UserTwoFactorDelete, nil); perr != nil {
		h.d.Logger.Error("activity entry refused", "event", activity.UserTwoFactorDelete, "error", perr)
	} else {
		h.log(c, p, activity.UserTwoFactorDelete, 0, props)
	}
	return c.NoContent(http.StatusNoContent)
}
