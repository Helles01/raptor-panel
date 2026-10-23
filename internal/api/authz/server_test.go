package authz

import (
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api"

	"github.com/Helles01/raptor-panel/internal/servers"
)

func (e *env) setState(s servers.State) {
	srv := e.f.servers["aaaa1111"]
	srv.State = s
	e.f.servers["aaaa1111"] = srv
}

func TestServerReferences(t *testing.T) {
	e := newEnv(t)
	tests := []struct {
		ref  string
		want int
	}{
		{"aaaa1111", 200},
		{"AAAA1111", 200},
		{"aaaa1111-2222-4333-8444-555555555555", 200},
		{"AAAA1111-2222-4333-8444-555555555555", 200},
		{"bbbb2222", 404},
		{"aaaa111", 404},
		{"aaaa11111", 404},
		{"aaaa1111-2222-4333-8444-55555555555", 404},
		{"zzzzzzzz", 404},
		{"%2e%2e", 404},
		{"aaaa1111%00", 404},
		{"aaaa1111,bbbb2222", 404},
	}
	for _, tt := range tests {
		if rp := e.call("GET", "/servers/"+tt.ref+"/any", "user=3"); rp.Status != tt.want {
			t.Errorf("/servers/%s = %d, want %d", tt.ref, rp.Status, tt.want)
		}
	}
}

func TestAMalformedReferenceIsNotLookedUp(t *testing.T) {
	e := newEnv(t)

	for _, ref := range []string{"aaaa111", "zzzzzzzz", "aaaa1111,bbbb", "%00", ".."} {
		e.call("GET", "/servers/"+ref+"/any", "user=3")
	}

	if e.f.finds != 0 {
		t.Errorf("the store was asked %d times about references that cannot name a server", e.f.finds)
	}
}

