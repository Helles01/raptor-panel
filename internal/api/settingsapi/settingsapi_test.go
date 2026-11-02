package settingsapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apitest"
	"github.com/Helles01/raptor-panel/internal/api/clientapi"
	"github.com/Helles01/raptor-panel/internal/servers"
	"github.com/Helles01/raptor-panel/internal/wings"
)

const (
	ada   = 1 // owner
	grace = 2 // administrator
	bob   = 3 // holds the rights of these routes
	cleo  = 4 // may only read the startup
	dave  = 5 // may only set variables
	eve   = 6 // a stranger
)

const serverUUID = "aaaa1111-2222-4333-8444-555555555555"

var errBoom = errors.New("the database is down: password=hunter2")

type setVar struct {
	Env   string
	Value string
}

type statusCall struct{ From, To string }

type store struct {
	mu sync.Mutex

	server   Server
	missing  bool
	fail     error
	vars     []setVar
	images   []string
	renames  [][2]string
	statuses []statusCall
	race     bool
	failBack bool // putting the status back fails
}

func (s *store) Server(_ context.Context, _ uint64) (Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return Server{}, s.fail
	}
	if s.missing {
		return Server{}, ErrNotFound
	}
	out := s.server
	out.Variables = append([]clientapi.Variable(nil), s.server.Variables...)
	return out, nil
}

func (s *store) SetVariable(_ context.Context, _ uint64, env, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vars = append(s.vars, setVar{env, value})
	return s.fail
}

func (s *store) SetImage(_ context.Context, _ uint64, image string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.images = append(s.images, image)
	return s.fail
}

func (s *store) Rename(_ context.Context, _ uint64, name, description string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renames = append(s.renames, [2]string{name, description})
	return s.fail
}

func (s *store) SetStatus(_ context.Context, _ uint64, from, to string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses = append(s.statuses, statusCall{from, to})
	if s.failBack && to == s.server.Status {
		return false, errBoom
	}
	return !s.race, nil
}

type reinstaller struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (r *reinstaller) Reinstall(_ context.Context, _ uint64, uuid string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, uuid)
	return r.err
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
	node     *reinstaller
	activity *activityLog
	logs     *bytes.Buffer
	handlers *Handlers
	world    *apitest.World
}

func s(v string) *string { return &v }

