package appservers

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/servers"
)

func TestDetails(t *testing.T) {
	r := newRig(t)
	rp := r.do(grace, "PATCH", "/7/details", `{"name":"Renamed","user":9,"external_id":"crm-70","description":"New"}`)
	if rp.Status != 200 || rp.At("object") != "server" || rp.At("attributes", "name") != "Renamed" || rp.At("attributes", "user") != 9.0 || rp.At("attributes", "external_id") != "crm-70" || rp.At("attributes", "description") != "New" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	want := update{ID: 7, Details: servers.Details{ExternalID: ptr("crm-70"), OwnerID: 9, Name: "Renamed", Description: "New"}, Revoke: 3}
	if len(r.store.updates) != 1 || !reflect.DeepEqual(r.store.updates[0], want) {
		t.Errorf("updates = %+v, want %+v", r.store.updates, want)
	}
	if len(r.daemon.calls) != 0 {
		t.Errorf("the node was called: %+v", r.daemon.calls)
	}

	// The same owner: nobody loses their access.
	r = newRig(t)
	r.do(grace, "PATCH", "/7/details", `{"name":"Renamed","user":3}`)
	if got := r.store.updates[0]; got.Revoke != 0 || got.Details.OwnerID != 3 {
		t.Errorf("update = %+v", got)
	}
}

func TestDetailsKeepWhatWasNotSent(t *testing.T) {
	r := newRig(t)
	if rp := r.do(grace, "PATCH", "/7/details", `{"name":"Renamed","user":3}`); rp.Status != 200 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if got := r.store.updates[0].Details; !reflect.DeepEqual(got.ExternalID, ptr("crm-7")) || got.Description != "A world" {
		t.Errorf("kept = %+v", got)
	}
	// Null or an empty text clears them (C-050).
	for _, body := range []string{
		`{"name":"R","user":3,"external_id":null,"description":null}`,
		`{"name":"R","user":3,"external_id":"","description":""}`,
	} {
		r = newRig(t)
		if rp := r.do(grace, "PATCH", "/7/details", body); rp.Status != 200 {
			t.Fatalf("%s: %d %s", body, rp.Status, rp.Raw)
		}
		if got := r.store.updates[0].Details; got.ExternalID != nil || got.Description != "" {
			t.Errorf("%s: cleared = %+v", body, got)
		}
	}
}

func TestDetailsRefusals(t *testing.T) {
	tests := []struct {
		name, path, body string
		status           int
		detail, field    string
	}{
		{"a server that is not there", "/99/details", `{"name":"x","user":3}`, 404, "", ""},
		{"a server that is not there, and a bad body", "/99/details", `{}`, 404, "", ""},
		{"no name", "/7/details", `{"user":3}`, 422, "The name field is required.", "name"},
		{"no user", "/7/details", `{"name":"x"}`, 422, "The user field is required.", "user"},
		{"a name that is a number", "/7/details", `{"name":5,"user":3}`, 422, "The name must be a string.", "name"},
		{"a user that is text", "/7/details", `{"name":"x","user":"ann"}`, 422, "The user must be an integer.", "user"},
		{"a name that is too long", "/7/details", `{"name":"` + strings.Repeat("a", 192) + `","user":3}`, 422, "", "name"},
		{"an owner that is not there", "/7/details", `{"name":"x","user":77}`, 422, "The selected user is invalid.", "user"},
		{"an external id of another server", "/8/details", `{"name":"x","user":4,"external_id":"crm-7"}`, 422, "The external id has already been taken.", "external_id"},
		{"an owner below one", "/7/details", `{"name":"x","user":-1}`, 422, "", "user"},
		{"not JSON", "/7/details", `name=x`, 400, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(grace, "PATCH", tt.path, tt.body)
			if rp.Status != tt.status || (tt.detail != "" && rp.Detail() != tt.detail) {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if tt.field != "" && rp.At("errors", 0, "meta", "source_field") != tt.field {
				t.Errorf("field = %v", rp.At("errors", 0, "meta"))
			}
			if tt.name != "an owner that is not there" && tt.name != "an external id of another server" && len(r.store.updates) != 0 {
				t.Error("a server was changed")
			}
		})
	}
}

