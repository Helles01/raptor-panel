//go:build integration

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/Helles01/raptor-panel/internal/db"
	"github.com/Helles01/raptor-panel/internal/legacycrypt/legacycrypttest"
	"github.com/Helles01/raptor-panel/internal/secrets"
	"github.com/Helles01/raptor-panel/internal/store"
)

// The cutover on a real database: the values of the old panel are only in the old encrypted columns, they are converted, and then the old columns are made empty
// with every safeguard in front of it. What the panel reads must be the same before and after.
func TestMigrateSecretsFinalize(t *testing.T) {
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
	oldKey := bytes.Repeat([]byte{0x33}, 32)
	vars := map[string]string{
		"RAPTOR_DB_HOST": host, "RAPTOR_DB_PORT": port, "RAPTOR_DB_NAME": cfg.DBName, "RAPTOR_DB_USER": cfg.User, "RAPTOR_DB_PASSWORD": cfg.Passwd,
		"RAPTOR_SECRET_KEY": key, "RAPTOR_APP_URL": "http://127.0.0.1",
	}
	withLegacy := map[string]string{}
	for k, v := range vars {
		withLegacy[k] = v
	}
	withLegacy[legacyKeyVariable] = "base64:" + base64.StdEncoding.EncodeToString(oldKey)

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
	for _, table := range []string{"users", "api_keys", "nodes", "database_hosts", "databases", "settings", "raptor_secrets"} {
		if _, err := conn.ExecContext(ctx, "TRUNCATE TABLE `"+table+"`"); err != nil {
			t.Fatalf("truncate %s: %v", table, err)
		}
	}
	if _, err := db.Migrate(ctx, sqlDB); err != nil {
		t.Fatal(err)
	}

	enc := func(s string) string { return legacycrypttest.Encrypt(oldKey, s) }
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO users (id, uuid, username, email, password, root_admin, use_totp, totp_secret, created_at, updated_at) VALUES (1, '00000000-0000-4000-8000-000000000001', 'ada', 'ada@example.com', '$2a$10$hash', 1, 1, ?, NOW(), NOW())", []any{enc("JBSWY3DPEHPK3PXP")}},
		{"INSERT INTO nodes (id, uuid, public, name, location_id, fqdn, scheme, memory, disk, daemon_token_id, daemon_token, daemonListen) VALUES (1, '11111111-1111-4111-8111-111111111111', 1, 'n1', 1, 'n1.example.com', 'https', 1024, 1024, 'tokenid000000001', ?, 8443)", []any{enc("the-node-token")}},
		{"INSERT INTO api_keys (id, user_id, key_type, identifier, token, allowed_ips, memo, created_at, updated_at) VALUES (1, 1, 1, 'ptlc_abcdefghijk', ?, '[]', 'm', NOW(), NOW())", []any{enc("the-api-secret")}},
		{"INSERT INTO database_hosts (id, name, host, port, username, password) VALUES (1, 'h', 'db.example.com', 3306, 'panel', ?)", []any{enc("host-password")}},
		{"INSERT INTO `databases` (id, server_id, database_host_id, `database`, username, remote, password, max_connections) VALUES (1, 1, 1, 's1_db', 'u1', '%', ?, 0)", []any{enc("db-password")}},
		{"INSERT INTO settings (`key`, `value`) VALUES ('settings::mail:mailers:smtp:password', ?), ('settings::app:name', 'Panel')", []any{enc("smtp-password")}},
	} {
		if _, err := conn.ExecContext(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 1")
	conn.Close()
	run := func(v map[string]string, args ...string) (int, string, string) {
		t.Helper()
		return runCmd(t, "", v, args...)
	}
	count := func(q string) (n int) { sqlDB.QueryRow(q).Scan(&n); return }
	oldValues := func() int {
		return count("SELECT (SELECT COUNT(*) FROM nodes WHERE daemon_token <> '') + (SELECT COUNT(*) FROM api_keys WHERE token <> '') + (SELECT COUNT(*) FROM users WHERE totp_secret IS NOT NULL AND totp_secret <> '') + " +
			"(SELECT COUNT(*) FROM database_hosts WHERE password <> '') + (SELECT COUNT(*) FROM `databases` WHERE password <> '') + (SELECT COUNT(*) FROM settings WHERE `key` = 'settings::mail:mailers:smtp:password' AND `value` <> '')")
	}
	if oldValues() != 6 {
		t.Fatalf("%d old values to begin with", oldValues())
	}

	// Before the conversion nothing is ready: the check says so, and changes nothing. It needs no old key.
	code, out, _ := run(vars, "migrate-secrets", "--finalize")
	if code != 1 || !strings.Contains(out, "NOT READY nodes.daemon_token id=1: not converted") || oldValues() != 6 {
		t.Fatalf("before the conversion: exit %d, %d old values\n%s", code, oldValues(), out)
	}
	if code, _, _ := run(vars, "migrate-secrets", "--finalize", "--yes", "--backup-taken"); code != 1 || oldValues() != 6 {
		t.Errorf("--yes before the conversion: exit %d, %d old values", code, oldValues())
	}

	if code, out, errOut := run(withLegacy, "migrate-secrets"); code != 0 {
		t.Fatalf("migrate-secrets = %d\n%s\n%s", code, out, errOut)
	}
	box, err := secrets.NewBox(mustKey(t, key))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.New(sqlDB, box)
	if err != nil {
		t.Fatal(err)
	}
	readNode := func() string {
		t.Helper()
		c, err := st.AuthzNodes().ByTokenID(ctx, "tokenid000000001")
		if err != nil {
			t.Fatalf("the node does not authenticate: %v", err)
		}
		return c.Token
	}
	if readNode() != "the-node-token" {
		t.Fatal("the converted node token is not the old one")
	}

	// The check passes now, and changes nothing.
	code, out, _ = run(vars, "migrate-secrets", "--finalize")
	if code != 0 || !strings.Contains(out, "nodes.daemon_token: 1 old values, 1 would be made empty") || !strings.Contains(out, "nothing was changed") || oldValues() != 6 {
		t.Fatalf("the check: exit %d, %d old values\n%s", code, oldValues(), out)
	}
	// Doing it needs the word that a backup was made, and a flag that does not belong is a mistake.
	if code, _, errOut := run(vars, "migrate-secrets", "--finalize", "--yes"); code != 1 || !strings.Contains(errOut, "--backup-taken") || oldValues() != 6 {
		t.Errorf("--yes without a backup: exit %d, %d old values", code, oldValues())
	}
	for _, args := range [][]string{{"--yes"}, {"--backup-taken"}, {"--finalize", "--all"}, {"--finalize", "--dry-run"}} {
		if code, _, _ := run(vars, append([]string{"migrate-secrets"}, args...)...); code != 2 || oldValues() != 6 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
	// A different key of this panel opens none of the copies: nothing is made empty.
	wrong := map[string]string{}
	for k, v := range vars {
		wrong[k] = v
	}
	other, _ := secrets.GenerateKey()
	wrong["RAPTOR_SECRET_KEY"] = other
	if code, out, _ := run(wrong, "migrate-secrets", "--finalize", "--yes", "--backup-taken"); code != 1 || !strings.Contains(out, "cannot be opened with this key") || oldValues() != 6 {
		t.Errorf("a wrong key: exit %d, %d old values\n%s", code, oldValues(), out)
	}

	// Then it is done.
	code, out, _ = run(vars, "migrate-secrets", "--finalize", "--yes", "--backup-taken")
	if code != 0 || !strings.Contains(out, "done: the old values are empty") || oldValues() != 0 {
		t.Fatalf("finalize: exit %d, %d old values\n%s", code, oldValues(), out)
	}
	var totp sql.NullString
	sqlDB.QueryRow("SELECT totp_secret FROM users WHERE id = 1").Scan(&totp)
	if totp.Valid {
		t.Errorf("the old second factor secret holds %q", totp.String)
	}
	// What else is in the tables is as it was, and what the panel reads is the same as before.
	if count("SELECT COUNT(*) FROM settings WHERE `key` = 'settings::app:name' AND `value` = 'Panel'") != 1 {
		t.Error("another setting was changed")
	}
	if readNode() != "the-node-token" {
		t.Error("the panel reads another node token after finalizing")
	}
	mailValues, password, err := st.MailSettings().Mail(ctx)
	if err != nil || password != "smtp-password" || mailValues == nil {
		t.Errorf("the mail password the panel reads = %q, %v", password, err)
	}
	if pw, err := st.ClientDatabases().Password(ctx, 1); err != nil || pw != "db-password" {
		t.Errorf("the database password the panel reads = %q, %v", pw, err)
	}
	// And again there is nothing to do.
	if code, out, _ := run(vars, "migrate-secrets", "--finalize", "--yes", "--backup-taken"); code != 0 || strings.Contains(out, "NOT READY") {
		t.Errorf("again: exit %d\n%s", code, out)
	}
}
