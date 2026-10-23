package clientapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/auth/session"
	"github.com/Helles01/raptor-panel/internal/servers"
)

var t0 = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

const keySecret = "abcdefghijklmnopqrstuvwxyzABCDEF" // 32 characters

// The users of the world. Ada owns most of the servers, Bob and Cleo are subusers of the first one, Eve is a stranger.
const (
	ada   = 1
	grace = 2 // an administrator
	bob   = 3
	cleo  = 4
	eve   = 5
)

// world is every store of the tests in memory, and a record of what the handlers asked for.
type world struct {
	mu sync.Mutex

	accounts map[uint64]Account
	uuids    map[uint64]string
	totp     map[uint64]bool
	servers  []Server
	subs     map[[2]uint64][]string
	subusers map[uint64][]Subuser
	level    int

	userLog   map[uint64][]ActivityEntry
	serverLog map[uint64][]ActivityEntry

	// What was asked for.
	lists       []ServerList
	subuserReqs [][]uint64
	userReqs    []ActivityList
	serverReqs  []serverActivityReq

	// Failures to inject.
	failList     error
	failOne      error
	failSubusers error
	failAccount  error
	failActivity error
}

type serverActivityReq struct {
	ServerID   uint64
	HideAdmins bool
	List       ActivityList
}

func newWorld() *world {
	w := &world{
		accounts: map[uint64]Account{},
		uuids:    map[uint64]string{},
		totp:     map[uint64]bool{},
		subs:     map[[2]uint64][]string{},
		subusers: map[uint64][]Subuser{},
		userLog:  map[uint64][]ActivityEntry{},

		serverLog: map[uint64][]ActivityEntry{},
	}
	add := func(id uint64, name string, admin bool) {
		w.accounts[id] = Account{ID: id, RootAdmin: admin, Username: name, Email: name + "@example.com", FirstName: strings.ToUpper(name[:1]) + name[1:], LastName: "Test", Language: "en"}
		w.uuids[id] = fmt.Sprintf("%08d-0000-4000-8000-000000000000", id)
	}
	add(ada, "ada", false)
	add(grace, "grace", true)
	add(bob, "bob", false)
	add(cleo, "cleo", false)
	add(eve, "eve", false)
	w.accounts[ada] = Account{ID: ada, Username: "ada", Email: "Ada@Example.com", FirstName: "Ada", LastName: "Lovelace", Language: "hu"}

	threads := "0-3"
	jar, world := "server.jar", "world2"
	notes, alias := "main", "play.example.com"
	primary := Allocation{ID: 10, IP: "10.0.0.5", Port: 25565, Notes: &notes}
	survival := Server{
		ID: 100, UUID: "aaaa1111-2222-4333-8444-555555555555", ShortID: "aaaa1111", OwnerID: ada,
		Name: "Survival", Description: "The main world",
		Node:   Node{Name: "node-1", FQDN: "node1.example.com", SFTPPort: 2022},
		Memory: 1024, Swap: 512, Disk: 5000, IO: 500, CPU: 200, Threads: &threads, OOMDisabled: true,
		Startup: "java -Xmx{{SERVER_MEMORY}}M -jar {{SERVER_JARFILE}} --world {{WORLD}} --pass {{SECRET}} on {{SERVER_IP}}:{{SERVER_PORT}}",
		Image:   "ghcr.io/example/java:21",
		Egg:     Egg{UUID: "egg-uuid", Name: "Vanilla", Features: []string{"eula", "java_version"}},

		DatabaseLimit: ptr(int64(2)), BackupLimit: ptr(int64(5)),
		Primary:     primary,
		Allocations: []Allocation{primary, {ID: 11, IP: "10.0.0.5", IPAlias: &alias, Port: 25566}},
		Variables: []Variable{
			{Name: "Server Jar", Description: "The file", EnvName: "SERVER_JARFILE", Default: jar, UserViewable: true, UserEditable: true, Rules: "required|string"},
			{Name: "World", Description: "", EnvName: "WORLD", Default: "world", ServerValue: &world, UserViewable: true, Rules: "string"},
			{Name: "Secret", EnvName: "SECRET", Default: "hunter2", Rules: "string"},
		},
	}
	creative := Server{ID: 101, UUID: "bbbb2222-2222-4333-8444-555555555555", ShortID: "bbbb2222", OwnerID: ada, Name: "Creative", Status: servers.StatusInstalling,
		Node: Node{Name: "node-1", FQDN: "node1.example.com", SFTPPort: 2022}, Primary: Allocation{ID: 20, IP: "10.0.0.5", Port: 25570}, Allocations: []Allocation{{ID: 20, IP: "10.0.0.5", Port: 25570}}}
	strange := Server{ID: 102, UUID: "cccc3333-2222-4333-8444-555555555555", ShortID: "cccc3333", OwnerID: eve, Name: "Strange",
		Node: Node{Name: "node-2", FQDN: "node2.example.com", SFTPPort: 2022}, Primary: Allocation{ID: 30, IP: "10.0.0.6", Port: 25565}, Allocations: []Allocation{{ID: 30, IP: "10.0.0.6", Port: 25565}}}
	paused := Server{ID: 103, UUID: "dddd4444-2222-4333-8444-555555555555", ShortID: "dddd4444", OwnerID: ada, Name: "Paused", Status: servers.StatusSuspended, Transferring: true,
		Node: Node{Name: "node-3", FQDN: "node3.example.com", SFTPPort: 2022, Maintenance: true}, Primary: Allocation{ID: 40, IP: "10.0.0.7", Port: 25565}, Allocations: []Allocation{{ID: 40, IP: "10.0.0.7", Port: 25565}}}
	w.servers = []Server{survival, creative, strange, paused}

	w.subs[[2]uint64{100, bob}] = []string{"control.start", "websocket.connect"}
	w.subs[[2]uint64{100, cleo}] = []string{"startup.read", "allocation.read", "user.read", "activity.read"}
	w.subusers[100] = []Subuser{
		{User: w.person(bob), Permissions: w.subs[[2]uint64{100, bob}]},
		{User: w.person(cleo), Permissions: w.subs[[2]uint64{100, cleo}]},
	}
	return w
}

