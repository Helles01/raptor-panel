package databaseapi

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

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apitest"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/dbhosts"
)

const (
	ada   = 1 // owner
	grace = 2 // administrator
	bob   = 3 // holds the database rights, also the view of the password
	cleo  = 4 // may look, not see passwords
	dave  = 5 // may create only
	eve   = 6 // a stranger
)

const serverUUID = "aaaa1111-2222-4333-8444-555555555555"

var errBoom = errors.New("the database is down: password=hunter2")

func ptr[T any](v T) *T { return &v }

type store struct {
	mu sync.Mutex

	records   []Record
	passwords map[uint64]string
	hosts     []dbhosts.HostChoice
	limit     *int64
	taken     bool
	nextID    uint64
	fail      error

	created   []NewDatabase
	deleted   []uint64
	setPasswd []string
}

func (s *store) List(_ context.Context, _ uint64) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Record(nil), s.records...), s.fail
}

func (s *store) Find(_ context.Context, _, id uint64) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return Record{}, s.fail
	}
	for _, r := range s.records {
		if r.ID == id {
			return r, nil
		}
	}
	return Record{}, ErrNotFound
}

func (s *store) Password(_ context.Context, id uint64) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return "", s.fail
	}
	p, ok := s.passwords[id]
	if !ok {
		return "", ErrNotFound
	}
	return p, nil
}

func (s *store) Hosts(context.Context) ([]dbhosts.HostChoice, error) { return s.hosts, nil }

func (s *store) Create(_ context.Context, r CreateRequest) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return Record{}, s.fail
	}
	if err := r.Check(s.limit, int64(len(s.records))); err != nil {
		return Record{}, err
	}
	if s.taken {
		return Record{}, ErrNameTaken
	}
	s.created = append(s.created, r.Database)
	s.nextID++
	rec := Record{ID: s.nextID, HostID: r.Database.HostID, HostAddress: "db.example.com", HostPort: 3306, Name: r.Database.Name, Username: r.Database.Username, Remote: r.Database.Remote}
	s.records = append(s.records, rec)
	s.passwords[rec.ID] = r.Database.Password
	return rec, nil
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

func (s *store) Delete(_ context.Context, _, id uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	s.deleted = append(s.deleted, id)
	for i, r := range s.records {
		if r.ID == id {
			s.records = append(s.records[:i:i], s.records[i+1:]...)
			break
		}
	}
	return nil
}

type run struct {
	Host uint64
	SQL  []string
	Args [][]any
}

