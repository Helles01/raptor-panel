package appkit

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/nodes"
	"github.com/Helles01/raptor-panel/internal/servers"
)

var t0 = time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

func viewOf(access map[string]auth.Access) View {
	return NewView(authz.Principal{Key: &authz.Key{Access: access}})
}

func everything() View {
	m := map[string]auth.Access{}
	for _, r := range auth.ApplicationResources() {
		m[r] = auth.AccessRead
	}
	return viewOf(m)
}

func sampleServer() Server {
	return Server{
		ID: 7, ExternalID: ptr("crm-7"), ShortID: "aaaa1111", OwnerID: 3, NodeID: 4, AllocationID: 5, NestID: 1, EggID: 10,
		DatabaseLimit: ptr(int64(2)), BackupLimit: ptr(int64(0)), CreatedAt: t0, UpdatedAt: t0.Add(time.Minute),
		Config: servers.Server{
			UUID: "aaaa1111-2222-4333-8444-555555555555", Name: "Survival", Description: "A world", Memory: 1024, Swap: 0, Disk: 5000, IO: 500, CPU: 100,
			Threads: ptr("0-1"), OOMDisabled: true, AllocationLimit: ptr(int64(5)), LocationShort: "us.nyc", Startup: "java -jar server.jar", Image: "ghcr.io/y/java_21",
			Variables: []servers.Variable{{EnvName: "SERVER_JARFILE", Default: "server.jar", Value: ptr("paper.jar")}, {EnvName: "MC_VERSION", Default: "latest"}},
		},
	}
}

