package allocapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apitest"
	"github.com/Helles01/raptor-panel/internal/api/clientapi"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
)

const (
	ada   = 1 // owner
	grace = 2 // administrator
	bob   = 3 // holds all the allocation rights
	cleo  = 4 // may only read
	eve   = 5 // a stranger
)

const serverUUID = "aaaa1111-2222-4333-8444-555555555555"

var errBoom = errors.New("the database is down: password=hunter2")

func s(v string) *string { return &v }

type store struct {
	mu sync.Mutex

	allocs  []clientapi.Allocation // the server's own
	primary uint64
	limit   *int64
	free    []clientapi.Allocation // free allocations on the server's address
	taken   []int                  // ports in use on the node in the range, besides the server's
	nextID  uint64
	fail    error
	release bool // Release answers false

	assigned []AssignRequest
	notes    []*string
	primaryC []uint64
	released []uint64
}

func (st *store) List(_ context.Context, _ uint64) ([]clientapi.Allocation, uint64, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]clientapi.Allocation(nil), st.allocs...), st.primary, st.fail
}

func (st *store) Limit(_ context.Context, _ uint64) (*int64, uint64, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.limit, st.primary, st.fail
}

func (st *store) Find(_ context.Context, _, id uint64) (clientapi.Allocation, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.fail != nil {
		return clientapi.Allocation{}, st.fail
	}
	for _, a := range st.allocs {
		if a.ID == id {
			return a, nil
		}
	}
	return clientapi.Allocation{}, ErrNotFound
}

func (st *store) Assign(_ context.Context, r AssignRequest) (clientapi.Allocation, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.assigned = append(st.assigned, r)
	if st.fail != nil {
		return clientapi.Allocation{}, st.fail
	}
	if err := r.Check(st.limit, int64(len(st.allocs))); err != nil {
		return clientapi.Allocation{}, err
	}
	var a clientapi.Allocation
	if len(st.free) > 0 {
		a, st.free = st.free[0], st.free[1:]
	} else {
		used := append([]int(nil), st.taken...)
		for _, x := range st.allocs {
			used = append(used, x.Port)
		}
		port, err := r.Port(used)
		if err != nil {
			return clientapi.Allocation{}, err
		}
		st.nextID++
		a = clientapi.Allocation{ID: st.nextID, IP: "10.0.0.5", Port: port}
	}
	st.allocs = append(st.allocs, a)
	return a, nil
}

func (st *store) SetNotes(_ context.Context, _, id uint64, notes *string) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.notes = append(st.notes, notes)
	for i := range st.allocs {
		if st.allocs[i].ID == id {
			st.allocs[i].Notes = notes
		}
	}
	return st.fail
}

func (st *store) SetPrimary(_ context.Context, _, id uint64) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.primaryC = append(st.primaryC, id)
	st.primary = id
	return st.fail
}

func (st *store) Release(_ context.Context, _, id uint64) (bool, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.released = append(st.released, id)
	return !st.release, st.fail
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
	activity *activityLog
	logs     *bytes.Buffer
	handlers *Handlers
	// policy is what the handlers are told at each request; a test changes it between two requests, as a command line change of a setting would.
	policy      Policy
	policyErr   error
	policyReads int
}

type options struct {
	disabled bool
	start    int
	end      int
}

func newRig(t *testing.T, opts ...func(*options)) *rig {
	t.Helper()
	o := options{start: 25565, end: 25575}
	for _, f := range opts {
		f(&o)
	}
	limit := int64(5)
	r := &rig{t: t, activity: &activityLog{}, logs: &bytes.Buffer{}, policy: Policy{Enabled: !o.disabled, RangeStart: o.start, RangeEnd: o.end}}
	r.store = &store{
		allocs: []clientapi.Allocation{
			{ID: 1, IP: "10.0.0.5", Port: 25565, Notes: s("main")},
			{ID: 2, IP: "10.0.0.5", Port: 25566},
			{ID: 3, IP: "10.0.0.5", Port: 25567, IPAlias: s("play.example.com")},
		},
		primary: 1, limit: &limit, nextID: 100, taken: []int{25570},
	}

	w := apitest.NewWorld()
	for id, admin := range map[uint64]bool{ada: false, grace: true, bob: false, cleo: false, eve: false} {
		w.AddUser(id, admin)
	}
	w.AddServer(100, serverUUID, ada)
	w.AddServer(101, "bbbb2222-2222-4333-8444-555555555555", ada)
	w.Subusers[[2]uint64{100, bob}] = []string{"allocation.read", "allocation.create", "allocation.update", "allocation.delete", "websocket.connect"}
	w.Subusers[[2]uint64{100, cleo}] = []string{"allocation.read", "websocket.connect"}

	limiter, err := middleware.NewLimiter(AddLimit, AddWindow)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Deps{
		Store: r.store, Activity: r.activity, AddLimiter: limiter, Policy: func(context.Context) (Policy, error) { r.policyReads++; return r.policy, r.policyErr },
		Logger: slog.New(slog.NewTextHandler(r.logs, nil)), Random: bytes.NewReader(make([]byte, 1024)),
	})
	if err != nil {
		t.Fatal(err)
	}
	r.handlers = h
	r.client = w.Serve(t, func(g api.Group) { h.Register(g) })
	return r
}