type hosts struct {
	mu    sync.Mutex
	runs  []run
	err   error
	failN int // the call with this number (from 1) fails, 0 for all of them when err is set
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

type activityLog struct {
	mu      sync.Mutex
	entries []Entry
	err     error
}

func (a *activityLog) Log(_ context.Context, e Entry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	a.entries = append(a.entries, e)
	return nil
}

type rig struct {
	t        *testing.T
	client   *apitest.Client
	store    *store
	hosts    *hosts
	activity *activityLog
	logs     *bytes.Buffer
	handlers *Handlers
}

type options struct {
	disabled    bool
	allowRandom bool
}

func newRig(t *testing.T, opts ...func(*options)) *rig {
	t.Helper()
	var o options
	for _, f := range opts {
		f(&o)
	}
	r := &rig{t: t, hosts: &hosts{}, activity: &activityLog{}, logs: &bytes.Buffer{}}
	node := uint64(1)
	r.store = &store{
		passwords: map[uint64]string{7: "Existing1Password.ok"}, limit: ptr(int64(3)), nextID: 100,
		hosts: []dbhosts.HostChoice{{ID: 11, NodeID: &node}},
		records: []Record{
			{ID: 7, HostID: 11, HostAddress: "db.example.com", HostPort: 3306, Name: "s100_world", Username: "u100_abcdefghij", Remote: "%", MaxConnections: ptr(5)},
		},
	}

	w := apitest.NewWorld()
	for id, admin := range map[uint64]bool{ada: false, grace: true, bob: false, cleo: false, dave: false, eve: false} {
		w.AddUser(id, admin)
	}
	w.AddServer(100, serverUUID, ada) // on node 1
	w.AddServer(101, "bbbb2222-2222-4333-8444-555555555555", ada)
	w.Subusers[[2]uint64{100, bob}] = []string{"database.read", "database.create", "database.update", "database.delete", "database.view_password", "websocket.connect"}
	w.Subusers[[2]uint64{100, cleo}] = []string{"database.read", "websocket.connect"}
	w.Subusers[[2]uint64{100, dave}] = []string{"database.create", "websocket.connect"}

	limiter, err := middleware.NewLimiter(CreateLimit, CreateWindow)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Deps{
		Store: r.store, Hosts: r.hosts, Activity: r.activity, CreateLimiter: limiter, Enabled: !o.disabled, AllowRandom: o.allowRandom,
		Logger: slog.New(slog.NewTextHandler(r.logs, nil)), Random: rand.Reader,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.handlers = h
	r.client = w.Serve(t, func(g api.Group) { h.Register(g) })
	return r
}

const base = "/api/client/servers/aaaa1111/databases"

func (r *rig) do(user uint64, method, path, body string) apitest.Reply {
	r.t.Helper()
	return r.client.As(user, method, base+path, body)
}

func TestList(t *testing.T) {
	r := newRig(t)
	rp := r.do(ada, "GET", "", "")
	if rp.Status != 200 || rp.At("object") != "list" {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	want := map[string]any{
		"id": "7", "host": map[string]any{"address": "db.example.com", "port": 3306.0}, "name": "s100_world", "username": "u100_abcdefghij",
		"connections_from": "%", "max_connections": 5.0,
	}
	if rp.At("data", 0, "object") != "server_database" || !reflect.DeepEqual(rp.At("data", 0, "attributes"), want) {
		t.Errorf("first = %v", rp.At("data", 0))
	}
	// Without the include, no password is mentioned or opened.
	if rp.Has("data", 0, "attributes", "relationships") || rp.Contains("Existing1") {
		t.Errorf("a password came without being asked for: %s", rp.Raw)
	}
}

func TestPasswordsAreForThoseWhoMaySeeThem(t *testing.T) {
	r := newRig(t)
	for _, user := range []uint64{ada, grace, bob} {
		rp := r.do(user, "GET", "?include=password", "")
		if rp.Status != 200 || rp.At("data", 0, "attributes", "relationships", "password", "object") != "database_password" ||
			rp.At("data", 0, "attributes", "relationships", "password", "attributes", "password") != "Existing1Password.ok" {
			t.Errorf("user %d: %s", user, rp.Raw)
		}
		if rp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("user %d: Cache-Control = %q", user, rp.Header.Get("Cache-Control"))
		}
	}
	rp := r.do(cleo, "GET", "?include=password", "")
	if rp.Status != 200 || rp.At("data", 0, "attributes", "relationships", "password", "object") != "null_resource" || rp.Contains("Existing1") {
		t.Errorf("cleo: %s", rp.Raw)
	}
	// Unknown includes are left out.
	if rp := r.do(ada, "GET", "?include=host", ""); rp.Has("data", 0, "attributes", "relationships") {
		t.Errorf("answer = %s", rp.Raw)
	}
}

func TestEveryRouteHasItsPermission(t *testing.T) {
	routes := []struct {
		name, method, path, body string
		allowed                  []uint64
	}{
		{"list", "GET", "", "", []uint64{bob, cleo}},
		{"create", "POST", "", `{"database":"world2","remote":"%"}`, []uint64{bob, dave}},
		{"rotate", "POST", "/7/rotate-password", "", []uint64{bob}},
		{"delete", "DELETE", "/7", "", []uint64{bob}},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			for _, user := range []uint64{ada, grace, bob, cleo, dave} {
				r := newRig(t)
				rp := r.do(user, rt.method, rt.path, rt.body)
				open := user == ada || user == grace
				for _, a := range rt.allowed {
					open = open || a == user
				}
				if open && (rp.Status < 200 || rp.Status > 299) || !open && rp.Status != 403 {
					t.Errorf("user %d: status = %d: %s", user, rp.Status, rp.Raw)
				}
			}
			r := newRig(t)
			if rp := r.do(eve, rt.method, rt.path, rt.body); rp.Status != 404 {
				t.Errorf("a stranger: status = %d", rp.Status)
			}
			if rp := r.do(0, rt.method, rt.path, rt.body); rp.Status != 401 {
				t.Errorf("nobody: status = %d", rp.Status)
			}
		})
	}
}

