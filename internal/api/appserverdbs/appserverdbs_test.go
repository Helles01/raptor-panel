package appserverdbs

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apitest"
	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/dbhosts"
)

const (
	grace = 1 // may read and write server databases, and read database hosts
	bob   = 2 // may only read server databases
	cleo  = 3 // has other resources only
	dave  = 4 // not an administrator
)

var errBoom = errors.New("the database is down: password=hunter2")

var t0 = time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

type store struct {
	mu sync.Mutex

	servers   map[uint64]bool
	dbs       map[uint64]*appkit.ServerDatabase
	passwords map[uint64]string
	hosts     map[uint64]bool
	limits    map[uint64]*int64
	nextID    uint64
	fail      error

	lastLoad  appkit.Includes
	created   []NewDatabase
	setPasswd []string
	deleted   []uint64
}

func (s *store) Exists(_ context.Context, id uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	if !s.servers[id] {
		return appkit.ErrNotFound
	}
	return nil
}

func (s *store) fill(d appkit.ServerDatabase, load appkit.Includes) appkit.ServerDatabase {
	if load["password"] {
		d.Password = ptr(s.passwords[d.ID])
	}
	if load["host"] {
		d.Host = &appkit.DatabaseHost{ID: d.HostID, Name: "db1", Host: "db.example.com", Port: 3306, Username: "panel", CreatedAt: t0, UpdatedAt: t0}
	}
	return d
}

func (s *store) List(_ context.Context, server uint64, load appkit.Includes) ([]appkit.ServerDatabase, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastLoad = load
	if s.fail != nil {
		return nil, s.fail
	}
	var out []appkit.ServerDatabase
	for id := uint64(1); id < 1000; id++ {
		if d, ok := s.dbs[id]; ok && d.ServerID == server {
			out = append(out, s.fill(*d, load))
		}
	}
	return out, nil
}

func (s *store) Find(_ context.Context, server, id uint64, load appkit.Includes) (appkit.ServerDatabase, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastLoad = load
	if s.fail != nil {
		return appkit.ServerDatabase{}, s.fail
	}
	if d, ok := s.dbs[id]; ok && d.ServerID == server {
		return s.fill(*d, load), nil
	}
	return appkit.ServerDatabase{}, appkit.ErrNotFound
}

func (s *store) Create(_ context.Context, r CreateRequest) (appkit.ServerDatabase, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return appkit.ServerDatabase{}, s.fail
	}
	count := int64(0)
	for _, d := range s.dbs {
		if d.ServerID == r.Database.ServerID {
			count++
			if d.Name == r.Database.Name {
				return appkit.ServerDatabase{}, ErrNameTaken
			}
		}
	}
	if err := r.Check(s.limits[r.Database.ServerID], count); err != nil {
		return appkit.ServerDatabase{}, err
	}
	if !s.hosts[r.Database.HostID] {
		return appkit.ServerDatabase{}, ErrNoHost
	}
	s.created = append(s.created, r.Database)
	s.nextID++
	d := appkit.ServerDatabase{
		ID: s.nextID, ServerID: r.Database.ServerID, HostID: r.Database.HostID, Name: r.Database.Name, Username: r.Database.Username, Remote: r.Database.Remote,
		CreatedAt: t0, UpdatedAt: t0,
	}
	s.dbs[d.ID] = &d
	s.passwords[d.ID] = r.Database.Password
	return d, nil
}

func (s *store) SetPassword(_ context.Context, id uint64, password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setPasswd = append(s.setPasswd, password)
	if s.fail != nil {
		return s.fail
	}
	s.passwords[id] = password
	return nil
}

func (s *store) Delete(_ context.Context, server, id uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	if d, ok := s.dbs[id]; !ok || d.ServerID != server {
		return appkit.ErrNotFound
	}
	s.deleted = append(s.deleted, id)
	delete(s.dbs, id)
	return nil
}

type run struct {
	Host uint64
	SQL  []string
	Args [][]any
}

type hosts struct {
	mu   sync.Mutex
	runs []run
	err  error
	// failN makes only the call with this number fail (from 1); 0 makes them all fail when err is set.
	failN int
}

