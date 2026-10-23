package authz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/auth/session"
)

var t0 = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

const (
	secretA = "abcdefghijklmnopqrstuvwxyzABCDEF" // 32 characters
	secretB = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef"
)

type fakes struct {
	mu       sync.Mutex
	users    map[uint64]User
	keys     map[string]Key
	servers  map[string]Server // by short id
	subusers map[[2]uint64][]string
	nodes    map[string]NodeCredentials
	level    int
	levelErr error
	touched  []uint64
	touchErr error
	blocked  []string
	failFind bool
	nodeErr  error
	finds    int
	keyErr   error
	userErr  error
}

func newFakes() *fakes {
	return &fakes{users: map[uint64]User{}, keys: map[string]Key{}, servers: map[string]Server{}, subusers: map[[2]uint64][]string{}, nodes: map[string]NodeCredentials{}}
}

func (f *fakes) ByID(_ context.Context, id uint64) (User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.userErr != nil {
		return User{}, f.userErr
	}
	u, ok := f.users[id]
	if !ok {
		return User{}, ErrNoUser
	}
	return u, nil
}

func (f *fakes) ByIdentifier(_ context.Context, id string) (Key, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.keyErr != nil {
		return Key{}, f.keyErr
	}
	k, ok := f.keys[id]
	if !ok {
		return Key{}, ErrNoKey
	}
	return k, nil
}

func (f *fakes) Touch(_ context.Context, id uint64, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touched = append(f.touched, id)
	return f.touchErr
}

func (f *fakes) Find(_ context.Context, ref string) (Server, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finds++
	if f.failFind {
		return Server{}, errors.New("db down")
	}
	for _, s := range f.servers {
		if s.ShortID == ref || s.UUID == ref {
			return s, nil
		}
	}
	return Server{}, ErrNoServer
}

func (f *fakes) SubuserPermissions(_ context.Context, serverID, userID uint64) ([]string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.subusers[[2]uint64{serverID, userID}]
	return p, ok, nil
}

func (f *fakes) ByTokenID(_ context.Context, id string) (NodeCredentials, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nodeErr != nil {
		return NodeCredentials{}, f.nodeErr
	}
	n, ok := f.nodes[id]
	if !ok {
		return NodeCredentials{}, ErrNoNode
	}
	return n, nil
}

func (f *fakes) TwoFactorLevel(context.Context) (int, error) { return f.level, f.levelErr }

func (f *fakes) IPBlocked(_ context.Context, userID uint64, key, ip, ua string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blocked = append(f.blocked, key+"|"+ip+"|"+ua)
	return nil
}

type env struct {
	t        *testing.T
	f        *fakes
	srv      http.Handler
	sessions *session.Manager
	logs     *bytes.Buffer
	clock    time.Time
}

// seed has an ordinary user, an administrator, an owner of a server, a subuser and a stranger.
func seed(f *fakes) {
	f.users[1] = User{ID: 1, UUID: "u1", RootAdmin: false}
	f.users[2] = User{ID: 2, UUID: "u2", RootAdmin: true}
	f.users[3] = User{ID: 3, UUID: "u3"} // owner of server 100
	f.users[4] = User{ID: 4, UUID: "u4"} // subuser of server 100
	f.users[5] = User{ID: 5, UUID: "u5"} // a stranger
	f.users[6] = User{ID: 6, UUID: "u6", UsesTOTP: true}
	f.servers["aaaa1111"] = Server{ID: 100, UUID: "aaaa1111-2222-4333-8444-555555555555", ShortID: "aaaa1111", OwnerID: 3, NodeID: 7}
	f.subusers[[2]uint64{100, 4}] = []string{"control.start", "websocket.connect"}
}

func addKey(f *fakes, id string, secret string, typ auth.KeyType, user uint64, mutate ...func(*Key)) {
	k := Key{ID: uint64(len(f.keys) + 1), UserID: user, Type: typ, Identifier: id, SecretHash: auth.HashKeySecret(secret)}
	for _, m := range mutate {
		m(&k)
	}
	f.keys[id] = k
}