func TestAnUnknownKindOfPermissionIsRefused(t *testing.T) {
	e := newEnv(t)
	az, err := New(Deps{Users: e.f, Keys: e.f, Servers: e.f, Nodes: e.f, Settings: e.f, Audit: e.f, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	c := echo.New().NewContext(httptest.NewRequest("GET", "/", nil), httptest.NewRecorder())

	if err := az.Authorize(c, api.Permission{Kind: "everyone"}); err == nil {
		t.Error("a kind the authorizer does not know must be refused, not allowed")
	}
}

func TestServerAccessOfOwnerAdministratorAndSubuser(t *testing.T) {
	e := newEnv(t)

	owner := e.call("GET", "/servers/aaaa1111/any", "user=3")
	admin := e.call("GET", "/servers/aaaa1111/any", "user=2")
	sub := e.call("GET", "/servers/aaaa1111/any", "user=4")

	for name, rp := range map[string]reply{"owner": owner, "admin": admin, "subuser": sub} {
		if rp.Status != 200 || rp.json()["server"] != float64(100) {
			t.Errorf("%s: %d %s", name, rp.Status, rp.Body)
		}
	}
	if got := asStrings(owner.json()["perms"]); !slices.Equal(got, []string{"*"}) {
		t.Errorf("owner perms = %v", got)
	}
	if got := asStrings(admin.json()["perms"]); !slices.Contains(got, "*") || !slices.Contains(got, "admin.websocket.errors") {
		t.Errorf("admin perms = %v", got)
	}
	if got := asStrings(sub.json()["perms"]); !slices.Equal(got, []string{"control.start", "websocket.connect"}) {
		t.Errorf("subuser perms = %v", got)
	}
}

func asStrings(v any) []string {
	list, _ := v.([]any)
	var out []string
	for _, x := range list {
		out = append(out, x.(string))
	}
	return out
}

func TestAStrangerDoesNotLearnThatAServerExists(t *testing.T) {
	e := newEnv(t)

	stranger := e.call("GET", "/servers/aaaa1111/any", "user=5")
	missing := e.call("GET", "/servers/bbbb2222/any", "user=5")

	if stranger.Status != 404 || string(stranger.Body) != string(missing.Body) {
		t.Errorf("a server that exists: %d %s\none that does not: %d %s", stranger.Status, stranger.Body, missing.Status, missing.Body)
	}
}

func TestServerRoutesNeedASignedInUser(t *testing.T) {
	e := newEnv(t)

	if rp := e.call("GET", "/servers/aaaa1111/any"); rp.Status != 401 {
		t.Errorf("anonymous = %d", rp.Status)
	}
}

func TestServerRouteByClientKey(t *testing.T) {
	e := newEnv(t)
	addKey(e.f, "ptlc_ownerkey000", secretA, 1, 3)

	rp := e.call("GET", "/servers/aaaa1111/any", bearerOf(keyToken("ptlc_ownerkey000", secretA)))

	if rp.Status != 200 || rp.json()["user"] != float64(3) || rp.json()["key"] != true {
		t.Errorf("got %d %s", rp.Status, rp.Body)
	}
}

func TestServerPermissionOfARoute(t *testing.T) {
	e := newEnv(t)

	if rp := e.call("POST", "/servers/aaaa1111/power", "user=4"); rp.Status != 200 {
		t.Errorf("a subuser with the permission = %d", rp.Status)
	}
	e.f.subusers[[2]uint64{100, 4}] = []string{"websocket.connect"}
	rp := e.call("POST", "/servers/aaaa1111/power", "user=4")
	if rp.Status != 403 || rp.detail() != "This action is unauthorized." {
		t.Errorf("a subuser without it = %d %q", rp.Status, rp.detail())
	}
	if rp := e.call("POST", "/servers/aaaa1111/power", "user=3"); rp.Status != 200 {
		t.Errorf("the owner = %d", rp.Status)
	}
	if rp := e.call("POST", "/servers/aaaa1111/power", "user=2"); rp.Status != 200 {
		t.Errorf("an administrator = %d", rp.Status)
	}
}

func TestASubuserWithNoPermissionsAtAll(t *testing.T) {
	e := newEnv(t)
	e.f.subusers[[2]uint64{100, 4}] = nil

	if rp := e.call("GET", "/servers/aaaa1111/any", "user=4"); rp.Status != 200 {
		t.Errorf("a route that asks for no permission = %d: being a subuser is access enough", rp.Status)
	}
	if rp := e.call("POST", "/servers/aaaa1111/power", "user=4"); rp.Status != 403 {
		t.Errorf("a route that asks for one = %d", rp.Status)
	}
}

func TestOnlyTheWildcardCoversAdminPermissions(t *testing.T) {
	e := newEnv(t)
	e.f.subusers[[2]uint64{100, 4}] = []string{"*"}

	if rp := e.call("POST", "/servers/aaaa1111/power", "user=4"); rp.Status != 200 {
		t.Errorf("a wildcard subuser = %d", rp.Status)
	}
}

func TestServerStates(t *testing.T) {
	suspended := servers.State{Status: servers.StatusSuspended}
	installing := servers.State{Status: servers.StatusInstalling}
	installFailed := servers.State{Status: servers.StatusInstallFailed}
	reinstallFailed := servers.State{Status: servers.StatusReinstallFailed}
	restoring := servers.State{Status: servers.StatusRestoringBackup}
	maintenance := servers.State{NodeMaintenance: true}
	transferring := servers.State{Transferring: true}

	const view, normal, ws, reinstall = "GET /servers/aaaa1111", "GET /servers/aaaa1111/any", "GET /servers/aaaa1111/ws", "POST /servers/aaaa1111/reinstall"
	tests := []struct {
		name  string
		state servers.State
		route string
		user  string
		want  int
	}{
		{"normal: any route", servers.State{}, normal, "user=3", 200},
		{"normal: the websocket", servers.State{}, ws, "user=3", 200},
		{"normal: a reinstall", servers.State{}, reinstall, "user=3", 200},

		{"suspended: the view", suspended, view, "user=3", 200},
		{"suspended: an ordinary route", suspended, normal, "user=3", 409},
		{"suspended: the websocket of the owner", suspended, ws, "user=3", 409},
		{"suspended: the websocket of an administrator", suspended, ws, "user=2", 409},
		{"suspended: a reinstall", suspended, reinstall, "user=3", 409},

		{"installing: the view", installing, view, "user=3", 200},
		{"installing: an ordinary route", installing, normal, "user=3", 409},
		{"installing: the websocket of the owner", installing, ws, "user=3", 409},
		{"installing: the websocket of an administrator", installing, ws, "user=2", 200},
		{"installing: a reinstall", installing, reinstall, "user=3", 409},

		{"failed install: an ordinary route", installFailed, normal, "user=3", 409},
		{"failed install: the owner reinstalls", installFailed, reinstall, "user=3", 200},
		{"failed install: the websocket of an administrator", installFailed, ws, "user=2", 200},
		{"failed install: the websocket of the owner", installFailed, ws, "user=3", 409},

		{"failed reinstall: an ordinary route", reinstallFailed, normal, "user=3", 200},
		{"failed reinstall: a reinstall", reinstallFailed, reinstall, "user=3", 200},

		{"restoring: an ordinary route", restoring, normal, "user=3", 409},
		{"restoring: the websocket of an administrator", restoring, ws, "user=2", 200},
		{"restoring: a reinstall", restoring, reinstall, "user=3", 409},

		{"maintenance: the view", maintenance, view, "user=3", 200},
		{"maintenance: an ordinary route", maintenance, normal, "user=3", 409},
		{"maintenance: the websocket of an administrator", maintenance, ws, "user=2", 409},

		{"transferring: an ordinary route", transferring, normal, "user=3", 409},
		{"transferring: the websocket of an administrator", transferring, ws, "user=2", 200},
		{"transferring: a reinstall", transferring, reinstall, "user=3", 409},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.setState(tt.state)
			method, path, _ := strings.Cut(tt.route, " ")

			rp := e.call(method, path, tt.user)

			if rp.Status != tt.want {
				t.Fatalf("status = %d, want %d: %s", rp.Status, tt.want, rp.Body)
			}
			if tt.want == 409 && rp.code() != "ConflictHttpException" {
				t.Errorf("code = %q", rp.code())
			}
		})
	}
}