func TestBuild(t *testing.T) {
	r := newRig(t)
	rp := r.do(grace, "PATCH", "/7/build", `{"limits":{"memory":2048,"swap":-1,"disk":6000,"io":600,"cpu":200,"threads":"0-1"},"feature_limits":{"databases":3,"allocations":null,"backups":1},"oom_disabled":false}`)
	if rp.Status != 200 || rp.At("object") != "server" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if rp.At("attributes", "limits", "memory") != 2048.0 || rp.At("attributes", "limits", "swap") != -1.0 || rp.At("attributes", "limits", "threads") != "0-1" || rp.At("attributes", "limits", "oom_disabled") != false ||
		rp.At("attributes", "feature_limits", "databases") != 3.0 || rp.At("attributes", "feature_limits", "allocations") != nil || rp.At("attributes", "feature_limits", "backups") != 1.0 {
		t.Errorf("attributes = %v", rp.At("attributes"))
	}
	if r.daemon.count("sync") != 1 || r.daemon.calls[0].UUID != uuid1 {
		t.Errorf("node calls = %+v", r.daemon.calls)
	}
}

func TestBuildKeepsWhatWasNotSent(t *testing.T) {
	r := newRig(t)
	// The limits at the top level, as older clients send them: only memory changes, the rest stays.
	rp := r.do(grace, "PATCH", "/7/build", `{"memory":4096,"feature_limits":{}}`)
	if rp.Status != 200 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	b := r.store.builds[0]
	if b.Limits.Memory != 4096 || b.Limits.Disk != 5000 || b.Limits.IO != 500 || b.Limits.CPU != 100 || b.Limits.Swap != 0 || !b.OOMDisabled {
		t.Errorf("limits = %+v", b)
	}
	if f := b.Features; f.Databases == nil || *f.Databases != 2 || f.Allocations == nil || *f.Allocations != 5 || f.Backups == nil || *f.Backups != 0 {
		t.Errorf("features = %+v", f)
	}
	if b.Plan.Default != 5 || len(b.Plan.Assign) != 0 || len(b.Plan.Release) != 0 {
		t.Errorf("plan = %+v", b.Plan)
	}

	// The ones in limits win over the ones at the top.
	r = newRig(t)
	r.do(grace, "PATCH", "/7/build", `{"memory":1,"disk":1,"limits":{"memory":2048},"feature_limits":{}}`)
	if b := r.store.builds[0]; b.Limits.Memory != 2048 || b.Limits.Disk != 1 {
		t.Errorf("limits = %+v", b.Limits)
	}
	// A CPU set that was there is cleared by null and kept when left out.
	r = newRig(t)
	r.store.servers[7].server.Config.Threads = ptr("0-3")
	r.do(grace, "PATCH", "/7/build", `{"limits":{"memory":1024},"feature_limits":{}}`)
	if b := r.store.builds[0]; b.Limits.Threads == nil || *b.Limits.Threads != "0-3" {
		t.Errorf("kept threads = %v", b.Limits.Threads)
	}
	r.do(grace, "PATCH", "/7/build", `{"limits":{"threads":null},"feature_limits":{}}`)
	if b := r.store.builds[1]; b.Limits.Threads != nil {
		t.Errorf("cleared threads = %v", b.Limits.Threads)
	}
}

func TestBuildAllocations(t *testing.T) {
	r := newRig(t)
	rp := r.do(grace, "PATCH", "/7/build", `{"allocation":8,"add_allocations":[8,9],"remove_allocations":[7],"feature_limits":{}}`)
	if rp.Status != 200 || rp.At("attributes", "allocation") != 8.0 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	b := r.store.builds[0]
	if !reflect.DeepEqual(b.Plan.Assign, []uint64{8, 9}) || !reflect.DeepEqual(b.Plan.Release, []uint64{7}) || b.Plan.Default != 8 {
		t.Errorf("plan = %+v", b.Plan)
	}
	if a := r.store.allocs[8]; a.ServerID == nil || *a.ServerID != 7 {
		t.Errorf("allocation 8 = %+v", a)
	}
	if a := r.store.allocs[7]; a.ServerID != nil {
		t.Errorf("allocation 7 = %+v", a)
	}

	// The default one can only go when another takes its place: the lowest of the new ones.
	r = newRig(t)
	rp = r.do(grace, "PATCH", "/7/build", `{"add_allocations":[9,8],"remove_allocations":[5],"feature_limits":{}}`)
	if rp.Status != 200 || rp.At("attributes", "allocation") != 8.0 {
		t.Errorf("replace the default: %d %s", rp.Status, rp.Raw)
	}
	if a := r.store.allocs[5]; a.ServerID != nil || a.Notes != nil {
		t.Errorf("released allocation 5 = %+v: its note must go", a)
	}
}

