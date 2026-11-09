//go:build integration

package db_test

// Integration tests need a MariaDB/MySQL database. Point RAPTOR_TEST_DSN at an EMPTY database that may be written to,
// for example:
//
//	RAPTOR_TEST_DSN='root:password@tcp(127.0.0.1:3306)/raptor_test?parseTime=true' go test -tags integration ./internal/db/...
//
// The stand-ins for the reference panel's tables are TEMPORARY tables on a single connection, so they never collide
// with a real schema and disappear with the connection.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"

	"github.com/Helles01/raptor-panel/internal/db"
	"github.com/Helles01/raptor-panel/internal/legacycrypt"
	"github.com/Helles01/raptor-panel/internal/legacycrypt/legacycrypttest"
	"github.com/Helles01/raptor-panel/internal/secretmigration"
	"github.com/Helles01/raptor-panel/internal/secrets"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("RAPTOR_TEST_DSN")
	if dsn == "" {
		t.Skip("RAPTOR_TEST_DSN is not set")
	}
	sqlDB, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sqlDB.SetMaxOpenConns(1) // one connection: temporary tables and the migration lock stay visible
	t.Cleanup(func() { sqlDB.Close() })
	return sqlDB
}

func TestMigrate(t *testing.T) {
	ctx := context.Background()
	sqlDB := openTestDB(t)

	// Start clean: only Raptor's own tables are ever dropped here.
	for _, table := range []string{"raptor_secrets", "raptor_outbox", "raptor_migrations"} {
		if _, err := sqlDB.ExecContext(ctx, "DROP TABLE IF EXISTS "+table); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
	}

	first, err := db.Migrate(ctx, sqlDB)
	if err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if len(first) == 0 || first[0] != "0001_raptor_secrets" {
		t.Errorf("first Migrate applied %v", first)
	}

	second, err := db.Migrate(ctx, sqlDB)
	if err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if len(second) != 0 {
		t.Errorf("second Migrate applied %v, want nothing", second)
	}
}

func TestSecretStoreEndToEnd(t *testing.T) {
	ctx := context.Background()
	sqlDB := openTestDB(t)
	if _, err := db.Migrate(ctx, sqlDB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx, "DELETE FROM raptor_secrets"); err != nil {
		t.Fatal(err)
	}

	legacyKey := bytes.Repeat([]byte{0x11}, 32)
	nodeToken := legacycrypttest.Encrypt(legacyKey, "node-token-1")
	hostPassword := legacycrypttest.Encrypt(legacyKey, "host-password")
	smtp := legacycrypttest.Encrypt(legacyKey, "smtp-password")

	setup := []string{
		"CREATE TEMPORARY TABLE nodes (id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY, daemon_token TEXT NOT NULL)",
		"CREATE TEMPORARY TABLE api_keys (id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY, token TEXT NOT NULL)",
		"CREATE TEMPORARY TABLE users (id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY, totp_secret TEXT NULL)",
		"CREATE TEMPORARY TABLE database_hosts (id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY, password TEXT NOT NULL)",
		"CREATE TEMPORARY TABLE `databases` (id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY, password TEXT NOT NULL)",
		"CREATE TEMPORARY TABLE settings (id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY, `key` VARCHAR(191) NOT NULL, `value` TEXT NOT NULL)",
		"INSERT INTO nodes (daemon_token) VALUES ('" + nodeToken + "')",
		"INSERT INTO users (totp_secret) VALUES (NULL), ('')",
		"INSERT INTO database_hosts (password) VALUES ('" + hostPassword + "')",
		"INSERT INTO settings (`key`, `value`) VALUES ('settings::app:name', 'Panel'), ('settings::mail:mailers:smtp:password', '" + smtp + "')",
	}
	for _, stmt := range setup {
		if _, err := sqlDB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt[:40], err)
		}
	}

	legacy, err := legacycrypt.New(legacyKey)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.NewBox(bytes.Repeat([]byte{0x77}, 32))
	if err != nil {
		t.Fatal(err)
	}
	store := db.NewSecretStore(sqlDB)

	report, err := secretmigration.Run(ctx, store, legacy, box, secretmigration.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	converted := 0
	for _, c := range report.Columns {
		converted += c.Converted
	}
	if converted != 3 {
		t.Errorf("converted %d rows, want 3 (node token, host password, smtp password; NULL, empty and other settings are skipped)", converted)
	}

	// The converted value opens under the location it was stored for, and the old column is untouched.
	var sealed string
	if err := sqlDB.QueryRowContext(ctx,
		"SELECT ciphertext FROM raptor_secrets WHERE table_name = 'nodes' AND row_id = 1 AND column_name = 'daemon_token'").Scan(&sealed); err != nil {
		t.Fatalf("read converted value: %v", err)
	}
	if got, err := box.Open(sealed, secrets.Context("nodes", 1, "daemon_token")); err != nil || got != "node-token-1" {
		t.Errorf("converted value opens to %q, %v", got, err)
	}
	var old string
	if err := sqlDB.QueryRowContext(ctx, "SELECT daemon_token FROM nodes WHERE id = 1").Scan(&old); err != nil || old != nodeToken {
		t.Errorf("the old column was changed: %q, %v", old, err)
	}

	again, err := secretmigration.Run(ctx, store, legacy, box, secretmigration.Options{})
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	for _, c := range again.Columns {
		if c.Converted != 0 {
			t.Errorf("second run converted %d rows of %s, want 0", c.Converted, c.Column)
		}
	}
}

