// Package authz decides, for every route, who is calling and whether they may. It implements api.Authorizer: the router hands it the
// permission descriptor of the route and it answers with nil, or with the refusal in the API's error format. What it learns on the way
// (the user, the key, the server, the node) it leaves in the request for the handler, so that a handler never looks anything up again
// and never has to remember to check.
package authz

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/ids"
	"github.com/Helles01/raptor-panel/internal/permissions"
	"github.com/Helles01/raptor-panel/internal/servers"
)

// The refusals the reference makes, with its texts.
const (
	msgUnauthorized   = "This action is unauthorized."
	msgNotAdmin       = "This account does not have permission to access the API."
	msgWrongKeyClient = "You are attempting to use an application API key on an endpoint that requires a client API key."
	msgWrongKeyApp    = "You are attempting to use a client API key on an endpoint that requires an application API key."
	msgNodeRefused    = "You are not authorized to access this resource."
	msgNodeFormat     = "The Authorization header provided was not in a valid format."
	msgNodeNoHeader   = "Access to this endpoint must include an Authorization header."
)

// ErrNoUser, ErrNoKey, ErrNoServer and ErrNoNode are what the stores answer when there is no such row.
var (
	ErrNoUser   = errors.New("authz: no such user")
	ErrNoKey    = errors.New("authz: no such key")
	ErrNoServer = errors.New("authz: no such server")
	ErrNoNode   = errors.New("authz: no such node")
)

// User is a user as far as access goes.
type User struct {
	ID        uint64
	UUID      string
	RootAdmin bool
	UsesTOTP  bool
}

// Key is a stored API key.
type Key struct {
	ID         uint64
	UserID     uint64
	Type       auth.KeyType
	Identifier string
	// SecretHash is auth.HashKeySecret of the secret.
	SecretHash string
	// AllowedIPs is the list as stored; an empty list restricts nothing.
	AllowedIPs []string
	// Access is the level per application resource (the r_* columns); empty for a client key.
	Access     map[string]auth.Access
	LastUsedAt *time.Time
}

// Server is a server as far as access goes.
type Server struct {
	ID       uint64
	UUID     string
	ShortID  string
	OwnerID  uint64
	NodeID   uint64
	State    servers.State
	NodeName string
}

// Node is a node that has authenticated.
type Node struct {
	ID   uint64
	UUID string
}

// NodeCredentials is what the store keeps to check a node's token: the token itself, opened by the store from its sealed form.
type NodeCredentials struct {
	Node  Node
	Token string
}

// Users, Keys, Servers, Nodes, Settings and Audit are the storage the authorizer reads and writes.
type (
	Users interface {
		ByID(ctx context.Context, id uint64) (User, error)
	}
	Keys interface {
		ByIdentifier(ctx context.Context, identifier string) (Key, error)
		// Touch stores the time a key was last used.
		Touch(ctx context.Context, id uint64, at time.Time) error
	}
	Servers interface {
		// Find resolves the {server} of a route: the first eight characters of the UUID, or the whole UUID, in lower case.
		Find(ctx context.Context, ref string) (Server, error)
		// SubuserPermissions returns the permissions stored for a user on a server, and whether the user is a subuser at all.
		SubuserPermissions(ctx context.Context, serverID, userID uint64) (perms []string, ok bool, err error)
	}
	Nodes interface {
		ByTokenID(ctx context.Context, tokenID string) (NodeCredentials, error)
	}
	Settings interface {
		// TwoFactorLevel is the panel's requirement: 0 none, 1 administrators, 2 everybody.
		TwoFactorLevel(ctx context.Context) (int, error)
	}
	Audit interface {
		// IPBlocked records that a key was used from an address it may not be used from (auth:ip-blocked).
		IPBlocked(ctx context.Context, userID uint64, keyIdentifier, ip, userAgent string) error
	}
)

// Deps are everything the authorizer needs.
type Deps struct {
	Users    Users
	Keys     Keys
	Servers  Servers
	Nodes    Nodes
	Settings Settings
	Audit    Audit
	Logger   *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
}

// Authorizer implements api.Authorizer.
type Authorizer struct{ d Deps }

var _ api.Authorizer = (*Authorizer)(nil)

// New checks the dependencies.
func New(d Deps) (*Authorizer, error) {
	if d.Users == nil || d.Keys == nil || d.Servers == nil || d.Nodes == nil || d.Settings == nil || d.Audit == nil || d.Logger == nil {
		return nil, errors.New("authz: every store and a logger are required")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Authorizer{d: d}, nil
}

// Authorize decides for the route's permission.
func (a *Authorizer) Authorize(c echo.Context, p api.Permission) error {
	switch p.Kind {
	case api.KindPublic:
		return nil
	case api.KindGuest:
		if s := middleware.CurrentSession(c); s != nil && s.UserID != 0 {
			return apierr.Forbidden("You are already signed in.")
		}
		return nil
	case api.KindDaemon:
		return a.node(c)
	case api.KindApplicationKey:
		return a.application(c, p)
	case api.KindAuthenticated, api.KindAdmin, api.KindServer:
		return a.user(c, p)
	}
	// A kind the router accepts but this switch does not know is a bug; refuse rather than allow.
	return apierr.Forbidden(msgUnauthorized)
}

// bearer returns the token of an "Authorization: Bearer" header, and whether there is one.
func bearer(c echo.Context) (string, bool) {
	h := c.Request().Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return "", false
	}
	return strings.TrimSpace(h[7:]), true
}

