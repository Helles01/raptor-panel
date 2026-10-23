package clientapi

import (
	"crypto/md5" //nolint:gosec // the expected value of the picture address
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/servers"
)

// realtime is the identifier written another way: the bytes of the UUID in lower case base32 without padding.
func realtime(prefix, uuid string) string {
	raw, _ := hex.DecodeString(strings.ReplaceAll(uuid, "-", ""))
	return prefix + "_" + strings.ToLower(strings.TrimRight(base32.StdEncoding.EncodeToString(raw), "="))
}

func gravatar(email string) string {
	sum := md5.Sum([]byte(strings.ToLower(email))) //nolint:gosec // see the import
	return "https://gravatar.com/avatar/" + hex.EncodeToString(sum[:])
}

func TestServerPageOfTheOwner(t *testing.T) {
	r := newRig(t)

	rp := r.as(ada, "GET", "/api/client/servers/aaaa1111")
	if rp.Status != 200 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if rp.at("object") != "server" {
		t.Errorf("object = %v", rp.at("object"))
	}

	want := map[string]any{
		"server_owner":              true,
		"identifier":                "aaaa1111",
		"__deprecated_uuid_short":   "aaaa1111",
		"server_identifier":         realtime("serv", "aaaa1111-2222-4333-8444-555555555555"),
		"internal_id":               100.0,
		"uuid":                      "aaaa1111-2222-4333-8444-555555555555",
		"name":                      "Survival",
		"node":                      "node-1",
		"is_node_under_maintenance": false,
		"sftp_details":              map[string]any{"ip": "node1.example.com", "port": 2022.0},
		"description":               "The main world",
		"limits":                    map[string]any{"memory": 1024.0, "swap": 512.0, "disk": 5000.0, "io": 500.0, "cpu": 200.0, "threads": "0-3", "oom_disabled": true},
		"invocation":                "java -Xmx1024M -jar server.jar --world world2 --pass [hidden] on 10.0.0.5:25565",
		"docker_image":              "ghcr.io/example/java:21",
		"egg_features":              []any{"eula", "java_version"},
		"feature_limits":            map[string]any{"databases": 2.0, "allocations": nil, "backups": 5.0},
		"status":                    nil,
		"is_suspended":              false,
		"is_installing":             false,
		"is_transferring":           false,
		"skip_scripts":              false,
	}
	if got := rp.at("attributes"); !reflect.DeepEqual(got, want) {
		t.Errorf("attributes =\n%v\nwant\n%v", got, want)
	}

	// The allocations come with it: the primary one is the default, the notes and alias are null when not set.
	if rp.at("relationships", "allocations", "object") != "list" || rp.length("relationships", "allocations", "data") != 2 {
		t.Fatalf("allocations = %v", rp.at("relationships", "allocations"))
	}
	wantAllocations := []any{
		map[string]any{"object": "allocation", "attributes": map[string]any{"id": 10.0, "ip": "10.0.0.5", "ip_alias": nil, "port": 25565.0, "notes": "main", "is_default": true}},
		map[string]any{"object": "allocation", "attributes": map[string]any{"id": 11.0, "ip": "10.0.0.5", "ip_alias": "play.example.com", "port": 25566.0, "notes": nil, "is_default": false}},
	}
	if got := rp.at("relationships", "allocations", "data"); !reflect.DeepEqual(got, wantAllocations) {
		t.Errorf("allocations =\n%v\nwant\n%v", got, wantAllocations)
	}

	// Only the variables a user may see; the value is null while the server has none of its own.
	wantVariables := []any{
		map[string]any{"object": "egg_variable", "attributes": map[string]any{"name": "Server Jar", "description": "The file", "env_variable": "SERVER_JARFILE", "default_value": "server.jar", "server_value": nil, "is_editable": true, "rules": "required|string"}},
		map[string]any{"object": "egg_variable", "attributes": map[string]any{"name": "World", "description": "", "env_variable": "WORLD", "default_value": "world", "server_value": "world2", "is_editable": false, "rules": "string"}},
	}
	if got := rp.at("relationships", "variables", "data"); !reflect.DeepEqual(got, wantVariables) {
		t.Errorf("variables =\n%v\nwant\n%v", got, wantVariables)
	}

	for _, name := range []string{"egg", "subusers"} {
		if rp.has("relationships", name) {
			t.Errorf("%s came without being asked for", name)
		}
	}
	if rp.at("meta", "is_server_owner") != true || !reflect.DeepEqual(rp.strings("meta", "user_permissions"), []string{"*"}) {
		t.Errorf("meta = %v", rp.at("meta"))
	}
}

