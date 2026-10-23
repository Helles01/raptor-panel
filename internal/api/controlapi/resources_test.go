package controlapi

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/wings"
)

func running() wings.Details {
	d := wings.Details{State: "running", IsSuspended: false}
	d.Utilization.MemoryBytes = 1048576
	d.Utilization.MemoryLimitBytes = 2097152
	d.Utilization.CPUAbsolute = 12.5
	d.Utilization.DiskBytes = 4096
	d.Utilization.Uptime = 3600
	d.Utilization.Network.RxBytes = 10
	d.Utilization.Network.TxBytes = 20
	return d
}

func TestResources(t *testing.T) {
	r := newRig(t)
	r.daemon.details = running()

	rp := r.get(ada, "/api/client/servers/aaaa1111/resources")
	if rp.Status != 200 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	want := map[string]any{"object": "stats", "attributes": map[string]any{
		"current_state": "running",
		"is_suspended":  false,
		"resources": map[string]any{
			"memory_bytes": 1048576.0, "cpu_absolute": 12.5, "disk_bytes": 4096.0,
			"network_rx_bytes": 10.0, "network_tx_bytes": 20.0, "uptime": 3600.0,
		},
	}}
	if !reflect.DeepEqual(rp.Body, want) {
		t.Errorf("answer =\n%v\nwant\n%v", rp.Body, want)
	}
	if calls := r.daemon.all(); !reflect.DeepEqual(calls, []daemonCall{{Kind: "details", Node: node1, UUID: normalUUID}}) {
		t.Errorf("calls = %+v", calls)
	}
}

func TestResourcesDefaults(t *testing.T) {
	r := newRig(t)
	r.daemon.details = wings.Details{IsSuspended: true}

	rp := r.get(ada, "/api/client/servers/aaaa1111/resources")
	if rp.At("attributes", "current_state") != "stopped" || rp.At("attributes", "is_suspended") != true {
		t.Errorf("answer = %s", rp.Raw)
	}
	if rp.At("attributes", "resources", "memory_bytes") != 0.0 || rp.At("attributes", "resources", "cpu_absolute") != 0.0 {
		t.Errorf("resources = %v", rp.At("attributes", "resources"))
	}
}

func TestResourcesAreKeptForTwentySeconds(t *testing.T) {
	r := newRig(t)
	r.daemon.details = running()
	url := "/api/client/servers/aaaa1111/resources"

	r.get(ada, url)
	r.clock = r.clock.Add(19*time.Second + 900*time.Millisecond)
	// Anyone who may see the server shares the answer.
	bobs := r.get(bob, url)
	if bobs.Status != 200 || r.daemon.count("details") != 1 {
		t.Fatalf("status %d, %d calls to the node after 19.9 s, want 1", bobs.Status, r.daemon.count("details"))
	}

	r.clock = r.clock.Add(100 * time.Millisecond)
	r.daemon.details = wings.Details{State: "offline"}
	rp := r.get(ada, url)
	if r.daemon.count("details") != 2 || rp.At("attributes", "current_state") != "offline" {
		t.Errorf("%d calls, state %v, after 20 s", r.daemon.count("details"), rp.At("attributes", "current_state"))
	}

	// Another server has its own entry.
	r.daemon.details = running()
	other := r.get(ada, "/api/client/servers/dddd4444/resources")
	if other.At("attributes", "current_state") != "running" || r.daemon.count("details") != 3 {
		t.Errorf("state %v after %d calls to the node, want a call for the other server", other.At("attributes", "current_state"), r.daemon.count("details"))
	}
	if calls := r.daemon.all(); calls[2].UUID != otherUUID {
		t.Errorf("the node was asked about %s", calls[2].UUID)
	}
}

func TestResourcesErrorsAreNotKept(t *testing.T) {
	r := newRig(t)
	r.daemon.err = &wings.Error{Status: 504, Unreachable: true, Message: "Could not establish a connection to the machine running this server. Please try again."}
	url := "/api/client/servers/aaaa1111/resources"

	if rp := r.get(ada, url); rp.Status != 504 || rp.Code() != "DaemonConnectionException" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	r.daemon.err = nil
	if rp := r.get(ada, url); rp.Status != 200 {
		t.Errorf("after the node came back: %d", rp.Status)
	}
	if r.daemon.count("details") != 2 {
		t.Errorf("%d calls, the failure was kept", r.daemon.count("details"))
	}
}

func TestResourcesAccess(t *testing.T) {
	tests := []struct {
		name   string
		user   uint64
		target string
		status int
	}{
		{"the owner", ada, "aaaa1111", 200},
		{"a subuser with no right to this", bob, "aaaa1111", 200},
		{"an administrator", grace, "aaaa1111", 200},
		{"a stranger", eve, "aaaa1111", 404},
		{"nobody", 0, "aaaa1111", 401},
		{"a server that is installing", ada, "bbbb2222", 409},
		{"a server that is being moved", ada, "cccc3333", 409},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			if rp := r.get(tt.user, "/api/client/servers/"+tt.target+"/resources"); rp.Status != tt.status {
				t.Errorf("status = %d, want %d", rp.Status, tt.status)
			}
			if tt.status != 200 && r.daemon.count("details") != 0 {
				t.Error("the node was called")
			}
		})
	}
}

func TestResourcesFailures(t *testing.T) {
	t.Run("the node of the server is gone", func(t *testing.T) {
		r := newRig(t)
		delete(r.targets.targets, 100)
		if rp := r.get(ada, "/api/client/servers/aaaa1111/resources"); rp.Status != 404 {
			t.Errorf("status = %d", rp.Status)
		}
	})
	t.Run("a failure that is not the node's does not leak", func(t *testing.T) {
		r := newRig(t)
		r.daemon.err = errBoom
		rp := r.get(ada, "/api/client/servers/aaaa1111/resources")
		if rp.Status != 500 || rp.Contains("hunter2") {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
}

func TestTheCacheIsBounded(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	c := newDetailsCache(20*time.Second, func() time.Time { return now })
	d := wings.Details{State: "running"}

	for i := 0; i < maxCachedServers; i++ {
		c.put(fmt.Sprintf("server-%d", i), d)
	}
	// Full of entries that are still good: another server is not kept, and the first ones are not pushed out.
	c.put("one-too-many", d)
	if _, ok := c.get("one-too-many"); ok {
		t.Error("an entry was added to a full cache")
	}
	if _, ok := c.get("server-0"); !ok {
		t.Error("a good entry was pushed out")
	}

	// A known server is renewed even when the cache is full.
	now = now.Add(10 * time.Second)
	c.put("server-0", wings.Details{State: "offline"})
	if got, ok := c.get("server-0"); !ok || got.State != "offline" {
		t.Errorf("a known entry was not renewed: %v %v", got, ok)
	}

	// Once the others have expired there is room again.
	now = now.Add(11 * time.Second)
	c.put("one-too-many", d)
	if _, ok := c.get("one-too-many"); !ok {
		t.Error("expired entries were not swept")
	}
	if _, ok := c.get("server-1"); ok {
		t.Error("an expired entry was served")
	}
	if len(c.items) > maxCachedServers {
		t.Errorf("%d entries", len(c.items))
	}
}
