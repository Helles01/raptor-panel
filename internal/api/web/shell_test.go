package web

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

const testIntegrity = "sha384-4Z8GvaY1DzatXfQmNE8MlOkmQLjF+aX6/loBS9FPsV4nbXhU6z2Gngv+SNlNvPNF"

func testManifest() Manifest {
	return Manifest{
		"main.js": {Src: "/assets/bundle.35150288.js", Integrity: testIntegrity},
		"auth.js": {Src: "/assets/auth.285299d4.js", Integrity: testIntegrity},
	}
}

func newTestShell(t *testing.T, opts ShellOptions) *Shell {
	t.Helper()
	s, err := NewShell(testManifest(), opts)
	if err != nil {
		t.Fatalf("NewShell: %v", err)
	}
	return s
}

func testUser() *User {
	return &User{
		UUID: "9f4d2c10-0000-4000-8000-000000000001", Username: "ada", Email: "ada@example.com",
		RootAdmin: true, UseTotp: true, Language: "en",
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), UpdatedAt: time.Date(2026, 6, 7, 8, 9, 10, 0, time.UTC),
	}
}

func render(t *testing.T, s *Shell, page Page, user *User, site Site) (string, http.Header) {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := s.Render(rec, page, user, site); err != nil {
		t.Fatalf("Render: %v", err)
	}
	return rec.Body.String(), rec.Header()
}

// global extracts and decodes the JSON that the page assigns to window.<name>.
func global(t *testing.T, page, name string, into any) {
	t.Helper()
	marker := "window." + name + " = "
	start := strings.Index(page, marker)
	if start < 0 {
		t.Fatalf("window.%s is not set in the page:\n%s", name, page)
	}
	rest := page[start+len(marker):]
	end := strings.Index(rest, ";</script>")
	if end < 0 {
		t.Fatalf("window.%s is not terminated:\n%s", name, rest)
	}
	if err := json.Unmarshal([]byte(rest[:end]), into); err != nil {
		t.Fatalf("window.%s is not valid JSON: %v\n%s", name, err, rest[:end])
	}
}

func TestRenderApp(t *testing.T) {
	s := newTestShell(t, ShellOptions{})
	page, header := render(t, s, PageApp, testUser(), Site{Name: "My Panel", Locale: "en"})

	var user map[string]any
	global(t, page, "PterodactylUser", &user)
	want := map[string]any{
		"uuid": "9f4d2c10-0000-4000-8000-000000000001", "username": "ada", "email": "ada@example.com",
		"root_admin": true, "use_totp": true, "language": "en",
		"created_at": "2026-01-02T03:04:05Z", "updated_at": "2026-06-07T08:09:10Z",
	}
	if len(user) != len(want) {
		t.Errorf("window.PterodactylUser has keys %v, want exactly %v (no id, no secrets)", user, want)
	}
	for k, v := range want {
		if user[k] != v {
			t.Errorf("PterodactylUser.%s = %v, want %v", k, user[k], v)
		}
	}

	var site map[string]any
	global(t, page, "SiteConfiguration", &site)
	recaptcha := site["recaptcha"].(map[string]any)
	if site["name"] != "My Panel" || site["locale"] != "en" || recaptcha["enabled"] != false || recaptcha["siteKey"] != "" {
		t.Errorf("window.SiteConfiguration = %v", site)
	}

	// Attribute values are HTML escaped ("+" becomes &#43;); the browser decodes them again.
	decoded := html.UnescapeString(page)
	for _, want := range []string{
		"<title>My Panel</title>",
		`<body class="bg-neutral-800">`,
		`<div id="modal-portal"></div>`,
		`<div id="app"></div>`,
		`<script src="/assets/bundle.35150288.js" integrity="` + testIntegrity + `" crossorigin="anonymous"></script>`,
	} {
		if !strings.Contains(decoded, want) {
			t.Errorf("the page lacks %q:\n%s", want, page)
		}
	}
	if header.Get("Cache-Control") != "no-store" || !strings.HasPrefix(header.Get("Content-Type"), "text/html") {
		t.Errorf("headers = %v", header)
	}
}

