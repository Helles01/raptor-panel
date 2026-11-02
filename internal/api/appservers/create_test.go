package appservers

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/servers"
	"github.com/Helles01/raptor-panel/internal/wings"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestCreate(t *testing.T) {
	r := newRig(t)
	rp := r.do(grace, "POST", "", createBodyOK)
	if rp.Status != 201 || rp.At("object") != "server" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	a := rp.At("attributes").(map[string]any)
	uuid, _ := a["uuid"].(string)
	if a["id"] != 101.0 || !uuidV4.MatchString(uuid) || a["identifier"] != uuid[:8] || a["status"] != "installing" || a["suspended"] != false ||
		a["user"] != 3.0 || a["node"] != 4.0 || a["allocation"] != 8.0 || a["egg"] != 10.0 || a["nest"] != 1.0 || a["description"] != "" || a["external_id"] != nil {
		t.Errorf("attributes = %v", a)
	}
	if !reflect.DeepEqual(a["limits"], map[string]any{"memory": 512.0, "swap": 0.0, "disk": 1000.0, "io": 500.0, "cpu": 0.0, "threads": nil, "oom_disabled": true}) ||
		!reflect.DeepEqual(a["feature_limits"], map[string]any{"databases": 1.0, "allocations": 2.0, "backups": 3.0}) {
		t.Errorf("limits = %v, features = %v", a["limits"], a["feature_limits"])
	}
	if c := a["container"].(map[string]any); c["installed"] != 0.0 || c["skip_scripts"] != false || c["image"] != "ghcr.io/y/java_21" {
		t.Errorf("container = %v", c)
	}

	// What was stored: the variables the egg has, the ones not sent with no value, and the allocation the server has now.
	if len(r.store.creates) != 1 {
		t.Fatalf("creates = %+v", r.store.creates)
	}
	req := r.store.creates[0]
	values := map[string]string{}
	for _, v := range req.Values {
		values[v.EnvName] = v.Store
	}
	if !reflect.DeepEqual(values, map[string]string{"SERVER_JARFILE": "server.jar", "MC_VERSION": ""}) || req.UUID != uuid || req.ShortID != uuid[:8] || req.NestID != 1 || !req.OOMDisabled || req.SkipScripts {
		t.Errorf("request = %+v", req)
	}
	if got := r.store.allocs[8]; got.ServerID == nil || *got.ServerID != 101 {
		t.Errorf("allocation 8 = %+v", got)
	}
	// The node is told to set the server up, and not to start it.
	if !reflect.DeepEqual(r.daemon.calls, []daemonCall{{"create", uuid, false}}) {
		t.Errorf("node calls = %+v", r.daemon.calls)
	}
}

func TestCreateWithEverything(t *testing.T) {
	r := newRig(t)
	body := `{"external_id":"crm-9","name":"Full","description":"All set","user":4,"egg":10,"docker_image":"ghcr.io/y/java_21","startup":"java -jar x.jar","environment":{"SERVER_JARFILE":"x.jar","MC_VERSION":"1.21"},` +
		`"skip_scripts":true,"oom_disabled":false,"limits":{"memory":2048,"swap":-1,"disk":0,"io":100,"cpu":50,"threads":"0-1"},"feature_limits":{"databases":null,"allocations":null,"backups":null},` +
		`"allocation":{"default":8,"additional":[9]},"start_on_completion":true}`
	rp := r.do(grace, "POST", "", body)
	if rp.Status != 201 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	a := rp.At("attributes").(map[string]any)
	if a["external_id"] != "crm-9" || a["description"] != "All set" || a["limits"].(map[string]any)["threads"] != "0-1" || a["limits"].(map[string]any)["oom_disabled"] != false ||
		a["container"].(map[string]any)["skip_scripts"] != true {
		t.Errorf("attributes = %v", a)
	}
	// A limit that is sent as null is stored as 0 when a server is made, as in the reference (an update stores null).
	if !reflect.DeepEqual(a["feature_limits"], map[string]any{"databases": 0.0, "allocations": 0.0, "backups": 0.0}) {
		t.Errorf("feature limits = %v", a["feature_limits"])
	}
	for _, id := range []uint64{8, 9} {
		if got := r.store.allocs[id]; got.ServerID == nil || *got.ServerID != 101 {
			t.Errorf("allocation %d = %+v", id, got)
		}
	}
	if r.daemon.calls[0].Kind != "create" || !r.daemon.calls[0].Start {
		t.Errorf("node calls = %+v", r.daemon.calls)
	}
	if got := r.store.creates[0].Values; len(got) != 2 {
		t.Errorf("values = %+v", got)
	}
}