func TestBuildAllocationRefusals(t *testing.T) {
	tests := []struct {
		name, body string
		status     int
		detail     string
	}{
		{"the default one with no one to take its place", `{"remove_allocations":[5],"feature_limits":{}}`, 400, servers.ErrNoFallback.Error()},
		{"a default that is not the server's", `{"allocation":30,"feature_limits":{}}`, 400, servers.ErrDefaultNotAssigned.Error()},
		{"an allocation of another node", `{"add_allocations":[20],"feature_limits":{}}`, 422, "Allocation 20 cannot be added: it does not exist, is not on the node, or is in use."},
		{"an allocation in use", `{"add_allocations":[30],"feature_limits":{}}`, 422, "Allocation 30 cannot be added: it does not exist, is not on the node, or is in use."},
		{"an allocation that is not there", `{"add_allocations":[999],"feature_limits":{}}`, 422, "Allocation 999 cannot be added: it does not exist, is not on the node, or is in use."},
		{"removing one that is not the server's", `{"remove_allocations":[9],"feature_limits":{}}`, 422, "Allocation 9 cannot be removed: it is not assigned to this server."},
		{"allocations as text", `{"add_allocations":"8","feature_limits":{}}`, 422, "The add allocations must be an array."},
		{"an allocation that is text", `{"add_allocations":["x"],"feature_limits":{}}`, 422, "The add allocations.0 must be an integer."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(grace, "PATCH", "/7/build", tt.body)
			if rp.Status != tt.status || rp.Detail() != tt.detail {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if len(r.store.builds) != 0 || r.daemon.count("sync") != 0 {
				t.Error("the build was changed")
			}
			if tt.status == 400 && rp.Code() != "DisplayException" {
				t.Errorf("code = %s", rp.Code())
			}
		})
	}
}

func TestBuildRefusals(t *testing.T) {
	tests := []struct {
		name, path, body string
		status           int
		detail, field    string
	}{
		{"a server that is not there", "/99/build", `{"feature_limits":{}}`, 404, "", ""},
		{"no feature limits", "/7/build", `{"memory":1}`, 422, "The feature limits field is required.", "feature_limits"},
		{"a memory that is text", "/7/build", `{"limits":{"memory":"lots"},"feature_limits":{}}`, 422, "The limits.memory must be a number.", "limits.memory"},
		{"an io below the range", "/7/build", `{"limits":{"io":5},"feature_limits":{}}`, 422, "", "limits.io"},
		{"a swap below -1", "/7/build", `{"limits":{"swap":-2},"feature_limits":{}}`, 422, "", "limits.swap"},
		{"a cpu set that is no set", "/7/build", `{"limits":{"threads":"abc"},"feature_limits":{}}`, 422, "", "limits.threads"},
		{"a database limit below zero", "/7/build", `{"feature_limits":{"databases":-1}}`, 422, "", "feature_limits.databases"},
		{"a backup limit that is text", "/7/build", `{"feature_limits":{"backups":"x"}}`, 422, "The feature limits.backups must be an integer.", "feature_limits.backups"},
		{"oom_disabled as text", "/7/build", `{"oom_disabled":"x","feature_limits":{}}`, 422, "The oom disabled field must be true or false.", "oom_disabled"},
		{"a default allocation that is text", "/7/build", `{"allocation":"x","feature_limits":{}}`, 422, "The allocation must be an integer.", "allocation"},
		{"not JSON", "/7/build", `x`, 400, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(grace, "PATCH", tt.path, tt.body)
			if rp.Status != tt.status || (tt.detail != "" && rp.Detail() != tt.detail) {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if tt.field != "" && rp.At("errors", 0, "meta", "source_field") != tt.field {
				t.Errorf("field = %v", rp.At("errors", 0, "meta"))
			}
			if len(r.store.builds) != 0 {
				t.Error("the build was changed")
			}
		})
	}
}