func (h *hosts) Run(_ context.Context, id uint64, st []dbhosts.Statement) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := run{Host: id}
	for _, s := range st {
		r.SQL = append(r.SQL, s.SQL)
		r.Args = append(r.Args, s.Args)
	}
	h.runs = append(h.runs, r)
	if h.err != nil && (h.failN == 0 || h.failN == len(h.runs)) {
		return h.err
	}
	return nil
}

type rig struct {
	t        *testing.T
	client   *apitest.Client
	store    *store
	hosts    *hosts
	logs     *bytes.Buffer
	handlers *Handlers
}

func newRig(t *testing.T, enabled bool) *rig {
	t.Helper()
	r := &rig{t: t, hosts: &hosts{}, logs: &bytes.Buffer{}}
	r.store = &store{
		nextID:  100,
		servers: map[uint64]bool{7: true, 8: true},
		hosts:   map[uint64]bool{11: true, 12: true},
		limits:  map[uint64]*int64{7: ptr(int64(3))},
		dbs: map[uint64]*appkit.ServerDatabase{
			1: {ID: 1, ServerID: 7, HostID: 11, Name: "s7_world", Username: "u7_abcdefghij", Remote: "%", MaxConnections: 5, CreatedAt: t0, UpdatedAt: t0.Add(time.Hour)},
			2: {ID: 2, ServerID: 8, HostID: 12, Name: "s8_other", Username: "u8_abcdefghij", Remote: "%", CreatedAt: t0, UpdatedAt: t0},
		},
		passwords: map[uint64]string{1: "Existing1Password.ok", 2: "Other1Password.ok"},
	}

	w := apitest.NewWorld()
	for id, admin := range map[uint64]bool{grace: true, bob: true, cleo: true, dave: false} {
		w.AddUser(id, admin)
	}
	w.GrantApp(grace, "server_databases=rw", "database_hosts=read")
	w.GrantApp(bob, "server_databases=read")
	w.GrantApp(cleo, "servers=rw")
	w.GrantApp(dave, "server_databases=rw")

	h, err := New(Deps{Store: r.store, Hosts: r.hosts, Enabled: enabled, BaseURL: "https://panel.example.com", Logger: slog.New(slog.NewTextHandler(r.logs, nil)), Random: rand.Reader})
	if err != nil {
		t.Fatal(err)
	}
	r.handlers = h
	r.client = w.Serve(t, func(g api.Group) { h.Register(g) })
	return r
}

const base = "/api/application/servers"

func (r *rig) do(user uint64, method, path, body string) apitest.Reply {
	r.t.Helper()
	return r.client.AsApp(user, method, base+path, body)
}

const goodBody = `{"database":"survival","remote":"203.0.113.%","host":11}`

func TestEveryRouteNeedsItsLevel(t *testing.T) {
	routes := []struct {
		name, method, path, body string
		write                    bool
	}{
		{"list", "GET", "/7/databases", "", false},
		{"view", "GET", "/7/databases/1", "", false},
		{"create", "POST", "/7/databases", goodBody, true},
		{"reset", "POST", "/7/databases/1/reset-password", "", true},
		{"delete", "DELETE", "/7/databases/1", "", true},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			for user, open := range map[uint64]bool{grace: true, bob: !rt.write, cleo: false, dave: false} {
				r := newRig(t, true)
				rp := r.do(user, rt.method, rt.path, rt.body)
				if open && (rp.Status < 200 || rp.Status > 299) || !open && rp.Status != 403 {
					t.Errorf("user %d: status = %d: %s", user, rp.Status, rp.Raw)
				}
				if !open && (len(r.store.created)+len(r.store.deleted)+len(r.store.setPasswd)+len(r.hosts.runs) != 0) {
					t.Errorf("user %d: something was changed", user)
				}
			}
			r := newRig(t, true)
			if rp := r.do(0, rt.method, rt.path, rt.body); rp.Status != 401 {
				t.Errorf("nobody: status = %d", rp.Status)
			}
			if rp := r.client.As(grace, rt.method, base+rt.path, rt.body); rp.Status != 403 {
				t.Errorf("a client key: status = %d", rp.Status)
			}
		})
	}
}

