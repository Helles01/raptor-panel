package appallocs

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
)

const (
	grace = 1 // may read and write allocations, and read nodes
	bob   = 2 // may only read allocations
	cleo  = 3 // has other resources only
	dave  = 4 // not an administrator
)

var errBoom = errors.New("the database is down: password=hunter2")

var t0 = time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

type store struct {
	mu sync.Mutex

	// allocations by node
	allocs map[uint64][]appkit.Allocation
	fail   error

	lastLoad  appkit.Includes
	lastQuery listquery.Query
	lastNode  uint64
	listPage  [2]int
	added     []nodes.Allocation
	deleted   []uint64
}

func (s *store) Exists(_ context.Context, id uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	if _, ok := s.allocs[id]; !ok {
		return appkit.ErrNotFound
	}
	return nil
}

func (s *store) List(_ context.Context, node uint64, q listquery.Query, page, perPage int, load appkit.Includes) ([]appkit.Allocation, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastNode, s.lastQuery, s.listPage, s.lastLoad = node, q, [2]int{page, perPage}, load
	if s.fail != nil {
		return nil, 0, s.fail
	}
	list, ok := s.allocs[node]
	if !ok {
		return nil, 0, appkit.ErrNotFound
	}
	out := make([]appkit.Allocation, len(list))
	for i, a := range list {
		if load["node"] {
			a.Node = &appkit.Node{ID: node, UUID: "70000000-0000-4000-8000-000000000000", Settings: nodes.Settings{Name: "node-7"}, CreatedAt: t0, UpdatedAt: t0}
		}
		out[i] = a
	}
	return out, len(out), nil
}

func (s *store) Add(_ context.Context, node uint64, list []nodes.Allocation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	if _, ok := s.allocs[node]; !ok {
		return appkit.ErrNotFound
	}
	s.lastNode = node
	s.added = append(s.added, list...)
	return nil
}

