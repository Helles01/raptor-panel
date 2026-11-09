//go:build integration

package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/admin"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/dbhosts"
	"github.com/Helles01/raptor-panel/internal/nodes"
)

const testEgg = `{
  "meta": {"version": "PTDL_v2", "update_url": null},
  "name": "Paper", "author": "parker@example.com", "description": "Minecraft Paper",
  "features": ["eula"],
  "docker_images": {"Java 21": "ghcr.io/example/yolks:java_21", "Java 17": "ghcr.io/example/yolks:java_17"},
  "file_denylist": ["*.jar"],
  "startup": "java -jar {{SERVER_JARFILE}}",
  "config": {"files": "{}", "startup": "{\"done\": \"Done\"}", "logs": "{}", "stop": "stop"},
  "scripts": {"installation": {"script": "#!/bin/ash\necho install", "container": "alpine:3.4", "entrypoint": "ash"}},
  "variables": [
    {"name": "Jar", "description": "jar", "env_variable": "SERVER_JARFILE", "default_value": "server.jar", "user_viewable": true, "user_editable": true, "rules": "required|string|max:20", "field_type": "text"},
    {"name": "Build", "description": "", "env_variable": "BUILD_NUMBER", "default_value": "123", "user_viewable": 1, "user_editable": 0, "rules": "required|string"}
  ]
}`

func (r *rig) service() *admin.Service {
	r.t.Helper()
	s, err := admin.New(r.db.AdminStore(), "https://panel.example.com", []string{"en", "hu"}, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	return s
}

// realServer makes a location, a node, a nest, an egg, an allocation and a server owned by the user, with every key in place (for a rig that has the
// foreign key checks on). It returns the id of the node and of the server.
func realServer(r *rig, svc *admin.Service, owner uint64) (node, server uint64) {
	r.t.Helper()
	loc, err := svc.CreateLocation(r.ctx, "eu", "")
	if err != nil {
		r.t.Fatal(err)
	}
	n, err := svc.CreateNode(r.ctx, validNode(loc))
	if err != nil {
		r.t.Fatal(err)
	}
	nest, err := svc.CreateNest(r.ctx, "Minecraft", "", "parker@example.com")
	if err != nil {
		r.t.Fatal(err)
	}
	egg, err := svc.ImportEgg(r.ctx, nest, strings.NewReader(testEgg))
	if err != nil {
		r.t.Fatal(err)
	}
	r.exec("INSERT INTO allocations (node_id, ip, port) VALUES (?, '0.0.0.1', 25565)", n.ID)
	r.exec("INSERT INTO servers (uuid, uuidShort, node_id, name, description, owner_id, memory, swap, disk, io, cpu, allocation_id, nest_id, egg_id, startup, image) VALUES (?, 'aaaaaaaa', ?, 's', '', ?, 1, 0, 1, 500, 0, 1, ?, ?, 'run', 'img')", uuidA, n.ID, owner, nest, egg.ID)
	return n.ID, 1
}

func resetAdminTables(r *rig) {
	r.truncate("locations", "nests", "eggs", "egg_variables", "database_hosts", "databases", "settings", "allocations", "nodes", "servers", "raptor_secrets")
}

func TestAdminUsersEndToEnd(t *testing.T) {
	r := newRig(t)
	resetAdminTables(r)
	svc := r.service()

	made, err := svc.CreateUser(r.ctx, admin.UserInput{Email: "Ada@Example.com", Username: "Ada", FirstName: "Ada", LastName: "Lovelace", Admin: true})
	if err != nil || made.Password == "" || made.ID == 0 {
		t.Fatalf("created = %+v, %v", made, err)
	}
	// What was made can sign in: the same store the sign-in uses finds the user and the generated password verifies.
	u, err := r.db.AuthapiUsers().ByLogin(r.ctx, "username", "ada")
	if err != nil || u.ID != made.ID || !u.RootAdmin || u.Email != "ada@example.com" && u.Email != "Ada@Example.com" || !auth.VerifyPassword(u.PasswordHash, made.Password) || u.UsesTOTP {
		t.Fatalf("user = %+v, %v", u, err)
	}
	if got, err := r.db.AdminStore().FindUser(r.ctx, "Ada@Example.com"); err != nil || got.ID != made.ID || !got.Admin {
		t.Errorf("find by address = %+v, %v", got, err)
	}
	for _, ref := range []string{"ada", "1"} {
		if got, err := r.db.AdminStore().FindUser(r.ctx, ref); err != nil || got.Username != "ada" {
			t.Errorf("find %q = %+v, %v", ref, got, err)
		}
	}
	if _, err := r.db.AdminStore().FindUser(r.ctx, "nobody"); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}

	for name, in := range map[string]admin.UserInput{
		"username": {Email: "other@example.com", Username: "ADA", FirstName: "A", LastName: "B"},
		"email":    {Email: "ada@example.com", Username: "other", FirstName: "A", LastName: "B"},
	} {
		_, err := svc.CreateUser(r.ctx, in)
		var taken *admin.TakenError
		if !errors.As(err, &taken) || taken.Field != name {
			t.Errorf("%s: err = %v", name, err)
		}
	}

	dir, err := admin.NewDirectory(r.db.AdminDirectory())
	if err != nil {
		t.Fatal(err)
	}
	if list, err := dir.Users(r.ctx); err != nil || len(list) != 1 || list[0].Username != "ada" || !list[0].Admin {
		t.Errorf("users = %+v, %v", list, err)
	}
}