func TestListAndView(t *testing.T) {
	r := newRig(t, true)
	rp := r.do(bob, "GET", "/7/databases", "")
	if rp.Status != 200 || rp.At("object") != "list" || len(rp.At("data").([]any)) != 1 || rp.Has("meta") {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	want := map[string]any{
		"id": 1.0, "server": 7.0, "host": 11.0, "database": "s7_world", "username": "u7_abcdefghij", "remote": "%", "max_connections": 5.0,
		"created_at": "2026-10-01T10:00:00+00:00", "updated_at": "2026-10-01T11:00:00+00:00",
	}
	if rp.At("data", 0, "object") != "server_database" || !reflect.DeepEqual(rp.At("data", 0, "attributes"), want) {
		t.Errorf("first = %v", rp.At("data", 0))
	}
	if rp.Contains("Existing1") || rp.Has("data", 0, "attributes", "relationships") || len(r.store.lastLoad) != 0 {
		t.Errorf("a password or a relation came without being asked for: %s, load %v", rp.Raw, r.store.lastLoad)
	}

	rp = r.do(bob, "GET", "/7/databases/1", "")
	if rp.Status != 200 || rp.At("attributes", "database") != "s7_world" {
		t.Errorf("view: %d %s", rp.Status, rp.Raw)
	}
	// A server with none has an empty list; a database of another server, or a server that is not there, is not found.
	if rp := r.do(bob, "GET", "/8/databases", ""); rp.Status != 200 || len(rp.At("data").([]any)) != 1 {
		t.Errorf("server 8: %d %s", rp.Status, rp.Raw)
	}
	for _, path := range []string{"/99/databases", "/x/databases", "/7/databases/2", "/7/databases/99", "/7/databases/x", "/99/databases/1"} {
		if rp := r.do(bob, "GET", path, ""); rp.Status != 404 {
			t.Errorf("%s: status = %d", path, rp.Status)
		}
	}
}

func TestIncludes(t *testing.T) {
	r := newRig(t, true)
	rp := r.do(grace, "GET", "/7/databases/1?include=password,host", "")
	if rp.Status != 200 || rp.Header.Get("Cache-Control") != "no-store" ||
		!reflect.DeepEqual(rp.At("attributes", "relationships", "password"), map[string]any{"object": "database_password", "attributes": map[string]any{"password": "Existing1Password.ok"}}) ||
		rp.At("attributes", "relationships", "host", "object") != "database_host" || rp.At("attributes", "relationships", "host", "attributes", "host") != "db.example.com" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if rp.Contains("hunter2") || rp.Contains("\"panel\"") && rp.Contains("password\":\"panel") {
		t.Error("the password of the host's account is shown")
	}
	if rp := r.do(grace, "GET", "/7/databases?include=password", ""); rp.Header.Get("Cache-Control") != "no-store" || rp.At("data", 0, "attributes", "relationships", "password", "attributes", "password") != "Existing1Password.ok" {
		t.Errorf("list: %s", rp.Raw)
	}
	// Without the right to read database hosts the host is a null resource, and the store is not asked.
	r = newRig(t, true)
	rp = r.do(bob, "GET", "/7/databases/1?include=host", "")
	if rp.At("attributes", "relationships", "host", "object") != "null_resource" || len(r.store.lastLoad) != 0 {
		t.Errorf("answer = %s, load %v", rp.Raw, r.store.lastLoad)
	}
}

var passwordForm = regexp.MustCompile(`^[A-Za-z0-9!@=.+^]{24}$`)

func TestCreate(t *testing.T) {
	r := newRig(t, true)
	rp := r.do(grace, "POST", "/7/databases", goodBody)
	if rp.Status != 201 || rp.At("object") != "server_database" || rp.At("meta", "resource") != "https://panel.example.com/api/application/servers/7/databases/101" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	a := rp.At("attributes").(map[string]any)
	if a["id"] != 101.0 || a["server"] != 7.0 || a["host"] != 11.0 || a["database"] != "s7_survival" || a["remote"] != "203.0.113.%" || !regexp.MustCompile(`^u7_[A-Za-z0-9]{10}$`).MatchString(a["username"].(string)) {
		t.Errorf("attributes = %v", a)
	}
	// The password is not part of the answer unless it is asked for.
	pw := r.store.passwords[101]
	if !passwordForm.MatchString(pw) || rp.Contains(pw) || rp.Has("attributes", "relationships") {
		t.Errorf("password %q, answer %s", pw, rp.Raw)
	}
	if len(r.hosts.runs) != 1 || r.hosts.runs[0].Host != 11 || len(r.hosts.runs[0].SQL) != 3 || !strings.HasPrefix(r.hosts.runs[0].SQL[0], "CREATE DATABASE IF NOT EXISTS `s7_survival`") || r.hosts.runs[0].Args[1][0] != pw {
		t.Fatalf("host runs = %+v", r.hosts.runs)
	}
	for _, sql := range r.hosts.runs[0].SQL {
		if strings.Contains(sql, pw) {
			t.Error("the password is in the text of a statement")
		}
	}
	if strings.Contains(r.logs.String(), pw) {
		t.Error("the password is in the log")
	}

	// Asked for, it is shown once, and the answer must not be kept.
	rp = r.do(grace, "POST", "/7/databases?include=password", `{"database":"second","remote":"%","host":12}`)
	if rp.Status != 201 || rp.Header.Get("Cache-Control") != "no-store" || rp.At("attributes", "relationships", "password", "attributes", "password") != r.store.passwords[102] || r.hosts.runs[1].Host != 12 {
		t.Errorf("with the password: %d %s", rp.Status, rp.Raw)
	}
}

func TestCreateRefusals(t *testing.T) {
	tests := []struct {
		name, body    string
		status        int
		detail, field string
	}{
		{"no name", `{"remote":"%","host":11}`, 422, "The database field is required.", "database"},
		{"a name too short", `{"database":"ab","remote":"%","host":11}`, 422, "The database must be between 3 and 48 characters.", "database"},
		{"a name too long", `{"database":"` + strings.Repeat("a", 49) + `","remote":"%","host":11}`, 422, "The database must be between 3 and 48 characters.", "database"},
		{"a name with a space", `{"database":"my world","remote":"%","host":11}`, 422, "The database may only contain letters, numbers, dashes and underscores.", "database"},
		{"a name with a quote", "{\"database\":\"a`b;c\",\"remote\":\"%\",\"host\":11}", 422, "The database may only contain letters, numbers, dashes and underscores.", "database"},
		{"a name that is a number", `{"database":123,"remote":"%","host":11}`, 422, "The database must be a string.", "database"},
		{"no remote", `{"database":"world","host":11}`, 422, "The remote field is required.", "remote"},
		{"a remote with a letter", `{"database":"world","remote":"a.b","host":11}`, 422, "The remote format is invalid.", "remote"},
		{"a remote that is too long", `{"database":"world","remote":"1234567890123456","host":11}`, 422, "The remote format is invalid.", "remote"},
		{"no host", `{"database":"world","remote":"%"}`, 422, "The host field is required.", "host"},
		{"a host that is text", `{"database":"world","remote":"%","host":"db1"}`, 422, "The host must be an integer.", "host"},
		{"a host that is not there", `{"database":"fresh","remote":"%","host":99}`, 422, "The selected host is invalid.", "host"},
		{"a host below one", `{"database":"fresh","remote":"%","host":-3}`, 422, "The selected host is invalid.", "host"},
		{"a name the server has", `{"database":"world","remote":"%","host":11}`, 422, "The database has already been taken.", "database"},
		{"not JSON", `database=world`, 400, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, true)
			rp := r.do(grace, "POST", "/7/databases", tt.body)
			if rp.Status != tt.status || (tt.detail != "" && rp.Detail() != tt.detail) {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if tt.field != "" && rp.At("errors", 0, "meta", "source_field") != tt.field {
				t.Errorf("field = %v", rp.At("errors", 0, "meta"))
			}
			if len(r.store.created) != 0 || len(r.hosts.runs) != 0 {
				t.Error("a database was made")
			}
		})
	}
}

