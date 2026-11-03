package pageapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apitest"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/api/web"
)

const integrity = "sha384-4Z8GvaY1DzatXfQmNE8MlOkmQLjF+aX6/loBS9FPsV4nbXhU6z2Gngv+SNlNvPNF"

var errBoom = errors.New("connection to db.internal refused: password=hunter2")

type fakeUsers struct {
	users map[uint64]web.User
	err   error
}

func (f fakeUsers) Find(_ context.Context, id uint64) (web.User, error) {
	if f.err != nil {
		return web.User{}, f.err
	}
	u, ok := f.users[id]
	if !ok {
		return web.User{}, ErrNotFound
	}
	return u, nil
}

type allowAll struct{}

func (allowAll) Authorize(echo.Context, api.Permission) error { return nil }

type fakeSettings struct{ err error }

func (f fakeSettings) Site(context.Context) (web.Site, error) {
	return web.Site{Name: "Raptor Test", Locale: "en"}, f.err
}

func newDeps(t *testing.T) Deps {
	t.Helper()
	shell, err := web.NewShell(web.Manifest{"main.js": {Src: "/assets/bundle.js", Integrity: integrity}}, web.ShellOptions{})
	if err != nil {
		t.Fatal(err)
	}
	locales, err := web.EmbeddedLocales()
	if err != nil {
		t.Fatal(err)
	}
	limiter, err := middleware.NewLimiter(100, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return Deps{
		Shell: shell, Locales: locales,
		Assets: fstest.MapFS{
			"bundle.js":     {Data: []byte("console.log(1)")},
			"manifest.json": {Data: []byte("{}")},
		},
		Users: fakeUsers{users: map[uint64]web.User{
			7: {UUID: "11111111-1111-4111-8111-111111111111", Username: "ada", Email: "ada@example.com"},
			8: {UUID: "22222222-2222-4222-8222-222222222222", Username: "bob", Email: "bob@example.com"},
			// A store that wrongly knows a user 0 must still not make a guest session a person.
			0: {UUID: "00000000-0000-4000-8000-000000000000", Username: "nobody", Email: "nobody@example.com"},
		}},
		Settings:      fakeSettings{},
		LocaleLimiter: limiter,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

type rig struct {
	world  *apitest.World
	client *apitest.Client
}

func serve(t *testing.T, mutate func(*Deps)) *rig {
	t.Helper()
	d := newDeps(t)
	if mutate != nil {
		mutate(&d)
	}
	h, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	w := apitest.NewWorld()
	return &rig{world: w, client: w.Serve(t, h.Register)}
}

// login returns the cookie of a session of a user; user 0 gives the cookie of a guest session.
func (r *rig) login(t *testing.T, user uint64) *http.Cookie {
	t.Helper()
	ctx := context.Background()
	s, err := r.world.Sessions.Create(ctx, "203.0.113.9", "test")
	if err != nil {
		t.Fatal(err)
	}
	if user != 0 {
		if s, err = r.world.Sessions.Login(ctx, s, user); err != nil {
			t.Fatal(err)
		}
	}
	return r.world.Sessions.Cookie(s)
}

func TestAppPageForTheSignedIn(t *testing.T) {
	r := serve(t, nil)
	ada := r.login(t, 7)

	// The application's own pages are all answered by the one page; its router knows the rest.
	for _, path := range []string{"/", "/account", "/account/api", "/server/1a2b3c4d", "/server/1a2b3c4d/files/edit"} {
		rp := r.client.AsSession(ada, "GET", path, "")
		if rp.Status != 200 {
			t.Errorf("GET %s = %d", path, rp.Status)
			continue
		}
		h := rp.Header
		if !strings.HasPrefix(h.Get("Content-Type"), "text/html") || h.Get("Cache-Control") != "no-store" || !strings.Contains(h.Get("Content-Security-Policy"), "script-src") {
			t.Errorf("GET %s headers = %v", path, h)
		}
		if !rp.Contains("window.PterodactylUser") || !rp.Contains("ada@example.com") || !rp.Contains("Raptor Test") {
			t.Errorf("GET %s: the page lacks the user or the site: %s", path, rp.Raw)
		}
	}
}

func TestEachPageHasItsOwnUser(t *testing.T) {
	r := serve(t, nil)
	bob := r.client.AsSession(r.login(t, 8), "GET", "/", "")
	if !bob.Contains("bob@example.com") || bob.Contains("ada@example.com") {
		t.Errorf("the page of bob = %s", bob.Raw)
	}
}

func TestAppPageSendsGuestsToSignIn(t *testing.T) {
	r := serve(t, nil)
	for name, rp := range map[string]apitest.Reply{
		"no cookie":     r.client.As(0, "GET", "/", ""),
		"guest session": r.client.AsSession(r.login(t, 0), "GET", "/account", ""),
		"deep path":     r.client.As(0, "GET", "/server/1a2b3c4d", ""),
		"junk cookie":   r.client.AsSession(&http.Cookie{Name: "raptor_session", Value: "nonsense"}, "GET", "/", ""),
	} {
		if rp.Status != http.StatusFound || rp.Header.Get("Location") != "/auth/login" || rp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: %d %v", name, rp.Status, rp.Header)
		}
		if rp.Contains("PterodactylUser") || rp.Contains("ada@example.com") {
			t.Errorf("%s: the answer holds account data: %s", name, rp.Raw)
		}
	}
}

func TestAuthPages(t *testing.T) {
	r := serve(t, nil)
	guest := r.login(t, 0)
	for _, path := range []string{"/auth/login", "/auth/password", "/auth/password/reset/0123456789abcdef", "/auth/anything/else"} {
		for name, rp := range map[string]apitest.Reply{
			"no cookie": r.client.As(0, "GET", path, ""),
			"guest":     r.client.AsSession(guest, "GET", path, ""),
		} {
			if rp.Status != 200 || !rp.Contains("window.SiteConfiguration") || rp.Contains("PterodactylUser") || rp.Header.Get("Cache-Control") != "no-store" {
				t.Errorf("%s as %s: %d %s", path, name, rp.Status, rp.Raw)
			}
			// The reset token in the address is for the frontend; the server never writes it into the page.
			if rp.Contains("0123456789abcdef") {
				t.Errorf("%s as %s: the page repeats the token", path, name)
			}
		}
	}
}

func TestAuthPagesSendTheSignedInHome(t *testing.T) {
	r := serve(t, nil)
	ada := r.login(t, 7)
	for _, path := range []string{"/auth/login", "/auth/password", "/auth/password/reset/abc", "/auth/other"} {
		rp := r.client.AsSession(ada, "GET", path, "")
		if rp.Status != http.StatusFound || rp.Header.Get("Location") != "/" {
			t.Errorf("%s: %d %v", path, rp.Status, rp.Header)
		}
	}
}

// A session whose account was deleted must not bounce between the two pages.
func TestDeletedAccountIsAGuest(t *testing.T) {
	r := serve(t, nil)
	ghost := r.login(t, 99)
	if rp := r.client.AsSession(ghost, "GET", "/", ""); rp.Status != http.StatusFound || rp.Header.Get("Location") != "/auth/login" {
		t.Errorf("app page: %d %v", rp.Status, rp.Header)
	}
	if rp := r.client.AsSession(ghost, "GET", "/auth/login", ""); rp.Status != 200 || rp.Contains("PterodactylUser") {
		t.Errorf("login page: %d %s", rp.Status, rp.Raw)
	}
}

func TestStoreFailuresAreGeneric(t *testing.T) {
	for name, mutate := range map[string]func(*Deps){
		"users":    func(d *Deps) { d.Users = fakeUsers{err: errBoom} },
		"settings": func(d *Deps) { d.Settings = fakeSettings{err: errBoom} },
	} {
		t.Run(name, func(t *testing.T) {
			r := serve(t, mutate)
			ada := r.login(t, 7)
			var replies []apitest.Reply
			if name == "users" {
				replies = []apitest.Reply{r.client.AsSession(ada, "GET", "/", ""), r.client.AsSession(ada, "GET", "/auth/login", "")}
			} else {
				replies = []apitest.Reply{r.client.AsSession(ada, "GET", "/", ""), r.client.As(0, "GET", "/auth/login", "")}
			}
			for _, rp := range replies {
				if rp.Status != 500 || rp.Contains("hunter2") || rp.Contains("db.internal") {
					t.Errorf("%d %s", rp.Status, rp.Raw)
				}
			}
		})
	}
}

func TestPlacesTheFrontendDoesNotAnswerFor(t *testing.T) {
	r := serve(t, nil)
	ada := r.login(t, 7)
	for _, path := range []string{"/api", "/api/nothing", "/admin", "/admin/users", "/daemon", "/daemon/x", "/auth"} {
		for name, rp := range map[string]apitest.Reply{"guest": r.client.As(0, "GET", path, ""), "signed in": r.client.AsSession(ada, "GET", path, "")} {
			if rp.Status != 404 || rp.Contains("PterodactylUser") || rp.Contains("<html") {
				t.Errorf("%s as %s: %d %s", path, name, rp.Status, rp.Raw)
			}
		}
	}
	// Only the first part of a path decides; a word that merely starts like a reserved one is the application's.
	if rp := r.client.AsSession(ada, "GET", "/apiary", ""); rp.Status != 200 {
		t.Errorf("/apiary = %d", rp.Status)
	}
}

func TestOnlyGet(t *testing.T) {
	r := serve(t, nil)
	for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		for _, path := range []string{"/", "/account", "/auth/login", "/assets/bundle.js", "/locales/locale.json", "/server/abc"} {
			if rp := r.client.As(0, method, path, ""); rp.Status < 400 || rp.Status >= 500 {
				t.Errorf("%s %s = %d", method, path, rp.Status)
			}
		}
	}
}

func TestAssets(t *testing.T) {
	r := serve(t, nil)
	rp := r.client.As(0, "GET", "/assets/bundle.js", "")
	if rp.Status != 200 || string(rp.Raw) != "console.log(1)" || !strings.Contains(rp.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("bundle: %d %v %s", rp.Status, rp.Header, rp.Raw)
	}
	if rp := r.client.As(0, "GET", "/assets/manifest.json", ""); rp.Status != 200 || rp.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("manifest: %d %v", rp.Status, rp.Header)
	}
	for _, path := range []string{"/assets/missing.js", "/assets/a/b.js", "/assets/.env", "/assets/", "/assets/..%2fbundle.js", "/assets/%2e%2e/x"} {
		if rp := r.client.As(0, "GET", path, ""); rp.Status != 404 {
			t.Errorf("%s = %d", path, rp.Status)
		}
	}
}

