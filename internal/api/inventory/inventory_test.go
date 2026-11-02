package inventory

import (
	"bufio"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/accountapi"
	"github.com/Helles01/raptor-panel/internal/api/allocapi"
	"github.com/Helles01/raptor-panel/internal/api/appallocs"
	"github.com/Helles01/raptor-panel/internal/api/applocations"
	"github.com/Helles01/raptor-panel/internal/api/appnests"
	"github.com/Helles01/raptor-panel/internal/api/appnodes"
	"github.com/Helles01/raptor-panel/internal/api/appserverdbs"
	"github.com/Helles01/raptor-panel/internal/api/appservers"
	"github.com/Helles01/raptor-panel/internal/api/appusers"
	"github.com/Helles01/raptor-panel/internal/api/authapi"
	"github.com/Helles01/raptor-panel/internal/api/backupapi"
	"github.com/Helles01/raptor-panel/internal/api/clientapi"
	"github.com/Helles01/raptor-panel/internal/api/controlapi"
	"github.com/Helles01/raptor-panel/internal/api/databaseapi"
	"github.com/Helles01/raptor-panel/internal/api/fileapi"
	"github.com/Helles01/raptor-panel/internal/api/healthapi"
	"github.com/Helles01/raptor-panel/internal/api/pageapi"
	"github.com/Helles01/raptor-panel/internal/api/remoteapi"
	"github.com/Helles01/raptor-panel/internal/api/scheduleapi"
	"github.com/Helles01/raptor-panel/internal/api/settingsapi"
	"github.com/Helles01/raptor-panel/internal/api/subuserapi"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/permissions"
)

type allowAll struct{}

func (allowAll) Authorize(echo.Context, api.Permission) error { return nil }

// registered puts every endpoint package on one router. Registering only builds method values, which a handler without its stores can
// do, so the zero value of each is enough. A package that is added to the program and not to this list fails TestEveryPackageIsListed.
func registered(t *testing.T) []api.Route {
	t.Helper()
	srv, err := api.New(api.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Authorizer: allowAll{}})
	if err != nil {
		t.Fatal(err)
	}
	g := srv.Router.Group
	for _, register := range []func(api.Group){
		(&accountapi.Handlers{}).Register, (&allocapi.Handlers{}).Register, (&appallocs.Handlers{}).Register,
		(&applocations.Handlers{}).Register, (&appnests.Handlers{}).Register, (&appnodes.Handlers{}).Register,
		(&appserverdbs.Handlers{}).Register, (&appservers.Handlers{}).Register, (&appusers.Handlers{}).Register,
		(&authapi.Handlers{}).Register, (&backupapi.Handlers{}).Register, (&clientapi.Handlers{}).Register,
		(&controlapi.Handlers{}).Register, (&databaseapi.Handlers{}).Register, (&fileapi.Handlers{}).Register,
		(&healthapi.Handlers{}).Register, (&pageapi.Handlers{}).Register, (&remoteapi.Handlers{}).Register,
		(&scheduleapi.Handlers{}).Register, (&settingsapi.Handlers{}).Register, (&subuserapi.Handlers{}).Register,
	} {
		register(g)
	}
	if _, err := srv.Handler(); err != nil {
		t.Fatalf("the router refuses its routes: %v", err)
	}
	return srv.Router.Routes()
}

var param = regexp.MustCompile(`:[A-Za-z_]+`)

// shape writes a route the way testdata/reference-routes.txt does: "GET /api/client/servers/{}/power".
func shape(r api.Route) string { return r.Method + " " + param.ReplaceAllString(r.Path, "{}") }