func TestBuildWhenTheNodeCannotBeTold(t *testing.T) {
	r := newRig(t)
	r.daemon.errs["sync"] = &wingsError
	rp := r.do(grace, "PATCH", "/7/build", `{"memory":2048,"feature_limits":{}}`)
	// The change is made and the node learns of it when the server starts: only a line in the log says so.
	if rp.Status != 200 || rp.At("attributes", "limits", "memory") != 2048.0 || len(r.store.builds) != 1 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if !strings.Contains(r.logs.String(), "was not told to sync") {
		t.Errorf("log = %s", r.logs.String())
	}
}

func TestStartup(t *testing.T) {
	r := newRig(t)
	rp := r.do(grace, "PATCH", "/7/startup", `{"startup":"java -jar paper.jar","environment":{"SERVER_JARFILE":"paper.jar","MC_VERSION":1.21},"egg":10,"image":"ghcr.io/y/java_17","skip_scripts":true}`)
	if rp.Status != 200 || rp.At("object") != "server" || rp.At("attributes", "container", "startup_command") != "java -jar paper.jar" || rp.At("attributes", "container", "image") != "ghcr.io/y/java_17" ||
		rp.At("attributes", "container", "skip_scripts") != true {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if len(r.store.startups) != 1 {
		t.Fatalf("startups = %+v", r.store.startups)
	}
	got := r.store.startups[0]
	if got.ID != 7 || got.NestID != 1 || got.Settings != (servers.StartupSettings{Startup: "java -jar paper.jar", EggID: 10, Image: "ghcr.io/y/java_17", SkipScripts: true}) {
		t.Errorf("startup = %+v", got)
	}
	values := map[string]string{}
	for _, v := range got.Values {
		values[v.EnvName] = v.Store
	}
	if !reflect.DeepEqual(values, map[string]string{"SERVER_JARFILE": "paper.jar", "MC_VERSION": "1.21"}) {
		t.Errorf("values = %v", values)
	}
	if len(r.daemon.calls) != 0 {
		t.Errorf("the node was called: %+v", r.daemon.calls)
	}
}

func TestStartupWithANewEgg(t *testing.T) {
	r := newRig(t)
	rp := r.do(grace, "PATCH", "/7/startup", `{"startup":"./run","environment":{},"egg":11,"image":"alpine:3.19","skip_scripts":false}`)
	if rp.Status != 200 || rp.At("attributes", "egg") != 11.0 || rp.At("attributes", "nest") != 2.0 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	// An empty environment changes no variable and asks for none, not even the required ones (09b).
	if got := r.store.startups[0]; len(got.Values) != 0 {
		t.Errorf("values = %+v", got.Values)
	}
	if rp := r.do(grace, "PATCH", "/7/startup", `{"startup":"./run","environment":[],"egg":10,"image":"alpine:3.19","skip_scripts":false}`); rp.Status != 200 {
		t.Errorf("an empty list for an empty environment: %d %s", rp.Status, rp.Raw)
	}
}

func TestStartupRefusals(t *testing.T) {
	ok := func(replace map[string]string) string {
		b := `{"startup":"java -jar x.jar","environment":{"SERVER_JARFILE":"x.jar"},"egg":10,"image":"ghcr.io/y/java_17","skip_scripts":false}`
		for from, to := range replace {
			b = strings.Replace(b, from, to, 1)
		}
		return b
	}
	tests := []struct {
		name, path, body string
		status           int
		detail, field    string
	}{
		{"a server that is not there", "/99/startup", ok(nil), 404, "", ""},
		{"no startup", "/7/startup", ok(map[string]string{`"startup":"java -jar x.jar",`: ""}), 422, "The startup field is required.", "startup"},
		{"no environment", "/7/startup", ok(map[string]string{`"environment":{"SERVER_JARFILE":"x.jar"},`: ""}), 422, "The environment field must be present.", "environment"},
		{"no egg", "/7/startup", ok(map[string]string{`"egg":10,`: ""}), 422, "The egg field is required.", "egg"},
		{"no image", "/7/startup", ok(map[string]string{`"image":"ghcr.io/y/java_17",`: ""}), 422, "The image field is required.", "image"},
		{"no skip_scripts", "/7/startup", ok(map[string]string{`,"skip_scripts":false`: ""}), 422, "The skip scripts field is required.", "skip_scripts"},
		{"an environment that is text", "/7/startup", ok(map[string]string{`{"SERVER_JARFILE":"x.jar"}`: `"x"`}), 422, "The environment must be an array.", "environment"},
		{"a value that is a list", "/7/startup", ok(map[string]string{`"x.jar"`: `["a"]`}), 422, "The environment.SERVER_JARFILE must be a string.", "environment.SERVER_JARFILE"},
		{"an egg that is not there", "/7/startup", ok(map[string]string{`"egg":10`: `"egg":99`}), 422, "The selected egg is invalid.", "egg"},
		{"an image with a quote", "/7/startup", ok(map[string]string{`ghcr.io/y/java_17`: `a\"b`}), 422, "", "image"},
		{"a startup of nothing", "/7/startup", ok(map[string]string{`java -jar x.jar`: `   `}), 422, "", "startup"},
		{"a value the rules refuse", "/7/startup", ok(map[string]string{`"x.jar"`: `"` + strings.Repeat("a", 21) + `"`}), 422, "The Server Jar File variable may not be greater than 20 characters.", "environment.SERVER_JARFILE"},
		{"a required variable that is missing", "/7/startup", ok(map[string]string{`{"SERVER_JARFILE":"x.jar"}`: `{"MC_VERSION":"1"}`}), 422, "The Server Jar File variable field is required.", "environment.SERVER_JARFILE"},
		{"not JSON", "/7/startup", `x`, 400, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(grace, "PATCH", tt.path, tt.body)
			if rp.Status != tt.status || (tt.detail != "" && rp.Detail() != tt.detail) {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if tt.field != "" && rp.At("errors", 0, "meta", "source_field") != tt.field {
				t.Errorf("field = %v", rp.At("errors", 0, "meta"))
			}
			if len(r.store.startups) != 0 {
				t.Error("the startup was changed")
			}
		})
	}
}

func TestStartupTellsAboutEveryProblem(t *testing.T) {
	r := newRig(t)
	r.store.eggs[10] = EggInfo{NestID: 1, Variables: append(r.store.eggs[10].Variables, servers.EggVariable{EnvName: "MOTD", Name: "Message", Rules: "required|string"})}
	rp := r.do(grace, "PATCH", "/7/startup", `{"startup":"x","environment":{"SERVER_JARFILE":"`+strings.Repeat("a", 21)+`"},"egg":10,"image":"alpine:3.19","skip_scripts":false}`)
	if rp.Status != 422 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	fields := map[any]bool{}
	for i := 0; i < 2; i++ {
		fields[rp.At("errors", i, "meta", "source_field")] = true
	}
	if !fields["environment.SERVER_JARFILE"] || !fields["environment.MOTD"] {
		t.Errorf("errors = %s", rp.Raw)
	}
}

func TestBuildCanMakeAnotherAllocationOfTheServerTheDefault(t *testing.T) {
	r := newRig(t)
	rp := r.do(grace, "PATCH", "/7/build", `{"allocation":6,"feature_limits":{}}`)
	if rp.Status != 200 || rp.At("attributes", "allocation") != 6.0 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if b := r.store.builds[0]; b.Plan.Default != 6 || len(b.Plan.Assign) != 0 || len(b.Plan.Release) != 0 {
		t.Errorf("plan = %+v", b.Plan)
	}
}

func TestStartupTakesNullAsNoValue(t *testing.T) {
	r := newRig(t)
	rp := r.do(grace, "PATCH", "/7/startup", `{"startup":"x","environment":{"SERVER_JARFILE":"x.jar","MC_VERSION":null},"egg":10,"image":"alpine:3.19","skip_scripts":false}`)
	if rp.Status != 200 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	values := map[string]string{}
	for _, v := range r.store.startups[0].Values {
		values[v.EnvName] = v.Store
	}
	if !reflect.DeepEqual(values, map[string]string{"SERVER_JARFILE": "x.jar", "MC_VERSION": ""}) {
		t.Errorf("values = %v", values)
	}
	// A required variable sent as null has no value.
	r = newRig(t)
	rp = r.do(grace, "PATCH", "/7/startup", `{"startup":"x","environment":{"SERVER_JARFILE":null},"egg":10,"image":"alpine:3.19","skip_scripts":false}`)
	if rp.Status != 422 || rp.At("errors", 0, "meta", "source_field") != "environment.SERVER_JARFILE" {
		t.Errorf("required null: %d %s", rp.Status, rp.Raw)
	}
}
