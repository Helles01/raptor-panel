package clientapi

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/activity"
	"github.com/Helles01/raptor-panel/internal/api/listquery"
)

// entry is a stored entry made at the test clock.
func entry(id uint64, event string, actor uint64, ip string, props map[string]any) ActivityEntry {
	s := activity.Stored{ID: id, Event: event, IP: ip, Description: "", Properties: props, Timestamp: t0}
	if actor != 0 {
		s.ActorID = ptr(actor)
	}
	return ActivityEntry{Stored: s}
}

// withActor sets the user behind an entry.
func (w *world) withActor(e ActivityEntry) ActivityEntry {
	if e.ActorID != nil {
		p := w.person(*e.ActorID)
		e.Actor = &p
	}
	return e
}

func (w *world) seedActivity() {
	e1 := entry(1, "auth:success", ada, "198.51.100.1", map[string]any{"ip": "198.51.100.1", "useragent": "Firefox"})
	e2 := entry(2, "user:account.email-changed", grace, "203.0.113.50", map[string]any{"ip": "203.0.113.50", "old": "a@example.com", "new": "b@example.com"})
	e3 := entry(3, "server:file.upload", ada, "198.51.100.1", nil)
	e4 := entry(4, "server:power.start", 0, "", nil)
	e4.Batch = "batch-1"
	e5 := entry(5, "user:api-key.create", ada, "198.51.100.1", nil)
	e5.APIKeyID = ptr(uint64(7))
	w.userLog[ada] = []ActivityEntry{w.withActor(e1), w.withActor(e2), w.withActor(e3), w.withActor(e4), w.withActor(e5)}

	s1 := entry(10, "server:power.start", ada, "198.51.100.1", map[string]any{"ip": "198.51.100.1"})
	s2 := entry(11, "server:settings.rename", cleo, "198.51.100.2", nil)
	s3 := entry(12, "server:backup.start", grace, "203.0.113.50", map[string]any{"name": "x"})
	w.serverLog[100] = []ActivityEntry{w.withActor(s1), w.withActor(s2), w.withActor(s3)}
}

func TestAccountActivityEntries(t *testing.T) {
	r := newRig(t)
	r.w.seedActivity()

	rp := r.as(ada, "GET", "/api/client/account/activity")
	if rp.Status != 200 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if rp.at("object") != "list" || rp.length("data") != 4 {
		t.Fatalf("%d entries (the uploads are left out): %s", rp.length("data"), rp.Raw)
	}
	for i := 0; i < 4; i++ {
		if rp.at("data", i, "object") != "activity_log" {
			t.Errorf("entry %d: object = %v", i, rp.at("data", i, "object"))
		}
		if rp.has("data", i, "attributes", "relationships") {
			t.Errorf("entry %d has relationships without being asked", i)
		}
	}
	if !reflect.DeepEqual(r.w.userReqs[0].Exclude, activity.HiddenEvents()) {
		t.Errorf("excluded events = %v", r.w.userReqs[0].Exclude)
	}

	// The user's own entry: the key is the SHA-1 of the row number, the address is shown, the address among the properties too.
	if got := rp.at("data", 0, "attributes", "id"); got != "356a192b7913b04c54574d18c28d46e6395428ab" {
		t.Errorf("id = %v", got)
	}
	want := map[string]any{
		"id":                      "356a192b7913b04c54574d18c28d46e6395428ab",
		"batch":                   nil,
		"event":                   "auth:success",
		"is_api":                  false,
		"ip":                      "198.51.100.1",
		"description":             "",
		"properties":              map[string]any{"ip": "198.51.100.1", "useragent": "Firefox"},
		"has_additional_metadata": false,
		"timestamp":               "2026-10-23T12:00:00+00:00",
	}
	if got := rp.at("data", 0, "attributes"); !reflect.DeepEqual(got, want) {
		t.Errorf("entry =\n%v\nwant\n%v", got, want)
	}

	// An administrator's entry about this user: the address is not for ada, nor the one among the properties.
	if rp.at("data", 1, "attributes", "ip") != nil || !rp.has("data", 1, "attributes", "ip") {
		t.Errorf("ip = %v, want null", rp.at("data", 1, "attributes", "ip"))
	}
	if rp.at("data", 1, "attributes", "properties", "ip") != "[hidden]" {
		t.Errorf("properties.ip = %v", rp.at("data", 1, "attributes", "properties", "ip"))
	}
	if rp.at("data", 1, "attributes", "properties", "old") != "a@example.com" {
		t.Errorf("properties = %v", rp.at("data", 1, "attributes", "properties"))
	}
	if rp.at("data", 1, "attributes", "has_additional_metadata") != true {
		t.Error("without texts every property is one that is not shown")
	}

	// Done by nobody, in a batch.
	if rp.at("data", 2, "attributes", "batch") != "batch-1" || rp.at("data", 2, "attributes", "ip") != nil {
		t.Errorf("entry = %v", rp.at("data", 2, "attributes"))
	}
	if props, ok := rp.at("data", 2, "attributes", "properties").(map[string]any); !ok || len(props) != 0 {
		t.Errorf("an entry without properties has %v, want {}", rp.at("data", 2, "attributes", "properties"))
	}
	if rp.at("data", 3, "attributes", "is_api") != true {
		t.Error("an entry made through a key is an API entry")
	}
}

