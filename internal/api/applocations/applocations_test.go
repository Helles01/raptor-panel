package applocations

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apitest"
	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/nodes"
	"github.com/Helles01/raptor-panel/internal/servers"
)

const (
	grace = 1 // may read and write locations and read nodes
	bob   = 2 // may only read locations
	cleo  = 3 // has other resources only
	dave  = 4 // not an administrator
)

var errBoom = errors.New("the database is down: password=hunter2")

var t0 = time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

type store struct {
	mu sync.Mutex

	locations []appkit.Location
	nodesOf   map[uint64][]appkit.Node
	nextID    uint64
	fail      error
	hasNodes  int64

	lastLoad  appkit.Includes
	lastQuery listquery.Query
	listPage  [2]int
	created   []nodes.Location
	updated   []nodes.Location
	deleted   []uint64
}

func (s *store) fill(l appkit.Location, load appkit.Includes) appkit.Location {
	if load["servers"] {
		l.Servers = []appkit.Server{{ID: 500, ShortID: "aaaa1111", Config: servers.Server{UUID: "aaaa1111-2222-4333-8444-555555555555", Name: "Survival"}}}
	}
	if load["nodes"] {
		l.Nodes = s.nodesOf[l.ID]
	}
	return l
}

func (s *store) List(_ context.Context, q listquery.Query, page, perPage int, load appkit.Includes) ([]appkit.Location, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastLoad, s.lastQuery, s.listPage = load, q, [2]int{page, perPage}
	out := make([]appkit.Location, len(s.locations))
	for i, l := range s.locations {
		out[i] = s.fill(l, load)
	}
	return out, len(out), s.fail
}

func (s *store) index(id uint64) (int, bool) {
	for i, l := range s.locations {
		if l.ID == id {
			return i, true
		}
	}
	return 0, false
}

func (s *store) Find(_ context.Context, id uint64, load appkit.Includes) (appkit.Location, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastLoad = load
	if s.fail != nil {
		return appkit.Location{}, s.fail
	}
	if i, ok := s.index(id); ok {
		return s.fill(s.locations[i], load), nil
	}
	return appkit.Location{}, appkit.ErrNotFound
}

func (s *store) taken(except uint64, short string) error {
	for _, l := range s.locations {
		if l.ID != except && l.Short == short {
			return &appkit.TakenError{Field: "short"}
		}
	}
	return nil
}

func longOf(l nodes.Location) *string {
	if l.Long == "" {
		return nil
	}
	return &l.Long
}

func (s *store) Create(_ context.Context, l nodes.Location) (appkit.Location, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return appkit.Location{}, s.fail
	}
	if err := s.taken(0, l.Short); err != nil {
		return appkit.Location{}, err
	}
	s.created = append(s.created, l)
	s.nextID++
	rec := appkit.Location{ID: s.nextID, Short: l.Short, Long: longOf(l), CreatedAt: t0, UpdatedAt: t0}
	s.locations = append(s.locations, rec)
	return rec, nil
}

func (s *store) Update(_ context.Context, id uint64, l nodes.Location) (appkit.Location, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return appkit.Location{}, s.fail
	}
	i, ok := s.index(id)
	if !ok {
		return appkit.Location{}, appkit.ErrNotFound
	}
	if err := s.taken(id, l.Short); err != nil {
		return appkit.Location{}, err
	}
	s.updated = append(s.updated, l)
	s.locations[i].Short, s.locations[i].Long, s.locations[i].UpdatedAt = l.Short, longOf(l), t0.Add(time.Hour)
	return s.locations[i], nil
}

func (s *store) Delete(_ context.Context, id uint64, check func(int64) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	if _, ok := s.index(id); !ok {
		return appkit.ErrNotFound
	}
	if err := check(s.hasNodes); err != nil {
		return err
	}
	s.deleted = append(s.deleted, id)
	return nil
}