const base = "/api/client/servers/aaaa1111/network/allocations"

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
	l, _ := rp.At("data").([]any)
	if len(l) != 3 {
		t.Fatalf("%d allocations: %s", len(l), rp.Raw)
	}
	want := map[string]any{"id": 1.0, "ip": "10.0.0.5", "ip_alias": nil, "port": 25565.0, "notes": "main", "is_default": true}
	if rp.At("data", 0, "object") != "allocation" || !reflect.DeepEqual(rp.At("data", 0, "attributes"), want) {
		t.Errorf("first = %v", rp.At("data", 0))
	}
	if rp.At("data", 1, "attributes", "is_default") != false || rp.At("data", 2, "attributes", "ip_alias") != "play.example.com" {
		t.Errorf("data = %s", rp.Raw)
	}

	// The default follows the primary allocation of the server.
	r.store.primary = 3
	rp = r.do(ada, "GET", "", "")
	if rp.At("data", 0, "attributes", "is_default") != false || rp.At("data", 2, "attributes", "is_default") != true {
		t.Errorf("data = %s", rp.Raw)
	}
}

func TestEveryRouteHasItsPermission(t *testing.T) {
	routes := []struct {
		name, method, path, body string
		allowed                  []uint64
	}{
		{"list", "GET", "", "", []uint64{bob, cleo}},
		{"add", "POST", "", "", []uint64{bob}},
		{"notes", "POST", "/2", `{"notes":"x"}`, []uint64{bob}},
		{"primary", "POST", "/2/primary", "", []uint64{bob}},
		{"delete", "DELETE", "/2", "", []uint64{bob}},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			for _, user := range []uint64{ada, grace, bob, cleo} {
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

func TestAddTakesAFreeAllocation(t *testing.T) {
	r := newRig(t)
	r.store.free = []clientapi.Allocation{{ID: 50, IP: "10.0.0.5", Port: 25580}}

	rp := r.do(ada, "POST", "", "")
	if rp.Status != 200 || rp.At("attributes", "id") != 50.0 || rp.At("attributes", "is_default") != false || rp.At("attributes", "port") != 25580.0 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if len(r.activity.entries) != 1 {
		t.Fatalf("entries = %+v", r.activity.entries)
	}
	keyID := uint64(ada)
	want := Entry{Event: "server:allocation.create", ServerID: 100, AllocationID: 50, ActorID: ada, APIKeyID: &keyID, IP: "203.0.113.9", Properties: `{"allocation":"10.0.0.5:25580"}`}
	if !reflect.DeepEqual(r.activity.entries[0], want) {
		t.Errorf("entry = %+v, want %+v", r.activity.entries[0], want)
	}
}

func TestAddMakesANewAllocationInTheRange(t *testing.T) {
	r := newRig(t)
	rp := r.do(ada, "POST", "", "")
	if rp.Status != 200 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	// With the random source at zero, the first port of the range that is free: 25565 to 25567 are the server's, 25570 is taken.
	if rp.At("attributes", "port") != 25568.0 {
		t.Errorf("port = %v", rp.At("attributes", "port"))
	}
	if len(r.store.allocs) != 4 {
		t.Errorf("%d allocations", len(r.store.allocs))
	}
}

func TestAddRefusals(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*rig)
		opts    []func(*options)
		detail  string
	}{
		{"the switch is off", nil, []func(*options){func(o *options) { o.disabled = true }}, "Server auto-allocation is not enabled for this instance."},
		{"the limit is reached", func(r *rig) { l := int64(3); r.store.limit = &l }, nil, "Cannot assign additional allocations to this server: limit has been reached."},
		{"no limit is set", func(r *rig) { r.store.limit = nil }, nil, "Cannot assign additional allocations to this server: limit has been reached."},
		{"a limit of nothing", func(r *rig) { l := int64(0); r.store.limit = &l }, nil, "Cannot assign additional allocations to this server: limit has been reached."},
		{"no range was set", nil, []func(*options){func(o *options) { o.start, o.end = 0, 0 }}, "Cannot assign additional allocation: no more space available on node."},
		{"the range is full", func(r *rig) { r.store.taken = []int{25568, 25569, 25570, 25571, 25572, 25573, 25574, 25575} }, nil, "Cannot assign additional allocation: no more space available on node."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, tt.opts...)
			if tt.prepare != nil {
				tt.prepare(r)
			}
			rp := r.do(ada, "POST", "", "")
			if rp.Status != 400 || rp.Code() != "DisplayException" || rp.Detail() != tt.detail {
				t.Errorf("answer = %d %s", rp.Status, rp.Raw)
			}
			if len(r.store.allocs) != 3 || len(r.activity.entries) != 0 {
				t.Error("an allocation was added or an entry written")
			}
		})
	}
}

