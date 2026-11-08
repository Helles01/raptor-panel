//go:build integration

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/Helles01/raptor-panel/internal/auth/totp"
	"github.com/Helles01/raptor-panel/internal/db"
	"github.com/Helles01/raptor-panel/internal/secrets"
)

// lockedBuffer is a log that the panel writes while the test reads it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// client is a browser that keeps cookies.
type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func newClient(t *testing.T, base string) *client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &client{t: t, base: base, http: &http.Client{
		Jar: jar, Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// do sends a request and returns the status, the headers and the body. A body that is not nil is sent as JSON.
func (c *client) do(method, path string, body any, header map[string]string) (int, http.Header, []byte) {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, r)
	if err != nil {
		c.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, out
}

// xsrf is the token the browser would send back in the header of a request that changes something.
func (c *client) xsrf() map[string]string {
	u, _ := url.Parse(c.base)
	for _, ck := range c.http.Jar.Cookies(u) {
		if ck.Name == "XSRF-TOKEN" {
			return map[string]string{"X-XSRF-TOKEN": ck.Value}
		}
	}
	return nil
}

// json sends a request as the signed-in user and decodes the answer.
func (c *client) json(method, path string, body any, wantStatus int) map[string]any {
	c.t.Helper()
	status, _, out := c.do(method, path, body, c.xsrf())
	if status != wantStatus {
		c.t.Fatalf("%s %s = %d, want %d\n%s", method, path, status, wantStatus, out)
	}
	var m map[string]any
	if len(out) > 0 {
		_ = json.Unmarshal(out, &m)
	}
	return m
}

func attr(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		next, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = next[k]
	}
	return cur
}

// The whole panel over the test database, from the first installation to the page of a server: what an administrator types, what a browser does and
// what a script with an API key does, against the router that app.Build makes. No node is behind it, so what needs one is not here.
func TestPanelEndToEnd(t *testing.T) {
	dsn := os.Getenv("RAPTOR_TEST_DSN")
	if dsn == "" {
		t.Skip("RAPTOR_TEST_DSN is not set")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	host, port, _ := strings.Cut(cfg.Addr, ":")
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	assets, err := filepath.Abs("../../web/public/assets")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(assets, "manifest.json")); err != nil {
		t.Skip("the frontend is not built (web/public/assets)")
	}
	// The configuration does not allow port 0, so the test asks for a free port and lets go of it.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := probe.Addr().String()
	probe.Close()
	vars := map[string]string{
		"RAPTOR_DB_HOST": host, "RAPTOR_DB_PORT": port, "RAPTOR_DB_NAME": cfg.DBName, "RAPTOR_DB_USER": cfg.User, "RAPTOR_DB_PASSWORD": cfg.Passwd,
		"RAPTOR_SECRET_KEY": key, "RAPTOR_APP_URL": "http://127.0.0.1", "RAPTOR_HTTP_LISTEN": listen, "RAPTOR_ASSETS_DIR": assets, "RAPTOR_LOG_FORMAT": "text",
	}

	ctx := context.Background()
	sqlDB, err := sql.Open("mysql", dsn+"&loc=UTC")
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0")
	for _, table := range []string{"users", "api_keys", "locations", "nodes", "nests", "eggs", "egg_variables", "settings", "database_hosts", "databases", "raptor_secrets", "recovery_tokens",
		"servers", "subusers", "allocations", "sessions", "password_resets", "activity_logs", "activity_log_subjects", "raptor_outbox", "backups", "schedules", "tasks", "tasks_log",
		"server_variables", "mounts", "mount_server", "mount_node", "egg_mount", "user_ssh_keys", "server_transfers"} {
		if _, err := conn.ExecContext(ctx, "TRUNCATE TABLE `"+table+"`"); err != nil {
			t.Fatalf("truncate %s: %v", table, err)
		}
	}
	conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 1")
	conn.Close()

	// A database that has not been migrated is not served; one that has, is.
	if _, err := db.Migrate(ctx, sqlDB); err != nil {
		t.Fatal(err)
	}

	do := func(stdin string, args ...string) string {
		t.Helper()
		code, out, errOut := runCmd(t, stdin, vars, args...)
		if code != 0 {
			t.Fatalf("raptor %s = %d\n%s", strings.Join(args, " "), code, errOut)
		}
		return out
	}

	// The first installation, as the administrator does it.
	password := strings.TrimSpace(do("", "user", "create", "--email", "admin@example.com", "--username", "admin", "--first-name", "Ada", "--last-name", "Admin", "--admin"))
	apiKey := strings.TrimSpace(do("", "apikey", "create", "--user", "admin", "--application", "--description", "bootstrap",
		"--access", "servers=rw", "--access", "nodes=rw", "--access", "users=rw", "--access", "locations=rw", "--access", "allocations=rw", "--access", "nests=rw", "--access", "eggs=rw"))
	do("", "location", "create", "--short", "eu.ams", "--long", "Amsterdam")
	do("", "node", "create", "--name", "Node 1", "--location-id", "1", "--fqdn", "node1.example.com", "--memory", "4096", "--disk", "50000")
	do("", "nest", "create", "--name", "Minecraft", "--author", "p@example.com")
	do(e2eEgg, "egg", "import", "--nest-id", "1", "-")
	do("", "settings", "set", "app:name", "Test Panel")

	// A server, as the Application API would make it once a node had accepted it: one row, with its allocation.
	const serverUUID = "11111111-1111-4111-8111-111111111111"
	sqlDB.Exec("INSERT INTO allocations (node_id, ip, port) VALUES (1, '10.0.0.1', 25565), (1, '10.0.0.1', 25566)")
	sqlDB.Exec("INSERT INTO servers (id, uuid, uuidShort, node_id, name, description, owner_id, memory, swap, disk, io, cpu, allocation_id, nest_id, egg_id, startup, image, backup_limit, database_limit, allocation_limit) "+
		"VALUES (1, ?, '11111111', 1, 'Survival', 'a server', 1, 1024, 0, 5000, 500, 100, 1, 1, 1, 'java -jar {{JAR}}', 'ghcr.io/example/yolks:java_21', 3, 2, 3)", serverUUID)
	sqlDB.Exec("UPDATE allocations SET server_id = 1 WHERE id = 1")

	// A panel that is asked to start on an empty database says what to do, and a configuration that is wrong stops it before it listens.
	code, _, errOut := runCmd(t, "", map[string]string{"RAPTOR_DB_HOST": host, "RAPTOR_DB_PORT": port, "RAPTOR_DB_NAME": cfg.DBName, "RAPTOR_DB_USER": cfg.User, "RAPTOR_DB_PASSWORD": cfg.Passwd,
		"RAPTOR_SECRET_KEY": key, "RAPTOR_APP_URL": "http://127.0.0.1", "RAPTOR_HTTP_LISTEN": listen, "RAPTOR_ASSETS_DIR": filepath.Join(t.TempDir(), "nothing")}, "serve")
	if code == 0 || !strings.Contains(errOut, "frontend build") {
		t.Errorf("serve without a frontend: %d %q", code, errOut)
	}

	// Start the panel.
	logs := &lockedBuffer{}
	srvCtx, stop := context.WithCancel(ctx)
	ready, done := make(chan net.Addr, 1), make(chan error, 1)
	e := &env{stdin: strings.NewReader(""), stdout: io.Discard, stderr: logs, getenv: func(k string) string { return vars[k] }}
	go func() { done <- serve(srvCtx, e, "", func(a net.Addr) { ready <- a }) }()
	var base string
	select {
	case addr := <-ready:
		base = "http://" + addr.String()
	case err := <-done:
		t.Fatalf("the panel did not start: %v\n%s", err, logs)
	case <-time.After(30 * time.Second):
		t.Fatalf("the panel did not start in time\n%s", logs)
	}
	stopped := false
	shutdown := func() {
		if stopped {
			return
		}
		stopped = true
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("the panel stopped with an error: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("the panel did not stop in time")
		}
	}
	defer shutdown()

	// The open routes.
	guest := newClient(t, base)
	if status, _, out := guest.do("GET", "/healthz", nil, nil); status != 200 || !strings.Contains(string(out), "ok") {
		t.Errorf("/healthz = %d %s", status, out)
	}
	if status, _, out := guest.do("GET", "/readyz", nil, nil); status != 200 || !strings.Contains(string(out), "database") {
		t.Errorf("/readyz = %d %s", status, out)
	}
	if status, h, _ := guest.do("GET", "/", nil, nil); status != 302 || !strings.Contains(h.Get("Location"), "/auth/login") {
		t.Errorf("/ for a guest = %d, %q", status, h.Get("Location"))
	}
	status, h, page := guest.do("GET", "/auth/login", nil, map[string]string{"Accept": "text/html"})
	if status != 200 || !strings.Contains(h.Get("Content-Security-Policy"), "script-src") || !strings.Contains(string(page), "Test Panel") || !strings.Contains(string(page), "/assets/") {
		t.Errorf("/auth/login = %d, csp %q\n%s", status, h.Get("Content-Security-Policy"), page)
	}
	var manifest map[string]struct{ Src string }
	raw, _ := os.ReadFile(filepath.Join(assets, "manifest.json"))
	json.Unmarshal(raw, &manifest)
	for _, entry := range manifest {
		if status, _, body := guest.do("GET", entry.Src, nil, nil); status != 200 || len(body) == 0 {
			t.Errorf("%s = %d", entry.Src, status)
		}
	}
	if status, _, _ := guest.do("GET", "/api/client", nil, nil); status != 401 {
		t.Errorf("the client API for a guest = %d", status)
	}
	if status, _, _ := guest.do("GET", "/api/application/nodes", nil, nil); status != 401 {
		t.Errorf("the Application API for a guest = %d", status)
	}

	// The Application API with the key the administrator made.
	bearer := map[string]string{"Authorization": "Bearer " + apiKey}
	script := newClient(t, base)
	status, _, out := script.do("GET", "/api/application/nodes", nil, bearer)
	if status != 200 || !strings.Contains(string(out), "node1.example.com") {
		t.Fatalf("application nodes = %d\n%s", status, out)
	}
	if status, _, out := script.do("GET", "/api/application/servers", nil, bearer); status != 200 || !strings.Contains(string(out), "Survival") {
		t.Errorf("application servers = %d\n%s", status, out)
	}
	if status, _, out := script.do("GET", "/api/application/servers/1?include=allocations,node,user", nil, bearer); status != 200 || !strings.Contains(string(out), "10.0.0.1") || !strings.Contains(string(out), "admin@example.com") {
		t.Errorf("application server = %d\n%s", status, out)
	}
	if status, _, out := script.do("POST", "/api/application/nodes/1/allocations", map[string]any{"ip": "10.0.0.1", "ports": []string{"26000-26002"}}, bearer); status != 204 {
		t.Errorf("add allocations = %d\n%s", status, out)
	}
	if status, _, out := script.do("GET", "/api/application/nodes/1/allocations", nil, bearer); status != 200 || !strings.Contains(string(out), "26002") {
		t.Errorf("list allocations = %d\n%s", status, out)
	}
	if status, _, out := script.do("POST", "/api/application/users", map[string]any{"email": "bob@example.com", "username": "bob", "first_name": "Bob", "last_name": "B", "password": "a-long-password-1"}, bearer); status != 201 {
		t.Errorf("create user = %d\n%s", status, out)
	}
	if status, _, _ := script.do("GET", "/api/application/nodes", nil, map[string]string{"Authorization": "Bearer ptla_notakeyatallxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}); status != 401 {
		t.Errorf("a key that does not exist = %d", status)
	}

	// The browser: the cookie, the token, the sign-in.
	b := newClient(t, base)
	if status, _, _ := b.do("GET", "/sanctum/csrf-cookie", nil, nil); status != 204 {
		t.Fatalf("csrf-cookie = %d", status)
	}
	if status, _, out := b.do("POST", "/auth/login", map[string]any{"user": "admin", "password": password}, nil); status != 403 {
		t.Errorf("a sign-in without the token = %d\n%s", status, out)
	}
	if status, _, out := b.do("POST", "/auth/login", map[string]any{"user": "admin", "password": "not-the-password"}, b.xsrf()); status != 400 {
		t.Errorf("a wrong password = %d\n%s", status, out)
	}
	login := b.json("POST", "/auth/login", map[string]any{"user": "admin", "password": password}, 200)
	if attr(login, "data", "complete") != true {
		t.Fatalf("login = %v", login)
	}
	if status, h, _ := b.do("GET", "/", nil, map[string]string{"Accept": "text/html"}); status != 200 {
		t.Errorf("/ for the signed-in = %d %q", status, h.Get("Location"))
	}

	// The client API: the list, the account, the page of a server.
	list := b.json("GET", "/api/client", nil, 200)
	if data, _ := list["data"].([]any); len(data) != 1 || attr(data[0].(map[string]any), "attributes", "identifier") != "11111111" {
		t.Fatalf("the server list = %v", list)
	}
	if acc := b.json("GET", "/api/client/account", nil, 200); attr(acc, "attributes", "email") != "admin@example.com" {
		t.Errorf("account = %v", acc)
	}
	view := b.json("GET", "/api/client/servers/11111111", nil, 200)
	if attr(view, "attributes", "name") != "Survival" || attr(view, "meta", "is_server_owner") != true {
		t.Errorf("server = %v", view)
	}
	b.json("GET", "/api/client/servers/11111111/network/allocations", nil, 200)
	b.json("GET", "/api/client/servers/11111111/startup", nil, 200)
	b.json("GET", "/api/client/servers/11111111/users", nil, 200)
	b.json("GET", "/api/client/servers/11111111/backups", nil, 200)
	b.json("GET", "/api/client/servers/11111111/databases", nil, 200)
	b.json("GET", "/api/client/servers/11111111/schedules", nil, 200)
	b.json("GET", "/api/client/servers/11111111/activity", nil, 200)
	b.json("GET", "/api/client/permissions", nil, 200)

	// Changes, with and without the token.
	if status, _, _ := b.do("POST", "/api/client/servers/11111111/settings/rename", map[string]any{"name": "Renamed"}, nil); status != 403 {
		t.Errorf("a change without the token = %d", status)
	}
	if status, _, out := b.do("POST", "/api/client/servers/11111111/settings/rename", map[string]any{"name": "Renamed"}, b.xsrf()); status != 204 {
		t.Errorf("rename = %d\n%s", status, out)
	}
	b.json("GET", "/api/client/servers/11111111", nil, 200)
	sched := b.json("POST", "/api/client/servers/11111111/schedules", map[string]any{
		"name": "Nightly", "minute": "0", "hour": "4", "day_of_month": "*", "month": "*", "day_of_week": "*", "is_active": true,
	}, 200)
	id, _ := attr(sched, "attributes", "id").(float64)
	if id == 0 {
		t.Fatalf("schedule = %v", sched)
	}
	task := b.json("POST", "/api/client/servers/11111111/schedules/"+itoa(id)+"/tasks", map[string]any{"action": "command", "payload": "say hi", "time_offset": 0}, 200)
	if attr(task, "attributes", "sequence_id") != float64(1) {
		t.Errorf("task = %v", task)
	}
	sched = b.json("GET", "/api/client/servers/11111111/schedules/"+itoa(id), nil, 200)
	if tasks, _ := attr(sched, "attributes", "relationships", "tasks", "data").([]any); len(tasks) != 1 {
		t.Errorf("schedule with its tasks = %v", sched)
	}
	b.json("POST", "/api/client/servers/11111111/users", map[string]any{"email": "bob@example.com", "permissions": []string{"control.console"}}, 200)
	if subs, _ := b.json("GET", "/api/client/servers/11111111/users", nil, 200)["data"].([]any); len(subs) != 1 {
		t.Errorf("subusers = %v", subs)
	}

	// A setting changed from the command line applies to the panel that is running, with no restart. The owners' own allocations are off until the
	// switch is turned on; the free allocations are moved to another address, so that the port of the new one has to come from the range that is set.
	sqlDB.Exec("UPDATE allocations SET ip = '10.9.9.9' WHERE server_id IS NULL")
	if status, _, out := b.do("POST", "/api/client/servers/11111111/network/allocations", nil, b.xsrf()); status != 400 || !strings.Contains(string(out), "auto-allocation is not enabled") {
		t.Errorf("an allocation with the switch off = %d\n%s", status, out)
	}
	do("", "settings", "set", "pterodactyl:client_features:allocations:enabled", "true")
	do("", "settings", "set", "pterodactyl:client_features:allocations:range_start", "30000")
	do("", "settings", "set", "pterodactyl:client_features:allocations:range_end", "30010")
	added := b.json("POST", "/api/client/servers/11111111/network/allocations", nil, 200)
	if port, _ := attr(added, "attributes", "port").(float64); port < 30000 || port > 30010 {
		t.Errorf("the new allocation = %v, want a port from 30000 to 30010", added)
	}

	b.json("POST", "/api/client/servers/11111111/backups", map[string]any{"name": "x"}, 504) // no node behind it: it is told so, and nothing is left behind
	var backups int
	sqlDB.QueryRow("SELECT COUNT(*) FROM backups WHERE deleted_at IS NULL").Scan(&backups)
	if backups != 0 {
		t.Errorf("%d backups after one that could not be started", backups)
	}

	// Two-factor authentication: set it up with the secret the panel gives, sign out, and sign in again with a code. A code is good once, so the second
	// sign-in needs the code of the next period.
	setup := b.json("GET", "/api/client/account/two-factor", nil, 200)
	secret, _ := attr(setup, "data", "secret").(string)
	if secret == "" {
		t.Fatalf("two-factor setup = %v", setup)
	}
	step := totp.Step(time.Now())
	otp, err := totp.Code(secret, step)
	if err != nil {
		t.Fatal(err)
	}
	if status, _, out := b.do("POST", "/api/client/account/two-factor", map[string]any{"code": "000000", "password": password}, b.xsrf()); status == 200 {
		t.Errorf("a wrong code switched two-factor on: %s", out)
	}
	b.json("POST", "/api/client/account/two-factor", map[string]any{"code": otp, "password": password}, 200)
	b.do("POST", "/auth/logout", nil, b.xsrf())
	b.do("GET", "/sanctum/csrf-cookie", nil, nil)
	again := b.json("POST", "/auth/login", map[string]any{"user": "admin", "password": password}, 200)
	confirmation, _ := attr(again, "data", "confirmation_token").(string)
	if attr(again, "data", "complete") != false || confirmation == "" {
		t.Fatalf("a sign-in with two-factor on = %v", again)
	}
	if status, _, _ := b.do("GET", "/api/client", nil, nil); status != 401 {
		t.Errorf("before the second factor the client API = %d", status)
	}
	next, _ := totp.Code(secret, step+1)
	if status, _, out := b.do("POST", "/auth/login/checkpoint", map[string]any{"confirmation_token": confirmation, "authentication_code": "000000"}, b.xsrf()); status == 200 {
		t.Errorf("a wrong code was accepted: %s", out)
	}
	if done := b.json("POST", "/auth/login/checkpoint", map[string]any{"confirmation_token": confirmation, "authentication_code": next}, 200); attr(done, "data", "complete") != true {
		t.Fatalf("checkpoint = %v", done)
	}
	b.json("GET", "/api/client", nil, 200)

	// The two APIs are limited per user, after the authorizer: 240 requests a minute. A guest's refused requests do not count against anybody.
	limited := func(c *client, path string, header map[string]string) (at int, retry string) {
		t.Helper()
		for i := 1; i <= 300; i++ {
			status, h, _ := c.do("GET", path, nil, header)
			if status == 429 {
				return i, h.Get("Retry-After")
			}
			if status != 200 {
				t.Fatalf("GET %s = %d on request %d", path, status, i)
			}
		}
		return 0, ""
	}
	if at, retry := limited(script, "/api/application/locations", bearer); at < 200 || at > 245 || retry == "" {
		t.Errorf("the Application API was limited at request %d (Retry-After %q)", at, retry)
	}
	if at, retry := limited(b, "/api/client/permissions", nil); at < 200 || at > 245 || retry == "" {
		t.Errorf("the client API was limited at request %d (Retry-After %q)", at, retry)
	}

	// Signing out ends the session.
	if status, _, out := b.do("POST", "/auth/logout", nil, b.xsrf()); status != 204 && status != 200 {
		t.Errorf("logout = %d\n%s", status, out)
	}
	if status, _, _ := b.do("GET", "/api/client", nil, nil); status != 401 {
		t.Errorf("after logout = %d", status)
	}

	// The panel stops when it is asked to, and said nothing was wrong while it ran.
	shutdown()
	for _, line := range strings.Split(logs.String(), "\n") {
		// The one request that was made to a node that is not there is logged as the error it is.
		if strings.Contains(line, "level=ERROR") && !strings.Contains(line, "lookup node1.example.com") {
			t.Errorf("the log has an error: %s", line)
		}
	}
}

func itoa(f float64) string { return strconv.FormatInt(int64(f), 10) }
