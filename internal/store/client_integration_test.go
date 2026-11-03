//go:build integration

package store_test

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api/clientapi"
	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/servers"
)

func parse(t *testing.T, spec listquery.Spec, kv ...string) listquery.Query {
	t.Helper()
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Add(kv[i], kv[i+1])
	}
	q, err := listquery.Parse(v, spec)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func uuids(page clientapi.ServerPage) string {
	var out []string
	for _, s := range page.Servers {
		out = append(out, s.UUID[:1])
	}
	return strings.Join(out, "")
}

// clientFx is a panel where ada (administrator) owns servers 1 and 2, bob owns 3 and 4, and ada is a subuser of 3.
func clientFx(t *testing.T) *fx {
	t.Helper()
	f := newFx(t)
	f.server(1, f.node1, uuid1, "ada")
	f.server(2, f.node1, uuid2, "ada")
	f.server(3, f.node2, uuid3, "bob")
	f.server(4, f.node2, "44444444-4444-4444-8444-444444444444", "bob")
	f.r.exec("INSERT INTO subusers (user_id, server_id, permissions) VALUES (1, 3, ?)", `["control.console","file.read"]`)
	return f
}

func TestClientAccount(t *testing.T) {
	f := newFx(t)
	acc, err := f.r.db.ClientAccounts().ByID(f.r.ctx, 1)
	if err != nil || acc.ID != 1 || !acc.RootAdmin || acc.Username != "ada" || acc.Email != "ada@example.com" || acc.FirstName != "A" || acc.LastName != "B" || acc.Language != "en" {
		t.Errorf("account = %+v, %v", acc, err)
	}
	for _, id := range []uint64{99, 0, 1 << 40} {
		if _, err := f.r.db.ClientAccounts().ByID(f.r.ctx, id); !errors.Is(err, clientapi.ErrNotFound) {
			t.Errorf("account %d: %v", id, err)
		}
	}
}

