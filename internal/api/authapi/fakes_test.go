package authapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/auth/csrf"
	"github.com/Helles01/raptor-panel/internal/auth/passwordreset"
	"github.com/Helles01/raptor-panel/internal/auth/session"
	"github.com/Helles01/raptor-panel/internal/mail"
)

var t0 = time.Date(2026, 10, 23, 12, 0, 20, 0, time.UTC) // 20 seconds into a step, so that a step start differs from now

const (
	adaPassword = "a long enough password"
	totpSecret  = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"
)

var adaHash = func() string {
	h, err := auth.HashPassword(adaPassword)
	if err != nil {
		panic(err)
	}
	return h
}()

// fakeUsers is an in-memory user store.
type fakeUsers struct {
	mu        sync.Mutex
	users     map[uint64]*User
	lookups   int
	stepTaken bool // a hook: the next UseTOTPStep reports that another request was first
	passwords map[uint64]string
	failFind  error
}

func newUsers(us ...User) *fakeUsers {
	f := &fakeUsers{users: map[uint64]*User{}, passwords: map[uint64]string{}}
	for i := range us {
		u := us[i]
		f.users[u.ID] = &u
	}
	return f
}

func (f *fakeUsers) find(match func(User) bool) (User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups++
	if f.failFind != nil {
		return User{}, f.failFind
	}
	for _, u := range f.users {
		if match(*u) {
			return *u, nil
		}
	}
	return User{}, ErrNoUser
}

func (f *fakeUsers) ByLogin(_ context.Context, fld, value string) (User, error) {
	return f.find(func(u User) bool {
		if fld == "email" {
			return strings.EqualFold(u.Email, value)
		}
		return strings.EqualFold(u.Username, value)
	})
}
func (f *fakeUsers) ByID(_ context.Context, id uint64) (User, error) {
	return f.find(func(u User) bool { return u.ID == id })
}
func (f *fakeUsers) ByEmail(_ context.Context, email string) (User, error) {
	return f.find(func(u User) bool { return strings.EqualFold(u.Email, email) })
}

func (f *fakeUsers) UseTOTPStep(_ context.Context, id uint64, step time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := f.users[id]
	if f.stepTaken || (u.TOTPLastUsed != nil && !step.After(*u.TOTPLastUsed)) {
		return false, nil
	}
	u.TOTPLastUsed = &step
	return true, nil
}

func (f *fakeUsers) SetPassword(_ context.Context, id uint64, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[id].PasswordHash = hash
	f.passwords[id] = hash
	return nil
}

func (f *fakeUsers) get(id uint64) User {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *f.users[id]
}

type fakeRecovery struct {
	mu       sync.Mutex
	tokens   map[uint64][]RecoveryToken
	raceLost bool
	deleted  []uint64
}

func (f *fakeRecovery) ForUser(_ context.Context, id uint64) ([]RecoveryToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]RecoveryToken(nil), f.tokens[id]...), nil
}

func (f *fakeRecovery) Delete(_ context.Context, tokenID uint64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.raceLost {
		return false, nil
	}
	for uid, list := range f.tokens {
		for i, t := range list {
			if t.ID == tokenID {
				f.tokens[uid] = append(list[:i:i], list[i+1:]...)
				f.deleted = append(f.deleted, tokenID)
				return true, nil
			}
		}
	}
	return false, nil
}

type fakeResets struct {
	mu          sync.Mutex
	records     map[string]passwordreset.Record
	consumeLost bool
	replaced    int
	latestErr   error
}

func (f *fakeResets) Latest(_ context.Context, email string) (passwordreset.Record, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.latestErr != nil {
		return passwordreset.Record{}, false, f.latestErr
	}
	r, ok := f.records[strings.ToLower(email)]
	return r, ok, nil
}

func (f *fakeResets) Replace(_ context.Context, email string, r passwordreset.Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records[strings.ToLower(email)] = r
	f.replaced++
	return nil
}

func (f *fakeResets) Consume(_ context.Context, email string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.consumeLost {
		return false, nil
	}
	_, ok := f.records[strings.ToLower(email)]
	delete(f.records, strings.ToLower(email))
	return ok, nil
}

type fakeOutbox struct {
	mu   sync.Mutex
	sent []mail.Message
}

func (f *fakeOutbox) Send(_ context.Context, m mail.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	return nil
}