func ptr[T any](v T) *T { return &v }

func (w *world) person(id uint64) Person {
	a := w.accounts[id]
	return Person{UUID: w.uuids[id], Username: a.Username, Email: a.Email, UsesTOTP: w.totp[id], CreatedAt: t0.Add(-48 * time.Hour)}
}

func keyIdentifier(user uint64) string { return fmt.Sprintf("ptlc_user%07d", user) }

// authz.Users, Keys, Servers, Nodes, Settings and Audit.

func (w *world) ByID(_ context.Context, id uint64) (authz.User, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	a, ok := w.accounts[id]
	if !ok {
		return authz.User{}, authz.ErrNoUser
	}
	return authz.User{ID: id, UUID: w.uuids[id], RootAdmin: a.RootAdmin, UsesTOTP: w.totp[id]}, nil
}

func (w *world) ByIdentifier(_ context.Context, identifier string) (authz.Key, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for id := range w.accounts {
		if keyIdentifier(id) == identifier {
			return authz.Key{ID: id, UserID: id, Type: auth.KeyAccount, Identifier: identifier, SecretHash: auth.HashKeySecret(keySecret)}, nil
		}
	}
	return authz.Key{}, authz.ErrNoKey
}

func (w *world) Touch(context.Context, uint64, time.Time) error { return nil }

func (w *world) Find(_ context.Context, ref string) (authz.Server, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, s := range w.servers {
		if s.ShortID == ref || s.UUID == ref {
			return authz.Server{
				ID: s.ID, UUID: s.UUID, ShortID: s.ShortID, OwnerID: s.OwnerID, NodeID: 1, NodeName: s.Node.Name,
				State: servers.State{Status: s.Status, NodeMaintenance: s.Node.Maintenance, Transferring: s.Transferring},
			}, nil
		}
	}
	return authz.Server{}, authz.ErrNoServer
}