func TestLocales(t *testing.T) {
	r := serve(t, nil)
	rp := r.client.As(0, "GET", "/locales/locale.json?locale=en&namespace=translation", "")
	if rp.Status != 200 || !rp.Has("en", "translation") || !strings.Contains(rp.Header.Get("Cache-Control"), "max-age=3600") {
		t.Errorf("translation: %d %v %.120s", rp.Status, rp.Header, rp.Raw)
	}
	for name, target := range map[string]string{
		"unknown locale":    "/locales/locale.json?locale=zz&namespace=translation",
		"unknown namespace": "/locales/locale.json?locale=en&namespace=nothing",
	} {
		if rp := r.client.As(0, "GET", target, ""); rp.Status != 404 {
			t.Errorf("%s = %d", name, rp.Status)
		}
	}
	// A value that could name a file is refused before anything is looked up.
	for _, target := range []string{
		"/locales/locale.json",
		"/locales/locale.json?locale=../../etc&namespace=translation",
		"/locales/locale.json?locale=en&namespace=../translation",
		"/locales/locale.json?locale=en&locale=hu&namespace=translation",
	} {
		if rp := r.client.As(0, "GET", target, ""); rp.Status != 422 && rp.Status != 400 {
			t.Errorf("%s = %d", target, rp.Status)
		}
	}
}

