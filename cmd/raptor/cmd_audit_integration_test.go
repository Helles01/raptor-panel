//go:build integration

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/Helles01/raptor-panel/internal/audit"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/db"
	"github.com/Helles01/raptor-panel/internal/secrets"
)

// The audit on a real database: every kind of row is read the way the checks expect it, the bad ones are found by name, the good ones are not, a secret that
// is stored next to them is never in the report, and a name with an escape sequence in it does not reach the terminal.
func TestAuditOnARealDatabase(t *testing.T) {
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
	vars := map[string]string{
		"RAPTOR_DB_HOST": host, "RAPTOR_DB_PORT": port, "RAPTOR_DB_NAME": cfg.DBName, "RAPTOR_DB_USER": cfg.User, "RAPTOR_DB_PASSWORD": cfg.Passwd,
		"RAPTOR_SECRET_KEY": key, "RAPTOR_APP_URL": "http://127.0.0.1",
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
	defer conn.Close()
	conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0")
	for _, table := range []string{"users", "recovery_tokens", "api_keys", "subusers", "servers", "nodes", "locations", "allocations", "eggs", "egg_variables", "schedules", "tasks", "settings", "raptor_secrets"} {
		if _, err := conn.ExecContext(ctx, "TRUNCATE TABLE `"+table+"`"); err != nil {
			t.Fatalf("truncate %s: %v", table, err)
		}
	}
	if _, err := db.Migrate(ctx, sqlDB); err != nil {
		t.Fatal(err)
	}
	good, err := auth.HashPassword("a good password")
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.NewBox(mustKey(t, key))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal("JBSWY3DPEHPK3PXP", secrets.Context("users", 5, "totp_secret"))
	if err != nil {
		t.Fatal(err)
	}

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// Users: 1 is fine; 2 has a password that is no hash; 3 has the second factor on and no secret anywhere; 4 has it on and the secret in the old column; 5 has it
	// on and the secret only in raptor_secrets (as after a conversion that emptied the old column, or a setup made with Raptor); 6 has an escape sequence in the name.
	user := func(id int, name, password string, totp int, secret any) {
		exec("INSERT INTO users (id, uuid, username, email, password, root_admin, use_totp, totp_secret, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 0, ?, ?, NOW(), NOW())",
			id, "00000000-0000-4000-8000-00000000000"+string(rune('0'+id)), name, name+"@example.com", password, totp, secret)
	}
	user(1, "ada", good, 0, nil)
	user(2, "bob", "plain-password", 0, nil)
	user(3, "carl", good, 1, nil)
	user(4, "dora", good, 1, "eyJpdiI6Im9sZCJ9")
	user(5, "eve", good, 1, nil)
	user(6, "evil\x1b[31mred", "plain-password", 0, nil)
	exec("INSERT INTO raptor_secrets (table_name, row_id, column_name, ciphertext, source_hash, created_at, updated_at) VALUES ('users', 5, 'totp_secret', ?, NULL, NOW(), NOW())", sealed)
	exec("INSERT INTO recovery_tokens (id, user_id, token, created_at) VALUES (1, 1, ?, NOW()), (2, 1, 'plain-token', NOW())", good)

	// Eggs: Paper is fine; Empty has no image; a variable with a rule Raptor does not know.
	exec("INSERT INTO eggs (id, uuid, nest_id, author, name, docker_images) VALUES (1, '11111111-1111-4111-8111-111111111111', 1, 'a@example.com', 'Paper', '{\"Java 21\": \"ghcr.io/example/yolks:java_21\"}')")
	exec("INSERT INTO eggs (id, uuid, nest_id, author, name, docker_images) VALUES (2, '22222222-2222-4222-8222-222222222222', 1, 'a@example.com', 'Empty', NULL)")
	exec("INSERT INTO egg_variables (id, egg_id, name, description, env_variable, default_value, user_viewable, user_editable, rules) VALUES " +
		"(1, 1, 'Jar', 'd', 'SERVER_JARFILE', 'server.jar', 1, 1, 'required|string|max:20'), (2, 1, 'Odd', 'd', 'ODD', '', 1, 1, 'required|made_up_rule:3')")

	// Schedules: a good one with a good task, a cron expression nobody can run, and a task of an action that does not exist.
	exec("INSERT INTO schedules (id, server_id, name, cron_day_of_week, cron_month, cron_day_of_month, cron_hour, cron_minute, is_active, is_processing) VALUES " +
		"(1, 1, 'Restart', '*', '*', '*', '4', '0', 1, 0), (2, 1, 'Broken', '*', '*', '*', '*', 'every five minutes', 1, 0)")
	exec("INSERT INTO tasks (id, schedule_id, sequence_id, action, payload, time_offset, is_queued) VALUES (1, 1, 1, 'command', 'say restarting', 0, 0), (2, 1, 2, 'bogus', 'x', 0, 0)")

	// API keys: no list; a list that works; a list that holds nothing that can be read; something that is not a list.
	key16 := func(n string) string { return "ptlc_" + n + strings.Repeat("x", 11-len(n)) }
	exec("INSERT INTO api_keys (id, user_id, key_type, identifier, token, allowed_ips, memo, created_at, updated_at) VALUES " +
		"(1, 1, 1, '" + key16("1") + "', '', NULL, 'm', NOW(), NOW()), (2, 1, 1, '" + key16("2") + "', '', '[\"10.0.0.0/8\"]', 'm', NOW(), NOW()), " +
		"(3, 1, 1, '" + key16("3") + "', '', '[\"not an address\"]', 'm', NOW(), NOW()), (4, 1, 1, '" + key16("4") + "', '', '{\"a\": 1}', 'm', NOW(), NOW())")

	// Subusers: one with a permission that does not exist, one whose permissions are not a list.
	exec("INSERT INTO subusers (id, user_id, server_id, permissions) VALUES (1, 2, 1, '[\"control.console\", \"nope.nope\"]'), (2, 2, 2, '{\"a\": 1}'), (3, 2, 3, '[\"control.console\"]')")

	// A server whose io is below what a change would be allowed, a node with a name that is no host, a location, an allocation with an address that is none.
	exec("INSERT INTO servers (id, uuid, uuidShort, node_id, name, description, owner_id, memory, swap, disk, io, cpu, allocation_id, nest_id, egg_id, startup, image) VALUES " +
		"(1, '33333333-3333-4333-8333-333333333333', '33333333', 1, 'Survival', '', 1, 1024, 0, 2048, 500, 0, 1, 1, 1, 'java -jar server.jar', 'ghcr.io/example/yolks:java_21'), " +
		"(2, '44444444-4444-4444-8444-444444444444', '44444444', 1, 'Slow', '', 1, 1024, 0, 2048, 5, 0, 2, 1, 1, 'java -jar server.jar', 'ghcr.io/example/yolks:java_21')")
	exec("INSERT INTO nodes (id, uuid, public, name, location_id, fqdn, scheme, memory, disk, daemon_token_id, daemon_token) VALUES " +
		"(1, '55555555-5555-4555-8555-555555555555', 1, 'n1', 1, 'n1.example.com', 'https', 4096, 40960, 'tokenid000000001', ''), " +
		"(2, '66666666-6666-4666-8666-666666666666', 1, 'n2', 1, 'not a host name!', 'https', 4096, 40960, 'tokenid000000002', '')")
	exec("INSERT INTO locations (id, short, `long`) VALUES (1, 'eu', 'Europe'), (2, '', NULL)")
	exec("INSERT INTO allocations (id, node_id, ip, port, ip_alias) VALUES (1, 1, '10.0.0.5', 25565, NULL), (2, 1, '999.1.1.1', 25566, NULL), (3, 1, '10.0.0.5', 25567, NULL)")

	// Settings: a good one, a known one with a value Raptor refuses, an unknown one, and the sealed password of the mail server, which must not be in the report.
	exec("INSERT INTO settings (`key`, `value`) VALUES ('settings::app:name', 'Panel'), ('settings::mail:mailers:smtp:port', 'abc'), ('settings::something:else', 'x'), " +
		"('settings::mail:mailers:smtp:password', 'SECRET-CIPHERTEXT-DO-NOT-PRINT'), ('not-a-setting', 'y')")
	exec("SET FOREIGN_KEY_CHECKS = 1")

	snapshot := func() (n int) {
		for _, table := range []string{"users", "recovery_tokens", "api_keys", "subusers", "servers", "nodes", "locations", "allocations", "eggs", "egg_variables", "schedules", "tasks", "settings", "raptor_secrets"} {
			var c int
			sqlDB.QueryRow("SELECT COUNT(*) FROM `" + table + "`").Scan(&c)
			n = n*7 + c
		}
		return
	}
	before := snapshot()

	code, out, errOut := runCmd(t, "", vars, "audit")
	if code != 1 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	for _, want := range []string{
		"users: 6 checked, 3 errors", // bob, carl and the evil one; dora and eve have their secret
		"  error   users#2 (bob): the stored password is not a bcrypt hash",
		"  error   users#3 (carl): two-factor authentication is on but there is no secret",
		"  error   recovery_tokens#2 (user 1): the stored token is not a bcrypt hash",
		"  error   egg_variables#2 (Paper ODD): the rules cannot be applied",
		"  error   eggs#2 (Empty): the egg has no Docker image",
		"  error   schedules#2 (Broken): the cron fields",
		"  error   schedules#1 (Restart), task 2: the action \"bogus\" is not known",
		"  error   api_keys#3 (" + key16("3") + "): none of the 1 allowed addresses can be read",
		"  error   api_keys#4 (" + key16("4") + "): none of the 1 allowed addresses can be read",
		"  error   allocations#2 (node 1, 999.1.1.1:25566): the address cannot be read",
		"  warning subusers#1 (server 1): permissions that do not exist: nope.nope",
		"  warning subusers#2 (server 2): permissions that do not exist: (not a list of texts)",
		"  warning servers#2 (Slow):",
		"  warning nodes#2 (n2):",
		"  warning locations#2: ",
		"  warning settings.mail:mailers:smtp:port:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report lacks %q", want)
		}
	}
	// A row that is fine is not named at the start of a finding (a bare substring would match "subusers#1" for "users#1").
	for _, not := range []string{"users#1 ", "users#4 ", "users#5 ", "api_keys#1 ", "api_keys#2 ", "subusers#3 ", "servers#1 ", "nodes#1 ", "locations#1", "allocations#1 ", "allocations#3 ", "eggs#1 ", "egg_variables#1 ", "settings.app:name", "settings.something"} {
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "  error   "+not) || strings.HasPrefix(line, "  warning "+not) {
				t.Errorf("the report names a row that is fine: %q", line)
			}
		}
	}
	if strings.Contains(out, "schedules#1 (Restart):") {
		t.Error("the schedule that is fine is named")
	}
	if strings.Contains(out+errOut, "SECRET-CIPHERTEXT") || strings.ContainsAny(out+errOut, "\x1b") {
		t.Errorf("the report carries the mail secret or an escape sequence:\n%q", out)
	}
	if !strings.Contains(errOut, "11 error(s)") && !strings.Contains(errOut, "error(s)") {
		t.Errorf("stderr = %q", errOut)
	}
	if snapshot() != before {
		t.Error("the audit changed the database")
	}
	// The settings that are read are the ones with the prefix, without it, and never the password of the mail server, whatever the checks do with them.
	loaded, err := db.NewAuditSource(sqlDB).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range loaded.Settings {
		names = append(names, s.Key)
	}
	if strings.Join(names, ",") != "app:name,mail:mailers:smtp:port,something:else" {
		t.Errorf("the settings read: %v", names)
	}

	// The JSON form is the same report.
	code, out, _ = runCmd(t, "", vars, "audit", "--json")
	var report audit.Report
	if code != 1 || json.Unmarshal([]byte(out), &report) != nil || len(report.Sections) != 12 || !report.HasErrors() {
		t.Fatalf("json: exit %d\n%s", code, out)
	}
	checked := map[string]int{}
	for _, s := range report.Sections {
		checked[s.Name] = s.Checked
	}
	for name, n := range map[string]int{"users": 6, "recovery_tokens": 2, "egg_variables": 2, "egg_images": 2, "schedules": 2, "api_keys": 4, "subusers": 3, "servers": 2, "nodes": 2, "locations": 2, "allocations": 3} {
		if checked[name] != n {
			t.Errorf("%s: %d rows read, want %d", name, checked[name], n)
		}
	}

	// Take the rows that are errors away: only warnings are left, and then the command succeeds.
	exec("SET FOREIGN_KEY_CHECKS = 0")
	exec("DELETE FROM users WHERE id IN (2, 3, 6)")
	exec("DELETE FROM recovery_tokens WHERE id = 2")
	exec("DELETE FROM egg_variables WHERE id = 2")
	exec("DELETE FROM eggs WHERE id = 2")
	exec("DELETE FROM schedules WHERE id = 2")
	exec("DELETE FROM tasks WHERE id = 2")
	exec("DELETE FROM api_keys WHERE id IN (3, 4)")
	exec("DELETE FROM allocations WHERE id = 2")
	code, out, errOut = runCmd(t, "", vars, "audit")
	if code != 0 || !strings.Contains(errOut, "0 error(s)") || strings.Contains(out, "  error ") || !strings.Contains(out, "  warning ") {
		t.Errorf("only warnings: exit %d\n%s\n%s", code, out, errOut)
	}
}