func TestServerPageShowsWhatTheCallerMayRead(t *testing.T) {
	tests := []struct {
		name       string
		user       uint64
		invocation string
		variables  int // -1: a null resource
		allocs     int
		notes      any
		owner      bool
		perms      []string
	}{
		{
			name: "a subuser with neither right", user: bob,
			invocation: "java -Xmx1024M -jar [hidden] --world [hidden] --pass [hidden] on 10.0.0.5:25565",
			variables:  -1, allocs: 1, notes: nil,
			perms: []string{"control.start", "websocket.connect"},
		},
		{
			name: "a subuser who reads the startup and the allocations", user: cleo,
			invocation: "java -Xmx1024M -jar server.jar --world world2 --pass [hidden] on 10.0.0.5:25565",
			variables:  2, allocs: 2, notes: "main",
			perms: []string{"startup.read", "allocation.read", "user.read", "activity.read"},
		},
		{
			name: "an administrator", user: grace,
			invocation: "java -Xmx1024M -jar server.jar --world world2 --pass [hidden] on 10.0.0.5:25565",
			variables:  2, allocs: 2, notes: "main",
			perms: []string{"*", "admin.websocket.errors", "admin.websocket.install", "admin.websocket.transfer"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.as(tt.user, "GET", "/api/client/servers/aaaa1111")
			if rp.Status != 200 {
				t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
			}
			if got := rp.at("attributes", "invocation"); got != tt.invocation {
				t.Errorf("invocation = %v", got)
			}
			if tt.variables < 0 {
				if rp.at("relationships", "variables", "object") != "null_resource" || rp.at("relationships", "variables", "attributes") != nil || !rp.has("relationships", "variables", "attributes") {
					t.Errorf("variables = %v, want a null resource", rp.at("relationships", "variables"))
				}
			} else if got := rp.length("relationships", "variables", "data"); got != tt.variables {
				t.Errorf("%d variables", got)
			}
			if got := rp.length("relationships", "allocations", "data"); got != tt.allocs {
				t.Errorf("%d allocations", got)
			}
			if got := rp.at("relationships", "allocations", "data", 0, "attributes", "notes"); got != tt.notes {
				t.Errorf("notes of the primary allocation = %v", got)
			}
			if got := rp.at("relationships", "allocations", "data", 0, "attributes", "is_default"); got != true {
				t.Errorf("the primary allocation is not the default: %v", got)
			}
			if rp.at("meta", "is_server_owner") != false {
				t.Errorf("is_server_owner = %v", rp.at("meta", "is_server_owner"))
			}
			if rp.at("attributes", "server_owner") != false {
				t.Errorf("server_owner = %v", rp.at("attributes", "server_owner"))
			}
			if got := rp.strings("meta", "user_permissions"); !reflect.DeepEqual(got, tt.perms) {
				t.Errorf("user_permissions = %v", got)
			}
		})
	}
}

func TestServerPageNeverShowsAHiddenVariable(t *testing.T) {
	r := newRig(t)
	for _, user := range []uint64{ada, bob, cleo, grace} {
		rp := r.as(user, "GET", "/api/client/servers/aaaa1111?include=egg,subusers")
		if strings.Contains(string(rp.Raw), "hunter2") || strings.Contains(string(rp.Raw), "SECRET") {
			t.Errorf("user %d: the hidden variable shows: %s", user, rp.Raw)
		}
	}
}

