//go:build integration

package store_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/admin"
	"github.com/Helles01/raptor-panel/internal/api/controlapi"
	"github.com/Helles01/raptor-panel/internal/api/remoteapi"
	"github.com/Helles01/raptor-panel/internal/backups"
	"github.com/Helles01/raptor-panel/internal/remote"
	"github.com/Helles01/raptor-panel/internal/servers"
)

// fx is a panel with two users, a location, two nodes (each with its token sealed), a nest and an egg with two variables, made through the
// services and with the foreign key checks on, so that what the node stores read is what the real thing would have made.
type fx struct {
	r            *rig
	svc          *admin.Service
	node1, node2 uint64
	nest, egg    uint64
}

func newFx(t *testing.T) *fx {
	t.Helper()
	r := newRigFK(t, true)
	resetAdminTables(r)
	svc := r.service()
	for _, u := range []admin.UserInput{
		{Email: "ada@example.com", Username: "ada", FirstName: "A", LastName: "B", Admin: true},
		{Email: "bob@example.com", Username: "bob", FirstName: "B", LastName: "B"},
	} {
		if _, err := svc.CreateUser(r.ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	loc, err := svc.CreateLocation(r.ctx, "eu.ams", "")
	if err != nil {
		t.Fatal(err)
	}
	f := &fx{r: r, svc: svc}
	for _, dst := range []*uint64{&f.node1, &f.node2} {
		n := validNode(loc)
		n.FQDN = map[bool]string{true: "node1.example.com", false: "node2.example.com"}[dst == &f.node1]
		made, err := svc.CreateNode(r.ctx, n)
		if err != nil {
			t.Fatal(err)
		}
		*dst = made.ID
	}
	if f.nest, err = svc.CreateNest(r.ctx, "Minecraft", "", "p@example.com"); err != nil {
		t.Fatal(err)
	}
	egg, err := svc.ImportEgg(r.ctx, f.nest, strings.NewReader(testEgg))
	if err != nil {
		t.Fatal(err)
	}
	f.egg = egg.ID
	return f
}

// server makes a server with an allocation of its own (10.0.0.1 and a port that depends on the number); it returns the id of the allocation.
func (f *fx) server(id, node uint64, uuid, owner string) (allocation uint64) {
	f.r.t.Helper()
	res, err := f.r.sql.Exec("INSERT INTO allocations (node_id, ip, port, server_id) VALUES (?, '10.0.0.1', ?, NULL)", node, 25560+id)
	if err != nil {
		f.r.t.Fatal(err)
	}
	a, _ := res.LastInsertId()
	var ownerID uint64
	f.r.sql.QueryRow("SELECT id FROM users WHERE username = ?", owner).Scan(&ownerID)
	f.r.exec("INSERT INTO servers (id, uuid, uuidShort, node_id, name, description, owner_id, memory, swap, disk, io, cpu, allocation_id, nest_id, egg_id, startup, image) VALUES (?, ?, ?, ?, ?, 'a server', ?, 1024, 0, 5000, 500, 100, ?, ?, ?, 'java -jar {{SERVER_JARFILE}}', 'ghcr.io/example/yolks:java_21')",
		id, uuid, uuid[:8], node, "server-"+uuid[:4], ownerID, a, f.nest, f.egg)
	f.r.exec("UPDATE allocations SET server_id = ? WHERE id = ?", id, a)
	return uint64(a)
}

const (
	uuid1 = "11111111-1111-4111-8111-111111111111"
	uuid2 = "22222222-2222-4222-8222-222222222222"
	uuid3 = "33333333-3333-4333-8333-333333333333"
)

func TestControlTargets(t *testing.T) {
	f := newFx(t)
	r := f.r
	f.server(1, f.node1, uuid1, "ada")
	targets := r.db.ControlTargets()

	got, err := targets.For(r.ctx, 1)
	if err != nil || got.UUID != uuid1 || got.Node.FQDN != "node1.example.com" || got.Node.Scheme != "https" || got.Node.Port != 8080 || got.Node.Token == "" || got.Transfer != nil {
		t.Fatalf("target = %+v, %v", got, err)
	}
	if _, err := targets.For(r.ctx, 99); !errors.Is(err, controlapi.ErrNotFound) {
		t.Errorf("unknown server: %v", err)
	}

	// A server that is being moved: the console may have to talk to the other node.
	r.exec("INSERT INTO server_transfers (server_id, old_node, new_node, old_allocation, new_allocation, archived) VALUES (1, ?, ?, 1, 2, 1)", f.node1, f.node2)
	got, err = targets.For(r.ctx, 1)
	if err != nil || got.Transfer == nil || !got.Transfer.Archived || got.Transfer.NewNode.FQDN != "node2.example.com" || got.Transfer.NewNode.Token == "" || got.Transfer.NewNode.Token == got.Node.Token {
		t.Fatalf("moving target = %+v, %v", got, err)
	}
	// A transfer that has ended is no transfer.
	r.exec("UPDATE server_transfers SET successful = 1")
	if got, _ := targets.For(r.ctx, 1); got.Transfer != nil {
		t.Errorf("a finished transfer: %+v", got.Transfer)
	}
	// A node with no token of Raptor's cannot be talked to, and the error says so without being "not found".
	r.exec("DELETE FROM raptor_secrets WHERE table_name = 'nodes' AND row_id = ?", f.node1)
	if _, err := targets.For(r.ctx, 1); err == nil || errors.Is(err, controlapi.ErrNotFound) || !strings.Contains(err.Error(), "token") {
		t.Errorf("a node without a token: %v", err)
	}
}

func TestControlActivity(t *testing.T) {
	f := newFx(t)
	r := f.r
	f.server(1, f.node1, uuid1, "ada")
	key := uint64(7)
	r.exec("INSERT INTO api_keys (id, user_id, key_type, identifier, token) VALUES (7, 1, 1, 'ptlc_activity001', '')")
	if err := r.db.ControlActivity().Log(r.ctx, controlapi.Entry{Event: "server:power.start", ServerID: 1, ActorID: 1, APIKeyID: &key, IP: "203.0.113.9", Properties: `{"action":"start","ip":"203.0.113.9"}`}); err != nil {
		t.Fatal(err)
	}
	var event, ip, actorType string
	var actor, apiKey uint64
	var subjectType string
	var subjectID uint64
	r.sql.QueryRow("SELECT event, ip, actor_type, actor_id, api_key_id FROM activity_logs").Scan(&event, &ip, &actorType, &actor, &apiKey)
	r.sql.QueryRow("SELECT subject_type, subject_id FROM activity_log_subjects").Scan(&subjectType, &subjectID)
	if event != "server:power.start" || ip != "203.0.113.9" || actor != 1 || apiKey != 7 || !strings.HasSuffix(actorType, "Models\\User") || !strings.HasSuffix(subjectType, "Models\\Server") || subjectID != 1 {
		t.Errorf("entry = %s %s %s %d %d / %s %d", event, ip, actorType, actor, apiKey, subjectType, subjectID)
	}
}

func TestRemoteServersListsWhatTheNodeNeeds(t *testing.T) {
	f := newFx(t)
	r := f.r
	a1 := f.server(1, f.node1, uuid1, "ada")
	f.server(2, f.node1, uuid2, "bob")
	f.server(3, f.node2, uuid3, "ada")
	// server 1 has a second allocation, its own value for one variable, a limit, a thread set and a mount
	r.exec("INSERT INTO allocations (node_id, ip, port, server_id) VALUES (?, '10.0.0.1', 25999, 1)", f.node1)
	r.exec("INSERT INTO server_variables (server_id, variable_id, variable_value) VALUES (1, (SELECT id FROM egg_variables WHERE env_variable = 'SERVER_JARFILE'), 'paper.jar')")
	r.exec("UPDATE servers SET threads = '0-3', allocation_limit = 5, oom_disabled = 1, status = 'suspended' WHERE id = 1")
	r.exec("INSERT INTO mounts (uuid, name, source, target, read_only, user_mountable) VALUES ('m1', 'mod', '/srv/mods', '/mods', 1, 0)")
	r.exec("INSERT INTO mount_server (server_id, mount_id) VALUES (1, 1)")

	page, err := r.db.RemoteServers().List(r.ctx, f.node1, 0, 50)
	if err != nil || page.Total != 2 || len(page.Servers) != 2 {
		t.Fatalf("page = %+v, %v", page, err)
	}
	s := page.Servers[0]
	if s.UUID != uuid1 || s.Name != "server-1111" || s.Status != "suspended" || s.Memory != 1024 || s.Disk != 5000 || s.IO != 500 || s.CPU != 100 || !s.OOMDisabled {
		t.Errorf("server = %+v", s)
	}
	if s.Threads == nil || *s.Threads != "0-3" || s.AllocationLimit == nil || *s.AllocationLimit != 5 || s.LocationShort != "eu.ams" {
		t.Errorf("threads %v, limit %v, location %q", s.Threads, s.AllocationLimit, s.LocationShort)
	}
	if s.Allocation.IP != "10.0.0.1" || s.Allocation.Port != 25561 || len(s.Allocations) != 2 || s.Allocations[1].Port != 25999 || a1 == 0 {
		t.Errorf("allocation %+v of %+v", s.Allocation, s.Allocations)
	}
	if len(s.Variables) != 2 || s.Variables[0].EnvName != "SERVER_JARFILE" || s.Variables[0].Value == nil || *s.Variables[0].Value != "paper.jar" || s.Variables[0].Default != "server.jar" || !s.Variables[0].UserViewable {
		t.Errorf("variables = %+v", s.Variables)
	}
	if v := s.Variables[1]; v.EnvName != "BUILD_NUMBER" || v.Value != nil || v.Current() != "123" {
		t.Errorf("the variable with no value of its own = %+v", v)
	}
	if len(s.Mounts) != 1 || s.Mounts[0] != (servers.Mount{Source: "/srv/mods", Target: "/mods", ReadOnly: true}) {
		t.Errorf("mounts = %+v", s.Mounts)
	}
	if s.Egg.UUID == "" || len(s.Egg.FileDenylist) != 1 || s.Egg.FileDenylist[0] != "*.jar" || s.Egg.ConfigStop != "stop" || !strings.Contains(s.Egg.ConfigStartup, "Done") {
		t.Errorf("egg = %+v", s.Egg)
	}
	if other := page.Servers[1]; other.UUID != uuid2 || len(other.Mounts) != 0 || len(other.Allocations) != 1 || other.Variables[0].Value != nil {
		t.Errorf("the second server = %+v", other)
	}

	// Pages: the order is fixed, the total is not the size of the page, and another node's servers are not here.
	first, _ := r.db.RemoteServers().List(r.ctx, f.node1, 0, 1)
	second, _ := r.db.RemoteServers().List(r.ctx, f.node1, 1, 1)
	if first.Total != 2 || len(first.Servers) != 1 || first.Servers[0].UUID != uuid1 || len(second.Servers) != 1 || second.Servers[0].UUID != uuid2 {
		t.Errorf("pages = %+v / %+v", first, second)
	}
	if empty, err := r.db.RemoteServers().List(r.ctx, f.node1, 5, 10); err != nil || empty.Total != 2 || len(empty.Servers) != 0 {
		t.Errorf("past the end = %+v, %v", empty, err)
	}
	if other, _ := r.db.RemoteServers().List(r.ctx, f.node2, 0, 10); len(other.Servers) != 1 || other.Servers[0].UUID != uuid3 {
		t.Errorf("node 2 = %+v", other)
	}
}

func TestAnEggThatTakesItsProcessConfigurationFromAnother(t *testing.T) {
	f := newFx(t)
	r := f.r
	f.server(1, f.node1, uuid1, "ada")
	r.exec("INSERT INTO eggs (uuid, nest_id, author, name, config_files, config_startup, config_stop, config_from) VALUES ('egg-child', ?, 'x', 'Child', '{\"own\":1}', '{\"own\":1}', 'own-stop', ?)", f.nest, f.egg)
	r.exec("UPDATE servers SET egg_id = LAST_INSERT_ID() WHERE id = 1")
	page, err := r.db.RemoteServers().List(r.ctx, f.node1, 0, 10)
	if err != nil || len(page.Servers) != 1 {
		t.Fatal(err)
	}
	e := page.Servers[0].Egg
	if e.ConfigStop != "stop" || !strings.Contains(e.ConfigStartup, "Done") || e.UUID != "egg-child" {
		t.Errorf("the child uses its own configuration: %+v", e)
	}
}

func TestWhatANodeMayReach(t *testing.T) {
	f := newFx(t)
	r := f.r
	f.server(1, f.node1, uuid1, "ada")
	st := r.db.RemoteServers()

	d, err := st.Reachable(r.ctx, f.node1, uuid1)
	if err != nil || d.NodeID != f.node1 || d.Transfer != nil || d.Server.UUID != uuid1 || len(d.Server.Variables) != 2 {
		t.Fatalf("own server = %+v, %v", d, err)
	}
	if _, err := st.Reachable(r.ctx, f.node2, uuid1); !errors.Is(err, remoteapi.ErrNotFound) {
		t.Errorf("another node's server: %v", err)
	}
	if _, err := st.Reachable(r.ctx, f.node1, uuid3); !errors.Is(err, remoteapi.ErrNotFound) {
		t.Errorf("a server that is not there: %v", err)
	}

	// While it moves, the node it moves to may see it, and so may the node it leaves; a third node still may not.
	r.exec("INSERT INTO server_transfers (server_id, old_node, new_node, old_allocation, new_allocation, new_additional_allocations) VALUES (1, ?, ?, 1, 2, '[5,6]')", f.node1, f.node2)
	for _, node := range []uint64{f.node1, f.node2} {
		d, err := st.Reachable(r.ctx, node, uuid1)
		if err != nil || d.Transfer == nil || d.Transfer.NewNode != f.node2 || len(d.Transfer.NewAdditional) != 2 || d.Transfer.NewAdditional[1] != 6 || d.Transfer.OldAdditional != nil {
			t.Errorf("node %d: %+v, %v", node, d, err)
		}
		if info, err := st.Transfer(r.ctx, node, uuid1); err != nil || info.ServerID != 1 || info.Transfer == nil {
			t.Errorf("node %d transfer: %+v, %v", node, info, err)
		}
	}
	if _, err := st.Reachable(r.ctx, 999, uuid1); !errors.Is(err, remoteapi.ErrNotFound) {
		t.Errorf("a third node: %v", err)
	}

	// Own is only the node that holds it, transfer or not.
	if h, err := st.Own(r.ctx, f.node1, uuid1); err != nil || h.ID != 1 || h.OwnerEmail != "ada@example.com" || h.OwnerUsername != "ada" || h.InstalledBefore || h.Name != "server-1111" {
		t.Errorf("own = %+v, %v", h, err)
	}
	if _, err := st.Own(r.ctx, f.node2, uuid1); !errors.Is(err, remoteapi.ErrNotFound) {
		t.Errorf("own, other node: %v", err)
	}
}

func TestSFTPServer(t *testing.T) {
	f := newFx(t)
	r := f.r
	f.server(1, f.node1, uuid1, "ada")
	r.exec("UPDATE nodes SET maintenance_mode = 1 WHERE id = ?", f.node1)
	r.exec("INSERT INTO subusers (user_id, server_id, permissions) VALUES (2, 1, ?)", `["file.read","file.sftp"]`)
	st := r.db.RemoteServers()

	s, err := st.SFTP(r.ctx, f.node1, "11111111", 2)
	if err != nil || s.ID != 1 || s.OwnerID != 1 || !s.State.NodeMaintenance || len(s.Subuser) != 2 || s.Subuser[1] != "file.sftp" {
		t.Fatalf("subuser = %+v, %v", s, err)
	}
	if s, err := st.SFTP(r.ctx, f.node1, "11111111", 1); err != nil || s.Subuser != nil {
		t.Errorf("the owner is no subuser: %+v, %v", s, err)
	}
	if _, err := st.SFTP(r.ctx, f.node2, "11111111", 2); !errors.Is(err, remoteapi.ErrNotFound) {
		t.Errorf("other node: %v", err)
	}
	// The column only takes valid JSON, so a damaged list is JSON of the wrong shape.
	r.exec(`UPDATE subusers SET permissions = '{"a": 1}'`)
	if s, _ := st.SFTP(r.ctx, f.node1, "11111111", 2); s.Subuser == nil || len(s.Subuser) != 0 {
		t.Errorf("a damaged list is a subuser with no right: %#v", s.Subuser)
	}
}

func TestInstallScriptAndResult(t *testing.T) {
	f := newFx(t)
	r := f.r
	f.server(1, f.node1, uuid1, "ada")
	st := r.db.RemoteServers()

	script, err := st.InstallScript(r.ctx, 1)
	if err != nil || script.ContainerImage != "alpine:3.4" || script.Entrypoint != "ash" || !strings.Contains(script.Script, "echo install") {
		t.Fatalf("script = %+v, %v", script, err)
	}
	// An egg that copies its script uses the other's.
	r.exec("INSERT INTO eggs (uuid, nest_id, author, name, script_container, script_entry, script_install, copy_script_from) VALUES ('egg-copy', ?, 'x', 'Copy', 'own:1', 'sh', 'own', ?)", f.nest, f.egg)
	r.exec("UPDATE servers SET egg_id = LAST_INSERT_ID() WHERE id = 1")
	if s, _ := st.InstallScript(r.ctx, 1); s.ContainerImage != "alpine:3.4" || !strings.Contains(s.Script, "echo install") {
		t.Errorf("copied script = %+v", s)
	}
	if _, err := st.InstallScript(r.ctx, 99); !errors.Is(err, remoteapi.ErrNotFound) {
		t.Errorf("unknown server: %v", err)
	}

	// The result of an installation is stored only against the status it was made for: of 20 reports at once, one.
	r.exec("UPDATE servers SET status = 'installing' WHERE id = 1")
	at := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	ok := servers.InstallOutcome{Status: "", SetInstalledAt: true}
	if n := once(t, 20, func() (bool, error) { return st.Installed(r.ctx, 1, "installing", ok, at) }); n != 1 {
		t.Errorf("%d reports were taken", n)
	}
	var status sql.NullString
	var installed sql.NullTime
	r.sql.QueryRow("SELECT status, installed_at FROM servers WHERE id = 1").Scan(&status, &installed)
	if status.Valid || !installed.Valid || !installed.Time.Equal(at) {
		t.Errorf("status %v, installed at %v", status, installed)
	}
	// From the normal state (a reinstall that failed) to a failure that keeps the first time.
	if done, err := st.Installed(r.ctx, 1, "", servers.InstallOutcome{Status: servers.StatusReinstallFailed}, at.Add(time.Hour)); err != nil || !done {
		t.Fatalf("from normal: %v %v", done, err)
	}
	r.sql.QueryRow("SELECT status, installed_at FROM servers WHERE id = 1").Scan(&status, &installed)
	if status.String != "reinstall_failed" || !installed.Time.Equal(at) {
		t.Errorf("after the failure: %v %v", status, installed)
	}
	if done, _ := st.Installed(r.ctx, 1, "installing", ok, at); done {
		t.Error("a report against a status the server no longer has was taken")
	}
	if done, err := st.Installed(r.ctx, 99, "", ok, at); err != nil || done {
		t.Errorf("unknown server: %v %v", done, err)
	}
}

func TestResettingServersAfterARestart(t *testing.T) {
	f := newFx(t)
	r := f.r
	f.server(1, f.node1, uuid1, "ada") // installing, never installed
	f.server(2, f.node1, uuid2, "bob") // installing, installed before
	f.server(3, f.node1, "33333333-3333-4333-8333-333333333333", "ada")
	f.server(4, f.node1, "44444444-4444-4444-8444-444444444444", "ada")
	f.server(5, f.node2, "55555555-5555-4555-8555-555555555555", "ada")
	r.exec("UPDATE servers SET status = 'installing' WHERE id IN (1, 2, 5)")
	r.exec("UPDATE servers SET installed_at = NOW() WHERE id = 2")
	r.exec("UPDATE servers SET status = 'restoring_backup' WHERE id = 3")
	r.exec("INSERT INTO backups (server_id, uuid, name, ignored_files, disk, is_successful, completed_at) VALUES (3, 'b-old', 'old', '', 'wings', 1, '2026-11-01 10:00:00'), (3, 'b-new', 'new', '', 'wings', 1, '2026-11-02 10:00:00'), (3, 'b-gone', 'deleted', '', 'wings', 1, '2026-11-03 10:00:00')")
	r.exec("UPDATE backups SET deleted_at = NOW() WHERE uuid = 'b-gone'")
	st := r.db.RemoteServers()

	list, err := st.Resettable(r.ctx, f.node1)
	if err != nil || len(list) != 3 {
		t.Fatalf("resettable = %+v, %v", list, err)
	}
	if list[0].ID != 1 || list[0].InstalledBefore || list[1].ID != 2 || !list[1].InstalledBefore || list[2].ID != 3 || list[2].Status != "restoring_backup" {
		t.Errorf("list = %+v", list)
	}
	if list[2].Restoring == nil || list[2].Restoring.Name != "new" {
		t.Errorf("the backup that was being restored = %+v (the newest that was not deleted)", list[2].Restoring)
	}

	changes := []remoteapi.ResetChange{
		{ServerID: 1, From: "installing", To: "install_failed"},
		{ServerID: 2, From: "installing", To: "reinstall_failed"},
		{ServerID: 3, From: "restoring_backup", To: "", RestoreFailed: list[2].Restoring},
		{ServerID: 4, From: "installing", To: "install_failed"}, // not installing any more: left alone
		{ServerID: 5, From: "installing", To: "install_failed"}, // another node's: left alone
	}
	if err := st.Reset(r.ctx, f.node1, changes); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int]string{1: "install_failed", 2: "reinstall_failed", 3: "", 4: "", 5: "installing"} {
		var s sql.NullString
		r.sql.QueryRow("SELECT status FROM servers WHERE id = ?", id).Scan(&s)
		if s.String != want {
			t.Errorf("server %d is %q, want %q", id, s.String, want)
		}
	}
	// One entry, about the server and the backup, with the backup's name.
	var n int
	var props string
	r.sql.QueryRow("SELECT COUNT(*), MIN(properties) FROM activity_logs WHERE event = 'server:backup.restore-failed'").Scan(&n, &props)
	var subjects int
	r.sql.QueryRow("SELECT COUNT(*) FROM activity_log_subjects").Scan(&subjects)
	if n != 1 || !strings.Contains(props, `"new"`) || subjects != 2 {
		t.Errorf("%d entries, properties %s, %d subjects", n, props, subjects)
	}
	// Again: nothing is installing any more, so nothing is written twice.
	if err := st.Reset(r.ctx, f.node1, changes); err != nil {
		t.Fatal(err)
	}
	r.sql.QueryRow("SELECT COUNT(*) FROM activity_logs").Scan(&n)
	if n != 1 {
		t.Errorf("%d entries after the second reset", n)
	}
}

func TestCompletingATransfer(t *testing.T) {
	f := newFx(t)
	r := f.r
	oldAlloc := f.server(1, f.node1, uuid1, "ada")
	r.exec("INSERT INTO allocations (node_id, ip, port, server_id) VALUES (?, '10.0.0.2', 30000, 1), (?, '10.0.0.3', 30001, 1)", f.node2, f.node2)
	var newAlloc, spare uint64
	r.sql.QueryRow("SELECT id FROM allocations WHERE port = 30000").Scan(&newAlloc)
	r.sql.QueryRow("SELECT id FROM allocations WHERE port = 30001").Scan(&spare)
	st := r.db.RemoteServers()

	if done, err := st.CompleteTransfer(r.ctx, 1, remote.TransferResult{Successful: true}); err != nil || done {
		t.Errorf("no transfer is running: %v %v", done, err)
	}
	r.exec("INSERT INTO server_transfers (server_id, old_node, new_node, old_allocation, new_allocation) VALUES (1, ?, ?, ?, ?)", f.node1, f.node2, oldAlloc, newAlloc)
	res := remote.TransferResult{Successful: true, Move: true, NewNode: f.node2, NewAllocation: newAlloc, Release: []uint64{oldAlloc}}
	if n := once(t, 10, func() (bool, error) { return st.CompleteTransfer(r.ctx, 1, res) }); n != 1 {
		t.Errorf("%d reports ended the transfer", n)
	}
	var node, alloc uint64
	var successful bool
	r.sql.QueryRow("SELECT node_id, allocation_id FROM servers WHERE id = 1").Scan(&node, &alloc)
	r.sql.QueryRow("SELECT successful FROM server_transfers").Scan(&successful)
	var released sql.NullInt64
	r.sql.QueryRow("SELECT server_id FROM allocations WHERE id = ?", oldAlloc).Scan(&released)
	if node != f.node2 || alloc != newAlloc || !successful || released.Valid {
		t.Errorf("server on node %d with allocation %d, successful %v, old allocation held by %v", node, alloc, successful, released)
	}

	// A transfer that failed: the server stays, the allocations that were reserved for it are let go.
	r.exec("INSERT INTO server_transfers (server_id, old_node, new_node, old_allocation, new_allocation) VALUES (1, ?, ?, ?, ?)", f.node2, f.node1, newAlloc, spare)
	if done, err := st.CompleteTransfer(r.ctx, 1, remote.TransferResult{Successful: false, Release: []uint64{spare}}); err != nil || !done {
		t.Fatalf("failure: %v %v", done, err)
	}
	r.sql.QueryRow("SELECT node_id FROM servers WHERE id = 1").Scan(&node)
	r.sql.QueryRow("SELECT server_id FROM allocations WHERE id = ?", spare).Scan(&released)
	r.sql.QueryRow("SELECT successful FROM server_transfers ORDER BY id DESC LIMIT 1").Scan(&successful)
	if node != f.node2 || released.Valid || successful {
		t.Errorf("after the failure: node %d, spare held by %v, successful %v", node, released, successful)
	}
}

func TestRemoteUsersAndActivity(t *testing.T) {
	f := newFx(t)
	r := f.r
	f.server(1, f.node1, uuid1, "ada")
	r.exec("INSERT INTO user_ssh_keys (user_id, name, fingerprint, public_key, deleted_at) VALUES (1, 'a', 'SHA256:aaa', 'k', NULL), (1, 'b', 'SHA256:bbb', 'k', NULL), (1, 'c', 'SHA256:ccc', 'k', NOW()), (2, 'd', 'SHA256:ddd', 'k', NULL)")
	users := r.db.RemoteUsers()

	u, err := users.ByUsername(r.ctx, "ADA")
	if err != nil || u.ID != 1 || !u.Admin || u.PasswordHash == "" || u.UUID == "" {
		t.Fatalf("user = %+v, %v", u, err)
	}
	if _, err := users.ByUsername(r.ctx, "nobody"); !errors.Is(err, remoteapi.ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
	if fps, err := users.Fingerprints(r.ctx, 1); err != nil || len(fps) != 2 || fps[0] != "SHA256:aaa" || fps[1] != "SHA256:bbb" {
		t.Errorf("fingerprints = %v, %v", fps, err)
	}

	sink := r.db.RemoteActivity()
	ids, err := sink.Servers(r.ctx, f.node1, []string{uuid1, uuid2, "nonsense"})
	if err != nil || len(ids) != 1 || ids[uuid1] != 1 {
		t.Errorf("servers = %v, %v", ids, err)
	}
	if ids, _ := sink.Servers(r.ctx, f.node2, []string{uuid1}); len(ids) != 0 {
		t.Errorf("another node's server: %v", ids)
	}
	if ids, err := sink.Servers(r.ctx, f.node1, nil); err != nil || len(ids) != 0 {
		t.Errorf("no uuids: %v, %v", ids, err)
	}
	var adaUUID string
	r.sql.QueryRow("SELECT uuid FROM users WHERE id = 1").Scan(&adaUUID)
	if got, err := sink.Users(r.ctx, []string{adaUUID, "nope"}); err != nil || len(got) != 1 || got[adaUUID] != 1 {
		t.Errorf("users = %v, %v", got, err)
	}

	at := time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC)
	err = sink.Store(r.ctx, []remoteapi.StoredEntry{
		{ServerID: 1, UserID: 1, Event: "server:file.write", IP: "203.0.113.9", Properties: `{"file":"a.txt"}`, When: at},
		{ServerID: 1, Event: "server:power.start", IP: "", Properties: `{}`, When: at.Add(time.Minute)},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := r.sql.Query("SELECT event, ip, actor_id, `timestamp`, (SELECT COUNT(*) FROM activity_log_subjects s WHERE s.activity_log_id = l.id) FROM activity_logs l ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var event, ip string
		var actor sql.NullInt64
		var ts time.Time
		var subjects int
		rows.Scan(&event, &ip, &actor, &ts, &subjects)
		got = append(got, event+"|"+ip+"|"+map[bool]string{true: "actor", false: "none"}[actor.Valid]+"|"+ts.UTC().Format("15:04")+"|"+map[int]string{1: "1"}[subjects])
	}
	if strings.Join(got, " ") != "server:file.write|203.0.113.9|actor|09:30|1 server:power.start||none|09:31|1" {
		t.Errorf("rows = %q", got)
	}

	// The panel's own entries about a node's report: a user, a server and a backup are its subjects, one that is 0 is none.
	if err := r.db.RemoteEvents().Log(r.ctx, remoteapi.Event{Name: "server:backup.complete", ServerSubject: 1, BackupSubject: 5, Actor: 0, Properties: `{"name":"b"}`}); err != nil {
		t.Fatal(err)
	}
	var subjectKinds string
	r.sql.QueryRow("SELECT GROUP_CONCAT(subject_type ORDER BY subject_type SEPARATOR ',') FROM activity_log_subjects WHERE activity_log_id = (SELECT MAX(id) FROM activity_logs)").Scan(&subjectKinds)
	if !strings.Contains(subjectKinds, "Backup") || !strings.Contains(subjectKinds, "Server") || strings.Contains(subjectKinds, "User") {
		t.Errorf("subjects = %s", subjectKinds)
	}
}

func TestRemoteBackups(t *testing.T) {
	f := newFx(t)
	r := f.r
	f.server(1, f.node1, uuid1, "ada")
	r.exec("INSERT INTO backups (server_id, uuid, name, ignored_files, disk) VALUES (1, 'bbbbbbbb-0000-4000-8000-000000000001', 'nightly', '', 'wings')")
	r.exec("INSERT INTO backups (server_id, uuid, name, ignored_files, disk, deleted_at) VALUES (1, 'bbbbbbbb-0000-4000-8000-000000000002', 'old', '', 'wings', NOW())")
	st := r.db.RemoteBackups()
	const id = "bbbbbbbb-0000-4000-8000-000000000001"

	b, err := st.OnNode(r.ctx, f.node1, id)
	if err != nil || b.Name != "nightly" || b.ServerID != 1 || b.NodeID != f.node1 || b.Completed || b.Successful || b.Locked || b.Disk != "wings" {
		t.Fatalf("backup = %+v, %v", b, err)
	}
	for name, tc := range map[string]struct {
		node uint64
		uuid string
	}{"other node": {f.node2, id}, "deleted": {f.node1, "bbbbbbbb-0000-4000-8000-000000000002"}, "unknown": {f.node1, "nope"}} {
		if _, err := st.OnNode(r.ctx, tc.node, tc.uuid); !errors.Is(err, remoteapi.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}

	at := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	res := backups.Result{Successful: true, Locked: true, Checksum: "sha1:abc", Bytes: 1234}
	if n := once(t, 20, func() (bool, error) { return st.Complete(r.ctx, b.ID, res, at) }); n != 1 {
		t.Errorf("%d reports ended the backup", n)
	}
	// A second report, even a different one, changes nothing.
	if done, err := st.Complete(r.ctx, b.ID, backups.Result{Successful: false}, at.Add(time.Hour)); err != nil || done {
		t.Errorf("a second report: %v %v", done, err)
	}
	var ok, locked bool
	var checksum string
	var bytes uint64
	var completed time.Time
	r.sql.QueryRow("SELECT is_successful, is_locked, checksum, bytes, completed_at FROM backups WHERE id = ?", b.ID).Scan(&ok, &locked, &checksum, &bytes, &completed)
	if !ok || !locked || checksum != "sha1:abc" || bytes != 1234 || !completed.Equal(at) {
		t.Errorf("stored %v %v %q %d %v", ok, locked, checksum, bytes, completed)
	}
	if again, _ := st.OnNode(r.ctx, f.node1, id); !again.Completed || !again.Successful || !again.Locked {
		t.Errorf("after: %+v", again)
	}

	// A failed backup has no checksum.
	r.exec("INSERT INTO backups (server_id, uuid, name, ignored_files, disk) VALUES (1, 'bbbbbbbb-0000-4000-8000-000000000003', 'bad', '', 'wings')")
	var failedID uint64
	r.sql.QueryRow("SELECT id FROM backups WHERE name = 'bad'").Scan(&failedID)
	if done, err := st.Complete(r.ctx, failedID, backups.Result{Successful: false}, at); err != nil || !done {
		t.Fatalf("failed backup: %v %v", done, err)
	}
	var sum sql.NullString
	r.sql.QueryRow("SELECT checksum FROM backups WHERE id = ?", failedID).Scan(&sum)
	if sum.Valid {
		t.Errorf("a failed backup has checksum %q", sum.String)
	}

	// Finishing a restore only when the server was restoring: of 20 reports at once, one.
	r.exec("UPDATE servers SET status = 'restoring_backup' WHERE id = 1")
	if n := once(t, 20, func() (bool, error) { return st.FinishRestore(r.ctx, 1) }); n != 1 {
		t.Errorf("%d reports finished the restore", n)
	}
	r.exec("UPDATE servers SET status = 'suspended' WHERE id = 1")
	if done, _ := st.FinishRestore(r.ctx, 1); done {
		t.Error("a suspended server was taken for one that was restoring")
	}
	var status string
	r.sql.QueryRow("SELECT status FROM servers WHERE id = 1").Scan(&status)
	if status != "suspended" {
		t.Errorf("status = %q", status)
	}
	_ = json.Valid
}