func (w *world) SubuserPermissions(_ context.Context, serverID, userID uint64) ([]string, bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p, ok := w.subs[[2]uint64{serverID, userID}]
	return p, ok, nil
}

func (w *world) ByTokenID(context.Context, string) (authz.NodeCredentials, error) {
	return authz.NodeCredentials{}, authz.ErrNoNode
}

func (w *world) TwoFactorLevel(context.Context) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.level, nil
}

func (w *world) IPBlocked(context.Context, uint64, string, string, string) error { return nil }

// Accounts.

func (w *world) accountByID(id uint64) (Account, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failAccount != nil {
		return Account{}, w.failAccount
	}
	a, ok := w.accounts[id]
	if !ok {
		return Account{}, ErrNotFound
	}
	return a, nil
}

type accountStore struct{ w *world }

func (s accountStore) ByID(_ context.Context, id uint64) (Account, error) { return s.w.accountByID(id) }

// Servers.

type serverStore struct{ w *world }

func (s serverStore) reaches(sv Server, user uint64) bool {
	_, sub := s.w.subs[[2]uint64{sv.ID, user}]
	return sv.OwnerID == user || sub
}

func (s serverStore) List(_ context.Context, l ServerList) (ServerPage, error) {
	w := s.w
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lists = append(w.lists, l)
	if w.failList != nil {
		return ServerPage{}, w.failList
	}

	var all []Server
	for _, sv := range w.servers {
		reaches := s.reaches(sv, l.Viewer)
		var in bool
		switch l.Scope {
		case servers.ScopeAccessible:
			in = reaches
		case servers.ScopeOwner:
			in = sv.OwnerID == l.Viewer
		case servers.ScopeAdminOthers:
			in = !reaches
		case servers.ScopeAdminAll:
			in = true
		}
		if in {
			sv.SubuserPermissions = w.subs[[2]uint64{sv.ID, l.Viewer}]
			all = append(all, sv)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	from := min((l.Page-1)*l.PerPage, len(all))
	to := min(from+l.PerPage, len(all))
	return ServerPage{Total: len(all), Servers: all[from:to]}, nil
}

func (s serverStore) One(_ context.Context, id uint64) (Server, error) {
	w := s.w
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failOne != nil {
		return Server{}, w.failOne
	}
	for _, sv := range w.servers {
		if sv.ID == id {
			return sv, nil
		}
	}
	return Server{}, ErrNotFound
}

func (s serverStore) Subusers(_ context.Context, ids []uint64) (map[uint64][]Subuser, error) {
	w := s.w
	w.mu.Lock()
	defer w.mu.Unlock()
	w.subuserReqs = append(w.subuserReqs, append([]uint64(nil), ids...))
	if w.failSubusers != nil {
		return nil, w.failSubusers
	}
	out := map[uint64][]Subuser{}
	for _, id := range ids {
		out[id] = w.subusers[id]
	}
	return out, nil
}

// Activity.

type activityStore struct{ w *world }

// page answers as the contract says: without the excluded events, with the actors only when asked for, one page of it.
func (activityStore) page(entries []ActivityEntry, l ActivityList) ActivityPage {
	var kept []ActivityEntry
	for _, e := range entries {
		hidden := false
		for _, x := range l.Exclude {
			hidden = hidden || e.Event == x
		}
		if hidden {
			continue
		}
		if !l.WithActors {
			e.Actor = nil
		}
		kept = append(kept, e)
	}
	from := min((l.Page-1)*l.PerPage, len(kept))
	to := min(from+l.PerPage, len(kept))
	return ActivityPage{Total: len(kept), Entries: kept[from:to]}
}

func (s activityStore) ForUser(_ context.Context, userID uint64, l ActivityList) (ActivityPage, error) {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	s.w.userReqs = append(s.w.userReqs, l)
	if s.w.failActivity != nil {
		return ActivityPage{}, s.w.failActivity
	}
	return s.page(s.w.userLog[userID], l), nil
}

func (s activityStore) ForServer(_ context.Context, serverID uint64, hideAdmins bool, l ActivityList) (ActivityPage, error) {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	s.w.serverReqs = append(s.w.serverReqs, serverActivityReq{ServerID: serverID, HideAdmins: hideAdmins, List: l})
	if s.w.failActivity != nil {
		return ActivityPage{}, s.w.failActivity
	}
	return s.page(s.w.serverLog[serverID], l), nil
}

// The server.

type rig struct {
	t    *testing.T
	w    *world
	srv  http.Handler
	logs *strings.Builder
}

type options struct {
	// extra adds routes that the real wiring does not have, to reach the guards that it makes unreachable.
	extra      func(g api.Group, h *Handlers)
	texts      map[string][]string
	hideAdmins bool
	location   *time.Location
}

func newRig(t *testing.T, opts ...func(*options)) *rig {
	t.Helper()
	var o options
	for _, f := range opts {
		f(&o)
	}
	w := newWorld()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	az, err := authz.New(authz.Deps{Users: w, Keys: w, Servers: w, Nodes: w, Settings: w, Audit: w, Logger: quiet, Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Deps{
		Accounts: accountStore{w}, Servers: serverStore{w}, Activity: activityStore{w},
		Texts: o.texts, HideAdminActivity: o.hideAdmins, Location: o.location, Logger: quiet,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(api.Options{
		Logger: quiet, Authorizer: az,
		Trust: middleware.NewProxyTrust([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, quiet),
	})
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := session.NewManager(session.NewMemoryStore(), session.Options{IdleTimeout: time.Hour, MaxLifetime: 8 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	g := srv.Router.Sub("", middleware.LoadSession(sessions, false))
	h.Register(g)
	if o.extra != nil {
		o.extra(g, h)
	}
	handler, err := srv.Handler()
	if err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, w: w, srv: handler}
}

// reply is an answer, decoded.
type reply struct {
	Status int
	Header http.Header
	Raw    []byte
	Body   map[string]any
}

// as sends a request as a user, who signs in with a client API key. user 0 sends none.
func (r *rig) as(user uint64, method, target string) reply {
	r.t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	if user != 0 {
		req.Header.Set("Authorization", "Bearer "+keyIdentifier(user)+keySecret)
	}
	rec := httptest.NewRecorder()
	r.srv.ServeHTTP(rec, req)

	out := reply{Status: rec.Code, Header: rec.Header(), Raw: rec.Body.Bytes()}
	_ = json.Unmarshal(out.Raw, &out.Body)
	return out
}

// at walks a decoded body: strings are keys, integers are positions in a list. It returns nil when the path is not there.
func (rp reply) at(path ...any) any {
	var cur any = rp.Body
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := cur.(map[string]any)
			if !ok {
				return nil
			}
			cur = m[k]
		case int:
			l, ok := cur.([]any)
			if !ok || k >= len(l) {
				return nil
			}
			cur = l[k]
		}
	}
	return cur
}

// has reports whether a key is present, null or not.
func (rp reply) has(path ...any) bool {
	if len(path) == 0 {
		return true
	}
	parent, ok := rp.at(path[:len(path)-1]...).(map[string]any)
	if !ok {
		return false
	}
	_, found := parent[path[len(path)-1].(string)]
	return found
}

func (rp reply) detail() string {
	d, _ := rp.at("errors", 0, "detail").(string)
	return d
}

func (rp reply) code() string {
	d, _ := rp.at("errors", 0, "code").(string)
	return d
}

func (rp reply) strings(path ...any) []string {
	l, _ := rp.at(path...).([]any)
	out := make([]string, len(l))
	for i, v := range l {
		out[i], _ = v.(string)
	}
	return out
}

func (rp reply) length(path ...any) int {
	l, _ := rp.at(path...).([]any)
	return len(l)
}

var errBoom = errors.New("the database is down: password=hunter2")