func TestRenderAuth(t *testing.T) {
	page, _ := render(t, newTestShell(t, ShellOptions{}), PageAuth, nil, Site{Name: "Panel", Locale: "en"})

	if strings.Contains(page, "PterodactylUser") {
		t.Errorf("the login page must not define a user:\n%s", page)
	}
	if !strings.Contains(page, `<body class="bg-neutral-900">`) || strings.Contains(page, "modal-portal") {
		t.Errorf("unexpected body of the login page:\n%s", page)
	}
	if !strings.Contains(page, "window.SiteConfiguration") {
		t.Error("the site configuration is missing")
	}
}

// Values from the database reach the page as JavaScript and HTML. None of them may be able to break out.
func TestHostileValuesCannotBreakOutOfThePage(t *testing.T) {
	s := newTestShell(t, ShellOptions{})
	hostile := []string{
		"</script><script>alert(1)</script>",
		`"; alert(1); //`,
		"'; alert(1); //",
		"<img src=x onerror=alert(1)>",
		"line\u2028separator\u2029here",
		"--></script><!--",
		"<!-- <script>",
		"${alert(1)}",
		"`+alert(1)+`",
	}
	for _, value := range hostile {
		user := testUser()
		user.Username, user.Email, user.Language = value, value+"@example.com", value
		site := Site{Name: value, Locale: value, Recaptcha: Recaptcha{SiteKey: value}}

		page, _ := render(t, s, PageApp, user, site)

		// Exactly the scripts the template has: two inline ones and the bundle.
		if n := strings.Count(page, "<script"); n != 3 {
			t.Errorf("value %q: %d script tags, want 3:\n%s", value, n, page)
		}
		if strings.Contains(page, "<script>alert") || strings.Contains(page, "<img src=x") {
			t.Errorf("value %q reached the page as markup:\n%s", value, page)
		}
		// U+2028 and U+2029 end a line in JavaScript but not in HTML, so only the script bodies must be free of them.
		for _, body := range regexp.MustCompile(`(?s)<script nonce="[^"]*">(.*?)</script>`).FindAllStringSubmatch(page, -1) {
			if strings.ContainsAny(body[1], "\u2028\u2029") {
				t.Errorf("value %q left a raw line separator inside a script", value)
			}
		}

		// The data must still arrive unchanged.
		var gotUser User
		global(t, page, "PterodactylUser", &gotUser)
		var gotSite Site
		global(t, page, "SiteConfiguration", &gotSite)
		if gotUser.Username != value || gotUser.Language != value || gotSite.Name != value || gotSite.Recaptcha.SiteKey != value {
			t.Errorf("value %q was changed on the way: %+v %+v", value, gotUser, gotSite)
		}

		title := regexp.MustCompile(`<title>(.*)</title>`).FindStringSubmatch(page)
		if title == nil || strings.ContainsAny(title[1], "<>") {
			t.Errorf("value %q is not escaped in the title: %v", value, title)
		}
	}
}

func TestEveryInlineScriptCarriesTheNonceOfThePolicy(t *testing.T) {
	s := newTestShell(t, ShellOptions{})
	page, header := render(t, s, PageApp, testUser(), Site{Name: "Panel", Locale: "en"})

	csp := header.Get("Content-Security-Policy")
	match := regexp.MustCompile(`'nonce-([0-9a-f]{32})'`).FindStringSubmatch(csp)
	if match == nil {
		t.Fatalf("no nonce in the policy: %s", csp)
	}

	inline := regexp.MustCompile(`<script nonce="([^"]*)">`).FindAllStringSubmatch(page, -1)
	if len(inline) != 2 {
		t.Fatalf("inline scripts = %d, want 2", len(inline))
	}
	for _, m := range inline {
		if m[1] != match[1] {
			t.Errorf("an inline script has nonce %q, the policy has %q", m[1], match[1])
		}
	}

	_, other := render(t, s, PageApp, testUser(), Site{Name: "Panel", Locale: "en"})
	if other.Get("Content-Security-Policy") == csp {
		t.Error("two pages share a nonce")
	}
}

