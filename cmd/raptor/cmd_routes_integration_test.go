//go:build integration

package main

import (
	"bufio"
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/Helles01/raptor-panel/internal/db"
	"github.com/Helles01/raptor-panel/internal/secrets"
)

// The panel that is built from the real stores serves exactly the routes of the reference, plus the ones Raptor adds on purpose (the same check as
// internal/api/inventory, which makes every package with nothing behind it, made over the whole of the wiring).
func TestBuiltPanelServesTheReferencesRoutes(t *testing.T) {
	dsn := os.Getenv("RAPTOR_TEST_DSN")
	if dsn == "" {
		t.Skip("RAPTOR_TEST_DSN is not set")
	}
	assets, err := filepath.Abs("../../web/public/assets")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(assets, "manifest.json")); err != nil {
		t.Skip("the frontend is not built (web/public/assets)")
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
		"RAPTOR_SECRET_KEY": key, "RAPTOR_APP_URL": "https://panel.example.com", "RAPTOR_ASSETS_DIR": assets,
	}
	ctx := context.Background()
	if err := migrateTestDB(ctx, dsn); err != nil {
		t.Fatal(err)
	}

	e := &env{stdin: strings.NewReader(""), stdout: io.Discard, stderr: io.Discard, getenv: func(k string) string { return vars[k] }}
	r, err := openPanel(ctx, e, "")
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()

	param := regexp.MustCompile(`:[A-Za-z_]+`)
	got := map[string]bool{}
	for _, route := range r.panel.Routes {
		got[route.Method+" "+param.ReplaceAllString(route.Path, "{}")] = true
	}

	f, err := os.Open("../../internal/api/inventory/testdata/reference-routes.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	want := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" && !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "MISSING ") {
			want[line] = true
		}
	}
	additions := map[string]bool{"GET /healthz": true, "GET /readyz": true, "GET /assets/*": true}

	var missing, extra []string
	for s := range want {
		if !got[s] {
			missing = append(missing, s)
		}
	}
	for s := range got {
		if !want[s] && !additions[s] {
			extra = append(extra, s)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("the built panel does not serve the routes of the reference:\nmissing:\n  %s\nextra:\n  %s", strings.Join(missing, "\n  "), strings.Join(extra, "\n  "))
	}
	if len(want) < 100 || len(got) < len(want) {
		t.Errorf("%d routes wanted, %d served", len(want), len(got))
	}
}

// migrateTestDB applies Raptor's own migrations, which a panel needs before it starts.
func migrateTestDB(ctx context.Context, dsn string) error {
	sqlDB, err := sql.Open("mysql", dsn+"&loc=UTC")
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	_, err = db.Migrate(ctx, sqlDB)
	return err
}