func (s *store) Delete(_ context.Context, node, id uint64, check func(*uint64) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	for _, a := range s.allocs[node] {
		if a.ID == id {
			if err := check(a.ServerID); err != nil {
				return err
			}
			s.deleted = append(s.deleted, id)
			return nil
		}
	}
	return appkit.ErrNotFound
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
	r := &rig{t: t, logs: &bytes.Buffer{}}
	r.store = &store{allocs: map[uint64][]appkit.Allocation{
		7: {
			{ID: 1, NodeID: 7, IP: "10.0.0.1", Port: 25565, Alias: ptr("play.example.com"), Notes: ptr("main"), ServerID: ptr(uint64(500))},
			{ID: 2, NodeID: 7, IP: "10.0.0.1", Port: 25566},
		},
		8: {{ID: 3, NodeID: 8, IP: "10.0.1.1", Port: 25565}},
		9: nil,
	}}

	w := apitest.NewWorld()
	for id, admin := range map[uint64]bool{grace: true, bob: true, cleo: true, dave: false} {
		w.AddUser(id, admin)
	}
	w.GrantApp(grace, "allocations=rw", "nodes=read")
	w.GrantApp(bob, "allocations=read")
	w.GrantApp(cleo, "nodes=rw")
	w.GrantApp(dave, "allocations=rw")

	h, err := New(Deps{Store: r.store, Logger: slog.New(slog.NewTextHandler(r.logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	r.handlers = h
	r.client = w.Serve(t, func(g api.Group) { h.Register(g) })
	return r
}

func (r *rig) do(user uint64, method, path, body string) apitest.Reply {
	r.t.Helper()
	return r.client.AsApp(user, method, "/api/application/nodes"+path, body)
}

const goodBody = `{"ip":"10.0.0.9","ports":["25565","25570-25572"],"alias":"play.example.com"}`

func TestEveryRouteNeedsItsLevel(t *testing.T) {
	routes := []struct {
		name, method, path, body string
		write                    bool
	}{
		{"list", "GET", "/7/allocations", "", false},
		{"add", "POST", "/7/allocations", goodBody, true},
		{"delete", "DELETE", "/7/allocations/2", "", true},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			for user, open := range map[uint64]bool{grace: true, bob: !rt.write, cleo: false, dave: false} {
				r := newRig(t)
				rp := r.do(user, rt.method, rt.path, rt.body)
				if open && (rp.Status < 200 || rp.Status > 299) || !open && rp.Status != 403 {
					t.Errorf("user %d: status = %d: %s", user, rp.Status, rp.Raw)
				}
				if !open && len(r.store.added)+len(r.store.deleted) != 0 {
					t.Errorf("user %d: something was changed", user)
				}
			}
			r := newRig(t)
			if rp := r.do(0, rt.method, rt.path, rt.body); rp.Status != 401 {
				t.Errorf("nobody: status = %d", rp.Status)
			}
			if rp := r.client.As(grace, rt.method, "/api/application/nodes"+rt.path, rt.body); rp.Status != 403 {
				t.Errorf("a client key: status = %d", rp.Status)
			}
		})
	}
}

func TestList(t *testing.T) {
	r := newRig(t)
	rp := r.do(bob, "GET", "/7/allocations?filter[ip]=10.0.0.1&filter[port]=25565&filter[ip_alias]=play&filter[server_id]=500&per_page=5", "")
	if rp.Status != 200 || rp.At("object") != "list" || rp.At("meta", "pagination", "total") != 2.0 || rp.At("meta", "pagination", "per_page") != 5.0 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	want := map[string]any{"id": 1.0, "ip": "10.0.0.1", "alias": "play.example.com", "port": 25565.0, "notes": "main", "assigned": true}
	if rp.At("data", 0, "object") != "allocation" || !reflect.DeepEqual(rp.At("data", 0, "attributes"), want) {
		t.Errorf("first = %v", rp.At("data", 0, "attributes"))
	}
	free := rp.At("data", 1, "attributes").(map[string]any)
	if free["assigned"] != false || free["alias"] != nil || free["notes"] != nil {
		t.Errorf("a free allocation = %v", free)
	}
	if r.store.lastNode != 7 || r.store.listPage != [2]int{1, 5} || len(r.store.lastQuery.Conditions) != 4 {
		t.Errorf("node %d, page %v, query %+v", r.store.lastNode, r.store.listPage, r.store.lastQuery)
	}
	for _, c := range r.store.lastQuery.Conditions {
		switch c.Column {
		case "ip", "port":
			if c.Match != listquery.Exact {
				t.Errorf("%s is not matched exactly", c.Column)
			}
		case "ip_alias":
			if c.Match != listquery.Partial {
				t.Errorf("%s is not matched in part", c.Column)
			}
		case "server_id":
			if c.Match != listquery.Owner {
				t.Errorf("%s is not matched by owner", c.Column)
			}
		}
	}

	// An empty node is an empty list, and the free ones can be asked for.
	if rp := r.do(bob, "GET", "/9/allocations", ""); rp.Status != 200 || rp.At("meta", "pagination", "total") != 0.0 || len(rp.At("data").([]any)) != 0 {
		t.Errorf("empty: %d %s", rp.Status, rp.Raw)
	}
	r.do(bob, "GET", "/7/allocations?filter[server_id]=", "")
	if c := r.store.lastQuery.Conditions; len(c) != 1 || c[0].Match != listquery.Owner || len(c[0].Values) != 0 {
		t.Errorf("free filter = %+v", c)
	}
}

func TestListRefusals(t *testing.T) {
	for name, tt := range map[string]struct {
		path   string
		status int
	}{
		"a node that is not there":      {"/99/allocations", 404},
		"a node that is not a number":   {"/x/allocations", 404},
		"a filter that is not offered":  {"/7/allocations?filter[notes]=x", 400},
		"a sort (none is offered)":      {"/7/allocations?sort=id", 400},
		"a page below one":              {"/7/allocations?page=0", 422},
		"a node that is not there, bad": {"/99/allocations?filter[notes]=x", 404},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			if rp := r.do(bob, "GET", tt.path, ""); rp.Status != tt.status {
				t.Errorf("answer = %d %s", rp.Status, rp.Raw)
			}
		})
	}
}

