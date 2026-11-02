package appnodes

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/nodes"
	"github.com/Helles01/raptor-panel/internal/servers"
	"github.com/Helles01/raptor-panel/internal/wings"
)

const (
	grace = 1 // may read and write nodes, and read allocations and locations
	bob   = 2 // may only read nodes
	cleo  = 3 // has other resources only
	dave  = 4 // not an administrator
)

var errBoom = errors.New("the database is down: password=hunter2")

var t0 = time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

const oldToken = "O0O0O0O0O0O0O0O0O0O0O0O0O0O0O0O0O0O0O0O0O0O0O0O0O0O0O0O0O0O0O0O0"

type store struct {
	mu sync.Mutex

	nodes    map[uint64]appkit.Node
	creds    map[uint64]nodes.Credentials
	mounts   map[uint64][]string
	nextID   uint64
	fail     error
	servers  int64
	noLoc    bool
	connFail error

	lastLoad  appkit.Includes
	lastQuery listquery.Query
	listPage  [2]int
	created   []CreateRequest
	updated   []nodes.Settings
	newCreds  []*nodes.Credentials
	deleted   []uint64
}

func (s *store) List(_ context.Context, q listquery.Query, page, perPage int, load appkit.Includes) ([]appkit.Node, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastLoad, s.lastQuery, s.listPage = load, q, [2]int{page, perPage}
	var out []appkit.Node
	for id := uint64(1); id < 1000; id++ {
		if n, ok := s.nodes[id]; ok {
			out = append(out, s.fill(n, load))
		}
	}
	return out, len(out), s.fail
}

func (s *store) fill(n appkit.Node, load appkit.Includes) appkit.Node {
	if load["allocations"] {
		n.Allocations = []appkit.Allocation{{ID: 70, NodeID: n.ID, IP: "10.0.0.1", Port: 25565}}
	}
	if load["location"] {
		n.Location = &appkit.Location{ID: n.Settings.LocationID, Short: "us.nyc", CreatedAt: t0, UpdatedAt: t0}
	}
	return n
}

func (s *store) Find(_ context.Context, id uint64, load appkit.Includes) (appkit.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastLoad = load
	if s.fail != nil {
		return appkit.Node{}, s.fail
	}
	if n, ok := s.nodes[id]; ok {
		return s.fill(n, load), nil
	}
	return appkit.Node{}, appkit.ErrNotFound
}

func (s *store) Create(_ context.Context, r CreateRequest) (appkit.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return appkit.Node{}, s.fail
	}
	if s.noLoc {
		return appkit.Node{}, ErrNoLocation
	}
	s.created = append(s.created, r)
	s.nextID++
	n := appkit.Node{ID: s.nextID, UUID: r.UUID, Settings: r.Settings, TokenID: r.Credentials.TokenID, CreatedAt: t0, UpdatedAt: t0}
	s.nodes[n.ID] = n
	s.creds[n.ID] = r.Credentials
	return n, nil
}

func (s *store) Update(_ context.Context, id uint64, st nodes.Settings, creds *nodes.Credentials) (appkit.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return appkit.Node{}, s.fail
	}
	n, ok := s.nodes[id]
	if !ok {
		return appkit.Node{}, appkit.ErrNotFound
	}
	if s.noLoc {
		return appkit.Node{}, ErrNoLocation
	}
	s.updated = append(s.updated, st)
	s.newCreds = append(s.newCreds, creds)
	n.Settings, n.UpdatedAt = st, t0.Add(time.Hour)
	if creds != nil {
		s.creds[id] = *creds
		n.TokenID = creds.TokenID
	}
	s.nodes[id] = n
	return n, nil
}

func (s *store) Delete(_ context.Context, id uint64, check func(int64) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	if _, ok := s.nodes[id]; !ok {
		return appkit.ErrNotFound
	}
	if err := check(s.servers); err != nil {
		return err
	}
	s.deleted = append(s.deleted, id)
	return nil
}

func (s *store) Config(_ context.Context, id uint64) (nodes.WingsNode, nodes.Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return nodes.WingsNode{}, nodes.Credentials{}, s.fail
	}
	n, ok := s.nodes[id]
	if !ok {
		return nodes.WingsNode{}, nodes.Credentials{}, appkit.ErrNotFound
	}
	st := n.Settings
	return nodes.WingsNode{
		UUID: n.UUID, FQDN: st.FQDN, Scheme: st.Scheme, BehindProxy: st.BehindProxy, DaemonBase: st.DaemonBase, DaemonSFTP: st.DaemonSFTP,
		DaemonListen: st.DaemonListen, UploadSize: st.UploadSize, AllowedMounts: s.mounts[id],
	}, s.creds[id], nil
}