func TestAddIsThrottledForEachServer(t *testing.T) {
	r := newRig(t)
	for i := 0; i < AddLimit; i++ {
		if rp := r.do(ada, "POST", "", ""); rp.Status != 200 {
			t.Fatalf("addition %d: status = %d: %s", i+1, rp.Status, rp.Raw)
		}
	}
	if rp := r.do(ada, "POST", "", ""); rp.Status != 429 || rp.Header.Get("Retry-After") == "" {
		t.Errorf("the third: %d, Retry-After %q", rp.Status, rp.Header.Get("Retry-After"))
	}
	if len(r.store.allocs) != 5 {
		t.Errorf("%d allocations", len(r.store.allocs))
	}
	// Another server has its own allowance.
	more := int64(10)
	r.store.limit = &more
	if rp := r.client.As(ada, "POST", "/api/client/servers/bbbb2222/network/allocations", ""); rp.Status != 200 {
		t.Errorf("another server: status = %d: %s", rp.Status, rp.Raw)
	}
}

func TestRefusedCallsDoNotUseUpTheAllowance(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 10; i++ {
		r.do(eve, "POST", "", "")
		r.do(cleo, "POST", "", "")
		r.do(0, "POST", "", "")
	}
	if rp := r.do(ada, "POST", "", ""); rp.Status != 200 {
		t.Errorf("status = %d", rp.Status)
	}
}

func TestSetNotes(t *testing.T) {
	r := newRig(t)
	rp := r.do(ada, "POST", "/2", `{"notes":"  for the map  "}`)
	if rp.Status != 200 || rp.At("attributes", "notes") != "for the map" || rp.At("attributes", "id") != 2.0 || rp.At("attributes", "is_default") != false {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:allocation.notes" ||
		r.activity.entries[0].Properties != `{"allocation":"10.0.0.5:25566","new":"for the map","old":null}` || r.activity.entries[0].AllocationID != 2 {
		t.Errorf("entries = %+v", r.activity.entries)
	}

	// The primary one says so.
	if rp := r.do(ada, "POST", "/1", `{"notes":"main"}`); rp.At("attributes", "is_default") != true {
		t.Errorf("answer = %s", rp.Raw)
	}
}

func TestSetNotesThatChangeNothing(t *testing.T) {
	r := newRig(t)
	if rp := r.do(ada, "POST", "/1", `{"notes":"main"}`); rp.Status != 200 {
		t.Fatalf("status = %d", rp.Status)
	}
	if rp := r.do(ada, "POST", "/2", `{"notes":null}`); rp.Status != 200 || rp.At("attributes", "notes") != nil {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if rp := r.do(ada, "POST", "/2", `{"notes":"   "}`); rp.Status != 200 {
		t.Fatalf("status = %d", rp.Status)
	}
	if len(r.activity.entries) != 0 {
		t.Errorf("entries = %+v: nothing changed", r.activity.entries)
	}
	if rp := r.do(ada, "POST", "/1", `{"notes":null}`); rp.Status != 200 || len(r.activity.entries) != 1 ||
		r.activity.entries[0].Properties != `{"allocation":"10.0.0.5:25565","new":null,"old":"main"}` {
		t.Errorf("clearing a note: entries = %+v", r.activity.entries)
	}
}

func TestSetNotesRefusals(t *testing.T) {
	for name, tt := range map[string]struct {
		path, body string
		status     int
		detail     string
	}{
		"not sent":                        {"/2", `{}`, 422, "The notes field must be present."},
		"not text":                        {"/2", `{"notes":5}`, 422, "The notes must be a string."},
		"too long":                        {"/2", `{"notes":"` + strings.Repeat("a", 192) + `"}`, 422, "The notes may not be greater than 191 characters."},
		"control characters":              {"/2", `{"notes":"a\u0000b"}`, 422, "The notes must not contain control characters."},
		"not JSON":                        {"/2", `notes`, 400, ""},
		"an allocation of another server": {"/77", `{"notes":"x"}`, 404, ""},
		"not a number":                    {"/abc", `{"notes":"x"}`, 404, ""},
		"zero":                            {"/0", `{"notes":"x"}`, 404, ""},
		"negative":                        {"/-1", `{"notes":"x"}`, 404, ""},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(ada, "POST", tt.path, tt.body)
			if rp.Status != tt.status || (tt.detail != "" && rp.Detail() != tt.detail) {
				t.Errorf("answer = %d %s", rp.Status, rp.Raw)
			}
			if len(r.store.notes) != 0 || len(r.activity.entries) != 0 {
				t.Error("a note was stored or logged")
			}
		})
	}
}

