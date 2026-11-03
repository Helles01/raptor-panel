//go:build integration

package store_test

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/appusers"
	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/auth/passwordreset"
	"github.com/Helles01/raptor-panel/internal/auth/session"
	"github.com/Helles01/raptor-panel/internal/nodes"
	"github.com/Helles01/raptor-panel/internal/users"
)

func includes(names ...string) appkit.Includes {
	i := appkit.Includes{}
	for _, n := range names {
		i[n] = true
	}
	return i
}

func TestAppNestsAndEggs(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.AppNests()

	nests, total, err := st.ListNests(r.ctx, 1, 50, includes("eggs", "servers"))
	if err != nil || total != 1 || len(nests) != 1 {
		t.Fatalf("nests = %+v %d %v", nests, total, err)
	}
	n := nests[0]
	if n.Name != "Minecraft" || n.Author != "p@example.com" || n.UUID == "" || len(n.Eggs) != 1 || len(n.Servers) != 4 || n.Description != nil {
		t.Errorf("nest = %+v", n)
	}
	// A server in the include has its configuration, the way the node would be told.
	if s := n.Servers[0]; s.ID != 1 || s.ShortID != "11111111" || s.Config.Name != "server-1111" || s.Config.Memory != 1024 || s.OwnerID != 1 || s.NodeID != f.node1 || len(s.Config.Variables) != 2 || s.Config.LocationShort != "eu.ams" {
		t.Errorf("server = %+v", s)
	}
	plain, _, _ := st.ListNests(r.ctx, 1, 50, nil)
	if len(plain[0].Eggs) != 0 || len(plain[0].Servers) != 0 {
		t.Errorf("relations that were not asked for: %+v", plain[0])
	}

	// A second nest, to see pages and the scope of an egg.
	r.exec("INSERT INTO nests (uuid, author, name, description) VALUES ('n2', 'x', 'Other', 'd')")
	if p, total, _ := st.ListNests(r.ctx, 2, 1, nil); total != 2 || len(p) != 1 || p[0].Name != "Other" || p[0].Description == nil {
		t.Errorf("page 2 = %+v %d", p, total)
	}
	if _, err := st.FindNest(r.ctx, 99, nil); !errors.Is(err, appkit.ErrNotFound) {
		t.Errorf("unknown nest: %v", err)
	}

	egg, err := st.FindEgg(r.ctx, f.nest, f.egg, includes("nest", "variables", "servers"))
	if err != nil || egg.Name != "Paper" || egg.Nest == nil || egg.Nest.Name != "Minecraft" || len(egg.Variables) != 2 || len(egg.Servers) != 4 {
		t.Fatalf("egg = %+v, %v", egg, err)
	}
	if len(egg.DockerImages) != 2 || egg.DockerImages[0].Name != "Java 21" || egg.DockerImages[1].Name != "Java 17" || len(egg.FileDenylist) != 1 || egg.ConfigStop == nil || *egg.ConfigStop != "stop" {
		t.Errorf("egg fields = %+v", egg)
	}
	if v := egg.Variables[0]; v.EnvVariable != "SERVER_JARFILE" || !v.UserViewable || !v.UserEditable || v.Rules != "required|string|max:20" || v.DefaultValue != "server.jar" {
		t.Errorf("variable = %+v", v)
	}
	if _, err := st.FindEgg(r.ctx, 2, f.egg, nil); !errors.Is(err, appkit.ErrNotFound) {
		t.Errorf("an egg of another nest: %v", err)
	}
	eggs, err := st.ListEggs(r.ctx, f.nest, nil)
	if err != nil || len(eggs) != 1 {
		t.Errorf("eggs = %+v, %v", eggs, err)
	}
	if _, err := st.ListEggs(r.ctx, 99, nil); !errors.Is(err, appkit.ErrNotFound) {
		t.Errorf("eggs of an unknown nest: %v", err)
	}

	// An egg that borrows its configuration and its script shows what it borrows, when asked.
	r.exec("INSERT INTO eggs (uuid, nest_id, author, name, config_files, config_startup, config_stop, config_logs, config_from, script_install, script_entry, script_container, copy_script_from) VALUES ('child', ?, 'x', 'Child', '{}', '{}', 'x', '{}', ?, 'own', 'sh', 'own:1', ?)", f.nest, f.egg, f.egg)
	var childID uint64
	r.sql.QueryRow("SELECT id FROM eggs WHERE uuid = 'child'").Scan(&childID)
	child, err := st.FindEgg(r.ctx, f.nest, childID, includes("config", "script"))
	if err != nil || child.Inherited == nil || child.InheritedScript == nil || child.Inherited.Stop == nil || *child.Inherited.Stop != "stop" ||
		!strings.Contains(string(child.Inherited.Startup), "Done") || child.InheritedScript.Container != "alpine:3.4" || child.InheritedScript.Install == nil {
		t.Fatalf("child = %+v, %v", child, err)
	}
	if bare, _ := st.FindEgg(r.ctx, f.nest, childID, nil); bare.Inherited != nil || bare.InheritedScript != nil {
		t.Error("an inheritance that was not asked for")
	}
	if own, _ := st.FindEgg(r.ctx, f.nest, f.egg, includes("config", "script")); own.Inherited != nil || own.InheritedScript != nil {
		t.Error("an egg that borrows nothing has an inheritance")
	}
}

