package remoteapi

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apitest"
	"github.com/Helles01/raptor-panel/internal/backups"
)

func reportURL(n int) string  { return "/api/remote/backups/" + backupUUIDN(n) }
func restoreURL(n int) string { return "/api/remote/backups/" + backupUUIDN(n) + "/restore" }

const goodReport = `{"successful":true,"checksum":"abc123","checksum_type":"sha256","size":1048576}`

func TestBackupReportSuccess(t *testing.T) {
	r := newRig(t)
	rp := r.post(node1, reportURL(1), goodReport)
	if rp.Status != 204 || len(rp.Raw) != 0 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	want := []completedBackup{{ID: 1, Result: backups.Result{Successful: true, Checksum: "sha256:abc123", Bytes: 1048576}, At: apitest.Now}}
	if !reflect.DeepEqual(r.store.backupDone, want) {
		t.Errorf("stored %+v, want %+v", r.store.backupDone, want)
	}
	wantEvent := Event{Name: "server:backup.complete", ServerSubject: 100, BackupSubject: 1, IP: "203.0.113.9", Properties: `{"name":"Running"}`}
	if len(r.store.events) != 1 || r.store.events[0] != wantEvent {
		t.Errorf("events = %+v, want %+v", r.store.events, wantEvent)
	}
}

func TestBackupReportFailureUnlocks(t *testing.T) {
	r := newRig(t)
	r.store.backupList[0].Locked = true
	if rp := r.post(node1, reportURL(1), `{"successful":false}`); rp.Status != 204 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	// A failed backup has no checksum, no size and no lock: nobody can unlock it afterwards.
	if want := (backups.Result{}); len(r.store.backupDone) != 1 || r.store.backupDone[0].Result != want {
		t.Errorf("stored %+v", r.store.backupDone)
	}
	if len(r.store.events) != 1 || r.store.events[0].Name != "server:backup.fail" {
		t.Errorf("events = %+v", r.store.events)
	}

	// A lock set on a backup that works stays.
	r = newRig(t)
	r.store.backupList[0].Locked = true
	r.post(node1, reportURL(1), goodReport)
	if !r.store.backupDone[0].Result.Locked {
		t.Error("the lock of a successful backup was lost")
	}
}