var passwordForm = regexp.MustCompile(`^[A-Za-z0-9!@=.+^]{24}$`)

func TestCreate(t *testing.T) {
	r := newRig(t)
	rp := r.do(ada, "POST", "", `{"database":"survival","remote":"203.0.113.%"}`)
	if rp.Status != 200 || rp.At("object") != "server_database" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	name, _ := rp.At("attributes", "name").(string)
	user, _ := rp.At("attributes", "username").(string)
	if name != "s100_survival" || !regexp.MustCompile(`^u100_[A-Za-z0-9]{10}$`).MatchString(user) ||
		rp.At("attributes", "connections_from") != "203.0.113.%" || rp.At("attributes", "id") != "101" {
		t.Errorf("attributes = %v", rp.At("attributes"))
	}
	pw, _ := rp.At("attributes", "relationships", "password", "attributes", "password").(string)
	if !passwordForm.MatchString(pw) {
		t.Errorf("password = %q", pw)
	}
	if rp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rp.Header.Get("Cache-Control"))
	}

	// The password shown is the one the host was given and the store holds.
	if len(r.hosts.runs) != 1 || r.hosts.runs[0].Host != 11 || len(r.hosts.runs[0].SQL) != 3 {
		t.Fatalf("runs = %+v", r.hosts.runs)
	}
	if !strings.HasPrefix(r.hosts.runs[0].SQL[0], "CREATE DATABASE IF NOT EXISTS `s100_survival`") || r.hosts.runs[0].Args[1][0] != pw || r.store.passwords[101] != pw {
		t.Errorf("host got %+v, store %q", r.hosts.runs[0], r.store.passwords[101])
	}
	for _, sql := range r.hosts.runs[0].SQL {
		if strings.Contains(sql, pw) {
			t.Error("the password is in the text of a statement")
		}
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:database.create" || r.activity.entries[0].Properties != `{"name":"s100_survival"}` || r.activity.entries[0].DatabaseID != 101 {
		t.Errorf("entries = %+v", r.activity.entries)
	}
}

func TestCreateByAUserWhoMaySeeNoPassword(t *testing.T) {
	r := newRig(t)
	rp := r.do(dave, "POST", "", `{"database":"world3","remote":"%"}`)
	if rp.Status != 200 || rp.At("attributes", "relationships", "password", "object") != "null_resource" || rp.Contains(r.store.passwords[101]) {
		t.Errorf("answer = %d %s", rp.Status, rp.Raw)
	}
}

func TestCreateRefusals(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		status int
		detail string
		field  string
	}{
		{"no name", `{"remote":"%"}`, 422, "The database field is required.", "database"},
		{"a name too short", `{"database":"ab","remote":"%"}`, 422, "The database must be between 3 and 48 characters.", "database"},
		{"a name too long", `{"database":"` + strings.Repeat("a", 49) + `","remote":"%"}`, 422, "The database must be between 3 and 48 characters.", "database"},
		{"a name with a space", `{"database":"my world","remote":"%"}`, 422, "The database may only contain letters, numbers, dashes and underscores.", "database"},
		{"a name with a quote", "{\"database\":\"a`b;c\",\"remote\":\"%\"}", 422, "The database may only contain letters, numbers, dashes and underscores.", "database"},
		{"a name that is a number", `{"database":123,"remote":"%"}`, 422, "The database must be a string.", "database"},
		{"no remote", `{"database":"world"}`, 422, "The remote field is required.", "remote"},
		{"a remote with a quote", `{"database":"world","remote":"a'b"}`, 422, "The remote format is invalid.", "remote"},
		{"a remote with a space", `{"database":"world","remote":"a b"}`, 422, "The remote format is invalid.", "remote"},
		{"not JSON", `database=world`, 400, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(ada, "POST", "", tt.body)
			if rp.Status != tt.status || (tt.detail != "" && rp.Detail() != tt.detail) {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if tt.field != "" && rp.At("errors", 0, "meta", "source_field") != tt.field {
				t.Errorf("field = %v", rp.At("errors", 0, "meta"))
			}
			if len(r.store.created) != 0 || len(r.hosts.runs) != 0 || len(r.activity.entries) != 0 {
				t.Error("a database was made")
			}
		})
	}
}