func TestActivityOfAdministratorsSeesAddresses(t *testing.T) {
	r := newRig(t)
	r.w.seedActivity()
	r.w.userLog[grace] = r.w.userLog[ada]

	rp := r.as(grace, "GET", "/api/client/account/activity")
	if rp.at("data", 0, "attributes", "ip") != "198.51.100.1" {
		t.Errorf("an administrator does not see the address: %v", rp.at("data", 0, "attributes", "ip"))
	}
	// The address in the properties is for its owner only, an administrator included.
	if rp.at("data", 0, "attributes", "properties", "ip") != "[hidden]" {
		t.Errorf("properties.ip = %v", rp.at("data", 0, "attributes", "properties", "ip"))
	}
	if rp.at("data", 1, "attributes", "properties", "ip") != "203.0.113.50" || rp.at("data", 1, "attributes", "ip") != "203.0.113.50" {
		t.Errorf("an administrator's own entry = %v", rp.at("data", 1, "attributes"))
	}
}

func TestActivityTextsAndZone(t *testing.T) {
	zone := time.FixedZone("CEST", 2*3600)
	r := newRig(t, func(o *options) {
		o.texts = map[string][]string{"user:account.email-changed": {"old", "new"}}
		o.location = zone
	})
	r.w.seedActivity()

	rp := r.as(ada, "GET", "/api/client/account/activity?include=actor")
	if rp.at("data", 1, "attributes", "has_additional_metadata") != false {
		t.Error("a text that shows every property leaves nothing more to show")
	}
	if got := rp.at("data", 1, "attributes", "timestamp"); got != "2026-10-23T14:00:00+02:00" {
		t.Errorf("timestamp = %v", got)
	}
	// Every time of the answer is in the panel's zone, also the creation of a user.
	if got := rp.at("data", 0, "attributes", "relationships", "actor", "attributes", "created_at"); got != "2026-10-21T14:00:00+02:00" {
		t.Errorf("created_at = %v", got)
	}
}

func TestActivityActors(t *testing.T) {
	r := newRig(t)
	r.w.seedActivity()

	rp := r.as(ada, "GET", "/api/client/account/activity?include=actor")
	if rp.Status != 200 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if !r.w.userReqs[0].WithActors {
		t.Error("the actors were not asked for")
	}
	wantActor := map[string]any{"object": "user", "attributes": map[string]any{
		"uuid":        "00000001-0000-4000-8000-000000000000",
		"identifier":  realtime("user", "00000001-0000-4000-8000-000000000000"),
		"username":    "ada",
		"email":       "Ada@Example.com",
		"image":       gravatar("Ada@Example.com"),
		"2fa_enabled": false,
		"created_at":  "2026-10-21T12:00:00+00:00",
	}}
	// The address is shown as stored; the picture address is made of the lower case form of it.
	if got := rp.at("data", 0, "attributes", "relationships", "actor"); !reflect.DeepEqual(got, wantActor) {
		t.Errorf("actor =\n%v\nwant\n%v", got, wantActor)
	}
	if rp.at("data", 2, "attributes", "relationships", "actor", "object") != "null_resource" || rp.at("data", 2, "attributes", "relationships", "actor", "attributes") != nil {
		t.Errorf("an entry without a user = %v", rp.at("data", 2, "attributes", "relationships"))
	}

	// Without the include nothing is read.
	r.as(ada, "GET", "/api/client/account/activity")
	if r.w.userReqs[1].WithActors {
		t.Error("the actors were read without being asked for")
	}
}

