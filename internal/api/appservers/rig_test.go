package appservers

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apitest"
	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/controlapi"
	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/dbhosts"
	"github.com/Helles01/raptor-panel/internal/servers"
	"github.com/Helles01/raptor-panel/internal/wings"
)

const (
	grace = 1 // may read and write servers, and read users, nodes, allocations, nests, eggs, locations and server databases
	bob   = 2 // may only read servers
	cleo  = 3 // has other resources only
	dave  = 4 // not an administrator
)

var errBoom = errors.New("the database is down: password=hunter2")

var t0 = time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

var node = wings.Node{Scheme: "https", FQDN: "node1.example.com", Port: 8080, Token: strings.Repeat("a", 64)}

const (
	uuid1 = "aaaa1111-2222-4333-8444-555555555555"
	uuid2 = "bbbb2222-2222-4333-8444-555555555555"
)

type allocRow struct {
	NodeID   uint64
	ServerID *uint64
	IP       string
	Port     int
	Notes    *string
}

type record struct {
	server appkit.Server
	values map[string]string
}

type update struct {
	ID      uint64
	Details servers.Details
	Revoke  uint64
}

type startupCall struct {
	ID       uint64
	Settings servers.StartupSettings
	NestID   uint64
	Values   []servers.VariableValue
}

type statusCall struct {
	ID       uint64
	From, To string
}

type store struct {
	mu sync.Mutex

	servers map[uint64]*record
	allocs  map[uint64]*allocRow
	eggs    map[uint64]EggInfo
	owners  map[uint64]bool
	dbs     map[uint64][]appkit.ServerDatabase
	usage   []servers.NodeUsage
	nextID  uint64

	fail        error
	transfer    bool
	statusBusy  bool // SetStatus answers false
	uuidTaken   int  // how many times Create answers "uuid taken" first
	taken       string
	createCheck bool

	lastLoad  appkit.Includes
	lastQuery listquery.Query
	listPage  [2]int
	updates   []update
	builds    []BuildChange
	startups  []startupCall
	creates   []CreateRequest
	statuses  []statusCall
	deletedDB []uint64
	deleted   []uint64
}

func (s *store) fill(r *record, load appkit.Includes) appkit.Server {
	srv := r.server
	if load["allocations"] {
		var ids []uint64
		for id, a := range s.allocs {
			if a.ServerID != nil && *a.ServerID == srv.ID {
				ids = append(ids, id)
			}
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			a := s.allocs[id]
			srv.Allocations = append(srv.Allocations, appkit.Allocation{ID: id, NodeID: a.NodeID, IP: a.IP, Port: a.Port, ServerID: a.ServerID})
		}
	}
	if load["user"] {
		srv.User = &appkit.User{ID: srv.OwnerID, Username: "ann", CreatedAt: t0, UpdatedAt: t0}
	}
	if load["node"] {
		srv.Node = &appkit.Node{ID: srv.NodeID, UUID: "70000000-0000-4000-8000-000000000000", CreatedAt: t0, UpdatedAt: t0}
	}
	if load["databases"] {
		srv.Databases = s.dbs[srv.ID]
	}
	if load["variables"] {
		srv.Variables = []appkit.ServerVariable{{Variable: appkit.EggVariable{ID: 100, EggID: srv.EggID, EnvVariable: "SERVER_JARFILE"}, ServerValue: ptr("paper.jar")}}
	}
	return srv
}

func (s *store) List(_ context.Context, q listquery.Query, page, perPage int, load appkit.Includes) ([]appkit.Server, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastQuery, s.listPage, s.lastLoad = q, [2]int{page, perPage}, load
	if s.fail != nil {
		return nil, 0, s.fail
	}
	var ids []uint64
	for id := range s.servers {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]appkit.Server, len(ids))
	for i, id := range ids {
		out[i] = s.fill(s.servers[id], load)
	}
	return out, len(out), nil
}