// dummyHash is compared with when a key does not exist, so that the time taken does not tell an identifier that exists from one
// that does not.
var dummyHash = auth.HashKeySecret("no such key")

// authenticateKey finds the key a bearer token names and checks its secret and the address it is used from. It returns the key
// and its user.
func (a *Authorizer) authenticateKey(c echo.Context, token string) (Key, User, error) {
	ctx := c.Request().Context()
	ident, secret, ok := auth.SplitKey(token)
	if !ok {
		return Key{}, User{}, apierr.Unauthenticated()
	}
	key, err := a.d.Keys.ByIdentifier(ctx, ident)
	switch {
	case errors.Is(err, ErrNoKey):
		subtle.ConstantTimeCompare([]byte(dummyHash), []byte(auth.HashKeySecret(secret)))
		return Key{}, User{}, apierr.Unauthenticated()
	case err != nil:
		return Key{}, User{}, apierr.Internal(err)
	}
	if subtle.ConstantTimeCompare([]byte(key.SecretHash), []byte(auth.HashKeySecret(secret))) != 1 {
		return Key{}, User{}, apierr.Unauthenticated()
	}

	user, err := a.d.Users.ByID(ctx, key.UserID)
	switch {
	case errors.Is(err, ErrNoUser):
		return Key{}, User{}, apierr.Unauthenticated()
	case err != nil:
		return Key{}, User{}, apierr.Internal(err)
	}

	if err := a.checkAddress(c, key, user); err != nil {
		return Key{}, User{}, err
	}
	if now := a.d.Now(); auth.ShouldTouchLastUsed(key.LastUsedAt, now) {
		if err := a.d.Keys.Touch(ctx, key.ID, now); err != nil {
			a.d.Logger.Error("the last use of an API key was not stored", "key", key.Identifier, "error", err)
		}
	}
	return key, user, nil
}

// checkAddress refuses a key that is used from an address its list does not allow. A list that has an entry is a restriction even if
// no entry can be read, and the number of entries that cannot be read is logged, so that an administrator notices.
func (a *Authorizer) checkAddress(c echo.Context, key Key, user User) error {
	allowed, bad := auth.LoadAllowedIPs(key.AllowedIPs)
	if bad > 0 {
		a.d.Logger.Warn("an API key has allowed addresses that cannot be read", "key", key.Identifier, "unreadable", bad)
	}
	if !allowed.Restricted() {
		return nil
	}
	ip, err := netip.ParseAddr(c.RealIP())
	if err == nil && allowed.Allows(ip) {
		return nil
	}
	if logErr := a.d.Audit.IPBlocked(c.Request().Context(), user.ID, key.Identifier, c.RealIP(), c.Request().UserAgent()); logErr != nil {
		a.d.Logger.Error("a blocked address was not logged", "key", key.Identifier, "error", logErr)
	}
	return apierr.Forbidden(auth.DeniedMessage(ip))
}

// twoFactor applies the panel's requirement for two-factor authentication: none, administrators, or everybody. A user who has to use
// it and does not is sent to the page that sets it up, except on the routes that make that possible.
func (a *Authorizer) twoFactor(ctx context.Context, u User, p api.Permission) error {
	if p.NoTwoFactor || u.UsesTOTP {
		return nil
	}
	level, err := a.d.Settings.TwoFactorLevel(ctx)
	if err != nil {
		return apierr.Internal(err)
	}
	if level == 0 || (level == 1 && !u.RootAdmin) {
		return nil
	}
	return apierr.TwoFactorRequired()
}

// application is the Application API: only an application key of an administrator, with the level the route needs on its resource.
// The reference lets a client key through, whatever its levels (C-054).
func (a *Authorizer) application(c echo.Context, p api.Permission) error {
	token, ok := bearer(c)
	if !ok {
		return apierr.Unauthenticated()
	}
	key, user, err := a.authenticateKey(c, token)
	if err != nil {
		return err
	}
	if key.Type != auth.KeyApplication {
		return apierr.Forbidden(msgWrongKeyApp)
	}
	if !user.RootAdmin {
		return apierr.Forbidden(msgNotAdmin)
	}
	if err := a.twoFactor(c.Request().Context(), user, p); err != nil {
		return err
	}
	need := auth.AccessRead
	if p.Access == auth.AccessReadWrite {
		need = 2 // the write bit
	}
	if !key.Access[p.Resource].Allows(need) {
		return apierr.Forbidden(msgUnauthorized)
	}
	setPrincipal(c, Principal{User: user, Key: &key})
	return nil
}

