package remoteapi

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/activity"
)

const activityURL = "/api/remote/activity"

func event(server, user, name, metadata, ip, timestamp string) string {
	return fmt.Sprintf(`{"user":%q,"server":%q,"event":%q,"metadata":%s,"ip":%q,"timestamp":%q}`, user, server, name, metadata, ip, timestamp)
}

func batch(events ...string) string { return `{"data":[` + strings.Join(events, ",") + `]}` }

func TestActivityIsStored(t *testing.T) {
	r := newRig(t)
	body := batch(
		event(normalUUID, userUUID(ada), "server:power.start", `{}`, "198.51.100.7", "2026-10-23T11:59:00Z"),
		event(normalUUID, "", "server:console.command", `{"command":"say hi"}`, "", "2026-10-23T11:59:30.123456789Z"),
		event(installingUUID, userUUID(bob), "server:sftp.write", `{"files":["/a.txt"]}`, "203.0.113.1", "2026-10-23T11:58:00+02:00"),
	)

	rp := r.post(node1, activityURL, body)
	if rp.Status != 204 || len(rp.Raw) != 0 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	want := []StoredEntry{
		{ServerID: 100, UserID: ada, Event: "server:power.start", IP: "198.51.100.7", Properties: `{}`, When: time.Date(2026, 10, 23, 11, 59, 0, 0, time.UTC)},
		{ServerID: 100, UserID: 0, Event: "server:console.command", IP: "127.0.0.1", Properties: `{"command":"say hi"}`, When: time.Date(2026, 10, 23, 11, 59, 30, 123456789, time.UTC)},
		{ServerID: 101, UserID: bob, Event: "server:sftp.write", IP: "203.0.113.1", Properties: `{"files":["/a.txt"]}`, When: time.Date(2026, 10, 23, 9, 58, 0, 0, time.UTC)},
	}
	if len(r.store.activity) != len(want) {
		t.Fatalf("%d entries: %+v", len(r.store.activity), r.store.activity)
	}
	for i, w := range want {
		g := r.store.activity[i]
		if g.ServerID != w.ServerID || g.UserID != w.UserID || g.Event != w.Event || g.IP != w.IP || g.Properties != w.Properties || !g.When.Equal(w.When) {
			t.Errorf("entry %d = %+v, want %+v", i, g, w)
		}
	}
	if r.store.storeCalls != 1 {
		t.Errorf("%d writes, the entries belong in one transaction", r.store.storeCalls)
	}
}

func TestActivityOfOtherServersAndOtherEventsIsLeftOut(t *testing.T) {
	r := newRig(t)
	body := batch(
		event(normalUUID, userUUID(ada), "server:power.start", `{}`, "", "2026-10-23T11:59:00Z"),
		event(otherNodeUUID, userUUID(ada), "server:power.start", `{}`, "", "2026-10-23T11:59:00Z"), // the server of node 2
		event("12345678-2222-4333-8444-555555555555", "", "server:power.start", `{}`, "", "2026-10-23T11:59:00Z"),
		event(normalUUID, userUUID(ada), "auth:success", `{}`, "", "2026-10-23T11:59:00Z"), // an event only the panel writes
		event(normalUUID, userUUID(ada), "server:forged", `{}`, "", "2026-10-23T11:59:00Z"),
		`{"server":5}`,
		`"text"`,
	)

	rp := r.post(node1, activityURL, body)
	if rp.Status != 204 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if len(r.store.activity) != 1 || r.store.activity[0].Event != "server:power.start" || r.store.activity[0].ServerID != 100 {
		t.Errorf("entries = %+v", r.store.activity)
	}
	if !strings.Contains(r.logs.String(), "events of a node were left out") || !strings.Contains(r.logs.String(), "skipped=6") {
		t.Errorf("the left-out events were not counted in the log: %s", r.logs.String())
	}
}

func TestActivityOfAUserThatIsNotKnownIsStoredWithoutOne(t *testing.T) {
	r := newRig(t)
	body := batch(event(normalUUID, "99999999-0000-4000-8000-000000000000", "server:power.stop", `{}`, "", "2026-10-23T11:59:00Z"))
	r.post(node1, activityURL, body)
	if len(r.store.activity) != 1 || r.store.activity[0].UserID != 0 {
		t.Errorf("entries = %+v", r.store.activity)
	}
}

func TestActivityOfAnotherNodeIsNotStoredForThisOne(t *testing.T) {
	r := newRig(t)
	body := batch(event(normalUUID, userUUID(ada), "server:power.start", `{}`, "", "2026-10-23T11:59:00Z"))
	if rp := r.post(node2, activityURL, body); rp.Status != 204 {
		t.Fatalf("status = %d", rp.Status)
	}
	if len(r.store.activity) != 0 || r.store.storeCalls != 0 {
		t.Errorf("node 2 wrote entries for a server of node 1: %+v", r.store.activity)
	}
}

func TestActivityUUIDsInUpperCase(t *testing.T) {
	r := newRig(t)
	body := batch(event(strings.ToUpper(normalUUID), strings.ToUpper(userUUID(ada)), "server:power.start", `{}`, "", "2026-10-23T11:59:00Z"))
	r.post(node1, activityURL, body)
	if len(r.store.activity) != 1 || r.store.activity[0].UserID != ada {
		t.Errorf("entries = %+v", r.store.activity)
	}
}

func TestActivityNothingToStore(t *testing.T) {
	for name, body := range map[string]string{
		"an empty batch":  `{"data":[]}`,
		"only bad events": batch(`{"event":"x"}`, event(normalUUID, "", "auth:fail", `{}`, "", "2026-10-23T11:59:00Z")),
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			if rp := r.post(node1, activityURL, body); rp.Status != 204 {
				t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
			}
			if r.store.storeCalls != 0 {
				t.Error("the store was written to")
			}
		})
	}
}

func TestActivityMalformedRequests(t *testing.T) {
	many := make([]string, activity.MaxBatch+1)
	for i := range many {
		many[i] = event(normalUUID, "", "server:power.start", `{}`, "", "2026-10-23T11:59:00Z")
	}
	tests := []struct {
		name   string
		body   string
		status int
	}{
		{"not JSON", `data`, 400},
		{"empty", ``, 400},
		{"no data", `{}`, 400},
		{"data that is not a list", `{"data":{"a":1}}`, 400},
		{"too many events", batch(many...), 400},
		{"a body that is too large", `{"data":["` + strings.Repeat("a", 3<<20) + `"]}`, 413},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			if rp := r.post(node1, activityURL, tt.body); rp.Status != tt.status {
				t.Errorf("status = %d, want %d: %s", rp.Status, tt.status, rp.Raw)
			}
			if r.store.storeCalls != 0 {
				t.Error("something was stored")
			}
		})
	}

	t.Run("the largest batch", func(t *testing.T) {
		r := newRig(t)
		if rp := r.post(node1, activityURL, batch(many[:activity.MaxBatch]...)); rp.Status != 204 || len(r.store.activity) != activity.MaxBatch {
			t.Errorf("status = %d, %d entries", rp.Status, len(r.store.activity))
		}
	})
}

func TestActivityStoreFailures(t *testing.T) {
	body := batch(event(normalUUID, userUUID(ada), "server:power.start", `{}`, "", "2026-10-23T11:59:00Z"))
	r := newRig(t)
	r.store.failActivity = errBoom
	rp := r.post(node1, activityURL, body)
	if rp.Status != 500 || rp.Contains("hunter2") {
		t.Errorf("answer = %d %s", rp.Status, rp.Raw)
	}
}
