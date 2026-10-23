package authapi

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/activity"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/field"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/api/validate"
	"github.com/Helles01/raptor-panel/internal/api/web"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/auth/lockout"
	"github.com/Helles01/raptor-panel/internal/auth/login"
	"github.com/Helles01/raptor-panel/internal/auth/session"
	"github.com/Helles01/raptor-panel/internal/auth/totp"
)

// CSRFCookie is the first call of the sign-in page (Laravel Sanctum's /sanctum/csrf-cookie). It makes sure the browser has a
// session, a guest one if need be, and gives it the token its later POST requests must send back.
func (h *Handlers) CSRFCookie(c echo.Context) error {
	s := middleware.CurrentSession(c)
	if s == nil {
		var err error
		if s, err = h.d.Sessions.Create(c.Request().Context(), c.RealIP(), c.Request().UserAgent()); err != nil {
			return apierr.Internal(fmt.Errorf("create session: %w", err))
		}
		http.SetCookie(c.Response(), h.d.Sessions.Cookie(s))
		middleware.SetCurrentSession(c, s)
	}
	token, err := h.d.CSRF.Issue(s.ID)
	if err != nil {
		return apierr.Internal(err)
	}
	middleware.SetCSRFCookie(c.Response(), token, h.d.Secure)
	noStore(c)
	return c.NoContent(http.StatusNoContent)
}

type loginBody struct {
	User     field.String `json:"user"`
	Password field.Secret `json:"password"`
}

// loginData is the answer of a sign-in step.
type loginData struct {
	Complete          bool      `json:"complete"`
	Intended          string    `json:"intended,omitempty"`
	User              *web.User `json:"user,omitempty"`
	ConfirmationToken string    `json:"confirmation_token,omitempty"`
}

type loginResponse struct {
	Data loginData `json:"data"`
}

func webUser(u User) *web.User {
	return &web.User{
		UUID: u.UUID, Username: u.Username, Email: u.Email, RootAdmin: u.RootAdmin, UseTotp: u.UsesTOTP, Language: u.Language,
		UpdatedAt: u.UpdatedAt, CreatedAt: u.CreatedAt,
	}
}

// Login is the first step of signing in, in the order of docs/modules/03d-login.md.
func (h *Handlers) Login(c echo.Context) error {
	cur, err := requireSession(c)
	if err != nil {
		return err
	}
	var body loginBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	ctx := c.Request().Context()

	// A name that cannot be one is a failed sign-in without a look-up. A missing or wrong-typed one is empty here, and so cannot be one.
	ident, usable := login.Identifier(body.User.V)
	account := ident
	if !usable {
		account = invalidAccount
	}
	counters, err := login.Counters(account, c.RealIP())
	if err != nil {
		return apierr.Internal(err)
	}
	if blocked, retry := h.d.Failures.Blocked(counters); blocked {
		return apierr.TooManyRequests(retry)
	}

	var user User
	found := false
	if usable {
		switch u, err := h.d.Users.ByLogin(ctx, login.Field(ident), ident); {
		case err == nil:
			user, found = u, true
		case !errors.Is(err, ErrNoUser):
			return apierr.Internal(fmt.Errorf("find user: %w", err))
		}
	}
	passwordOK := found && auth.VerifyPassword(user.PasswordHash, body.Password.V)

	result, burn := login.FirstFactor(found, passwordOK, found && user.UsesTOTP)
	if burn {
		auth.VerifyUnknownUser(body.Password.V)
	}
	switch result {
	case login.Done:
		return h.signIn(c, cur, user, nil)

	case login.NeedsSecondFactor:
		token, err := h.d.Sessions.StartConfirmation(cur, user.ID)
		if err != nil {
			return apierr.Internal(err)
		}
		if err := h.d.Sessions.Save(ctx, cur); err != nil {
			return apierr.Internal(fmt.Errorf("save session: %w", err))
		}
		h.logEvent(c, activity.AuthCheckpoint, user.ID, 0, nil)
		return ok(c, loginResponse{Data: loginData{Complete: false, ConfirmationToken: token}})
	}

	h.d.Failures.Fail(counters)
	if found {
		h.logEvent(c, activity.AuthFail, user.ID, 0, nil)
	}
	return apierr.Display(login.ErrFailed.Error())
}

type checkpointBody struct {
	ConfirmationToken  field.String `json:"confirmation_token"`
	AuthenticationCode field.String `json:"authentication_code"`
	RecoveryToken      field.String `json:"recovery_token"`
}

// Checkpoint is the second step: the confirmation token of the first step and a code or a recovery token.
func (h *Handlers) Checkpoint(c echo.Context) error {
	cur, err := requireSession(c)
	if err != nil {
		return err
	}
	var body checkpointBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	ctx := c.Request().Context()

	var pending uint64
	if cur.Confirmation != nil {
		pending = cur.Confirmation.UserID
	}
	counters, err := login.Counters(fmt.Sprintf("id:%d", pending), c.RealIP())
	if err != nil {
		return apierr.Internal(err)
	}
	if blocked, retry := h.d.Failures.Blocked(counters); blocked {
		return apierr.TooManyRequests(retry)
	}

	if !h.d.Sessions.ConfirmationPending(cur) {
		h.d.Failures.Fail(counters)
		return apierr.Display(login.ConfirmationFailure(false).Error())
	}
	userID, matches := h.d.Sessions.CheckConfirmation(cur, body.ConfirmationToken.V)
	if !matches {
		return h.checkpointFailed(c, cur, counters, nil, login.ConfirmationFailure(true))
	}

	method, value, err := login.SecondFactor(body.AuthenticationCode.V, body.RecoveryToken.V)
	if errors.Is(err, login.ErrNoSecondFactor) {
		return apierr.Validation([]apierr.FieldError{{Field: "authentication_code", Rule: "required", Detail: login.ErrNoSecondFactor.Error()}})
	}
	if err != nil {
		return h.checkpointFailed(c, cur, counters, nil, err)
	}

	user, err := h.d.Users.ByID(ctx, userID)
	switch {
	case errors.Is(err, ErrNoUser):
		return h.checkpointFailed(c, cur, counters, nil, login.ConfirmationFailure(false))
	case err != nil:
		return apierr.Internal(fmt.Errorf("find user: %w", err))
	}

	var valid bool
	event := activity.AuthToken
	if method == login.Recovery {
		valid, err = h.useRecoveryToken(c, user, value)
		event = activity.AuthRecoveryToken
	} else {
		valid, err = h.useCode(c, user, value)
	}
	if err != nil {
		return err
	}
	if !valid {
		return h.checkpointFailed(c, cur, counters, &user, login.SecondFactorFailure(method))
	}
	return h.signIn(c, cur, user, []string{event})
}