func TestClientServerListScopes(t *testing.T) {
	f := clientFx(t)
	st := f.r.db.ClientServers()
	list := func(viewer uint64, scope servers.ListScope, q listquery.Query, page, per int) clientapi.ServerPage {
		t.Helper()
		p, err := st.List(f.r.ctx, clientapi.ServerList{Viewer: viewer, Scope: scope, Query: q, Page: page, PerPage: per})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	none := parse(t, listquery.ClientServers)

	// ada: her own two and the one she is a subuser of; bob: his two
	if p := list(1, servers.ScopeAccessible, none, 1, 50); p.Total != 3 || uuids(p) != "123" {
		t.Errorf("ada accessible: %d %s", p.Total, uuids(p))
	}
	// bob is the owner of the server ada is a subuser of: the permissions of ada are not his.
	for _, s := range list(2, servers.ScopeAccessible, none, 1, 50).Servers {
		if s.SubuserPermissions != nil {
			t.Errorf("server %s of bob carries the permissions %v", s.UUID[:1], s.SubuserPermissions)
		}
	}
	if p := list(2, servers.ScopeAccessible, none, 1, 50); p.Total != 2 || uuids(p) != "34" {
		t.Errorf("bob accessible: %d %s", p.Total, uuids(p))
	}
	if p := list(1, servers.ScopeOwner, none, 1, 50); uuids(p) != "12" {
		t.Errorf("ada owner: %s", uuids(p))
	}
	// An administrator's own view: what she reaches only because she is one is server 4, not 3 (she is a subuser there)
	if p := list(1, servers.ScopeAdminOthers, none, 1, 50); p.Total != 1 || uuids(p) != "4" {
		t.Errorf("ada admin others: %d %s", p.Total, uuids(p))
	}
	if p := list(1, servers.ScopeAdminAll, none, 1, 50); p.Total != 4 {
		t.Errorf("ada admin all: %d", p.Total)
	}
	if p := list(1, servers.ScopeNone, none, 1, 50); p.Total != 0 || len(p.Servers) != 0 {
		t.Errorf("none: %+v", p)
	}

	// The permissions the viewer holds as a subuser go with the row; for an owner there are none.
	p := list(1, servers.ScopeAccessible, none, 1, 50)
	if p.Servers[0].SubuserPermissions != nil || p.Servers[2].SubuserPermissions == nil || len(p.Servers[2].SubuserPermissions) != 2 || p.Servers[2].SubuserPermissions[0] != "control.console" {
		t.Errorf("subuser permissions: %v %v", p.Servers[0].SubuserPermissions, p.Servers[2].SubuserPermissions)
	}

	// Pages, and the total that is not the size of a page.
	if p := list(1, servers.ScopeAdminAll, none, 2, 3); p.Total != 4 || len(p.Servers) != 1 || uuids(p) != "4" {
		t.Errorf("page 2 of 3: %d %s", p.Total, uuids(p))
	}
	if p := list(1, servers.ScopeAdminAll, none, 9, 3); p.Total != 4 || len(p.Servers) != 0 {
		t.Errorf("past the end: %d %s", p.Total, uuids(p))
	}

	// Filters and the search of the list; values are only ever arguments.
	if p := list(1, servers.ScopeAdminAll, parse(t, listquery.ClientServers, "filter[name]", "server-2222"), 1, 50); uuids(p) != "2" {
		t.Errorf("filter by name: %s", uuids(p))
	}
	if p := list(1, servers.ScopeAdminAll, parse(t, listquery.ClientServers, "filter[*]", "10.0.0.1:25562"), 1, 50); uuids(p) != "2" {
		t.Errorf("search by address and port: %s", uuids(p))
	}
	if p := list(1, servers.ScopeAdminAll, parse(t, listquery.ClientServers, "filter[*]", "33333333"), 1, 50); uuids(p) != "3" {
		t.Errorf("search by uuid: %s", uuids(p))
	}
	for _, evil := range []string{"x' OR '1'='1", "'; DROP TABLE servers; --", "%", "_"} {
		p := list(1, servers.ScopeAdminAll, parse(t, listquery.ClientServers, "filter[name]", evil), 1, 50)
		if p.Total != 0 {
			t.Errorf("%q matched %d servers", evil, p.Total)
		}
	}
	var n int
	f.r.sql.QueryRow("SELECT COUNT(*) FROM servers").Scan(&n)
	if n != 4 {
		t.Errorf("%d servers are left", n)
	}
}

func TestClientServerDetails(t *testing.T) {
	f := clientFx(t)
	r := f.r
	r.exec("INSERT INTO allocations (node_id, ip, ip_alias, port, notes, server_id) VALUES (?, '10.0.0.1', 'mc.example.com', 26000, 'extra', 1)", f.node1)
	r.exec("INSERT INTO server_variables (server_id, variable_id, variable_value) VALUES (1, (SELECT id FROM egg_variables WHERE env_variable = 'SERVER_JARFILE'), 'paper.jar')")
	r.exec("UPDATE servers SET database_limit = NULL, backup_limit = 3, allocation_limit = 7, threads = '0-1', status = 'suspended', swap = -1 WHERE id = 1")
	r.exec("UPDATE nodes SET maintenance_mode = 1 WHERE id = ?", f.node1)
	r.exec("INSERT INTO server_transfers (server_id, old_node, new_node, old_allocation, new_allocation) VALUES (1, ?, ?, 1, 2)", f.node1, f.node2)

	s, err := r.db.ClientServers().One(r.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != 1 || s.UUID != uuid1 || s.ShortID != "11111111" || s.OwnerID != 1 || s.Name != "server-1111" || s.Description != "a server" || s.Status != "suspended" || !s.Transferring {
		t.Errorf("server = %+v", s)
	}
	if s.Memory != 1024 || s.Swap != -1 || s.Disk != 5000 || s.IO != 500 || s.CPU != 100 || s.Threads == nil || *s.Threads != "0-1" || s.OOMDisabled || s.SkipScripts {
		t.Errorf("limits = %+v", s)
	}
	if s.DatabaseLimit != nil || s.BackupLimit == nil || *s.BackupLimit != 3 || s.AllocationLimit == nil || *s.AllocationLimit != 7 {
		t.Errorf("feature limits: %v %v %v", s.DatabaseLimit, s.BackupLimit, s.AllocationLimit)
	}
	if s.Node.Name != "Node 1" || s.Node.FQDN != "node1.example.com" || s.Node.SFTPPort != 2022 || !s.Node.Maintenance {
		t.Errorf("node = %+v", s.Node)
	}
	if s.Egg.UUID == "" || s.Egg.Name != "Paper" || len(s.Egg.Features) != 1 || s.Egg.Features[0] != "eula" {
		t.Errorf("egg = %+v", s.Egg)
	}
	if s.Primary.IP != "10.0.0.1" || s.Primary.Port != 25561 || s.Primary.ID == 0 || s.Primary.IPAlias != nil || s.Primary.Notes != nil || len(s.Allocations) != 2 {
		t.Errorf("primary %+v of %+v", s.Primary, s.Allocations)
	}
	if a := s.Allocations[1]; a.Port != 26000 || a.IPAlias == nil || *a.IPAlias != "mc.example.com" || a.Notes == nil || *a.Notes != "extra" {
		t.Errorf("the second allocation = %+v", a)
	}
	if len(s.Variables) != 2 || s.Variables[0].EnvName != "SERVER_JARFILE" || s.Variables[0].ServerValue == nil || *s.Variables[0].ServerValue != "paper.jar" ||
		s.Variables[0].Default != "server.jar" || !s.Variables[0].UserViewable || !s.Variables[0].UserEditable || s.Variables[0].Rules != "required|string|max:20" ||
		s.Variables[1].UserEditable || s.Variables[1].ServerValue != nil || s.Variables[1].Name != "Build" {
		t.Errorf("variables = %+v", s.Variables)
	}
	if s.SubuserPermissions != nil {
		t.Errorf("a page of one server has no viewer: %v", s.SubuserPermissions)
	}
	if other, _ := r.db.ClientServers().One(r.ctx, 3); other.Transferring || other.Node.Maintenance || other.Status != "" || other.DatabaseLimit == nil || *other.DatabaseLimit != 0 {
		t.Errorf("server 3 (another node) = %+v", other)
	}
	// A transfer that has ended is not one that is going on.
	r.exec("INSERT INTO server_transfers (server_id, old_node, new_node, old_allocation, new_allocation, successful) VALUES (2, ?, ?, 1, 2, 1)", f.node1, f.node2)
	if done, _ := r.db.ClientServers().One(r.ctx, 2); done.Transferring {
		t.Error("a finished transfer makes the server transferring")
	}
	if _, err := r.db.ClientServers().One(r.ctx, 99); !errors.Is(err, clientapi.ErrNotFound) {
		t.Errorf("unknown server: %v", err)
	}
}

func TestClientSubusers(t *testing.T) {
	f := clientFx(t)
	f.r.exec("INSERT INTO subusers (user_id, server_id, permissions) VALUES (2, 1, NULL)")
	got, err := f.r.db.ClientServers().Subusers(f.r.ctx, []uint64{1, 3, 4})
	if err != nil || len(got) != 2 {
		t.Fatalf("subusers = %+v, %v", got, err)
	}
	if s := got[3]; len(s) != 1 || s[0].User.Username != "ada" || s[0].User.Email != "ada@example.com" || s[0].User.UUID == "" || len(s[0].Permissions) != 2 || s[0].User.CreatedAt.IsZero() {
		t.Errorf("server 3: %+v", s)
	}
	if s := got[1]; len(s) != 1 || s[0].User.Username != "bob" || s[0].Permissions == nil || len(s[0].Permissions) != 0 {
		t.Errorf("server 1: %+v", s)
	}
	if empty, err := f.r.db.ClientServers().Subusers(f.r.ctx, nil); err != nil || len(empty) != 0 {
		t.Errorf("no servers: %v %v", empty, err)
	}
}

func TestClientActivity(t *testing.T) {
	f := clientFx(t)
	r := f.r
	base := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	userType, serverType := `Pterodactyl\Models\User`, `Pterodactyl\Models\Server`
	entry := func(event string, actor uint64, actorType string, subjectType string, subject uint64, minutes int, props string) {
		var actorArg any
		if actor != 0 {
			actorArg = actor
		}
		res, err := r.sql.Exec("INSERT INTO activity_logs (event, ip, actor_type, actor_id, properties, `timestamp`) VALUES (?, '203.0.113.9', ?, ?, ?, ?)",
			event, nullIfEmpty(actorType), actorArg, props, base.Add(time.Duration(minutes)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		r.exec("INSERT INTO activity_log_subjects (activity_log_id, subject_type, subject_id) VALUES (?, ?, ?)", id, subjectType, subject)
	}
	// about ada (user 1)
	entry("auth:success", 1, userType, userType, 1, 0, `{"ip":"203.0.113.9"}`)
	entry("user:account.email-changed", 1, userType, userType, 1, 1, `{"old":"a","new":"b"}`)
	entry("auth:fail", 0, "", userType, 1, 2, `{}`)
	entry("auth:success", 2, userType, userType, 2, 3, `{}`) // about bob
	// about server 1: by ada (admin, the owner), by bob (a user), by an administrator who has nothing to do with it (user 3)
	r.exec("INSERT INTO users (id, uuid, username, email, password, root_admin, use_totp) VALUES (3, 'cccccccc-cccc-4ccc-8ccc-cccccccccccc', 'root', 'root@example.com', 'x', 1, 0)")
	entry("server:power.start", 1, userType, serverType, 1, 10, `{"action":"start"}`)
	entry("server:console.command", 2, userType, serverType, 1, 11, `{"command":"say hi"}`)
	entry("server:power.stop", 3, userType, serverType, 1, 12, `{"action":"stop"}`)
	entry("server:backup.start", 0, "", serverType, 1, 13, `{"name":"b"}`)
	st := r.db.ClientActivity()
	none := parse(t, listquery.ClientActivity)

	page, err := st.ForUser(r.ctx, 1, clientapi.ActivityList{Query: none, Page: 1, PerPage: 25})
	if err != nil || page.Total != 3 || len(page.Entries) != 3 || page.Entries[0].Event != "auth:success" {
		t.Fatalf("ada's = %+v, %v", page, err)
	}
	if e := page.Entries[0]; e.IP != "203.0.113.9" || e.Properties["ip"] != "203.0.113.9" || e.ActorID == nil || *e.ActorID != 1 || !e.Timestamp.Equal(base) || e.Actor != nil {
		t.Errorf("entry = %+v", e)
	}
	if page.Entries[2].ActorID != nil {
		t.Errorf("an entry that nobody did has the actor %v", *page.Entries[2].ActorID)
	}

	// Newest first, the events that no list shows, a filter, the people behind the entries, and pages.
	l := clientapi.ActivityList{Query: parse(t, listquery.ClientActivity, "sort", "-timestamp"), Page: 1, PerPage: 25, Exclude: []string{"auth:fail"}, WithActors: true}
	page, _ = st.ForUser(r.ctx, 1, l)
	if page.Total != 2 || page.Entries[0].Event != "user:account.email-changed" || page.Entries[0].Actor == nil || page.Entries[0].Actor.Username != "ada" || page.Entries[0].Actor.Email != "ada@example.com" {
		t.Errorf("newest first = %+v", page)
	}
	page, _ = st.ForUser(r.ctx, 1, clientapi.ActivityList{Query: parse(t, listquery.ClientActivity, "filter[event]", "email"), Page: 1, PerPage: 25})
	if page.Total != 1 || page.Entries[0].Event != "user:account.email-changed" {
		t.Errorf("filter = %+v", page)
	}
	page, _ = st.ForUser(r.ctx, 1, clientapi.ActivityList{Query: parse(t, listquery.ClientActivity, "sort", "timestamp"), Page: 2, PerPage: 2})
	if page.Total != 3 || len(page.Entries) != 1 || page.Entries[0].Event != "auth:fail" {
		t.Errorf("page 2 = %+v", page)
	}
	if page, _ := st.ForUser(r.ctx, 99, clientapi.ActivityList{Query: none, Page: 1, PerPage: 25}); page.Total != 0 || len(page.Entries) != 0 {
		t.Errorf("a user nothing happened to: %+v", page)
	}

	// A server's entries; with the administrators hidden the stranger's entry goes, the owner's (also an administrator) stays.
	all, err := st.ForServer(r.ctx, 1, false, clientapi.ActivityList{Query: none, Page: 1, PerPage: 25})
	if err != nil || all.Total != 4 {
		t.Fatalf("server 1 = %+v, %v", all, err)
	}
	hidden, err := st.ForServer(r.ctx, 1, true, clientapi.ActivityList{Query: none, Page: 1, PerPage: 25, WithActors: true})
	if err != nil || hidden.Total != 3 {
		t.Fatalf("hidden = %+v, %v", hidden, err)
	}
	for _, e := range hidden.Entries {
		if e.Event == "server:power.stop" {
			t.Error("the entry of an administrator who has nothing to do with the server is shown")
		}
	}
	// An administrator who is a subuser of the server is not hidden.
	r.exec("INSERT INTO subusers (user_id, server_id, permissions) VALUES (3, 1, '[]')")
	if again, _ := st.ForServer(r.ctx, 1, true, clientapi.ActivityList{Query: none, Page: 1, PerPage: 25}); again.Total != 4 {
		t.Errorf("a subuser who is an administrator: %d", again.Total)
	}
	if other, _ := st.ForServer(r.ctx, 2, false, clientapi.ActivityList{Query: none, Page: 1, PerPage: 25}); other.Total != 0 {
		t.Errorf("server 2: %d", other.Total)
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
