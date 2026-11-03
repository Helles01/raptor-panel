//go:build integration

package store_test

// These tests need the test database (docs/testing-database.md): RAPTOR_TEST_DSN names an EMPTY database that may be written to and was made with
// `raptor db init`. They empty the tables they use first.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/Helles01/raptor-panel/internal/api/authapi"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/auth/passwordreset"
	"github.com/Helles01/raptor-panel/internal/auth/session"
	"github.com/Helles01/raptor-panel/internal/db"
	"github.com/Helles01/raptor-panel/internal/outbox"
	"github.com/Helles01/raptor-panel/internal/secrets"
	"github.com/Helles01/raptor-panel/internal/store"
)

type rig struct {
	t   *testing.T
	fk  bool
	sql *sql.DB
	box *secrets.Box
	db  *store.DB
	ctx context.Context
}

func newRig(t *testing.T) *rig { return newRigFK(t, false) }

// newRigFK is newRig with the foreign key checks of the database on (fk) or off. Off, a row can point at rows that are not made, which keeps the fixtures of
// most tests short; on, the cascades and the refusals of the database are part of what is tested.
func newRigFK(t *testing.T, fk bool) *rig {
	t.Helper()
	dsn := os.Getenv("RAPTOR_TEST_DSN")
	if dsn == "" {
		t.Skip("RAPTOR_TEST_DSN is not set")
	}
	// The rows of the tests point at rows that are not made (locations, nests, eggs); the checks stay off for this pool.
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	params := "loc=UTC"
	if !fk {
		params += "&foreign_key_checks=0"
	}
	sqlDB, err := sql.Open("mysql", dsn+sep+params)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	ctx := context.Background()
	if _, err := db.Migrate(ctx, sqlDB); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.New(sqlDB, box)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, fk: fk, sql: sqlDB, box: box, db: s, ctx: ctx}
	r.truncate("users", "api_keys", "servers", "nodes", "subusers", "settings", "sessions", "password_resets", "recovery_tokens", "activity_logs", "activity_log_subjects", "server_transfers", "raptor_secrets", "raptor_outbox",
		"allocations", "locations", "nests", "eggs", "egg_variables", "database_hosts", "databases")
	return r
}