// checkpointFailed counts a failed second step against the pending login, the counters and, when the user is known, their log, and
// answers with err.
func (h *Handlers) checkpointFailed(c echo.Context, cur *session.Session, counters []lockout.Counter, user *User, err error) error {
	h.d.Sessions.FailConfirmation(cur)
	if saveErr := h.d.Sessions.Save(c.Request().Context(), cur); saveErr != nil {
		return apierr.Internal(fmt.Errorf("save session: %w", saveErr))
	}
	h.d.Failures.Fail(counters)
	if user != nil {
		h.logEvent(c, activity.AuthFail, user.ID, 0, nil)
	}
	return apierr.Display(err.Error())
}

// useRecoveryToken checks the token against the user's stored ones and spends the one that matches. A token another request spent
// meanwhile does not count.
func (h *Handlers) useRecoveryToken(c echo.Context, user User, value string) (bool, error) {
	ctx := c.Request().Context()
	tokens, err := h.d.Recovery.ForUser(ctx, user.ID)
	if err != nil {
		return false, apierr.Internal(fmt.Errorf("load recovery tokens: %w", err))
	}
	for _, t := range tokens {
		if !auth.VerifyPassword(t.Hash, value) {
			continue
		}
		deleted, err := h.d.Recovery.Delete(ctx, t.ID)
		if err != nil {
			return false, apierr.Internal(fmt.Errorf("delete recovery token: %w", err))
		}
		return deleted, nil
	}
	return false, nil
}

// useCode checks a code from the authenticator app. A code is good once: it must be of a later step than the last one used, and the
// store takes the step with a conditional update, so that two requests with the same code cannot both pass.
func (h *Handlers) useCode(c echo.Context, user User, code string) (bool, error) {
	if user.TOTPSecret == "" {
		return false, nil
	}
	last := int64(-1)
	if user.TOTPLastUsed != nil {
		last = totp.Step(*user.TOTPLastUsed)
	}
	step, matched, err := totp.Verify(user.TOTPSecret, code, h.d.Now(), totp.DefaultWindow, last)
	if err != nil {
		return false, apierr.Internal(fmt.Errorf("verify code: %w", err))
	}
	if !matched {
		return false, nil
	}
	used, err := h.d.Users.UseTOTPStep(c.Request().Context(), user.ID, time.Unix(step*totp.Period, 0).UTC())
	if err != nil {
		return false, apierr.Internal(fmt.Errorf("store the step of the code: %w", err))
	}
	return used, nil
}

// signIn gives the browser a new session of the user (a new identifier, so one that was planted before is worthless) and a token for
// it, and answers the way the frontend expects. extra are the events to write besides auth:success.
func (h *Handlers) signIn(c echo.Context, cur *session.Session, user User, extra []string) error {
	if err := h.startSession(c, cur, user); err != nil {
		return err
	}
	for _, e := range extra {
		h.logEvent(c, e, user.ID, user.ID, nil)
	}
	h.logEvent(c, activity.AuthSuccess, user.ID, user.ID, nil)
	return ok(c, loginResponse{Data: loginData{Complete: true, Intended: intended, User: webUser(user)}})
}

// startSession replaces the guest session by one of the user and sets the cookies.
func (h *Handlers) startSession(c echo.Context, cur *session.Session, user User) error {
	fresh, err := h.d.Sessions.Login(c.Request().Context(), cur, user.ID)
	if err != nil {
		return apierr.Internal(fmt.Errorf("start session: %w", err))
	}
	token, err := h.d.CSRF.Issue(fresh.ID)
	if err != nil {
		return apierr.Internal(err)
	}
	http.SetCookie(c.Response(), h.d.Sessions.Cookie(fresh))
	middleware.SetCSRFCookie(c.Response(), token, h.d.Secure)
	middleware.SetCurrentSession(c, fresh)
	return nil
}

// Logout ends the session and clears the cookies. It answers the same without a session.
func (h *Handlers) Logout(c echo.Context) error {
	if s := middleware.CurrentSession(c); s != nil {
		if err := h.d.Sessions.Destroy(c.Request().Context(), s); err != nil {
			return apierr.Internal(fmt.Errorf("end session: %w", err))
		}
	}
	http.SetCookie(c.Response(), h.d.Sessions.ClearCookie())
	http.SetCookie(c.Response(), &http.Cookie{
		Name: middleware.CSRFCookieName, Value: "", Path: "/", MaxAge: -1, Secure: h.d.Secure, SameSite: http.SameSiteLaxMode,
	})
	noStore(c)
	return c.NoContent(http.StatusNoContent)
}