const (
	clientKeyID = "ptlc_abcdefghijk" // 16 characters
	appKeyID    = "ptla_abcdefghijk"
)

func keyToken(id, secret string) string { return id + secret }

func newEnv(t *testing.T) *env {
	t.Helper()
	f := newFakes()
	seed(f)
	addKey(f, clientKeyID, secretA, auth.KeyAccount, 1)
	addKey(f, appKeyID, secretB, auth.KeyApplication, 2, func(k *Key) {
		k.Access = map[string]auth.Access{"servers": auth.AccessReadWrite, "users": auth.AccessRead, "nodes": auth.AccessNone}
	})
	f.nodes["nodeid0123456789"] = NodeCredentials{Node: Node{ID: 7, UUID: "node-uuid"}, Token: strings.Repeat("t", 64)}

	e := &env{t: t, f: f, logs: &bytes.Buffer{}, clock: t0}
	var err error
	e.sessions, err = session.NewManager(session.NewMemoryStore(), session.Options{IdleTimeout: time.Hour, MaxLifetime: 8 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	az, err := New(Deps{Users: f, Keys: f, Servers: f, Nodes: f, Settings: f, Audit: f, Logger: slog.New(slog.NewTextHandler(e.logs, nil)), Now: func() time.Time { return e.clock }})
	if err != nil {
		t.Fatal(err)
	}

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := api.New(api.Options{
		Logger: quiet, Authorizer: az,
		Trust: middleware.NewProxyTrust([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, quiet),
	})
	if err != nil {
		t.Fatal(err)
	}
	g := srv.Router.Sub("", middleware.LoadSession(e.sessions, false))
	who := func(c echo.Context) error {
		out := map[string]any{}
		if p, ok := From(c); ok {
			out["user"] = p.User.ID
			out["key"] = p.Key != nil
			out["session"] = p.Session != nil
			if p.Server != nil {
				out["server"] = p.Server.ID
				out["perms"] = p.Permissions
			}
		}
		if n, ok := NodeFrom(c); ok {
			out["node"] = n.ID
		}
		return c.JSON(200, out)
	}
	g.GET("/public", who, api.Public())
	g.GET("/guest", who, api.Guest())
	g.GET("/user", who, api.Authenticated())
	g.GET("/account", who, api.Authenticated().WithoutTwoFactor())
	g.GET("/admin", who, api.Admin())
	g.GET("/app/servers", who, api.ApplicationKey("servers", auth.AccessRead))
	g.POST("/app/servers", who, api.ApplicationKey("servers", auth.AccessReadWrite))
	g.GET("/app/users", who, api.ApplicationKey("users", auth.AccessRead))
	g.POST("/app/users", who, api.ApplicationKey("users", auth.AccessReadWrite))
	g.GET("/app/nodes", who, api.ApplicationKey("nodes", auth.AccessRead))
	g.GET("/app/eggs", who, api.ApplicationKey("eggs", auth.AccessRead))
	g.GET("/servers/:server", who, api.ServerPermissionIn("", api.StateAny))
	g.GET("/servers/:server/any", who, api.ServerPermission(""))
	g.POST("/servers/:server/power", who, api.ServerPermission("control.start"))
	g.GET("/servers/:server/ws", who, api.ServerPermissionIn("websocket.connect", api.StateAdminConsole))
	g.POST("/servers/:server/reinstall", who, api.ServerPermissionIn("settings.reinstall", api.StateReinstall))
	g.GET("/remote/servers", who, api.Daemon())
	h, err := srv.Handler()
	if err != nil {
		t.Fatal(err)
	}
	e.srv = h
	return e
}

type reply struct {
	Status int
	Header http.Header
	Body   []byte
}

func (r reply) json() map[string]any {
	var m map[string]any
	_ = json.Unmarshal(r.Body, &m)
	return m
}

func (r reply) detail() string {
	errs, _ := r.json()["errors"].([]any)
	if len(errs) == 0 {
		return ""
	}
	d, _ := errs[0].(map[string]any)["detail"].(string)
	return d
}

func (r reply) code() string {
	errs, _ := r.json()["errors"].([]any)
	if len(errs) == 0 {
		return ""
	}
	d, _ := errs[0].(map[string]any)["code"].(string)
	return d
}

// call sends a request. opts are "Header: value", "ip=...", or "user=N" for a session of that user.
func (e *env) call(method, path string, opts ...string) reply {
	e.t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.Header.Set("User-Agent", "TestAgent/1.0")
	for _, o := range opts {
		switch {
		case strings.HasPrefix(o, "ip="):
			req.Header.Set("X-Forwarded-For", strings.TrimPrefix(o, "ip="))
		case strings.HasPrefix(o, "user="):
			id := uint64(0)
			for _, ch := range strings.TrimPrefix(o, "user=") {
				id = id*10 + uint64(ch-'0')
			}
			s, err := e.sessions.Create(context.Background(), "203.0.113.9", "x")
			if err != nil {
				e.t.Fatal(err)
			}
			if id != 0 {
				if s, err = e.sessions.Login(context.Background(), s, id); err != nil {
					e.t.Fatal(err)
				}
			}
			req.AddCookie(e.sessions.Cookie(s))
		default:
			k, v, _ := strings.Cut(o, ": ")
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return reply{Status: rec.Code, Header: rec.Header(), Body: rec.Body.Bytes()}
}

func bearerOf(token string) string { return "Authorization: Bearer " + token }

func TestPublicNeedsNothing(t *testing.T) {
	e := newEnv(t)

	if rp := e.call("GET", "/public"); rp.Status != 200 {
		t.Errorf("status = %d", rp.Status)
	}
}

func TestGuestRoutesAreClosedToTheSignedIn(t *testing.T) {
	e := newEnv(t)

	if rp := e.call("GET", "/guest"); rp.Status != 200 {
		t.Errorf("anonymous = %d", rp.Status)
	}
	if rp := e.call("GET", "/guest", "user=1"); rp.Status != 403 {
		t.Errorf("signed in = %d", rp.Status)
	}
	if rp := e.call("GET", "/guest", "user=0"); rp.Status != 200 {
		t.Errorf("a guest session = %d", rp.Status)
	}
}

func TestAuthenticatedBySession(t *testing.T) {
	e := newEnv(t)

	rp := e.call("GET", "/user", "user=1")

	if rp.Status != 200 || rp.json()["user"] != float64(1) || rp.json()["session"] != true || rp.json()["key"] != false {
		t.Errorf("got %d %s", rp.Status, rp.Body)
	}
}

func TestAuthenticatedWithoutAnyone(t *testing.T) {
	e := newEnv(t)

	for name, opts := range map[string][]string{"nothing": nil, "a guest session": {"user=0"}} {
		if rp := e.call("GET", "/user", opts...); rp.Status != 401 {
			t.Errorf("%s = %d", name, rp.Status)
		}
	}
}

func TestASessionOfAUserWhoWasDeleted(t *testing.T) {
	e := newEnv(t)
	delete(e.f.users, 1)

	if rp := e.call("GET", "/user", "user=1"); rp.Status != 401 {
		t.Errorf("status = %d", rp.Status)
	}
}

func TestAuthenticatedByClientKey(t *testing.T) {
	e := newEnv(t)

	rp := e.call("GET", "/user", bearerOf(keyToken(clientKeyID, secretA)))

	if rp.Status != 200 || rp.json()["user"] != float64(1) || rp.json()["key"] != true || rp.json()["session"] != false {
		t.Errorf("got %d %s", rp.Status, rp.Body)
	}
}

func TestKeyAuthenticationRefusals(t *testing.T) {
	e := newEnv(t)
	tests := map[string]string{
		"wrong secret":       bearerOf(keyToken(clientKeyID, secretB)),
		"unknown identifier": bearerOf(keyToken("ptlc_zzzzzzzzzzz", secretA)),
		"too short":          bearerOf("ptlc_abc"),
		"not a key":          bearerOf("hello"),
		"empty":              "Authorization: Bearer ",
		"punctuation":        bearerOf(keyToken(clientKeyID, strings.Repeat("-", 32))),
	}
	var bodies []string
	for name, h := range tests {
		rp := e.call("GET", "/user", h)
		if rp.Status != 401 {
			t.Errorf("%s = %d", name, rp.Status)
		}
		bodies = append(bodies, string(rp.Body))
	}
	for _, b := range bodies[1:] {
		if b != bodies[0] {
			t.Errorf("the refusals differ:\n%s\n%s", bodies[0], b)
		}
	}
}

func TestKeyTokenIsNotTakenFromTheCookieOrAnotherScheme(t *testing.T) {
	e := newEnv(t)

	if rp := e.call("GET", "/user", "Authorization: Basic "+keyToken(clientKeyID, secretA)); rp.Status != 401 {
		t.Errorf("Basic scheme = %d", rp.Status)
	}
	if rp := e.call("GET", "/user", "Authorization: "+keyToken(clientKeyID, secretA)); rp.Status != 401 {
		t.Errorf("no scheme = %d", rp.Status)
	}
	if rp := e.call("GET", "/user", "authorization: bEaReR  "+keyToken(clientKeyID, secretA)+"  "); rp.Status != 200 {
		t.Errorf("a scheme in other case and spaces = %d", rp.Status)
	}
}

func TestABearerTokenWinsOverTheCookie(t *testing.T) {
	e := newEnv(t)

	good := e.call("GET", "/user", "user=2", bearerOf(keyToken(clientKeyID, secretA)))
	bad := e.call("GET", "/user", "user=2", bearerOf(keyToken(clientKeyID, secretB)))

	if good.json()["user"] != float64(1) {
		t.Errorf("the key's user must be the caller, not the cookie's: %s", good.Body)
	}
	if bad.Status != 401 {
		t.Errorf("a wrong key with a good cookie = %d: the cookie must not rescue it", bad.Status)
	}
}

func TestAnApplicationKeyIsRefusedOnAClientRoute(t *testing.T) {
	e := newEnv(t)

	rp := e.call("GET", "/user", bearerOf(keyToken(appKeyID, secretB)))

	if rp.Status != 403 || !strings.Contains(rp.detail(), "application API key on an endpoint that requires a client API key") {
		t.Errorf("got %d %q", rp.Status, rp.detail())
	}
}

func TestAKeyOfADeletedUser(t *testing.T) {
	e := newEnv(t)
	delete(e.f.users, 1)

	if rp := e.call("GET", "/user", bearerOf(keyToken(clientKeyID, secretA))); rp.Status != 401 {
		t.Errorf("status = %d", rp.Status)
	}
}

func TestKeyAddressRestriction(t *testing.T) {
	e := newEnv(t)
	k := e.f.keys[clientKeyID]
	k.AllowedIPs = []string{"203.0.113.0/24", "2001:db8::/32"}
	e.f.keys[clientKeyID] = k
	token := bearerOf(keyToken(clientKeyID, secretA))

	tests := []struct {
		ip   string
		want int
	}{
		{"203.0.113.9", 200},
		{"2001:db8::7", 200},
		{"::ffff:203.0.113.9", 200},
		{"198.51.100.1", 403},
		{"2001:db9::1", 403},
	}
	for _, tt := range tests {
		if rp := e.call("GET", "/user", token, "ip="+tt.ip); rp.Status != tt.want {
			t.Errorf("from %s = %d, want %d", tt.ip, rp.Status, tt.want)
		}
	}
	if len(e.f.blocked) != 2 || !strings.HasPrefix(e.f.blocked[0], clientKeyID+"|198.51.100.1|TestAgent/1.0") {
		t.Errorf("blocked = %v; the refusals must be logged with the key, the address and the browser", e.f.blocked)
	}
}

func TestAKeyAddressRefusalSaysWhichAddress(t *testing.T) {
	e := newEnv(t)
	k := e.f.keys[clientKeyID]
	k.AllowedIPs = []string{"203.0.113.9"}
	e.f.keys[clientKeyID] = k

	rp := e.call("GET", "/user", bearerOf(keyToken(clientKeyID, secretA)), "ip=198.51.100.1")

	if !strings.Contains(rp.detail(), "198.51.100.1") || !strings.Contains(rp.detail(), "does not have permission") {
		t.Errorf("detail = %q", rp.detail())
	}
}

func TestAListThatCannotBeReadBlocksEverything(t *testing.T) {
	e := newEnv(t)
	k := e.f.keys[clientKeyID]
	k.AllowedIPs = []string{"nonsense"}
	e.f.keys[clientKeyID] = k

	rp := e.call("GET", "/user", bearerOf(keyToken(clientKeyID, secretA)))

	if rp.Status != 403 {
		t.Errorf("status = %d; a restriction that cannot be read must not become no restriction", rp.Status)
	}
	if !strings.Contains(e.logs.String(), "cannot be read") {
		t.Error("the unreadable list must be logged")
	}
}

func TestAKeyWithoutARestrictionWorksFromAnywhere(t *testing.T) {
	e := newEnv(t)

	if rp := e.call("GET", "/user", bearerOf(keyToken(clientKeyID, secretA)), "ip=198.51.100.1"); rp.Status != 200 {
		t.Errorf("status = %d", rp.Status)
	}
	if len(e.f.blocked) != 0 {
		t.Errorf("blocked = %v", e.f.blocked)
	}
}

func TestSessionsAreNotRestrictedByKeyAddresses(t *testing.T) {
	e := newEnv(t)
	k := e.f.keys[clientKeyID]
	k.AllowedIPs = []string{"203.0.113.9"}
	e.f.keys[clientKeyID] = k

	if rp := e.call("GET", "/user", "user=1", "ip=198.51.100.1"); rp.Status != 200 {
		t.Errorf("status = %d: the list is the key's, not the user's", rp.Status)
	}
}

func TestTheLastUseOfAKeyIsStoredOncePerMinute(t *testing.T) {
	e := newEnv(t)
	token := bearerOf(keyToken(clientKeyID, secretA))

	e.call("GET", "/user", token)
	if len(e.f.touched) != 1 {
		t.Fatalf("touched = %v, want the first use stored", e.f.touched)
	}
	last := e.clock
	k := e.f.keys[clientKeyID]
	k.LastUsedAt = &last
	e.f.keys[clientKeyID] = k

	e.clock = e.clock.Add(30 * time.Second)
	e.call("GET", "/user", token)
	if len(e.f.touched) != 1 {
		t.Errorf("touched = %v, want no write within a minute", e.f.touched)
	}
	e.clock = e.clock.Add(31 * time.Second)
	e.call("GET", "/user", token)
	if len(e.f.touched) != 2 {
		t.Errorf("touched = %v, want a write after a minute", e.f.touched)
	}
}

func TestAFailureToStoreTheLastUseDoesNotRefuseTheRequest(t *testing.T) {
	e := newEnv(t)
	e.f.touchErr = errors.New("db down")

	rp := e.call("GET", "/user", bearerOf(keyToken(clientKeyID, secretA)))

	if rp.Status != 200 || !strings.Contains(e.logs.String(), "last use of an API key was not stored") {
		t.Errorf("status = %d, logs = %s", rp.Status, e.logs.String())
	}
}

func TestTwoFactorRequirement(t *testing.T) {
	tests := []struct {
		name   string
		level  int
		user   string // the session
		route  string
		status int
	}{
		{"not required", 0, "user=1", "/user", 200},
		{"admins, an ordinary user", 1, "user=1", "/user", 200},
		{"admins, an administrator without it", 1, "user=2", "/user", 400},
		{"everybody, an ordinary user", 2, "user=1", "/user", 400},
		{"everybody, an administrator", 2, "user=2", "/user", 400},
		{"everybody, a user who has it", 2, "user=6", "/user", 200},
		{"admins, a user who has it", 1, "user=6", "/user", 200},
		{"the account route is exempt", 2, "user=1", "/account", 200},
		{"a server route is not exempt", 2, "user=3", "/servers/aaaa1111/any", 400},
		{"the admin route", 1, "user=2", "/admin", 400},
		{"an unknown level is the strictest", 7, "user=1", "/user", 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.f.level = tt.level

			rp := e.call("GET", tt.route, tt.user)

			if rp.Status != tt.status {
				t.Fatalf("status = %d, want %d: %s", rp.Status, tt.status, rp.Body)
			}
			if tt.status == 400 && rp.code() != "TwoFactorAuthRequiredException" {
				t.Errorf("code = %q, want the one the frontend reads", rp.code())
			}
		})
	}
}

func TestTwoFactorRequirementAlsoHoldsForKeys(t *testing.T) {
	e := newEnv(t)
	e.f.level = 2

	if rp := e.call("GET", "/user", bearerOf(keyToken(clientKeyID, secretA))); rp.Status != 400 || rp.code() != "TwoFactorAuthRequiredException" {
		t.Errorf("client key = %d %s", rp.Status, rp.code())
	}
	if rp := e.call("GET", "/app/servers", bearerOf(keyToken(appKeyID, secretB))); rp.Status != 400 || rp.code() != "TwoFactorAuthRequiredException" {
		t.Errorf("application key = %d %s", rp.Status, rp.code())
	}
}

func TestAFailingSettingIsAServerError(t *testing.T) {
	e := newEnv(t)
	e.f.levelErr = errors.New("db down")

	if rp := e.call("GET", "/user", "user=1"); rp.Status != 500 {
		t.Errorf("status = %d", rp.Status)
	}
	if rp := e.call("GET", "/account", "user=1"); rp.Status != 200 {
		t.Errorf("an exempt route must not even ask: %d", rp.Status)
	}
	if rp := e.call("GET", "/user", "user=6"); rp.Status != 200 {
		t.Errorf("a user who has two-factor authentication must not be asked about the setting: %d", rp.Status)
	}
}

func TestAdminRoute(t *testing.T) {
	e := newEnv(t)

	if rp := e.call("GET", "/admin", "user=1"); rp.Status != 403 {
		t.Errorf("ordinary user = %d", rp.Status)
	}
	if rp := e.call("GET", "/admin", "user=2"); rp.Status != 200 {
		t.Errorf("administrator = %d", rp.Status)
	}
	if rp := e.call("GET", "/admin"); rp.Status != 401 {
		t.Errorf("anonymous = %d", rp.Status)
	}
}

func TestApplicationRoutesNeedAnApplicationKey(t *testing.T) {
	e := newEnv(t)
	app := bearerOf(keyToken(appKeyID, secretB))

	if rp := e.call("GET", "/app/servers"); rp.Status != 401 {
		t.Errorf("anonymous = %d", rp.Status)
	}
	if rp := e.call("GET", "/app/servers", "user=2"); rp.Status != 401 {
		t.Errorf("an administrator's session = %d; the Application API is for keys", rp.Status)
	}
	rp := e.call("GET", "/app/servers", bearerOf(keyToken(clientKeyID, secretA)))
	if rp.Status != 403 || !strings.Contains(rp.detail(), "client API key on an endpoint that requires an application API key") {
		t.Errorf("a client key = %d %q", rp.Status, rp.detail())
	}
	if rp := e.call("GET", "/app/servers", app); rp.Status != 200 || rp.json()["user"] != float64(2) || rp.json()["key"] != true {
		t.Errorf("application key = %d %s", rp.Status, rp.Body)
	}
}

func TestApplicationKeyLevels(t *testing.T) {
	e := newEnv(t)
	app := bearerOf(keyToken(appKeyID, secretB))

	tests := []struct {
		method, path string
		want         int
	}{
		{"GET", "/app/servers", 200},  // rw: read
		{"POST", "/app/servers", 200}, // rw: write
		{"GET", "/app/users", 200},    // read: read
		{"POST", "/app/users", 403},   // read: write
		{"GET", "/app/nodes", 403},    // none
		{"GET", "/app/eggs", 403},     // not listed at all
	}
	for _, tt := range tests {
		if rp := e.call(tt.method, tt.path, app); rp.Status != tt.want {
			t.Errorf("%s %s = %d, want %d: %s", tt.method, tt.path, rp.Status, tt.want, rp.Body)
		}
	}
}

func TestAWriteOnlyLevelCannotRead(t *testing.T) {
	e := newEnv(t)
	k := e.f.keys[appKeyID]
	k.Access = map[string]auth.Access{"servers": auth.Access(2)}
	e.f.keys[appKeyID] = k
	app := bearerOf(keyToken(appKeyID, secretB))

	if rp := e.call("GET", "/app/servers", app); rp.Status != 403 {
		t.Errorf("read = %d; only the write bit is set", rp.Status)
	}
	if rp := e.call("POST", "/app/servers", app); rp.Status != 200 {
		t.Errorf("write = %d", rp.Status)
	}
}

func TestAnApplicationKeyOfAUserWhoIsNoLongerAnAdministrator(t *testing.T) {
	e := newEnv(t)
	u := e.f.users[2]
	u.RootAdmin = false
	e.f.users[2] = u

	rp := e.call("GET", "/app/servers", bearerOf(keyToken(appKeyID, secretB)))

	if rp.Status != 403 || rp.detail() != "This account does not have permission to access the API." {
		t.Errorf("got %d %q; the key must stop working the moment its user loses the role", rp.Status, rp.detail())
	}
}

func TestApplicationKeyAddressRestrictionAndLastUse(t *testing.T) {
	e := newEnv(t)
	k := e.f.keys[appKeyID]
	k.AllowedIPs = []string{"198.51.100.0/24"}
	e.f.keys[appKeyID] = k
	app := bearerOf(keyToken(appKeyID, secretB))

	if rp := e.call("GET", "/app/servers", app, "ip=203.0.113.9"); rp.Status != 403 {
		t.Errorf("a refused address = %d", rp.Status)
	}
	if rp := e.call("GET", "/app/servers", app, "ip=198.51.100.5"); rp.Status != 200 {
		t.Errorf("an allowed address = %d", rp.Status)
	}
	if len(e.f.touched) != 1 || len(e.f.blocked) != 1 {
		t.Errorf("touched %v, blocked %v; a refused request must not count as a use", e.f.touched, e.f.blocked)
	}
}

func TestAKeyThatDoesNotRevealWhetherTheSecretOrTheIdentifierWasWrong(t *testing.T) {
	e := newEnv(t)

	wrongSecret := e.call("GET", "/app/servers", bearerOf(keyToken(appKeyID, secretA)))
	wrongID := e.call("GET", "/app/servers", bearerOf(keyToken("ptla_zzzzzzzzzzz", secretB)))

	if string(wrongSecret.Body) != string(wrongID.Body) || wrongSecret.Status != 401 {
		t.Errorf("%d %s vs %d %s", wrongSecret.Status, wrongSecret.Body, wrongID.Status, wrongID.Body)
	}
}

func TestNewChecksItsDependencies(t *testing.T) {
	f := newFakes()
	good := Deps{Users: f, Keys: f, Servers: f, Nodes: f, Settings: f, Audit: f, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	tests := map[string]func(*Deps){
		"users": func(d *Deps) { d.Users = nil }, "keys": func(d *Deps) { d.Keys = nil }, "servers": func(d *Deps) { d.Servers = nil },
		"nodes": func(d *Deps) { d.Nodes = nil }, "settings": func(d *Deps) { d.Settings = nil }, "audit": func(d *Deps) { d.Audit = nil },
		"logger": func(d *Deps) { d.Logger = nil },
	}
	for name, mutate := range tests {
		d := good
		mutate(&d)
		if a, err := New(d); err == nil || a != nil {
			t.Errorf("New accepted a missing %s", name)
		}
	}
	a, err := New(good)
	if err != nil || a.d.Now == nil || time.Since(a.d.Now()) > time.Minute {
		t.Errorf("New = %v, %v; the clock must default to the real one", a, err)
	}
}
