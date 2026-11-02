package appservers

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/servers"
	"github.com/Helles01/raptor-panel/internal/wings"
)

func status(r *rig, id uint64) string { return r.store.servers[id].server.Config.Status }

func TestSuspendAndUnsuspend(t *testing.T) {
	r := newRig(t)
	if rp := r.do(grace, "POST", "/7/suspend", ""); rp.Status != 204 || len(rp.Raw) != 0 || status(r, 7) != "suspended" {
		t.Fatalf("suspend: %d %s, status %q", rp.Status, rp.Raw, status(r, 7))
	}
	if !reflect.DeepEqual(r.store.statuses, []statusCall{{7, "", "suspended"}}) || r.daemon.count("sync") != 1 || r.daemon.calls[0].UUID != uuid1 {
		t.Errorf("statuses %+v, node calls %+v", r.store.statuses, r.daemon.calls)
	}
	if rp := r.do(grace, "POST", "/7/unsuspend", ""); rp.Status != 204 || status(r, 7) != "" {
		t.Errorf("unsuspend: %d %s, status %q", rp.Status, rp.Raw, status(r, 7))
	}
	if r.daemon.count("sync") != 2 {
		t.Errorf("node calls %+v", r.daemon.calls)
	}
}

func TestASuspensionThatChangesNothingIsLeftAlone(t *testing.T) {
	r := newRig(t)
	// Server 8 is suspended already, and server 7 is not.
	for _, path := range []string{"/8/suspend", "/7/unsuspend"} {
		if rp := r.do(grace, "POST", path, ""); rp.Status != 204 {
			t.Errorf("%s: %d %s", path, rp.Status, rp.Raw)
		}
	}
	if len(r.store.statuses) != 0 || len(r.daemon.calls) != 0 {
		t.Errorf("statuses %+v, node calls %+v", r.store.statuses, r.daemon.calls)
	}
}

func TestSuspensionRefusals(t *testing.T) {
	r := newRig(t)
	r.store.transfer = true
	rp := r.do(grace, "POST", "/7/suspend", "")
	if rp.Status != 409 || rp.Detail() != servers.ErrSuspendTransfer.Error() {
		t.Errorf("transfer: %d %s", rp.Status, rp.Raw)
	}
	// A server that is installing would lose that state (C-050).
	r = newRig(t)
	r.store.servers[7].server.Config.Status = servers.StatusInstalling
	rp = r.do(grace, "POST", "/7/suspend", "")
	if rp.Status != 409 || rp.Detail() != servers.ErrSuspendBusy.Error() || status(r, 7) != servers.StatusInstalling {
		t.Errorf("busy: %d %s", rp.Status, rp.Raw)
	}
	// Someone else changed the status between the read and the change.
	r = newRig(t)
	r.store.statusBusy = true
	if rp := r.do(grace, "POST", "/7/suspend", ""); rp.Status != 409 || len(r.daemon.calls) != 0 {
		t.Errorf("changed meanwhile: %d %s", rp.Status, rp.Raw)
	}
	for _, path := range []string{"/99/suspend", "/x/suspend", "/99/unsuspend"} {
		if rp := r.do(grace, "POST", path, ""); rp.Status != 404 {
			t.Errorf("%s: %d", path, rp.Status)
		}
	}
	if len(r.store.statuses) != 1 {
		t.Errorf("statuses = %+v", r.store.statuses)
	}
}

func TestSuspensionWhenTheNodeCannotBeTold(t *testing.T) {
	r := newRig(t)
	r.daemon.errs["sync"] = &wings.Error{Status: 504, Message: "no reply from the node", Unreachable: true}
	rp := r.do(grace, "POST", "/7/suspend", "")
	if rp.Status != 504 || rp.Code() != "DaemonConnectionException" || rp.Detail() != "no reply from the node" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	// The status is put back, so the server is not suspended on the panel while it runs on the node.
	if status(r, 7) != "" || !reflect.DeepEqual(r.store.statuses, []statusCall{{7, "", "suspended"}, {7, "suspended", ""}}) {
		t.Errorf("status %q, statuses %+v", status(r, 7), r.store.statuses)
	}
	// Putting it back can fail too; the person is told about the first failure and the log about the second.
	r = newRig(t)
	r.daemon.errs["sync"] = &wings.Error{Status: 504, Message: "no reply", Unreachable: true}
	r.store.servers[7].server.Config.Status = ""
	rp = r.do(grace, "POST", "/8/unsuspend", "")
	if rp.Status != 504 || status(r, 8) != "suspended" {
		t.Errorf("unsuspend: %d %s, status %q", rp.Status, rp.Raw, status(r, 8))
	}
}