type fakeNodes struct {
	mu    sync.Mutex
	users []uint64
	err   error
}

func (f *fakeNodes) DeauthorizeUser(_ context.Context, id uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users = append(f.users, id)
	return f.err
}

type fakeLog struct {
	mu      sync.Mutex
	entries []LogEntry
}

func (f *fakeLog) Log(_ context.Context, e LogEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, e)
	return nil
}

func (f *fakeLog) events() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.entries {
		out = append(out, e.Event)
	}
	return out
}

// testAuthorizer mirrors what the production authorizer does for these routes: a guest route is closed to a signed-in session, and
// a route for the signed-in needs one.
type testAuthorizer struct{}

func (testAuthorizer) Authorize(c echo.Context, p api.Permission) error {
	s := middleware.CurrentSession(c)
	signedIn := s != nil && s.UserID != 0
	switch p.Kind {
	case api.KindGuest:
		if signedIn {
			return apierr.Forbidden("You are already signed in.")
		}
	case api.KindAuthenticated:
		if !signedIn {
			return apierr.Unauthenticated()
		}
	}
	return nil
}

// rig is a running server with its fakes and a browser that keeps cookies.
type rig struct {
	t        *testing.T
	ts       *httptest.Server
	client   *http.Client
	users    *fakeUsers
	recovery *fakeRecovery
	resets   *fakeResets
	outbox   *fakeOutbox
	nodes    *fakeNodes
	log      *fakeLog
	store    *session.MemoryStore
	sessions *session.Manager
	logs     *bytes.Buffer
	deps     Deps

	clk *testClock
	// ip is sent as the client address (X-Forwarded-For), so that a test can be several clients.
	ip string
}

func ada() User {
	return User{ID: 1, UUID: "11111111-2222-4333-8444-555555555555", Username: "ada", Email: "ada@example.com", Language: "en", PasswordHash: adaHash,
		CreatedAt: t0.Add(-24 * time.Hour), UpdatedAt: t0.Add(-time.Hour)}
}

func grace() User {
	u := ada()
	u.ID, u.Username, u.Email, u.UUID = 2, "grace", "grace@example.com", "22222222-3333-4444-8555-666666666666"
	u.UsesTOTP, u.TOTPSecret = true, totpSecret
	return u
}

func newRig(t *testing.T, users ...User) *rig {
	t.Helper()
	if len(users) == 0 {
		users = []User{ada(), grace()}
	}
	r := &rig{
		t: t, users: newUsers(users...), recovery: &fakeRecovery{tokens: map[uint64][]RecoveryToken{}},
		resets: &fakeResets{records: map[string]passwordreset.Record{}}, outbox: &fakeOutbox{}, nodes: &fakeNodes{}, log: &fakeLog{},
		store: session.NewMemoryStore(), logs: &bytes.Buffer{}, clk: &testClock{now: t0}, ip: "203.0.113.9",
	}

	var err error
	r.sessions, err = session.NewManager(r.store, session.Options{IdleTimeout: time.Hour, MaxLifetime: 8 * time.Hour, Secure: false})
	if err != nil {
		t.Fatal(err)
	}
	prot, err := csrf.New(bytes.Repeat([]byte("k"), 32))
	if err != nil {
		t.Fatal(err)
	}
	limiter := func(n int, d time.Duration) *middleware.Limiter {
		l, err := middleware.NewLimiter(n, d)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	site, err := mail.NewSite("Test Panel", "https://panel.example.com")
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(r.logs, nil))
	r.deps = Deps{
		Users: r.users, Recovery: r.recovery, Resets: r.resets, Outbox: r.outbox, Nodes: r.nodes, Log: r.log,
		Sessions: r.sessions, CSRF: prot, Failures: middleware.NewTracker(),
		LoginLimiter: limiter(10, time.Minute), RequestLimiter: limiter(passwordreset.RequestPerIPPerMinute, time.Minute),
		ResetLimiter: limiter(passwordreset.ResetPerIPPerMinute, time.Minute), EmailLimiter: limiter(passwordreset.RequestPerEmailPerHour, time.Hour),
		Site: site, Logger: logger, Random: rand.Reader,
		Now: r.clk.get,
	}
	r.start()
	return r
}