func TestServerPageIncludes(t *testing.T) {
	t.Run("the owner asks for the egg and the subusers", func(t *testing.T) {
		r := newRig(t)
		rp := r.as(ada, "GET", "/api/client/servers/aaaa1111?include=egg,subusers")
		if rp.Status != 200 {
			t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
		}
		wantEgg := map[string]any{"object": "egg", "attributes": map[string]any{"uuid": "egg-uuid", "name": "Vanilla"}}
		if got := rp.at("relationships", "egg"); !reflect.DeepEqual(got, wantEgg) {
			t.Errorf("egg = %v", got)
		}

		if rp.at("relationships", "subusers", "object") != "list" || rp.length("relationships", "subusers", "data") != 2 {
			t.Fatalf("subusers = %v", rp.at("relationships", "subusers"))
		}
		wantBob := map[string]any{"object": "server_subuser", "attributes": map[string]any{
			"uuid":        "00000003-0000-4000-8000-000000000000",
			"identifier":  realtime("user", "00000003-0000-4000-8000-000000000000"),
			"username":    "bob",
			"email":       "bob@example.com",
			"image":       gravatar("bob@example.com"),
			"2fa_enabled": false,
			"created_at":  "2026-10-21T12:00:00+00:00",
			"permissions": []any{"control.start", "websocket.connect"},
		}}
		if got := rp.at("relationships", "subusers", "data", 0); !reflect.DeepEqual(got, wantBob) {
			t.Errorf("subuser =\n%v\nwant\n%v", got, wantBob)
		}
	})

	t.Run("a subuser who may not read users gets a null resource, and the store is not asked", func(t *testing.T) {
		r := newRig(t)
		rp := r.as(bob, "GET", "/api/client/servers/aaaa1111?include=subusers")
		if rp.at("relationships", "subusers", "object") != "null_resource" {
			t.Errorf("subusers = %v", rp.at("relationships", "subusers"))
		}
		if len(r.w.subuserReqs) != 0 {
			t.Errorf("the subusers were read: %v", r.w.subuserReqs)
		}
	})

	t.Run("a subuser who may read users sees them", func(t *testing.T) {
		r := newRig(t)
		rp := r.as(cleo, "GET", "/api/client/servers/aaaa1111?include=subusers")
		if rp.length("relationships", "subusers", "data") != 2 {
			t.Errorf("subusers = %v", rp.at("relationships", "subusers"))
		}
	})

	t.Run("nothing is read that was not asked for", func(t *testing.T) {
		r := newRig(t)
		r.as(ada, "GET", "/api/client/servers/aaaa1111")
		r.as(ada, "GET", "/api/client/servers/aaaa1111?include=node,allocations.server,variables")
		if len(r.w.subuserReqs) != 0 {
			t.Errorf("the subusers were read: %v", r.w.subuserReqs)
		}
	})

	t.Run("the egg include may carry a path", func(t *testing.T) {
		r := newRig(t)
		if rp := r.as(ada, "GET", "/api/client/servers/aaaa1111?include=egg.nest"); !rp.has("relationships", "egg") {
			t.Errorf("no egg: %s", rp.Raw)
		}
	})

	t.Run("a subuser list without users is an empty list", func(t *testing.T) {
		r := newRig(t)
		r.w.subusers[100] = nil
		rp := r.as(ada, "GET", "/api/client/servers/aaaa1111?include=subusers")
		if rp.at("relationships", "subusers", "object") != "list" || rp.length("relationships", "subusers", "data") != 0 || rp.at("relationships", "subusers", "data") == nil {
			t.Errorf("subusers = %s", rp.Raw)
		}
	})
}

func TestServerPageStates(t *testing.T) {
	r := newRig(t)

	// The page of a server that cannot be used yet can still be looked at.
	rp := r.as(ada, "GET", "/api/client/servers/bbbb2222")
	if rp.Status != 200 {
		t.Fatalf("installing: %d: %s", rp.Status, rp.Raw)
	}
	for key, want := range map[string]any{"status": "installing", "is_installing": true, "is_suspended": false, "is_transferring": false, "threads": nil} {
		got := rp.at("attributes", key)
		if key == "threads" {
			got = rp.at("attributes", "limits", "threads")
			if !rp.has("attributes", "limits", "threads") {
				t.Error("threads is missing, it must be null")
			}
		}
		if got != want {
			t.Errorf("installing: %s = %v, want %v", key, got, want)
		}
	}

	rp = r.as(ada, "GET", "/api/client/servers/dddd4444")
	if rp.Status != 200 {
		t.Fatalf("suspended: %d: %s", rp.Status, rp.Raw)
	}
	for key, want := range map[string]any{"status": "suspended", "is_installing": false, "is_suspended": true, "is_transferring": true, "is_node_under_maintenance": true} {
		if got := rp.at("attributes", key); got != want {
			t.Errorf("suspended: %s = %v, want %v", key, got, want)
		}
	}

	if got := rp.at("attributes", "egg_features"); got != nil || !rp.has("attributes", "egg_features") {
		t.Errorf("egg_features = %v, want null for an egg without features", got)
	}
}