func TestIncludes(t *testing.T) {
	r := newRig(t)
	rp := r.do(grace, "GET", "/7/allocations?include=node,server", "")
	if rp.Status != 200 || rp.At("data", 0, "relationships", "node", "object") != "node" || rp.At("data", 0, "relationships", "node", "attributes", "name") != "node-7" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if rp.Has("data", 0, "relationships", "server") {
		t.Error("the server is not shown yet")
	}
	if !r.store.lastLoad["node"] {
		t.Errorf("load = %v", r.store.lastLoad)
	}

	r = newRig(t)
	rp = r.do(bob, "GET", "/7/allocations?include=node", "")
	if rp.At("data", 0, "relationships", "node", "object") != "null_resource" || len(r.store.lastLoad) != 0 {
		t.Errorf("answer = %s, load = %v", rp.Raw, r.store.lastLoad)
	}
}

func TestAdd(t *testing.T) {
	r := newRig(t)
	rp := r.do(grace, "POST", "/7/allocations", goodBody)
	if rp.Status != 204 || len(rp.Raw) != 0 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	var want []nodes.Allocation
	for _, p := range []int{25565, 25570, 25571, 25572} {
		want = append(want, nodes.Allocation{IP: "10.0.0.9", Port: p, Alias: "play.example.com"})
	}
	if r.store.lastNode != 7 || !reflect.DeepEqual(r.store.added, want) {
		t.Errorf("node %d, added %+v", r.store.lastNode, r.store.added)
	}

	// A network is every address in it, and the alias may be left out or null.
	r = newRig(t)
	if rp := r.do(grace, "POST", "/7/allocations", `{"ip":"192.0.2.0/30","ports":["30000"],"alias":null}`); rp.Status != 204 || len(r.store.added) != 4 || r.store.added[3].IP != "192.0.2.3" || r.store.added[0].Alias != "" {
		t.Errorf("network: %d %+v", rp.Status, r.store.added)
	}
}