func TestContentSecurityPolicy(t *testing.T) {
	s := newTestShell(t, ShellOptions{ConnectSources: []string{"https://node1.example.com:8080", "wss://node1.example.com:8080"}})

	csp := s.ContentSecurityPolicy("abc123")

	for _, want := range []string{
		"default-src 'none'",
		"script-src 'self' 'nonce-abc123'",
		"connect-src 'self' https://node1.example.com:8080 wss://node1.example.com:8080",
		"frame-ancestors 'none'",
		"base-uri 'none'",
		"object-src 'none'",
		"form-action 'self'",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("the policy lacks %q: %s", want, csp)
		}
	}
	for _, bad := range []string{"'unsafe-eval'", "script-src *", "'unsafe-inline'; connect"} {
		if strings.Contains(csp, bad) {
			t.Errorf("the policy must not contain %q: %s", bad, csp)
		}
	}
	if strings.Contains(strings.SplitN(csp, "style-src", 2)[0], "unsafe-inline") {
		t.Errorf("scripts must not allow inline code: %s", csp)
	}
}

func TestConnectSourcesAreValidated(t *testing.T) {
	for _, bad := range []string{"*", "https:", "javascript:alert(1)", "https://a.example.com; script-src *", "https://a b.example.com",
		"'self'", "ftp://example.com", "https://example.com/path", "http://", "https://exa mple.com", ""} {
		if _, err := NewShell(testManifest(), ShellOptions{ConnectSources: []string{bad}}); err == nil {
			t.Errorf("connect source %q was accepted", bad)
		}
	}
	for _, good := range []string{"https://node.example.com", "http://10.0.0.5:8080", "wss://node-1.example.com:443"} {
		if _, err := NewShell(testManifest(), ShellOptions{ConnectSources: []string{good}}); err != nil {
			t.Errorf("connect source %q was refused: %v", good, err)
		}
	}
}

func TestNewShellNeedsAMainEntry(t *testing.T) {
	if _, err := NewShell(Manifest{"auth.js": {Src: "/assets/a.js", Integrity: testIntegrity}}, ShellOptions{}); err == nil {
		t.Error("a manifest without main.js was accepted")
	}
}

func TestRenderRefusesAnUnknownPage(t *testing.T) {
	if err := newTestShell(t, ShellOptions{}).Render(httptest.NewRecorder(), Page("admin"), nil, Site{}); err == nil {
		t.Error("an unknown page was rendered")
	}
}