func TestCreateRules(t *testing.T) {
	// A name the server has is a clash, whatever the host; the same name on another server is not.
	r := newRig(t, true)
	if rp := r.do(grace, "POST", "/8/databases", `{"database":"other","remote":"%","host":12}`); rp.Status != 422 {
		t.Errorf("a name of the server: %d %s", rp.Status, rp.Raw)
	}
	if rp := r.do(grace, "POST", "/7/databases", `{"database":"other","remote":"%","host":12}`); rp.Status != 201 {
		t.Errorf("a name of another server: %d %s", rp.Status, rp.Raw)
	}

	// The switch of the panel for creating databases applies here as well, and so does the limit of the server.
	r = newRig(t, false)
	rp := r.do(grace, "POST", "/7/databases", goodBody)
	if rp.Status != 400 || rp.Code() != "DisplayException" || rp.Detail() != dbhosts.ErrFeatureDisabled.Error() || len(r.hosts.runs) != 0 {
		t.Errorf("switch off: %d %s", rp.Status, rp.Raw)
	}
	r = newRig(t, true)
	r.store.limits[7] = ptr(int64(1))
	rp = r.do(grace, "POST", "/7/databases", goodBody)
	if rp.Status != 400 || rp.Code() != "DisplayException" || rp.Detail() != dbhosts.ErrTooMany.Error() || len(r.hosts.runs) != 0 {
		t.Errorf("limit: %d %s", rp.Status, rp.Raw)
	}
	// No limit means no limit.
	r.store.limits[7] = nil
	if rp := r.do(grace, "POST", "/7/databases", goodBody); rp.Status != 201 {
		t.Errorf("no limit: %d %s", rp.Status, rp.Raw)
	}
	if rp := r.do(grace, "POST", "/99/databases", goodBody); rp.Status != 404 {
		t.Errorf("unknown server: %d", rp.Status)
	}
}