func TestSetPrimary(t *testing.T) {
	r := newRig(t)
	rp := r.do(ada, "POST", "/3/primary", "")
	if rp.Status != 200 || rp.At("attributes", "id") != 3.0 || rp.At("attributes", "is_default") != true {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if !reflect.DeepEqual(r.store.primaryC, []uint64{3}) {
		t.Errorf("primary = %v", r.store.primaryC)
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:allocation.primary" || r.activity.entries[0].Properties != `{"allocation":"10.0.0.5:25567"}` ||
		r.activity.entries[0].AllocationID != 3 {
		t.Errorf("entries = %+v", r.activity.entries)
	}
	for _, path := range []string{"/77/primary", "/x/primary"} {
		if rp := r.do(ada, "POST", path, ""); rp.Status != 404 {
			t.Errorf("%s: status = %d", path, rp.Status)
		}
	}
	if len(r.store.primaryC) != 1 {
		t.Errorf("primary = %v", r.store.primaryC)
	}
}

func TestDelete(t *testing.T) {
	r := newRig(t)
	rp := r.do(ada, "DELETE", "/2", "")
	if rp.Status != 204 || len(rp.Raw) != 0 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if !reflect.DeepEqual(r.store.released, []uint64{2}) {
		t.Errorf("released %v", r.store.released)
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:allocation.delete" || r.activity.entries[0].Properties != `{"allocation":"10.0.0.5:25566"}` {
		t.Errorf("entries = %+v", r.activity.entries)
	}
}

func TestDeleteRefusals(t *testing.T) {
	zero := int64(0)
	tests := []struct {
		name    string
		path    string
		prepare func(*rig)
		status  int
		detail  string
	}{
		{"the primary allocation", "/1", nil, 400, "You cannot delete the primary allocation for this server."},
		{"no limit set", "/2", func(r *rig) { r.store.limit = nil }, 400, "You cannot delete allocations for this server: no allocation limit is set."},
		{"a limit of nothing", "/2", func(r *rig) { r.store.limit = &zero }, 400, "You cannot delete allocations for this server: no allocation limit is set."},
		{"an allocation of another server", "/77", nil, 404, ""},
		{"not a number", "/abc", nil, 404, ""},
		{"it was taken meanwhile", "/2", func(r *rig) { r.store.release = true }, 409, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			if tt.prepare != nil {
				tt.prepare(r)
			}
			rp := r.do(ada, "DELETE", tt.path, "")
			if rp.Status != tt.status || (tt.detail != "" && rp.Detail() != tt.detail) {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if len(r.activity.entries) != 0 {
				t.Error("an entry was written for an allocation that was not given up")
			}
			if tt.status == 400 && len(r.store.released) != 0 {
				t.Error("the store was asked to release")
			}
		})
	}
}

func TestStoreFailuresDoNotLeak(t *testing.T) {
	for name, call := range map[string]func(*rig) apitest.Reply{
		"list":    func(r *rig) apitest.Reply { return r.do(ada, "GET", "", "") },
		"add":     func(r *rig) apitest.Reply { return r.do(ada, "POST", "", "") },
		"notes":   func(r *rig) apitest.Reply { return r.do(ada, "POST", "/2", `{"notes":"x"}`) },
		"primary": func(r *rig) apitest.Reply { return r.do(ada, "POST", "/2/primary", "") },
		"delete":  func(r *rig) apitest.Reply { return r.do(ada, "DELETE", "/2", "") },
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
	if rp := r.do(ada, "POST", "/3/primary", ""); rp.Status != 200 || len(r.store.primaryC) != 1 {
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
		b.POST("", r.handlers.Add, api.Public())
		b.POST("/:allocation", r.handlers.SetNotes, api.Public())
		b.POST("/:allocation/primary", r.handlers.SetPrimary, api.Public())
		b.DELETE("/:allocation", r.handlers.Delete, api.Public())
	})
	for _, rt := range []struct{ method, path string }{{"GET", "/bare"}, {"POST", "/bare"}, {"POST", "/bare/2"}, {"POST", "/bare/2/primary"}, {"DELETE", "/bare/2"}} {
		if rp := client.As(0, rt.method, rt.path, `{"notes":"x"}`); rp.Status != 403 {
			t.Errorf("%s %s: status = %d, want 403", rt.method, rt.path, rp.Status)
		}
	}
	if len(r.store.assigned)+len(r.store.notes)+len(r.store.released) != 0 {
		t.Error("the store was asked")
	}
}

func TestNewNeedsEverything(t *testing.T) {
	r := newRig(t)
	full := r.handlers.d
	if _, err := New(full); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Deps){
		"no store": func(d *Deps) { d.Store = nil }, "no activity": func(d *Deps) { d.Activity = nil },
		"no limiter": func(d *Deps) { d.AddLimiter = nil }, "no logger": func(d *Deps) { d.Logger = nil },
	} {
		d := full
		mutate(&d)
		if _, err := New(d); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// What the administrator sets is read at each request: a setting changed from the command line applies to a panel that is running.
func TestAddFollowsTheSettingsAtEachRequest(t *testing.T) {
	r := newRig(t, func(o *options) { o.disabled = true })
	if rp := r.do(ada, "POST", "", ""); rp.Status != 400 || rp.Detail() != "Server auto-allocation is not enabled for this instance." {
		t.Fatalf("switched off: %d %s", rp.Status, rp.Raw)
	}

	r.policy.Enabled = true
	rp := r.do(ada, "POST", "", "")
	if rp.Status != 200 || rp.At("attributes", "port") != 25568.0 {
		t.Fatalf("switched on: %d %s", rp.Status, rp.Raw)
	}
	if r.policyReads != 2 {
		t.Errorf("the policy was read %d times for two requests", r.policyReads)
	}

	// A new range counts from the next request on, too.
	r2 := newRig(t)
	r2.policy.RangeStart, r2.policy.RangeEnd = 30000, 30000
	if rp := r2.do(ada, "POST", "", ""); rp.Status != 200 || rp.At("attributes", "port") != 30000.0 {
		t.Errorf("the new range: %d %s", rp.Status, rp.Raw)
	}
}

// A request that was refused by the limiter does not read the settings, and one that cannot read them adds nothing.
func TestAddWhenTheSettingsCannotBeRead(t *testing.T) {
	r := newRig(t)
	r.policyErr = errors.New("the database is away: password=hunter2")
	rp := r.do(ada, "POST", "", "")
	if rp.Status != 500 || strings.Contains(string(rp.Raw), "hunter2") {
		t.Errorf("answer = %d %s", rp.Status, rp.Raw)
	}
	if len(r.store.allocs) != 3 || len(r.activity.entries) != 0 {
		t.Error("an allocation was added or an entry written")
	}

	r = newRig(t)
	for i := 0; i < AddLimit; i++ {
		r.do(ada, "POST", "", "")
	}
	reads := r.policyReads
	if rp := r.do(ada, "POST", "", ""); rp.Status != 429 || r.policyReads != reads {
		t.Errorf("throttled: %d, reads %d -> %d", rp.Status, reads, r.policyReads)
	}
}

func TestNewNeedsThePolicy(t *testing.T) {
	limiter, err := middleware.NewLimiter(AddLimit, AddWindow)
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(Deps{Store: &store{}, Activity: &activityLog{}, AddLimiter: limiter, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err == nil {
		t.Error("handlers without a policy")
	}
}