func (s *store) Connection(_ context.Context, id uint64) (wings.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.connFail != nil {
		return wings.Node{}, s.connFail
	}
	n := s.nodes[id]
	return wings.Node{Scheme: n.Settings.Scheme, FQDN: n.Settings.FQDN, Port: n.Settings.DaemonListen, Token: s.creds[id].Token}, nil
}

func (s *store) Candidates(context.Context) ([]Candidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return nil, s.fail
	}
	var out []Candidate
	for id := uint64(1); id < 1000; id++ {
		if n, ok := s.nodes[id]; ok {
			st := n.Settings
			out = append(out, Candidate{Node: n, Usage: servers.NodeUsage{
				ID: n.ID, LocationID: st.LocationID, Public: st.Public, Memory: st.Memory, MemoryOverallocate: st.MemoryOverallocate,
				Disk: st.Disk, DiskOverallocate: st.DiskOverallocate, UsedMemory: n.AllocatedMemory, UsedDisk: n.AllocatedDisk,
			}})
		}
	}
	// A real store has no order to promise; the handler must not depend on one.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

type pushed struct {
	Node   wings.Node
	Config []byte
}

type daemon struct {
	mu    sync.Mutex
	calls []pushed
	err   error
}

func (d *daemon) UpdateConfiguration(_ context.Context, n wings.Node, config []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, pushed{n, config})
	return d.err
}

type rig struct {
	t        *testing.T
	client   *apitest.Client
	store    *store
	daemon   *daemon
	logs     *bytes.Buffer
	handlers *Handlers
}

