package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// ManifestEntry is one file of the frontend build, as written by webpack-assets-manifest.
type ManifestEntry struct {
	Src       string `json:"src"`
	Integrity string `json:"integrity"`
}

// Manifest maps an entry name such as "main.js" to its hashed file name and its SHA-384 subresource integrity value.
type Manifest map[string]ManifestEntry

var (
	srcPattern       = regexp.MustCompile(`^/assets/[A-Za-z0-9._-]+$`)
	integrityPattern = regexp.MustCompile(`^sha384-[A-Za-z0-9+/]+={0,2}$`)
	sourcePattern    = regexp.MustCompile(`^(?:'self'|https?://[A-Za-z0-9.\-]+(?::[0-9]{1,5})?|wss?://[A-Za-z0-9.\-]+(?::[0-9]{1,5})?)$`)
)

// LoadManifest reads assets/manifest.json of the frontend build and checks every entry: the file must live under
// /assets/ with a plain name, and a script must carry a SHA-384 integrity value (other files, such as fonts, have none).
// Both end up in the page, so nothing else is accepted.
func LoadManifest(r io.Reader) (Manifest, error) {
	var m Manifest
	if err := json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(&m); err != nil {
		return nil, fmt.Errorf("read asset manifest: %w", err)
	}
	for name, e := range m {
		if !srcPattern.MatchString(e.Src) {
			return nil, fmt.Errorf("asset manifest: %q has the file name %q, which is not a plain name under /assets/", name, e.Src)
		}
		needsIntegrity := strings.HasSuffix(name, ".js")
		if (needsIntegrity || e.Integrity != "") && !integrityPattern.MatchString(e.Integrity) {
			return nil, fmt.Errorf("asset manifest: %q has no valid sha384 integrity value", name)
		}
	}
	return m, nil
}

// User is what the frontend learns about the logged-in user: window.PterodactylUser. It has no id and no secrets.
type User struct {
	UUID      string    `json:"uuid"`
	Username  string    `json:"username"`
	Email     string    `json:"email"`
	RootAdmin bool      `json:"root_admin"`
	UseTotp   bool      `json:"use_totp"`
	Language  string    `json:"language"`
	UpdatedAt time.Time `json:"updated_at"`
	CreatedAt time.Time `json:"created_at"`
}

// Site is window.SiteConfiguration.
type Site struct {
	Name      string    `json:"name"`
	Locale    string    `json:"locale"`
	Recaptcha Recaptcha `json:"recaptcha"`
}

// Recaptcha is the reCAPTCHA part of Site. Raptor does not implement reCAPTCHA, so it is always disabled.
type Recaptcha struct {
	Enabled bool   `json:"enabled"`
	SiteKey string `json:"siteKey"`
}

// Page selects the shell: the logged-in application or the login pages, which differ in the background.
type Page string

const (
	PageApp  Page = "app"
	PageAuth Page = "auth"
)

var bodyClasses = map[Page]string{PageApp: "bg-neutral-800", PageAuth: "bg-neutral-900"}

// ShellOptions configures the page shell.
type ShellOptions struct {
	// ConnectSources are the extra origins the page may open connections to, for example the Wings nodes the console
	// and file transfers talk to ("https://node1.example.com:8080", "wss://node1.example.com:8080").
	ConnectSources []string
}

// Shell renders the HTML page that starts the React frontend.
type Shell struct {
	tmpl    *template.Template
	main    ManifestEntry
	connect string
}

// The structure follows the reference's templates/wrapper.blade.php. The user and site data are given to the template
// as values, never as text, so html/template escapes them for the JavaScript context: "</script>", quotes and the line
// separators U+2028 and U+2029 cannot end the script or the string.
const shellTemplate = `<!DOCTYPE html>
<html>
<head>
<title>{{.Site.Name}}</title>
<meta charset="utf-8">
<meta http-equiv="X-UA-Compatible" content="IE=edge">
<meta content="width=device-width, initial-scale=1, maximum-scale=1, user-scalable=no" name="viewport">
<meta name="robots" content="noindex">
<meta name="theme-color" content="#0e4688">
{{if .User}}<script nonce="{{.Nonce}}">window.PterodactylUser = {{.User}};</script>
{{end}}<script nonce="{{.Nonce}}">window.SiteConfiguration = {{.Site}};</script>
</head>
<body class="{{.BodyClass}}">
{{if .Modal}}<div id="modal-portal"></div>
{{end}}<div id="app"></div>
<script src="{{.Main.Src}}" integrity="{{.Main.Integrity}}" crossorigin="anonymous"></script>
</body>
</html>
`

// NewShell returns a Shell for a frontend build. The manifest must have a "main.js" entry.
func NewShell(m Manifest, opts ShellOptions) (*Shell, error) {
	main, ok := m["main.js"]
	if !ok {
		return nil, errors.New("the asset manifest has no main.js entry")
	}

	sources := []string{"'self'"}
	for _, s := range opts.ConnectSources {
		if !sourcePattern.MatchString(s) || s == "'self'" {
			return nil, fmt.Errorf("%q is not an allowed connect source: use an http(s):// or ws(s):// origin", s)
		}
		sources = append(sources, s)
	}

	tmpl, err := template.New("shell").Parse(shellTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse shell template: %w", err)
	}
	return &Shell{tmpl: tmpl, main: main, connect: strings.Join(sources, " ")}, nil
}

type shellData struct {
	Site      Site
	User      *User
	Nonce     string
	BodyClass string
	Modal     bool
	Main      ManifestEntry
}

// ContentSecurityPolicy returns the policy for a page with the given script nonce. Scripts run only if they come from
// this origin or carry the nonce; styles may be inline because the frontend's CSS-in-JS injects them.
func (s *Shell) ContentSecurityPolicy(nonce string) string {
	return strings.Join([]string{
		"default-src 'none'",
		"script-src 'self' 'nonce-" + nonce + "'",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data:",
		"font-src 'self' data:",
		"connect-src " + s.connect,
		"base-uri 'none'",
		"form-action 'self'",
		"frame-ancestors 'none'",
		"object-src 'none'",
	}, "; ")
}

// Render writes the page. user is nil for the login pages.
func (s *Shell) Render(w http.ResponseWriter, page Page, user *User, site Site) error {
	class, ok := bodyClasses[page]
	if !ok {
		return fmt.Errorf("unknown page %q", page)
	}

	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return fmt.Errorf("generate nonce: %w", err)
	}
	nonce := hex.EncodeToString(nonceBytes)

	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	// The page carries account data, so it is never cached or shared.
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", s.ContentSecurityPolicy(nonce))

	return s.tmpl.Execute(w, shellData{Site: site, User: user, Nonce: nonce, BodyClass: class, Modal: page == PageApp, Main: s.main})
}