func TestAppUsers(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.AppUsers()
	ext := "crm-7"
	rec := users.Record{UUID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", ExternalID: &ext, Email: "cy@example.com", Username: "cy", FirstName: "Cy", LastName: "Dee", PasswordHash: "$2a$10$x", Language: "hu", Admin: false}

	made, err := st.Create(r.ctx, appusers.CreateRequest{User: rec, SetupTokenHash: "setup-hash"})
	if err != nil || made.ID == 0 || made.Username != "cy" || made.Email != "cy@example.com" || made.FirstName != "Cy" || made.Language != "hu" || made.Admin || made.TwoFactor ||
		made.ExternalID == nil || *made.ExternalID != "crm-7" || made.CreatedAt.IsZero() {
		t.Fatalf("created = %+v, %v", made, err)
	}
	if got, found, _ := r.db.AuthapiResets().Latest(r.ctx, "cy@example.com"); !found || got.Hash != "setup-hash" {
		t.Errorf("the setup token = %+v %v", got, found)
	}
	// Taken values are named by their field and nothing is stored.
	for field, bad := range map[string]users.Record{
		"username":    {UUID: "e1", Email: "other@example.com", Username: "CY", FirstName: "A", LastName: "B", Language: "en", PasswordHash: "x"},
		"email":       {UUID: "e2", Email: "cy@example.com", Username: "other", FirstName: "A", LastName: "B", Language: "en", PasswordHash: "x"},
		"external_id": {UUID: "e3", Email: "o3@example.com", Username: "o3", ExternalID: &ext, FirstName: "A", LastName: "B", Language: "en", PasswordHash: "x"},
	} {
		_, err := st.Create(r.ctx, appusers.CreateRequest{User: bad, SetupTokenHash: "h"})
		var taken *appkit.TakenError
		if !errors.As(err, &taken) || taken.Field != field {
			t.Errorf("%s: %v", field, err)
		}
	}
	var n int
	r.sql.QueryRow("SELECT COUNT(*) FROM users").Scan(&n)
	if n != 3 {
		t.Errorf("%d users", n)
	}
	// A user with no setup token has no link.
	plain := users.Record{UUID: "dddddddd-dddd-4ddd-8ddd-ddddddddddde", Email: "dee@example.com", Username: "dee", FirstName: "D", LastName: "E", PasswordHash: "x", Language: "en"}
	if _, err := st.Create(r.ctx, appusers.CreateRequest{User: plain}); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := r.db.AuthapiResets().Latest(r.ctx, "dee@example.com"); found {
		t.Error("a link for a user without a setup token")
	}

	// Reading: by id, by external id, with the servers the user owns, in lists with filters and pages.
	if u, err := st.FindExternal(r.ctx, "crm-7", nil); err != nil || u.ID != made.ID {
		t.Errorf("external = %+v, %v", u, err)
	}
	if _, err := st.FindExternal(r.ctx, "nope", nil); !errors.Is(err, appkit.ErrNotFound) {
		t.Errorf("unknown external id: %v", err)
	}
	ada, err := st.Find(r.ctx, 1, includes("servers"))
	if err != nil || len(ada.Servers) != 2 || !ada.Admin || ada.Servers[0].ID != 1 {
		t.Fatalf("ada = %+v, %v", ada, err)
	}
	if bare, _ := st.Find(r.ctx, 1, nil); len(bare.Servers) != 0 {
		t.Error("servers that were not asked for")
	}
	if _, err := st.Find(r.ctx, 999, nil); !errors.Is(err, appkit.ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
	page, total, err := st.List(r.ctx, parse(t, listquery.Users, "filter[email]", "example.com", "sort", "-id"), 1, 2, nil)
	if err != nil || total != 4 || len(page) != 2 || page[0].ID < page[1].ID {
		t.Errorf("list = %+v %d %v", page, total, err)
	}
	if page, total, _ := st.List(r.ctx, parse(t, listquery.Users, "filter[username]", "cy"), 1, 50, nil); total != 1 || page[0].Username != "cy" {
		t.Errorf("filter = %+v %d", page, total)
	}
	if _, total, _ := st.List(r.ctx, parse(t, listquery.Users, "filter[username]", "x' OR '1'='1"), 1, 50, nil); total != 0 {
		t.Errorf("an injected filter matched %d users", total)
	}
}

func TestAppUserUpdateAndItsEffects(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.AppUsers()
	at := newTime()

	// bob (user 2) owns servers on node 2; he has a session, a link for his address, an application key and a client key
	r.exec("UPDATE users SET root_admin = 1 WHERE id = 2")
	r.db.Sessions().Put(r.ctx, newSession("k-bob", 2, at))
	r.db.Sessions().Put(r.ctx, newSession("k-ada", 1, at))
	r.db.AuthapiResets().Replace(r.ctx, "bob@example.com", passwordreset.Record{Hash: "old-link", CreatedAt: at})
	r.exec("INSERT INTO api_keys (id, user_id, key_type, identifier, token) VALUES (50, 2, 2, 'ptla_bobapp00001', ''), (51, 2, 1, 'ptlc_bobacct0001', '')")
	r.seal("api_keys", 50, "token_sha256", "h")
	r.seal("api_keys", 51, "token_sha256", "h")

	ext := "new-ext"
	upd := users.Update{Account: users.Account{ExternalID: &ext, Email: "bob2@example.com", Username: "bobby", FirstName: "Bobby", LastName: "B", Language: "hu", Admin: false}, PasswordHash: "$2a$10$newhash"}
	got, err := st.Update(r.ctx, 2, upd, users.Effects{PasswordChanged: true, EmailChanged: true, AdminRevoked: true})
	if err != nil || got.Username != "bobby" || got.Email != "bob2@example.com" || got.Admin || got.FirstName != "Bobby" || got.Language != "hu" || got.ExternalID == nil || *got.ExternalID != "new-ext" {
		t.Fatalf("updated = %+v, %v", got, err)
	}
	if u, _ := r.db.AuthapiUsers().ByID(r.ctx, 2); u.PasswordHash != "$2a$10$newhash" {
		t.Errorf("hash = %q", u.PasswordHash)
	}
	if _, err := r.db.Sessions().Get(r.ctx, "k-bob"); err == nil {
		t.Error("a session of the old password survives")
	}
	if _, err := r.db.Sessions().Get(r.ctx, "k-ada"); err != nil {
		t.Errorf("another user's session: %v", err)
	}
	if _, found, _ := r.db.AuthapiResets().Latest(r.ctx, "bob@example.com"); found {
		t.Error("a link for the old address survives")
	}
	var appKeys, acctKeys, secrets int
	r.sql.QueryRow("SELECT COUNT(*) FROM api_keys WHERE key_type = 2").Scan(&appKeys)
	r.sql.QueryRow("SELECT COUNT(*) FROM api_keys WHERE key_type = 1 AND user_id = 2").Scan(&acctKeys)
	r.sql.QueryRow("SELECT COUNT(*) FROM raptor_secrets WHERE table_name = 'api_keys'").Scan(&secrets)
	if appKeys != 0 || acctKeys != 1 || secrets != 1 {
		t.Errorf("application keys %d, account keys %d, sealed values %d", appKeys, acctKeys, secrets)
	}
	// The orders for the node he has servers on (node 2) are queued with the change.
	var jobs int
	r.sql.QueryRow("SELECT COUNT(*) FROM raptor_outbox WHERE kind = 'node.deauthorize'").Scan(&jobs)
	if jobs != 1 {
		t.Errorf("%d orders queued", jobs)
	}

	// Without effects nothing but the fields change; a taken value changes nothing at all.
	r.db.Sessions().Put(r.ctx, newSession("k-bob2", 2, at))
	plain := users.Update{Account: users.Account{Email: "bob2@example.com", Username: "bobby", FirstName: "Robert", LastName: "B", Language: "hu"}}
	if got, err := st.Update(r.ctx, 2, plain, users.Effects{}); err != nil || got.FirstName != "Robert" || got.ExternalID != nil {
		t.Fatalf("plain update = %+v, %v", got, err)
	}
	if _, err := r.db.Sessions().Get(r.ctx, "k-bob2"); err != nil {
		t.Errorf("a session was ended without a password change: %v", err)
	}
	if u, _ := r.db.AuthapiUsers().ByID(r.ctx, 2); u.PasswordHash != "$2a$10$newhash" {
		t.Errorf("an empty password hash changed the password: %q", u.PasswordHash)
	}
	taken := users.Update{Account: users.Account{Email: "ada@example.com", Username: "bobby", FirstName: "X", LastName: "B", Language: "hu"}}
	var te *appkit.TakenError
	if _, err := st.Update(r.ctx, 2, taken, users.Effects{PasswordChanged: true}); !errors.As(err, &te) || te.Field != "email" {
		t.Errorf("a taken address: %v", err)
	}
	if u, _ := st.Find(r.ctx, 2, nil); u.FirstName != "Robert" {
		t.Errorf("a refused update changed the user: %+v", u)
	}
	if _, err := r.db.Sessions().Get(r.ctx, "k-bob2"); err != nil {
		t.Error("a refused update ended a session")
	}
	if _, err := st.Update(r.ctx, 99, plain, users.Effects{}); !errors.Is(err, appkit.ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
}

func TestAppUserDelete(t *testing.T) {
	f := clientFx(t)
	st := f.r.db.AppUsers()
	var seen int64 = -1
	err := st.Delete(f.r.ctx, 2, func(owned int64) error { seen = owned; return errors.New("owns servers") })
	if err == nil || seen != 2 {
		t.Errorf("check saw %d servers, err %v", seen, err)
	}
	if u, _ := st.Find(f.r.ctx, 2, nil); u.ID != 2 {
		t.Error("a refused delete removed the user")
	}
	f.r.exec("DELETE FROM servers WHERE owner_id = 2")
	if err := st.Delete(f.r.ctx, 2, func(int64) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Find(f.r.ctx, 2, nil); !errors.Is(err, appkit.ErrNotFound) {
		t.Errorf("after: %v", err)
	}
	if err := st.Delete(f.r.ctx, 2, func(int64) error { return nil }); !errors.Is(err, appkit.ErrNotFound) {
		t.Errorf("twice: %v", err)
	}
}

func TestAppLocations(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.AppLocations()

	l, err := st.Create(r.ctx, nodes.Location{Short: "us.nyc", Long: "New York"})
	if err != nil || l.Short != "us.nyc" || l.Long == nil || *l.Long != "New York" || l.CreatedAt.IsZero() {
		t.Fatalf("created = %+v, %v", l, err)
	}
	if bare, _ := st.Create(r.ctx, nodes.Location{Short: "us.sfo"}); bare.Long != nil {
		t.Errorf("an empty long name is stored as %q", *bare.Long)
	}
	var te *appkit.TakenError
	if _, err := st.Create(r.ctx, nodes.Location{Short: "us.nyc"}); !errors.As(err, &te) || te.Field != "short" {
		t.Errorf("a taken short name: %v", err)
	}
	if _, err := st.Update(r.ctx, l.ID, nodes.Location{Short: "eu.ams"}); !errors.As(err, &te) {
		t.Errorf("a taken short name on update: %v", err)
	}
	up, err := st.Update(r.ctx, l.ID, nodes.Location{Short: "us.ny", Long: ""})
	if err != nil || up.Short != "us.ny" || up.Long != nil || !up.UpdatedAt.After(l.CreatedAt.Add(-1)) {
		t.Errorf("updated = %+v, %v", up, err)
	}
	if _, err := st.Update(r.ctx, 999, nodes.Location{Short: "x"}); !errors.Is(err, appkit.ErrNotFound) {
		t.Errorf("unknown location: %v", err)
	}

	eu, err := st.Find(r.ctx, 1, includes("nodes", "servers"))
	if err != nil || len(eu.Nodes) != 2 || len(eu.Servers) != 4 || eu.Nodes[0].Settings.FQDN != "node1.example.com" || eu.Nodes[0].UUID == "" {
		t.Fatalf("eu = %+v, %v", eu, err)
	}
	if node := eu.Nodes[0]; node.TokenID == "" || node.Settings.DaemonListen != 8080 || node.Settings.Memory != 4096 || node.AllocatedMemory != 2048 || node.AllocatedDisk != 10000 {
		t.Errorf("node = %+v", node)
	}
	list, total, err := st.List(r.ctx, parse(t, listquery.Locations, "filter[short]", "eu"), 1, 50, nil)
	if err != nil || total != 1 || list[0].Short != "eu.ams" {
		t.Errorf("filter = %+v %d %v", list, total, err)
	}

	// Not while it has nodes; the check sees how many.
	var seen int64
	errHasNodes := errors.New("has nodes")
	if err := st.Delete(r.ctx, 1, func(n int64) error { seen = n; return errHasNodes }); !errors.Is(err, errHasNodes) || seen != 2 {
		t.Errorf("seen %d, err %v", seen, err)
	}
	if err := st.Delete(r.ctx, l.ID, func(int64) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(r.ctx, l.ID, func(int64) error { return nil }); !errors.Is(err, appkit.ErrNotFound) {
		t.Errorf("twice: %v", err)
	}
}

func TestAppAllocations(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.AppAllocations()

	if err := st.Exists(r.ctx, f.node1); err != nil {
		t.Errorf("exists: %v", err)
	}
	for _, id := range []uint64{999, 0, 1 << 40} {
		if err := st.Exists(r.ctx, id); !errors.Is(err, appkit.ErrNotFound) {
			t.Errorf("node %d: %v", id, err)
		}
	}

	// More rows than one statement takes, and a port the node has already.
	var list []nodes.Allocation
	for p := 30000; p < 31200; p++ {
		list = append(list, nodes.Allocation{IP: "10.1.0.1", Port: p})
	}
	list = append(list, nodes.Allocation{IP: "10.1.0.1", Port: 30000}, nodes.Allocation{IP: "10.1.0.2", Port: 30000, Alias: "mc.example.com"})
	if err := st.Add(r.ctx, f.node1, list); err != nil {
		t.Fatal(err)
	}
	if err := st.Add(r.ctx, f.node1, list[:10]); err != nil {
		t.Fatalf("the same again: %v", err)
	}
	var n int
	r.sql.QueryRow("SELECT COUNT(*) FROM allocations WHERE node_id = ? AND ip IN ('10.1.0.1', '10.1.0.2')", f.node1).Scan(&n)
	if n != 1201 {
		t.Errorf("%d allocations, want 1201", n)
	}
	// The same address and port on another node is another allocation.
	if err := st.Add(r.ctx, f.node2, list[:3]); err != nil {
		t.Fatal(err)
	}

	page, total, err := st.List(r.ctx, f.node1, parse(t, listquery.Allocations, "filter[ip]", "10.1.0.2"), 1, 50, includes("node"))
	if err != nil || total != 1 || page[0].Port != 30000 || page[0].Alias == nil || *page[0].Alias != "mc.example.com" || page[0].Node == nil || page[0].Node.UUID == "" || page[0].ServerID != nil {
		t.Fatalf("filtered = %+v %d %v", page, total, err)
	}
	// Free ones: the owner filter with no value; the ones a server has come with the server when asked.
	free, total, _ := st.List(r.ctx, f.node1, parse(t, listquery.Allocations, "filter[server_id]", ""), 1, 5, nil)
	if total != 1201 || len(free) != 5 {
		t.Errorf("free = %d of %d", len(free), total)
	}
	held, total, _ := st.List(r.ctx, f.node1, parse(t, listquery.Allocations, "filter[server_id]", "1"), 1, 50, includes("server"))
	if total != 1 || held[0].Server == nil || held[0].Server.ID != 1 || held[0].ServerID == nil || *held[0].ServerID != 1 {
		t.Errorf("held = %+v %d", held, total)
	}
	if page, total, _ := st.List(r.ctx, f.node1, parse(t, listquery.Allocations, "filter[ip]", "x' OR '1'='1"), 1, 50, nil); total != 0 || len(page) != 0 {
		t.Errorf("an injected filter matched %d", total)
	}
	// Only this node's allocations are listed.
	if _, total, _ := st.List(r.ctx, f.node2, parse(t, listquery.Allocations), 1, 50, nil); total != 2+3 {
		t.Errorf("node 2 has %d", total)
	}

	// Deleting: the check sees who holds it; an allocation of another node is not found; a free one goes.
	var heldID, freeID uint64
	r.sql.QueryRow("SELECT id FROM allocations WHERE server_id = 1").Scan(&heldID)
	r.sql.QueryRow("SELECT id FROM allocations WHERE ip = '10.1.0.2'").Scan(&freeID)
	var holder *uint64
	// An extra allocation of a server is not protected by a foreign key, only by the check.
	r.exec("INSERT INTO allocations (node_id, ip, port, server_id) VALUES (?, '10.9.9.9', 40000, 1)", f.node1)
	var extraID uint64
	r.sql.QueryRow("SELECT id FROM allocations WHERE port = 40000").Scan(&extraID)
	if err := st.Delete(r.ctx, f.node1, extraID, func(s *uint64) error { return errors.New("in use") }); err == nil {
		t.Error("an extra allocation of a server was deleted although the check refused")
	}
	var stillThere int
	r.sql.QueryRow("SELECT COUNT(*) FROM allocations WHERE id = ?", extraID).Scan(&stillThere)
	if stillThere != 1 {
		t.Error("the extra allocation is gone")
	}
	errInUse := errors.New("in use")
	if err := st.Delete(r.ctx, f.node1, heldID, func(s *uint64) error { holder = s; return errInUse }); !errors.Is(err, errInUse) || holder == nil || *holder != 1 {
		t.Errorf("held: holder %v, err %v", holder, err)
	}
	if err := st.Delete(r.ctx, f.node2, freeID, func(*uint64) error { return nil }); !errors.Is(err, appkit.ErrNotFound) {
		t.Errorf("another node's allocation: %v", err)
	}
	holder = ptrU(99)
	if err := st.Delete(r.ctx, f.node1, freeID, func(s *uint64) error { holder = s; return nil }); err != nil || holder != nil {
		t.Errorf("free: holder %v, err %v", holder, err)
	}
	var left int
	r.sql.QueryRow("SELECT COUNT(*) FROM allocations WHERE id = ?", freeID).Scan(&left)
	if left != 0 {
		t.Error("the allocation was not removed")
	}
	_ = sql.ErrNoRows
}

func newTime() time.Time { return time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC) }

func newSession(key string, user uint64, at time.Time) session.Record {
	return session.Record{Key: key, UserID: user, Payload: []byte("{}"), LastActivity: at}
}

// users.external_id has no unique key in the schema, so the store keeps the rule: of many requests at once with the same value, one gets it.
func TestAppUserExternalIDIsUnique(t *testing.T) {
	f := newFx(t)
	st := f.r.db.AppUsers()
	var seq int32
	wins := once(t, 12, func() (bool, error) {
		n := addInt32(&seq)
		ext := "crm-same"
		rec := users.Record{UUID: "f0000000-0000-4000-8000-0000000000" + pad(int(n))[4:], ExternalID: &ext, Email: "u" + pad(int(n)) + "@example.com", Username: "u" + pad(int(n)), FirstName: "A", LastName: "B", Language: "en", PasswordHash: "x"}
		_, err := st.Create(f.r.ctx, appusers.CreateRequest{User: rec})
		var taken *appkit.TakenError
		if errors.As(err, &taken) {
			return false, nil
		}
		return err == nil, err
	})
	var n int
	f.r.sql.QueryRow("SELECT COUNT(*) FROM users WHERE external_id = 'crm-same'").Scan(&n)
	if wins != 1 || n != 1 {
		t.Errorf("%d requests got the external id (%d rows)", wins, n)
	}
	// An update to a value another user has is refused; to the user's own is fine.
	other := "crm-other"
	if _, err := st.Update(f.r.ctx, 1, users.Update{Account: users.Account{ExternalID: &other, Email: "ada@example.com", Username: "ada", FirstName: "A", LastName: "B", Language: "en", Admin: true}}, users.Effects{}); err != nil {
		t.Fatal(err)
	}
	same := "crm-same"
	var te *appkit.TakenError
	if _, err := st.Update(f.r.ctx, 1, users.Update{Account: users.Account{ExternalID: &same, Email: "ada@example.com", Username: "ada", FirstName: "A", LastName: "B", Language: "en", Admin: true}}, users.Effects{}); !errors.As(err, &te) || te.Field != "external_id" {
		t.Errorf("an external id another user has: %v", err)
	}
	if _, err := st.Update(f.r.ctx, 1, users.Update{Account: users.Account{ExternalID: &other, Email: "ada@example.com", Username: "ada", FirstName: "A2", LastName: "B", Language: "en", Admin: true}}, users.Effects{}); err != nil {
		t.Errorf("an update that keeps the user's own external id: %v", err)
	}
}

// A change of the address alone, with no new password, still stops the links that were sent to the old address.
func TestAppUserEmailChangeAloneRemovesTheOldLinks(t *testing.T) {
	f := clientFx(t)
	r := f.r
	r.db.AuthapiResets().Replace(r.ctx, "bob@example.com", passwordreset.Record{Hash: "old-link", CreatedAt: newTime()})
	r.db.Sessions().Put(r.ctx, newSession("k-bob", 2, newTime()))
	upd := users.Update{Account: users.Account{Email: "robert@example.com", Username: "bob", FirstName: "B", LastName: "B", Language: "en"}}
	if _, err := r.db.AppUsers().Update(r.ctx, 2, upd, users.Effects{EmailChanged: true}); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := r.db.AuthapiResets().Latest(r.ctx, "bob@example.com"); found {
		t.Error("a link for the old address survives an address change")
	}
	if _, err := r.db.Sessions().Get(r.ctx, "k-bob"); err != nil {
		t.Errorf("an address change ended a session: %v", err)
	}
}

// What a server takes from the egg it borrows its process configuration from is in the configuration the API shows.
func TestAppServerConfigurationFollowsTheEggItBorrowsFrom(t *testing.T) {
	f := clientFx(t)
	r := f.r
	r.exec("INSERT INTO eggs (uuid, nest_id, author, name, config_files, config_startup, config_stop, config_logs, config_from) VALUES ('child', ?, 'x', 'Child', '{\"own\":1}', '{\"own\":1}', 'own-stop', '{}', ?)", f.nest, f.egg)
	r.exec("UPDATE servers SET egg_id = LAST_INSERT_ID() WHERE id = 2")
	nests, _, err := r.db.AppNests().ListNests(r.ctx, 1, 50, includes("servers"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range nests[0].Servers {
		if s.ID != 2 {
			continue
		}
		if s.Config.Egg.ConfigStop != "stop" || !strings.Contains(s.Config.Egg.ConfigStartup, "Done") || s.EggID == f.egg {
			t.Errorf("egg = %+v (egg id %d)", s.Config.Egg, s.EggID)
		}
		return
	}
	t.Error("server 2 is not listed")
}