func settings(name string, loc uint64, public bool, memory, disk int64) nodes.Settings {
	s := nodes.DefaultNewNode()
	s.Name, s.LocationID, s.Public, s.Memory, s.Disk, s.FQDN = name, loc, public, memory, disk, strings.ToLower(name)+".example.com"
	s.DaemonBase = "/srv/daemon"
	return s
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, daemon: &daemon{}, logs: &bytes.Buffer{}}
	n7 := appkit.Node{ID: 7, UUID: "70000000-0000-4000-8000-000000000000", Settings: settings("node-7", 1, true, 4096, 10000), TokenID: "TOKENIDOFNODE007", AllocatedMemory: 1024, AllocatedDisk: 2048, CreatedAt: t0, UpdatedAt: t0.Add(time.Minute)}
	n8 := appkit.Node{ID: 8, UUID: "80000000-0000-4000-8000-000000000000", Settings: settings("node-8", 2, false, 8192, 20000), TokenID: "TOKENIDOFNODE008", CreatedAt: t0, UpdatedAt: t0}
	n8.Settings.Description = "Spare"
	n9 := appkit.Node{ID: 9, UUID: "90000000-0000-4000-8000-000000000000", Settings: settings("node-9", 2, true, 2048, 4000), TokenID: "TOKENIDOFNODE009", AllocatedMemory: 2048, AllocatedDisk: 100, CreatedAt: t0, UpdatedAt: t0}
	n9.Settings.MemoryOverallocate = 50
	r.store = &store{
		nextID: 100, nodes: map[uint64]appkit.Node{7: n7, 8: n8, 9: n9},
		creds:  map[uint64]nodes.Credentials{7: {TokenID: "TOKENIDOFNODE007", Token: oldToken[:64]}, 8: {TokenID: "TOKENIDOFNODE008", Token: strings.Repeat("8", 64)}, 9: {TokenID: "TOKENIDOFNODE009", Token: strings.Repeat("9", 64)}},
		mounts: map[uint64][]string{7: {"/mnt/shared"}},
	}

	w := apitest.NewWorld()
	for id, admin := range map[uint64]bool{grace: true, bob: true, cleo: true, dave: false} {
		w.AddUser(id, admin)
	}
	w.GrantApp(grace, "nodes=rw", "allocations=read", "locations=read")
	w.GrantApp(bob, "nodes=read")
	w.GrantApp(cleo, "users=rw")
	w.GrantApp(dave, "nodes=rw")

	h, err := New(Deps{Store: r.store, Daemon: r.daemon, BaseURL: "https://panel.example.com", Logger: slog.New(slog.NewTextHandler(r.logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	r.handlers = h
	r.client = w.Serve(t, func(g api.Group) { h.Register(g) })
	return r
}

const base = "/api/application/nodes"

func (r *rig) do(user uint64, method, path, body string) apitest.Reply {
	r.t.Helper()
	return r.client.AsApp(user, method, base+path, body)
}

const goodBody = `{"name":"node-new","location_id":1,"fqdn":"new.example.com","scheme":"https","memory":2048,"memory_overallocate":0,"disk":5000,"disk_overallocate":0,"daemon_sftp":2022,"daemon_listen":8080}`

func TestEveryRouteNeedsItsLevel(t *testing.T) {
	routes := []struct {
		name, method, path, body string
		write                    bool
	}{
		{"list", "GET", "", "", false},
		{"deployable", "GET", "/deployable?memory=1&disk=1", "", false},
		{"view", "GET", "/7", "", false},
		{"configuration", "GET", "/7/configuration", "", true},
		{"create", "POST", "", goodBody, true},
		{"update", "PATCH", "/7", goodBody, true},
		{"delete", "DELETE", "/7", "", true},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			for user, open := range map[uint64]bool{grace: true, bob: !rt.write, cleo: false, dave: false} {
				r := newRig(t)
				rp := r.do(user, rt.method, rt.path, rt.body)
				if open && (rp.Status < 200 || rp.Status > 299) || !open && rp.Status != 403 {
					t.Errorf("user %d: status = %d: %s", user, rp.Status, rp.Raw)
				}
				if !open && (len(r.store.created)+len(r.store.updated)+len(r.store.deleted)+len(r.daemon.calls) != 0 || rp.Contains(oldToken[:10])) {
					t.Errorf("user %d: something was changed or shown", user)
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
	rp := r.do(bob, "GET", "?filter[name]=node&sort=-memory&per_page=2", "")
	if rp.Status != 200 || rp.At("object") != "list" || rp.At("meta", "pagination", "total") != 3.0 || rp.At("meta", "pagination", "per_page") != 2.0 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	want := map[string]any{
		"id": 7.0, "uuid": "70000000-0000-4000-8000-000000000000", "public": true, "name": "node-7", "description": nil, "location_id": 1.0,
		"fqdn": "node-7.example.com", "scheme": "https", "behind_proxy": false, "maintenance_mode": false, "memory": 4096.0, "memory_overallocate": 0.0,
		"disk": 10000.0, "disk_overallocate": 0.0, "upload_size": 100.0, "daemon_listen": 8080.0, "daemon_sftp": 2022.0, "daemon_base": "/srv/daemon",
		"created_at": "2026-10-01T10:00:00+00:00", "updated_at": "2026-10-01T10:01:00+00:00",
		"allocated_resources": map[string]any{"memory": 1024.0, "disk": 2048.0},
	}
	if rp.At("data", 0, "object") != "node" || !reflect.DeepEqual(rp.At("data", 0, "attributes"), want) {
		t.Errorf("first = %v", rp.At("data", 0, "attributes"))
	}
	if rp.At("data", 1, "attributes", "description") != "Spare" || rp.At("data", 1, "attributes", "public") != false {
		t.Errorf("second = %v", rp.At("data", 1, "attributes"))
	}
	// The credentials of a node are never part of it.
	for _, secret := range []string{"TOKENID", "token", oldToken[:10], strings.Repeat("8", 10)} {
		if rp.Contains(secret) {
			t.Errorf("%q is in %s", secret, rp.Raw)
		}
	}
	if len(r.store.lastQuery.Conditions) != 1 || r.store.lastQuery.Conditions[0].Column != "name" || r.store.listPage != [2]int{1, 2} {
		t.Errorf("query = %+v page = %v", r.store.lastQuery, r.store.listPage)
	}
	// The token id is found by its whole value only (C-044).
	r.do(bob, "GET", "?filter[daemon_token_id]=TOKENIDOFNODE007", "")
	if c := r.store.lastQuery.Conditions; len(c) != 1 || c[0].Match != listquery.Exact {
		t.Errorf("token id filter = %+v", c)
	}

	rp = r.do(bob, "GET", "/8", "")
	if rp.Status != 200 || rp.At("attributes", "name") != "node-8" {
		t.Errorf("view: %d %s", rp.Status, rp.Raw)
	}
	for _, path := range []string{"/99", "/0", "/x", "/7x"} {
		if rp := r.do(bob, "GET", path, ""); rp.Status != 404 {
			t.Errorf("%s: status = %d", path, rp.Status)
		}
	}
	for _, q := range []string{"?filter[token]=x", "?sort=name", "?per_page=0"} {
		if rp := r.do(bob, "GET", q, ""); rp.Status != 400 && rp.Status != 422 {
			t.Errorf("%s: status = %d", q, rp.Status)
		}
	}
}

func TestIncludes(t *testing.T) {
	r := newRig(t)
	rp := r.do(grace, "GET", "/7?include=allocations,location", "")
	if rp.Status != 200 || rp.At("relationships", "allocations", "object") != "list" || rp.At("relationships", "allocations", "data", 0, "attributes", "port") != 25565.0 ||
		rp.At("relationships", "location", "object") != "location" || rp.At("relationships", "location", "attributes", "short") != "us.nyc" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if !r.store.lastLoad["allocations"] || !r.store.lastLoad["location"] {
		t.Errorf("load = %v", r.store.lastLoad)
	}

	// A key that may read nodes only is told so, and the store is not asked.
	r = newRig(t)
	rp = r.do(bob, "GET", "?include=allocations,location", "")
	for _, name := range []string{"allocations", "location"} {
		if rp.At("data", 0, "relationships", name, "object") != "null_resource" {
			t.Errorf("%s = %v", name, rp.At("data", 0, "relationships", name))
		}
	}
	if len(r.store.lastLoad) != 0 {
		t.Errorf("load = %v", r.store.lastLoad)
	}
	if rp := r.do(grace, "GET", "/7?include=servers", ""); rp.Has("relationships", "servers") {
		t.Errorf("servers shown: %s", rp.Raw)
	}
}

func TestConfiguration(t *testing.T) {
	r := newRig(t)
	rp := r.do(grace, "GET", "/7/configuration", "")
	if rp.Status != 200 || rp.Header.Get("Cache-Control") != "no-store" || !strings.HasPrefix(rp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("answer = %d %s %v", rp.Status, rp.Raw, rp.Header)
	}
	want := map[string]any{
		"debug": false, "uuid": "70000000-0000-4000-8000-000000000000", "token_id": "TOKENIDOFNODE007", "token": oldToken[:64],
		"api": map[string]any{
			"host": "0.0.0.0", "port": 8080.0, "upload_limit": 100.0,
			"ssl": map[string]any{"enabled": true, "cert": "/etc/letsencrypt/live/node-7.example.com/fullchain.pem", "key": "/etc/letsencrypt/live/node-7.example.com/privkey.pem"},
		},
		"system":         map[string]any{"data": "/srv/daemon", "sftp": map[string]any{"bind_port": 2022.0}},
		"allowed_mounts": []any{"/mnt/shared"},
		"remote":         "https://panel.example.com",
	}
	if !reflect.DeepEqual(rp.Body, want) {
		t.Errorf("configuration = %v", rp.Body)
	}
	if rp := r.do(grace, "GET", "/99/configuration", ""); rp.Status != 404 {
		t.Errorf("unknown: %d", rp.Status)
	}
}

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestCreate(t *testing.T) {
	r := newRig(t)
	rp := r.do(grace, "POST", "", goodBody)
	if rp.Status != 201 || rp.At("object") != "node" || rp.At("attributes", "id") != 101.0 || rp.At("meta", "resource") != "https://panel.example.com/api/application/nodes/101" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if len(r.store.created) != 1 {
		t.Fatalf("created = %+v", r.store.created)
	}
	req := r.store.created[0]
	// What was not sent is what the node form offers.
	want := nodes.Settings{
		Name: "node-new", LocationID: 1, FQDN: "new.example.com", Scheme: "https", Public: true, Memory: 2048, Disk: 5000, DaemonBase: "/var/lib/pterodactyl/volumes",
		DaemonSFTP: 2022, DaemonListen: 8080, UploadSize: 100,
	}
	if req.Settings != want {
		t.Errorf("settings = %+v, want %+v", req.Settings, want)
	}
	if !uuidV4.MatchString(req.UUID) || len(req.Credentials.TokenID) != 16 || len(req.Credentials.Token) != 64 {
		t.Errorf("uuid %q, credentials %d/%d", req.UUID, len(req.Credentials.TokenID), len(req.Credentials.Token))
	}
	// The answer does not carry the token, and neither does the log.
	if rp.Contains(req.Credentials.Token) || rp.Contains(req.Credentials.TokenID) || strings.Contains(r.logs.String(), req.Credentials.Token) {
		t.Error("the credentials of the node were shown")
	}
	if len(r.daemon.calls) != 0 {
		t.Error("a node that has just been made was sent something")
	}

	// Everything sent is used.
	rp = r.do(grace, "POST", "", `{"name":"Full Node","description":"All set","location_id":"2","public":false,"fqdn":"full.example.com","scheme":"http","behind_proxy":true,"maintenance_mode":true,`+
		`"memory":"1024","memory_overallocate":-1,"disk":2000,"disk_overallocate":10,"upload_size":500,"daemon_base":"/data/wings","daemon_sftp":2222,"daemon_listen":9090}`)
	if rp.Status != 201 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	full := nodes.Settings{
		Name: "Full Node", Description: "All set", LocationID: 2, FQDN: "full.example.com", Scheme: "http", BehindProxy: true, MaintenanceMode: true, Memory: 1024, MemoryOverallocate: -1,
		Disk: 2000, DiskOverallocate: 10, UploadSize: 500, DaemonBase: "/data/wings", DaemonSFTP: 2222, DaemonListen: 9090,
	}
	if got := r.store.created[1].Settings; got != full {
		t.Errorf("settings = %+v, want %+v", got, full)
	}
}

func TestCreateRefusals(t *testing.T) {
	without := func(key string) string {
		var m map[string]any
		_ = json.Unmarshal([]byte(goodBody), &m)
		delete(m, key)
		b, _ := json.Marshal(m)
		return string(b)
	}
	with := func(key string, value any) string {
		var m map[string]any
		_ = json.Unmarshal([]byte(goodBody), &m)
		m[key] = value
		b, _ := json.Marshal(m)
		return string(b)
	}
	tests := []struct {
		name, body string
		status     int
		detail     string
		field      string
	}{
		{"not JSON", `name=x`, 400, "", ""},
		{"a location that is not there", goodBody, 422, "The selected location id is invalid.", "location_id"},
	}
	for _, key := range []string{"name", "location_id", "fqdn", "scheme", "memory", "memory_overallocate", "disk", "disk_overallocate", "daemon_sftp", "daemon_listen"} {
		tests = append(tests, struct {
			name, body string
			status     int
			detail     string
			field      string
		}{"no " + key, without(key), 422, "The " + strings.ReplaceAll(key, "_", " ") + " field is required.", key})
	}
	for _, c := range []struct {
		name, key string
		value     any
		detail    string
	}{
		{"a name that is a number", "name", 5, "The name must be a string."},
		{"a description that is a list", "description", []string{"x"}, "The description must be a string."},
		{"public as text", "public", "yes", "The public field must be true or false."},
		{"a memory that is text", "memory", "lots", "The memory must be a number."},
		{"a port that is a list", "daemon_listen", []int{1}, "The daemon listen must be a number."},
		{"an upload size that is a fraction", "upload_size", 1.5, "The upload size must be an integer."},
		{"a blank base path", "daemon_base", nil, "The daemon base field is required."},
		{"a location that is text", "location_id", "here", "The location id must be an integer."},
	} {
		tests = append(tests, struct {
			name, body string
			status     int
			detail     string
			field      string
		}{c.name, with(c.key, c.value), 422, c.detail, c.key})
	}
	for _, c := range []struct {
		name, key string
		value     any
	}{
		{"a name with a slash", "name", "a/b"}, {"a scheme that is ftp", "scheme", "ftp"}, {"a memory of zero", "memory", 0}, {"a disk below zero", "disk", -5},
		{"a port above the range", "daemon_sftp", 70000}, {"a host that is a URL", "fqdn", "https://x.example.com/"}, {"a relative base path", "daemon_base", "data"},
		{"a base path with ..", "daemon_base", "/data/../etc"}, {"an overallocation below -1", "memory_overallocate", -2}, {"an upload size of zero", "upload_size", 0},
	} {
		tests = append(tests, struct {
			name, body string
			status     int
			detail     string
			field      string
		}{c.name, with(c.key, c.value), 422, "", c.key})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			r.store.noLoc = tt.name == "a location that is not there"
			rp := r.do(grace, "POST", "", tt.body)
			if rp.Status != tt.status || (tt.detail != "" && rp.Detail() != tt.detail) {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if tt.field != "" && rp.At("errors", 0, "meta", "source_field") != tt.field {
				t.Errorf("field = %v", rp.At("errors", 0, "meta"))
			}
			if len(r.store.created) != 0 {
				t.Error("a node was made")
			}
		})
	}
}

func TestUpdate(t *testing.T) {
	r := newRig(t)
	body := `{"name":"node-7b","location_id":2,"fqdn":"seven.example.com","scheme":"https","memory":8192,"memory_overallocate":5,"disk":20000,"disk_overallocate":0,"daemon_sftp":2023,"daemon_listen":8081,"description":"Renamed"}`
	rp := r.do(grace, "PATCH", "/7", body)
	if rp.Status != 200 || rp.At("object") != "node" || rp.At("attributes", "name") != "node-7b" || rp.At("attributes", "fqdn") != "seven.example.com" || rp.At("attributes", "description") != "Renamed" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	got := r.store.updated[0]
	// What was not sent stays, the base path included (C-044).
	if got.DaemonBase != "/srv/daemon" || !got.Public || got.UploadSize != 100 || got.LocationID != 2 || got.DaemonListen != 8081 {
		t.Errorf("settings = %+v", got)
	}
	if r.store.newCreds[0] != nil {
		t.Error("the credentials changed without being asked to")
	}

	// The node is told where it is now, with the token it has now, and gets the configuration as stored.
	if len(r.daemon.calls) != 1 {
		t.Fatalf("calls = %+v", r.daemon.calls)
	}
	call := r.daemon.calls[0]
	if call.Node.FQDN != "node-7.example.com" || call.Node.Port != 8080 || call.Node.Token != oldToken[:64] {
		t.Errorf("sent to %+v", call.Node)
	}
	var cfg map[string]any
	if err := json.Unmarshal(call.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["token"] != oldToken[:64] || cfg["token_id"] != "TOKENIDOFNODE007" || cfg["api"].(map[string]any)["port"] != 8081.0 || cfg["remote"] != "https://panel.example.com" {
		t.Errorf("config = %v", cfg)
	}
	if rp.Contains(oldToken[:10]) {
		t.Error("the token was shown")
	}
}

func TestUpdateClearsTheDescriptionWithNull(t *testing.T) {
	r := newRig(t)
	body := func(d string) string {
		return `{"name":"node-8","location_id":2,"fqdn":"node-8.example.com","scheme":"https","memory":8192,"memory_overallocate":0,"disk":20000,"disk_overallocate":0,"daemon_sftp":2022,"daemon_listen":8080` + d + `}`
	}
	if rp := r.do(grace, "PATCH", "/8", body(``)); rp.At("attributes", "description") != "Spare" {
		t.Errorf("kept: %s", rp.Raw)
	}
	if rp := r.do(grace, "PATCH", "/8", body(`,"description":null`)); rp.Status != 200 || rp.At("attributes", "description") != nil {
		t.Errorf("cleared: %s", rp.Raw)
	}
}

func TestResetSecret(t *testing.T) {
	// Only the JSON value true resets the token, as in the reference.
	for _, tc := range []struct {
		value string
		reset bool
	}{{`true`, true}, {`"true"`, false}, {`1`, false}, {`false`, false}, {`null`, false}} {
		r := newRig(t)
		body := strings.TrimSuffix(goodBody, "}") + `,"reset_secret":` + tc.value + `}`
		rp := r.do(grace, "PATCH", "/7", body)
		if rp.Status != 200 {
			t.Fatalf("%s: %d %s", tc.value, rp.Status, rp.Raw)
		}
		creds := r.store.newCreds[0]
		if (creds != nil) != tc.reset {
			t.Errorf("%s: credentials = %v", tc.value, creds)
		}
		if tc.reset {
			if len(creds.TokenID) != 16 || len(creds.Token) != 64 || creds.Token == oldToken[:64] {
				t.Errorf("new credentials = %+v", creds)
			}
			// The node is reached with the old token, and told the new one.
			call := r.daemon.calls[0]
			if call.Node.Token != oldToken[:64] || !strings.Contains(string(call.Config), creds.Token) || !strings.Contains(string(call.Config), creds.TokenID) {
				t.Errorf("sent to %+v: %s", call.Node, call.Config)
			}
			if rp.Contains(creds.Token) || rp.Contains(creds.TokenID) || strings.Contains(r.logs.String(), creds.Token) {
				t.Error("the new token was shown")
			}
		}
	}
}

func TestUpdateWhenTheNodeCannotBeTold(t *testing.T) {
	const text = "The daemon configuration has been updated, however there was an error encountered while attempting to automatically update the configuration file on the Daemon. You will need to manually update the configuration file (config.yml) for the daemon to apply these changes."
	for name, setup := range map[string]func(r *rig){
		"the node refuses":       func(r *rig) { r.daemon.err = &wings.Error{Status: 504, Message: "no reply", Unreachable: true} },
		"the connection is lost": func(r *rig) { r.store.connFail = errBoom },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			setup(r)
			rp := r.do(grace, "PATCH", "/7", goodBody)
			if rp.Status != 400 || rp.Code() != "DisplayException" || rp.Detail() != text || rp.Contains("hunter2") {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			// The change is made all the same.
			if len(r.store.updated) != 1 || r.store.nodes[7].Settings.Name != "node-new" {
				t.Error("the change was not stored")
			}
			if !strings.Contains(r.logs.String(), "was not delivered") {
				t.Errorf("log = %s", r.logs.String())
			}
		})
	}
}

func TestUpdateRefusals(t *testing.T) {
	tests := []struct {
		name, path, body string
		status           int
		detail           string
	}{
		{"not there", "/99", goodBody, 404, ""},
		{"not there, and a bad body", "/99", `{}`, 404, ""},
		{"no name", "/7", strings.Replace(goodBody, `"name":"node-new",`, "", 1), 422, "The name field is required."},
		{"a memory of zero", "/7", strings.Replace(goodBody, `"memory":2048`, `"memory":0`, 1), 422, ""},
		{"a base path that is relative", "/7", strings.TrimSuffix(goodBody, "}") + `,"daemon_base":"data"}`, 422, ""},
		{"not JSON", "/7", `x`, 400, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(grace, "PATCH", tt.path, tt.body)
			if rp.Status != tt.status || (tt.detail != "" && rp.Detail() != tt.detail) {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if len(r.store.updated) != 0 || len(r.daemon.calls) != 0 {
				t.Error("a node was changed")
			}
		})
	}
	r := newRig(t)
	r.store.noLoc = true
	if rp := r.do(grace, "PATCH", "/7", goodBody); rp.Status != 422 || rp.Detail() != "The selected location id is invalid." || len(r.daemon.calls) != 0 {
		t.Errorf("a location that is not there: %d %s", rp.Status, rp.Raw)
	}
}

func TestDelete(t *testing.T) {
	r := newRig(t)
	if rp := r.do(grace, "DELETE", "/8", ""); rp.Status != 204 || len(rp.Raw) != 0 || !reflect.DeepEqual(r.store.deleted, []uint64{8}) {
		t.Errorf("answer = %d %s, deleted %v", rp.Status, rp.Raw, r.store.deleted)
	}
	r.store.servers = 3
	rp := r.do(grace, "DELETE", "/7", "")
	if rp.Status != 400 || rp.Code() != "DisplayException" || rp.Detail() != "A node must have no servers linked to it in order to be deleted." || len(r.store.deleted) != 1 {
		t.Errorf("a node with servers: %d %s", rp.Status, rp.Raw)
	}
	for _, path := range []string{"/99", "/x"} {
		if rp := r.do(grace, "DELETE", path, ""); rp.Status != 404 {
			t.Errorf("%s: status = %d", path, rp.Status)
		}
	}
}

func idsOf(rp apitest.Reply) []float64 {
	var out []float64
	list, _ := rp.At("data").([]any)
	for i := range list {
		out = append(out, rp.At("data", i, "attributes", "id").(float64))
	}
	return out
}

func TestDeployable(t *testing.T) {
	r := newRig(t)
	// Node 7 has room; 8 is not public; 9 has used its memory, but may be given half as much again. The answer is in the order of the ids.
	rp := r.do(bob, "GET", "/deployable?memory=1024&disk=1000", "")
	if rp.Status != 200 || rp.At("object") != "list" || !reflect.DeepEqual(idsOf(rp), []float64{7, 9}) {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if rp.Has("meta") {
		t.Errorf("a list without a page has no pagination: %v", rp.At("meta"))
	}
	if rp.At("data", 0, "object") != "node" || rp.At("data", 0, "attributes", "name") != "node-7" {
		t.Errorf("first = %v", rp.At("data", 0))
	}

	// The room is the sum of what is held and what is asked for.
	if rp := r.do(bob, "GET", "/deployable?memory=3000&disk=1000", ""); !reflect.DeepEqual(idsOf(rp), []float64{7}) {
		t.Errorf("memory 3000: %s", rp.Raw)
	}
	// Locations narrow the choice; both spellings of an array are read.
	for _, q := range []string{"&location_ids[]=2", "&location_ids[0]=2", "&location_ids%5B%5D=2"} {
		if rp := r.do(bob, "GET", "/deployable?memory=1&disk=1"+q, ""); !reflect.DeepEqual(idsOf(rp), []float64{9}) {
			t.Errorf("%s: %s", q, rp.Raw)
		}
	}
	if rp := r.do(bob, "GET", "/deployable?memory=1&disk=1&location_ids[]=1&location_ids[]=2", ""); !reflect.DeepEqual(idsOf(rp), []float64{7, 9}) {
		t.Errorf("two locations: %s", rp.Raw)
	}
	// A page.
	rp = r.do(bob, "GET", "/deployable?memory=1&disk=1&page=2&per_page=1", "")
	if rp.Status != 200 || !reflect.DeepEqual(idsOf(rp), []float64{9}) || rp.At("meta", "pagination", "total") != 2.0 || rp.At("meta", "pagination", "current_page") != 2.0 {
		t.Errorf("page 2: %d %s", rp.Status, rp.Raw)
	}
	rp = r.do(bob, "GET", "/deployable?memory=1&disk=1&page=3&per_page=1", "")
	if rp.Status != 200 || len(idsOf(rp)) != 0 {
		t.Errorf("page 3: %d %s", rp.Status, rp.Raw)
	}
}

func TestDeployableRefusals(t *testing.T) {
	tests := []struct {
		name, query string
		status      int
		detail      string
	}{
		{"nothing fits", "?memory=100000&disk=1", 400, "No nodes satisfying the requirements specified for automatic deployment could be found."},
		{"no location has room", "?memory=1&disk=1&location_ids[]=99", 400, "No nodes satisfying the requirements specified for automatic deployment could be found."},
		{"no memory", "?disk=1", 422, "The memory field is required."},
		{"no disk", "?memory=1", 422, "The disk field is required."},
		{"a memory that is text", "?memory=lots&disk=1", 422, "The memory must be an integer."},
		{"a negative disk", "?memory=1&disk=-1", 422, "The disk must be at least 0."},
		{"a location that is text", "?memory=1&disk=1&location_ids[]=x", 422, "The location_ids.0 must be an integer."},
		{"locations that are not an array", "?memory=1&disk=1&location_ids=2", 422, "The location ids must be an array."},
		{"a page that is not a number", "?memory=1&disk=1&page=x", 422, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(bob, "GET", "/deployable"+tt.query, "")
			if rp.Status != tt.status || (tt.detail != "" && rp.Detail() != tt.detail) {
				t.Errorf("answer = %d %s", rp.Status, rp.Raw)
			}
		})
	}
}

func TestStoreFailuresAreNotShown(t *testing.T) {
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "", ""}, {"GET", "/7", ""}, {"GET", "/7/configuration", ""}, {"GET", "/deployable?memory=1&disk=1", ""},
		{"POST", "", goodBody}, {"PATCH", "/7", goodBody}, {"DELETE", "/7", ""},
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
		b.GET("/deployable", r.handlers.Deployable, api.Public())
		b.GET("/view", r.handlers.View, api.Public())
		b.GET("/configuration", r.handlers.Configuration, api.Public())
		b.POST("", r.handlers.Create, api.Public())
		b.PATCH("/update", r.handlers.Update, api.Public())
		b.DELETE("/delete", r.handlers.Delete, api.Public())
	})
	for _, rt := range []struct{ method, path string }{
		{"GET", "/bare"}, {"GET", "/bare/deployable"}, {"GET", "/bare/view"}, {"GET", "/bare/configuration"}, {"POST", "/bare"}, {"PATCH", "/bare/update"}, {"DELETE", "/bare/delete"},
	} {
		if rp := client.As(0, rt.method, rt.path, `{}`); rp.Status != 403 {
			t.Errorf("%s %s: status = %d, want 403", rt.method, rt.path, rp.Status)
		}
	}
	if len(r.store.created)+len(r.store.updated)+len(r.store.deleted)+len(r.daemon.calls) != 0 {
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
		"no store": func(d *Deps) { d.Store = nil }, "no daemon": func(d *Deps) { d.Daemon = nil }, "no address": func(d *Deps) { d.BaseURL = "" }, "no logger": func(d *Deps) { d.Logger = nil },
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

func TestUpdateUsesEveryOptionalFieldSent(t *testing.T) {
	r := newRig(t)
	body := strings.TrimSuffix(goodBody, "}") + `,"public":false,"behind_proxy":true,"maintenance_mode":true,"upload_size":300,"daemon_base":"/new/base"}`
	if rp := r.do(grace, "PATCH", "/7", body); rp.Status != 200 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	got := r.store.updated[0]
	if got.Public || !got.BehindProxy || !got.MaintenanceMode || got.UploadSize != 300 || got.DaemonBase != "/new/base" {
		t.Errorf("settings = %+v", got)
	}
}

func TestALocationBelowOneIsNotALocation(t *testing.T) {
	for _, id := range []string{"0", "-5"} {
		r := newRig(t)
		rp := r.do(grace, "POST", "", strings.Replace(goodBody, `"location_id":1`, `"location_id":`+id, 1))
		if rp.Status != 422 || rp.At("errors", 0, "meta", "source_field") != "location_id" || len(r.store.created) != 0 {
			t.Errorf("%s: %d %s", id, rp.Status, rp.Raw)
		}
	}
}

func TestDeployableAcceptsZero(t *testing.T) {
	r := newRig(t)
	// A server that asks for nothing fits wherever there is a public node.
	if rp := r.do(bob, "GET", "/deployable?memory=0&disk=0", ""); rp.Status != 200 || !reflect.DeepEqual(idsOf(rp), []float64{7, 9}) {
		t.Errorf("answer = %d %s", rp.Status, rp.Raw)
	}
}