func TestReinstall(t *testing.T) {
	r := newRig(t)
	if rp := r.do(grace, "POST", "/7/reinstall", ""); rp.Status != 204 || len(rp.Raw) != 0 || status(r, 7) != servers.StatusInstalling {
		t.Fatalf("answer = %d %s, status %q", rp.Status, rp.Raw, status(r, 7))
	}
	if !reflect.DeepEqual(r.store.statuses, []statusCall{{7, "", servers.StatusInstalling}}) || !reflect.DeepEqual(r.daemon.calls, []daemonCall{{"reinstall", uuid1, false}}) {
		t.Errorf("statuses %+v, node calls %+v", r.store.statuses, r.daemon.calls)
	}
}

func TestReinstallRefusals(t *testing.T) {
	// A server that skips its scripts gains nothing from it, unless it is in a failed state.
	r := newRig(t)
	r.store.servers[7].server.Config.SkipScripts = true
	rp := r.do(grace, "POST", "/7/reinstall", "")
	if rp.Status != 400 || rp.Code() != "DisplayException" || rp.Detail() != servers.ErrReinstallSkipped.Error() || len(r.daemon.calls) != 0 {
		t.Errorf("skipping: %d %s", rp.Status, rp.Raw)
	}
	r.store.servers[7].server.Config.Status = servers.StatusInstallFailed
	if rp := r.do(grace, "POST", "/7/reinstall", ""); rp.Status != 204 {
		t.Errorf("failed install: %d %s", rp.Status, rp.Raw)
	}

	r = newRig(t)
	r.store.statusBusy = true
	if rp := r.do(grace, "POST", "/7/reinstall", ""); rp.Status != 409 || len(r.daemon.calls) != 0 {
		t.Errorf("changed meanwhile: %d %s", rp.Status, rp.Raw)
	}
	if rp := r.do(grace, "POST", "/99/reinstall", ""); rp.Status != 404 {
		t.Errorf("unknown: %d", rp.Status)
	}
}

func TestReinstallWhenTheNodeRefuses(t *testing.T) {
	r := newRig(t)
	r.daemon.errs["reinstall"] = &wings.Error{Status: 409, Message: "server is already installing", NodeStatus: 409}
	rp := r.do(grace, "POST", "/7/reinstall", "")
	if rp.Status != 409 || rp.Detail() != "server is already installing" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if status(r, 7) != "" || len(r.store.statuses) != 2 {
		t.Errorf("status %q, statuses %+v", status(r, 7), r.store.statuses)
	}
}

func database(id uint64) appkit.ServerDatabase {
	return appkit.ServerDatabase{ID: id, ServerID: 7, HostID: 11, Name: "s7_world", Username: "u7_abcdefghij", Remote: "%", MaxConnections: 5}
}

func TestDelete(t *testing.T) {
	for _, path := range []string{"/7", "/7/force"} {
		r := newRig(t)
		r.store.dbs[7] = []appkit.ServerDatabase{database(1), database(2)}
		if rp := r.do(grace, "DELETE", path, ""); rp.Status != 204 || len(rp.Raw) != 0 {
			t.Fatalf("%s: %d %s", path, rp.Status, rp.Raw)
		}
		if !reflect.DeepEqual(r.store.deleted, []uint64{7}) || !reflect.DeepEqual(r.daemon.calls, []daemonCall{{"delete", uuid1, false}}) {
			t.Errorf("%s: deleted %v, node calls %+v", path, r.store.deleted, r.daemon.calls)
		}
		// The databases are dropped on their host first, with their users, and their rows go.
		if len(r.hosts.runs) != 2 || r.hosts.runs[0].Host != 11 || !strings.Contains(strings.Join(r.hosts.runs[0].SQL, ";"), "DROP DATABASE") || !reflect.DeepEqual(r.store.deletedDB, []uint64{1, 2}) {
			t.Errorf("%s: host runs %+v, rows %v", path, r.hosts.runs, r.store.deletedDB)
		}
		// The allocations are free again, with their notes gone.
		if a := r.store.allocs[5]; a.ServerID != nil || a.Notes != nil {
			t.Errorf("%s: allocation 5 = %+v", path, a)
		}
		if _, ok := r.store.servers[7]; ok {
			t.Errorf("%s: the server is still there", path)
		}
	}
}

func TestDeleteRefusals(t *testing.T) {
	r := newRig(t)
	for _, path := range []string{"/99", "/x", "/7/forced", "/7/FORCE", "/7/1"} {
		if rp := r.do(grace, "DELETE", path, ""); rp.Status != 404 {
			t.Errorf("%s: status = %d", path, rp.Status)
		}
	}
	if len(r.daemon.calls) != 0 || len(r.store.deleted) != 0 {
		t.Errorf("node calls %+v, deleted %v", r.daemon.calls, r.store.deleted)
	}
}