type rig struct {
	t        *testing.T
	client   *apitest.Client
	store    *store
	logs     *bytes.Buffer
	handlers *Handlers
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, store: &store{nextID: 100}, logs: &bytes.Buffer{}}
	r.store.locations = []appkit.Location{
		{ID: 1, Short: "us.nyc", Long: ptr("New York"), CreatedAt: t0, UpdatedAt: t0.Add(time.Minute)},
		{ID: 2, Short: "eu.ams", CreatedAt: t0, UpdatedAt: t0},
	}
	r.store.nodesOf = map[uint64][]appkit.Node{1: {{
		ID: 7, UUID: "70000000-0000-4000-8000-000000000000", CreatedAt: t0, UpdatedAt: t0,
		Settings: nodes.Settings{Name: "node-7", LocationID: 1, FQDN: "n7.example.com", Scheme: "https", Memory: 1024, Disk: 2048, DaemonBase: "/srv/daemon", DaemonSFTP: 2022, DaemonListen: 8080, UploadSize: 100},
	}}}

	w := apitest.NewWorld()
	for id, admin := range map[uint64]bool{grace: true, bob: true, cleo: true, dave: false} {
		w.AddUser(id, admin)
	}
	w.GrantApp(grace, "locations=rw", "nodes=read", "servers=read")
	w.GrantApp(bob, "locations=read")
	w.GrantApp(cleo, "users=rw")
	w.GrantApp(dave, "locations=rw")

	h, err := New(Deps{Store: r.store, BaseURL: "https://panel.example.com", Logger: slog.New(slog.NewTextHandler(r.logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	r.handlers = h
	r.client = w.Serve(t, func(g api.Group) { h.Register(g) })
	return r
}

const base = "/api/application/locations"

func (r *rig) do(user uint64, method, path, body string) apitest.Reply {
	r.t.Helper()
	return r.client.AsApp(user, method, base+path, body)
}

func TestEveryRouteNeedsItsLevel(t *testing.T) {
	routes := []struct {
		name, method, path, body string
		write                    bool
	}{
		{"list", "GET", "", "", false},
		{"view", "GET", "/1", "", false},
		{"create", "POST", "", `{"short":"asia.tyo"}`, true},
		{"update", "PATCH", "/1", `{"short":"us.nyc2"}`, true},
		{"delete", "DELETE", "/2", "", true},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			for user, open := range map[uint64]bool{grace: true, bob: !rt.write, cleo: false, dave: false} {
				r := newRig(t)
				rp := r.do(user, rt.method, rt.path, rt.body)
				if open && (rp.Status < 200 || rp.Status > 299) || !open && rp.Status != 403 {
					t.Errorf("user %d: status = %d: %s", user, rp.Status, rp.Raw)
				}
				if !open && len(r.store.created)+len(r.store.updated)+len(r.store.deleted) != 0 {
					t.Errorf("user %d: something was changed", user)
				}
			}
			r := newRig(t)
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
	r := newRig(t)
	rp := r.do(bob, "GET", "?filter[short]=us&sort=id&per_page=10", "")
	if rp.Status != 200 || rp.At("object") != "list" || rp.At("meta", "pagination", "total") != 2.0 || rp.At("meta", "pagination", "per_page") != 10.0 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	want := map[string]any{"id": 1.0, "short": "us.nyc", "long": "New York", "updated_at": "2026-10-01T10:01:00+00:00", "created_at": "2026-10-01T10:00:00+00:00"}
	if rp.At("data", 0, "object") != "location" || !reflect.DeepEqual(rp.At("data", 0, "attributes"), want) {
		t.Errorf("first = %v", rp.At("data", 0))
	}
	if rp.Has("data", 1, "attributes", "long") != true || rp.At("data", 1, "attributes", "long") != nil {
		t.Errorf("a location without a long name = %v", rp.At("data", 1, "attributes"))
	}
	if len(r.store.lastQuery.Conditions) != 1 || r.store.lastQuery.Conditions[0].Column != "short" || r.store.listPage != [2]int{1, 10} {
		t.Errorf("query = %+v, page = %v", r.store.lastQuery, r.store.listPage)
	}

	rp = r.do(bob, "GET", "/2", "")
	if rp.Status != 200 || rp.At("attributes", "short") != "eu.ams" {
		t.Errorf("view: %d %s", rp.Status, rp.Raw)
	}
	for _, path := range []string{"/99", "/0", "/x"} {
		if rp := r.do(bob, "GET", path, ""); rp.Status != 404 {
			t.Errorf("%s: status = %d", path, rp.Status)
		}
	}
	for _, q := range []string{"?filter[id]=1", "?sort=short", "?per_page=200"} {
		if rp := r.do(bob, "GET", q, ""); rp.Status != 400 && rp.Status != 422 {
			t.Errorf("%s: status = %d", q, rp.Status)
		}
	}
}

func TestIncludes(t *testing.T) {
	r := newRig(t)
	// A key that may read nodes gets them, and the store is asked for them.
	rp := r.do(grace, "GET", "?include=nodes", "")
	if rp.Status != 200 || rp.At("data", 0, "attributes", "relationships", "nodes", "object") != "list" || rp.At("data", 0, "attributes", "relationships", "nodes", "data", 0, "attributes", "name") != "node-7" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if !r.store.lastLoad["nodes"] {
		t.Errorf("load = %v", r.store.lastLoad)
	}
	// The second location has none: an empty list, not a null resource.
	if rp.At("data", 1, "attributes", "relationships", "nodes", "object") != "list" || len(rp.At("data", 1, "attributes", "relationships", "nodes", "data").([]any)) != 0 {
		t.Errorf("empty = %v", rp.At("data", 1, "attributes", "relationships"))
	}
	// A node shown here does not carry the secrets of the node.
	if rp.Contains("daemon_token") || rp.Contains("token_id") {
		t.Errorf("secrets in %s", rp.Raw)
	}

	// A key that may not is told so, and the store is not asked.
	r = newRig(t)
	rp = r.do(bob, "GET", "/1?include=nodes", "")
	if rp.Status != 200 || rp.At("attributes", "relationships", "nodes", "object") != "null_resource" || rp.At("attributes", "relationships", "nodes", "attributes") != nil || len(r.store.lastLoad) != 0 {
		t.Errorf("answer = %d %s, load = %v", rp.Status, rp.Raw, r.store.lastLoad)
	}

	// The servers of a location are shown to a key that may read servers, and a null resource is shown to one that may not.
	r = newRig(t)
	rp = r.do(grace, "GET", "/1?include=servers", "")
	if rp.Status != 200 || rp.At("attributes", "relationships", "servers", "object") != "list" || rp.At("attributes", "relationships", "servers", "data", 0, "attributes", "name") != "Survival" || !r.store.lastLoad["servers"] {
		t.Errorf("servers: %d %s, load = %v", rp.Status, rp.Raw, r.store.lastLoad)
	}
	r = newRig(t)
	if rp = r.do(bob, "GET", "/1?include=servers", ""); rp.At("attributes", "relationships", "servers", "object") != "null_resource" || len(r.store.lastLoad) != 0 {
		t.Errorf("no right to servers: %s, load = %v", rp.Raw, r.store.lastLoad)
	}
	// A name that is not an include is left out.
	for _, q := range []string{"?include=nothing", "?include=nodes.nothing"} {
		if rp = r.do(grace, "GET", "/1"+q, ""); rp.Status != 200 || rp.Has("attributes", "relationships", "nothing") {
			t.Errorf("%s: %d %s", q, rp.Status, rp.Raw)
		}
	}
}

func TestCreate(t *testing.T) {
	r := newRig(t)
	rp := r.do(grace, "POST", "", `{"short":"asia.tyo","long":"Tokyo"}`)
	if rp.Status != 201 || rp.At("object") != "location" || rp.At("attributes", "id") != 101.0 || rp.At("attributes", "long") != "Tokyo" ||
		rp.At("meta", "resource") != "https://panel.example.com/api/application/locations/101" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if !reflect.DeepEqual(r.store.created, []nodes.Location{{Short: "asia.tyo", Long: "Tokyo"}}) {
		t.Errorf("created = %+v", r.store.created)
	}
	// Without a long name, or with a null one, there is none.
	for _, body := range []string{`{"short":"a.b"}`, `{"short":"a.c","long":null}`, `{"short":"a.d","long":""}`} {
		rp := r.do(grace, "POST", "", body)
		if rp.Status != 201 || rp.Has("attributes", "long") != true || rp.At("attributes", "long") != nil {
			t.Errorf("%s: %d %s", body, rp.Status, rp.Raw)
		}
	}
}

func TestCreateRefusals(t *testing.T) {
	tests := []struct {
		name, body string
		status     int
		detail     string
		field      string
	}{
		{"no short name", `{"long":"x"}`, 422, "The short field is required.", "short"},
		{"an empty short name", `{"short":""}`, 422, "The short field is required.", "short"},
		{"a short name that is a number", `{"short":5}`, 422, "The short must be a string.", "short"},
		{"a long name that is a list", `{"short":"a","long":["x"]}`, 422, "The long must be a string.", "long"},
		{"a short name that is too long", `{"short":"` + strings.Repeat("a", 61) + `"}`, 422, "", "short"},
		{"a long name that is too long", `{"short":"a","long":"` + strings.Repeat("a", 192) + `"}`, 422, "", "long"},
		{"a control character", `{"short":"a\u0000b"}`, 422, "", "short"},
		{"a short name that exists", `{"short":"us.nyc"}`, 422, "The short has already been taken.", "short"},
		{"not JSON", `short=a`, 400, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(grace, "POST", "", tt.body)
			if rp.Status != tt.status || (tt.detail != "" && rp.Detail() != tt.detail) {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if tt.field != "" && rp.At("errors", 0, "meta", "source_field") != tt.field {
				t.Errorf("field = %v", rp.At("errors", 0, "meta"))
			}
			if len(r.store.created) != 0 {
				t.Error("a location was made")
			}
		})
	}
}

func TestUpdate(t *testing.T) {
	r := newRig(t)
	// The long name stays as it was when it is not sent.
	rp := r.do(grace, "PATCH", "/1", `{"short":"us.ny"}`)
	if rp.Status != 200 || rp.At("attributes", "short") != "us.ny" || rp.At("attributes", "long") != "New York" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	// It changes when sent, and null or an empty text clears it.
	rp = r.do(grace, "PATCH", "/1", `{"short":"us.ny","long":"NYC"}`)
	if rp.At("attributes", "long") != "NYC" {
		t.Errorf("changed: %s", rp.Raw)
	}
	for _, body := range []string{`{"short":"us.ny","long":null}`, `{"short":"us.ny","long":""}`} {
		rp = r.do(grace, "PATCH", "/1", body)
		if rp.Status != 200 || rp.At("attributes", "long") != nil {
			t.Errorf("%s: %d %s", body, rp.Status, rp.Raw)
		}
		r.store.locations[0].Long = ptr("again")
	}
	// A short name that is the location's own is not a clash.
	if rp := r.do(grace, "PATCH", "/1", `{"short":"us.nyc"}`); rp.Status != 200 {
		t.Errorf("own short name: %d %s", rp.Status, rp.Raw)
	}
}

func TestUpdateRefusals(t *testing.T) {
	tests := []struct {
		name, path, body string
		status           int
		detail           string
	}{
		{"not there", "/99", `{"short":"a"}`, 404, ""},
		{"not there, and a bad body", "/99", `{}`, 404, ""},
		{"no short name", "/1", `{"long":"x"}`, 422, "The short field is required."},
		{"a short name that exists", "/1", `{"short":"eu.ams"}`, 422, "The short has already been taken."},
		{"a long name that is too long", "/1", `{"short":"a","long":"` + strings.Repeat("a", 192) + `"}`, 422, ""},
		{"not JSON", "/1", `x`, 400, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(grace, "PATCH", tt.path, tt.body)
			if rp.Status != tt.status || (tt.detail != "" && rp.Detail() != tt.detail) {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if len(r.store.updated) != 0 {
				t.Error("a location was changed")
			}
		})
	}
}

func TestDelete(t *testing.T) {
	r := newRig(t)
	if rp := r.do(grace, "DELETE", "/2", ""); rp.Status != 204 || len(rp.Raw) != 0 || !reflect.DeepEqual(r.store.deleted, []uint64{2}) {
		t.Errorf("answer = %d %s, deleted %v", rp.Status, rp.Raw, r.store.deleted)
	}
	r.store.hasNodes = 1
	rp := r.do(grace, "DELETE", "/1", "")
	if rp.Status != 400 || rp.Code() != "DisplayException" || rp.Detail() != "Cannot delete a location that has active nodes attached to it." || len(r.store.deleted) != 1 {
		t.Errorf("a location with nodes: %d %s", rp.Status, rp.Raw)
	}
	for _, path := range []string{"/99", "/x"} {
		if rp := r.do(grace, "DELETE", path, ""); rp.Status != 404 {
			t.Errorf("%s: status = %d", path, rp.Status)
		}
	}
}

func TestStoreFailuresAreNotShown(t *testing.T) {
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "", ""}, {"GET", "/1", ""}, {"POST", "", `{"short":"a"}`}, {"PATCH", "/1", `{"short":"a"}`}, {"DELETE", "/1", ""},
	} {
		r := newRig(t)
		r.store.fail = errBoom
		rp := r.do(grace, rt.method, rt.path, rt.body)
		if rp.Status != 500 || rp.Contains("hunter2") {
			t.Errorf("%s %s: %d %s", rt.method, rt.path, rp.Status, rp.Raw)
		}
	}
}

func TestHandlersRefuseWithoutTheAuthorizer(t *testing.T) {
	r := newRig(t)
	client := apitest.NewWorld().Serve(t, func(g api.Group) {
		b := g.Sub("/bare")
		b.GET("", r.handlers.List, api.Public())
		b.GET("/view", r.handlers.View, api.Public())
		b.POST("", r.handlers.Create, api.Public())
		b.PATCH("/update", r.handlers.Update, api.Public())
		b.DELETE("/delete", r.handlers.Delete, api.Public())
	})
	for _, rt := range []struct{ method, path string }{{"GET", "/bare"}, {"GET", "/bare/view"}, {"POST", "/bare"}, {"PATCH", "/bare/update"}, {"DELETE", "/bare/delete"}} {
		if rp := client.As(0, rt.method, rt.path, `{}`); rp.Status != 403 {
			t.Errorf("%s %s: status = %d, want 403", rt.method, rt.path, rp.Status)
		}
	}
	if len(r.store.created)+len(r.store.updated)+len(r.store.deleted) != 0 {
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
		"no store": func(d *Deps) { d.Store = nil }, "no address": func(d *Deps) { d.BaseURL = "" }, "no logger": func(d *Deps) { d.Logger = nil },
	} {
		d := full
		mutate(&d)
		if _, err := New(d); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