// start builds the handlers from r.deps and serves them.
func (r *rig) start() {
	r.t.Helper()
	h, err := New(r.deps)
	if err != nil {
		r.t.Fatalf("New: %v", err)
	}
	srv, err := api.New(api.Options{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Trust:      middleware.NewProxyTrust([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, slog.New(slog.NewTextHandler(io.Discard, nil))),
		Authorizer: testAuthorizer{},
	})
	if err != nil {
		r.t.Fatal(err)
	}
	g := srv.Router.Sub("", middleware.LoadSession(r.sessions, false), middleware.CSRF(middleware.CSRFOptions{Protector: r.deps.CSRF}))
	h.Register(g)
	g.GET("/whoami", func(c echo.Context) error {
		s := middleware.CurrentSession(c)
		return c.JSON(200, map[string]uint64{"user": s.UserID})
	}, api.Authenticated())

	handler, err := srv.Handler()
	if err != nil {
		r.t.Fatalf("Handler: %v", err)
	}
	r.ts = httptest.NewServer(handler)
	r.t.Cleanup(r.ts.Close)
	jar, _ := cookiejar.New(nil)
	r.client = &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (r *rig) advance(d time.Duration) { r.clk.advance(d) }

// testClock is the time the handlers see.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) get() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// restart serves the handlers again with the dependencies in r.deps, for a test that changed one. The browser starts over.
func (r *rig) restart() {
	r.t.Helper()
	r.ts.Close()
	r.start()
}

// roomy lets a test send many requests: the limit of the endpoints by address is raised, and the browser starts over.
func (r *rig) roomy() {
	r.t.Helper()
	big := func() *middleware.Limiter { l, _ := middleware.NewLimiter(100000, time.Minute); return l }
	r.deps.LoginLimiter, r.deps.RequestLimiter, r.deps.ResetLimiter = big(), big(), big()
	r.restart()
	r.prime()
}

// otherBrowser is a second browser, with its own cookies, talking to the same server.
func (r *rig) otherBrowser() *rig {
	cp := *r
	jar, _ := cookiejar.New(nil)
	cp.client = &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &cp
}

// cookie returns a cookie of the browser.
func (r *rig) cookie(name string) string {
	u, _ := url.Parse(r.ts.URL)
	for _, c := range r.client.Jar.Cookies(u) {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

// reply is a response with its body read.
type reply struct {
	Status int
	Header http.Header
	Body   []byte
}

func (rp reply) json() map[string]any {
	var m map[string]any
	_ = json.Unmarshal(rp.Body, &m)
	return m
}

// detail is the first error's text.
func (rp reply) detail() string {
	errs, _ := rp.json()["errors"].([]any)
	if len(errs) == 0 {
		return ""
	}
	first, _ := errs[0].(map[string]any)
	d, _ := first["detail"].(string)
	return d
}

// do sends a request the way the frontend's HTTP client does: JSON, and the token of the XSRF-TOKEN cookie in X-XSRF-TOKEN.
func (r *rig) do(method, path string, body any, headers ...string) reply {
	r.t.Helper()
	var rd io.Reader
	if body != nil {
		var raw []byte
		switch b := body.(type) {
		case string:
			raw = []byte(b)
		default:
			var err error
			if raw, err = json.Marshal(b); err != nil {
				r.t.Fatal(err)
			}
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, r.ts.URL+path, rd)
	if err != nil {
		r.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "TestBrowser/1.0")
	req.Header.Set("X-Forwarded-For", r.ip)
	if tok := r.cookie(middleware.CSRFCookieName); tok != "" {
		req.Header.Set(middleware.CSRFHeaderName, tok)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "-" {
			req.Header.Del(headers[i])
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	resp, err := r.client.Do(req)
	if err != nil {
		r.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return reply{Status: resp.StatusCode, Header: resp.Header, Body: data}
}

// prime asks for the CSRF cookie, as the sign-in page does first.
func (r *rig) prime() {
	r.t.Helper()
	if rp := r.do(http.MethodGet, "/sanctum/csrf-cookie", nil); rp.Status != http.StatusNoContent {
		r.t.Fatalf("csrf-cookie = %d %s", rp.Status, rp.Body)
	}
}

func (r *rig) login(user, password string) reply {
	r.t.Helper()
	return r.do(http.MethodPost, "/auth/login", map[string]string{"user": user, "password": password})
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}