func TestAdminKeysEndToEnd(t *testing.T) {
	r := newRig(t)
	resetAdminTables(r)
	svc := r.service()
	if _, err := svc.CreateUser(r.ctx, admin.UserInput{Email: "ada@example.com", Username: "ada", FirstName: "A", LastName: "B", Admin: true}); err != nil {
		t.Fatal(err)
	}

	// The whole key that the command shows can be used: the authorizer finds it by the identifier and the hash agrees with the secret.
	k, err := svc.CreateKey(r.ctx, admin.KeyInput{User: "ada", Application: true, Description: "deploy", Access: []string{"servers=rw", "users=read"}, AllowedIPs: []string{"10.0.0.0/8"}})
	if err != nil {
		t.Fatal(err)
	}
	ident, secret, ok := auth.SplitKey(k.Token)
	if !ok || ident != k.Identifier {
		t.Fatalf("token = %q", k.Token)
	}
	got, err := r.db.AuthzKeys().ByIdentifier(r.ctx, ident)
	if err != nil || got.SecretHash != auth.HashKeySecret(secret) || got.Type != auth.KeyApplication || got.UserID != 1 {
		t.Fatalf("key = %+v, %v", got, err)
	}
	if got.Access["servers"] != auth.AccessReadWrite || got.Access["users"] != auth.AccessRead || got.Access["nodes"] != auth.AccessNone || len(got.AllowedIPs) != 1 || got.AllowedIPs[0] != "10.0.0.0/8" {
		t.Errorf("access %v, allowed %v", got.Access, got.AllowedIPs)
	}
	// The secret itself is nowhere in the table.
	var stored string
	r.sql.QueryRow("SELECT token FROM api_keys WHERE identifier = ?", ident).Scan(&stored)
	if stored != "" || strings.Contains(stored, secret) {
		t.Errorf("the token column holds %q", stored)
	}
	var sealed string
	r.sql.QueryRow("SELECT ciphertext FROM raptor_secrets WHERE table_name = 'api_keys' AND column_name = 'token_sha256'").Scan(&sealed)
	if sealed == "" || strings.Contains(sealed, auth.HashKeySecret(secret)) {
		t.Errorf("the sealed hash is %q", sealed)
	}

	dir, err := admin.NewDirectory(r.db.AdminDirectory())
	if err != nil {
		t.Fatal(err)
	}
	list, err := dir.Keys(r.ctx, "ada")
	if err != nil || len(list) != 1 || list[0].Identifier != ident || list[0].Username != "ada" || list[0].Description != "deploy" || list[0].Access["servers"] != auth.AccessReadWrite {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if _, err := dir.Keys(r.ctx, "nobody"); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}

	// Revoking removes the key and its sealed hash.
	if _, err := dir.RevokeKey(r.ctx, ident); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.AuthzKeys().ByIdentifier(r.ctx, ident); !errors.Is(err, authz.ErrNoKey) {
		t.Errorf("revoked key: %v", err)
	}
	var secretsLeft int
	r.sql.QueryRow("SELECT COUNT(*) FROM raptor_secrets WHERE table_name = 'api_keys'").Scan(&secretsLeft)
	if secretsLeft != 0 {
		t.Errorf("%d sealed values of a key that is gone", secretsLeft)
	}
	if _, err := dir.RevokeKey(r.ctx, ident); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("revoking twice: %v", err)
	}

	// A key of the Application API is for an administrator.
	if _, err := svc.CreateUser(r.ctx, admin.UserInput{Email: "bob@example.com", Username: "bob", FirstName: "B", LastName: "B"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateKey(r.ctx, admin.KeyInput{User: "bob", Application: true, Description: "x", Access: []string{"servers=read"}}); err == nil {
		t.Error("an application key for a user who is not an administrator")
	}
	if _, err := svc.CreateKey(r.ctx, admin.KeyInput{User: "nobody", Description: "x"}); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
}

// The limit of keys is counted with the user's row locked, so a crowd of requests cannot get past it.
func TestAdminKeyLimitHoldsUnderLoad(t *testing.T) {
	r := newRig(t)
	resetAdminTables(r)
	svc := r.service()
	if _, err := svc.CreateUser(r.ctx, admin.UserInput{Email: "ada@example.com", Username: "ada", FirstName: "A", LastName: "B"}); err != nil {
		t.Fatal(err)
	}
	var made int
	wins := once(t, 40, func() (bool, error) {
		_, err := svc.CreateKey(context.Background(), admin.KeyInput{User: "ada", Description: "load"})
		if err != nil && !errors.Is(err, auth.ErrTooManyKeys) {
			return false, err
		}
		return err == nil, nil
	})
	made = wins
	var rows int
	r.sql.QueryRow("SELECT COUNT(*) FROM api_keys").Scan(&rows)
	if made != auth.MaxAccountKeys || rows != auth.MaxAccountKeys {
		t.Errorf("%d keys were made (%d rows), the limit is %d", made, rows, auth.MaxAccountKeys)
	}
}

func TestAdminDeletingAUserAndTwoFactor(t *testing.T) {
	r := newRigFK(t, true)
	resetAdminTables(r)
	svc := r.service()
	if _, err := svc.CreateUser(r.ctx, admin.UserInput{Email: "ada@example.com", Username: "ada", FirstName: "A", LastName: "B", Admin: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateUser(r.ctx, admin.UserInput{Email: "bob@example.com", Username: "bob", FirstName: "B", LastName: "B"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateKey(r.ctx, admin.KeyInput{User: "bob", Description: "k"}); err != nil {
		t.Fatal(err)
	}

	// Two factor: the flag, the secret and the recovery tokens go.
	r.exec("UPDATE users SET use_totp = 1 WHERE id = 2")
	r.seal("users", 2, "totp_secret", "JBSWY3DPEHPK3PXP")
	r.exec("INSERT INTO recovery_tokens (user_id, token, created_at) VALUES (2, 'h1', NOW()), (2, 'h2', NOW())")
	if _, err := svc.DisableTwoFactor(r.ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	if u, _ := r.db.AuthapiUsers().ByID(r.ctx, 2); u.UsesTOTP || u.TOTPSecret != "" {
		t.Errorf("two-factor is still on: %+v", u)
	}
	var left int
	r.sql.QueryRow("SELECT COUNT(*) FROM recovery_tokens").Scan(&left)
	if left != 0 {
		t.Errorf("%d recovery tokens left", left)
	}
	if _, err := svc.DisableTwoFactor(r.ctx, "bob"); err != nil {
		t.Errorf("a second time: %v", err)
	}
	if _, err := svc.DisableTwoFactor(r.ctx, "nobody"); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}

	// A user who owns a server is not deleted; one who does not is, with the sealed secrets of their keys.
	realServer(r, svc, 2)
	if _, err := svc.DeleteUser(r.ctx, "bob"); err == nil {
		t.Fatal("a user who owns a server was deleted")
	}
	r.exec("DELETE FROM servers")
	if _, err := svc.DeleteUser(r.ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	var users, keys, secretsLeft int
	r.sql.QueryRow("SELECT COUNT(*) FROM users WHERE username = 'bob'").Scan(&users)
	r.sql.QueryRow("SELECT COUNT(*) FROM api_keys").Scan(&keys)
	r.sql.QueryRow("SELECT COUNT(*) FROM raptor_secrets WHERE table_name IN ('api_keys', 'users')").Scan(&secretsLeft)
	if users != 0 || keys != 0 || secretsLeft != 0 {
		t.Errorf("users %d, keys %d, sealed values %d after the delete", users, keys, secretsLeft)
	}
}

func TestAdminLocationsNodesNestsEggsAndSettings(t *testing.T) {
	r := newRig(t)
	resetAdminTables(r)
	svc := r.service()

	if _, err := svc.CreateNode(r.ctx, validNode(99)); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("a node in a location that is not there: %v", err)
	}
	loc, err := svc.CreateLocation(r.ctx, "eu.ams", "Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	node, err := svc.CreateNode(r.ctx, validNode(loc))
	if err != nil || len(node.Config) == 0 {
		t.Fatalf("node = %+v, %v", node, err)
	}
	// The token in the configuration the node is given is the one the authorizer checks.
	var tokenID string
	r.sql.QueryRow("SELECT daemon_token_id FROM nodes WHERE id = ?", node.ID).Scan(&tokenID)
	creds, err := r.db.AuthzNodes().ByTokenID(r.ctx, tokenID)
	if err != nil || creds.Node.ID != node.ID || creds.Token == "" || !strings.Contains(string(node.Config), creds.Token) {
		t.Fatalf("credentials = %+v, %v", creds, err)
	}
	var tokenColumn string
	r.sql.QueryRow("SELECT daemon_token FROM nodes WHERE id = ?", node.ID).Scan(&tokenColumn)
	if tokenColumn != "" {
		t.Errorf("the token column holds %q", tokenColumn)
	}

	dir, err := admin.NewDirectory(r.db.AdminDirectory())
	if err != nil {
		t.Fatal(err)
	}
	if ls, err := dir.Locations(r.ctx); err != nil || len(ls) != 1 || ls[0].Short != "eu.ams" || ls[0].Long != "Amsterdam" || ls[0].Nodes != 1 {
		t.Errorf("locations = %+v, %v", ls, err)
	}
	if ns, err := dir.Nodes(r.ctx); err != nil || len(ns) != 1 || ns[0].FQDN != "node1.example.com" || ns[0].Port != 8080 || ns[0].LocationID != loc || ns[0].Servers != 0 {
		t.Errorf("nodes = %+v, %v", ns, err)
	}

	if _, err := svc.ImportEgg(r.ctx, 99, strings.NewReader(testEgg)); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("an egg in a nest that is not there: %v", err)
	}
	nest, err := svc.CreateNest(r.ctx, "Minecraft", "Games", "parker@example.com")
	if err != nil {
		t.Fatal(err)
	}
	egg, err := svc.ImportEgg(r.ctx, nest, strings.NewReader(testEgg))
	if err != nil || egg.Variables != 2 {
		t.Fatalf("egg = %+v, %v", egg, err)
	}
	var images, features, startup, rules string
	r.sql.QueryRow("SELECT docker_images, features, startup FROM eggs WHERE id = ?", egg.ID).Scan(&images, &features, &startup)
	// The first image stays the first: the order of the file is the order of the object.
	if !strings.HasPrefix(images, `{"Java 21"`) || !strings.Contains(images, `"Java 17"`) || features != `["eula"]` || !strings.Contains(startup, "SERVER_JARFILE") {
		t.Errorf("images %s, features %s, startup %s", images, features, startup)
	}
	var viewable, editable int
	r.sql.QueryRow("SELECT user_viewable, user_editable, rules FROM egg_variables WHERE env_variable = 'BUILD_NUMBER'").Scan(&viewable, &editable, &rules)
	if viewable != 1 || editable != 0 || rules != "required|string" {
		t.Errorf("variable: %d %d %q", viewable, editable, rules)
	}

	if err := svc.SetSetting(r.ctx, "app:name", "My Panel"); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetSetting(r.ctx, "app:name", "Renamed"); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := dir.Setting(r.ctx, "app:name"); err != nil || !ok || v != "Renamed" {
		t.Errorf("setting = %q %v %v", v, ok, err)
	}
	if _, ok, err := dir.Setting(r.ctx, "app:locale"); err != nil || ok {
		t.Errorf("a setting never set: %v %v", ok, err)
	}
	if site, err := r.db.PageSettings().Site(r.ctx); err != nil || site.Name != "Renamed" {
		t.Errorf("the page does not see it: %+v, %v", site, err)
	}
}

func validNode(location uint64) nodes.Settings {
	n := nodes.DefaultNewNode()
	n.Name, n.LocationID, n.FQDN, n.Memory, n.Disk = "Node 1", location, "node1.example.com", 4096, 50000
	return n
}

type fakeAccess struct {
	tested []dbhosts.Conn
	ran    [][]dbhosts.Statement
	err    error
}

func (f *fakeAccess) Test(_ context.Context, c dbhosts.Conn) error {
	f.tested = append(f.tested, c)
	return f.err
}
func (f *fakeAccess) RunOn(_ context.Context, _ dbhosts.Conn, st []dbhosts.Statement) error {
	f.ran = append(f.ran, st)
	return f.err
}

func TestAdminDatabaseHosts(t *testing.T) {
	r := newRigFK(t, true)
	resetAdminTables(r)
	svc := r.service()
	if _, err := svc.CreateUser(r.ctx, admin.UserInput{Email: "ada@example.com", Username: "ada", FirstName: "A", LastName: "B"}); err != nil {
		t.Fatal(err)
	}
	realServer(r, svc, 1)
	access := &fakeAccess{}
	hosts, err := admin.NewHostService(r.db.AdminHosts(), access, nil)
	if err != nil {
		t.Fatal(err)
	}
	node := uint64(1)

	id, err := hosts.CreateHost(r.ctx, admin.HostInput{Name: "main", Host: "db.example.com", Port: 3306, Username: "panel", Password: "p4ss word!", NodeID: &node})
	if err != nil {
		t.Fatal(err)
	}
	var column string
	r.sql.QueryRow("SELECT password FROM database_hosts WHERE id = ?", id).Scan(&column)
	if column != "" {
		t.Errorf("the password column holds %q", column)
	}
	conn, err := r.db.DBHostLookup().Conn(r.ctx, id)
	if err != nil || conn.Password != "p4ss word!" || conn.Host != "db.example.com" || conn.Port != 3306 || conn.Username != "panel" {
		t.Fatalf("conn = %v, %v", conn, err)
	}
	if _, err := r.db.DBHostLookup().Conn(r.ctx, 99); !errors.Is(err, dbhosts.ErrNoHost) {
		t.Errorf("unknown host: %v", err)
	}
	if _, err := hosts.CreateHost(r.ctx, admin.HostInput{Name: "x", Host: "h", Port: 1, Username: "u", Password: "p", NodeID: ptrU(77)}); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("a node that is not there: %v", err)
	}

	list, err := hosts.ListHosts(r.ctx)
	if err != nil || len(list) != 1 || list[0].NodeID == nil || *list[0].NodeID != 1 || list[0].Databases != 0 {
		t.Fatalf("list = %+v, %v", list, err)
	}

	// Rotating the password changes it on the host and in the table together, the old one is what signs in.
	password, err := hosts.RotatePassword(r.ctx, id)
	if err != nil || len(access.ran) != 1 {
		t.Fatalf("rotate: %q, %v", password, err)
	}
	if c, _ := r.db.DBHostLookup().Conn(r.ctx, id); c.Password != password || password == "p4ss word!" {
		t.Errorf("stored password = %q", c.Password)
	}
	// A host that refuses leaves the row as it was.
	access.err = errors.New("refused")
	before, _ := r.db.DBHostLookup().Conn(r.ctx, id)
	if _, err := hosts.RotatePassword(r.ctx, id); err == nil {
		t.Error("a refusal was taken for success")
	}
	if after, _ := r.db.DBHostLookup().Conn(r.ctx, id); after != before {
		t.Error("the row changed although the host refused")
	}
	access.err = nil

	name := "renamed"
	if err := hosts.UpdateHost(r.ctx, id, admin.HostPatch{Name: &name, NodeSet: true}); err != nil {
		t.Fatal(err)
	}
	if list, _ := hosts.ListHosts(r.ctx); list[0].Name != "renamed" || list[0].NodeID != nil {
		t.Errorf("update: %+v", list[0])
	}

	// Not while it has databases.
	r.exec("INSERT INTO `databases` (server_id, database_host_id, `database`, username, remote, password, max_connections) VALUES (1, ?, 'db', 'u', '%', 'x', 0)", id)
	if err := hosts.DeleteHost(r.ctx, id); !errors.Is(err, admin.ErrHostInUse) {
		t.Errorf("delete with a database: %v", err)
	}
	r.exec("DELETE FROM `databases`")
	if err := hosts.DeleteHost(r.ctx, id); err != nil {
		t.Fatal(err)
	}
	var secretsLeft int
	r.sql.QueryRow("SELECT COUNT(*) FROM raptor_secrets WHERE table_name = 'database_hosts'").Scan(&secretsLeft)
	if secretsLeft != 0 {
		t.Errorf("%d sealed passwords of a host that is gone", secretsLeft)
	}
	if err := hosts.DeleteHost(r.ctx, id); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
	if err := hosts.TestHost(r.ctx, id); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("testing a host that is gone: %v", err)
	}
}

func ptrU(v uint64) *uint64 { return &v }

// Taking the second factor of everybody: the ones of the old panel (only in the old column), the ones Raptor sealed, the recovery tokens, and a secret that was left
// behind; and nothing else that is sealed.
func TestAdminResetAllTwoFactor(t *testing.T) {
	r := newRig(t)
	resetAdminTables(r)
	r.truncate("users", "recovery_tokens")
	svc := r.service()
	ids := map[string]uint64{}
	for _, name := range []string{"ada", "bob", "cat", "dan"} {
		created, err := svc.CreateUser(r.ctx, admin.UserInput{Email: name + "@example.com", Username: name, FirstName: "A", LastName: "B"})
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = created.ID
	}
	r.exec("UPDATE users SET use_totp = 1, totp_secret = 'eyJpdiI6Im9sZCJ9', totp_authenticated_at = NOW() WHERE id = ?", ids["ada"])
	r.exec("UPDATE users SET use_totp = 1 WHERE id = ?", ids["bob"])
	r.seal("users", ids["bob"], "totp_secret", "JBSWY3DPEHPK3PXP")
	r.seal("users", ids["dan"], "totp_secret", "KRSXG5CTMVRXEZLU") // left behind: the switch is off
	for _, name := range []string{"ada", "bob", "dan"} {
		r.exec("INSERT INTO recovery_tokens (user_id, token, created_at) VALUES (?, 'hash', NOW())", ids[name])
	}
	// What is sealed and is not a second factor must come through.
	r.seal("nodes", 1, "daemon_token", "a-node-token")
	r.seal("api_keys", 1, "token_sha256", "a-key-hash")

	count := func(q string, args ...any) (n int) { r.sql.QueryRow(q, args...).Scan(&n); return }

	// Without the go-ahead: how many, and nothing changed.
	var nc *admin.TwoFactorNotConfirmedError
	if _, err := svc.ResetAllTwoFactor(r.ctx, false); !errors.As(err, &nc) || nc.Count != 3 {
		t.Fatalf("not confirmed: %v", err)
	}
	if count("SELECT COUNT(*) FROM users WHERE use_totp = 1") != 2 || count("SELECT COUNT(*) FROM recovery_tokens") != 3 || count("SELECT COUNT(*) FROM raptor_secrets WHERE column_name = 'totp_secret'") != 2 {
		t.Fatal("something was changed without the go-ahead")
	}

	n, err := svc.ResetAllTwoFactor(r.ctx, true)
	if err != nil || n != 3 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
	if count("SELECT COUNT(*) FROM users WHERE use_totp = 1 OR totp_secret IS NOT NULL OR totp_authenticated_at IS NOT NULL") != 0 {
		t.Error("a user still has a second factor in the users table")
	}
	if count("SELECT COUNT(*) FROM recovery_tokens") != 0 || count("SELECT COUNT(*) FROM raptor_secrets WHERE table_name = 'users' AND column_name = 'totp_secret'") != 0 {
		t.Error("a recovery token or a sealed secret is left")
	}
	if count("SELECT COUNT(*) FROM raptor_secrets WHERE table_name = 'nodes'") != 1 || count("SELECT COUNT(*) FROM raptor_secrets WHERE table_name = 'api_keys'") != 1 {
		t.Error("a secret that is not a second factor was removed")
	}
	if count("SELECT COUNT(*) FROM users") != 4 {
		t.Error("a user was removed")
	}

	// Again, there is nobody to take it from.
	if _, err := svc.ResetAllTwoFactor(r.ctx, false); !errors.As(err, &nc) || nc.Count != 0 {
		t.Errorf("the count afterwards: %v", err)
	}
	if n, err := svc.ResetAllTwoFactor(r.ctx, true); err != nil || n != 0 {
		t.Errorf("again: n = %d, err = %v", n, err)
	}
}

// The databases of servers as the rotation of their passwords reads and writes them: all of them or one host's, and the new password sealed in the place the
// panel reads it from, next to an old one that is still in the old encrypted column.
func TestAdminDatabasesForTheRotationOfPasswords(t *testing.T) {
	r := newRig(t)
	resetAdminTables(r)
	r.truncate("databases", "database_hosts", "raptor_secrets")
	r.exec("INSERT INTO database_hosts (id, name, host, port, username, password) VALUES (1, 'a', 'db-a.example.com', 3306, 'panel', ''), (2, 'b', 'db-b.example.com', 3306, 'panel', '')")
	r.exec("INSERT INTO `databases` (id, server_id, database_host_id, `database`, username, remote, password, max_connections) VALUES " +
		"(1, 10, 1, 's10_world', 'u10_abc', '%', 'eyJpdiI6Im9sZCJ9', 0), (2, 10, 2, 's10_stats', 'u10_def', '10.0.0.%', 'eyJpdiI6Im9sZCJ9', 5), (3, 11, 1, 's11_data', 'u11_ghi', '%', '', NULL)")
	st := r.db.AdminDatabases()

	all, err := st.Databases(r.ctx, nil)
	if err != nil || len(all) != 3 {
		t.Fatalf("all = %+v, %v", all, err)
	}
	want := admin.ServerDatabase{ID: 2, ServerID: 10, HostID: 2, Name: "s10_stats", Username: "u10_def", Remote: "10.0.0.%", MaxConnections: 5}
	if all[1] != want || all[2].MaxConnections != 0 || all[0].ID != 1 {
		t.Errorf("the rows = %+v", all)
	}
	one := uint64(1)
	if got, err := st.Databases(r.ctx, &one); err != nil || len(got) != 2 || got[0].ID != 1 || got[1].ID != 3 {
		t.Errorf("host 1 = %+v, %v", got, err)
	}
	none := uint64(9)
	if got, err := st.Databases(r.ctx, &none); err != nil || len(got) != 0 {
		t.Errorf("host 9 = %+v, %v", got, err)
	}

	// The new password is what the panel reads back, and the old encrypted value is left as it was.
	if err := st.SetPassword(r.ctx, 1, "Newpass1+x"); err != nil {
		t.Fatal(err)
	}
	if got, err := r.db.ClientDatabases().Password(r.ctx, 1); err != nil || got != "Newpass1+x" {
		t.Errorf("the password read back = %q, %v", got, err)
	}
	var old string
	r.sql.QueryRow("SELECT password FROM `databases` WHERE id = 1").Scan(&old)
	if old != "eyJpdiI6Im9sZCJ9" {
		t.Errorf("the old column holds %q", old)
	}
	// A second time replaces the first, and a database that is not there is not found.
	if err := st.SetPassword(r.ctx, 1, "Another2+y"); err != nil {
		t.Fatal(err)
	}
	var sealed int
	r.sql.QueryRow("SELECT COUNT(*) FROM raptor_secrets WHERE table_name = 'databases' AND row_id = 1").Scan(&sealed)
	if got, _ := r.db.ClientDatabases().Password(r.ctx, 1); got != "Another2+y" || sealed != 1 {
		t.Errorf("after the second: %q, %d sealed", got, sealed)
	}
	if err := st.SetPassword(r.ctx, 99, "Whatever1+"); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("a database that is not there: %v", err)
	}
	if err := st.SetPassword(r.ctx, 1<<33, "Whatever1+"); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("an id too big for the column: %v", err)
	}
}
