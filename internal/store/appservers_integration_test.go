//go:build integration

package store_test

import (
	"database/sql"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/appnodes"
	"github.com/Helles01/raptor-panel/internal/api/appservers"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/nodes"
	"github.com/Helles01/raptor-panel/internal/outbox"
	"github.com/Helles01/raptor-panel/internal/servers"
)

func TestAppNodes(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.AppNodes()

	// Create: the token the node is given is the one the authorizer checks.
	creds := nodes.Credentials{TokenID: "newnodetokenid01", Token: "t0k3n-of-the-new-node"}
	n := validNode(1)
	n.Name, n.FQDN, n.Description = "Node 3", "node3.example.com", "third"
	made, err := st.Create(r.ctx, appnodes.CreateRequest{UUID: "33333333-aaaa-4aaa-8aaa-333333333333", Settings: n, Credentials: creds})
	if err != nil || made.ID == 0 || made.Settings.Name != "Node 3" || made.Settings.Description != "third" || made.TokenID != creds.TokenID || made.AllocatedMemory != 0 || !made.Settings.Public {
		t.Fatalf("created = %+v, %v", made, err)
	}
	if c, err := r.db.AuthzNodes().ByTokenID(r.ctx, creds.TokenID); err != nil || c.Token != creds.Token || c.Node.ID != made.ID {
		t.Errorf("credentials = %+v, %v", c, err)
	}
	var column string
	r.sql.QueryRow("SELECT daemon_token FROM nodes WHERE id = ?", made.ID).Scan(&column)
	if column != "" {
		t.Errorf("the token column holds %q", column)
	}
	n.LocationID = 99
	if _, err := st.Create(r.ctx, appnodes.CreateRequest{UUID: "x", Settings: n, Credentials: nodes.Credentials{TokenID: "othernodetoken01", Token: "t"}}); !errors.Is(err, appnodes.ErrNoLocation) {
		t.Errorf("a location that is not there: %v", err)
	}

	// Reading: what the servers hold, the includes, the filters.
	one, err := st.Find(r.ctx, f.node1, includes("allocations", "location", "servers"))
	if err != nil || one.AllocatedMemory != 2048 || one.AllocatedDisk != 10000 || len(one.Allocations) != 2 || one.Location == nil || one.Location.Short != "eu.ams" || len(one.Servers) != 2 {
		t.Fatalf("node 1 = %+v, %v", one, err)
	}
	list, total, err := st.List(r.ctx, parse(t, listquery.Nodes, "filter[fqdn]", "node1", "sort", "-memory"), 1, 50, nil)
	if err != nil || total != 1 || list[0].ID != f.node1 {
		t.Errorf("filter = %+v %d %v", list, total, err)
	}
	if _, total, _ := st.List(r.ctx, parse(t, listquery.Nodes, "filter[daemon_token_id]", "newnodetokenid01"), 1, 50, nil); total != 1 {
		t.Errorf("token id filter: %d", total)
	}
	if _, total, _ := st.List(r.ctx, parse(t, listquery.Nodes, "filter[daemon_token_id]", "newnodetoken"), 1, 50, nil); total != 0 {
		t.Errorf("a part of a token id matched %d nodes", total)
	}
	if _, err := st.Find(r.ctx, 999, nil); !errors.Is(err, appkit.ErrNotFound) {
		t.Errorf("unknown node: %v", err)
	}

	// Update: new settings, the same token; then new credentials replace the old ones everywhere.
	n = validNode(1)
	n.Name, n.FQDN, n.Memory, n.Public, n.MaintenanceMode, n.DaemonListen = "Renamed", "renamed.example.com", 8192, false, true, 8443
	up, err := st.Update(r.ctx, made.ID, n, nil)
	if err != nil || up.Settings.Name != "Renamed" || up.Settings.FQDN != "renamed.example.com" || up.Settings.Memory != 8192 || up.Settings.Public || !up.Settings.MaintenanceMode || up.Settings.DaemonListen != 8443 || up.TokenID != creds.TokenID {
		t.Fatalf("updated = %+v, %v", up, err)
	}
	if c, err := r.db.AuthzNodes().ByTokenID(r.ctx, creds.TokenID); err != nil || c.Token != creds.Token {
		t.Errorf("the token changed without new credentials: %+v, %v", c, err)
	}
	fresh := nodes.Credentials{TokenID: "freshtokenid0001", Token: "fresh-token"}
	if _, err := st.Update(r.ctx, made.ID, n, &fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.AuthzNodes().ByTokenID(r.ctx, creds.TokenID); !errors.Is(err, authz.ErrNoNode) {
		t.Errorf("the old token id still finds the node: %v", err)
	}
	if c, err := r.db.AuthzNodes().ByTokenID(r.ctx, fresh.TokenID); err != nil || c.Token != "fresh-token" {
		t.Errorf("the new credentials: %+v, %v", c, err)
	}
	var sealed int
	r.sql.QueryRow("SELECT COUNT(*) FROM raptor_secrets WHERE table_name = 'nodes' AND row_id = ?", made.ID).Scan(&sealed)
	if sealed != 1 {
		t.Errorf("%d sealed tokens for one node", sealed)
	}
	n.LocationID = 99
	if _, err := st.Update(r.ctx, made.ID, n, nil); !errors.Is(err, appnodes.ErrNoLocation) {
		t.Errorf("a location that is not there: %v", err)
	}
	if _, err := st.Update(r.ctx, 999, validNode(1), nil); !errors.Is(err, appkit.ErrNotFound) {
		t.Errorf("unknown node: %v", err)
	}

	// Config and connection, with the mounts attached to the node; a node without a sealed token says so.
	r.exec("INSERT INTO mounts (uuid, name, source, target, read_only, user_mountable) VALUES ('m1', 'a', '/srv/a', '/a', 0, 0), ('m2', 'b', '/srv/b', '/b', 0, 0)")
	r.exec("INSERT INTO mount_node (node_id, mount_id) VALUES (?, 1), (?, 2)", made.ID, made.ID)
	wn, c, err := st.Config(r.ctx, made.ID)
	if err != nil || wn.UUID != made.UUID || wn.FQDN != "renamed.example.com" || wn.DaemonListen != 8443 || wn.DaemonSFTP != 2022 || len(wn.AllowedMounts) != 2 || wn.AllowedMounts[0] != "/srv/a" ||
		c.TokenID != fresh.TokenID || c.Token != "fresh-token" {
		t.Errorf("config = %+v %+v, %v", wn, c, err)
	}
	conn, err := st.Connection(r.ctx, made.ID)
	if err != nil || conn.FQDN != "renamed.example.com" || conn.Port != 8443 || conn.Token != "fresh-token" || conn.Scheme != "https" {
		t.Errorf("connection = %+v, %v", conn, err)
	}
	for _, id := range []uint64{999, 0, 1 << 40} {
		if _, _, err := st.Config(r.ctx, id); !errors.Is(err, appkit.ErrNotFound) {
			t.Errorf("config of node %d: %v", id, err)
		}
		if _, err := st.Connection(r.ctx, id); !errors.Is(err, appkit.ErrNotFound) {
			t.Errorf("connection to node %d: %v", id, err)
		}
	}
	r.exec("DELETE FROM raptor_secrets WHERE table_name = 'nodes' AND row_id = ?", made.ID)
	if _, _, err := st.Config(r.ctx, made.ID); err == nil || errors.Is(err, appkit.ErrNotFound) {
		t.Errorf("a node without a token: %v", err)
	}

	// Candidates: every node with what its servers hold.
	cands, err := st.Candidates(r.ctx)
	if err != nil || len(cands) != 3 {
		t.Fatalf("candidates = %+v, %v", cands, err)
	}
	if u := cands[0].Usage; u.ID != f.node1 || u.UsedMemory != 2048 || u.UsedDisk != 10000 || u.Memory != 4096 || u.LocationID != 1 || !u.Public || cands[0].Node.ID != f.node1 {
		t.Errorf("usage = %+v", u)
	}

	// Delete: the check sees the servers; a node with none goes, with its sealed token and its allocations.
	var seen int64
	errBusy := errors.New("has servers")
	if err := st.Delete(r.ctx, f.node1, func(n int64) error { seen = n; return errBusy }); !errors.Is(err, errBusy) || seen != 2 {
		t.Errorf("seen %d, err %v", seen, err)
	}
	r.exec("INSERT INTO allocations (node_id, ip, port) VALUES (?, '10.0.0.9', 1111)", made.ID)
	r.seal("nodes", made.ID, "daemon_token", "again")
	if err := st.Delete(r.ctx, made.ID, func(n int64) error { seen = n; return nil }); err != nil || seen != 0 {
		t.Fatalf("delete: seen %d, err %v", seen, err)
	}
	var left int
	r.sql.QueryRow("SELECT (SELECT COUNT(*) FROM nodes WHERE id = ?) + (SELECT COUNT(*) FROM allocations WHERE node_id = ?) + (SELECT COUNT(*) FROM raptor_secrets WHERE table_name = 'nodes' AND row_id = ?)", made.ID, made.ID, made.ID).Scan(&left)
	if left != 0 {
		t.Errorf("%d rows of the node are left", left)
	}
	if err := st.Delete(r.ctx, made.ID, func(int64) error { return nil }); !errors.Is(err, appkit.ErrNotFound) {
		t.Errorf("twice: %v", err)
	}
}

// freeAllocations makes ports 31000 to 31000+n-1 on a node and returns their ids.
func freeAllocations(f *fx, node uint64, n int) []uint64 {
	f.r.t.Helper()
	var ids []uint64
	for i := 0; i < n; i++ {
		res, err := f.r.sql.Exec("INSERT INTO allocations (node_id, ip, port, notes) VALUES (?, '10.5.0.1', ?, 'note')", node, 31000+i)
		if err != nil {
			f.r.t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		ids = append(ids, uint64(id))
	}
	return ids
}

func newServerRequest(uuid string, owner, egg uint64, primary uint64, extra ...uint64) appservers.CreateRequest {
	short := uuid[28:] // the tests' uuids differ in their last digits
	ext := "ext-" + short
	return appservers.CreateRequest{
		UUID: uuid, ShortID: short, NestID: 1, OOMDisabled: true, SkipScripts: false,
		Server: servers.NewServer{
			ExternalID: &ext, Name: "created", Description: "made by a test", OwnerID: owner, EggID: egg, Image: "ghcr.io/example/yolks:java_21", Startup: "java -jar {{SERVER_JARFILE}}",
			Limits: servers.Limits{Memory: 2048, Swap: 0, Disk: 8000, IO: 500, CPU: 200}, Features: servers.FeatureLimits{},
			Allocation: primary, Additional: extra,
		},
		Values:      []servers.VariableValue{{EnvName: "SERVER_JARFILE", Store: "created.jar"}, {EnvName: "NOT_A_VARIABLE", Store: "x"}},
		Allocations: append([]uint64{primary}, extra...),
		Check:       func(map[uint64]servers.AllocationRow) error { return nil },
	}
}

func TestAppServerCreate(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.AppServers()
	ids := freeAllocations(f, f.node1, 4)
	const uuidN = "aaaa0000-0000-4000-8000-000000000001"

	req := newServerRequest(uuidN, 2, f.egg, ids[0], ids[1])
	var seen map[uint64]servers.AllocationRow
	req.Check = func(rows map[uint64]servers.AllocationRow) error { seen = rows; return nil }
	req.Server.Features.Allocations = ptrI(3)
	req.Server.Limits.Threads = ptrS("0-1")
	made, err := st.Create(r.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[ids[0]].NodeID != f.node1 || seen[ids[0]].ServerID != nil || seen[ids[1]].ID != ids[1] {
		t.Errorf("the check saw %+v", seen)
	}
	c := made.Config
	if made.ID == 0 || made.ShortID != "00000001" || c.UUID != uuidN || c.Name != "created" || c.Status != "installing" || made.OwnerID != 2 || made.NodeID != f.node1 || made.AllocationID != ids[0] || made.EggID != f.egg ||
		c.Memory != 2048 || c.Disk != 8000 || c.CPU != 200 || !c.OOMDisabled || c.Threads == nil || *c.Threads != "0-1" || c.AllocationLimit == nil || *c.AllocationLimit != 3 ||
		made.DatabaseLimit != nil || made.BackupLimit == nil || *made.BackupLimit != 0 || made.ExternalID == nil || *made.ExternalID != "ext-00000001" {
		t.Errorf("server = %+v", made)
	}
	// Both allocations belong to the server, the primary is the one named, and the value that matches a variable was stored (the other was not).
	var held int
	r.sql.QueryRow("SELECT COUNT(*) FROM allocations WHERE server_id = ?", made.ID).Scan(&held)
	var value string
	var rows int
	r.sql.QueryRow("SELECT COUNT(*), MAX(variable_value) FROM server_variables WHERE server_id = ?", made.ID).Scan(&rows, &value)
	if held != 2 || rows != 1 || value != "created.jar" {
		t.Errorf("%d allocations held, %d values (%q)", held, rows, value)
	}

	// Refusals leave nothing behind.
	count := func() (servers, held int) {
		r.sql.QueryRow("SELECT COUNT(*) FROM servers").Scan(&servers)
		r.sql.QueryRow("SELECT COUNT(*) FROM allocations WHERE server_id IS NOT NULL").Scan(&held)
		return
	}
	sBefore, hBefore := count()
	errNo := errors.New("not on one node")
	bad := newServerRequest("aaaa0000-0000-4000-8000-000000000002", 2, f.egg, ids[2])
	bad.Check = func(map[uint64]servers.AllocationRow) error { return errNo }
	if _, err := st.Create(r.ctx, bad); !errors.Is(err, errNo) {
		t.Errorf("the check's error: %v", err)
	}
	owner := newServerRequest("aaaa0000-0000-4000-8000-000000000003", 99, f.egg, ids[2])
	if _, err := st.Create(r.ctx, owner); !errors.Is(err, appservers.ErrNoOwner) {
		t.Errorf("no owner: %v", err)
	}
	egg := newServerRequest("aaaa0000-0000-4000-8000-000000000004", 2, 99, ids[2])
	if _, err := st.Create(r.ctx, egg); !errors.Is(err, appservers.ErrNoEgg) {
		t.Errorf("no egg: %v", err)
	}
	dupExt := newServerRequest("aaaa0000-0000-4000-8000-000000000005", 2, f.egg, ids[2])
	dupExt.Server.ExternalID = made.ExternalID
	var te *appkit.TakenError
	if _, err := st.Create(r.ctx, dupExt); !errors.As(err, &te) || te.Field != "external_id" {
		t.Errorf("a taken external id: %v", err)
	}
	dupUUID := newServerRequest(uuidN, 2, f.egg, ids[2])
	dupUUID.Server.ExternalID = nil
	if _, err := st.Create(r.ctx, dupUUID); !errors.As(err, &te) || te.Field != "uuid" {
		t.Errorf("a taken uuid: %v", err)
	}
	// An allocation that was taken although the check passed (it did not look) ends the creation.
	taken := newServerRequest("aaaa0000-0000-4000-8000-000000000006", 2, f.egg, ids[0])
	taken.Server.ExternalID = nil
	if _, err := st.Create(r.ctx, taken); err == nil {
		t.Error("a server was made on an allocation another server has")
	}
	if s, h := count(); s != sBefore || h != hBefore {
		t.Errorf("a refusal left rows behind: servers %d -> %d, held %d -> %d", sBefore, s, hBefore, h)
	}
}

// Of many requests at once for one free allocation, one server is made; the others are refused by the check, which sees the allocation under its lock.
func TestAppServerCreateUnderLoad(t *testing.T) {
	f := clientFx(t)
	st := f.r.db.AppServers()
	ids := freeAllocations(f, f.node1, 1)
	errTaken := errors.New("the allocation is taken")
	var seq atomic.Int32
	wins := once(t, 10, func() (bool, error) {
		req := newServerRequest("bbbb0000-0000-4000-8000-0000000000"+pad(int(seq.Add(1)))[4:], 2, f.egg, ids[0])
		req.Server.ExternalID = nil
		req.Check = func(rows map[uint64]servers.AllocationRow) error {
			if rows[ids[0]].ServerID != nil {
				return errTaken
			}
			return nil
		}
		_, err := st.Create(f.r.ctx, req)
		if errors.Is(err, errTaken) {
			return false, nil
		}
		return err == nil, err
	})
	var made int
	f.r.sql.QueryRow("SELECT COUNT(*) FROM servers WHERE allocation_id = ?", ids[0]).Scan(&made)
	if wins != 1 || made != 1 {
		t.Errorf("%d requests made a server on one allocation (%d rows)", wins, made)
	}
}

func ptrI(v int64) *int64   { return &v }
func ptrS(v string) *string { return &v }

func TestAppServerDetailsBuildAndStartup(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.AppServers()
	ids := freeAllocations(f, f.node1, 3)

	// Details: a new owner, with the previous owner's access to be taken away on the node, in the same transaction.
	ext := "moved"
	d := servers.Details{ExternalID: &ext, OwnerID: 2, Name: "Renamed", Description: "new"}
	got, err := st.UpdateDetails(r.ctx, 1, d, 1)
	if err != nil || got.OwnerID != 2 || got.Config.Name != "Renamed" || got.Config.Description != "new" || got.ExternalID == nil || *got.ExternalID != "moved" {
		t.Fatalf("details = %+v, %v", got, err)
	}
	var kind, payload string
	if err := r.sql.QueryRow("SELECT kind, payload FROM raptor_outbox").Scan(&kind, &payload); err != nil || kind != outbox.KindDeauthorize || !strings.Contains(payload, uuid1) || !strings.Contains(payload, `"node_id":`) {
		t.Errorf("order = %s %s, %v", kind, payload, err)
	}
	var te *appkit.TakenError
	other := servers.Details{ExternalID: &ext, OwnerID: 2, Name: "x"}
	if _, err := st.UpdateDetails(r.ctx, 2, other, 0); !errors.As(err, &te) || te.Field != "external_id" {
		t.Errorf("a taken external id: %v", err)
	}
	if _, err := st.UpdateDetails(r.ctx, 2, servers.Details{OwnerID: 99, Name: "x"}, 0); !errors.Is(err, appservers.ErrNoOwner) {
		t.Errorf("no owner: %v", err)
	}
	if _, err := st.UpdateDetails(r.ctx, 99, d, 0); !errors.Is(err, appkit.ErrNotFound) {
		t.Errorf("unknown server: %v", err)
	}
	var jobs int
	r.sql.QueryRow("SELECT COUNT(*) FROM raptor_outbox").Scan(&jobs)
	if jobs != 1 {
		t.Errorf("%d orders; a refused change queued some", jobs)
	}

	// Build: the change is decided from what is read under the lock; the plan assigns and releases allocations.
	var curSeen appkit.Server
	var rowsSeen map[uint64]servers.AllocationRow
	build, err := st.UpdateBuild(r.ctx, 2, appservers.BuildRequest{Rows: []uint64{ids[0], ids[1]}, Change: func(cur appkit.Server, rows map[uint64]servers.AllocationRow) (appservers.BuildChange, error) {
		curSeen, rowsSeen = cur, rows
		return appservers.BuildChange{
			Limits: servers.Limits{Memory: 4096, Swap: -1, Disk: 9000, IO: 300, CPU: 0, Threads: ptrS("2-3")}, Features: servers.FeatureLimits{Databases: ptrI(2), Backups: ptrI(5)}, OOMDisabled: true,
			Plan: servers.AllocationPlan{Assign: []uint64{ids[0], ids[1]}, Release: nil, Default: curSeen.AllocationID},
		}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if curSeen.ID != 2 || curSeen.Config.Memory != 1024 || len(rowsSeen) != 2 || rowsSeen[ids[0]].ServerID != nil {
		t.Errorf("Change saw %+v %+v", curSeen.Config.Memory, rowsSeen)
	}
	if c := build.Config; c.Memory != 4096 || c.Swap != -1 || c.Disk != 9000 || c.CPU != 0 || !c.OOMDisabled || c.Threads == nil || *c.Threads != "2-3" || len(c.Allocations) != 3 ||
		build.DatabaseLimit == nil || *build.DatabaseLimit != 2 || build.BackupLimit == nil || *build.BackupLimit != 5 || c.AllocationLimit != nil {
		t.Errorf("build = %+v", build)
	}
	// Releasing clears the notes; an allocation another server has ends the whole change; an error of Change is returned as it is.
	if _, err := st.UpdateBuild(r.ctx, 2, appservers.BuildRequest{Rows: []uint64{ids[0]}, Change: func(cur appkit.Server, rows map[uint64]servers.AllocationRow) (appservers.BuildChange, error) {
		return appservers.BuildChange{Limits: servers.Limits{Memory: 1, Disk: 1, IO: 500}, Plan: servers.AllocationPlan{Release: []uint64{ids[0]}, Default: cur.AllocationID}}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	var notes sql.NullString
	var holder sql.NullInt64
	r.sql.QueryRow("SELECT notes, server_id FROM allocations WHERE id = ?", ids[0]).Scan(&notes, &holder)
	if notes.Valid || holder.Valid {
		t.Errorf("released: notes %v, held by %v", notes, holder)
	}
	// An allocation that another server holds is not given away: a plan that assigns it ends the change, and the limits of the change are not stored.
	r.exec("UPDATE allocations SET server_id = 1 WHERE id = ?", ids[2])
	var heldBy *uint64
	_, err = st.UpdateBuild(r.ctx, 2, appservers.BuildRequest{Rows: []uint64{ids[2]}, Change: func(cur appkit.Server, rows map[uint64]servers.AllocationRow) (appservers.BuildChange, error) {
		heldBy = rows[ids[2]].ServerID
		return appservers.BuildChange{Limits: servers.Limits{Memory: 7, Disk: 7, IO: 500}, Plan: servers.AllocationPlan{Assign: []uint64{ids[2]}, Default: cur.AllocationID}}, nil
	}})
	if !errors.Is(err, appservers.ErrBusy) || heldBy == nil || *heldBy != 1 {
		t.Errorf("an allocation of another server: held by %v, %v", heldBy, err)
	}
	errBad := errors.New("nope")
	if _, err := st.UpdateBuild(r.ctx, 2, appservers.BuildRequest{Change: func(appkit.Server, map[uint64]servers.AllocationRow) (appservers.BuildChange, error) {
		return appservers.BuildChange{}, errBad
	}}); !errors.Is(err, errBad) {
		t.Errorf("the error of Change: %v", err)
	}
	if after, _ := st.Find(r.ctx, 2, nil); after.Config.Memory != 1 {
		t.Errorf("a change that was refused was stored: memory %d", after.Config.Memory)
	}
	if _, err := st.UpdateBuild(r.ctx, 99, appservers.BuildRequest{Change: func(appkit.Server, map[uint64]servers.AllocationRow) (appservers.BuildChange, error) {
		return appservers.BuildChange{}, nil
	}}); !errors.Is(err, appkit.ErrNotFound) {
		t.Errorf("unknown server: %v", err)
	}

	// Startup: the egg and its nest follow, the values of the egg's variables are stored.
	info, err := st.Egg(r.ctx, f.egg)
	if err != nil || info.NestID != f.nest || len(info.Variables) != 2 || info.Variables[0].EnvName != "SERVER_JARFILE" || info.Variables[0].Rules != "required|string|max:20" || !info.Variables[1].UserViewable || info.Variables[1].UserEditable {
		t.Errorf("egg = %+v, %v", info, err)
	}
	if _, err := st.Egg(r.ctx, 99); !errors.Is(err, appservers.ErrNoEgg) {
		t.Errorf("unknown egg: %v", err)
	}
	set := servers.StartupSettings{Startup: "java -jar new.jar", EggID: f.egg, Image: "ghcr.io/example/yolks:java_17", SkipScripts: true}
	started, err := st.UpdateStartup(r.ctx, 2, set, f.nest, []servers.VariableValue{{EnvName: "BUILD_NUMBER", Store: "999"}})
	if err != nil || started.Config.Startup != "java -jar new.jar" || started.Config.Image != "ghcr.io/example/yolks:java_17" || !started.Config.SkipScripts || started.NestID != f.nest {
		t.Fatalf("startup = %+v, %v", started, err)
	}
	vars, _ := st.Find(r.ctx, 2, includes("variables"))
	if len(vars.Variables) != 2 || vars.Variables[1].ServerValue == nil || *vars.Variables[1].ServerValue != "999" {
		t.Errorf("variables = %+v", vars.Variables)
	}
	if _, err := st.UpdateStartup(r.ctx, 2, set, f.nest, []servers.VariableValue{{EnvName: "BUILD_NUMBER", Store: "1000"}}); err != nil {
		t.Fatal(err)
	}
	var rows int
	r.sql.QueryRow("SELECT COUNT(*) FROM server_variables WHERE server_id = 2").Scan(&rows)
	if rows != 1 {
		t.Errorf("%d rows for one variable after storing it twice", rows)
	}
	if _, err := st.UpdateStartup(r.ctx, 99, set, f.nest, nil); !errors.Is(err, appkit.ErrNotFound) {
		t.Errorf("unknown server: %v", err)
	}
}

func TestAppServerStateAndDelete(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.AppServers()

	usage, err := st.Usage(r.ctx)
	if err != nil || len(usage) != 2 || usage[0].ID != f.node1 || usage[0].UsedMemory != 2048 || usage[1].UsedDisk != 10000 || usage[0].MemoryOverallocate != 0 {
		t.Errorf("usage = %+v, %v", usage, err)
	}
	r.exec("INSERT INTO allocations (node_id, ip, port) VALUES (?, '10.6.0.1', 1000)", f.node1)
	cands, err := st.Allocations(r.ctx, []uint64{f.node1})
	if err != nil || len(cands) != 3 || !cands[0].Used || cands[2].Used || cands[2].IP != "10.6.0.1" || cands[2].Port != 1000 || cands[0].NodeID != f.node1 {
		t.Errorf("candidates = %+v, %v", cands, err)
	}
	if none, err := st.Allocations(r.ctx, nil); err != nil || len(none) != 0 {
		t.Errorf("no nodes: %v %v", none, err)
	}

	moving, _ := st.Transferring(r.ctx, 1)
	r.exec("INSERT INTO server_transfers (server_id, old_node, new_node, old_allocation, new_allocation) VALUES (1, ?, ?, 1, 2)", f.node1, f.node2)
	now, _ := st.Transferring(r.ctx, 1)
	other, _ := st.Transferring(r.ctx, 2)
	if moving || !now || other {
		t.Errorf("transferring: %v %v %v", moving, now, other)
	}

	// The status changes from the status the change was made against only; of 10 requests at once, one.
	if n := once(t, 10, func() (bool, error) { return st.SetStatus(r.ctx, 2, "", "suspended") }); n != 1 {
		t.Errorf("%d requests suspended the server", n)
	}
	if done, _ := st.SetStatus(r.ctx, 2, "", "suspended"); done {
		t.Error("suspended twice")
	}
	if done, err := st.SetStatus(r.ctx, 2, "suspended", ""); err != nil || !done {
		t.Errorf("unsuspend: %v %v", done, err)
	}

	// Databases, and the end of a server: allocations released with their notes, the sealed passwords and the rows that depend on it gone.
	r.exec("UPDATE allocations SET notes = 'keep me?' WHERE server_id = 3")
	r.exec("INSERT INTO database_hosts (name, host, port, username, password) VALUES ('h', 'db', 3306, 'u', '')")
	r.exec("INSERT INTO `databases` (server_id, database_host_id, `database`, username, remote, password, max_connections) VALUES (3, 1, 's3_a', 'u3_a', '%', '', 5), (3, 1, 's3_b', 'u3_b', '%', '', 0)")
	r.seal("databases", 1, "password", "p1")
	r.seal("databases", 2, "password", "p2")
	r.exec("INSERT INTO server_variables (server_id, variable_id, variable_value) VALUES (3, 1, 'v')")
	r.exec("INSERT INTO backups (server_id, uuid, name, ignored_files, disk) VALUES (3, 'bbbbbbbb-0000-4000-8000-0000000000aa', 'b', '', 'wings')")
	dbs, err := st.Databases(r.ctx, 3)
	if err != nil || len(dbs) != 2 || dbs[0].Name != "s3_a" || dbs[0].MaxConnections != 5 || dbs[0].HostID != 1 {
		t.Fatalf("databases = %+v, %v", dbs, err)
	}
	if err := st.DeleteDatabase(r.ctx, dbs[1].ID); err != nil {
		t.Fatal(err)
	}
	if left, _ := st.Databases(r.ctx, 3); len(left) != 1 {
		t.Errorf("%d databases after deleting one", len(left))
	}
	// A server with a database left is not removed (nothing of it is touched); with none left it is.
	if err := st.Delete(r.ctx, 3); err == nil || errors.Is(err, appkit.ErrNotFound) {
		t.Errorf("a server with a database: %v", err)
	}
	var stillHeld int
	r.sql.QueryRow("SELECT COUNT(*) FROM allocations WHERE server_id = 3").Scan(&stillHeld)
	if stillHeld != 1 {
		t.Errorf("a refused delete released %d allocations", 1-stillHeld)
	}
	if err := st.DeleteDatabase(r.ctx, dbs[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(r.ctx, 3); err != nil {
		t.Fatal(err)
	}
	var gone, held, notes, secrets, deps int
	r.sql.QueryRow("SELECT COUNT(*) FROM servers WHERE id = 3").Scan(&gone)
	r.sql.QueryRow("SELECT COUNT(*) FROM allocations WHERE server_id = 3").Scan(&held)
	r.sql.QueryRow("SELECT COUNT(*) FROM allocations WHERE id = 3 AND server_id IS NULL AND notes IS NULL").Scan(&notes)
	r.sql.QueryRow("SELECT COUNT(*) FROM raptor_secrets WHERE table_name = 'databases'").Scan(&secrets)
	r.sql.QueryRow("SELECT (SELECT COUNT(*) FROM server_variables WHERE server_id = 3) + (SELECT COUNT(*) FROM backups WHERE server_id = 3) + (SELECT COUNT(*) FROM `databases` WHERE server_id = 3)").Scan(&deps)
	if gone != 0 || held != 0 || notes != 1 || secrets != 0 || deps != 0 {
		t.Errorf("servers %d, held %d, released clean %d, sealed %d, dependents %d", gone, held, notes, secrets, deps)
	}
	if err := st.Delete(r.ctx, 3); !errors.Is(err, appkit.ErrNotFound) {
		t.Errorf("twice: %v", err)
	}
	// The other servers are as they were.
	if s, err := st.Find(r.ctx, 4, includes("allocations", "user", "nest", "egg", "location", "node", "variables", "subusers")); err != nil || s.User == nil || s.Nest == nil || s.Egg == nil || s.Location == nil || s.Node == nil || len(s.Allocations) != 1 || len(s.Variables) != 2 {
		t.Errorf("server 4 = %+v, %v", s, err)
	}
	if _, err := st.FindExternal(r.ctx, "nope", nil); !errors.Is(err, appkit.ErrNotFound) {
		t.Errorf("external: %v", err)
	}
}