func (s *store) Find(_ context.Context, id uint64, load appkit.Includes) (appkit.Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastLoad = load
	if s.fail != nil {
		return appkit.Server{}, s.fail
	}
	if r, ok := s.servers[id]; ok {
		return s.fill(r, load), nil
	}
	return appkit.Server{}, appkit.ErrNotFound
}

func (s *store) FindExternal(_ context.Context, ext string, load appkit.Includes) (appkit.Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastLoad = load
	if s.fail != nil {
		return appkit.Server{}, s.fail
	}
	for _, r := range s.servers {
		if r.server.ExternalID != nil && *r.server.ExternalID == ext {
			return s.fill(r, load), nil
		}
	}
	return appkit.Server{}, appkit.ErrNotFound
}

func (s *store) UpdateDetails(_ context.Context, id uint64, d servers.Details, revoke uint64) (appkit.Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return appkit.Server{}, s.fail
	}
	r, ok := s.servers[id]
	if !ok {
		return appkit.Server{}, appkit.ErrNotFound
	}
	if !s.owners[d.OwnerID] {
		return appkit.Server{}, ErrNoOwner
	}
	if d.ExternalID != nil {
		for oid, o := range s.servers {
			if oid != id && o.server.ExternalID != nil && *o.server.ExternalID == *d.ExternalID {
				return appkit.Server{}, &appkit.TakenError{Field: "external_id"}
			}
		}
	}
	s.updates = append(s.updates, update{id, d, revoke})
	r.server.ExternalID, r.server.OwnerID, r.server.Config.Name, r.server.Config.Description = d.ExternalID, d.OwnerID, d.Name, d.Description
	r.server.UpdatedAt = t0.Add(time.Hour)
	return r.server, nil
}

func (s *store) UpdateBuild(_ context.Context, id uint64, req BuildRequest) (appkit.Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return appkit.Server{}, s.fail
	}
	r, ok := s.servers[id]
	if !ok {
		return appkit.Server{}, appkit.ErrNotFound
	}
	rows := map[uint64]servers.AllocationRow{}
	for _, aid := range append(append([]uint64{}, req.Rows...), r.server.AllocationID) {
		if a, ok := s.allocs[aid]; ok {
			rows[aid] = servers.AllocationRow{ID: aid, NodeID: a.NodeID, ServerID: a.ServerID}
		}
	}
	ch, err := req.Change(r.server, rows)
	if err != nil {
		return appkit.Server{}, err
	}
	s.builds = append(s.builds, ch)
	c := &r.server.Config
	c.Memory, c.Swap, c.Disk, c.IO, c.CPU, c.Threads, c.OOMDisabled = ch.Limits.Memory, ch.Limits.Swap, ch.Limits.Disk, ch.Limits.IO, ch.Limits.CPU, ch.Limits.Threads, ch.OOMDisabled
	r.server.DatabaseLimit, c.AllocationLimit, r.server.BackupLimit = ch.Features.Databases, ch.Features.Allocations, ch.Features.Backups
	for _, aid := range ch.Plan.Assign {
		sid := id
		s.allocs[aid].ServerID, s.allocs[aid].Notes = &sid, nil
	}
	for _, aid := range ch.Plan.Release {
		s.allocs[aid].ServerID, s.allocs[aid].Notes = nil, nil
	}
	r.server.AllocationID = ch.Plan.Default
	return r.server, nil
}

func (s *store) Egg(_ context.Context, id uint64) (EggInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return EggInfo{}, s.fail
	}
	if e, ok := s.eggs[id]; ok {
		return e, nil
	}
	return EggInfo{}, ErrNoEgg
}