func TestServerPageAccess(t *testing.T) {
	r := newRig(t)

	tests := []struct {
		name   string
		user   uint64
		target string
		status int
	}{
		{"nobody", 0, "/api/client/servers/aaaa1111", 401},
		{"a stranger", eve, "/api/client/servers/aaaa1111", 404},
		{"a server that does not exist", ada, "/api/client/servers/ffffffff", 404},
		{"the full UUID", ada, "/api/client/servers/aaaa1111-2222-4333-8444-555555555555", 200},
		{"an administrator", grace, "/api/client/servers/cccc3333", 200},
		{"somebody else's server", ada, "/api/client/servers/cccc3333", 404},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rp := r.as(tt.user, "GET", tt.target); rp.Status != tt.status {
				t.Errorf("status = %d, want %d: %s", rp.Status, tt.status, rp.Raw)
			}
		})
	}
}

func TestServerPageStoreFailures(t *testing.T) {
	t.Run("a server that is gone since the authorizer found it", func(t *testing.T) {
		r := newRig(t)
		r.w.failOne = fmt.Errorf("one: %w", ErrNotFound)
		if rp := r.as(ada, "GET", "/api/client/servers/aaaa1111"); rp.Status != 404 {
			t.Errorf("status = %d", rp.Status)
		}
	})
	t.Run("a broken store does not leak its message", func(t *testing.T) {
		r := newRig(t)
		r.w.failOne = errBoom
		rp := r.as(ada, "GET", "/api/client/servers/aaaa1111")
		if rp.Status != 500 || strings.Contains(string(rp.Raw), "hunter2") {
			t.Errorf("status = %d: %s", rp.Status, rp.Raw)
		}
	})
	t.Run("subusers that cannot be read", func(t *testing.T) {
		r := newRig(t)
		r.w.failSubusers = errBoom
		rp := r.as(ada, "GET", "/api/client/servers/aaaa1111?include=subusers")
		if rp.Status != 500 || strings.Contains(string(rp.Raw), "hunter2") {
			t.Errorf("status = %d: %s", rp.Status, rp.Raw)
		}
	})
	t.Run("a server whose UUID is not one", func(t *testing.T) {
		r := newRig(t)
		r.w.servers[0].UUID = "aaaa1111"
		if rp := r.as(ada, "GET", "/api/client/servers/aaaa1111"); rp.Status != 500 {
			t.Errorf("status = %d", rp.Status)
		}
	})
}

func internalIDs(rp reply) []float64 {
	var out []float64
	items, _ := rp.at("data").([]any)
	for i := range items {
		out = append(out, rp.at("data", i, "attributes", "internal_id").(float64))
	}
	return out
}

func TestServerListScopes(t *testing.T) {
	tests := []struct {
		name   string
		user   uint64
		target string
		want   []float64
		asked  bool // whether the store was asked at all
	}{
		{"the servers an owner has", ada, "/api/client", []float64{100, 101, 103}, true},
		{"a subuser sees the server they were given", bob, "/api/client", []float64{100}, true},
		{"a stranger with servers of their own", eve, "/api/client", []float64{102}, true},
		{"an administrator without servers has none of their own", grace, "/api/client", nil, true},
		{"only the owned ones", ada, "/api/client?type=owner", []float64{100, 101, 103}, true},
		{"a subuser owns none", bob, "/api/client?type=owner", nil, true},
		{"the administrator's other servers", grace, "/api/client?type=admin", []float64{100, 101, 102, 103}, true},
		{"all of them", grace, "/api/client?type=admin-all", []float64{100, 101, 102, 103}, true},
		{"an administrator's list asked by another is empty", ada, "/api/client?type=admin", nil, false},
		{"and the list of all", ada, "/api/client?type=admin-all", nil, false},
		{"any other type is the default", ada, "/api/client?type=everything", []float64{100, 101, 103}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.as(tt.user, "GET", tt.target)
			if rp.Status != 200 {
				t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
			}
			if rp.at("object") != "list" {
				t.Errorf("object = %v", rp.at("object"))
			}
			if got := internalIDs(rp); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("servers = %v, want %v", got, tt.want)
			}
			if asked := len(r.w.lists) > 0; asked != tt.asked {
				t.Errorf("the store was asked: %v, want %v", asked, tt.asked)
			}
			if rp.at("data") == nil {
				t.Error("data is null, it must be a list")
			}
			if got := rp.at("meta", "pagination", "total"); got != float64(len(tt.want)) {
				t.Errorf("total = %v", got)
			}
		})
	}
}