// user is every route for a signed-in user: by session or by client key.
func (a *Authorizer) user(c echo.Context, p api.Permission) error {
	pr, err := a.authenticateUser(c)
	if err != nil {
		return err
	}
	if err := a.twoFactor(c.Request().Context(), pr.User, p); err != nil {
		return err
	}
	if p.Kind == api.KindAdmin && !pr.User.RootAdmin {
		return apierr.Forbidden(msgUnauthorized)
	}
	if p.Kind == api.KindServer {
		if err := a.server(c, p, &pr); err != nil {
			return err
		}
	}
	setPrincipal(c, pr)
	return nil
}

// authenticateUser reads who is calling. A bearer token is a key, and then the cookie is ignored: whoever sends one means to use the
// key. Without one the session decides.
func (a *Authorizer) authenticateUser(c echo.Context) (Principal, error) {
	if token, ok := bearer(c); ok {
		key, user, err := a.authenticateKey(c, token)
		if err != nil {
			return Principal{}, err
		}
		if key.Type != auth.KeyAccount {
			return Principal{}, apierr.Forbidden(msgWrongKeyClient)
		}
		return Principal{User: user, Key: &key}, nil
	}

	s := middleware.CurrentSession(c)
	if s == nil || s.UserID == 0 {
		return Principal{}, apierr.Unauthenticated()
	}
	user, err := a.d.Users.ByID(c.Request().Context(), s.UserID)
	switch {
	case errors.Is(err, ErrNoUser):
		return Principal{}, apierr.Unauthenticated()
	case err != nil:
		return Principal{}, apierr.Internal(err)
	}
	return Principal{User: user, Session: s}, nil
}

var serverRef = regexp.MustCompile(`^(?:[0-9a-f]{8}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)

// server resolves the {server} of the route and checks that the user may reach it, that it is in a state the route may be used in,
// and that the user holds the permission, in that order, as the reference does. A server the user has no part in is not found.
func (a *Authorizer) server(c echo.Context, p api.Permission, pr *Principal) error {
	ctx := c.Request().Context()
	ref := strings.ToLower(c.Param("server"))
	if strings.HasPrefix(ref, "serv_") {
		// The long identifier the client API shows as server_identifier is the UUID written another way, and the reference accepts
		// it in every route (C-055).
		uuid, ok := ids.ParseRealtime("serv", ref)
		if !ok {
			return apierr.NotFound()
		}
		ref = uuid
	}
	if !serverRef.MatchString(ref) {
		return apierr.NotFound()
	}
	srv, err := a.d.Servers.Find(ctx, ref)
	switch {
	case errors.Is(err, ErrNoServer):
		return apierr.NotFound()
	case err != nil:
		return apierr.Internal(err)
	}

	owner := srv.OwnerID == pr.User.ID
	var sub []string
	if !pr.User.RootAdmin && !owner {
		var isSub bool
		if sub, isSub, err = a.d.Servers.SubuserPermissions(ctx, srv.ID, pr.User.ID); err != nil {
			return apierr.Internal(err)
		} else if !isSub {
			return apierr.NotFound()
		}
	}

	if err := a.state(p, pr.User, srv); err != nil {
		return err
	}

	held := permissions.Effective(pr.User.RootAdmin, owner, sub)
	if p.Resource != "" && !permissions.Has(held, p.Resource) {
		return apierr.Forbidden(msgUnauthorized)
	}
	pr.Server, pr.Permissions = &srv, held
	return nil
}

// state applies the route's server state policy.
func (a *Authorizer) state(p api.Permission, u User, srv Server) error {
	if p.State == api.StateAny {
		return nil
	}
	check := srv.State.Validate
	if p.State == api.StateReinstall {
		check = srv.State.ValidateReinstall
	}
	err := check()
	if err == nil {
		return nil
	}
	if p.State == api.StateAdminConsole && u.RootAdmin && srv.State.Status != servers.StatusSuspended && !srv.State.NodeMaintenance {
		return nil
	}
	return apierr.Conflict(err.Error())
}

// node authenticates a Wings node by "Authorization: Bearer <token id>.<token>".
func (a *Authorizer) node(c echo.Context) error {
	token, ok := bearer(c)
	if !ok {
		c.Response().Header().Set("WWW-Authenticate", "Bearer")
		return &apierr.Error{Status: 401, Code: apierr.CodeUnauthenticated, Detail: msgNodeNoHeader}
	}
	id, secret, found := strings.Cut(token, ".")
	if !found || id == "" || secret == "" || strings.Contains(secret, ".") {
		return apierr.BadRequest(msgNodeFormat)
	}

	creds, err := a.d.Nodes.ByTokenID(c.Request().Context(), id)
	switch {
	case errors.Is(err, ErrNoNode):
		// Compared against something anyway, so that the time does not show that the token id exists.
		subtle.ConstantTimeCompare([]byte(dummyHash), []byte(auth.HashKeySecret(secret)))
		return apierr.Forbidden(msgNodeRefused)
	case err != nil:
		return apierr.Internal(err)
	}
	if subtle.ConstantTimeCompare([]byte(creds.Token), []byte(secret)) != 1 {
		return apierr.Forbidden(msgNodeRefused)
	}
	setNode(c, creds.Node)
	return nil
}
