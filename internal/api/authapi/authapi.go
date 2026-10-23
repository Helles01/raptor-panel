// Package authapi serves the sign-in endpoints: /sanctum/csrf-cookie, /auth/login, /auth/login/checkpoint, /auth/logout,
// /auth/password and /auth/password/reset. The rules are in internal/auth (login, passwordreset, totp, lockout, session);
// this package puts them in order and talks to the storage through the interfaces below, which the database layer fills.
package authapi

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/activity"
	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/auth/csrf"
	"github.com/Helles01/raptor-panel/internal/auth/passwordreset"
	"github.com/Helles01/raptor-panel/internal/auth/session"
	"github.com/Helles01/raptor-panel/internal/mail"
)

// ErrNoUser is what a store answers when there is no such user.
var ErrNoUser = errors.New("authapi: no such user")

// User is a user as far as signing in goes.
type User struct {
	ID        uint64
	UUID      string
	Username  string
	Email     string
	Language  string
	RootAdmin bool
	CreatedAt time.Time
	UpdatedAt time.Time

	PasswordHash string
	UsesTOTP     bool
	// TOTPSecret is the secret in the clear, opened by the store from its sealed form. It is empty when none is stored.
	TOTPSecret string
	// TOTPLastUsed is the start of the step of the last code that was used (totp_authenticated_at), nil if none.
	TOTPLastUsed *time.Time
}

// Users is the part of the user store the endpoints need.
type Users interface {
	// ByLogin finds a user by user name or e-mail address: field is "username" or "email". Names are compared the way the
	// database compares them, without regard to case.
	ByLogin(ctx context.Context, field, value string) (User, error)
	ByID(ctx context.Context, id uint64) (User, error)
	ByEmail(ctx context.Context, email string) (User, error)
	// UseTOTPStep stores the time of a used code, only if it is later than the stored one (a conditional update). It returns
	// false when another request used a code of the same or a later step first, so that one code cannot sign two requests in.
	UseTOTPStep(ctx context.Context, userID uint64, step time.Time) (bool, error)
	// SetPassword stores a new password hash.
	SetPassword(ctx context.Context, userID uint64, hash string) error
}

// RecoveryToken is a stored recovery token: the hash of ten letters and digits, good once.
type RecoveryToken struct {
	ID   uint64
	Hash string
}

// RecoveryTokens is the store of recovery tokens.
type RecoveryTokens interface {
	ForUser(ctx context.Context, userID uint64) ([]RecoveryToken, error)
	// Delete removes one token and reports whether it was still there. Of two requests that use the same token only one may win.
	Delete(ctx context.Context, id uint64) (bool, error)
}

// Resets is the store of password reset tokens, one for each address.
type Resets interface {
	Latest(ctx context.Context, email string) (rec passwordreset.Record, found bool, err error)
	// Replace stores the token of an address, replacing an older one.
	Replace(ctx context.Context, email string, rec passwordreset.Record) error
	// Consume deletes the token of an address and reports whether there was one. Only the request that deletes it may use it.
	Consume(ctx context.Context, email string) (bool, error)
}

// Outbox takes mail to be sent later. Nothing is sent in the request: the time a mail server takes must not show whether an
// address has an account.
type Outbox interface {
	Send(ctx context.Context, m mail.Message) error
}

// Nodes tells the nodes to forget what they hold for a user: tokens for the console and files, and SFTP sessions.
type Nodes interface {
	DeauthorizeUser(ctx context.Context, userID uint64) error
}

// LogEntry is an entry of the activity log about a user.
type LogEntry struct {
	Event string
	// Subject is the user the entry is about; Actor is the user who did it, 0 for none.
	Subject uint64
	Actor   uint64
	// Properties is the JSON object from activity.Properties.
	Properties string
}

// ActivityLog stores activity entries.
type ActivityLog interface {
	Log(ctx context.Context, e LogEntry) error
}