func TestCreateWhenTheHostRefuses(t *testing.T) {
	r := newRig(t, true)
	r.hosts.err, r.hosts.failN = errBoom, 1
	rp := r.do(grace, "POST", "/7/databases", goodBody)
	if rp.Status != 500 || rp.Contains("hunter2") {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	// What the host may have made is dropped, and the row goes; the first failure is what is told.
	if len(r.hosts.runs) != 2 || !strings.Contains(strings.Join(r.hosts.runs[1].SQL, ";"), "DROP DATABASE") || !reflect.DeepEqual(r.store.deleted, []uint64{101}) {
		t.Errorf("host runs %+v, deleted %v", r.hosts.runs, r.store.deleted)
	}
	r = newRig(t, true)
	r.hosts.err = errBoom
	if rp := r.do(grace, "POST", "/7/databases", goodBody); rp.Status != 500 || !strings.Contains(r.logs.String(), "was not dropped on the host") {
		t.Errorf("both fail: %d %s, log %s", rp.Status, rp.Raw, r.logs.String())
	}
}

func TestResetPassword(t *testing.T) {
	r := newRig(t, true)
	rp := r.do(grace, "POST", "/7/databases/1/reset-password", "")
	if rp.Status != 204 || len(rp.Raw) != 0 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	pw := r.store.passwords[1]
	if !passwordForm.MatchString(pw) || pw == "Existing1Password.ok" || len(r.hosts.runs) != 1 || r.hosts.runs[0].Host != 11 {
		t.Fatalf("password %q, host runs %+v", pw, r.hosts.runs)
	}
	found := false
	for _, args := range r.hosts.runs[0].Args {
		for _, a := range args {
			found = found || a == pw
		}
	}
	if !found {
		t.Error("the host was not given the new password")
	}
	// The new password is the one a later request can ask for.
	if rp := r.do(grace, "GET", "/7/databases/1?include=password", ""); rp.At("attributes", "relationships", "password", "attributes", "password") != pw {
		t.Errorf("shown: %s", rp.Raw)
	}
	for _, path := range []string{"/99/databases/1/reset-password", "/7/databases/2/reset-password", "/7/databases/99/reset-password"} {
		if rp := r.do(grace, "POST", path, ""); rp.Status != 404 {
			t.Errorf("%s: status = %d", path, rp.Status)
		}
	}
}

func TestResetPasswordWhenTheHostRefuses(t *testing.T) {
	r := newRig(t, true)
	r.hosts.err = errBoom
	rp := r.do(grace, "POST", "/7/databases/1/reset-password", "")
	if rp.Status != 500 || rp.Contains("hunter2") {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	// The panel never keeps a password the host does not have.
	if r.store.passwords[1] != "Existing1Password.ok" || len(r.store.setPasswd) != 2 || r.store.setPasswd[1] != "Existing1Password.ok" {
		t.Errorf("password %q, stored %v", r.store.passwords[1], r.store.setPasswd)
	}
}

func TestDelete(t *testing.T) {
	r := newRig(t, true)
	if rp := r.do(grace, "DELETE", "/7/databases/1", ""); rp.Status != 204 || len(rp.Raw) != 0 || !reflect.DeepEqual(r.store.deleted, []uint64{1}) {
		t.Fatalf("answer = %d %s, deleted %v", rp.Status, rp.Raw, r.store.deleted)
	}
	if len(r.hosts.runs) != 1 || r.hosts.runs[0].Host != 11 || !strings.Contains(strings.Join(r.hosts.runs[0].SQL, ";"), "DROP DATABASE") {
		t.Errorf("host runs = %+v", r.hosts.runs)
	}
	for _, path := range []string{"/99/databases/2", "/7/databases/2", "/7/databases/99", "/7/databases/x"} {
		if rp := r.do(grace, "DELETE", path, ""); rp.Status != 404 {
			t.Errorf("%s: status = %d", path, rp.Status)
		}
	}
	// The host refuses: the row stays and the request can be made again.
	r = newRig(t, true)
	r.hosts.err = errBoom
	if rp := r.do(grace, "DELETE", "/7/databases/1", ""); rp.Status != 500 || rp.Contains("hunter2") || len(r.store.deleted) != 0 {
		t.Errorf("host refuses: %d %s", rp.Status, rp.Raw)
	}
}

func TestStoreFailuresAreNotShown(t *testing.T) {
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "/7/databases", ""}, {"GET", "/7/databases/1", ""}, {"POST", "/7/databases", goodBody},
		{"POST", "/7/databases/1/reset-password", ""}, {"DELETE", "/7/databases/1", ""},
	} {
		r := newRig(t, true)
		r.store.fail = errBoom
		rp := r.do(grace, rt.method, rt.path, rt.body)
		if rp.Status != 500 || rp.Contains("hunter2") {
			t.Errorf("%s %s: %d %s", rt.method, rt.path, rp.Status, rp.Raw)
		}
	}
}