// truncate empties tables whatever points at them: the checks are off on the one connection that does it, and back at what the pool has before it goes back.
func (r *rig) truncate(tables ...string) {
	r.t.Helper()
	conn, err := r.sql.Conn(r.ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(r.ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		r.t.Fatal(err)
	}
	for _, table := range tables {
		if _, err := conn.ExecContext(r.ctx, "TRUNCATE TABLE `"+table+"`"); err != nil {
			r.t.Fatalf("truncate %s: %v", table, err)
		}
	}
	restore := "SET FOREIGN_KEY_CHECKS = 0"
	if r.fk {
		restore = "SET FOREIGN_KEY_CHECKS = 1"
	}
	if _, err := conn.ExecContext(r.ctx, restore); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) exec(q string, args ...any) {
	r.t.Helper()
	if _, err := r.sql.ExecContext(r.ctx, q, args...); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
}

func (r *rig) seal(table string, id uint64, column, value string) {
	r.t.Helper()
	sealed, err := r.box.Seal(value, secrets.Context(table, id, column))
	if err != nil {
		r.t.Fatal(err)
	}
	r.exec("INSERT INTO raptor_secrets (table_name, row_id, column_name, ciphertext) VALUES (?, ?, ?, ?)", table, id, column, sealed)
}

func (r *rig) user(id uint64, name string, admin, totp bool) {
	r.t.Helper()
	r.exec("INSERT INTO users (id, uuid, username, email, password, root_admin, use_totp, created_at, updated_at) VALUES (?, ?, ?, ?, '$2a$10$hash', ?, ?, NOW(), NOW())",
		id, "00000000-0000-4000-8000-00000000000"+string(rune('0'+id)), name, name+"@example.com", admin, totp)
}

func (r *rig) node(id uint64, tokenID string) {
	r.t.Helper()
	r.exec("INSERT INTO nodes (id, uuid, public, name, location_id, fqdn, scheme, memory, disk, daemon_token_id, daemon_token, daemonListen) VALUES (?, ?, 1, ?, 1, ?, 'https', 1024, 1024, ?, 'laravel-encrypted', 8443)",
		id, "11111111-1111-4111-8111-11111111111"+string(rune('0'+id)), "node"+string(rune('0'+id)), "n"+string(rune('0'+id))+".example.com", tokenID)
}

func (r *rig) server(id, owner, node uint64, short, uuid string) {
	r.t.Helper()
	r.exec("INSERT INTO servers (id, uuid, uuidShort, node_id, name, description, owner_id, memory, swap, disk, io, cpu, allocation_id, nest_id, egg_id, startup, image) VALUES (?, ?, ?, ?, 's', '', ?, 1, 0, 1, 500, 0, ?, 1, 1, 'run', 'img')",
		id, uuid, short, node, owner, id)
}

const (
	uuidA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	uuidB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

func TestAuthzUsersAndServers(t *testing.T) {
	r := newRig(t)
	r.user(1, "ada", true, true)
	r.user(2, "bob", false, false)
	r.node(1, "nodetokenid0001")
	r.server(1, 1, 1, "aaaaaaaa", uuidA)
	r.server(2, 2, 1, "bbbbbbbb", uuidB)
	r.exec("UPDATE servers SET status = 'suspended' WHERE id = 2")
	r.exec("UPDATE nodes SET maintenance_mode = 1 WHERE id = 1")
	r.exec("INSERT INTO server_transfers (server_id, old_node, new_node, old_allocation, new_allocation) VALUES (1, 1, 2, 1, 2)")
	r.exec("INSERT INTO subusers (user_id, server_id, permissions) VALUES (2, 1, ?)", `["control.console","file.read"]`)

	users := r.db.AuthzUsers()
	u, err := users.ByID(r.ctx, 1)
	if err != nil || u.ID != 1 || !u.RootAdmin || !u.UsesTOTP || u.UUID == "" {
		t.Errorf("ada = %+v, %v", u, err)
	}
	if u, err := users.ByID(r.ctx, 2); err != nil || u.RootAdmin || u.UsesTOTP {
		t.Errorf("bob = %+v, %v", u, err)
	}
	for _, id := range []uint64{99, 0, 1 << 40} {
		if _, err := users.ByID(r.ctx, id); !errors.Is(err, authz.ErrNoUser) {
			t.Errorf("user %d: %v", id, err)
		}
	}

	srv := r.db.AuthzServers()
	for _, ref := range []string{"aaaaaaaa", uuidA} {
		s, err := srv.Find(r.ctx, ref)
		if err != nil || s.ID != 1 || s.OwnerID != 1 || s.NodeID != 1 || s.ShortID != "aaaaaaaa" || s.UUID != uuidA || s.NodeName != "node1" {
			t.Errorf("%s: %+v, %v", ref, s, err)
		}
		if !s.State.NodeMaintenance || !s.State.Transferring || s.State.Status != "" {
			t.Errorf("%s: state %+v", ref, s.State)
		}
	}
	if s, err := srv.Find(r.ctx, "bbbbbbbb"); err != nil || s.State.Status != "suspended" || s.State.Transferring {
		t.Errorf("bob's server: %+v, %v", s, err)
	}
	for _, ref := range []string{"cccccccc", "short", "", uuidA + "x", strings.ToUpper(uuidA[:8]) + "x"} {
		if _, err := srv.Find(r.ctx, ref); !errors.Is(err, authz.ErrNoServer) {
			t.Errorf("%q: %v", ref, err)
		}
	}

	perms, ok, err := srv.SubuserPermissions(r.ctx, 1, 2)
	if err != nil || !ok || len(perms) != 2 || perms[0] != "control.console" {
		t.Errorf("perms = %v %v %v", perms, ok, err)
	}
	if _, ok, err := srv.SubuserPermissions(r.ctx, 1, 1); err != nil || ok {
		t.Errorf("an owner is no subuser: %v %v", ok, err)
	}
	r.exec("UPDATE subusers SET permissions = NULL WHERE user_id = 2")
	if perms, ok, err := srv.SubuserPermissions(r.ctx, 1, 2); err != nil || !ok || len(perms) != 0 {
		t.Errorf("null permissions = %v %v %v", perms, ok, err)
	}
}

func TestAuthzKeys(t *testing.T) {
	r := newRig(t)
	r.user(1, "ada", true, false)
	keys := r.db.AuthzKeys()
	// A key that came from the old panel: the secret is sealed, the hash is made on reading.
	r.exec("INSERT INTO api_keys (id, user_id, key_type, identifier, token, allowed_ips, last_used_at) VALUES (1, 1, 1, 'ptlc_migrated001', 'x', ?, '2026-11-01 10:00:00')", `["10.0.0.0/8","192.0.2.7"]`)
	r.seal("api_keys", 1, "token", "the-old-secret")
	k, err := keys.ByIdentifier(r.ctx, "ptlc_migrated001")
	if err != nil || k.ID != 1 || k.UserID != 1 || k.Type != auth.KeyAccount || k.SecretHash != auth.HashKeySecret("the-old-secret") || k.Access != nil {
		t.Fatalf("migrated key = %+v, %v", k, err)
	}
	if len(k.AllowedIPs) != 2 || k.AllowedIPs[1] != "192.0.2.7" || k.LastUsedAt == nil || k.LastUsedAt.Year() != 2026 {
		t.Errorf("allowed %v, last used %v", k.AllowedIPs, k.LastUsedAt)
	}

	// A key made by Raptor: only the hash is sealed, and it is used as it is; an application key carries its levels.
	r.exec("INSERT INTO api_keys (id, user_id, key_type, identifier, token, r_servers, r_users, r_nodes, r_server_databases) VALUES (2, 1, 2, 'ptla_raptorkey02', '', 3, 1, 0, 3)")
	r.seal("api_keys", 2, "token_sha256", auth.HashKeySecret("new-secret"))
	k, err = keys.ByIdentifier(r.ctx, "ptla_raptorkey02")
	if err != nil || k.SecretHash != auth.HashKeySecret("new-secret") || k.Type != auth.KeyApplication {
		t.Fatalf("new key = %+v, %v", k, err)
	}
	for resource, want := range map[string]auth.Access{"servers": auth.AccessReadWrite, "users": auth.AccessRead, "nodes": auth.AccessNone, "server_databases": auth.AccessReadWrite, "eggs": auth.AccessNone} {
		if k.Access[resource] != want {
			t.Errorf("%s = %d, want %d", resource, k.Access[resource], want)
		}
	}
	if len(k.Access) != len(auth.ApplicationResources()) {
		t.Errorf("%d resources", len(k.Access))
	}

	// A damaged list restricts the key.
	r.exec("INSERT INTO api_keys (id, user_id, key_type, identifier, token, allowed_ips) VALUES (3, 1, 1, 'ptlc_damaged0003', '', 'not json')")
	r.seal("api_keys", 3, "token", "s")
	if k, err := keys.ByIdentifier(r.ctx, "ptlc_damaged0003"); err != nil || len(k.AllowedIPs) != 1 {
		t.Errorf("damaged list = %v, %v", k.AllowedIPs, err)
	}
	// No secret of Raptor's, an expired key and a key that is not there are all not keys.
	r.exec("INSERT INTO api_keys (id, user_id, key_type, identifier, token) VALUES (4, 1, 1, 'ptlc_nosecret004', 'laravel')")
	r.exec("INSERT INTO api_keys (id, user_id, key_type, identifier, token, expires_at) VALUES (5, 1, 1, 'ptlc_expired0005', '', '2020-01-01 00:00:00')")
	r.seal("api_keys", 5, "token", "s")
	for _, ident := range []string{"ptlc_nosecret004", "ptlc_expired0005", "ptlc_nothere0006", ""} {
		if _, err := keys.ByIdentifier(r.ctx, ident); !errors.Is(err, authz.ErrNoKey) {
			t.Errorf("%q: %v", ident, err)
		}
	}

	at := time.Date(2026, 11, 3, 12, 30, 0, 0, time.UTC)
	if err := keys.Touch(r.ctx, 1, at); err != nil {
		t.Fatal(err)
	}
	if k, _ := keys.ByIdentifier(r.ctx, "ptlc_migrated001"); k.LastUsedAt == nil || !k.LastUsedAt.Equal(at) {
		t.Errorf("last used = %v", k.LastUsedAt)
	}
}

func TestAuthzNodesSettingsAndAudit(t *testing.T) {
	r := newRig(t)
	r.user(1, "ada", true, false)
	r.node(1, "nodetokenid0001")
	r.node(2, "nodetokenid0002")
	r.seal("nodes", 1, "daemon_token", "the-node-token")

	nodes := r.db.AuthzNodes()
	c, err := nodes.ByTokenID(r.ctx, "nodetokenid0001")
	if err != nil || c.Node.ID != 1 || c.Token != "the-node-token" || c.Node.UUID == "" {
		t.Errorf("node = %+v, %v", c, err)
	}
	// Node 2 has only the old, Laravel-encrypted token: it cannot authenticate.
	for _, id := range []string{"nodetokenid0002", "nodetokenid9999"} {
		if _, err := nodes.ByTokenID(r.ctx, id); !errors.Is(err, authz.ErrNoNode) {
			t.Errorf("%s: %v", id, err)
		}
	}

	set := r.db.AuthzSettings()
	if n, err := set.TwoFactorLevel(r.ctx); err != nil || n != 0 {
		t.Errorf("unset = %d, %v", n, err)
	}
	for value, want := range map[string]int{"0": 0, "1": 1, "2": 2} {
		r.exec("DELETE FROM settings")
		r.exec("INSERT INTO settings (`key`, `value`) VALUES ('settings::pterodactyl:auth:2fa_required', ?)", value)
		if n, err := set.TwoFactorLevel(r.ctx); err != nil || n != want {
			t.Errorf("%s = %d, %v", value, n, err)
		}
	}
	for _, bad := range []string{"3", "-1", "yes", ""} {
		r.exec("UPDATE settings SET `value` = ?", bad)
		if _, err := set.TwoFactorLevel(r.ctx); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}

	if err := r.db.AuthzAudit().IPBlocked(r.ctx, 1, "ptlc_migrated001", "203.0.113.9", "curl/8"); err != nil {
		t.Fatal(err)
	}
	var event, ip, props, actorType sql.NullString
	var subjects int
	err = r.sql.QueryRow("SELECT event, ip, properties, actor_type FROM activity_logs").Scan(&event, &ip, &props, &actorType)
	if err != nil || event.String != "auth:ip-blocked" || ip.String != "203.0.113.9" || actorType.Valid {
		t.Fatalf("entry = %v %v %v, %v", event, ip, actorType, err)
	}
	var p map[string]any
	if json.Unmarshal([]byte(props.String), &p) != nil || p["identifier"] != "ptlc_migrated001" || p["useragent"] != "curl/8" || p["ip"] != "203.0.113.9" {
		t.Errorf("properties = %s", props.String)
	}
	r.sql.QueryRow("SELECT COUNT(*) FROM activity_log_subjects WHERE subject_id = 1 AND subject_type = 'Pterodactyl\\\\Models\\\\User'").Scan(&subjects)
	if subjects != 1 {
		t.Errorf("%d subjects", subjects)
	}
}

func TestSessions(t *testing.T) {
	r := newRig(t)
	st := r.db.Sessions()
	at := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	guest := session.Record{Key: "k-guest", Payload: []byte(`{"a":1}`), IP: "203.0.113.9", UserAgent: "ua", LastActivity: at}
	if err := st.Put(r.ctx, guest); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(r.ctx, "k-guest")
	if err != nil || got.UserID != 0 || string(got.Payload) != `{"a":1}` || got.IP != "203.0.113.9" || !got.LastActivity.Equal(at) {
		t.Fatalf("guest = %+v, %v", got, err)
	}
	// A put of the same key replaces the session.
	guest.UserID, guest.Payload = 7, []byte(`{"a":2}`)
	if err := st.Put(r.ctx, guest); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Get(r.ctx, "k-guest"); got.UserID != 7 || string(got.Payload) != `{"a":2}` {
		t.Errorf("replaced = %+v", got)
	}
	if _, err := st.Get(r.ctx, "nothing"); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}

	for _, k := range []string{"k1", "k2", "k3"} {
		if err := st.Put(r.ctx, session.Record{Key: k, UserID: 9, Payload: []byte("{}"), LastActivity: at}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.DeleteForUserExcept(r.ctx, 9, "k2"); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]bool{"k1": false, "k2": true, "k3": false, "k-guest": true} {
		if _, err := st.Get(r.ctx, k); (err == nil) != want {
			t.Errorf("%s present = %v, want %v", k, err == nil, want)
		}
	}
	if err := st.DeleteForUser(r.ctx, 9); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(r.ctx, "k2"); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("k2 after DeleteForUser: %v", err)
	}
	if err := st.Delete(r.ctx, "k-guest"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(r.ctx, "k-guest"); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("k-guest after Delete: %v", err)
	}
}

// once runs f in many goroutines at once and counts the ones that answered true.
func once(t *testing.T, n int, f func() (bool, error)) int {
	t.Helper()
	var wins atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := f()
			if err != nil {
				t.Error(err)
			}
			if ok {
				wins.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	return int(wins.Load())
}

func TestAuthapiUsers(t *testing.T) {
	r := newRig(t)
	r.user(1, "Ada", true, true)
	r.user(2, "bob", false, false)
	r.seal("users", 1, "totp_secret", "JBSWY3DPEHPK3PXP")
	us := r.db.AuthapiUsers()

	for _, tc := range []struct{ field, value string }{{"username", "ADA"}, {"username", "ada"}, {"email", "Ada@Example.com"}} {
		u, err := us.ByLogin(r.ctx, tc.field, tc.value)
		if err != nil || u.ID != 1 || u.Username != "Ada" || !u.RootAdmin || !u.UsesTOTP || u.TOTPSecret != "JBSWY3DPEHPK3PXP" || u.PasswordHash != "$2a$10$hash" || u.CreatedAt.IsZero() {
			t.Errorf("%v: %+v, %v", tc, u, err)
		}
	}
	if u, err := us.ByLogin(r.ctx, "username", "bob"); err != nil || u.TOTPSecret != "" || u.UsesTOTP || u.TOTPLastUsed != nil {
		t.Errorf("bob = %+v, %v", u, err)
	}
	for _, tc := range []struct{ field, value string }{{"username", "nobody"}, {"email", "x@example.com"}} {
		if _, err := us.ByLogin(r.ctx, tc.field, tc.value); !errors.Is(err, authapi.ErrNoUser) {
			t.Errorf("%v: %v", tc, err)
		}
	}
	if _, err := us.ByLogin(r.ctx, "password", "x"); err == nil || errors.Is(err, authapi.ErrNoUser) {
		t.Errorf("a field that is not a way to sign in: %v", err)
	}
	if u, err := us.ByID(r.ctx, 2); err != nil || u.Username != "bob" {
		t.Errorf("ByID = %+v, %v", u, err)
	}
	if u, err := us.ByEmail(r.ctx, "BOB@example.com"); err != nil || u.ID != 2 {
		t.Errorf("ByEmail = %+v, %v", u, err)
	}
	for _, id := range []uint64{0, 77, 1 << 40} {
		if _, err := us.ByID(r.ctx, id); !errors.Is(err, authapi.ErrNoUser) {
			t.Errorf("ByID(%d): %v", id, err)
		}
	}

	if err := us.SetPassword(r.ctx, 2, "$2a$10$other"); err != nil {
		t.Fatal(err)
	}
	if u, _ := us.ByID(r.ctx, 2); u.PasswordHash != "$2a$10$other" {
		t.Errorf("hash = %q", u.PasswordHash)
	}
	if err := us.SetPassword(r.ctx, 99, "x"); !errors.Is(err, authapi.ErrNoUser) {
		t.Errorf("unknown user: %v", err)
	}
}

// One code, one request: of any number of requests that present the same step, exactly one is let in.
func TestUseTOTPStepLetsOneInAndOnlyForwards(t *testing.T) {
	r := newRig(t)
	r.user(1, "ada", false, true)
	us := r.db.AuthapiUsers()
	step := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

	if n := once(t, 20, func() (bool, error) { return us.UseTOTPStep(r.ctx, 1, step) }); n != 1 {
		t.Errorf("%d requests used the same code", n)
	}
	if ok, err := us.UseTOTPStep(r.ctx, 1, step); err != nil || ok {
		t.Errorf("the same step again: %v %v", ok, err)
	}
	if ok, err := us.UseTOTPStep(r.ctx, 1, step.Add(-30*time.Second)); err != nil || ok {
		t.Errorf("an earlier step: %v %v", ok, err)
	}
	if ok, err := us.UseTOTPStep(r.ctx, 1, step.Add(30*time.Second)); err != nil || !ok {
		t.Errorf("a later step: %v %v", ok, err)
	}
	if u, _ := us.ByID(r.ctx, 1); u.TOTPLastUsed == nil || !u.TOTPLastUsed.Equal(step.Add(30*time.Second)) {
		t.Errorf("last used = %v", u.TOTPLastUsed)
	}
	if ok, err := us.UseTOTPStep(r.ctx, 99, step); err != nil || ok {
		t.Errorf("unknown user: %v %v", ok, err)
	}
}

func TestRecoveryTokensAreSpentOnce(t *testing.T) {
	r := newRig(t)
	r.user(1, "ada", false, true)
	r.exec("INSERT INTO recovery_tokens (user_id, token, created_at) VALUES (1, 'hash-a', NOW()), (1, 'hash-b', NOW()), (2, 'hash-other', NOW())")
	rec := r.db.AuthapiRecovery()
	tokens, err := rec.ForUser(r.ctx, 1)
	if err != nil || len(tokens) != 2 || tokens[0].Hash != "hash-a" || tokens[1].Hash != "hash-b" {
		t.Fatalf("tokens = %+v, %v", tokens, err)
	}
	if n := once(t, 20, func() (bool, error) { return rec.Delete(r.ctx, tokens[0].ID) }); n != 1 {
		t.Errorf("%d requests spent the same token", n)
	}
	if left, _ := rec.ForUser(r.ctx, 1); len(left) != 1 || left[0].Hash != "hash-b" {
		t.Errorf("left = %+v", left)
	}
}

func TestPasswordResetLinks(t *testing.T) {
	r := newRig(t)
	rs := r.db.AuthapiResets()
	at := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	if _, found, err := rs.Latest(r.ctx, "ada@example.com"); err != nil || found {
		t.Fatalf("empty: %v %v", found, err)
	}
	if err := rs.Replace(r.ctx, "ada@example.com", passwordreset.Record{Hash: "hash-1", CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := rs.Replace(r.ctx, "ada@example.com", passwordreset.Record{Hash: "hash-2", CreatedAt: at.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := rs.Replace(r.ctx, "bob@example.com", passwordreset.Record{Hash: "hash-bob", CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	rec, found, err := rs.Latest(r.ctx, "ADA@example.com")
	if err != nil || !found || rec.Hash != "hash-2" || !rec.CreatedAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("latest = %+v %v %v", rec, found, err)
	}
	var rows int
	r.sql.QueryRow("SELECT COUNT(*) FROM password_resets WHERE email = 'ada@example.com'").Scan(&rows)
	if rows != 1 {
		t.Errorf("%d links for one address: an older link still works", rows)
	}
	if n := once(t, 20, func() (bool, error) { return rs.Consume(r.ctx, "ada@example.com") }); n != 1 {
		t.Errorf("%d requests used the same link", n)
	}
	if _, found, _ := rs.Latest(r.ctx, "bob@example.com"); !found {
		t.Error("another address lost its link")
	}
}

func TestDeauthorizingAUserQueuesOneOrderForEachNode(t *testing.T) {
	r := newRig(t)
	r.user(1, "ada", false, false)
	r.user(2, "bob", false, false)
	r.node(1, "nodetokenid0001")
	r.node(2, "nodetokenid0002")
	r.node(3, "nodetokenid0003")
	r.server(1, 1, 1, "aaaaaaaa", uuidA) // ada owns one on node 1
	r.server(2, 2, 2, "bbbbbbbb", uuidB) // bob owns one on node 2, ada is a subuser there
	r.server(3, 1, 1, "cccccccc", "cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	r.exec("INSERT INTO subusers (user_id, server_id, permissions) VALUES (1, 2, '[]')")
	r.exec("UPDATE servers SET node_id = 3 WHERE id = 3")

	if err := r.db.AuthapiNodes().DeauthorizeUser(r.ctx, 1); err != nil {
		t.Fatal(err)
	}
	rows, err := r.sql.Query("SELECT kind, payload FROM raptor_outbox ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[uint64]bool{}
	for rows.Next() {
		var kind, payload string
		rows.Scan(&kind, &payload)
		var d outbox.Deauthorization
		if kind != outbox.KindDeauthorize || json.Unmarshal([]byte(payload), &d) != nil || d.UserUUID != "00000000-0000-4000-8000-000000000001" || len(d.ServerUUIDs) != 0 {
			t.Errorf("job = %s %s", kind, payload)
		}
		seen[d.NodeID] = true
	}
	if len(seen) != 3 || !seen[1] || !seen[2] || !seen[3] {
		t.Errorf("orders for nodes %v, want 1, 2 and 3", seen)
	}
	// A user with no servers, and one that is not there, queue nothing more.
	before := len(seen)
	if err := r.db.AuthapiNodes().DeauthorizeUser(r.ctx, 99); err != nil {
		t.Fatal(err)
	}
	var n int
	r.sql.QueryRow("SELECT COUNT(*) FROM raptor_outbox").Scan(&n)
	if n != before {
		t.Errorf("%d jobs after an unknown user", n)
	}
}

func TestActivityOfTheSignIn(t *testing.T) {
	r := newRig(t)
	log := r.db.AuthapiActivity()
	props := `{"ip":"198.51.100.4","useragent":"ua"}`
	if err := log.Log(r.ctx, authapi.LogEntry{Event: "auth:success", Subject: 5, Actor: 5, Properties: props}); err != nil {
		t.Fatal(err)
	}
	if err := log.Log(r.ctx, authapi.LogEntry{Event: "auth:fail", Subject: 6, Properties: props}); err != nil {
		t.Fatal(err)
	}
	rows, err := r.sql.Query("SELECT event, ip, actor_id, properties FROM activity_logs ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var event, ip, p string
		var actor sql.NullInt64
		rows.Scan(&event, &ip, &actor, &p)
		got = append(got, event+"|"+ip+"|"+map[bool]string{true: "actor", false: "none"}[actor.Valid]+"|"+p)
	}
	want := []string{"auth:success|198.51.100.4|actor|" + props, "auth:fail|198.51.100.4|none|" + props}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("rows = %q", got)
	}
}

func TestOutbox(t *testing.T) {
	r := newRig(t)
	q, st := r.db.OutboxQueue(), r.db.OutboxStore()
	for i := 0; i < 5; i++ {
		if err := q.Enqueue(r.ctx, outbox.New{Kind: "test", Payload: []byte(`{"n":1}`)}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Add(time.Second)

	// Many claimers at once: no job is given to two of them.
	var mu sync.Mutex
	taken := map[uint64]int{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			jobs, err := st.Claim(r.ctx, now, 3, time.Minute)
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			for _, j := range jobs {
				taken[j.ID]++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(taken) != 5 {
		t.Errorf("%d of 5 jobs were claimed", len(taken))
	}
	for id, n := range taken {
		if n != 1 {
			t.Errorf("job %d was claimed %d times", id, n)
		}
	}
	// While the lease runs nothing is due; after it, everything is.
	if jobs, _ := st.Claim(r.ctx, now.Add(30*time.Second), 10, time.Minute); len(jobs) != 0 {
		t.Errorf("%d jobs claimed during the lease", len(jobs))
	}
	jobs, err := st.Claim(r.ctx, now.Add(2*time.Minute), 10, time.Minute)
	if err != nil || len(jobs) != 5 || jobs[0].Kind != "test" || string(jobs[0].Payload) != `{"n":1}` || jobs[0].Attempts != 0 {
		t.Fatalf("after the lease: %d jobs, %v", len(jobs), err)
	}
	if jobs[0].ID > jobs[1].ID {
		t.Error("the jobs are not oldest first")
	}

	// Done removes, Retry puts back for later with the failure, Fail sets aside for good.
	if err := st.Done(r.ctx, jobs[0].ID); err != nil {
		t.Fatal(err)
	}
	later := now.Add(10 * time.Minute)
	if err := st.Retry(r.ctx, jobs[1].ID, 3, later, "the server was busy"); err != nil {
		t.Fatal(err)
	}
	if err := st.Fail(r.ctx, jobs[2].ID, 10, "refused for good"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Claim(r.ctx, now.Add(5*time.Minute), 10, time.Minute); len(got) != 0 && got[0].ID == jobs[1].ID {
		t.Errorf("the retried job was due before its time: %+v", got)
	}
	got, _ := st.Claim(r.ctx, later.Add(time.Second), 10, time.Minute)
	var retried *outbox.Job
	for i := range got {
		if got[i].ID == jobs[1].ID {
			retried = &got[i]
		}
		if got[i].ID == jobs[0].ID || got[i].ID == jobs[2].ID {
			t.Errorf("job %d is back: it was done or set aside", got[i].ID)
		}
	}
	if retried == nil || retried.Attempts != 3 {
		t.Errorf("retried = %+v", retried)
	}
	var failure, state string
	r.sql.QueryRow("SELECT state, failure FROM raptor_outbox WHERE id = ?", jobs[2].ID).Scan(&state, &failure)
	if state != "failed" || failure != "refused for good" {
		t.Errorf("set aside: %s %q", state, failure)
	}
}

func TestOutboxNodesGiveTheTokenOpened(t *testing.T) {
	r := newRig(t)
	r.node(1, "nodetokenid0001")
	r.node(2, "nodetokenid0002")
	r.seal("nodes", 1, "daemon_token", "the-node-token")
	n, err := r.db.OutboxNodes().Node(r.ctx, 1)
	if err != nil || n.Token != "the-node-token" || n.FQDN != "n1.example.com" || n.Port != 8443 || n.Scheme != "https" {
		t.Errorf("node = %+v, %v", n, err)
	}
	for _, id := range []uint64{2, 9, 1 << 40} {
		if _, err := r.db.OutboxNodes().Node(r.ctx, id); !errors.Is(err, outbox.ErrNoNode) {
			t.Errorf("node %d: %v", id, err)
		}
	}
}

func TestPages(t *testing.T) {
	r := newRig(t)
	r.user(1, "ada", true, true)
	r.node(1, "nodetokenid0001")
	r.node(2, "nodetokenid0002")
	r.exec("UPDATE nodes SET scheme = 'http', daemonListen = 8080 WHERE id = 2")

	u, err := r.db.PageUsers().Find(r.ctx, 1)
	if err != nil || u.Username != "ada" || !u.RootAdmin || !u.UseTotp || u.Email != "ada@example.com" || u.Language != "en" || u.UUID == "" {
		t.Errorf("user = %+v, %v", u, err)
	}
	if _, err := r.db.PageUsers().Find(r.ctx, 9); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("unknown user: %v", err)
	}

	site, err := r.db.PageSettings().Site(r.ctx)
	if err != nil || site.Name != store.DefaultSiteName || site.Locale != store.DefaultLocale {
		t.Errorf("defaults = %+v, %v", site, err)
	}
	r.exec("INSERT INTO settings (`key`, `value`) VALUES ('settings::app:name', 'My Panel'), ('settings::app:locale', 'hu')")
	if site, err := r.db.PageSettings().Site(r.ctx); err != nil || site.Name != "My Panel" || site.Locale != "hu" {
		t.Errorf("set = %+v, %v", site, err)
	}

	origins, err := r.db.PageOrigins().ConnectSources(r.ctx)
	want := []string{"https://n1.example.com:8443", "wss://n1.example.com:8443", "http://n2.example.com:8080", "ws://n2.example.com:8080"}
	if err != nil || strings.Join(origins, " ") != strings.Join(want, " ") {
		t.Errorf("origins = %v, %v", origins, err)
	}
}

// The old panel could leave several links for one address; the newest is the one that counts.
func TestLatestResetLinkIsTheNewestOfSeveral(t *testing.T) {
	r := newRig(t)
	r.exec("INSERT INTO password_resets (email, token, created_at) VALUES ('old@example.com', 'h-old', '2026-11-01 10:00:00'), ('old@example.com', 'h-new', '2026-11-02 10:00:00'), ('old@example.com', 'h-mid', '2026-11-01 20:00:00')")
	rec, found, err := r.db.AuthapiResets().Latest(r.ctx, "old@example.com")
	if err != nil || !found || rec.Hash != "h-new" {
		t.Errorf("latest = %+v %v %v", rec, found, err)
	}
}