func (s *store) UpdateStartup(_ context.Context, id uint64, st servers.StartupSettings, nestID uint64, values []servers.VariableValue) (appkit.Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return appkit.Server{}, s.fail
	}
	r, ok := s.servers[id]
	if !ok {
		return appkit.Server{}, appkit.ErrNotFound
	}
	s.startups = append(s.startups, startupCall{id, st, nestID, values})
	r.server.Config.Startup, r.server.EggID, r.server.NestID, r.server.Config.Image, r.server.Config.SkipScripts = st.Startup, st.EggID, nestID, st.Image, st.SkipScripts
	for _, v := range values {
		r.values[v.EnvName] = v.Store
	}
	return r.server, nil
}

func (s *store) Usage(context.Context) ([]servers.NodeUsage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usage, s.fail
}

func (s *store) Allocations(_ context.Context, nodeIDs []uint64) ([]servers.Candidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return nil, s.fail
	}
	var ids []uint64
	for id := range s.allocs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var out []servers.Candidate
	for _, id := range ids {
		a := s.allocs[id]
		for _, n := range nodeIDs {
			if a.NodeID == n {
				out = append(out, servers.Candidate{ID: id, NodeID: a.NodeID, IP: a.IP, Port: a.Port, Used: a.ServerID != nil})
			}
		}
	}
	return out, nil
}

func (s *store) Create(_ context.Context, req CreateRequest) (appkit.Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return appkit.Server{}, s.fail
	}
	if s.uuidTaken > 0 {
		s.uuidTaken--
		s.creates = append(s.creates, req)
		return appkit.Server{}, &appkit.TakenError{Field: "uuid"}
	}
	rows := map[uint64]servers.AllocationRow{}
	for _, aid := range req.Allocations {
		if a, ok := s.allocs[aid]; ok {
			rows[aid] = servers.AllocationRow{ID: aid, NodeID: a.NodeID, ServerID: a.ServerID}
		}
	}
	if err := req.Check(rows); err != nil {
		return appkit.Server{}, err
	}
	if !s.owners[req.Server.OwnerID] {
		return appkit.Server{}, ErrNoOwner
	}
	if _, ok := s.eggs[req.Server.EggID]; !ok {
		return appkit.Server{}, ErrNoEgg
	}
	if s.taken != "" {
		return appkit.Server{}, &appkit.TakenError{Field: s.taken}
	}
	s.creates = append(s.creates, req)
	s.nextID++
	id := s.nextID
	ns := req.Server
	srv := appkit.Server{
		ID: id, ExternalID: ns.ExternalID, ShortID: req.ShortID, OwnerID: ns.OwnerID, NodeID: s.allocs[ns.Allocation].NodeID, AllocationID: ns.Allocation, NestID: req.NestID, EggID: ns.EggID,
		DatabaseLimit: zero(ns.Features.Databases), BackupLimit: zero(ns.Features.Backups), CreatedAt: t0, UpdatedAt: t0,
		Config: servers.Server{
			UUID: req.UUID, Name: ns.Name, Description: ns.Description, Status: servers.StatusInstalling, Startup: ns.Startup, Image: ns.Image, Memory: ns.Limits.Memory,
			Swap: ns.Limits.Swap, Disk: ns.Limits.Disk, IO: ns.Limits.IO, CPU: ns.Limits.CPU, Threads: ns.Limits.Threads, OOMDisabled: req.OOMDisabled, SkipScripts: req.SkipScripts,
			AllocationLimit: zero(ns.Features.Allocations),
		},
	}
	for _, aid := range req.Allocations {
		sid := id
		s.allocs[aid].ServerID = &sid
	}
	s.servers[id] = &record{server: srv, values: map[string]string{}}
	for _, v := range req.Values {
		s.servers[id].values[v.EnvName] = v.Store
	}
	return srv, nil
}

// zero is what the reference stores for a limit that was sent as null when a server is made.
func zero(p *int64) *int64 {
	if p == nil {
		return ptr(int64(0))
	}
	return p
}

func (s *store) Transferring(context.Context, uint64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transfer, s.fail
}