func TestConflictMessagesAreTheReferences(t *testing.T) {
	tests := []struct {
		state servers.State
		want  string
	}{
		{servers.State{Status: servers.StatusSuspended}, "currently suspended"},
		{servers.State{NodeMaintenance: true}, "under maintenance"},
		{servers.State{Status: servers.StatusInstalling}, "not yet completed its installation"},
		{servers.State{Status: servers.StatusRestoringBackup}, "restoring from a backup"},
		{servers.State{Transferring: true}, "being transferred"},
	}
	for _, tt := range tests {
		e := newEnv(t)
		e.setState(tt.state)

		if rp := e.call("GET", "/servers/aaaa1111/any", "user=3"); !strings.Contains(rp.detail(), tt.want) {
			t.Errorf("%+v: detail = %q, want it to contain %q", tt.state, rp.detail(), tt.want)
		}
	}
}

func TestTheOrderOfTheServerChecks(t *testing.T) {
	e := newEnv(t)
	e.setState(servers.State{Status: servers.StatusSuspended})
	e.f.subusers[[2]uint64{100, 4}] = []string{"websocket.connect"}

	if rp := e.call("POST", "/servers/aaaa1111/power", "user=4"); rp.Status != 409 {
		t.Errorf("a subuser without the permission on a suspended server = %d, want the state first, as in the reference", rp.Status)
	}
	if rp := e.call("POST", "/servers/aaaa1111/power", "user=5"); rp.Status != 404 {
		t.Errorf("a stranger on a suspended server = %d, want access first", rp.Status)
	}
}

func TestAnAdministratorWithoutTheStateExemptionIsStopped(t *testing.T) {
	e := newEnv(t)
	e.setState(servers.State{Status: servers.StatusInstalling})

	if rp := e.call("GET", "/servers/aaaa1111/any", "user=2"); rp.Status != 409 {
		t.Errorf("status = %d; only the websocket route is open to an administrator in a bad state", rp.Status)
	}
}

func TestAFailingServerStoreIsAServerError(t *testing.T) {
	e := newEnv(t)
	e.f.failFind = true

	rp := e.call("GET", "/servers/aaaa1111/any", "user=3")

	if rp.Status != 500 || strings.Contains(string(rp.Body), "db down") {
		t.Errorf("got %d %s", rp.Status, rp.Body)
	}
}

