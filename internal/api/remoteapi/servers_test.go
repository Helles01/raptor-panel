package remoteapi

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/servers"
)

func uuidsOf(rp interface{ At(...any) any }, n int) []string {
	var out []string
	for i := 0; i < n; i++ {
		s, _ := rp.At("data", i, "uuid").(string)
		out = append(out, s)
	}
	return out
}

func TestListOfTheServersOfANode(t *testing.T) {
	r := newRig(t)

	rp := r.get(node1, "/api/remote/servers")
	if rp.Status != 200 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	want := []string{normalUUID, installingUUID, reinstallUUID, restoringUUID, suspendedUUID, movingUUID}
	if got := uuidsOf(rp, 6); !reflect.DeepEqual(got, want) {
		t.Errorf("servers = %v, want %v", got, want)
	}
	meta := map[string]any{"current_page": 1.0, "from": 1.0, "last_page": 1.0, "per_page": 50.0, "to": 6.0, "total": 6.0}
	if got := rp.At("meta"); !reflect.DeepEqual(got, meta) {
		t.Errorf("meta = %v, want %v", got, meta)
	}
	if calls := r.store.listCalls; !reflect.DeepEqual(calls, [][3]int{{node1, 0, 50}}) {
		t.Errorf("store asked %v", calls)
	}

	// What the node reads of each server.
	if rp.At("data", 0, "settings", "uuid") != normalUUID || rp.At("data", 0, "settings", "invocation") != "java -jar server.jar" ||
		rp.At("data", 0, "settings", "container", "image") != "ghcr.io/example/java:21" || rp.At("data", 0, "settings", "build", "memory_limit") != 1024.0 {
		t.Errorf("settings = %v", rp.At("data", 0, "settings"))
	}
	if rp.At("data", 0, "process_configuration", "stop", "value") != "stop" || !rp.Has("data", 0, "process_configuration", "configs") {
		t.Errorf("process configuration = %v", rp.At("data", 0, "process_configuration"))
	}
	if rp.At("data", 0, "settings", "mounts") == nil {
		t.Error("mounts is null, the node needs a list")
	}
}

func TestListOfAnotherNode(t *testing.T) {
	r := newRig(t)
	rp := r.get(node2, "/api/remote/servers")
	if got := uuidsOf(rp, 1); !reflect.DeepEqual(got, []string{otherNodeUUID}) || rp.At("meta", "total") != 1.0 {
		t.Errorf("answer = %s", rp.Raw)
	}
	rp = r.get(node3, "/api/remote/servers")
	if rp.Status != 200 || rp.At("data") == nil || len(rp.At("data").([]any)) != 0 {
		t.Errorf("a node without servers: %d %s", rp.Status, rp.Raw)
	}
	if rp.At("meta", "from") != nil || !rp.Has("meta", "from") || rp.At("meta", "last_page") != 1.0 {
		t.Errorf("meta = %v", rp.At("meta"))
	}
}

func TestListPaging(t *testing.T) {
	tests := []struct {
		query  string
		offset int
		limit  int
	}{
		{"", 0, 50},
		{"?page=2&per_page=4", 4, 4},
		{"?per_page=1000", 0, 500},
		{"?page=abc&per_page=xyz", 0, 50},
		{"?page=0&per_page=0", 0, 50},
		{"?page=-3", 0, 50},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			r := newRig(t)
			r.get(node1, "/api/remote/servers"+tt.query)
			if got := r.store.listCalls[0]; got != [3]int{node1, tt.offset, tt.limit} {
				t.Errorf("store asked %v, want offset %d limit %d", got, tt.offset, tt.limit)
			}
		})
	}

	r := newRig(t)
	rp := r.get(node1, "/api/remote/servers?page=2&per_page=4")
	meta := map[string]any{"current_page": 2.0, "from": 5.0, "last_page": 2.0, "per_page": 4.0, "to": 6.0, "total": 6.0}
	if got := rp.At("meta"); !reflect.DeepEqual(got, meta) || len(rp.At("data").([]any)) != 2 {
		t.Errorf("answer = %s", rp.Raw)
	}
	rp = r.get(node1, "/api/remote/servers?page=9&per_page=4")
	if len(rp.At("data").([]any)) != 0 || rp.At("meta", "from") != nil {
		t.Errorf("a page past the end: %s", rp.Raw)
	}
}