func TestDeployment(t *testing.T) {
	deploy := func(extra string) string {
		return `{"name":"Auto","user":3,"egg":10,"docker_image":"ghcr.io/y/java_21","startup":"x","environment":{"SERVER_JARFILE":"x.jar"},"limits":{"memory":512,"swap":0,"disk":1000,"io":500,"cpu":0},` +
			`"feature_limits":{"databases":0,"backups":0},"deploy":` + extra + `}`
	}
	// Node 4 is the only public one in location 1 with room; of its free allocations only 9 has the port.
	r := newRig(t)
	rp := r.do(grace, "POST", "", deploy(`{"locations":[1],"dedicated_ip":false,"port_range":["25565"]}`))
	if rp.Status != 201 || rp.At("attributes", "node") != 4.0 || rp.At("attributes", "allocation") != 9.0 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	// Another location is another node.
	r = newRig(t)
	if rp := r.do(grace, "POST", "", deploy(`{"locations":[2],"dedicated_ip":false,"port_range":[]}`)); rp.Status != 201 || rp.At("attributes", "node") != 5.0 || rp.At("attributes", "allocation") != 20.0 {
		t.Errorf("location 2: %d %s", rp.Status, rp.Raw)
	}
	// A dedicated address is one that no server uses yet: 10.0.0.1 has servers, so only 25568 is out of the question.
	r = newRig(t)
	rp = r.do(grace, "POST", "", deploy(`{"locations":[1],"dedicated_ip":true,"port_range":["25568"]}`))
	if rp.Status != 400 || rp.Code() != "DisplayException" || rp.Detail() != servers.ErrNoViableAllocation.Error() {
		t.Errorf("dedicated: %d %s", rp.Status, rp.Raw)
	}
	r = newRig(t)
	if rp := r.do(grace, "POST", "", deploy(`{"locations":[1],"dedicated_ip":false,"port_range":["25568"]}`)); rp.Status != 201 || rp.At("attributes", "allocation") != 8.0 {
		t.Errorf("shared: %d %s", rp.Status, rp.Raw)
	}
	// Whatever is wanted of the node is checked.
	for name, tt := range map[string]struct {
		body   string
		status int
		detail string
	}{
		"no node has room":         {strings.Replace(deploy(`{"locations":[],"dedicated_ip":false,"port_range":[]}`), `"memory":512`, `"memory":100000`, 1), 400, servers.ErrNoViableNode.Error()},
		"no node in the location":  {deploy(`{"locations":[99],"dedicated_ip":false,"port_range":[]}`), 400, servers.ErrNoViableNode.Error()},
		"a port that is no port":   {deploy(`{"locations":[1],"dedicated_ip":false,"port_range":["http"]}`), 400, "The mapping provided for http was invalid and could not be processed."},
		"no locations":             {deploy(`{"dedicated_ip":false,"port_range":[]}`), 422, "The deploy.locations field must be present."},
		"no port range":            {deploy(`{"locations":[]}`), 422, "The deploy.port range field must be present."},
		"no dedicated flag":        {deploy(`{"locations":[],"port_range":[]}`), 422, "The deploy.dedicated ip field is required."},
		"a location below one":     {deploy(`{"locations":[0],"dedicated_ip":false,"port_range":[]}`), 422, "The deploy.locations.0 must be at least 1."},
		"a location that is text":  {deploy(`{"locations":["x"],"dedicated_ip":false,"port_range":[]}`), 422, "The deploy.locations.0 must be an integer."},
		"a port that is a number":  {deploy(`{"locations":[],"dedicated_ip":false,"port_range":[25565]}`), 422, "The deploy.port range.0 must be a string."},
		"locations that are text":  {deploy(`{"locations":"1","dedicated_ip":false,"port_range":[]}`), 422, "The deploy.locations must be an array."},
		"a dedicated flag as text": {deploy(`{"locations":[],"dedicated_ip":"yes","port_range":[]}`), 422, "The deploy.dedicated ip field must be true or false."},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(grace, "POST", "", tt.body)
			if rp.Status != tt.status || rp.Detail() != tt.detail {
				t.Errorf("answer = %d %s", rp.Status, rp.Raw)
			}
			if len(r.store.creates) != 0 || len(r.daemon.calls) != 0 {
				t.Error("a server was made")
			}
		})
	}
}