func TestServerListRows(t *testing.T) {
	t.Run("every row is judged on its own permissions", func(t *testing.T) {
		r := newRig(t)
		r.w.subs[[2]uint64{101, bob}] = []string{"startup.read"}
		r.w.subs[[2]uint64{102, bob}] = nil // a subuser with no permission at all

		rp := r.as(bob, "GET", "/api/client")
		if got := internalIDs(rp); !reflect.DeepEqual(got, []float64{100, 101, 102}) {
			t.Fatalf("servers = %v", got)
		}
		// 100: no startup.read; 101: startup.read; 102: no permissions.
		wantNull := []bool{true, false, true}
		for i, null := range wantNull {
			isNull := rp.at("data", i, "relationships", "variables", "object") == "null_resource"
			if isNull != null {
				t.Errorf("row %d: variables is a null resource: %v, want %v", i, isNull, null)
			}
		}
		if rp.at("data", 0, "attributes", "server_owner") != false {
			t.Error("a subuser is not the owner")
		}
	})

	t.Run("the owner sees all and is the owner", func(t *testing.T) {
		r := newRig(t)
		rp := r.as(ada, "GET", "/api/client")
		if rp.length("data", 0, "relationships", "allocations", "data") != 2 || rp.length("data", 0, "relationships", "variables", "data") != 2 {
			t.Errorf("the owner does not see everything: %s", rp.Raw)
		}
		for i := 0; i < 3; i++ {
			if rp.at("data", i, "attributes", "server_owner") != true || rp.at("data", i, "object") != "server" {
				t.Errorf("row %d = %v", i, rp.at("data", i))
			}
			if rp.at("data", i, "relationships", "allocations", "object") != "list" {
				t.Errorf("row %d has no allocations", i)
			}
		}
		if rp.has("data", 0, "meta") {
			t.Error("a row of the list has no meta")
		}
	})

	t.Run("an administrator sees the others' servers whole", func(t *testing.T) {
		r := newRig(t)
		rp := r.as(grace, "GET", "/api/client?type=admin-all")
		if rp.length("data", 0, "relationships", "variables", "data") != 2 {
			t.Errorf("the variables = %v", rp.at("data", 0, "relationships", "variables"))
		}
		if rp.at("data", 0, "attributes", "server_owner") != false {
			t.Error("an administrator does not own what they see")
		}
	})
}