// InitBaseline needs a database with none of the panel's tables; it drops the ones it makes first, so point RAPTOR_TEST_DSN at a database that is
// only for tests.
func TestInitBaseline(t *testing.T) {
	ctx := context.Background()
	sqlDB := openTestDB(t)

	if _, err := sqlDB.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		t.Fatal(err)
	}
	for _, table := range db.BaselineTables() {
		if _, err := sqlDB.ExecContext(ctx, "DROP TABLE IF EXISTS `"+table+"`"); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
	}
	if _, err := sqlDB.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 1"); err != nil {
		t.Fatal(err)
	}

	made, err := db.InitBaseline(ctx, sqlDB)
	if err != nil {
		t.Fatalf("InitBaseline: %v", err)
	}
	if made != len(db.BaselineTables()) {
		t.Errorf("made %d tables", made)
	}
	applied, err := db.AppliedLaravelMigrations(ctx, sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	if report := db.CompareMigrations(applied, db.ExpectedMigrations()); !report.OK() {
		t.Errorf("the check disagrees with the baseline: %+v", report)
	}

	// A database that has the tables is left alone.
	if _, err := db.InitBaseline(ctx, sqlDB); err == nil {
		t.Error("a database that already has the tables was initialised again")
	}
}

// Making the old values empty touches a row only while it holds the value that was converted: a value that was changed since is not destroyed, and the whole
// step is one transaction.
func TestSecretStoreNeutraliseKeepsWhatWasChanged(t *testing.T) {
	ctx := context.Background()
	sqlDB := openTestDB(t)
	if _, err := db.Migrate(ctx, sqlDB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx, "DELETE FROM raptor_secrets"); err != nil {
		t.Fatal(err)
	}
	legacyKey := bytes.Repeat([]byte{0x11}, 32)
	nodeA := legacycrypttest.Encrypt(legacyKey, "node-a")
	nodeB := legacycrypttest.Encrypt(legacyKey, "node-b")
	totp := legacycrypttest.Encrypt(legacyKey, "JBSWY3DPEHPK3PXP")
	smtp := legacycrypttest.Encrypt(legacyKey, "smtp-password")
	for _, stmt := range []string{
		"CREATE TEMPORARY TABLE nodes (id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY, daemon_token TEXT NOT NULL)",
		"CREATE TEMPORARY TABLE api_keys (id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY, token TEXT NOT NULL)",
		"CREATE TEMPORARY TABLE users (id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY, totp_secret TEXT NULL)",
		"CREATE TEMPORARY TABLE database_hosts (id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY, password TEXT NOT NULL)",
		"CREATE TEMPORARY TABLE `databases` (id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY, password TEXT NOT NULL)",
		"CREATE TEMPORARY TABLE settings (id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY, `key` VARCHAR(191) NOT NULL, `value` TEXT NOT NULL)",
		"INSERT INTO nodes (daemon_token) VALUES ('" + nodeA + "'), ('" + nodeB + "')",
		"INSERT INTO users (totp_secret) VALUES ('" + totp + "')",
		"INSERT INTO settings (`key`, `value`) VALUES ('settings::mail:mailers:smtp:password', '" + smtp + "'), ('settings::other', '" + smtp + "')",
	} {
		if _, err := sqlDB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	legacy, err := legacycrypt.New(legacyKey)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.NewBox(bytes.Repeat([]byte{0x77}, 32))
	if err != nil {
		t.Fatal(err)
	}
	store := db.NewSecretStore(sqlDB)
	if _, err := secretmigration.Run(ctx, store, legacy, box, secretmigration.Options{}); err != nil {
		t.Fatal(err)
	}

	// The converted copies are what SealedValues gives, and they open.
	sealed, err := store.SealedValues(ctx, secretmigration.Column{Table: "nodes", Column: "daemon_token"})
	if err != nil || len(sealed) != 2 {
		t.Fatalf("sealed = %v, %v", sealed, err)
	}
	if got, err := box.Open(sealed[2], secrets.Context("nodes", 2, "daemon_token")); err != nil || got != "node-b" {
		t.Errorf("node 2 opens to %q, %v", got, err)
	}

	hashOf := func(v string) string { sum := sha256.Sum256([]byte(v)); return hex.EncodeToString(sum[:]) }
	nodes := secretmigration.Column{Table: "nodes", Column: "daemon_token"}
	read := func(q string) (v sql.NullString) { sqlDB.QueryRowContext(ctx, q).Scan(&v); return }

	// Node 2 was changed after it was converted: the step stops, and node 1, which is first and fine, is not made empty either.
	if _, err := sqlDB.ExecContext(ctx, "UPDATE nodes SET daemon_token = 'something new' WHERE id = 2"); err != nil {
		t.Fatal(err)
	}
	err = store.Neutralise(ctx, []secretmigration.NeutraliseRow{{Column: nodes, ID: 1, SourceHash: hashOf(nodeA)}, {Column: nodes, ID: 2, SourceHash: hashOf(nodeB)}})
	if err == nil || !strings.Contains(err.Error(), "changed while this ran") {
		t.Fatalf("a row that changed: %v", err)
	}
	if v := read("SELECT daemon_token FROM nodes WHERE id = 1"); v.String != nodeA {
		t.Errorf("node 1 was made empty although the step failed: %q", v.String)
	}
	if v := read("SELECT daemon_token FROM nodes WHERE id = 2"); v.String != "something new" {
		t.Errorf("the changed value was touched: %q", v.String)
	}

	// With the right hashes, each column gets its neutral value: empty text, and NULL where the column allows it. Another setting is not touched.
	if _, err := sqlDB.ExecContext(ctx, "UPDATE nodes SET daemon_token = '"+nodeB+"' WHERE id = 2"); err != nil {
		t.Fatal(err)
	}
	rows := []secretmigration.NeutraliseRow{
		{Column: nodes, ID: 1, SourceHash: hashOf(nodeA)},
		{Column: nodes, ID: 2, SourceHash: hashOf(nodeB)},
		{Column: secretmigration.Column{Table: "users", Column: "totp_secret"}, ID: 1, SourceHash: hashOf(totp)},
		{Column: secretmigration.Column{Table: "settings", Column: "value", SettingsKey: "settings::mail:mailers:smtp:password"}, ID: 1, SourceHash: hashOf(smtp)},
	}
	if err := store.Neutralise(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if v := read("SELECT daemon_token FROM nodes WHERE id = 1"); !v.Valid || v.String != "" {
		t.Errorf("node 1 = %+v", v)
	}
	if v := read("SELECT totp_secret FROM users WHERE id = 1"); v.Valid {
		t.Errorf("the second factor secret = %+v, want NULL", v)
	}
	if v := read("SELECT `value` FROM settings WHERE id = 1"); !v.Valid || v.String != "" {
		t.Errorf("the mail password setting = %+v", v)
	}
	if v := read("SELECT `value` FROM settings WHERE id = 2"); v.String != smtp {
		t.Errorf("another setting was touched: %q", v.String)
	}
	// The converted copies are all still there.
	var left int
	sqlDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM raptor_secrets").Scan(&left)
	if left != 4 {
		t.Errorf("%d converted copies are left, want 4", left)
	}
	// A column with no statement is refused, not guessed at.
	if err := store.Neutralise(ctx, []secretmigration.NeutraliseRow{{Column: secretmigration.Column{Table: "users", Column: "password"}, ID: 1, SourceHash: "x"}}); err == nil {
		t.Error("a column nobody made a statement for was accepted")
	}
}