func TestLocalesAreLimited(t *testing.T) {
	r := serve(t, func(d *Deps) { d.LocaleLimiter, _ = middleware.NewLimiter(2, time.Minute) })
	statuses := []int{}
	for i := 0; i < 3; i++ {
		statuses = append(statuses, r.client.As(0, "GET", "/locales/locale.json?locale=en&namespace=translation", "").Status)
	}
	if statuses[0] != 200 || statuses[1] != 200 || statuses[2] != http.StatusTooManyRequests {
		t.Errorf("statuses = %v", statuses)
	}
}

func TestWhatTheUserHasInItsNameStaysInsideTheScript(t *testing.T) {
	r := serve(t, func(d *Deps) {
		d.Users = fakeUsers{users: map[uint64]web.User{7: {UUID: "u", Username: "</script><script>alert(1)</script>", Email: "x@example.com"}}}
	})
	rp := r.client.AsSession(r.login(t, 7), "GET", "/", "")
	if rp.Status != 200 || rp.Contains("</script><script>alert(1)") {
		t.Errorf("%d %s", rp.Status, rp.Raw)
	}
}

func TestEveryRouteIsPublicAndGet(t *testing.T) {
	h, err := New(newDeps(t))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(api.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Authorizer: allowAll{}})
	if err != nil {
		t.Fatal(err)
	}
	h.Register(srv.Router.Group)
	for _, rt := range srv.Router.Routes() {
		if rt.Method != "GET" || rt.Permission.Kind != api.KindPublic {
			t.Errorf("%s %s has %s", rt.Method, rt.Path, rt.Permission)
		}
	}
	if len(srv.Router.Routes()) != 9 {
		t.Errorf("%d routes", len(srv.Router.Routes()))
	}
}

