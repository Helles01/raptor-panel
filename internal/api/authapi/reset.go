package authapi

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/activity"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/field"
	"github.com/Helles01/raptor-panel/internal/api/validate"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/auth/passwordreset"
)

// LinkSent is the answer to every request for a reset link, whether or not the address has an account (the reference's text).
const LinkSent = "We have e-mailed your password reset link!"

type forgotBody struct {
	Email field.String `json:"email"`
}

// ForgotPassword is the request for a reset link, in the order of docs/modules/03c-password-reset.md. Whatever happens, the answer is
// the same.
func (h *Handlers) ForgotPassword(c echo.Context) error {
	var body forgotBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	v := validate.New()
	v.Sent("email", body.Email.State)
	v.Type("email", body.Email.State, validate.KindString, false)
	if body.Email.State == field.Valid {
		v.Email("email", body.Email.V)
	}
	if err := v.Err(); err != nil {
		return err
	}
	ctx := c.Request().Context()
	now := h.d.Now()
	email := body.Email.V

	user, err := h.d.Users.ByEmail(ctx, email)
	exists := err == nil
	if err != nil && !errors.Is(err, ErrNoUser) {
		return apierr.Internal(fmt.Errorf("find user: %w", err))
	}

	var last *time.Time
	if exists {
		rec, found, err := h.d.Resets.Latest(ctx, user.Email)
		if err != nil {
			return apierr.Internal(fmt.Errorf("load reset token: %w", err))
		}
		if found {
			last = &rec.CreatedAt
		}
	}

	// A token is made in every case, so that the work done does not tell the cases apart.
	token, err := passwordreset.NewToken(h.d.Random)
	if err != nil {
		return apierr.Internal(err)
	}

	switch passwordreset.Decide(exists, last, now) {
	case passwordreset.Send:
		// Too many links to one address are left unsent; the answer stays the same.
		if allowed, _ := h.d.EmailLimiter.Allow(strings.ToLower(user.Email)); !allowed {
			h.d.Logger.Warn("password reset links to one address are limited", "user", user.ID)
			break
		}
		if err := h.d.Resets.Replace(ctx, user.Email, passwordreset.Record{Hash: token.Hash, CreatedAt: now}); err != nil {
			return apierr.Internal(fmt.Errorf("store reset token: %w", err))
		}
		msg, err := h.d.Site.PasswordReset(user.Email, token.Plain)
		if err != nil {
			return apierr.Internal(fmt.Errorf("build reset mail: %w", err))
		}
		if err := h.d.Outbox.Send(ctx, msg); err != nil {
			return apierr.Internal(fmt.Errorf("queue reset mail: %w", err))
		}
		h.logEvent(c, activity.AuthResetPassword, user.ID, 0, nil)
	case passwordreset.SkipUnknown:
		h.d.Logger.Warn("password reset requested for an address without an account", "ip", c.RealIP())
	case passwordreset.SkipRecent:
		h.d.Logger.Info("password reset link requested again within a minute", "user", user.ID)
	}
	return ok(c, map[string]string{"status": LinkSent})
}

type resetBody struct {
	Email                field.String `json:"email"`
	Token                field.String `json:"token"`
	Password             field.Secret `json:"password"`
	PasswordConfirmation field.Secret `json:"password_confirmation"`
}

type resetResponse struct {
	Success     bool   `json:"success"`
	RedirectTo  string `json:"redirect_to"`
	SendToLogin bool   `json:"send_to_login"`
}

// ResetPassword sets a new password with the token from the mail.
func (h *Handlers) ResetPassword(c echo.Context) error {
	cur, err := requireSession(c)
	if err != nil {
		return err
	}
	var body resetBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	if err := h.validateReset(body); err != nil {
		return err
	}
	ctx := c.Request().Context()
	now := h.d.Now()

	// Every failure from here on is the same refusal: nothing says whether the address, the token or its age was wrong.
	invalid := apierr.Display(passwordreset.ErrInvalidToken.Error())

	user, err := h.d.Users.ByEmail(ctx, body.Email.V)
	switch {
	case errors.Is(err, ErrNoUser):
		return invalid
	case err != nil:
		return apierr.Internal(fmt.Errorf("find user: %w", err))
	}
	rec, found, err := h.d.Resets.Latest(ctx, user.Email)
	if err != nil {
		return apierr.Internal(fmt.Errorf("load reset token: %w", err))
	}
	var recPtr *passwordreset.Record
	if found {
		recPtr = &rec
	}
	if err := passwordreset.Redeem(recPtr, body.Token.V, now); err != nil {
		return invalid
	}
	// The token is spent before anything else happens, and only the request that spends it goes on.
	if spent, err := h.d.Resets.Consume(ctx, user.Email); err != nil {
		return apierr.Internal(fmt.Errorf("spend reset token: %w", err))
	} else if !spent {
		return invalid
	}

	hash, err := auth.HashPassword(body.Password.V)
	if err != nil {
		return apierr.Internal(fmt.Errorf("hash password: %w", err))
	}
	if err := h.d.Users.SetPassword(ctx, user.ID, hash); err != nil {
		return apierr.Internal(fmt.Errorf("store password: %w", err))
	}

	fx := passwordreset.EffectsOf(user.UsesTOTP)
	if fx.RevokeSessions {
		if err := h.d.Sessions.RevokeUser(ctx, user.ID); err != nil {
			return apierr.Internal(fmt.Errorf("end sessions: %w", err))
		}
	}
	if fx.DeauthorizeNodes {
		// The nodes are told on a best-effort basis: the password is already changed, and a node that cannot be reached
		// forgets the tokens when they expire.
		if err := h.d.Nodes.DeauthorizeUser(ctx, user.ID); err != nil {
			h.d.Logger.Error("a node was not told to forget a user", "user", user.ID, "error", err)
		}
	}
	h.logEvent(c, activity.AuthPasswordReset, user.ID, 0, nil)

	if fx.LoginUser {
		if err := h.startSession(c, cur, user); err != nil {
			return err
		}
	}
	return ok(c, resetResponse{Success: true, RedirectTo: intended, SendToLogin: fx.SendToLogin})
}

// validateReset checks the form of a reset request: the fields, and the rules of a new password with the reference's messages.
func (h *Handlers) validateReset(b resetBody) error {
	v := validate.New()
	v.Sent("token", b.Token.State)
	v.Type("token", b.Token.State, validate.KindString, false)
	v.Sent("email", b.Email.State)
	v.Type("email", b.Email.State, validate.KindString, false)
	if b.Email.State == field.Valid {
		v.Email("email", b.Email.V)
	}
	v.Sent("password", b.Password.State)
	v.Type("password", b.Password.State, validate.KindString, false)
	if err := v.Err(); err != nil {
		return err
	}

	if msgs := passwordreset.CheckNewPassword(b.Password.V, b.PasswordConfirmation.V); len(msgs) > 0 {
		fields := make([]apierr.FieldError, len(msgs))
		for i, m := range msgs {
			fields[i] = apierr.FieldError{Field: "password", Rule: "password", Detail: m}
		}
		return apierr.Validation(fields)
	}
	return nil
}