func decode(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestServerItem(t *testing.T) {
	got := decode(t, everything().Server(sampleServer(), nil))
	want := map[string]any{
		"object": "server",
		"attributes": map[string]any{
			"id": 7.0, "external_id": "crm-7", "uuid": "aaaa1111-2222-4333-8444-555555555555", "identifier": "aaaa1111", "name": "Survival", "description": "A world",
			"status": nil, "suspended": false,
			"limits":         map[string]any{"memory": 1024.0, "swap": 0.0, "disk": 5000.0, "io": 500.0, "cpu": 100.0, "threads": "0-1", "oom_disabled": true},
			"feature_limits": map[string]any{"databases": 2.0, "allocations": 5.0, "backups": 0.0},
			"user":           3.0, "node": 4.0, "allocation": 5.0, "nest": 1.0, "egg": 10.0,
			"container": map[string]any{
				"startup_command": "java -jar server.jar", "image": "ghcr.io/y/java_21", "installed": 1.0, "skip_scripts": false,
				"environment": map[string]any{
					"SERVER_JARFILE": "paper.jar", "MC_VERSION": "latest", "STARTUP": "java -jar server.jar", "P_SERVER_LOCATION": "us.nyc",
					"P_SERVER_UUID": "aaaa1111-2222-4333-8444-555555555555", "P_SERVER_ALLOCATION_LIMIT": 5.0,
				},
			},
			"updated_at": "2026-10-01T10:01:00+00:00", "created_at": "2026-10-01T10:00:00+00:00",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("server = %v", got)
	}
}

func TestServerStates(t *testing.T) {
	for status, tc := range map[string]struct {
		suspended bool
		installed float64
	}{
		"": {false, 1}, "suspended": {true, 1}, "installing": {false, 0}, "install_failed": {false, 0}, "reinstall_failed": {false, 1}, "restoring_backup": {false, 1},
	} {
		s := sampleServer()
		s.Config.Status = status
		a := decode(t, everything().Server(s, nil))["attributes"].(map[string]any)
		if (a["status"] == nil) != (status == "") || (status != "" && a["status"] != status) || a["suspended"] != tc.suspended || a["container"].(map[string]any)["installed"] != tc.installed {
			t.Errorf("%q: status %v suspended %v installed %v", status, a["status"], a["suspended"], a["container"].(map[string]any)["installed"])
		}
	}
	// A server without limits set shows them as null.
	s := sampleServer()
	s.DatabaseLimit, s.BackupLimit, s.Config.AllocationLimit, s.Config.Threads, s.ExternalID = nil, nil, nil, nil, nil
	a := decode(t, everything().Server(s, nil))["attributes"].(map[string]any)
	f := a["feature_limits"].(map[string]any)
	if f["databases"] != nil || f["allocations"] != nil || f["backups"] != nil || a["limits"].(map[string]any)["threads"] != nil || a["external_id"] != nil {
		t.Errorf("unset = %v", a)
	}
}

func TestServerIncludes(t *testing.T) {
	s := sampleServer()
	s.Allocations = []Allocation{{ID: 5, NodeID: 4, IP: "10.0.0.1", Port: 25565, ServerID: ptr(uint64(7))}}
	s.Subusers = []Subuser{{ID: 9, UserID: 8, ServerID: 7, Permissions: []string{"control.console"}, CreatedAt: t0, UpdatedAt: t0}, {ID: 10, UserID: 11, ServerID: 7}}
	s.User = &User{ID: 3, Username: "ann", CreatedAt: t0, UpdatedAt: t0}
	s.Nest = &Nest{ID: 1, Name: "Minecraft", CreatedAt: t0, UpdatedAt: t0}
	s.Egg = &Egg{ID: 10, Name: "Vanilla", NestID: 1, CreatedAt: t0, UpdatedAt: t0}
	s.Variables = []ServerVariable{{Variable: EggVariable{ID: 100, EggID: 10, EnvVariable: "SERVER_JARFILE", CreatedAt: t0, UpdatedAt: t0}, ServerValue: ptr("paper.jar")}, {Variable: EggVariable{ID: 101, EggID: 10}}}
	s.Location = &Location{ID: 2, Short: "us.nyc", CreatedAt: t0, UpdatedAt: t0}
	s.Node = &Node{ID: 4, Settings: nodes.Settings{Name: "node-4"}, CreatedAt: t0, UpdatedAt: t0}
	s.Databases = []ServerDatabase{{ID: 12, ServerID: 7, HostID: 2, Name: "s7_world", Username: "u7_abc", Remote: "%", MaxConnections: 5, CreatedAt: t0, UpdatedAt: t0}}

	all := Includes{"allocations": true, "subusers": true, "user": true, "nest": true, "egg": true, "variables": true, "location": true, "node": true, "databases": true}
	rel := decode(t, everything().Server(s, all))["attributes"].(map[string]any)["relationships"].(map[string]any)
	object := func(name string) any { return rel[name].(map[string]any)["object"] }
	first := func(name string) map[string]any {
		return rel[name].(map[string]any)["data"].([]any)[0].(map[string]any)
	}

	if object("allocations") != "list" || first("allocations")["object"] != "allocation" ||
		object("user") != "user" || object("nest") != "nest" || object("egg") != "egg" || object("location") != "location" || object("node") != "node" {
		t.Errorf("relationships = %v", rel)
	}
	sub := first("subusers")
	if object("subusers") != "list" || sub["object"] != "subuser" ||
		!reflect.DeepEqual(sub["attributes"], map[string]any{"id": 9.0, "user_id": 8.0, "server_id": 7.0, "permissions": []any{"control.console"}, "created_at": "2026-10-01T10:00:00+00:00", "updated_at": "2026-10-01T10:00:00+00:00"}) {
		t.Errorf("subuser = %v", sub)
	}
	// A subuser with no permissions has an empty list, not null.
	if second := rel["subusers"].(map[string]any)["data"].([]any)[1].(map[string]any); !reflect.DeepEqual(second["attributes"].(map[string]any)["permissions"], []any{}) {
		t.Errorf("no permissions = %v", second)
	}
	// The variables carry the server's own value besides what the egg says.
	vars := rel["variables"].(map[string]any)["data"].([]any)
	if v0 := vars[0].(map[string]any); v0["object"] != "server_variable" || v0["attributes"].(map[string]any)["server_value"] != "paper.jar" || v0["attributes"].(map[string]any)["env_variable"] != "SERVER_JARFILE" ||
		vars[1].(map[string]any)["attributes"].(map[string]any)["server_value"] != nil {
		t.Errorf("variables = %v", vars)
	}
	// The databases are named after the include, as the reference does.
	if first("databases")["object"] != "databases" || first("databases")["attributes"].(map[string]any)["database"] != "s7_world" {
		t.Errorf("databases = %v", first("databases"))
	}

	// Without the right to read a resource its include is a null resource.
	none := viewOf(map[string]auth.Access{})
	rel = decode(t, none.Server(s, all))["attributes"].(map[string]any)["relationships"].(map[string]any)
	for name := range all {
		if object(name) != "null_resource" {
			t.Errorf("%s = %v", name, rel[name])
		}
	}
	// And what is not asked for is not there; an asked include that was not loaded is a null resource.
	if rel := decode(t, everything().Server(Server{}, Includes{"user": true, "node": true, "nest": true, "egg": true, "location": true}))["attributes"].(map[string]any)["relationships"].(map[string]any); rel["user"].(map[string]any)["object"] != "null_resource" || rel["node"].(map[string]any)["object"] != "null_resource" ||
		rel["nest"].(map[string]any)["object"] != "null_resource" || rel["egg"].(map[string]any)["object"] != "null_resource" || rel["location"].(map[string]any)["object"] != "null_resource" {
		t.Errorf("not loaded = %v", rel)
	}
	if got := decode(t, everything().Server(s, nil)); got["attributes"].(map[string]any)["relationships"] != nil {
		t.Errorf("relationships without includes = %v", got["attributes"].(map[string]any)["relationships"])
	}
}

func TestServerDatabaseItem(t *testing.T) {
	d := ServerDatabase{
		ID: 12, ServerID: 7, HostID: 2, Name: "s7_world", Username: "u7_abc", Remote: "203.0.113.%", MaxConnections: 5, CreatedAt: t0, UpdatedAt: t0.Add(time.Hour),
		Password: ptr("Secret1!"), Host: &DatabaseHost{ID: 2, Name: "db1", Host: "db.example.com", Port: 3306, Username: "panel", NodeID: ptr(uint64(4)), CreatedAt: t0, UpdatedAt: t0},
	}
	got := decode(t, everything().ServerDatabase(d, nil))
	want := map[string]any{
		"object": "server_database",
		"attributes": map[string]any{
			"id": 12.0, "server": 7.0, "host": 2.0, "database": "s7_world", "username": "u7_abc", "remote": "203.0.113.%", "max_connections": 5.0,
			"created_at": "2026-10-01T10:00:00+00:00", "updated_at": "2026-10-01T11:00:00+00:00",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("database = %v", got)
	}

	rel := decode(t, everything().ServerDatabase(d, Includes{"password": true, "host": true}))["attributes"].(map[string]any)["relationships"].(map[string]any)
	if !reflect.DeepEqual(rel["password"], map[string]any{"object": "database_password", "attributes": map[string]any{"password": "Secret1!"}}) {
		t.Errorf("password = %v", rel["password"])
	}
	wantHost := map[string]any{
		"object": "database_host",
		"attributes": map[string]any{
			"id": 2.0, "name": "db1", "host": "db.example.com", "port": 3306.0, "username": "panel", "node": 4.0,
			"created_at": "2026-10-01T10:00:00+00:00", "updated_at": "2026-10-01T10:00:00+00:00",
		},
	}
	if !reflect.DeepEqual(rel["host"], wantHost) {
		t.Errorf("host = %v", rel["host"])
	}

	// The password is part of the answer to whoever may read the databases; the host needs the right to read database hosts.
	view := viewOf(map[string]auth.Access{"server_databases": auth.AccessRead})
	rel = decode(t, view.ServerDatabase(d, Includes{"password": true, "host": true}))["attributes"].(map[string]any)["relationships"].(map[string]any)
	if rel["password"].(map[string]any)["object"] != "database_password" || rel["host"].(map[string]any)["object"] != "null_resource" {
		t.Errorf("limited = %v", rel)
	}
	// A password the store did not open is not a password.
	d.Password, d.Host = nil, nil
	rel = decode(t, everything().ServerDatabase(d, Includes{"password": true, "host": true}))["attributes"].(map[string]any)["relationships"].(map[string]any)
	if rel["password"].(map[string]any)["object"] != "null_resource" || rel["host"].(map[string]any)["object"] != "null_resource" {
		t.Errorf("not loaded = %v", rel)
	}
}

func TestTheServersOfOtherResources(t *testing.T) {
	list := []Server{sampleServer()}
	inc := Includes{"servers": true}
	v := everything()
	for name, item := range map[string]any{
		"user":       v.User(User{Servers: list}, inc),
		"location":   v.Location(Location{Servers: list}, inc),
		"node":       v.Node(Node{Servers: list}, inc),
		"nest":       v.Nest(Nest{Servers: list}, inc),
		"egg":        v.Egg(Egg{Servers: list}, inc),
		"allocation": v.Allocation(Allocation{Server: &list[0]}, Includes{"server": true}),
	} {
		rel := decode(t, item)["attributes"].(map[string]any)["relationships"].(map[string]any)
		key := "servers"
		if name == "allocation" {
			key = "server"
		}
		got := rel[key].(map[string]any)
		wantObject := "list"
		if name == "allocation" {
			wantObject = "server"
		}
		if got["object"] != wantObject {
			t.Errorf("%s: %v", name, got)
		}
		if name != "allocation" && got["data"].([]any)[0].(map[string]any)["attributes"].(map[string]any)["name"] != "Survival" {
			t.Errorf("%s: %v", name, got)
		}
	}
	// Without the right to read servers: a null resource, in every place.
	v = viewOf(map[string]auth.Access{})
	for name, item := range map[string]any{
		"user": v.User(User{Servers: list}, inc), "location": v.Location(Location{}, inc), "node": v.Node(Node{}, inc), "nest": v.Nest(Nest{}, inc), "egg": v.Egg(Egg{}, inc),
	} {
		if decode(t, item)["attributes"].(map[string]any)["relationships"].(map[string]any)["servers"].(map[string]any)["object"] != "null_resource" {
			t.Errorf("%s shows servers to a key that may not read them", name)
		}
	}
}