func (s *store) SetStatus(_ context.Context, id uint64, from, to string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses = append(s.statuses, statusCall{id, from, to})
	if s.fail != nil {
		return false, s.fail
	}
	r, ok := s.servers[id]
	if !ok || s.statusBusy || r.server.Config.Status != from {
		return false, nil
	}
	r.server.Config.Status = to
	return true, nil
}

func (s *store) Databases(_ context.Context, serverID uint64) ([]appkit.ServerDatabase, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dbs[serverID], s.fail
}

func (s *store) DeleteDatabase(_ context.Context, id uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletedDB = append(s.deletedDB, id)
	return s.fail
}

func (s *store) Delete(_ context.Context, id uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	for _, a := range s.allocs {
		if a.ServerID != nil && *a.ServerID == id {
			a.ServerID, a.Notes = nil, nil
		}
	}
	delete(s.servers, id)
	s.deleted = append(s.deleted, id)
	return nil
}

type targets struct {
	s   *store
	err error
}

func (t targets) For(_ context.Context, id uint64) (controlapi.Target, error) {
	if t.err != nil {
		return controlapi.Target{}, t.err
	}
	t.s.mu.Lock()
	defer t.s.mu.Unlock()
	r, ok := t.s.servers[id]
	if !ok {
		return controlapi.Target{}, controlapi.ErrNotFound
	}
	return controlapi.Target{UUID: r.server.Config.UUID, Node: node}, nil
}

type daemonCall struct {
	Kind, UUID string
	Start      bool
}

type daemon struct {
	mu    sync.Mutex
	calls []daemonCall
	errs  map[string]error
}

func (d *daemon) record(kind, uuid string, start bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, daemonCall{kind, uuid, start})
	return d.errs[kind]
}

func (d *daemon) Create(_ context.Context, _ wings.Node, uuid string, start bool) error {
	return d.record("create", uuid, start)
}
func (d *daemon) Sync(_ context.Context, _ wings.Node, uuid string) error {
	return d.record("sync", uuid, false)
}
func (d *daemon) Reinstall(_ context.Context, _ wings.Node, uuid string) error {
	return d.record("reinstall", uuid, false)
}
func (d *daemon) Delete(_ context.Context, _ wings.Node, uuid string) error {
	return d.record("delete", uuid, false)
}

func (d *daemon) count(kind string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, c := range d.calls {
		if c.Kind == kind {
			n++
		}
	}
	return n
}

type hostRun struct {
	Host uint64
	SQL  []string
}

type hosts struct {
	mu   sync.Mutex
	runs []hostRun
	err  error
}

func (h *hosts) Run(_ context.Context, id uint64, st []dbhosts.Statement) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := hostRun{Host: id}
	for _, s := range st {
		r.SQL = append(r.SQL, s.SQL)
	}
	h.runs = append(h.runs, r)
	return h.err
}

type rig struct {
	t        *testing.T
	client   *apitest.Client
	store    *store
	targets  *targets
	daemon   *daemon
	hosts    *hosts
	logs     *bytes.Buffer
	handlers *Handlers
}