func TestNewNeedsItsDependencies(t *testing.T) {
	for name, mutate := range map[string]func(*Deps){
		"shell":    func(d *Deps) { d.Shell = nil },
		"locales":  func(d *Deps) { d.Locales = nil },
		"assets":   func(d *Deps) { d.Assets = nil },
		"users":    func(d *Deps) { d.Users = nil },
		"settings": func(d *Deps) { d.Settings = nil },
		"limiter":  func(d *Deps) { d.LocaleLimiter = nil },
		"logger":   func(d *Deps) { d.Logger = nil },
	} {
		d := newDeps(t)
		mutate(&d)
		if _, err := New(d); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

type fakeOrigins struct {
	list  []string
	err   error
	calls int
}

func (f *fakeOrigins) ConnectSources(context.Context) ([]string, error) {
	f.calls++
	return f.list, f.err
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func csp(rp apitest.Reply) string { return rp.Header.Get("Content-Security-Policy") }

func TestThePageAllowsTheNodes(t *testing.T) {
	origins := &fakeOrigins{list: web.NodeSources("https", "n1.example.com", 8080)}
	r := serve(t, func(d *Deps) { d.Origins = origins })
	rp := r.client.AsSession(r.login(t, 7), "GET", "/", "")
	if !strings.Contains(csp(rp), "https://n1.example.com:8080") || !strings.Contains(csp(rp), "wss://n1.example.com:8080") {
		t.Errorf("csp = %s", csp(rp))
	}
	// The sign-in pages talk to no node and are not given any.
	if rp := r.client.As(0, "GET", "/auth/login", ""); strings.Contains(csp(rp), "n1.example.com") || origins.calls != 1 {
		t.Errorf("csp = %s, the store was asked %d times", csp(rp), origins.calls)
	}
}

func TestTheOriginsAreKeptForAWhile(t *testing.T) {
	origins := &fakeOrigins{list: []string{"https://n1.example.com:8080"}}
	now := &clock{now: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}
	r := serve(t, func(d *Deps) { d.Origins, d.Now, d.OriginsTTL = origins, now.Now, time.Minute })
	ada := r.login(t, 7)
	get := func() string { return csp(r.client.AsSession(ada, "GET", "/", "")) }

	get()
	get()
	now.now = now.now.Add(59 * time.Second)
	get()
	if origins.calls != 1 {
		t.Fatalf("the store was asked %d times within the minute", origins.calls)
	}
	// A node was added; after the minute the page knows it.
	origins.list = []string{"https://n1.example.com:8080", "https://n2.example.com:8080"}
	if strings.Contains(get(), "n2.example.com") {
		t.Error("the new node was known before the list was asked for again")
	}
	now.now = now.now.Add(2 * time.Second)
	if got := get(); !strings.Contains(got, "n2.example.com") || origins.calls != 2 {
		t.Errorf("csp = %s, calls %d", got, origins.calls)
	}
}

func TestADownStoreKeepsTheLastListAndIsNotAskedByEveryPage(t *testing.T) {
	origins := &fakeOrigins{list: []string{"https://n1.example.com:8080"}}
	now := &clock{now: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}
	r := serve(t, func(d *Deps) { d.Origins, d.Now, d.OriginsTTL = origins, now.Now, time.Minute })
	ada := r.login(t, 7)
	r.client.AsSession(ada, "GET", "/", "")

	origins.err = errBoom
	now.now = now.now.Add(2 * time.Minute)
	for i := 0; i < 3; i++ {
		rp := r.client.AsSession(ada, "GET", "/", "")
		if rp.Status != 200 || !strings.Contains(csp(rp), "n1.example.com") || rp.Contains("hunter2") {
			t.Fatalf("page %d: %d %s", i, rp.Status, csp(rp))
		}
	}
	if origins.calls != 2 {
		t.Errorf("the store was asked %d times, want 1 + 1", origins.calls)
	}
}

func TestADownStoreAtTheStartGivesNoOriginsButAPage(t *testing.T) {
	origins := &fakeOrigins{err: errBoom}
	r := serve(t, func(d *Deps) { d.Origins = origins })
	rp := r.client.AsSession(r.login(t, 7), "GET", "/", "")
	if rp.Status != 200 || !strings.Contains(csp(rp), "connect-src 'self'") {
		t.Errorf("%d %s", rp.Status, csp(rp))
	}
}

func TestOriginsAreOptional(t *testing.T) {
	r := serve(t, nil)
	rp := r.client.AsSession(r.login(t, 7), "GET", "/", "")
	if rp.Status != 200 || !strings.Contains(csp(rp)+";", "connect-src 'self';") {
		t.Errorf("%d %s", rp.Status, csp(rp))
	}
}

func TestOriginsFromTheTableCannotAddADirective(t *testing.T) {
	origins := &fakeOrigins{list: []string{"https://ok.example.com:8080", "https://evil.example.com; script-src *", "*"}}
	r := serve(t, func(d *Deps) { d.Origins = origins })
	got := csp(r.client.AsSession(r.login(t, 7), "GET", "/", ""))
	if !strings.Contains(got, "https://ok.example.com:8080") || strings.Contains(got, "evil") || strings.Contains(got, "script-src *") {
		t.Errorf("csp = %s", got)
	}
}

func TestTheListIsAskedForAgainAtExactlyTheTimeLimit(t *testing.T) {
	origins := &fakeOrigins{list: []string{"https://n1.example.com:8080"}}
	now := &clock{now: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}
	r := serve(t, func(d *Deps) { d.Origins, d.Now, d.OriginsTTL = origins, now.Now, time.Minute })
	ada := r.login(t, 7)
	r.client.AsSession(ada, "GET", "/", "")
	now.now = now.now.Add(time.Minute)
	r.client.AsSession(ada, "GET", "/", "")
	if origins.calls != 2 {
		t.Errorf("the store was asked %d times", origins.calls)
	}
}

func TestTheDefaultsOfTheOriginsList(t *testing.T) {
	// The default time limit is long enough that two pages in a row share one question.
	origins := &fakeOrigins{list: []string{"https://n1.example.com:8080"}}
	r := serve(t, func(d *Deps) { d.Origins = origins })
	ada := r.login(t, 7)
	r.client.AsSession(ada, "GET", "/", "")
	r.client.AsSession(ada, "GET", "/account", "")
	if origins.calls != 1 {
		t.Errorf("the store was asked %d times for two pages", origins.calls)
	}
	// And the clock is the real one: a short time limit runs out.
	origins = &fakeOrigins{list: []string{"https://n1.example.com:8080"}}
	r = serve(t, func(d *Deps) { d.Origins, d.OriginsTTL = origins, 20*time.Millisecond })
	ada = r.login(t, 7)
	r.client.AsSession(ada, "GET", "/", "")
	time.Sleep(60 * time.Millisecond)
	r.client.AsSession(ada, "GET", "/", "")
	if origins.calls != 2 {
		t.Errorf("with the real clock the store was asked %d times", origins.calls)
	}
}
