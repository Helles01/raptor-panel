package appservers

import (
	"reflect"
	"testing"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apitest"
)

const createBodyOK = `{"name":"New","user":3,"egg":10,"docker_image":"ghcr.io/y/java_21","startup":"java -jar server.jar","environment":{"SERVER_JARFILE":"server.jar"},` +
	`"limits":{"memory":512,"swap":0,"disk":1000,"io":500,"cpu":0},"feature_limits":{"databases":1,"allocations":2,"backups":3},"allocation":{"default":8}}`

const startupBodyOK = `{"startup":"java -jar paper.jar","environment":{"SERVER_JARFILE":"paper.jar"},"egg":10,"image":"ghcr.io/y/java_17","skip_scripts":false}`

func TestEveryRouteNeedsItsLevel(t *testing.T) {
	routes := []struct {
		name, method, path, body string
		write                    bool
	}{
		{"list", "GET", "", "", false},
		{"view", "GET", "/7", "", false},
		{"external", "GET", "/external/crm-7", "", false},
		{"create", "POST", "", createBodyOK, true},
		{"details", "PATCH", "/7/details", `{"name":"Renamed","user":3}`, true},
		{"build", "PATCH", "/7/build", `{"feature_limits":{"databases":1,"backups":0}}`, true},
		{"startup", "PATCH", "/7/startup", startupBodyOK, true},
		{"suspend", "POST", "/7/suspend", "", true},
		{"unsuspend", "POST", "/8/unsuspend", "", true},
		{"reinstall", "POST", "/7/reinstall", "", true},
		{"delete", "DELETE", "/7", "", true},
		{"force delete", "DELETE", "/7/force", "", true},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			for user, open := range map[uint64]bool{grace: true, bob: !rt.write, cleo: false, dave: false} {
				r := newRig(t)
				rp := r.do(user, rt.method, rt.path, rt.body)
				if open && (rp.Status < 200 || rp.Status > 299) || !open && rp.Status != 403 {
					t.Errorf("user %d: status = %d: %s", user, rp.Status, rp.Raw)
				}
				if !open && (len(r.store.creates)+len(r.store.updates)+len(r.store.builds)+len(r.store.startups)+len(r.store.statuses)+len(r.store.deleted) != 0 || len(r.daemon.calls) != 0) {
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
	rp := r.do(bob, "GET", "?filter[name]=sur&filter[uuidShort]=aaaa&sort=-id&per_page=5", "")
	if rp.Status != 200 || rp.At("object") != "list" || rp.At("meta", "pagination", "total") != 2.0 || rp.At("meta", "pagination", "per_page") != 5.0 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	want := map[string]any{
		"id": 7.0, "external_id": "crm-7", "uuid": uuid1, "identifier": "aaaa1111", "name": "Survival", "description": "A world", "status": nil, "suspended": false,
		"limits":         map[string]any{"memory": 1024.0, "swap": 0.0, "disk": 5000.0, "io": 500.0, "cpu": 100.0, "threads": nil, "oom_disabled": true},
		"feature_limits": map[string]any{"databases": 2.0, "allocations": 5.0, "backups": 0.0},
		"user":           3.0, "node": 4.0, "allocation": 5.0, "nest": 1.0, "egg": 10.0,
		"container": map[string]any{
			"startup_command": "java -jar server.jar", "image": "ghcr.io/y/java_21", "installed": 1.0, "skip_scripts": false,
			"environment": map[string]any{"SERVER_JARFILE": "paper.jar", "STARTUP": "java -jar server.jar", "P_SERVER_LOCATION": "us.nyc", "P_SERVER_UUID": uuid1, "P_SERVER_ALLOCATION_LIMIT": 5.0},
		},
		"updated_at": "2026-10-01T10:01:00+00:00", "created_at": "2026-10-01T10:00:00+00:00",
	}
	if rp.At("data", 0, "object") != "server" || !reflect.DeepEqual(rp.At("data", 0, "attributes"), want) {
		t.Errorf("first = %v", rp.At("data", 0, "attributes"))
	}
	if rp.At("data", 1, "attributes", "status") != "suspended" || rp.At("data", 1, "attributes", "suspended") != true {
		t.Errorf("suspended = %v", rp.At("data", 1, "attributes"))
	}
	if len(r.store.lastQuery.Conditions) != 2 || r.store.listPage != [2]int{1, 5} || len(r.store.lastQuery.Order) != 1 {
		t.Errorf("query = %+v page = %v", r.store.lastQuery, r.store.listPage)
	}

	for _, path := range []string{"/7", "/external/crm-7"} {
		if rp := r.do(bob, "GET", path, ""); rp.Status != 200 || rp.At("attributes", "name") != "Survival" {
			t.Errorf("%s: %d %s", path, rp.Status, rp.Raw)
		}
	}
	for _, path := range []string{"/99", "/0", "/x", "/external/nobody", "/external/"} {
		if rp := r.do(bob, "GET", path, ""); rp.Status != 404 {
			t.Errorf("%s: status = %d", path, rp.Status)
		}
	}
	for _, q := range []string{"?filter[owner_id]=3", "?sort=name", "?per_page=0"} {
		if rp := r.do(bob, "GET", q, ""); rp.Status != 400 && rp.Status != 422 {
			t.Errorf("%s: status = %d", q, rp.Status)
		}
	}
}

func TestIncludes(t *testing.T) {
	r := newRig(t)
	rp := r.do(grace, "GET", "/7?include=allocations,user,node,variables,databases,transfer,nothing", "")
	if rp.Status != 200 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if rp.At("attributes", "relationships", "allocations", "object") != "list" || len(rp.At("attributes", "relationships", "allocations", "data").([]any)) != 3 ||
		rp.At("attributes", "relationships", "user", "object") != "user" || rp.At("attributes", "relationships", "node", "object") != "node" {
		t.Errorf("relationships = %v", rp.At("attributes", "relationships"))
	}
	// The variables of a server are read with the right to read servers, and carry the server's own value.
	if rp.At("attributes", "relationships", "variables", "data", 0, "object") != "server_variable" || rp.At("attributes", "relationships", "variables", "data", 0, "attributes", "server_value") != "paper.jar" {
		t.Errorf("variables = %v", rp.At("attributes", "relationships", "variables"))
	}
	if rp.Has("attributes", "relationships", "transfer") || rp.Has("attributes", "relationships", "nothing") {
		t.Errorf("relationships = %v", rp.At("attributes", "relationships"))
	}
	for _, name := range []string{"allocations", "user", "node", "variables", "databases"} {
		if !r.store.lastLoad[name] {
			t.Errorf("%s was not loaded: %v", name, r.store.lastLoad)
		}
	}

	// Each include needs the right to read what it shows; without it the store is not asked.
	r = newRig(t)
	rp = r.do(bob, "GET", "/7?include=allocations,user,node,databases,nest,egg,location,subusers", "")
	for _, name := range []string{"allocations", "user", "node", "databases", "nest", "egg", "location", "subusers"} {
		if rp.At("attributes", "relationships", name, "object") != "null_resource" {
			t.Errorf("%s = %v", name, rp.At("attributes", "relationships", name))
		}
	}
	if len(r.store.lastLoad) != 0 {
		t.Errorf("load = %v", r.store.lastLoad)
	}
	// What is asked for in a list is attached to each server.
	if rp := r.do(grace, "GET", "?include=user", ""); rp.At("data", 0, "attributes", "relationships", "user", "object") != "user" || rp.At("data", 1, "attributes", "relationships", "user", "object") != "user" {
		t.Errorf("list: %s", rp.Raw)
	}
}

func TestStoreFailuresAreNotShown(t *testing.T) {
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "", ""}, {"GET", "/7", ""}, {"GET", "/external/crm-7", ""}, {"POST", "", createBodyOK},
		{"PATCH", "/7/details", `{"name":"x","user":3}`}, {"PATCH", "/7/build", `{"feature_limits":{}}`}, {"PATCH", "/7/startup", startupBodyOK},
		{"POST", "/7/suspend", ""}, {"POST", "/7/reinstall", ""}, {"DELETE", "/7", ""},
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
		b.GET("/external", r.handlers.External, api.Public())
		b.POST("", r.handlers.Create, api.Public())
		b.PATCH("/details", r.handlers.Details, api.Public())
		b.PATCH("/build", r.handlers.Build, api.Public())
		b.PATCH("/startup", r.handlers.Startup, api.Public())
		b.POST("/suspend", r.handlers.Suspend, api.Public())
		b.POST("/unsuspend", r.handlers.Unsuspend, api.Public())
		b.POST("/reinstall", r.handlers.Reinstall, api.Public())
		b.DELETE("/delete", r.handlers.Delete, api.Public())
	})
	for _, rt := range []struct{ method, path string }{
		{"GET", "/bare"}, {"GET", "/bare/view"}, {"GET", "/bare/external"}, {"POST", "/bare"}, {"PATCH", "/bare/details"}, {"PATCH", "/bare/build"}, {"PATCH", "/bare/startup"},
		{"POST", "/bare/suspend"}, {"POST", "/bare/unsuspend"}, {"POST", "/bare/reinstall"}, {"DELETE", "/bare/delete"},
	} {
		if rp := client.As(0, rt.method, rt.path, `{}`); rp.Status != 403 {
			t.Errorf("%s %s: status = %d, want 403", rt.method, rt.path, rp.Status)
		}
	}
	if len(r.store.creates)+len(r.store.updates)+len(r.store.builds)+len(r.store.statuses)+len(r.store.deleted)+len(r.daemon.calls) != 0 {
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
		"no store": func(d *Deps) { d.Store = nil }, "no targets": func(d *Deps) { d.Targets = nil }, "no daemon": func(d *Deps) { d.Daemon = nil },
		"no hosts": func(d *Deps) { d.Hosts = nil }, "no logger": func(d *Deps) { d.Logger = nil },
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