func TestHandlersRefuseWithoutTheAuthorizer(t *testing.T) {
	r := newRig(t, true)
	client := apitest.NewWorld().Serve(t, func(g api.Group) {
		b := g.Sub("/bare")
		b.GET("", r.handlers.List, api.Public())
		b.GET("/view", r.handlers.View, api.Public())
		b.POST("", r.handlers.Create, api.Public())
		b.POST("/reset", r.handlers.ResetPassword, api.Public())
		b.DELETE("/delete", r.handlers.Delete, api.Public())
	})
	for _, rt := range []struct{ method, path string }{{"GET", "/bare"}, {"GET", "/bare/view"}, {"POST", "/bare"}, {"POST", "/bare/reset"}, {"DELETE", "/bare/delete"}} {
		if rp := client.As(0, rt.method, rt.path, `{}`); rp.Status != 403 {
			t.Errorf("%s %s: status = %d, want 403", rt.method, rt.path, rp.Status)
		}
	}
	if len(r.store.created)+len(r.store.deleted)+len(r.store.setPasswd)+len(r.hosts.runs) != 0 {
		t.Error("something was changed")
	}
}

func TestNewNeedsEverything(t *testing.T) {
	r := newRig(t, true)
	full := r.handlers.d
	if _, err := New(full); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Deps){
		"no store": func(d *Deps) { d.Store = nil }, "no hosts": func(d *Deps) { d.Hosts = nil }, "no address": func(d *Deps) { d.BaseURL = "" }, "no logger": func(d *Deps) { d.Logger = nil },
	} {
		d := full
		mutate(&d)
		if _, err := New(d); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	d := full
	d.Random = nil
	if h, err := New(d); err != nil || h.d.Random == nil {
		t.Errorf("default random: %v", err)
	}
}
