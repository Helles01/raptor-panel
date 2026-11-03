//go:build integration

package main

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/db"
	"github.com/Helles01/raptor-panel/internal/secrets"
	"github.com/Helles01/raptor-panel/internal/store"
)

const e2eEgg = `{"meta": {"version": "PTDL_v2"}, "name": "Paper", "author": "p@example.com", "description": "d", "features": [],
 "docker_images": {"Java 21": "ghcr.io/example/yolks:java_21"}, "file_denylist": [], "startup": "java -jar {{JAR}}",
 "config": {"files": "{}", "startup": "{}", "logs": "{}", "stop": "stop"},
 "scripts": {"installation": {"script": "echo", "container": "alpine:3.4", "entrypoint": "ash"}},
 "variables": [{"name": "Jar", "description": "", "env_variable": "JAR", "default_value": "server.jar", "user_viewable": true, "user_editable": true, "rules": "required|string"}]}`

// The commands the administrator types, from `raptor user create` to `raptor apikey revoke`, over the test database, the way a first installation
// goes. Everything they say is checked, and so is what they leave in the database.
func TestAdminCommandsEndToEnd(t *testing.T) {
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
		"RAPTOR_SECRET_KEY": key, "RAPTOR_APP_URL": "https://panel.example.com",
	}

	ctx := context.Background()
	sqlDB, err := sql.Open("mysql", dsn+"&loc=UTC")
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if _, err := db.Migrate(ctx, sqlDB); err != nil {
		t.Fatal(err)
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0")
	for _, table := range []string{"users", "api_keys", "locations", "nodes", "nests", "eggs", "egg_variables", "settings", "database_hosts", "raptor_secrets", "recovery_tokens", "servers", "subusers"} {
		if _, err := conn.ExecContext(ctx, "TRUNCATE TABLE `"+table+"`"); err != nil {
			t.Fatalf("truncate %s: %v", table, err)
		}
	}
	conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 1")
	conn.Close()

	do := func(stdin string, args ...string) (stdout, stderr string) {
		t.Helper()
		code, out, errOut := runCmd(t, stdin, vars, args...)
		if code != 0 {
			t.Fatalf("raptor %s = %d\n%s", strings.Join(args, " "), code, errOut)
		}
		return out, errOut
	}

	// 1. the first user, an administrator, with a password that is made and shown once
	out, errOut := do("", "user", "create", "--email", "admin@example.com", "--username", "admin", "--first-name", "Ada", "--last-name", "Admin", "--admin")
	password := strings.TrimSpace(out)
	if len(password) < 16 || strings.Contains(errOut, password) {
		t.Fatalf("password on stdout %q, stderr %q", out, errOut)
	}
	out, _ = do("", "user", "list")
	if !strings.Contains(out, "admin@example.com") || !strings.Contains(out, "yes") {
		t.Errorf("user list:\n%s", out)
	}

	// 2. an application key for it; the whole key is on the standard output, once
	out, _ = do("", "apikey", "create", "--user", "admin", "--application", "--description", "bootstrap", "--access", "servers=rw", "--access", "nodes=rw", "--allow-ip", "10.0.0.0/8")
	token := strings.TrimSpace(out)
	ident, secret, ok := auth.SplitKey(token)
	if !ok {
		t.Fatalf("token %q", out)
	}
	box, err := secrets.NewBox(mustKey(t, key))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.New(sqlDB, box)
	if err != nil {
		t.Fatal(err)
	}
	k, err := st.AuthzKeys().ByIdentifier(ctx, ident)
	if err != nil || k.SecretHash != auth.HashKeySecret(secret) || k.Access["servers"] != auth.AccessReadWrite || len(k.AllowedIPs) != 1 {
		t.Fatalf("the authorizer's view of the key: %+v, %v", k, err)
	}
	out, _ = do("", "apikey", "list", "--user", "admin")
	if !strings.Contains(out, ident) || !strings.Contains(out, "servers=rw nodes=rw") || strings.Contains(out, secret) {
		t.Errorf("apikey list:\n%s", out)
	}

	// 3. a location and a node; the configuration for the node holds the token the authorizer accepts
	do("", "location", "create", "--short", "eu.ams", "--long", "Amsterdam")
	out, _ = do("", "node", "create", "--name", "Node 1", "--location-id", "1", "--fqdn", "node1.example.com", "--memory", "4096", "--disk", "50000")
	var tokenID string
	sqlDB.QueryRow("SELECT daemon_token_id FROM nodes WHERE id = 1").Scan(&tokenID)
	creds, err := st.AuthzNodes().ByTokenID(ctx, tokenID)
	if err != nil || creds.Token == "" || !strings.Contains(out, creds.Token) || !strings.Contains(out, tokenID) {
		t.Fatalf("the node's configuration and the authorizer disagree: %v\n%s", err, out)
	}
	out, _ = do("", "node", "list")
	if !strings.Contains(out, "node1.example.com") || !strings.Contains(out, "eu.ams") && !strings.Contains(out, "Node 1") {
		t.Errorf("node list:\n%s", out)
	}
	out, _ = do("", "location", "list")
	if !strings.Contains(out, "eu.ams") || !strings.Contains(out, "Amsterdam") {
		t.Errorf("location list:\n%s", out)
	}

	// 4. a nest and an egg, read from the standard input
	do("", "nest", "create", "--name", "Minecraft", "--author", "p@example.com")
	_, errOut = do(e2eEgg, "egg", "import", "--nest-id", "1", "-")
	if !strings.Contains(errOut, "Imported the egg Paper") || !strings.Contains(errOut, "1 variable") {
		t.Errorf("egg import: %q", errOut)
	}

	// 5. a setting, and the page that shows it
	do("", "settings", "set", "app:name", "My Panel")
	out, _ = do("", "settings", "get", "app:name")
	if strings.TrimSpace(out) != "My Panel" {
		t.Errorf("settings get = %q", out)
	}
	if site, err := st.PageSettings().Site(ctx); err != nil || site.Name != "My Panel" {
		t.Errorf("the page does not see the setting: %+v, %v", site, err)
	}

	// 6. a database host: the panel signs in to a real MariaDB with the account it is given (the test database itself)
	do(cfg.Passwd+"\n", "dbhost", "create", "--name", "local", "--host", host, "--port", port, "--username", cfg.User, "--password-stdin")
	out, _ = do("", "dbhost", "list")
	if !strings.Contains(out, "local") || !strings.Contains(out, host+":"+port) {
		t.Errorf("dbhost list:\n%s", out)
	}
	_, errOut = do("", "dbhost", "test", "1")
	if !strings.Contains(errOut, "signed in") {
		t.Errorf("dbhost test: %q", errOut)
	}
	// A wrong password is refused at the door and nothing is saved.
	code, _, errOut := runCmd(t, "wrong-password\n", vars, "dbhost", "create", "--name", "bad", "--host", host, "--port", port, "--username", cfg.User, "--password-stdin")
	if code == 0 || strings.Contains(errOut, "wrong-password") || !strings.Contains(errOut, "1045") {
		t.Errorf("a wrong password: code %d, %q", code, errOut)
	}
	var hosts int
	sqlDB.QueryRow("SELECT COUNT(*) FROM database_hosts").Scan(&hosts)
	if hosts != 1 {
		t.Errorf("%d hosts", hosts)
	}
	do("", "dbhost", "delete", "1")

	// 7. revoking the key, and the rest of the housekeeping
	_, errOut = do("", "apikey", "revoke", ident)
	if !strings.Contains(errOut, "Revoked the key "+ident) {
		t.Errorf("revoke: %q", errOut)
	}
	if _, err := st.AuthzKeys().ByIdentifier(ctx, ident); err != authz.ErrNoKey {
		t.Errorf("a revoked key: %v", err)
	}
	do("", "user", "disable-2fa", "admin")
	do("", "user", "delete", "admin")
	out, _ = do("", "user", "list")
	if strings.Contains(out, "admin@example.com") {
		t.Errorf("the user is still listed:\n%s", out)
	}
}

func mustKey(t *testing.T, s string) []byte {
	t.Helper()
	b, err := secrets.ParseKey(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