func TestDaemonAuthentication(t *testing.T) {
	e := newEnv(t)
	good := "nodeid0123456789." + strings.Repeat("t", 64)

	rp := e.call("GET", "/remote/servers", bearerOf(good))

	if rp.Status != 200 || rp.json()["node"] != float64(7) {
		t.Fatalf("got %d %s", rp.Status, rp.Body)
	}
	if _, has := rp.json()["user"]; has {
		t.Error("a node is not a user")
	}
}

func TestDaemonAuthenticationWithoutAHeader(t *testing.T) {
	e := newEnv(t)

	rp := e.call("GET", "/remote/servers")

	if rp.Status != 401 || rp.Header.Get("WWW-Authenticate") != "Bearer" || !strings.Contains(rp.detail(), "must include an Authorization header") {
		t.Errorf("got %d %q %q", rp.Status, rp.Header.Get("WWW-Authenticate"), rp.detail())
	}
}

func TestDaemonAuthenticationRefusesABadFormat(t *testing.T) {
	e := newEnv(t)

	for _, tok := range []string{"nodetoken", ".token", "id.", "a.b.c", "", "id.token.", ".."} {
		rp := e.call("GET", "/remote/servers", "Authorization: Bearer "+tok)
		if rp.Status != 400 && rp.Status != 401 {
			t.Errorf("token %q = %d", tok, rp.Status)
		}
		if tok != "" && rp.Status != 400 {
			t.Errorf("token %q = %d, want 400", tok, rp.Status)
		}
	}
}

func TestDaemonAuthenticationSaysTheSameForEveryWrongToken(t *testing.T) {
	e := newEnv(t)

	unknown := e.call("GET", "/remote/servers", bearerOf("nope000000000000."+strings.Repeat("t", 64)))
	wrong := e.call("GET", "/remote/servers", bearerOf("nodeid0123456789."+strings.Repeat("x", 64)))
	short := e.call("GET", "/remote/servers", bearerOf("nodeid0123456789."+strings.Repeat("t", 63)))
	long := e.call("GET", "/remote/servers", bearerOf("nodeid0123456789."+strings.Repeat("t", 65)))

	for name, rp := range map[string]reply{"unknown": unknown, "wrong": wrong, "short": short, "long": long} {
		if rp.Status != 403 || string(rp.Body) != string(unknown.Body) {
			t.Errorf("%s: %d %s", name, rp.Status, rp.Body)
		}
	}
}

func TestDaemonAuthenticationNeedsAnExactNodeToken(t *testing.T) {
	e := newEnv(t)

	if rp := e.call("GET", "/remote/servers", "user=2"); rp.Status != 401 {
		t.Errorf("a session on a daemon route = %d", rp.Status)
	}
	if rp := e.call("GET", "/remote/servers", bearerOf(keyToken(appKeyID, secretB))); rp.Status == 200 {
		t.Error("an API key opened a daemon route")
	}
}

func TestFailingStoresAreServerErrors(t *testing.T) {
	node := bearerOf("nodeid0123456789." + strings.Repeat("t", 64))
	key := bearerOf(keyToken(clientKeyID, secretA))
	fail := errors.New("db down")
	tests := []struct {
		name   string
		break_ func(*fakes)
		path   string
		opts   []string
	}{
		{"nodes", func(f *fakes) { f.nodeErr = fail }, "/remote/servers", []string{node}},
		{"keys", func(f *fakes) { f.keyErr = fail }, "/user", []string{key}},
		{"users by key", func(f *fakes) { f.userErr = fail }, "/user", []string{key}},
		{"users by session", func(f *fakes) { f.userErr = fail }, "/user", []string{"user=1"}},
	}
	for _, tt := range tests {
		e := newEnv(t)
		tt.break_(e.f)

		rp := e.call("GET", tt.path, tt.opts...)

		if rp.Status != 500 || strings.Contains(string(rp.Body), "db down") {
			t.Errorf("%s: %d %s", tt.name, rp.Status, rp.Body)
		}
	}
}