// Deps are everything the endpoints need.
type Deps struct {
	Users    Users
	Recovery RecoveryTokens
	Resets   Resets
	Outbox   Outbox
	Nodes    Nodes
	Log      ActivityLog

	Sessions *session.Manager
	CSRF     *csrf.Protector
	// Failures counts the failed sign-ins and second factors.
	Failures *middleware.Tracker
	// The limiters of the endpoints, by client address (the reference's throttles): the sign-in endpoints, the request of a link,
	// the reset. EmailLimiter caps the links sent to one address (passwordreset.RequestPerEmailPerHour an hour).
	LoginLimiter   *middleware.Limiter
	RequestLimiter *middleware.Limiter
	ResetLimiter   *middleware.Limiter
	EmailLimiter   *middleware.Limiter

	// Site names the panel in e-mails and makes their links from the configured public address.
	Site   mail.Site
	Secure bool
	Logger *slog.Logger
	// Now and Random default to the clock and crypto/rand.
	Now    func() time.Time
	Random io.Reader
}

// Handlers are the endpoints.
type Handlers struct {
	d Deps
}

// New checks the dependencies and returns the endpoints.
func New(d Deps) (*Handlers, error) {
	switch {
	case d.Users == nil || d.Recovery == nil || d.Resets == nil || d.Outbox == nil || d.Nodes == nil || d.Log == nil:
		return nil, errors.New("authapi: every store is required")
	case d.Sessions == nil || d.CSRF == nil || d.Failures == nil:
		return nil, errors.New("authapi: the session manager, the CSRF protector and the failure tracker are required")
	case d.LoginLimiter == nil || d.RequestLimiter == nil || d.ResetLimiter == nil || d.EmailLimiter == nil:
		return nil, errors.New("authapi: every limiter is required")
	case d.Logger == nil:
		return nil, errors.New("authapi: a logger is required")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Random == nil {
		d.Random = rand.Reader
	}
	return &Handlers{d: d}, nil
}

// Register adds the routes. The group must already have the session loaded and the CSRF check in front of it.
func (h *Handlers) Register(g api.Group) {
	byIP := func(l *middleware.Limiter) echo.MiddlewareFunc { return middleware.Throttle(l, middleware.KeyByIP) }

	g.GET("/sanctum/csrf-cookie", h.CSRFCookie, api.Public())
	auth := g.Sub("/auth")
	auth.POST("/login", h.Login, api.Guest(), byIP(h.d.LoginLimiter))
	auth.POST("/login/checkpoint", h.Checkpoint, api.Guest(), byIP(h.d.LoginLimiter))
	auth.POST("/logout", h.Logout, api.Authenticated())
	auth.POST("/password", h.ForgotPassword, api.Guest(), byIP(h.d.RequestLimiter))
	auth.POST("/password/reset", h.ResetPassword, api.Guest(), byIP(h.d.ResetLimiter))
}

// maxBody is the largest request body these endpoints read.
const maxBody = 8 << 10

const (
	// intended is where the frontend goes after a sign-in.
	intended = "/"
	// noCache keeps the answers, which hold tokens, out of every cache.
	noCache = "no-store"
	// invalidAccount stands for a login name that cannot be one, in the counters.
	invalidAccount = "-"
)

// logEvent writes an entry about a user. A failure is logged and never reaches the person: the reference does the same.
func (h *Handlers) logEvent(c echo.Context, event string, subject, actor uint64, values map[string]any) {
	props, err := activity.Properties(event, activity.WithRequestMetadata(values, c.RealIP(), c.Request().UserAgent()))
	if err != nil {
		h.d.Logger.Error("activity entry refused", "event", event, "error", err)
		return
	}
	if err := h.d.Log.Log(c.Request().Context(), LogEntry{Event: event, Subject: subject, Actor: actor, Properties: props}); err != nil {
		h.d.Logger.Error("activity entry not stored", "event", event, "error", err)
	}
}

// requireSession returns the session of the request. The CSRF check in front of the POST routes already refuses a request
// without one; this is for a route that was wired without it.
func requireSession(c echo.Context) (*session.Session, error) {
	s := middleware.CurrentSession(c)
	if s == nil {
		return nil, apierr.Forbidden("CSRF token mismatch.")
	}
	return s, nil
}

func noStore(c echo.Context) { c.Response().Header().Set("Cache-Control", noCache) }

func ok(c echo.Context, body any) error {
	noStore(c)
	return c.JSON(http.StatusOK, body)
}