// The world: node 4 has allocations 5 (the server's own default), 6 and 7 (also the server's) and 8 and 9 (free); node 5 has 20 (free).
// Server 7 is on node 4, owned by user 3, of egg 10 in nest 1. Server 8 is of another owner, on node 4.
func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, daemon: &daemon{errs: map[string]error{}}, hosts: &hosts{}, logs: &bytes.Buffer{}}
	s := &store{
		nextID: 100,
		servers: map[uint64]*record{
			7: {values: map[string]string{"SERVER_JARFILE": "paper.jar"}, server: appkit.Server{
				ID: 7, ExternalID: ptr("crm-7"), ShortID: "aaaa1111", OwnerID: 3, NodeID: 4, AllocationID: 5, NestID: 1, EggID: 10,
				DatabaseLimit: ptr(int64(2)), BackupLimit: ptr(int64(0)), CreatedAt: t0, UpdatedAt: t0.Add(time.Minute),
				Config: servers.Server{
					UUID: uuid1, Name: "Survival", Description: "A world", Memory: 1024, Swap: 0, Disk: 5000, IO: 500, CPU: 100, OOMDisabled: true, AllocationLimit: ptr(int64(5)),
					Startup: "java -jar server.jar", Image: "ghcr.io/y/java_21", LocationShort: "us.nyc",
					Variables: []servers.Variable{{EnvName: "SERVER_JARFILE", Default: "server.jar", Value: ptr("paper.jar")}},
				},
			}},
			8: {values: map[string]string{}, server: appkit.Server{
				ID: 8, ShortID: "bbbb2222", OwnerID: 4, NodeID: 4, AllocationID: 30, NestID: 1, EggID: 10, CreatedAt: t0, UpdatedAt: t0,
				Config: servers.Server{UUID: uuid2, Name: "Creative", Startup: "java -jar server.jar", Image: "ghcr.io/y/java_21", Status: servers.StatusSuspended},
			}},
		},
		allocs: map[uint64]*allocRow{},
		eggs: map[uint64]EggInfo{
			10: {NestID: 1, Variables: []servers.EggVariable{{EnvName: "SERVER_JARFILE", Name: "Server Jar File", Rules: "required|string|max:20", UserViewable: true, UserEditable: true}, {EnvName: "MC_VERSION", Name: "Version", Rules: "nullable|string"}}},
			11: {NestID: 2},
		},
		owners: map[uint64]bool{3: true, 4: true, 9: true},
		dbs:    map[uint64][]appkit.ServerDatabase{},
	}
	seven, eight := uint64(7), uint64(8)
	s.allocs[5] = &allocRow{NodeID: 4, ServerID: &seven, IP: "10.0.0.1", Port: 25565, Notes: ptr("main")}
	s.allocs[6] = &allocRow{NodeID: 4, ServerID: &seven, IP: "10.0.0.1", Port: 25566}
	s.allocs[7] = &allocRow{NodeID: 4, ServerID: &seven, IP: "10.0.0.1", Port: 25567}
	s.allocs[8] = &allocRow{NodeID: 4, IP: "10.0.0.1", Port: 25568}
	s.allocs[9] = &allocRow{NodeID: 4, IP: "10.0.0.2", Port: 25565}
	s.allocs[20] = &allocRow{NodeID: 5, IP: "10.0.1.1", Port: 25565}
	s.allocs[30] = &allocRow{NodeID: 4, ServerID: &eight, IP: "10.0.0.3", Port: 25565}
	s.usage = []servers.NodeUsage{
		{ID: 4, LocationID: 1, Public: true, Memory: 8192, Disk: 100000, UsedMemory: 1024, UsedDisk: 5000},
		{ID: 5, LocationID: 2, Public: true, Memory: 8192, Disk: 100000},
		{ID: 6, LocationID: 1, Public: false, Memory: 8192, Disk: 100000},
	}
	r.store = s
	r.targets = &targets{s: s}

	w := apitest.NewWorld()
	for id, admin := range map[uint64]bool{grace: true, bob: true, cleo: true, dave: false} {
		w.AddUser(id, admin)
	}
	w.GrantApp(grace, "servers=rw", "users=read", "nodes=read", "allocations=read", "nests=read", "eggs=read", "locations=read", "server_databases=read")
	w.GrantApp(bob, "servers=read")
	w.GrantApp(cleo, "users=rw")
	w.GrantApp(dave, "servers=rw")

	h, err := New(Deps{Store: s, Targets: r.targets, Daemon: r.daemon, Hosts: r.hosts, Logger: slog.New(slog.NewTextHandler(r.logs, nil))})
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

// wingsError is a node that cannot be reached.
var wingsError = wings.Error{Status: 504, Message: "no reply from the node", Unreachable: true}