func TestLoadManifest(t *testing.T) {
	good := `{"main.js":{"src":"/assets/bundle.1.js","integrity":"` + testIntegrity + `"}}`
	if m, err := LoadManifest(strings.NewReader(good)); err != nil || m["main.js"].Src != "/assets/bundle.1.js" {
		t.Fatalf("LoadManifest(good) = %v, %v", m, err)
	}
	// Fonts and other non-script files have no integrity value in the real build.
	withFont := `{"f.woff2":{"src":"/assets/f.woff2"}}`
	if _, err := LoadManifest(strings.NewReader(withFont)); err != nil {
		t.Errorf("a file without integrity was refused: %v", err)
	}

	tests := []struct {
		name string
		json string
	}{
		{"not json", `nope`},
		{"src outside assets", `{"main.js":{"src":"/other/a.js","integrity":"` + testIntegrity + `"}}`},
		{"src with a quote", `{"main.js":{"src":"/assets/a\".js","integrity":"` + testIntegrity + `"}}`},
		{"src with traversal", `{"main.js":{"src":"/assets/../a.js","integrity":"` + testIntegrity + `"}}`},
		{"src with a nested path", `{"main.js":{"src":"/assets/x/a.js","integrity":"` + testIntegrity + `"}}`},
		{"src that is a script url", `{"main.js":{"src":"javascript:alert(1)","integrity":"` + testIntegrity + `"}}`},
		{"external src", `{"main.js":{"src":"https://evil.example.com/a.js","integrity":"` + testIntegrity + `"}}`},
		{"a script without integrity", `{"main.js":{"src":"/assets/a.js"}}`},
		{"a font with a bad integrity", `{"f.woff2":{"src":"/assets/f.woff2","integrity":"md5-abc"}}`},
		{"weaker hash", `{"main.js":{"src":"/assets/a.js","integrity":"sha1-abc="}}`},
		{"integrity with a quote", `{"main.js":{"src":"/assets/a.js","integrity":"sha384-abc\"onload=\"x"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := LoadManifest(strings.NewReader(tt.json)); err == nil {
				t.Error("LoadManifest accepted it")
			}
		})
	}
}

// The real build output, when the frontend has been built, must satisfy the same checks.
func TestTheBuiltManifestIsAccepted(t *testing.T) {
	path := filepath.Join("..", "..", "..", "web", "public", "assets", "manifest.json")
	f, err := os.Open(path)
	if err != nil {
		t.Skip("the frontend has not been built (web/public/assets/manifest.json)")
	}
	defer f.Close()

	m, err := LoadManifest(f)
	if err != nil {
		t.Fatalf("the built manifest is refused: %v", err)
	}
	if _, err := NewShell(m, ShellOptions{}); err != nil {
		t.Errorf("NewShell with the built manifest: %v", err)
	}
}

func TestAssetsHandler(t *testing.T) {
	fsys := fstest.MapFS{
		"bundle.35150288.js": {Data: []byte("console.log(1)")},
		"manifest.json":      {Data: []byte("{}")},
		"b33a81b7.woff2":     {Data: []byte("font")},
		".gitkeep":           {Data: []byte("")},
		"images/logo.png":    {Data: []byte("png")},
		"secret.txt":         {Data: []byte("not an asset")},
	}
	h := AssetsHandler(fsys)

	tests := []struct {
		name       string
		method     string
		target     string
		wantStatus int
		wantCache  string
	}{
		{"hashed script", "GET", "/assets/bundle.35150288.js", 200, "public, max-age=31536000, immutable"},
		{"font", "GET", "/assets/b33a81b7.woff2", 200, "public, max-age=31536000, immutable"},
		{"manifest is not cached long", "GET", "/assets/manifest.json", 200, "no-cache"},
		{"head", "HEAD", "/assets/bundle.35150288.js", 200, ""},
		{"directory listing", "GET", "/assets/", 404, ""},
		{"directory without slash", "GET", "/assets", 404, ""},
		{"subfolder", "GET", "/assets/images/logo.png", 404, ""},
		{"subfolder listing", "GET", "/assets/images/", 404, ""},
		{"dot file", "GET", "/assets/.gitkeep", 404, ""},
		{"traversal", "GET", "/assets/../secret.txt", 404, ""},
		{"encoded traversal", "GET", "/assets/%2e%2e%2fsecret.txt", 404, ""},
		{"missing", "GET", "/assets/nope.js", 404, ""},
		{"outside the mount", "GET", "/other/bundle.35150288.js", 404, ""},
		{"post", "POST", "/assets/bundle.35150288.js", 405, ""},
		{"index redirect", "GET", "/assets/index.html", 404, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.target, nil))

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantCache != "" && rec.Header().Get("Cache-Control") != tt.wantCache {
				t.Errorf("Cache-Control = %q, want %q", rec.Header().Get("Cache-Control"), tt.wantCache)
			}
			if strings.Contains(rec.Body.String(), "not an asset") {
				t.Error("a file outside the allowed names was served")
			}
		})
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/assets/bundle.35150288.js", nil))
	if rec.Body.String() != "console.log(1)" || !strings.Contains(rec.Header().Get("Content-Type"), "javascript") {
		t.Errorf("body %q, content type %q", rec.Body.String(), rec.Header().Get("Content-Type"))
	}
}

func TestNodeSources(t *testing.T) {
	for name, tc := range map[string]struct {
		scheme, fqdn string
		port         int
		want         []string
	}{
		"https node":   {"https", "n1.example.com", 8080, []string{"https://n1.example.com:8080", "wss://n1.example.com:8080"}},
		"http node":    {"http", "10.0.0.5", 8080, []string{"http://10.0.0.5:8080", "ws://10.0.0.5:8080"}},
		"other scheme": {"ftp", "n1.example.com", 8080, nil},
		"empty scheme": {"", "n1.example.com", 8080, nil},
		"port zero":    {"https", "n1.example.com", 0, nil},
		"port high":    {"https", "n1.example.com", 65536, nil},
		"a directive":  {"https", "n1.example.com;script-src", 8080, nil},
		"a space":      {"https", "a b", 8080, nil},
		"a star":       {"https", "*", 8080, nil},
		"empty host":   {"https", "", 8080, nil},
		"a path":       {"https", "n1.example.com/x", 8080, nil},
	} {
		if got := NodeSources(tc.scheme, tc.fqdn, tc.port); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
}

func TestContentSecurityPolicyWithMoreOrigins(t *testing.T) {
	s := newTestShell(t, ShellOptions{ConnectSources: []string{"https://fixed.example.com:8080"}})
	connect := func(csp string) string {
		for _, d := range strings.Split(csp, "; ") {
			if strings.HasPrefix(d, "connect-src ") {
				return strings.TrimPrefix(d, "connect-src ")
			}
		}
		t.Fatalf("no connect-src in %q", csp)
		return ""
	}
	if got := connect(s.ContentSecurityPolicy("n")); got != "'self' https://fixed.example.com:8080" {
		t.Errorf("without more: %q", got)
	}
	got := connect(s.ContentSecurityPolicyWith("n", []string{
		"https://n1.example.com:8080", "wss://n1.example.com:8080",
		"https://n1.example.com:8080",            // twice
		"https://fixed.example.com:8080",         // already there
		"https://evil.example.com; script-src *", // a directive
		"*", "'unsafe-inline'", "data:", "http://a b:1", "'self'",
	}))
	if got != "'self' https://fixed.example.com:8080 https://n1.example.com:8080 wss://n1.example.com:8080" {
		t.Errorf("with more: %q", got)
	}

	// The rest of the policy does not move, and the other directives are not given the origins.
	csp := s.ContentSecurityPolicyWith("abc", []string{"https://n1.example.com:8080"})
	for _, want := range []string{"default-src 'none'", "script-src 'self' 'nonce-abc'", "frame-ancestors 'none'", "object-src 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("the policy lacks %q: %s", want, csp)
		}
	}
	if strings.Count(csp, "n1.example.com") != 1 {
		t.Errorf("the origin is in more than one directive: %s", csp)
	}
}

func TestContentSecurityPolicyWithIsCapped(t *testing.T) {
	s := newTestShell(t, ShellOptions{})
	var many []string
	for i := 0; i < MaxExtraSources+50; i++ {
		many = append(many, fmt.Sprintf("https://n%d.example.com:8080", i))
	}
	csp := s.ContentSecurityPolicyWith("n", many)
	if got := strings.Count(csp, "https://n"); got != MaxExtraSources {
		t.Errorf("%d origins were allowed, want %d", got, MaxExtraSources)
	}
	// Invalid ones do not use up the allowance.
	mixed := append([]string{"bad one", "bad two"}, many[:MaxExtraSources]...)
	if got := strings.Count(s.ContentSecurityPolicyWith("n", mixed), "https://n"); got != MaxExtraSources {
		t.Errorf("with invalid ones first: %d", got)
	}
}

func TestRenderWithPutsTheOriginsInTheHeaderOnly(t *testing.T) {
	s := newTestShell(t, ShellOptions{})
	rec := httptest.NewRecorder()
	if err := s.RenderWith(rec, PageApp, &User{Username: "ada"}, Site{Name: "x"}, []string{"wss://n1.example.com:8080"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "wss://n1.example.com:8080") {
		t.Errorf("csp = %s", rec.Header().Get("Content-Security-Policy"))
	}
	if strings.Contains(rec.Body.String(), "n1.example.com") {
		t.Error("the origins are in the page")
	}
	// Render is RenderWith without any.
	rec = httptest.NewRecorder()
	if err := s.Render(rec, PageApp, nil, Site{Name: "x"}); err != nil || strings.Contains(rec.Header().Get("Content-Security-Policy"), "n1.example.com") {
		t.Errorf("err = %v, csp = %s", err, rec.Header().Get("Content-Security-Policy"))
	}
}

func TestShellReloadServesTheNewMainScript(t *testing.T) {
	first := Manifest{"main.js": {Src: "/assets/bundle.one.js", Integrity: "sha384-one"}}
	s, err := NewShell(first, ShellOptions{})
	if err != nil {
		t.Fatal(err)
	}
	page := func() string {
		rec := httptest.NewRecorder()
		if err := s.Render(rec, PageAuth, nil, Site{Name: "Raptor"}); err != nil {
			t.Fatal(err)
		}
		return rec.Body.String()
	}
	if got := page(); !strings.Contains(got, `src="/assets/bundle.one.js"`) || !strings.Contains(got, `integrity="sha384-one"`) {
		t.Fatalf("before: %s", got)
	}
	if err := s.Reload(Manifest{"main.js": {Src: "/assets/bundle.two.js", Integrity: "sha384-two"}}); err != nil {
		t.Fatal(err)
	}
	if got := page(); !strings.Contains(got, `src="/assets/bundle.two.js"`) || !strings.Contains(got, `integrity="sha384-two"`) || strings.Contains(got, "bundle.one") {
		t.Errorf("after: %s", got)
	}

	// A manifest without a main script is refused and the shell keeps the build it had.
	if err := s.Reload(Manifest{"font.woff": {Src: "/assets/font.woff2"}}); err == nil {
		t.Error("a manifest without main.js was taken")
	}
	if got := page(); !strings.Contains(got, "bundle.two") {
		t.Errorf("after a refusal: %s", got)
	}
}

// Pages are drawn while a new build is taken: with the race detector this is where a missing lock shows.
func TestShellReloadWhilePagesAreDrawn(t *testing.T) {
	s, err := NewShell(Manifest{"main.js": {Src: "/assets/bundle.0.js", Integrity: "sha384-0"}}, ShellOptions{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 1; i <= 200; i++ {
			_ = s.Reload(Manifest{"main.js": {Src: fmt.Sprintf("/assets/bundle.%d.js", i), Integrity: fmt.Sprintf("sha384-%d", i)}})
		}
	}()
	for i := 0; i < 200; i++ {
		rec := httptest.NewRecorder()
		if err := s.Render(rec, PageAuth, nil, Site{Name: "Raptor"}); err != nil {
			t.Fatal(err)
		}
		// The script and its integrity value are of the same build, never of two.
		body := rec.Body.String()
		var n, m int
		if _, err := fmt.Sscanf(body[strings.Index(body, "bundle.")+len("bundle."):], "%d.js", &n); err != nil {
			t.Fatalf("no script in the page: %v", err)
		}
		if _, err := fmt.Sscanf(body[strings.Index(body, `integrity="sha384-`)+len(`integrity="sha384-`):], "%d", &m); err != nil || n != m {
			t.Fatalf("the script is of build %d and the integrity value of %d: %v", n, m, err)
		}
	}
	<-done
}
