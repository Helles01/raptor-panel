package appnests

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apitest"
	"github.com/Helles01/raptor-panel/internal/api/appkit"
)

const (
	grace = 1 // may read nests and eggs
	bob   = 2 // may read nests only
	cleo  = 3 // may read eggs only
	dave  = 4 // not an administrator
)

var errBoom = errors.New("the database is down: password=hunter2")

var t0 = time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

type store struct {
	nests []appkit.Nest
	eggs  []appkit.Egg
	vars  map[uint64][]appkit.EggVariable
	fail  error

	lastLoad appkit.Includes
	page     [2]int
}

func (s *store) fillEgg(e appkit.Egg, load appkit.Includes) appkit.Egg {
	if load["nest"] {
		for _, n := range s.nests {
			if n.ID == e.NestID {
				n := n
				e.Nest = &n
			}
		}
	}
	if load["variables"] {
		e.Variables = s.vars[e.ID]
	}
	if load["config"] && e.ConfigFrom != nil {
		e.Inherited = &appkit.ConfigSet{Files: json.RawMessage(`{"a.yml":{"parser":"yaml"}}`), Startup: json.RawMessage(`{"done":"ready"}`), Stop: ptr("stop"), Logs: json.RawMessage(`{"custom":false}`)}
	}
	if load["script"] && e.CopyScriptFrom != nil {
		e.InheritedScript = &appkit.ScriptSet{Install: ptr("#!/bin/sh\necho borrowed"), Entry: "sh", Container: "alpine:3.19"}
	}
	return e
}

func (s *store) fillNest(n appkit.Nest, load appkit.Includes) appkit.Nest {
	if load["eggs"] {
		for _, e := range s.eggs {
			if e.NestID == n.ID {
				n.Eggs = append(n.Eggs, e)
			}
		}
	}
	return n
}

func (s *store) ListNests(_ context.Context, page, perPage int, load appkit.Includes) ([]appkit.Nest, int, error) {
	s.page, s.lastLoad = [2]int{page, perPage}, load
	if s.fail != nil {
		return nil, 0, s.fail
	}
	out := make([]appkit.Nest, len(s.nests))
	for i, n := range s.nests {
		out[i] = s.fillNest(n, load)
	}
	return out, len(out), nil
}

func (s *store) FindNest(_ context.Context, id uint64, load appkit.Includes) (appkit.Nest, error) {
	s.lastLoad = load
	if s.fail != nil {
		return appkit.Nest{}, s.fail
	}
	for _, n := range s.nests {
		if n.ID == id {
			return s.fillNest(n, load), nil
		}
	}
	return appkit.Nest{}, appkit.ErrNotFound
}

func (s *store) ListEggs(_ context.Context, nestID uint64, load appkit.Includes) ([]appkit.Egg, error) {
	s.lastLoad = load
	if s.fail != nil {
		return nil, s.fail
	}
	found := false
	for _, n := range s.nests {
		found = found || n.ID == nestID
	}
	if !found {
		return nil, appkit.ErrNotFound
	}
	out := []appkit.Egg{}
	for _, e := range s.eggs {
		if e.NestID == nestID {
			out = append(out, s.fillEgg(e, load))
		}
	}
	return out, nil
}