func TestServerListIncludes(t *testing.T) {
	t.Run("the egg of every row", func(t *testing.T) {
		r := newRig(t)
		rp := r.as(ada, "GET", "/api/client?include=egg")
		for i := 0; i < 3; i++ {
			if rp.at("data", i, "relationships", "egg", "object") != "egg" {
				t.Errorf("row %d: egg = %v", i, rp.at("data", i, "relationships", "egg"))
			}
		}
	})

	t.Run("the subusers are read once for the servers whose subusers the caller may see", func(t *testing.T) {
		r := newRig(t)
		rp := r.as(ada, "GET", "/api/client?include=subusers")
		if !reflect.DeepEqual(r.w.subuserReqs, [][]uint64{{100, 101, 103}}) {
			t.Errorf("subusers asked for: %v", r.w.subuserReqs)
		}
		if rp.length("data", 0, "relationships", "subusers", "data") != 2 || rp.at("data", 1, "relationships", "subusers", "data") == nil {
			t.Errorf("subusers = %s", rp.Raw)
		}
	})

	t.Run("a subuser asks only where they may", func(t *testing.T) {
		r := newRig(t)
		r.w.subs[[2]uint64{101, bob}] = []string{"user.read"}
		rp := r.as(bob, "GET", "/api/client?include=subusers")
		if !reflect.DeepEqual(r.w.subuserReqs, [][]uint64{{101}}) {
			t.Errorf("subusers asked for: %v", r.w.subuserReqs)
		}
		if rp.at("data", 0, "relationships", "subusers", "object") != "null_resource" || rp.at("data", 1, "relationships", "subusers", "object") != "list" {
			t.Errorf("subusers = %s", rp.Raw)
		}
	})

	t.Run("no includes, no reading", func(t *testing.T) {
		r := newRig(t)
		rp := r.as(ada, "GET", "/api/client")
		if len(r.w.subuserReqs) != 0 || rp.has("data", 0, "relationships", "egg") || rp.has("data", 0, "relationships", "subusers") {
			t.Errorf("subusers = %v, %s", r.w.subuserReqs, rp.Raw)
		}
	})

	t.Run("an empty list reads nothing", func(t *testing.T) {
		r := newRig(t)
		r.as(grace, "GET", "/api/client?include=subusers")
		if len(r.w.subuserReqs) != 0 {
			t.Errorf("subusers asked for: %v", r.w.subuserReqs)
		}
	})
}

func TestServerListRequest(t *testing.T) {
	t.Run("what is passed to the store", func(t *testing.T) {
		r := newRig(t)
		r.as(ada, "GET", "/api/client?type=owner&filter[name]=sur&page=2&per_page=2")
		if len(r.w.lists) != 1 {
			t.Fatalf("%d requests", len(r.w.lists))
		}
		got := r.w.lists[0]
		if got.Viewer != ada || got.Scope != servers.ScopeOwner || got.Page != 2 || got.PerPage != 2 {
			t.Errorf("request = %+v", got)
		}
		want := []listquery.Condition{{Column: "name", Match: listquery.Partial, Values: []string{"sur"}}}
		if !reflect.DeepEqual(got.Query.Conditions, want) || got.Query.Table != "servers" {
			t.Errorf("query = %+v", got.Query)
		}
	})

	t.Run("the search", func(t *testing.T) {
		r := newRig(t)
		r.as(ada, "GET", "/api/client?filter[*]=10.0.0.5:25565")
		c := r.w.lists[0].Query.Conditions
		if len(c) != 1 || c[0].Column != "*" || c[0].Match != listquery.Search || c[0].Values[0] != "10.0.0.5:25565" {
			t.Errorf("conditions = %+v", c)
		}
	})

	t.Run("defaults", func(t *testing.T) {
		r := newRig(t)
		r.as(ada, "GET", "/api/client")
		if got := r.w.lists[0]; got.Page != 1 || got.PerPage != 50 || got.Scope != servers.ScopeAccessible {
			t.Errorf("request = %+v", got)
		}
	})

	refused := []struct {
		name, target string
		status       int
	}{
		{"a filter that is not offered", "/api/client?filter[owner_id]=1", 400},
		{"a sort, the list has none", "/api/client?sort=name", 400},
		{"too large a page", "/api/client?per_page=101", 422},
		{"an empty page", "/api/client?per_page=0", 422},
		{"a page that is not a number", "/api/client?page=abc", 422},
		{"page zero", "/api/client?page=0", 422},
	}
	for _, tt := range refused {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			if rp := r.as(ada, "GET", tt.target); rp.Status != tt.status {
				t.Errorf("status = %d, want %d: %s", rp.Status, tt.status, rp.Raw)
			}
			if len(r.w.lists) != 0 {
				t.Error("the store was asked for a request that was refused")
			}
		})
	}

	t.Run("the largest page is allowed", func(t *testing.T) {
		r := newRig(t)
		if rp := r.as(ada, "GET", "/api/client?per_page=100"); rp.Status != 200 {
			t.Errorf("status = %d", rp.Status)
		}
	})
}