func newServer() Server {
	return Server{
		Server: clientapi.Server{
			ID: 100, UUID: serverUUID, OwnerID: ada, Name: "Survival", Description: "The main world", Memory: 1024,
			Startup: "java -Xmx{{SERVER_MEMORY}}M -jar {{JAR}} {{MOTD}} {{SECRET}}", Image: "ghcr.io/example/java:21",
			Primary: clientapi.Allocation{ID: 1, IP: "10.0.0.5", Port: 25565},
			Variables: []clientapi.Variable{
				{Name: "Jar", Description: "The jar", EnvName: "JAR", Default: "server.jar", UserViewable: true, UserEditable: true, Rules: "required|string|max:20"},
				{Name: "Message", EnvName: "MOTD", Default: "hello", ServerValue: s("hi"), UserViewable: true, UserEditable: true, Rules: "nullable|string|max:10"},
				{Name: "Locked", EnvName: "LOCKED", Default: "x", UserViewable: true, Rules: "string"},
				{Name: "Secret", EnvName: "SECRET", Default: "hunter2", UserEditable: true, Rules: "string"},
			},
		},
		EggImages: Images{{"Java 21", "ghcr.io/example/java:21"}, {"Java 17", "ghcr.io/example/java:17"}},
	}
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, store: &store{server: newServer()}, node: &reinstaller{}, activity: &activityLog{}, logs: &bytes.Buffer{}}

	w := apitest.NewWorld()
	for id, admin := range map[uint64]bool{ada: false, grace: true, bob: false, cleo: false, dave: false, eve: false} {
		w.AddUser(id, admin)
	}
	w.AddServer(100, serverUUID, ada)
	w.Subusers[[2]uint64{100, bob}] = []string{"startup.read", "startup.update", "startup.docker-image", "settings.rename", "settings.reinstall", "websocket.connect"}
	w.Subusers[[2]uint64{100, cleo}] = []string{"startup.read", "websocket.connect"}
	w.Subusers[[2]uint64{100, dave}] = []string{"startup.update", "websocket.connect"}
	r.world = w

	h, err := New(Deps{Store: r.store, Reinstaller: r.node, Activity: r.activity, Logger: slog.New(slog.NewTextHandler(r.logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	r.handlers = h
	r.client = w.Serve(t, func(g api.Group) { h.Register(g) })
	return r
}

const base = "/api/client/servers/aaaa1111"

func (r *rig) do(user uint64, method, path, body string) apitest.Reply {
	r.t.Helper()
	return r.client.As(user, method, base+path, body)
}

func TestStartup(t *testing.T) {
	r := newRig(t)
	rp := r.do(ada, "GET", "/startup", "")
	if rp.Status != 200 || rp.At("object") != "list" {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	// The variables a user may see; the others are not mentioned.
	l, _ := rp.At("data").([]any)
	if len(l) != 3 || rp.At("data", 0, "object") != "egg_variable" || rp.At("data", 0, "attributes", "env_variable") != "JAR" || rp.At("data", 1, "attributes", "server_value") != "hi" {
		t.Errorf("data = %s", rp.Raw)
	}
	if rp.Contains("SECRET") && !rp.Contains("{{SECRET}}") {
		t.Errorf("a variable that is not viewable is named: %s", rp.Raw)
	}
	if rp.At("meta", "startup_command") != "java -Xmx1024M -jar server.jar hi [hidden]" {
		t.Errorf("startup_command = %v", rp.At("meta", "startup_command"))
	}
	if rp.At("meta", "raw_startup_command") != "java -Xmx{{SERVER_MEMORY}}M -jar {{JAR}} {{MOTD}} {{SECRET}}" {
		t.Errorf("raw_startup_command = %v", rp.At("meta", "raw_startup_command"))
	}
	// The images keep the order of the egg.
	if !strings.Contains(string(rp.Raw), `"docker_images":{"Java 21":"ghcr.io/example/java:21","Java 17":"ghcr.io/example/java:17"}`) {
		t.Errorf("docker_images = %s", rp.Raw)
	}
}

func TestEveryRouteHasItsPermission(t *testing.T) {
	routes := []struct {
		name, method, path, body string
		allowed                  []uint64 // besides the owner and the administrator
	}{
		{"startup", "GET", "/startup", "", []uint64{bob, cleo}},
		{"variable", "PUT", "/startup/variable", `{"key":"JAR","value":"a.jar"}`, []uint64{bob, dave}},
		{"rename", "POST", "/settings/rename", `{"name":"New"}`, []uint64{bob}},
		{"image", "PUT", "/settings/docker-image", `{"docker_image":"ghcr.io/example/java:17"}`, []uint64{bob}},
		{"reinstall", "POST", "/settings/reinstall", ``, []uint64{bob}},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			for _, user := range []uint64{ada, grace, bob, cleo, dave} {
				r := newRig(t)
				rp := r.do(user, rt.method, rt.path, rt.body)
				want := 403
				if user == ada || user == grace || slicesContains(rt.allowed, user) {
					want = 0 // any success
				}
				if want == 403 && rp.Status != 403 || want == 0 && (rp.Status < 200 || rp.Status > 299) {
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

func slicesContains(l []uint64, x uint64) bool {
	for _, v := range l {
		if v == x {
			return true
		}
	}
	return false
}

func TestSetVariable(t *testing.T) {
	r := newRig(t)

	rp := r.do(ada, "PUT", "/startup/variable", `{"key":"JAR","value":"  paper.jar  "}`)
	if rp.Status != 200 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if !reflect.DeepEqual(r.store.vars, []setVar{{"JAR", "paper.jar"}}) {
		t.Errorf("stored %+v", r.store.vars)
	}
	if rp.At("object") != "egg_variable" || rp.At("attributes", "server_value") != "paper.jar" || rp.At("attributes", "env_variable") != "JAR" {
		t.Errorf("answer = %s", rp.Raw)
	}
	if rp.At("meta", "startup_command") != "java -Xmx1024M -jar paper.jar hi [hidden]" || rp.At("meta", "raw_startup_command") == nil {
		t.Errorf("meta = %v", rp.At("meta"))
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:startup.edit" ||
		r.activity.entries[0].Properties != `{"new":"paper.jar","old":null,"variable":"JAR"}` {
		t.Errorf("entries = %+v", r.activity.entries)
	}
}

func TestSetVariableValues(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		stored    string
		logged    bool
		shown     any
		oldInProp string
	}{
		{"a changed value", `{"key":"MOTD","value":"yo"}`, "yo", true, "yo", `"old":"hi"`},
		{"the same value", `{"key":"MOTD","value":"hi"}`, "hi", false, "hi", ""},
		{"null clears an optional one", `{"key":"MOTD","value":null}`, "", true, nil, `"new":""`},
		{"blank clears it too", `{"key":"MOTD","value":"   "}`, "", true, nil, `"new":""`},
		{"a number is taken as text", `{"key":"MOTD","value":12}`, "12", true, "12", `"new":"12"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(ada, "PUT", "/startup/variable", tt.body)
			if rp.Status != 200 {
				t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
			}
			if !reflect.DeepEqual(r.store.vars, []setVar{{"MOTD", tt.stored}}) {
				t.Errorf("stored %+v, want %q", r.store.vars, tt.stored)
			}
			if got := rp.At("attributes", "server_value"); got != tt.shown {
				t.Errorf("server_value = %v, want %v", got, tt.shown)
			}
			if tt.logged != (len(r.activity.entries) == 1) || (tt.logged && !strings.Contains(r.activity.entries[0].Properties, tt.oldInProp)) {
				t.Errorf("entries = %+v", r.activity.entries)
			}
		})
	}
}

func TestSetVariableRefusals(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		status int
		detail string
		count  int // entries of the answer
	}{
		{"a variable that does not exist", `{"key":"NOPE","value":"x"}`, 400, "The environment variable you are trying to edit does not exist.", 1},
		{"a variable the user may not see", `{"key":"SECRET","value":"x"}`, 400, "The environment variable you are trying to edit does not exist.", 1},
		{"a variable that is read-only", `{"key":"LOCKED","value":"x"}`, 400, "The environment variable you are trying to edit is read-only.", 1},
		{"a value the rules refuse", `{"key":"JAR","value":"` + strings.Repeat("a", 21) + `"}`, 422, "", 1},
		{"a required value left empty", `{"key":"JAR","value":null}`, 422, "The value field is required.", 1},
		{"no key", `{"value":"x"}`, 422, "The key field is required.", 1},
		{"a key that is not text", `{"key":5,"value":"x"}`, 422, "The key must be a string.", 1},
		{"no value", `{"key":"JAR"}`, 422, "The value field must be present.", 1},
		{"a value that is a flag", `{"key":"JAR","value":true}`, 422, "The value must be a string or a number.", 1},
		{"not JSON", `key=JAR`, 400, "", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(ada, "PUT", "/startup/variable", tt.body)
			if rp.Status != tt.status {
				t.Fatalf("status = %d, want %d: %s", rp.Status, tt.status, rp.Raw)
			}
			if tt.detail != "" && rp.Detail() != tt.detail {
				t.Errorf("detail = %q, want %q", rp.Detail(), tt.detail)
			}
			if len(r.store.vars) != 0 || len(r.activity.entries) != 0 {
				t.Error("something was stored or logged for a refused request")
			}
		})
	}
}

func TestEachBrokenRuleIsOneMessage(t *testing.T) {
	r := newRig(t)
	r.store.server.Variables[0].Rules = "integer|min:10|max:5"
	rp := r.do(ada, "PUT", "/startup/variable", `{"key":"JAR","value":"abc"}`)
	errs, _ := rp.At("errors").([]any)
	if rp.Status != 422 || len(errs) < 2 || rp.At("errors", 0, "meta", "source_field") != "value" {
		t.Errorf("answer = %d %s", rp.Status, rp.Raw)
	}
}

func TestRename(t *testing.T) {
	r := newRig(t)
	rp := r.do(ada, "POST", "/settings/rename", `{"name":"New name","description":"New description"}`)
	if rp.Status != 204 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if !reflect.DeepEqual(r.store.renames, [][2]string{{"New name", "New description"}}) {
		t.Errorf("stored %+v", r.store.renames)
	}
	if len(r.activity.entries) != 2 || r.activity.entries[0].Event != "server:settings.rename" || r.activity.entries[0].Properties != `{"new":"New name","old":"Survival"}` ||
		r.activity.entries[1].Event != "server:settings.description" || r.activity.entries[1].Properties != `{"new":"New description","old":"The main world"}` {
		t.Errorf("entries = %+v", r.activity.entries)
	}
}

func TestRenameKeepsAndClearsTheDescription(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		description string
		entries     int
	}{
		{"not sent: kept", `{"name":"Survival 2"}`, "The main world", 1},
		{"null: cleared", `{"name":"Survival","description":null}`, "", 1},
		{"blank: cleared", `{"name":"Survival","description":"  "}`, "", 1},
		{"the same: nothing logged", `{"name":"Survival","description":"The main world"}`, "The main world", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			if rp := r.do(ada, "POST", "/settings/rename", tt.body); rp.Status != 204 {
				t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
			}
			if r.store.renames[0][1] != tt.description || len(r.activity.entries) != tt.entries {
				t.Errorf("stored %+v, %d entries", r.store.renames, len(r.activity.entries))
			}
		})
	}
}

func TestRenameRefusals(t *testing.T) {
	for name, tt := range map[string]struct{ body, detail string }{
		"no name":            {`{}`, "The name field is required."},
		"a blank name":       {`{"name":" "}`, "The name field is required."},
		"a name of a list":   {`{"name":["a"]}`, "The name must be a string."},
		"a name too long":    {`{"name":"` + strings.Repeat("a", 192) + `"}`, ""},
		"a description list": {`{"name":"x","description":["a"]}`, "The description must be a string."},
		"control characters": {`{"name":"a\u0000b"}`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(ada, "POST", "/settings/rename", tt.body)
			if rp.Status != 422 || (tt.detail != "" && rp.Detail() != tt.detail) {
				t.Errorf("answer = %d %s", rp.Status, rp.Raw)
			}
			if len(r.store.renames) != 0 {
				t.Error("a rename was stored")
			}
		})
	}
}

func TestSetImage(t *testing.T) {
	r := newRig(t)
	if rp := r.do(ada, "PUT", "/settings/docker-image", `{"docker_image":"ghcr.io/example/java:17"}`); rp.Status != 204 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if !reflect.DeepEqual(r.store.images, []string{"ghcr.io/example/java:17"}) {
		t.Errorf("stored %v", r.store.images)
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:startup.image" ||
		r.activity.entries[0].Properties != `{"new":"ghcr.io/example/java:17","old":"ghcr.io/example/java:21"}` {
		t.Errorf("entries = %+v", r.activity.entries)
	}

	// The image it has already: nothing to store or log.
	r = newRig(t)
	if rp := r.do(ada, "PUT", "/settings/docker-image", `{"docker_image":"ghcr.io/example/java:21"}`); rp.Status != 204 || len(r.store.images) != 0 || len(r.activity.entries) != 0 {
		t.Errorf("status = %d, stored %v, logged %v", rp.Status, r.store.images, r.activity.entries)
	}
}

func TestSetImageRefusals(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		status  int
		prepare func(*rig)
	}{
		{"an image the egg does not offer", `{"docker_image":"evil/image:latest"}`, 422, nil},
		{"no image", `{}`, 422, nil},
		{"a blank image", `{"docker_image":"  "}`, 422, nil},
		{"an image that is not text", `{"docker_image":5}`, 422, nil},
		{"an image of odd characters", `{"docker_image":"a;b"}`, 422, nil},
		{"an image too long", `{"docker_image":"` + strings.Repeat("a", 192) + `"}`, 422, nil},
		{"a server that uses an image set by hand", `{"docker_image":"ghcr.io/example/java:17"}`, 400, func(r *rig) { r.store.server.Image = "registry.local/custom:1" }},
		{"the request is checked before the lock", `{"docker_image":"evil/image:latest"}`, 422, func(r *rig) { r.store.server.Image = "registry.local/custom:1" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			if tt.prepare != nil {
				tt.prepare(r)
			}
			rp := r.do(ada, "PUT", "/settings/docker-image", tt.body)
			if rp.Status != tt.status {
				t.Fatalf("status = %d, want %d: %s", rp.Status, tt.status, rp.Raw)
			}
			if tt.status == 400 && rp.Detail() != "This server's Docker image has been manually set by an administrator and cannot be updated." {
				t.Errorf("detail = %q", rp.Detail())
			}
			if len(r.store.images) != 0 || len(r.activity.entries) != 0 {
				t.Error("something was stored or logged")
			}
		})
	}
}

func TestReinstall(t *testing.T) {
	r := newRig(t)
	rp := r.do(ada, "POST", "/settings/reinstall", "")
	if rp.Status != 202 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if !reflect.DeepEqual(r.store.statuses, []statusCall{{"", "installing"}}) || !reflect.DeepEqual(r.node.calls, []string{serverUUID}) {
		t.Errorf("statuses %+v, node %v", r.store.statuses, r.node.calls)
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:reinstall" || r.activity.entries[0].Properties != "{}" {
		t.Errorf("entries = %+v", r.activity.entries)
	}
}

func TestReinstallFromAFailedState(t *testing.T) {
	for _, status := range []string{servers.StatusInstallFailed, servers.StatusReinstallFailed} {
		r := newRig(t)
		r.store.server.Status = status
		r.store.server.SkipScripts = true // allowed all the same: the report is the way out of a failure
		// The state of the server for the authorizer.
		for i := range r.world.Servers {
			r.world.Servers[i].State = servers.State{Status: status}
		}
		rp := r.do(ada, "POST", "/settings/reinstall", "")
		if rp.Status != 202 || !reflect.DeepEqual(r.store.statuses, []statusCall{{status, "installing"}}) {
			t.Errorf("%s: status = %d, statuses %+v: %s", status, rp.Status, r.store.statuses, rp.Raw)
		}
	}
}

func TestReinstallRefusals(t *testing.T) {
	t.Run("a server that skips its install script", func(t *testing.T) {
		r := newRig(t)
		r.store.server.SkipScripts = true
		rp := r.do(ada, "POST", "/settings/reinstall", "")
		if rp.Status != 400 || rp.Code() != "DisplayException" || len(r.store.statuses) != 0 || len(r.node.calls) != 0 {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
	t.Run("a server that is installing", func(t *testing.T) {
		r := newRig(t)
		r.world.Servers[0].State = servers.State{Status: servers.StatusInstalling}
		if rp := r.do(ada, "POST", "/settings/reinstall", ""); rp.Status != 409 || len(r.node.calls) != 0 {
			t.Errorf("status = %d", rp.Status)
		}
	})
	t.Run("a server that is suspended", func(t *testing.T) {
		r := newRig(t)
		r.world.Servers[0].State = servers.State{Status: servers.StatusSuspended}
		if rp := r.do(ada, "POST", "/settings/reinstall", ""); rp.Status != 409 {
			t.Errorf("status = %d", rp.Status)
		}
	})
	t.Run("a status that changed meanwhile", func(t *testing.T) {
		r := newRig(t)
		r.store.race = true
		rp := r.do(ada, "POST", "/settings/reinstall", "")
		if rp.Status != 409 || len(r.node.calls) != 0 || len(r.activity.entries) != 0 {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
}

func TestReinstallThatTheNodeRefusesIsTakenBack(t *testing.T) {
	r := newRig(t)
	r.node.err = &wings.Error{Status: 409, NodeStatus: 409, Message: "An error occurred on the remote host: busy. (request id: <nil>)"}
	rp := r.do(ada, "POST", "/settings/reinstall", "")
	if rp.Status != 409 || rp.Code() != "DaemonConnectionException" {
		t.Errorf("answer = %d %s", rp.Status, rp.Raw)
	}
	if !reflect.DeepEqual(r.store.statuses, []statusCall{{"", "installing"}, {"installing", ""}}) {
		t.Errorf("statuses = %+v: the mark must be taken back", r.store.statuses)
	}
	if len(r.activity.entries) != 0 {
		t.Error("a reinstall that did not start was logged")
	}

	r = newRig(t)
	r.node.err = errBoom
	r.store.failBack = true
	rp = r.do(ada, "POST", "/settings/reinstall", "")
	if rp.Status != 500 || rp.Contains("hunter2") || !strings.Contains(r.logs.String(), "was not put back") {
		t.Errorf("answer = %d %s, logs %s", rp.Status, rp.Raw, r.logs.String())
	}
}

func TestStoreFailuresDoNotLeak(t *testing.T) {
	for name, call := range map[string]func(*rig) apitest.Reply{
		"startup":  func(r *rig) apitest.Reply { return r.do(ada, "GET", "/startup", "") },
		"variable": func(r *rig) apitest.Reply { return r.do(ada, "PUT", "/startup/variable", `{"key":"JAR","value":"a"}`) },
		"rename":   func(r *rig) apitest.Reply { return r.do(ada, "POST", "/settings/rename", `{"name":"x"}`) },
		"image": func(r *rig) apitest.Reply {
			return r.do(ada, "PUT", "/settings/docker-image", `{"docker_image":"ghcr.io/example/java:17"}`)
		},
		"reinstall": func(r *rig) apitest.Reply { return r.do(ada, "POST", "/settings/reinstall", "") },
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
	t.Run("a server that is gone", func(t *testing.T) {
		r := newRig(t)
		r.store.missing = true
		if rp := r.do(ada, "GET", "/startup", ""); rp.Status != 404 {
			t.Errorf("status = %d", rp.Status)
		}
	})
}

func TestAnEntryThatCannotBeStoredDoesNotUndoTheChange(t *testing.T) {
	r := newRig(t)
	r.activity.err = errBoom
	if rp := r.do(ada, "POST", "/settings/rename", `{"name":"Other"}`); rp.Status != 204 || len(r.store.renames) != 1 {
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
		b.GET("/startup", r.handlers.Startup, api.Public())
		b.PUT("/variable", r.handlers.SetVariable, api.Public())
		b.POST("/rename", r.handlers.Rename, api.Public())
		b.PUT("/image", r.handlers.SetImage, api.Public())
		b.POST("/reinstall", r.handlers.Reinstall, api.Public())
	})
	for _, rt := range []struct{ method, path string }{{"GET", "/bare/startup"}, {"PUT", "/bare/variable"}, {"POST", "/bare/rename"}, {"PUT", "/bare/image"}, {"POST", "/bare/reinstall"}} {
		if rp := client.As(0, rt.method, rt.path, `{}`); rp.Status != 403 {
			t.Errorf("%s %s: status = %d, want 403", rt.method, rt.path, rp.Status)
		}
	}
	if len(r.store.statuses)+len(r.store.vars)+len(r.node.calls) != 0 {
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
		"no store": func(d *Deps) { d.Store = nil }, "no reinstaller": func(d *Deps) { d.Reinstaller = nil },
		"no activity": func(d *Deps) { d.Activity = nil }, "no logger": func(d *Deps) { d.Logger = nil },
	} {
		d := full
		mutate(&d)
		if _, err := New(d); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