func TestBackupReportRefusals(t *testing.T) {
	tests := []struct {
		name   string
		node   uint64
		url    string
		body   string
		status int
		detail string
		field  string
	}{
		{"a backup that has ended", node1, reportURL(2), goodReport, 400, "Cannot update the status of a backup that is already marked as completed.", ""},
		{"a backup of another node", node2, reportURL(1), goodReport, 403, "Requesting node does not have permission to access this server.", ""},
		{"a backup of another node, the other way", node1, reportURL(3), goodReport, 403, "", ""},
		{"a backup that does not exist", node1, reportURL(9), goodReport, 403, "", ""},
		{"not a UUID", node1, "/api/remote/backups/12", goodReport, 404, "", ""},
		{"no verdict", node1, reportURL(1), `{"checksum":"x"}`, 422, "The successful field is required.", "successful"},
		{"a verdict that is not a flag", node1, reportURL(1), `{"successful":"maybe"}`, 422, "The successful field must be true or false.", "successful"},
		{"no checksum type", node1, reportURL(1), `{"successful":true,"checksum":"abc","size":5}`, 422, "The checksum type must be 1 to 16 letters or digits.", "checksum_type"},
		{"no checksum", node1, reportURL(1), `{"successful":true,"checksum_type":"sha256","size":5}`, 422, "", "checksum"},
		{"a checksum that is a number", node1, reportURL(1), `{"successful":true,"checksum":5,"checksum_type":"sha256","size":5}`, 422, "The checksum must be a string.", "checksum"},
		{"a size below zero", node1, reportURL(1), `{"successful":true,"checksum":"abc","checksum_type":"sha256","size":-1}`, 422, "", "size"},
		{"a size that is not a number", node1, reportURL(1), `{"successful":true,"checksum":"abc","checksum_type":"sha256","size":"big"}`, 422, "The size must be a number.", "size"},
		{"not JSON", node1, reportURL(1), `done`, 400, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.post(tt.node, tt.url, tt.body)
			if rp.Status != tt.status || (tt.detail != "" && rp.Detail() != tt.detail) {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if tt.field != "" && rp.At("errors", 0, "meta", "source_field") != tt.field {
				t.Errorf("field = %v", rp.At("errors", 0, "meta"))
			}
			if len(r.store.backupDone) != 0 || len(r.store.events) != 0 {
				t.Error("a backup was completed or an entry written")
			}
		})
	}

	t.Run("a store that finds the backups of other nodes is not trusted", func(t *testing.T) {
		r := newRig(t)
		r.store.leak = true
		if rp := r.post(node2, reportURL(1), goodReport); rp.Status != 403 || len(r.store.backupDone) != 0 {
			t.Errorf("status = %d", rp.Status)
		}
	})
	t.Run("two reports at once: the second loses", func(t *testing.T) {
		r := newRig(t)
		r.store.completeBackupRace = true
		rp := r.post(node1, reportURL(1), goodReport)
		if rp.Status != 400 || len(r.store.events) != 0 {
			t.Errorf("answer = %d %s, events %v", rp.Status, rp.Raw, r.store.events)
		}
	})
	t.Run("a broken store", func(t *testing.T) {
		r := newRig(t)
		r.store.failBackups = errBoom
		rp := r.post(node1, reportURL(1), goodReport)
		if rp.Status != 500 || rp.Contains("hunter2") {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
}

func TestBackupRestoreReport(t *testing.T) {
	for _, tt := range []struct {
		body  string
		event string
	}{
		{`{"successful":true}`, "server:backup.restore-complete"},
		{`{"successful":false}`, "server:backup.restore-failed"},
		{`{}`, "server:backup.restore-failed"},
	} {
		r := newRig(t)
		rp := r.post(node1, restoreURL(4), tt.body)
		if rp.Status != 204 {
			t.Fatalf("%s: status = %d: %s", tt.body, rp.Status, rp.Raw)
		}
		if !reflect.DeepEqual(r.store.restoreEnds, []uint64{103}) {
			t.Errorf("%s: restores ended %v", tt.body, r.store.restoreEnds)
		}
		want := Event{Name: tt.event, ServerSubject: 103, BackupSubject: 4, IP: "203.0.113.9", Properties: `{"name":"Nightly"}`}
		if len(r.store.events) != 1 || r.store.events[0] != want {
			t.Errorf("%s: events = %+v, want %+v", tt.body, r.store.events, want)
		}
	}
}

func TestBackupRestoreReportRules(t *testing.T) {
	t.Run("a server that was not restoring is left alone", func(t *testing.T) {
		r := newRig(t)
		rp := r.post(node1, restoreURL(1), `{"successful":true}`)
		if rp.Status != 204 || len(r.store.events) != 0 || !strings.Contains(r.logs.String(), "was not restoring") {
			t.Errorf("answer = %d, events %v, logs %s", rp.Status, r.store.events, r.logs.String())
		}
	})
	for name, call := range map[string]func(*rig) int{
		"another node":       func(r *rig) int { return r.post(node2, restoreURL(4), `{}`).Status },
		"no such backup":     func(r *rig) int { return r.post(node1, restoreURL(9), `{}`).Status },
		"a backup of node 2": func(r *rig) int { return r.post(node1, restoreURL(3), `{}`).Status },
	} {
		r := newRig(t)
		if got := call(r); got != 403 || len(r.store.restoreEnds) != 0 {
			t.Errorf("%s: status = %d", name, got)
		}
	}
	r := newRig(t)
	r.store.leak = true
	if rp := r.post(node2, restoreURL(4), `{}`); rp.Status != 403 || len(r.store.restoreEnds) != 0 {
		t.Errorf("a leaking store: status = %d", rp.Status)
	}
	r = newRig(t)
	if rp := r.post(node1, restoreURL(4), `{"successful":"maybe"}`); rp.Status != 422 {
		t.Errorf("a verdict that is not a flag: status = %d", rp.Status)
	}
	r = newRig(t)
	r.store.failBackups = errBoom
	if rp := r.post(node1, restoreURL(4), `{}`); rp.Status != 500 || rp.Contains("hunter2") {
		t.Errorf("a broken store: %d %s", rp.Status, rp.Raw)
	}
}

func TestBackupUploadIsForS3Only(t *testing.T) {
	r := newRig(t)
	rp := r.get(node1, reportURL(1)+"?size=1048576")
	if rp.Status != 400 || rp.Detail() != "The configured backup adapter is not an S3 compatible adapter." {
		t.Errorf("a running backup: %d %s", rp.Status, rp.Raw)
	}
	if rp := r.get(node1, reportURL(2)+"?size=5"); rp.Status != 409 || rp.Detail() != "This backup is already in a completed state." {
		t.Errorf("a finished backup: %d %s", rp.Status, rp.Raw)
	}
	for _, q := range []string{"", "?size=0", "?size=-5", "?size=abc"} {
		if rp := r.get(node1, reportURL(1)+q); rp.Status != 400 || rp.Detail() != `A non-empty "size" query parameter must be provided.` {
			t.Errorf("%q: %d %s", q, rp.Status, rp.Raw)
		}
	}
	if rp := r.get(node2, reportURL(1)+"?size=5"); rp.Status != 403 {
		t.Errorf("another node: status = %d", rp.Status)
	}
	if rp := r.get(node1, "/api/remote/backups/x?size=5"); rp.Status != 404 {
		t.Errorf("not a UUID: status = %d", rp.Status)
	}
}

func TestBackupRoutesRefuseWithoutANode(t *testing.T) {
	r := newRig(t)
	client := apitest.NewWorld().Serve(t, func(g api.Group) {
		b := g.Sub("/bare")
		b.GET("/:backup", r.handlers.BackupUpload, api.Public())
		b.POST("/:backup", r.handlers.BackupReport, api.Public())
		b.POST("/:backup/restore", r.handlers.BackupRestoreReport, api.Public())
	})
	for _, rt := range []struct{ method, path string }{
		{"GET", "/bare/" + backupUUIDN(1) + "?size=5"}, {"POST", "/bare/" + backupUUIDN(1)}, {"POST", "/bare/" + backupUUIDN(1) + "/restore"},
	} {
		if rp := client.As(0, rt.method, rt.path, goodReport); rp.Status != 403 {
			t.Errorf("%s %s: status = %d, want 403", rt.method, rt.path, rp.Status)
		}
	}
	if len(r.store.backupDone)+len(r.store.restoreEnds) != 0 {
		t.Error("something was changed")
	}
	for _, rt := range []struct{ method, path string }{{"GET", reportURL(1)}, {"POST", reportURL(1)}, {"POST", restoreURL(1)}} {
		if rp := r.client.AsNode(0, rt.method, rt.path, ""); rp.Status != 401 {
			t.Errorf("%s %s without a token: status = %d", rt.method, rt.path, rp.Status)
		}
	}
}