func (s *store) FindEgg(_ context.Context, nestID, id uint64, load appkit.Includes) (appkit.Egg, error) {
	s.lastLoad = load
	if s.fail != nil {
		return appkit.Egg{}, s.fail
	}
	for _, e := range s.eggs {
		if e.ID == id && e.NestID == nestID {
			return s.fillEgg(e, load), nil
		}
	}
	return appkit.Egg{}, appkit.ErrNotFound
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
	r.store = &store{
		nests: []appkit.Nest{
			{ID: 1, UUID: "a0000000-0000-4000-8000-000000000001", Author: "support@example.com", Name: "Minecraft", Description: ptr("Servers"), CreatedAt: t0, UpdatedAt: t0.Add(time.Minute)},
			{ID: 2, UUID: "a0000000-0000-4000-8000-000000000002", Author: "support@example.com", Name: "Source", CreatedAt: t0, UpdatedAt: t0},
		},
		eggs: []appkit.Egg{
			{
				ID: 10, UUID: "e0000000-0000-4000-8000-000000000010", NestID: 1, Name: "Vanilla", Author: "support@example.com", Description: ptr("The plain server"),
				DockerImages: []appkit.DockerImage{{Name: "Java 21", Image: "ghcr.io/y/java_21"}, {Name: "Java 17", Image: "ghcr.io/y/java_17"}},
				ConfigFiles:  json.RawMessage(`{"server.properties":{"parser":"properties","find":{"server-ip":"0.0.0.0"}}}`), ConfigStartup: json.RawMessage(`{"done":")! For help, type "}`),
				ConfigStop: ptr("stop"), ConfigLogs: json.RawMessage(`{"custom":false,"location":"logs/latest.log"}`), FileDenylist: []string{"a.txt"},
				Startup: "java -jar {{SERVER_JARFILE}}", ScriptPrivileged: true, ScriptInstall: ptr("#!/bin/bash\necho hi"), ScriptEntry: "bash", ScriptContainer: "ghcr.io/y/installers:alpine",
				CreatedAt: t0, UpdatedAt: t0.Add(time.Hour),
			},
			{
				ID: 11, UUID: "e0000000-0000-4000-8000-000000000011", NestID: 1, Name: "Paper", Author: "support@example.com", ConfigFiles: json.RawMessage(`[]`),
				ConfigFrom: ptr(uint64(10)), CopyScriptFrom: ptr(uint64(10)), Startup: "java -jar paper.jar", ScriptEntry: "bash", ScriptContainer: "alpine", CreatedAt: t0, UpdatedAt: t0,
			},
		},
		vars: map[uint64][]appkit.EggVariable{10: {{
			ID: 100, EggID: 10, Name: "Server Jar", Description: "The jar", EnvVariable: "SERVER_JARFILE", DefaultValue: "server.jar", UserViewable: true, UserEditable: false,
			Rules: "required|string", CreatedAt: t0, UpdatedAt: t0.Add(time.Second),
		}}},
	}

	w := apitest.NewWorld()
	for id, admin := range map[uint64]bool{grace: true, bob: true, cleo: true, dave: false} {
		w.AddUser(id, admin)
	}
	w.GrantApp(grace, "nests=read", "eggs=read")
	w.GrantApp(bob, "nests=read")
	w.GrantApp(cleo, "eggs=read")
	w.GrantApp(dave, "nests=read", "eggs=read")

	h, err := New(Deps{Store: r.store, Logger: slog.New(slog.NewTextHandler(r.logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	r.handlers = h
	r.client = w.Serve(t, func(g api.Group) { h.Register(g) })
	return r
}

func (r *rig) do(user uint64, path string) apitest.Reply {
	r.t.Helper()
	return r.client.AsApp(user, "GET", "/api/application/nests"+path, "")
}

func TestEveryRouteNeedsItsResource(t *testing.T) {
	routes := []struct {
		name, path string
		allowed    []uint64 // the users, besides grace, who may
	}{
		{"nests", "", []uint64{bob}},
		{"nest", "/1", []uint64{bob}},
		{"eggs", "/1/eggs", []uint64{cleo}},
		{"egg", "/1/eggs/10", []uint64{cleo}},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			r := newRig(t)
			for _, user := range []uint64{grace, bob, cleo, dave} {
				open := user == grace
				for _, a := range rt.allowed {
					open = open || a == user
				}
				rp := r.do(user, rt.path)
				if open && rp.Status != 200 || !open && rp.Status != 403 {
					t.Errorf("user %d: status = %d: %s", user, rp.Status, rp.Raw)
				}
			}
			if rp := r.do(0, rt.path); rp.Status != 401 {
				t.Errorf("nobody: status = %d", rp.Status)
			}
			if rp := r.client.As(grace, "GET", "/api/application/nests"+rt.path, ""); rp.Status != 403 {
				t.Errorf("a client key: status = %d", rp.Status)
			}
			// The routes only read.
			if rp := r.client.AsApp(grace, "POST", "/api/application/nests"+rt.path, `{}`); rp.Status != 404 && rp.Status != 405 {
				t.Errorf("POST: status = %d", rp.Status)
			}
		})
	}
}

func TestNests(t *testing.T) {
	r := newRig(t)
	rp := r.do(bob, "?per_page=1&filter[name]=ignored&sort=name")
	if rp.Status != 200 || rp.At("object") != "list" || rp.At("meta", "pagination", "total") != 2.0 || rp.At("meta", "pagination", "per_page") != 1.0 || r.store.page != [2]int{1, 1} {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	want := map[string]any{
		"id": 1.0, "uuid": "a0000000-0000-4000-8000-000000000001", "author": "support@example.com", "name": "Minecraft", "description": "Servers",
		"created_at": "2026-10-01T10:00:00+00:00", "updated_at": "2026-10-01T10:01:00+00:00",
	}
	if rp.At("data", 0, "object") != "nest" || !reflect.DeepEqual(rp.At("data", 0, "attributes"), want) {
		t.Errorf("first = %v", rp.At("data", 0, "attributes"))
	}
	rp = r.do(bob, "/2")
	if rp.Status != 200 || rp.At("attributes", "description") != nil {
		t.Errorf("a nest without a description: %d %s", rp.Status, rp.Raw)
	}
	for _, path := range []string{"/99", "/0", "/x"} {
		if rp := r.do(bob, path); rp.Status != 404 {
			t.Errorf("%s: status = %d", path, rp.Status)
		}
	}
	if rp := r.do(bob, "?per_page=101"); rp.Status != 422 {
		t.Errorf("per_page 101: %d", rp.Status)
	}
}

func TestNestIncludes(t *testing.T) {
	r := newRig(t)
	rp := r.do(grace, "/1?include=eggs")
	if rp.Status != 200 || rp.At("relationships", "eggs", "object") != "list" || rp.At("relationships", "eggs", "data", 1, "attributes", "name") != "Paper" || rp.At("relationships", "eggs", "data", 0, "object") != "egg" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if rp := r.do(grace, "/2?include=eggs"); len(rp.At("relationships", "eggs", "data").([]any)) != 0 {
		t.Errorf("a nest without eggs: %s", rp.Raw)
	}
	// Without the right to read eggs the nest is shown with a null resource, and the store is not asked.
	r = newRig(t)
	rp = r.do(bob, "/1?include=eggs")
	if rp.At("relationships", "eggs", "object") != "null_resource" || len(r.store.lastLoad) != 0 {
		t.Errorf("answer = %s, load = %v", rp.Raw, r.store.lastLoad)
	}
	if rp := r.do(grace, "/1?include=servers"); rp.Has("relationships", "servers") {
		t.Errorf("servers shown: %s", rp.Raw)
	}
}

func TestEgg(t *testing.T) {
	r := newRig(t)
	rp := r.do(grace, "/1/eggs/10")
	if rp.Status != 200 || rp.At("object") != "egg" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	want := map[string]any{
		"id": 10.0, "uuid": "e0000000-0000-4000-8000-000000000010", "name": "Vanilla", "nest": 1.0, "author": "support@example.com", "description": "The plain server",
		"docker_image":  "ghcr.io/y/java_21",
		"docker_images": map[string]any{"Java 21": "ghcr.io/y/java_21", "Java 17": "ghcr.io/y/java_17"},
		"config": map[string]any{
			"files":         map[string]any{"server.properties": map[string]any{"parser": "properties", "find": map[string]any{"server-ip": "0.0.0.0"}}},
			"startup":       map[string]any{"done": ")! For help, type "},
			"stop":          "stop",
			"logs":          map[string]any{"custom": false, "location": "logs/latest.log"},
			"file_denylist": []any{"a.txt"},
			"extends":       nil,
		},
		"startup": "java -jar {{SERVER_JARFILE}}",
		"script": map[string]any{
			"privileged": true, "install": "#!/bin/bash\necho hi", "entry": "bash", "container": "ghcr.io/y/installers:alpine", "extends": nil,
		},
		"created_at": "2026-10-01T10:00:00+00:00", "updated_at": "2026-10-01T11:00:00+00:00",
	}
	if !reflect.DeepEqual(rp.At("attributes"), want) {
		t.Errorf("attributes = %v", rp.At("attributes"))
	}
	// The first image is the default one, so the order the egg has them in is kept.
	if !strings.Contains(string(rp.Raw), `"docker_images":{"Java 21":"ghcr.io/y/java_21","Java 17":"ghcr.io/y/java_17"}`) {
		t.Errorf("images: %s", rp.Raw)
	}

	// An egg that borrows has no files, which are an empty object and not a list, and no startup.
	rp = r.do(grace, "/1/eggs/11")
	if rp.Status != 200 || !strings.Contains(string(rp.Raw), `"files":{}`) || rp.At("attributes", "config", "startup") != nil || rp.At("attributes", "config", "extends") != 10.0 ||
		rp.At("attributes", "script", "extends") != 10.0 || rp.At("attributes", "docker_image") != "" || !strings.Contains(string(rp.Raw), `"docker_images":{}`) ||
		!strings.Contains(string(rp.Raw), `"file_denylist":[]`) {
		t.Errorf("borrowing egg: %d %s", rp.Status, rp.Raw)
	}

	rp = r.do(grace, "/1/eggs")
	if rp.Status != 200 || rp.At("object") != "list" || len(rp.At("data").([]any)) != 2 || rp.Has("meta") {
		t.Errorf("list: %d %s", rp.Status, rp.Raw)
	}
	if rp := r.do(grace, "/2/eggs"); rp.Status != 200 || len(rp.At("data").([]any)) != 0 {
		t.Errorf("a nest without eggs: %d %s", rp.Status, rp.Raw)
	}
	// An egg is found among the eggs of its own nest.
	for _, path := range []string{"/2/eggs/10", "/99/eggs", "/99/eggs/10", "/1/eggs/99", "/1/eggs/x", "/x/eggs"} {
		if rp := r.do(grace, path); rp.Status != 404 {
			t.Errorf("%s: status = %d", path, rp.Status)
		}
	}
}

func TestEggIncludes(t *testing.T) {
	r := newRig(t)
	rp := r.do(grace, "/1/eggs/10?include=nest,variables,config,script")
	if rp.Status != 200 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if rp.At("relationships", "nest", "object") != "nest" || rp.At("relationships", "nest", "attributes", "name") != "Minecraft" {
		t.Errorf("nest = %v", rp.At("relationships", "nest"))
	}
	wantVar := map[string]any{
		"id": 100.0, "egg_id": 10.0, "name": "Server Jar", "description": "The jar", "env_variable": "SERVER_JARFILE", "default_value": "server.jar",
		"user_viewable": true, "user_editable": false, "rules": "required|string", "created_at": "2026-10-01T10:00:00.000000Z", "updated_at": "2026-10-01T10:00:01.000000Z",
	}
	if rp.At("relationships", "variables", "object") != "list" || rp.At("relationships", "variables", "data", 0, "object") != "egg_variable" ||
		!reflect.DeepEqual(rp.At("relationships", "variables", "data", 0, "attributes"), wantVar) {
		t.Errorf("variables = %v", rp.At("relationships", "variables"))
	}
	// An egg that borrows nothing has nothing to include.
	if rp.At("relationships", "config", "object") != "null_resource" || rp.At("relationships", "script", "object") != "null_resource" {
		t.Errorf("config = %v, script = %v", rp.At("relationships", "config"), rp.At("relationships", "script"))
	}

	// One that borrows is shown what it gets: an item without an object name, as the reference's serializer writes it.
	rp = r.do(grace, "/1/eggs/11?include=config,script")
	if !strings.Contains(string(rp.Raw), `"config":{"object":null,"attributes":`) || !strings.Contains(string(rp.Raw), `"script":{"object":null,"attributes":`) {
		t.Fatalf("borrowing: %s", rp.Raw)
	}
	cfg := rp.At("relationships", "config", "attributes").(map[string]any)
	if cfg["stop"] != "stop" || !reflect.DeepEqual(cfg["startup"], map[string]any{"done": "ready"}) || !reflect.DeepEqual(cfg["files"], map[string]any{"a.yml": map[string]any{"parser": "yaml"}}) {
		t.Errorf("config = %v", cfg)
	}
	scr := rp.At("relationships", "script", "attributes").(map[string]any)
	if scr["install"] != "#!/bin/sh\necho borrowed" || scr["entry"] != "sh" || scr["container"] != "alpine:3.19" || scr["privileged"] != false {
		t.Errorf("script = %v", scr)
	}

	// Without the right to read nests or eggs, those includes are null; the egg's own are shown whatever the key.
	r = newRig(t)
	rp = r.do(cleo, "/1/eggs/10?include=nest,variables,config")
	if rp.At("relationships", "nest", "object") != "null_resource" || rp.At("relationships", "variables", "object") != "list" || rp.Has("relationships", "config", "attributes") && rp.At("relationships", "config", "object") != "null_resource" {
		t.Errorf("answer = %s", rp.Raw)
	}
	if r.store.lastLoad["nest"] || !r.store.lastLoad["variables"] {
		t.Errorf("load = %v", r.store.lastLoad)
	}
	if rp := r.do(grace, "/1/eggs?include=nest,servers"); rp.At("data", 0, "relationships", "nest", "object") != "nest" || rp.Has("data", 0, "relationships", "servers") {
		t.Errorf("list includes: %s", rp.Raw)
	}
}

func TestStoreFailuresAreNotShown(t *testing.T) {
	for _, path := range []string{"", "/1", "/1/eggs", "/1/eggs/10"} {
		r := newRig(t)
		r.store.fail = errBoom
		rp := r.do(grace, path)
		if rp.Status != 500 || rp.Contains("hunter2") {
			t.Errorf("%s: %d %s", path, rp.Status, rp.Raw)
		}
	}
}

func TestHandlersRefuseWithoutTheAuthorizer(t *testing.T) {
	r := newRig(t)
	client := apitest.NewWorld().Serve(t, func(g api.Group) {
		b := g.Sub("/bare")
		b.GET("", r.handlers.ListNests, api.Public())
		b.GET("/nest", r.handlers.ViewNest, api.Public())
		b.GET("/eggs", r.handlers.ListEggs, api.Public())
		b.GET("/egg", r.handlers.ViewEgg, api.Public())
	})
	for _, path := range []string{"/bare", "/bare/nest", "/bare/eggs", "/bare/egg"} {
		if rp := client.As(0, "GET", path, ""); rp.Status != 403 {
			t.Errorf("%s: status = %d, want 403", path, rp.Status)
		}
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