func TestAddRefusals(t *testing.T) {
	tests := []struct {
		name, body string
		status     int
		detail     string
	}{
		{"no address", `{"ports":["25565"]}`, 422, "The ip field is required."},
		{"an address that is a number", `{"ip":5,"ports":["25565"]}`, 422, "The ip must be a string."},
		{"no ports", `{"ip":"10.0.0.9"}`, 422, "The ports field is required."},
		{"an empty list of ports", `{"ip":"10.0.0.9","ports":[]}`, 422, "The ports field is required."},
		{"null ports", `{"ip":"10.0.0.9","ports":null}`, 422, "The ports field is required."},
		{"ports as text", `{"ip":"10.0.0.9","ports":"25565"}`, 422, "The ports must be an array."},
		{"ports as an object", `{"ip":"10.0.0.9","ports":{"a":1}}`, 422, "The ports must be an array."},
		{"a port that is a number", `{"ip":"10.0.0.9","ports":["25565",25566]}`, 422, "The ports.1 must be a string."},
		{"an alias that is a list", `{"ip":"10.0.0.9","ports":["25565"],"alias":["x"]}`, 422, "The alias must be a string."},
		{"an alias that is too long", `{"ip":"10.0.0.9","ports":["25565"],"alias":"` + strings.Repeat("a", 192) + `"}`, 422, ""},
		{"an alias with a space", `{"ip":"10.0.0.9","ports":["25565"],"alias":"a b"}`, 400, "The alias must be at most 191 letters, digits, dots, dashes, underscores or colons."},
		{"a name for an address", `{"ip":"play.example.com","ports":["25565"]}`, 400, nodes.ErrNotAnIP.Error()},
		{"a network that is too wide", `{"ip":"10.0.0.0/8","ports":["25565"]}`, 400, nodes.ErrCIDR.Error()},
		{"a port below the floor", `{"ip":"10.0.0.9","ports":["80"]}`, 400, nodes.ErrPortRange.Error()},
		{"a port above the ceiling", `{"ip":"10.0.0.9","ports":["65536"]}`, 400, nodes.ErrPortRange.Error()},
		{"a range that is too long", `{"ip":"10.0.0.9","ports":["20000-22000"]}`, 400, nodes.ErrTooManyPorts.Error()},
		{"a port that is no port", `{"ip":"10.0.0.9","ports":["http"]}`, 400, "The mapping provided for http was invalid and could not be processed."},
		{"more than a request may hold", `{"ip":"10.0.0.0/25","ports":["20000-20999","22000-22999"]}`, 400, nodes.ErrTooMany.Error()},
		{"not JSON", `ip=1`, 400, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(grace, "POST", "/7/allocations", tt.body)
			if rp.Status != tt.status || (tt.detail != "" && rp.Detail() != tt.detail) {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if tt.status == 400 && tt.detail != "" && rp.Code() != "DisplayException" {
				t.Errorf("code = %s", rp.Code())
			}
			if len(r.store.added) != 0 {
				t.Error("allocations were added")
			}
		})
	}
	// The node is looked for first, as the reference binds the route before it reads the body.
	r := newRig(t)
	if rp := r.do(grace, "POST", "/99/allocations", `{}`); rp.Status != 404 {
		t.Errorf("unknown node: %d", rp.Status)
	}
}

func TestDelete(t *testing.T) {
	r := newRig(t)
	if rp := r.do(grace, "DELETE", "/7/allocations/2", ""); rp.Status != 204 || len(rp.Raw) != 0 || !reflect.DeepEqual(r.store.deleted, []uint64{2}) {
		t.Errorf("answer = %d %s, deleted %v", rp.Status, rp.Raw, r.store.deleted)
	}
	rp := r.do(grace, "DELETE", "/7/allocations/1", "")
	if rp.Status != 400 || rp.Code() != "DisplayException" || rp.Detail() != nodes.ErrAllocationInUse.Error() || len(r.store.deleted) != 1 {
		t.Errorf("an allocation in use: %d %s", rp.Status, rp.Raw)
	}
	// An allocation of another node is not found on this one.
	for _, path := range []string{"/7/allocations/3", "/99/allocations/2", "/7/allocations/99", "/7/allocations/x", "/x/allocations/2"} {
		if rp := r.do(grace, "DELETE", path, ""); rp.Status != 404 {
			t.Errorf("%s: status = %d", path, rp.Status)
		}
	}
}

func TestStoreFailuresAreNotShown(t *testing.T) {
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "/7/allocations", ""}, {"POST", "/7/allocations", goodBody}, {"DELETE", "/7/allocations/2", ""},
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
		b.POST("", r.handlers.Add, api.Public())
		b.DELETE("/delete", r.handlers.Delete, api.Public())
	})
	for _, rt := range []struct{ method, path string }{{"GET", "/bare"}, {"POST", "/bare"}, {"DELETE", "/bare/delete"}} {
		if rp := client.As(0, rt.method, rt.path, `{}`); rp.Status != 403 {
			t.Errorf("%s %s: status = %d, want 403", rt.method, rt.path, rp.Status)
		}
	}
	if len(r.store.added)+len(r.store.deleted) != 0 {
		t.Error("something was changed")
	}
}

func TestNewNeedsEverything(t *testing.T) {
	r := newRig(t)
	full := r.handlers.d
	if _, err := New(full); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Deps){"no store": func(d *Deps) { d.Store = nil }, "no logger": func(d *Deps) { d.Logger = nil }} {
		d := full
		mutate(&d)
		if _, err := New(d); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