func TestActivityRequest(t *testing.T) {
	t.Run("what is passed to the store", func(t *testing.T) {
		r := newRig(t)
		r.as(ada, "GET", "/api/client/account/activity?filter[event]=auth&sort=-timestamp&page=3&per_page=10")
		got := r.w.userReqs[0]
		if got.Page != 3 || got.PerPage != 10 {
			t.Errorf("request = %+v", got)
		}
		wantConds := []listquery.Condition{{Column: "event", Match: listquery.Partial, Values: []string{"auth"}}}
		if !reflect.DeepEqual(got.Query.Conditions, wantConds) || !reflect.DeepEqual(got.Query.Order, []listquery.Order{{Column: "timestamp", Desc: true}}) || got.Query.Table != "activity_logs" {
			t.Errorf("query = %+v", got.Query)
		}
	})

	t.Run("the page is 25 by default and the paging says so", func(t *testing.T) {
		r := newRig(t)
		r.w.seedActivity()
		rp := r.as(ada, "GET", "/api/client/account/activity")
		if r.w.userReqs[0].PerPage != 25 || r.w.userReqs[0].Page != 1 {
			t.Errorf("request = %+v", r.w.userReqs[0])
		}
		for k, want := range map[string]any{"total": 4.0, "count": 4.0, "per_page": 25.0, "current_page": 1.0, "total_pages": 1.0} {
			if got := rp.at("meta", "pagination", k); got != want {
				t.Errorf("pagination.%s = %v, want %v", k, got, want)
			}
		}
	})

	t.Run("a second page has its links", func(t *testing.T) {
		r := newRig(t)
		r.w.seedActivity()
		rp := r.as(ada, "GET", "/api/client/account/activity?per_page=3&page=1&filter[event]=a")
		next, _ := rp.at("meta", "pagination", "links", "next").(string)
		if !strings.HasPrefix(next, "/api/client/account/activity?") || !strings.Contains(next, "page=2") || !strings.Contains(next, "per_page=3") {
			t.Errorf("next = %q", next)
		}
	})

	refused := []struct {
		name, target string
		status       int
	}{
		{"a filter that is not offered", "/api/client/account/activity?filter[ip]=1.2.3.4", 400},
		{"a sort that is not offered", "/api/client/account/activity?sort=ip", 400},
		{"too large a page", "/api/client/account/activity?per_page=101", 422},
		{"an empty page", "/api/client/account/activity?per_page=0", 422},
		{"page zero", "/api/client/account/activity?page=0", 422},
	}
	for _, tt := range refused {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			if rp := r.as(ada, "GET", tt.target); rp.Status != tt.status {
				t.Errorf("status = %d, want %d: %s", rp.Status, tt.status, rp.Raw)
			}
			if len(r.w.userReqs) != 0 {
				t.Error("the store was asked for a request that was refused")
			}
		})
	}

	t.Run("the largest page is allowed", func(t *testing.T) {
		r := newRig(t)
		if rp := r.as(ada, "GET", "/api/client/account/activity?per_page=100"); rp.Status != 200 {
			t.Errorf("status = %d", rp.Status)
		}
	})

	t.Run("another user's entries are not asked for", func(t *testing.T) {
		r := newRig(t)
		r.w.seedActivity()
		if rp := r.as(eve, "GET", "/api/client/account/activity"); rp.length("data") != 0 || rp.at("data") == nil {
			t.Errorf("eve sees %s", rp.Raw)
		}
	})

	t.Run("nobody", func(t *testing.T) {
		r := newRig(t)
		if rp := r.as(0, "GET", "/api/client/account/activity"); rp.Status != 401 {
			t.Errorf("status = %d", rp.Status)
		}
	})

	t.Run("a broken store", func(t *testing.T) {
		r := newRig(t)
		r.w.failActivity = errBoom
		rp := r.as(ada, "GET", "/api/client/account/activity")
		if rp.Status != 500 || strings.Contains(string(rp.Raw), "hunter2") {
			t.Errorf("status = %d: %s", rp.Status, rp.Raw)
		}
	})
}