func reference(t *testing.T) (served map[string]bool, left []string) {
	t.Helper()
	f, err := os.Open("testdata/reference-routes.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	served = map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, "MISSING "):
			left = append(left, strings.TrimPrefix(line, "MISSING "))
		default:
			if served[line] {
				t.Fatalf("testdata lists %q twice", line)
			}
			served[line] = true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return served, left
}

// Raptor's own additions to the reference's routes. Each is on purpose.
var additions = map[string]string{
	"GET /healthz":  "liveness for a load balancer (18a)",
	"GET /readyz":   "readiness for a load balancer (18a)",
	"GET /assets/*": "the files of the frontend build, served by the panel itself (03)",
}

func TestRoutesAreTheReferencesRoutes(t *testing.T) {
	want, left := reference(t)
	got := map[string]bool{}
	for _, r := range registered(t) {
		s := shape(r)
		if got[s] {
			t.Errorf("%s is registered twice under different parameter names", s)
		}
		got[s] = true
	}

	var missing, extra []string
	for s := range want {
		if !got[s] {
			missing = append(missing, s)
		}
	}
	for s := range got {
		if _, ok := additions[s]; !want[s] && !ok {
			extra = append(extra, s)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("routes of the reference that are not served:\n  %s", strings.Join(missing, "\n  "))
	}
	if len(extra) > 0 {
		t.Errorf("routes that are served and neither in the reference nor on the list of additions:\n  %s", strings.Join(extra, "\n  "))
	}
	for s := range additions {
		if !got[s] {
			t.Errorf("the addition %s is not served any more", s)
		}
		if want[s] {
			t.Errorf("%s is both in the reference and an addition", s)
		}
	}

	// What is left out stays out: nothing answers below it.
	for _, prefix := range left {
		for _, r := range registered(t) {
			if r.Path == prefix || strings.HasPrefix(r.Path, prefix+"/") {
				t.Errorf("%s is left out on purpose and %s %s is served", prefix, r.Method, r.Path)
			}
		}
	}
}

// publicRoutes are the only routes that need no sign-in, and guestRoutes the only ones for people who are not signed in. A new one
// has to be written here, with a reason in the review.
var (
	publicRoutes = map[string]bool{
		"GET /sanctum/csrf-cookie": true, "GET /healthz": true, "GET /readyz": true,
		"GET /": true, "GET /account": true, "GET /auth/login": true, "GET /auth/password": true,
		"GET /auth/password/reset/{}": true, "GET /auth/*": true, "GET /locales/locale.json": true,
		"GET /assets/*": true, "GET /*": true,
	}
	guestRoutes = map[string]bool{
		"POST /auth/login": true, "POST /auth/login/checkpoint": true, "POST /auth/password": true, "POST /auth/password/reset": true,
	}
)

func TestWhoMayCallWhat(t *testing.T) {
	for _, r := range registered(t) {
		s, p := shape(r), r.Permission
		var want api.Kind
		switch {
		case strings.HasPrefix(r.Path, "/api/application/"):
			want = api.KindApplicationKey
		case strings.HasPrefix(r.Path, "/api/remote/"):
			want = api.KindDaemon
		case strings.HasPrefix(r.Path, "/api/client/servers/"):
			want = api.KindServer
		case strings.HasPrefix(r.Path, "/api/client"):
			want = api.KindAuthenticated
		case publicRoutes[s]:
			want = api.KindPublic
		case guestRoutes[s]:
			want = api.KindGuest
		case s == "POST /auth/logout":
			want = api.KindAuthenticated
		default:
			t.Errorf("%s is outside the API and on neither list", s)
			continue
		}
		if p.Kind != want {
			t.Errorf("%s is %s, want kind %s", s, p, want)
		}
		if (publicRoutes[s] || guestRoutes[s]) && want != api.KindPublic && want != api.KindGuest {
			t.Errorf("%s is on a list of open routes but under %s", s, p)
		}
	}
}

// An Application API key must be able to write to change anything, and the one read that hands out a node's secret is a write too.
func TestApplicationKeysNeedWriteToChange(t *testing.T) {
	for _, r := range registered(t) {
		if r.Permission.Kind != api.KindApplicationKey {
			continue
		}
		wantWrite := r.Method != "GET" || strings.HasSuffix(r.Path, "/configuration")
		if wantWrite != (r.Permission.Access == auth.AccessReadWrite) {
			t.Errorf("%s %s needs access %d", r.Method, r.Path, r.Permission.Access)
		}
	}
}

// The permission a server route asks for must be one that exists, or nobody but an owner could ever use the route.
func TestServerRoutesAskForRealPermissions(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range registered(t) {
		if r.Permission.Kind != api.KindServer || r.Permission.Resource == "" {
			continue
		}
		seen[r.Permission.Resource] = true
		if !permissions.Valid(r.Permission.Resource) {
			t.Errorf("%s %s asks for %q, which is not a permission", r.Method, r.Path, r.Permission.Resource)
		}
	}
	if len(seen) < 20 {
		t.Errorf("only %d different permissions are asked for", len(seen))
	}
}

// Nothing that changes anything is open to the public: the sign-in posts are for guests, and a guest is refused once signed in.
func TestPublicRoutesOnlyRead(t *testing.T) {
	for _, r := range registered(t) {
		if r.Permission.Kind == api.KindPublic && r.Method != "GET" {
			t.Errorf("%s %s is open to the public", r.Method, r.Path)
		}
	}
}

// A package that serves endpoints and is not in registered would escape every check above.
func TestEveryPackageIsListed(t *testing.T) {
	self, err := os.ReadFile("inventory_test.go")
	if err != nil {
		t.Fatal(err)
	}
	dirs, err := os.ReadDir("..")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		files, _ := filepath.Glob(filepath.Join("..", d.Name(), "*.go"))
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(src), ") Register(g api.Group)") {
				continue
			}
			found++
			if !strings.Contains(string(self), "internal/api/"+d.Name()+"\"") {
				t.Errorf("internal/api/%s registers routes and is not in the inventory", d.Name())
			}
			break
		}
	}
	if found < 20 {
		t.Errorf("only %d packages with routes were found", found)
	}
}