func TestDeleteWhenTheNodeRefuses(t *testing.T) {
	refuse := &wings.Error{Status: 500, Message: "the node cannot delete it", NodeStatus: 500}
	r := newRig(t)
	r.daemon.errs["delete"] = refuse
	rp := r.do(grace, "DELETE", "/7", "")
	if rp.Status != 500 || rp.Code() != "DaemonConnectionException" || rp.Detail() != "the node cannot delete it" || len(r.store.deleted) != 0 || len(r.hosts.runs) != 0 {
		t.Errorf("refused: %d %s, deleted %v", rp.Status, rp.Raw, r.store.deleted)
	}
	// A forced deletion goes on, and so does one for a server the node does not have.
	r = newRig(t)
	r.daemon.errs["delete"] = refuse
	if rp := r.do(grace, "DELETE", "/7/force", ""); rp.Status != 204 || len(r.store.deleted) != 1 || !strings.Contains(r.logs.String(), "deleted all the same") {
		t.Errorf("forced: %d %s", rp.Status, rp.Raw)
	}
	r = newRig(t)
	r.daemon.errs["delete"] = &wings.Error{Status: 404, Message: "not found", NodeStatus: 404}
	if rp := r.do(grace, "DELETE", "/7", ""); rp.Status != 204 || len(r.store.deleted) != 1 {
		t.Errorf("unknown to the node: %d %s", rp.Status, rp.Raw)
	}
	// A node that cannot be reached has not said it does not know the server.
	r = newRig(t)
	r.daemon.errs["delete"] = &wings.Error{Status: 504, Message: "no reply", Unreachable: true}
	if rp := r.do(grace, "DELETE", "/7", ""); rp.Status != 504 || len(r.store.deleted) != 0 {
		t.Errorf("unreachable: %d %s", rp.Status, rp.Raw)
	}
}

func TestDeleteWhenADatabaseCannotBeDropped(t *testing.T) {
	r := newRig(t)
	r.store.dbs[7] = []appkit.ServerDatabase{database(1), database(2)}
	r.hosts.err = errBoom
	rp := r.do(grace, "DELETE", "/7", "")
	if rp.Status != 500 || rp.Contains("hunter2") || len(r.store.deleted) != 0 || len(r.store.deletedDB) != 0 || len(r.hosts.runs) != 1 {
		t.Errorf("careful: %d %s, deleted %v, rows %v, runs %d", rp.Status, rp.Raw, r.store.deleted, r.store.deletedDB, len(r.hosts.runs))
	}
	// The server is no longer on its node, and a second try finds the databases still there: that is why this stops.
	r = newRig(t)
	r.store.dbs[7] = []appkit.ServerDatabase{database(1), database(2)}
	r.hosts.err = errBoom
	if rp := r.do(grace, "DELETE", "/7/force", ""); rp.Status != 204 || !reflect.DeepEqual(r.store.deletedDB, []uint64{1, 2}) || len(r.store.deleted) != 1 || !strings.Contains(r.logs.String(), "was not dropped on its host") {
		t.Errorf("forced: %d %s, rows %v", rp.Status, rp.Raw, r.store.deletedDB)
	}
	// A database whose names cannot be dropped safely is not sent to the host.
	r = newRig(t)
	bad := database(1)
	bad.Name = "other_world"
	r.store.dbs[7] = []appkit.ServerDatabase{bad}
	if rp := r.do(grace, "DELETE", "/7", ""); rp.Status != 500 || len(r.hosts.runs) != 0 || len(r.store.deleted) != 0 {
		t.Errorf("bad name: %d %s, runs %d", rp.Status, rp.Raw, len(r.hosts.runs))
	}
}

func TestDeleteGoesByWhatTheNodeSaid(t *testing.T) {
	// What counts is the status the node answered, not the one the panel makes up for the person.
	r := newRig(t)
	r.daemon.errs["delete"] = &wings.Error{Status: 502, NodeStatus: 404, Message: "the reply made no sense"}
	if rp := r.do(grace, "DELETE", "/7", ""); rp.Status != 204 || len(r.store.deleted) != 1 {
		t.Errorf("the node said 404: %d %s", rp.Status, rp.Raw)
	}
	r = newRig(t)
	r.daemon.errs["delete"] = &wings.Error{Status: 404, Message: "made up by the panel"}
	if rp := r.do(grace, "DELETE", "/7", ""); rp.Status != 404 || len(r.store.deleted) != 0 {
		t.Errorf("the node said nothing: %d %s", rp.Status, rp.Raw)
	}
}