func TestServerListPages(t *testing.T) {
	r := newRig(t)

	rp := r.as(ada, "GET", "/api/client?per_page=2&page=1")
	if got := internalIDs(rp); !reflect.DeepEqual(got, []float64{100, 101}) {
		t.Errorf("first page = %v", got)
	}
	pg := map[string]any{"total": 3.0, "count": 2.0, "per_page": 2.0, "current_page": 1.0, "total_pages": 2.0}
	for k, want := range pg {
		if got := rp.at("meta", "pagination", k); got != want {
			t.Errorf("pagination.%s = %v, want %v", k, got, want)
		}
	}
	if next, _ := rp.at("meta", "pagination", "links", "next").(string); !strings.Contains(next, "page=2") || !strings.Contains(next, "per_page=2") || !strings.HasPrefix(next, "/api/client?") {
		t.Errorf("next = %q", next)
	}

	rp = r.as(ada, "GET", "/api/client?per_page=2&page=2")
	if got := internalIDs(rp); !reflect.DeepEqual(got, []float64{103}) {
		t.Errorf("second page = %v", got)
	}
	if prev, _ := rp.at("meta", "pagination", "links", "previous").(string); !strings.Contains(prev, "page=1") {
		t.Errorf("previous = %q", prev)
	}

	rp = r.as(ada, "GET", "/api/client?per_page=2&page=9")
	if got := rp.length("data"); got != 0 || rp.at("data") == nil || rp.at("meta", "pagination", "total") != 3.0 {
		t.Errorf("a page past the end: %s", rp.Raw)
	}
}

func TestServerListStoreFailure(t *testing.T) {
	r := newRig(t)
	r.w.failList = errBoom
	rp := r.as(ada, "GET", "/api/client")
	if rp.Status != 500 || strings.Contains(string(rp.Raw), "hunter2") {
		t.Errorf("status = %d: %s", rp.Status, rp.Raw)
	}

	r = newRig(t)
	r.w.failSubusers = errBoom
	if rp := r.as(ada, "GET", "/api/client?include=subusers"); rp.Status != 500 {
		t.Errorf("subusers: status = %d", rp.Status)
	}
}

func TestPermissionTree(t *testing.T) {
	r := newRig(t)

	rp := r.as(ada, "GET", "/api/client/permissions")
	if rp.Status != 200 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if rp.at("object") != "system_permissions" {
		t.Errorf("object = %v", rp.at("object"))
	}
	for _, key := range []string{"control", "user", "file", "backup", "allocation", "startup", "database", "schedule", "settings", "activity", "websocket"} {
		if !rp.has("attributes", "permissions", key) {
			t.Errorf("group %q is missing", key)
		}
	}
	if desc, _ := rp.at("attributes", "permissions", "control", "keys", "start").(string); desc == "" {
		t.Errorf("control.start has no description: %v", rp.at("attributes", "permissions", "control"))
	}
	if strings.Contains(string(rp.Raw), "admin.websocket") {
		t.Error("an administrator's permission is on offer")
	}

	if rp := r.as(0, "GET", "/api/client/permissions"); rp.Status != 401 {
		t.Errorf("without a login: %d", rp.Status)
	}
}

func TestSubuserWithoutPermissionsHasAnEmptyList(t *testing.T) {
	r := newRig(t)
	r.w.subs[[2]uint64{100, bob}] = nil

	rp := r.as(bob, "GET", "/api/client/servers/aaaa1111")
	if rp.Status != 200 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	perms, ok := rp.at("meta", "user_permissions").([]any)
	if !ok || len(perms) != 0 {
		t.Errorf("user_permissions = %v, want []", rp.at("meta", "user_permissions"))
	}
}

func TestSubuserWithoutPermissionsIsShownWithAnEmptyList(t *testing.T) {
	r := newRig(t)
	r.w.subusers[100][0].Permissions = nil

	rp := r.as(ada, "GET", "/api/client/servers/aaaa1111?include=subusers")
	perms, ok := rp.at("relationships", "subusers", "data", 0, "attributes", "permissions").([]any)
	if !ok || len(perms) != 0 {
		t.Errorf("permissions = %v, want []", rp.at("relationships", "subusers", "data", 0, "attributes", "permissions"))
	}
}