func TestServerActivity(t *testing.T) {
	t.Run("a subuser needs activity.read", func(t *testing.T) {
		r := newRig(t)
		r.w.seedActivity()
		tests := []struct {
			user   uint64
			status int
		}{{bob, 403}, {cleo, 200}, {ada, 200}, {grace, 200}, {eve, 404}, {0, 401}}
		for _, tt := range tests {
			if rp := r.as(tt.user, "GET", "/api/client/servers/aaaa1111/activity"); rp.Status != tt.status {
				t.Errorf("user %d: status = %d, want %d: %s", tt.user, rp.Status, tt.status, rp.Raw)
			}
		}
	})

	t.Run("the log of this server is asked for", func(t *testing.T) {
		r := newRig(t)
		r.w.seedActivity()
		rp := r.as(cleo, "GET", "/api/client/servers/aaaa1111/activity?filter[event]=power&include=actor&per_page=5")
		if rp.Status != 200 {
			t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
		}
		got := r.w.serverReqs[0]
		if got.ServerID != 100 || got.HideAdmins || got.List.PerPage != 5 || !got.List.WithActors || !reflect.DeepEqual(got.List.Exclude, activity.HiddenEvents()) {
			t.Errorf("request = %+v", got)
		}
		if rp.length("data") != 3 || rp.at("data", 0, "attributes", "relationships", "actor", "attributes", "username") != "ada" {
			t.Errorf("entries = %s", rp.Raw)
		}
		if len(r.w.userReqs) != 0 {
			t.Error("the user's own log was read")
		}
	})

	t.Run("the setting that hides administrators reaches the store", func(t *testing.T) {
		r := newRig(t, func(o *options) { o.hideAdmins = true })
		r.as(ada, "GET", "/api/client/servers/aaaa1111/activity")
		if !r.w.serverReqs[0].HideAdmins {
			t.Error("the setting did not reach the store")
		}
	})

	t.Run("addresses are for the one who did it and for administrators", func(t *testing.T) {
		r := newRig(t)
		r.w.seedActivity()

		// Cleo reads the log: the entries of others have no address, her own has.
		rp := r.as(cleo, "GET", "/api/client/servers/aaaa1111/activity")
		want := []any{nil, "198.51.100.2", nil}
		for i, w := range want {
			if got := rp.at("data", i, "attributes", "ip"); got != w {
				t.Errorf("cleo, entry %d: ip = %v, want %v", i, got, w)
			}
		}
		if rp.at("data", 0, "attributes", "properties", "ip") != "[hidden]" {
			t.Errorf("properties.ip = %v", rp.at("data", 0, "attributes", "properties", "ip"))
		}

		// An administrator reads all addresses.
		rp = r.as(grace, "GET", "/api/client/servers/aaaa1111/activity")
		want = []any{"198.51.100.1", "198.51.100.2", "203.0.113.50"}
		for i, w := range want {
			if got := rp.at("data", i, "attributes", "ip"); got != w {
				t.Errorf("grace, entry %d: ip = %v, want %v", i, got, w)
			}
		}
	})

	t.Run("a server that cannot be used yet has no log", func(t *testing.T) {
		r := newRig(t)
		for _, target := range []string{"/api/client/servers/bbbb2222/activity", "/api/client/servers/dddd4444/activity"} {
			if rp := r.as(ada, "GET", target); rp.Status != 409 {
				t.Errorf("%s: status = %d, want 409: %s", target, rp.Status, rp.Raw)
			}
		}
		if len(r.w.serverReqs) != 0 {
			t.Error("the store was asked")
		}
	})

	t.Run("a broken store", func(t *testing.T) {
		r := newRig(t)
		r.w.failActivity = errBoom
		rp := r.as(ada, "GET", "/api/client/servers/aaaa1111/activity")
		if rp.Status != 500 || strings.Contains(string(rp.Raw), "hunter2") {
			t.Errorf("status = %d: %s", rp.Status, rp.Raw)
		}
	})

	t.Run("refused requests do not reach the store", func(t *testing.T) {
		r := newRig(t)
		for _, target := range []string{"?filter[ip]=1", "?sort=ip", "?per_page=101"} {
			if rp := r.as(ada, "GET", "/api/client/servers/aaaa1111/activity"+target); rp.Status < 400 || rp.Status > 422 {
				t.Errorf("%s: status = %d", target, rp.Status)
			}
		}
		if len(r.w.serverReqs) != 0 {
			t.Error("the store was asked")
		}
	})
}

func TestServerActivityIsOfTheServerInTheRoute(t *testing.T) {
	r := newRig(t)
	r.w.seedActivity()
	r.w.serverLog[102] = []ActivityEntry{entry(20, "server:power.stop", eve, "198.51.100.9", nil)}

	rp := r.as(eve, "GET", "/api/client/servers/cccc3333/activity")
	if rp.Status != 200 || rp.length("data") != 1 || rp.at("data", 0, "attributes", "event") != "server:power.stop" {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if got := r.w.serverReqs[0].ServerID; got != 102 {
		t.Errorf("the log of server %d was read", got)
	}
}
