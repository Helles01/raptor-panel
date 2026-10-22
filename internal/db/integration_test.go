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
	"database/sql"
	"os"
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
	for _, table := range []string{"raptor_secrets", "raptor_migrations"} {
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