func TestCreateRules(t *testing.T) {
	t.Run("the same name twice", func(t *testing.T) {
		r := newRig(t)
		r.store.taken = true
		rp := r.do(ada, "POST", "", `{"database":"world","remote":"%"}`)
		if rp.Status != 422 || rp.Detail() != "The database name you have selected is already in use by this server." || len(r.hosts.runs) != 0 {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
	t.Run("the switch is off", func(t *testing.T) {
		r := newRig(t, func(o *options) { o.disabled = true })
		rp := r.do(ada, "POST", "", `{"database":"world","remote":"%"}`)
		if rp.Status != 400 || rp.Code() != "DisplayException" || rp.Detail() != "Client database creation is not enabled in this Panel." {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
	t.Run("the limit is reached", func(t *testing.T) {
		r := newRig(t)
		r.store.limit = ptr(int64(1))
		rp := r.do(ada, "POST", "", `{"database":"world","remote":"%"}`)
		if rp.Status != 400 || rp.Detail() != "Operation aborted: creating a new database would put this server over the defined limit." || len(r.hosts.runs) != 0 {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
	t.Run("no limit set is no limit", func(t *testing.T) {
		r := newRig(t)
		r.store.limit = nil
		if rp := r.do(ada, "POST", "", `{"database":"world","remote":"%"}`); rp.Status != 200 {
			t.Errorf("status = %d: %s", rp.Status, rp.Raw)
		}
	})
	t.Run("a host not linked to the node", func(t *testing.T) {
		r := newRig(t)
		other := uint64(2)
		r.store.hosts = []dbhosts.HostChoice{{ID: 12, NodeID: &other}}
		rp := r.do(ada, "POST", "", `{"database":"world","remote":"%"}`)
		if rp.Status != 400 || rp.Detail() != "No database host was found that meets the requirements for this server." {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
	t.Run("a host not linked to the node, where that is allowed", func(t *testing.T) {
		r := newRig(t, func(o *options) { o.allowRandom = true })
		other := uint64(2)
		r.store.hosts = []dbhosts.HostChoice{{ID: 12, NodeID: &other}}
		if rp := r.do(ada, "POST", "", `{"database":"world","remote":"%"}`); rp.Status != 200 || r.hosts.runs[0].Host != 12 {
			t.Errorf("status = %d, runs %+v", rp.Status, r.hosts.runs)
		}
	})
	t.Run("no hosts at all", func(t *testing.T) {
		r := newRig(t)
		r.store.hosts = nil
		if rp := r.do(ada, "POST", "", `{"database":"world","remote":"%"}`); rp.Status != 400 {
			t.Errorf("status = %d", rp.Status)
		}
	})
}

func TestCreateIsThrottledForEachServer(t *testing.T) {
	r := newRig(t)
	r.store.limit = nil
	for i, name := range []string{"first", "second"} {
		if rp := r.do(ada, "POST", "", `{"database":"`+name+`","remote":"%"}`); rp.Status != 200 {
			t.Fatalf("creation %d: status = %d: %s", i+1, rp.Status, rp.Raw)
		}
	}
	if rp := r.do(ada, "POST", "", `{"database":"third","remote":"%"}`); rp.Status != 429 || rp.Header.Get("Retry-After") == "" {
		t.Errorf("the third: %d, Retry-After %q", rp.Status, rp.Header.Get("Retry-After"))
	}
	if rp := r.client.As(ada, "POST", "/api/client/servers/bbbb2222/databases", `{"database":"third","remote":"%"}`); rp.Status == 429 {
		t.Errorf("another server was throttled too")
	}
	r = newRig(t)
	for i := 0; i < 10; i++ {
		r.do(eve, "POST", "", `{}`)
		r.do(cleo, "POST", "", `{}`)
	}
	if rp := r.do(ada, "POST", "", `{"database":"world2","remote":"%"}`); rp.Status != 200 {
		t.Errorf("refused calls used the allowance up: %d", rp.Status)
	}
}

func TestACreationTheHostRefusesIsTakenBack(t *testing.T) {
	r := newRig(t)
	r.hosts.err = errBoom
	r.hosts.failN = 1 // the creation fails, the clean-up goes through
	rp := r.do(ada, "POST", "", `{"database":"world2","remote":"%"}`)
	if rp.Status != 500 || rp.Contains("hunter2") {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if len(r.store.records) != 1 || len(r.store.deleted) != 1 || len(r.activity.entries) != 0 {
		t.Errorf("records %d, deleted %v, entries %v", len(r.store.records), r.store.deleted, r.activity.entries)
	}
	// What may have been made on the host is dropped.
	if len(r.hosts.runs) != 2 || !strings.HasPrefix(r.hosts.runs[1].SQL[0], "DROP DATABASE IF EXISTS `s100_world2`") {
		t.Errorf("runs = %+v", r.hosts.runs)
	}

	// If the clean-up fails as well, the person is told about the first failure and the second is logged.
	r = newRig(t)
	r.hosts.err = errBoom
	rp = r.do(ada, "POST", "", `{"database":"world2","remote":"%"}`)
	if rp.Status != 500 || !strings.Contains(r.logs.String(), "was not dropped on the host") {
		t.Errorf("answer = %d, logs %s", rp.Status, r.logs.String())
	}
}

func TestRotatePassword(t *testing.T) {
	r := newRig(t)
	rp := r.do(bob, "POST", "/7/rotate-password", "")
	if rp.Status != 200 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	pw, _ := rp.At("attributes", "relationships", "password", "attributes", "password").(string)
	if !passwordForm.MatchString(pw) || pw == "Existing1Password.ok" || r.store.passwords[7] != pw {
		t.Errorf("password %q, stored %q", pw, r.store.passwords[7])
	}
	if len(r.hosts.runs) != 1 || len(r.hosts.runs[0].SQL) != 1 || !strings.HasPrefix(r.hosts.runs[0].SQL[0], "ALTER USER `u100_abcdefghij`@`%` IDENTIFIED BY ?") || r.hosts.runs[0].Args[0][0] != pw {
		t.Errorf("host got %+v", r.hosts.runs)
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:database.rotate-password" || r.activity.entries[0].Properties != `{"name":"s100_world"}` {
		t.Errorf("entries = %+v", r.activity.entries)
	}
}

func TestRotateFailures(t *testing.T) {
	t.Run("the host refuses and the old password is put back", func(t *testing.T) {
		r := newRig(t)
		r.hosts.err = errBoom
		rp := r.do(bob, "POST", "/7/rotate-password", "")
		if rp.Status != 500 || rp.Contains("hunter2") {
			t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
		}
		if r.store.passwords[7] != "Existing1Password.ok" || len(r.store.setPasswd) != 2 || len(r.activity.entries) != 0 {
			t.Errorf("stored %q after %v, entries %v", r.store.passwords[7], r.store.setPasswd, r.activity.entries)
		}
	})
	t.Run("a database of another server", func(t *testing.T) {
		r := newRig(t)
		for _, path := range []string{"/99/rotate-password", "/abc/rotate-password", "/0/rotate-password"} {
			if rp := r.do(bob, "POST", path, ""); rp.Status != 404 {
				t.Errorf("%s: status = %d", path, rp.Status)
			}
		}
		if len(r.hosts.runs) != 0 {
			t.Error("a host was told")
		}
	})
}

func TestDelete(t *testing.T) {
	r := newRig(t)
	rp := r.do(bob, "DELETE", "/7", "")
	if rp.Status != 204 || len(rp.Raw) != 0 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if len(r.hosts.runs) != 1 || len(r.hosts.runs[0].SQL) != 2 || r.hosts.runs[0].SQL[0] != "DROP DATABASE IF EXISTS `s100_world`" || r.hosts.runs[0].SQL[1] != "DROP USER IF EXISTS `u100_abcdefghij`@`%`" {
		t.Errorf("host got %+v", r.hosts.runs)
	}
	if !reflect.DeepEqual(r.store.deleted, []uint64{7}) {
		t.Errorf("deleted %v", r.store.deleted)
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:database.delete" {
		t.Errorf("entries = %+v", r.activity.entries)
	}
}

func TestDeleteWhenTheHostRefuses(t *testing.T) {
	r := newRig(t)
	r.hosts.err = errBoom
	rp := r.do(bob, "DELETE", "/7", "")
	if rp.Status != 500 || rp.Contains("hunter2") || len(r.store.deleted) != 0 || len(r.activity.entries) != 0 || len(r.store.records) != 1 {
		t.Errorf("answer = %d %s, deleted %v", rp.Status, rp.Raw, r.store.deleted)
	}
	if rp := r.do(bob, "DELETE", "/99", ""); rp.Status != 404 {
		t.Errorf("unknown: %d", rp.Status)
	}
}

func TestADatabaseThatIsNotTheServersIsNeverDropped(t *testing.T) {
	r := newRig(t)
	// A row that names a database of server 5 for server 100: the builders refuse it, and nothing is run.
	r.store.records[0].Name = "s5_world"
	if rp := r.do(bob, "DELETE", "/7", ""); rp.Status != 500 || len(r.hosts.runs) != 0 || len(r.store.deleted) != 0 {
		t.Errorf("status = %d, runs %v", rp.Status, r.hosts.runs)
	}
}

func TestStoreFailuresDoNotLeak(t *testing.T) {
	for name, call := range map[string]func(*rig) apitest.Reply{
		"list":   func(r *rig) apitest.Reply { return r.do(ada, "GET", "", "") },
		"create": func(r *rig) apitest.Reply { return r.do(ada, "POST", "", `{"database":"world2","remote":"%"}`) },
		"rotate": func(r *rig) apitest.Reply { return r.do(ada, "POST", "/7/rotate-password", "") },
		"delete": func(r *rig) apitest.Reply { return r.do(ada, "DELETE", "/7", "") },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.store.fail = errBoom
			rp := call(r)
			if rp.Status != 500 || rp.Contains("hunter2") {
				t.Errorf("answer = %d %s", rp.Status, rp.Raw)
			}
		})
	}
}

func TestAnEntryThatCannotBeStoredDoesNotUndoTheChange(t *testing.T) {
	r := newRig(t)
	r.activity.err = errBoom
	if rp := r.do(bob, "DELETE", "/7", ""); rp.Status != 204 || len(r.store.deleted) != 1 {
		t.Errorf("status = %d", rp.Status)
	}
	if !strings.Contains(r.logs.String(), "activity entry not stored") {
		t.Errorf("logs = %s", r.logs.String())
	}
}

func TestHandlersRefuseWithoutTheirPrincipal(t *testing.T) {
	r := newRig(t)
	client := apitest.NewWorld().Serve(t, func(g api.Group) {
		b := g.Sub("/bare")
		b.GET("", r.handlers.List, api.Public())
		b.POST("", r.handlers.Create, api.Public())
		b.POST("/:database/rotate-password", r.handlers.RotatePassword, api.Public())
		b.DELETE("/:database", r.handlers.Delete, api.Public())
	})
	for _, rt := range []struct{ method, path string }{{"GET", "/bare"}, {"POST", "/bare"}, {"POST", "/bare/7/rotate-password"}, {"DELETE", "/bare/7"}} {
		if rp := client.As(0, rt.method, rt.path, `{}`); rp.Status != 403 {
			t.Errorf("%s %s: status = %d, want 403", rt.method, rt.path, rp.Status)
		}
	}
	if len(r.hosts.runs)+len(r.store.created) != 0 {
		t.Error("something was changed")
	}
}

func TestNewNeedsEverything(t *testing.T) {
	r := newRig(t)
	full := r.handlers.d
	if _, err := New(full); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Deps){
		"no store": func(d *Deps) { d.Store = nil }, "no hosts": func(d *Deps) { d.Hosts = nil }, "no activity": func(d *Deps) { d.Activity = nil },
		"no limiter": func(d *Deps) { d.CreateLimiter = nil }, "no logger": func(d *Deps) { d.Logger = nil },
	} {
		d := full
		mutate(&d)
		if _, err := New(d); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
