//go:build integration

package main

import (
	"context"
	"database/sql"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/Helles01/raptor-panel/internal/db"
	"github.com/Helles01/raptor-panel/internal/secrets"
)

// The doctor on a real database: an empty one that is migrated, then one that has an administrator, a node, a mail server, and a node that came from the old
// panel with only its old token.
func TestDoctorOnARealDatabase(t *testing.T) {
	dsn := os.Getenv("RAPTOR_TEST_DSN")
	if dsn == "" {
		t.Skip("RAPTOR_TEST_DSN is not set")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	host, port, _ := strings.Cut(cfg.Addr, ":")
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	assets, err := filepath.Abs("../../web/public/assets")
	if err != nil {
		t.Fatal(err)
	}
	// The configuration does not allow port 0, so a free port is asked for and let go of.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := probe.Addr().String()
	probe.Close()
	vars := map[string]string{
		"RAPTOR_DB_HOST": host, "RAPTOR_DB_PORT": port, "RAPTOR_DB_NAME": cfg.DBName, "RAPTOR_DB_USER": cfg.User, "RAPTOR_DB_PASSWORD": cfg.Passwd,
		"RAPTOR_SECRET_KEY": key, "RAPTOR_APP_URL": "http://127.0.0.1:8000", "RAPTOR_HTTP_LISTEN": listen, "RAPTOR_ASSETS_DIR": assets,
	}
	ctx := context.Background()
	sqlDB, err := sql.Open("mysql", dsn+"&loc=UTC")
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0")
	for _, table := range []string{"users", "api_keys", "locations", "nodes", "settings", "raptor_secrets", "servers"} {
		if _, err := conn.ExecContext(ctx, "TRUNCATE TABLE `"+table+"`"); err != nil {
			t.Fatalf("truncate %s: %v", table, err)
		}
	}
	conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 1")
	conn.Close()
	if _, err := db.Migrate(ctx, sqlDB); err != nil {
		t.Fatal(err)
	}

	doctor := func() (int, string) {
		t.Helper()
		code, out, _ := runCmd(t, "", vars, "doctor")
		return code, out
	}
	has := func(out, line string) {
		t.Helper()
		if !strings.Contains(out, line) {
			t.Errorf("the report lacks %q:\n%s", line, out)
		}
	}

	// An empty panel: reachable and up to date, but nobody can sign in and nothing can run.
	code, out := doctor()
	has(out, "ok    database connection: connected, server ")
	has(out, "ok    schema:")
	has(out, "ok    clock:")
	has(out, "FAIL  administrators: there is no administrator")
	has(out, "warn  nodes: there is no node")
	has(out, "warn  mail: no mail server is set up")
	has(out, "ok    old secrets: no value of the old panel is waiting to be converted")
	if code != 1 {
		t.Errorf("exit %d for a panel with no administrator", code)
	}

	// Set up the way the administrator does.
	do := func(stdin string, args ...string) string {
		t.Helper()
		code, o, errOut := runCmd(t, stdin, vars, args...)
		if code != 0 {
			t.Fatalf("raptor %s = %d\n%s", strings.Join(args, " "), code, errOut)
		}
		return o
	}
	do("", "user", "create", "--email", "admin@example.com", "--username", "admin", "--first-name", "Ada", "--last-name", "Admin", "--admin")
	do("", "location", "create", "--short", "eu.ams", "--long", "Amsterdam")
	do("", "node", "create", "--name", "Node 1", "--location-id", "1", "--fqdn", "node1.example.com", "--memory", "4096", "--disk", "50000")
	do("", "settings", "set", "mail:mailers:smtp:host", "smtp.example.com")
	do("", "settings", "set", "mail:mailers:smtp:port", "587")
	do("", "settings", "set", "mail:mailers:smtp:encryption", "tls")
	do("", "settings", "set", "mail:mailers:smtp:username", "mailer")
	code, out = doctor()
	has(out, "ok    administrators: 1")
	has(out, "ok    nodes: 1, each with a token")
	has(out, "warn  mail: a user name is set for smtp.example.com:587 but no password")
	if code != 0 {
		t.Errorf("exit %d for a panel with warnings only:\n%s", code, out)
	}
	do("pass-word\n", "settings", "set-mail-password", "--password-stdin")
	_, out = doctor()
	has(out, "ok    mail: smtp.example.com:587")

	// A node, a token and a user with two-factor from the old panel: values that are only in the old, encrypted columns.
	if _, err := sqlDB.Exec("INSERT INTO nodes (id, uuid, public, name, location_id, fqdn, scheme, memory, disk, daemon_token_id, daemon_token, daemonListen) VALUES (7, '77777777-7777-4777-8777-777777777777', 1, 'Old node', 1, 'old.example.com', 'https', 1024, 1024, 'oldtokenid00007', 'eyJpdiI6Im9sZCJ9', 8443)"); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec("UPDATE users SET use_totp = 1, totp_secret = 'eyJpdiI6Im9sZCJ9' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	code, out = doctor()
	has(out, `warn  node 7: "Old node" has no token the panel can use: give it one with `+"`raptor node rotate-token 7`")
	has(out, "warn  old secrets: nodes.daemon_token: 1 of 1 are not converted")
	has(out, "warn  old secrets: users.totp_secret: 1 of 1 are not converted")
	if code != 0 {
		t.Errorf("exit %d: old values that are not converted are for a person to read, not a failure", code)
	}
	// Nothing was changed by looking.
	var users, nodes int
	sqlDB.QueryRow("SELECT COUNT(*) FROM users").Scan(&users)
	sqlDB.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&nodes)
	if users != 1 || nodes != 2 {
		t.Errorf("%d users and %d nodes: the doctor changed something", users, nodes)
	}

	// Rotating the token of the old node is what the line says to do, and the line goes.
	do("", "node", "rotate-token", "7")
	_, out = doctor()
	if strings.Contains(out, "node 7") {
		t.Errorf("the node still has no token:\n%s", out)
	}
}