func TestListLeavesOutAServerWhoseEggIsBroken(t *testing.T) {
	r := newRig(t)
	r.store.records[1].config.Egg.ConfigStartup = "{not json"

	rp := r.get(node1, "/api/remote/servers")
	if rp.Status != 200 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	got := uuidsOf(rp, 5)
	if len(rp.At("data").([]any)) != 5 || got[0] != normalUUID || got[1] != reinstallUUID {
		t.Errorf("servers = %v", got)
	}
	if !strings.Contains(r.logs.String(), installingUUID) {
		t.Errorf("the fault was not logged: %s", r.logs.String())
	}
	// The total still counts every server, so that the node does not think the last page is missing.
	if rp.At("meta", "total") != 6.0 {
		t.Errorf("total = %v", rp.At("meta", "total"))
	}
}

func TestListStoreFailure(t *testing.T) {
	r := newRig(t)
	r.store.failList = errBoom
	rp := r.get(node1, "/api/remote/servers")
	if rp.Status != 500 || rp.Contains("hunter2") {
		t.Errorf("answer = %d %s", rp.Status, rp.Raw)
	}
}

func TestDetails(t *testing.T) {
	tests := []struct {
		name   string
		node   uint64
		uuid   string
		status int
	}{
		{"its own node", node1, normalUUID, 200},
		{"another node", node2, normalUUID, 403},
		{"a node that has nothing to do with it", node3, normalUUID, 403},
		{"a server that does not exist", node1, "12345678-2222-4333-8444-555555555555", 403},
		{"a server that is being moved, the old node", node1, movingUUID, 200},
		{"a server that is being moved, the new node", node2, movingUUID, 200},
		{"a server that is being moved, a third node", node3, movingUUID, 403},
		{"a UUID in upper case", node1, strings.ToUpper(normalUUID), 200},
		{"not a UUID", node1, "aaaa1111", 404},
		{"a path", node1, "..%2F..%2Fetc", 404},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.get(tt.node, "/api/remote/servers/"+tt.uuid)
			if rp.Status != tt.status {
				t.Fatalf("status = %d, want %d: %s", rp.Status, tt.status, rp.Raw)
			}
			if tt.status == 403 && rp.Detail() != "Requesting node does not have permission to access this server." {
				t.Errorf("detail = %q", rp.Detail())
			}
			if tt.status == 200 && (rp.At("uuid") == nil || rp.At("settings", "uuid") == nil || !rp.Has("process_configuration", "startup")) {
				t.Errorf("answer = %s", rp.Raw)
			}
		})
	}

	t.Run("a store that finds servers of other nodes is not trusted", func(t *testing.T) {
		r := newRig(t)
		r.store.leak = true
		if rp := r.get(node2, "/api/remote/servers/"+normalUUID); rp.Status != 403 {
			t.Errorf("status = %d: %s", rp.Status, rp.Raw)
		}
		if rp := r.get(node3, "/api/remote/servers/"+movingUUID); rp.Status != 403 {
			t.Errorf("a third node and a transfer: status = %d", rp.Status)
		}
	})
	t.Run("a broken egg", func(t *testing.T) {
		r := newRig(t)
		r.store.records[0].config.Egg.ConfigFiles = "{not json"
		rp := r.get(node1, "/api/remote/servers/"+normalUUID)
		if rp.Status != 500 || rp.Contains("not json") {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
	t.Run("a broken store", func(t *testing.T) {
		r := newRig(t)
		r.store.failFind = errBoom
		rp := r.get(node1, "/api/remote/servers/"+normalUUID)
		if rp.Status != 500 || rp.Contains("hunter2") {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
}

func TestInstallScript(t *testing.T) {
	r := newRig(t)
	rp := r.get(node1, "/api/remote/servers/"+installingUUID+"/install")
	want := map[string]any{"container_image": "ghcr.io/example/installer:latest", "entrypoint": "bash", "script": "#!/bin/bash\necho installing"}
	if rp.Status != 200 || !reflect.DeepEqual(rp.Body, want) {
		t.Errorf("answer = %d %s", rp.Status, rp.Raw)
	}
	for _, node := range []uint64{node2, node3} {
		if rp := r.get(node, "/api/remote/servers/"+installingUUID+"/install"); rp.Status != 403 {
			t.Errorf("node %d: status = %d", node, rp.Status)
		}
	}
	// A node that is moving a server is not the node of it for this.
	if rp := r.get(node2, "/api/remote/servers/"+movingUUID+"/install"); rp.Status != 403 {
		t.Errorf("the new node of a move: status = %d", rp.Status)
	}
	r.store.leak = true
	if rp := r.get(node2, "/api/remote/servers/"+installingUUID+"/install"); rp.Status != 403 {
		t.Errorf("a store that leaks: status = %d", rp.Status)
	}
}

func report(successful, reinstall string) string {
	parts := []string{}
	if successful != "" {
		parts = append(parts, `"successful":`+successful)
	}
	if reinstall != "" {
		parts = append(parts, `"reinstall":`+reinstall)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func TestInstallReports(t *testing.T) {
	tests := []struct {
		name       string
		uuid       string
		body       string
		wantStatus string
		wantNotify servers.Notification
	}{
		{"the first installation worked", installingUUID, report("true", ""), "", servers.NotifyInstalled},
		{"the first installation failed", installingUUID, report("false", ""), servers.StatusInstallFailed, servers.NotifyNone},
		{"a reinstallation worked", reinstallUUID, report("true", "true"), "", servers.NotifyReinstalled},
		{"a reinstallation failed", reinstallUUID, report("false", "true"), servers.StatusReinstallFailed, servers.NotifyNone},
		{"the flags as numbers", installingUUID, report("1", "0"), "", servers.NotifyInstalled},
		{"a suspended server stays suspended", suspendedUUID, report("true", ""), servers.StatusSuspended, servers.NotifyInstalled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.post(node1, "/api/remote/servers/"+tt.uuid+"/install", tt.body)
			if rp.Status != 204 {
				t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
			}
			if len(r.store.installs) != 1 {
				t.Fatalf("%d installs", len(r.store.installs))
			}
			got := r.store.installs[0]
			if got.Outcome.Status != tt.wantStatus || got.Outcome.Notify != tt.wantNotify || !got.Outcome.SetInstalledAt || !got.At.Equal(r.store.installs[0].At) {
				t.Errorf("outcome = %+v", got.Outcome)
			}
			if got.From != r.store.find(node1, tt.uuid).status {
				t.Errorf("stored over the status %q", got.From)
			}
			wantMails := 0
			if tt.wantNotify != servers.NotifyNone {
				wantMails = 1
			}
			if len(r.store.mails) != wantMails {
				t.Errorf("%d mails", len(r.store.mails))
			}
		})
	}

	t.Run("the mail goes to the owner", func(t *testing.T) {
		r := newRig(t)
		r.post(node1, "/api/remote/servers/"+installingUUID+"/install", report("true", ""))
		if len(r.store.mails) != 1 || r.store.mails[0].To != "ada@example.com" || !strings.Contains(r.store.mails[0].Subject+r.store.mails[0].Text, "Creative") {
			t.Errorf("mails = %+v", r.store.mails)
		}
	})
	t.Run("the switches of the mails", func(t *testing.T) {
		r := newRig(t, func(o *options) { o.notify.Install = false })
		r.post(node1, "/api/remote/servers/"+installingUUID+"/install", report("true", ""))
		r.post(node1, "/api/remote/servers/"+reinstallUUID+"/install", report("true", "true"))
		if len(r.store.mails) != 1 {
			t.Errorf("%d mails, want only the reinstallation", len(r.store.mails))
		}
		r = newRig(t, func(o *options) { o.notify.Reinstall = false })
		r.post(node1, "/api/remote/servers/"+reinstallUUID+"/install", report("true", "true"))
		if len(r.store.mails) != 0 {
			t.Errorf("%d mails", len(r.store.mails))
		}
	})
}

func TestInstallReportRefusals(t *testing.T) {
	tests := []struct {
		name   string
		node   uint64
		uuid   string
		body   string
		status int
		detail string
	}{
		{"a server that is not installing", node1, normalUUID, report("true", ""), 409, "This server is not waiting for the result of an installation."},
		{"a server that is restoring", node1, restoringUUID, report("true", ""), 409, ""},
		{"another node", node2, installingUUID, report("true", ""), 403, "Requesting node does not have permission to access this server."},
		{"a server that does not exist", node1, "12345678-2222-4333-8444-555555555555", report("true", ""), 403, ""},
		{"no result", node1, installingUUID, report("", "true"), 422, "The successful field is required."},
		{"a null result", node1, installingUUID, `{"successful":null}`, 422, "The successful field is required."},
		{"a result that is not a flag", node1, installingUUID, report(`"yes"`, ""), 422, "The successful field must be true or false."},
		{"a reinstall that is not a flag", node1, installingUUID, report("true", `"maybe"`), 422, "The reinstall field must be true or false."},
		{"not JSON", node1, installingUUID, `successful`, 400, ""},
		{"empty", node1, installingUUID, ``, 400, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.post(tt.node, "/api/remote/servers/"+tt.uuid+"/install", tt.body)
			if rp.Status != tt.status {
				t.Fatalf("status = %d, want %d: %s", rp.Status, tt.status, rp.Raw)
			}
			if tt.detail != "" && rp.Detail() != tt.detail {
				t.Errorf("detail = %q", rp.Detail())
			}
			if len(r.store.installs) != 0 || len(r.store.mails) != 0 {
				t.Error("something was stored or mailed for a refused report")
			}
		})
	}

	t.Run("a store that finds servers of other nodes is not trusted", func(t *testing.T) {
		r := newRig(t)
		r.store.leak = true
		if rp := r.post(node2, "/api/remote/servers/"+installingUUID+"/install", report("true", "")); rp.Status != 403 || len(r.store.installs) != 0 {
			t.Errorf("status = %d", rp.Status)
		}
	})
	t.Run("two reports at once: the second loses", func(t *testing.T) {
		r := newRig(t)
		r.store.installRace = true
		rp := r.post(node1, "/api/remote/servers/"+installingUUID+"/install", report("true", ""))
		if rp.Status != 409 || len(r.store.mails) != 0 {
			t.Errorf("status = %d, %d mails", rp.Status, len(r.store.mails))
		}
	})
	t.Run("a mail that cannot be queued does not undo the report", func(t *testing.T) {
		r := newRig(t)
		r.store.failMail = errors.New("outbox full")
		rp := r.post(node1, "/api/remote/servers/"+installingUUID+"/install", report("true", ""))
		if rp.Status != 204 || !strings.Contains(r.logs.String(), "the mail about an installation was not queued") {
			t.Errorf("status = %d, logs %s", rp.Status, r.logs.String())
		}
	})
	t.Run("a broken store", func(t *testing.T) {
		r := newRig(t)
		r.store.failFind = errBoom
		rp := r.post(node1, "/api/remote/servers/"+installingUUID+"/install", report("true", ""))
		if rp.Status != 500 || rp.Contains("hunter2") {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
}

func TestReset(t *testing.T) {
	r := newRig(t)

	rp := r.post(node1, "/api/remote/servers/reset", "")
	if rp.Status != 204 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	want := []ResetChange{
		{ServerID: 101, From: servers.StatusInstalling, To: servers.StatusInstallFailed},
		{ServerID: 102, From: servers.StatusInstalling, To: servers.StatusReinstallFailed},
		{ServerID: 103, From: servers.StatusRestoringBackup, To: "", RestoreFailed: &BackupRef{ID: 7, Name: "nightly"}},
	}
	if len(r.store.resets) != 1 || !reflect.DeepEqual(r.store.resets[0], want) {
		t.Errorf("changes = %+v\nwant %+v", r.store.resets, want)
	}
}

func TestResetTouchesOnlyTheCallingNode(t *testing.T) {
	r := newRig(t)
	r.store.records[5].status = servers.StatusInstalling // the server of node 2

	r.post(node3, "/api/remote/servers/reset", "")
	if len(r.store.resets) != 0 {
		t.Errorf("a node without servers reset %+v", r.store.resets)
	}
	r.post(node2, "/api/remote/servers/reset", "")
	if len(r.store.resets) != 1 || len(r.store.resets[0]) != 1 || r.store.resets[0][0].ServerID != 105 {
		t.Errorf("changes = %+v", r.store.resets)
	}
}

func TestResetWithoutAKnownBackup(t *testing.T) {
	r := newRig(t)
	r.store.records[3].backup = nil
	r.post(node1, "/api/remote/servers/reset", "")
	for _, c := range r.store.resets[0] {
		if c.ServerID == 103 && (c.RestoreFailed != nil || c.To != "") {
			t.Errorf("change = %+v: the server is normal again, without an entry", c)
		}
	}
}

func TestResetFailure(t *testing.T) {
	r := newRig(t)
	r.store.failReset = errBoom
	rp := r.post(node1, "/api/remote/servers/reset", "")
	if rp.Status != 500 || rp.Contains("hunter2") {
		t.Errorf("answer = %d %s", rp.Status, rp.Raw)
	}
}

// Only the two statuses that a restart interrupts are reset, whatever a store hands over: a suspended server stays suspended.
func TestResetLeavesOtherStatusesAlone(t *testing.T) {
	r := newRig(t)
	r.store.extraReset = []ResetCandidate{
		{ID: 104, Status: servers.StatusSuspended},
		{ID: 100, Status: ""},
		{ID: 105, Status: servers.StatusInstallFailed},
	}
	r.post(node1, "/api/remote/servers/reset", "")
	for _, c := range r.store.resets[0] {
		if c.ServerID == 104 || c.ServerID == 100 || c.ServerID == 105 {
			t.Errorf("change = %+v: this server was not interrupted", c)
		}
	}
}