func TestCreateWhenTheNodeRefuses(t *testing.T) {
	r := newRig(t)
	r.daemon.errs["create"] = &wings.Error{Status: 500, Message: "The server could not be created on the node.", NodeStatus: 500}
	rp := r.do(grace, "POST", "", createBodyOK)
	if rp.Status != 500 || rp.Code() != "DaemonConnectionException" || rp.Detail() != "The server could not be created on the node." {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	// The server is taken back, from the node as well in case it half made it, and its allocation is free again.
	if !reflect.DeepEqual(r.store.deleted, []uint64{101}) || r.daemon.count("delete") != 1 || r.store.allocs[8].ServerID != nil {
		t.Errorf("deleted %v, node calls %+v, allocation %+v", r.store.deleted, r.daemon.calls, r.store.allocs[8])
	}
	if _, ok := r.store.servers[101]; ok {
		t.Error("the server is still there")
	}

	// A node that does not know the server it was asked to delete is no trouble; one that cannot be reached is logged.
	r = newRig(t)
	r.daemon.errs["create"] = &wings.Error{Status: 504, Message: "no reply", Unreachable: true}
	r.daemon.errs["delete"] = &wings.Error{Status: 504, Message: "no reply", Unreachable: true}
	if rp := r.do(grace, "POST", "", createBodyOK); rp.Status != 504 || len(r.store.deleted) != 1 || !strings.Contains(r.logs.String(), "was not asked to delete") {
		t.Errorf("unreachable: %d %s, log %s", rp.Status, rp.Raw, r.logs.String())
	}

	// The server has gone from the panel when its node is looked for: nothing to take back from the node.
	r = newRig(t)
	r.targets.err = errBoom
	if rp := r.do(grace, "POST", "", createBodyOK); rp.Status != 500 || rp.Contains("hunter2") || len(r.store.deleted) != 1 || r.daemon.count("create") != 0 {
		t.Errorf("no target: %d %s", rp.Status, rp.Raw)
	}
}

func TestACreatedServerGetsAFreshNameIfItIsTaken(t *testing.T) {
	r := newRig(t)
	r.store.uuidTaken = 2
	if rp := r.do(grace, "POST", "", createBodyOK); rp.Status != 201 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if len(r.store.creates) != 3 {
		t.Fatalf("%d tries", len(r.store.creates))
	}
	seen := map[string]bool{}
	for _, c := range r.store.creates {
		seen[c.UUID] = true
		if !uuidV4.MatchString(c.UUID) || c.ShortID != c.UUID[:8] {
			t.Errorf("request = %+v", c)
		}
	}
	if len(seen) != 3 {
		t.Error("the same UUID was tried twice")
	}
	// A store that keeps refusing is given up on.
	r = newRig(t)
	r.store.uuidTaken = 100
	if rp := r.do(grace, "POST", "", createBodyOK); rp.Status == 201 || len(r.store.creates) != maxUUIDTries+1 {
		t.Errorf("answer = %d, tries = %d", rp.Status, len(r.store.creates))
	}
}

func TestCreateRefusals(t *testing.T) {
	with := func(replace map[string]string) string {
		b := createBodyOK
		for from, to := range replace {
			if !strings.Contains(b, from) {
				panic("no " + from)
			}
			b = strings.Replace(b, from, to, 1)
		}
		return b
	}
	tests := []struct {
		name, body    string
		status        int
		detail, field string
	}{
		{"no name", with(map[string]string{`"name":"New",`: ""}), 422, "The name field is required.", "name"},
		{"no user", with(map[string]string{`"user":3,`: ""}), 422, "The user field is required.", "user"},
		{"no egg", with(map[string]string{`"egg":10,`: ""}), 422, "The egg field is required.", "egg"},
		{"no image", with(map[string]string{`"docker_image":"ghcr.io/y/java_21",`: ""}), 422, "The docker image field is required.", "docker_image"},
		{"no startup", with(map[string]string{`"startup":"java -jar server.jar",`: ""}), 422, "The startup field is required.", "startup"},
		{"no environment", with(map[string]string{`"environment":{"SERVER_JARFILE":"server.jar"},`: ""}), 422, "The environment field must be present.", "environment"},
		{"no limits", with(map[string]string{`"limits":{"memory":512,"swap":0,"disk":1000,"io":500,"cpu":0},`: ""}), 422, "The limits field is required.", "limits"},
		{"no memory", with(map[string]string{`"memory":512,`: ""}), 422, "The limits.memory field is required.", "limits.memory"},
		{"no io", with(map[string]string{`"io":500,`: ""}), 422, "The limits.io field is required.", "limits.io"},
		{"no feature limits", with(map[string]string{`"feature_limits":{"databases":1,"allocations":2,"backups":3},`: ""}), 422, "The feature limits field is required.", "feature_limits"},
		{"no allocation", with(map[string]string{`,"allocation":{"default":8}`: ""}), 422, "The allocation.default field is required.", "allocation.default"},
		{"an empty allocation", with(map[string]string{`"allocation":{"default":8}`: `"allocation":{}`}), 422, "The allocation.default field is required.", "allocation.default"},
		{"a name that is a number", with(map[string]string{`"name":"New"`: `"name":5`}), 422, "The name must be a string.", "name"},
		{"an environment that is text", with(map[string]string{`{"SERVER_JARFILE":"server.jar"}`: `"x"`}), 422, "The environment must be an array.", "environment"},
		{"oom_disabled as text", with(map[string]string{`"name":"New",`: `"name":"New","oom_disabled":"x",`}), 422, "The oom disabled field must be true or false.", "oom_disabled"},
		{"a limit that is text", with(map[string]string{`"memory":512`: `"memory":"x"`}), 422, "The limits.memory must be a number.", "limits.memory"},
		{"additional allocations as text", with(map[string]string{`"default":8`: `"default":8,"additional":"9"`}), 422, "The allocation.additional must be an array.", "allocation.additional"},
		{"an io below the range", with(map[string]string{`"io":500`: `"io":5`}), 422, "", "limits.io"},
		{"a cpu set that is no set", with(map[string]string{`"cpu":0`: `"cpu":0,"threads":"abc"`}), 422, "", "limits.threads"},
		{"a name of nothing", with(map[string]string{`"name":"New"`: `"name":"  "`}), 422, "", "name"},
		{"an image with a space", with(map[string]string{`ghcr.io/y/java_21`: `a b`}), 422, "", "docker_image"},
		{"an owner that is not there", with(map[string]string{`"user":3`: `"user":77`}), 422, "The selected user is invalid.", "user"},
		{"an egg that is not there", with(map[string]string{`"egg":10`: `"egg":77`}), 422, "The selected egg is invalid.", "egg"},
		{"an allocation that is not there", with(map[string]string{`"default":8`: `"default":999`}), 422, "Allocation 999 does not exist.", "allocation"},
		{"an allocation in use", with(map[string]string{`"default":8`: `"default":5`}), 422, "Allocation 5 is already in use.", "allocation"},
		{"an allocation of another node", with(map[string]string{`"default":8`: `"default":8,"additional":[20]`}), 422, "Allocation 20 is not on the node of the server.", "allocation"},
		{"the same allocation twice", with(map[string]string{`"default":8`: `"default":8,"additional":[8]`}), 422, "Allocation 8 is missing or used twice.", "allocation"},
		{"a value the rules refuse", with(map[string]string{`"server.jar"`: `"` + strings.Repeat("a", 21) + `"`}), 422, "The Server Jar File variable may not be greater than 20 characters.", "environment.SERVER_JARFILE"},
		{"a required variable that is missing", with(map[string]string{`{"SERVER_JARFILE":"server.jar"}`: `{}`}), 422, "The Server Jar File variable field is required.", "environment.SERVER_JARFILE"},
		{"an external id of another server", with(map[string]string{`"name":"New",`: `"name":"New","external_id":"crm-7",`}), 422, "The external id has already been taken.", "external_id"},
		{"not JSON", `name=x`, 400, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			if tt.name == "an external id of another server" {
				r.store.taken = "external_id"
			}
			rp := r.do(grace, "POST", "", tt.body)
			if rp.Status != tt.status || (tt.detail != "" && rp.Detail() != tt.detail) {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if tt.field != "" && rp.At("errors", 0, "meta", "source_field") != tt.field {
				t.Errorf("field = %v", rp.At("errors", 0, "meta"))
			}
			if len(r.store.servers) != 2 || len(r.daemon.calls) != 0 {
				t.Error("a server was made")
			}
		})
	}
}

func TestAnAllocationTakenMeanwhileIsNotTaken(t *testing.T) {
	// The rows are read again under the lock: an allocation that another request took after the first look is refused all the same.
	r := newRig(t)
	other := uint64(8)
	r.store.allocs[9].ServerID = &other
	rp := r.do(grace, "POST", "", strings.Replace(createBodyOK, `"default":8`, `"default":8,"additional":[9]`, 1))
	if rp.Status != 422 || rp.Detail() != "Allocation 9 is already in use." || r.store.allocs[8].ServerID != nil {
		t.Errorf("answer = %d %s, allocation 8 = %+v", rp.Status, rp.Raw, r.store.allocs[8])
	}
}
