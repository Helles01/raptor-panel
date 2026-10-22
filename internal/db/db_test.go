package db

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Helles01/raptor-panel/internal/config"
	"github.com/Helles01/raptor-panel/internal/secretmigration"
)

func TestDriverConfig(t *testing.T) {
	tests := []struct {
		name     string
		cfg      config.DB
		wantAddr string
		wantTLS  string
	}{
		{"ipv4", config.DB{Host: "10.0.0.5", Port: 3307, Name: "panel", User: "raptor", Password: "pw", TLS: "false"}, "10.0.0.5:3307", "false"},
		{"hostname", config.DB{Host: "db.internal", Port: 3306, Name: "panel", User: "raptor", TLS: "preferred"}, "db.internal:3306", "preferred"},
		{"ipv6 is bracketed", config.DB{Host: "::1", Port: 3306, Name: "panel", User: "raptor", TLS: "true"}, "[::1]:3306", "true"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := driverConfig(tt.cfg)
			if c.Addr != tt.wantAddr || c.TLSConfig != tt.wantTLS {
				t.Errorf("Addr/TLSConfig = %q/%q, want %q/%q", c.Addr, c.TLSConfig, tt.wantAddr, tt.wantTLS)
			}
			if c.User != tt.cfg.User || c.Passwd != tt.cfg.Password || c.DBName != tt.cfg.Name {
				t.Errorf("credentials or database name not mapped: %+v", c)
			}
			if !c.ParseTime || c.Loc.String() != "UTC" {
				t.Errorf("times must be parsed as UTC, got ParseTime=%v Loc=%v", c.ParseTime, c.Loc)
			}
			if c.MultiStatements || c.AllowAllFiles || c.AllowCleartextPasswords {
				t.Errorf("unsafe driver options enabled: %+v", c)
			}
		})
	}
}

func TestLoadMigrations(t *testing.T) {
	file := func(content string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(content)} }

	tests := []struct {
		name    string
		files   fstest.MapFS
		want    []string
		wantErr string
	}{
		{
			name: "sorted by name, trailing semicolon removed",
			files: fstest.MapFS{
				"migrations/0002_second.sql": file("CREATE TABLE b (id INT);\n"),
				"migrations/0001_first.sql":  file("CREATE TABLE a (id INT)"),
			},
			want: []string{"0001_first", "0002_second"},
		},
		{
			name:    "two statements",
			files:   fstest.MapFS{"migrations/0001_x.sql": file("CREATE TABLE a (id INT); CREATE TABLE b (id INT);")},
			wantErr: "exactly one statement",
		},
		{
			name:    "empty",
			files:   fstest.MapFS{"migrations/0001_x.sql": file(" \n;")},
			wantErr: "is empty",
		},
		{
			name:    "bad file name",
			files:   fstest.MapFS{"migrations/create_table.sql": file("CREATE TABLE a (id INT)")},
			wantErr: "must be named",
		},
		{
			name: "duplicate number",
			files: fstest.MapFS{
				"migrations/0001_a.sql": file("CREATE TABLE a (id INT)"),
				"migrations/0001_b.sql": file("CREATE TABLE b (id INT)"),
			},
			wantErr: "share the number",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := loadMigrations(tt.files)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("loadMigrations error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("loadMigrations: %v", err)
			}
			var names []string
			for _, m := range got {
				names = append(names, m.Name)
				if strings.Contains(m.Statement, ";") {
					t.Errorf("statement of %s still contains a semicolon: %q", m.Name, m.Statement)
				}
			}
			if !reflect.DeepEqual(names, tt.want) {
				t.Errorf("names = %v, want %v", names, tt.want)
			}
		})
	}
}

func TestEmbeddedMigrationsAreValid(t *testing.T) {
	ms, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatalf("the embedded migrations are invalid: %v", err)
	}
	if len(ms) == 0 || ms[0].Name != "0001_raptor_secrets" {
		t.Errorf("first embedded migration = %+v", ms)
	}
	for _, m := range ms {
		if !strings.Contains(strings.ToLower(m.Statement), "raptor_") {
			t.Errorf("migration %s does not touch a raptor_ table; Raptor only adds raptor_ tables (docs/compat.md C-006)", m.Name)
		}
	}
}

func TestPendingMigrations(t *testing.T) {
	all := []migration{{Name: "0001_a"}, {Name: "0002_b"}, {Name: "0003_c"}}

	got := pendingMigrations(all, map[string]bool{"0001_a": true, "0003_c": true})
	if len(got) != 1 || got[0].Name != "0002_b" {
		t.Errorf("pending = %+v, want only 0002_b", got)
	}
	if got := pendingMigrations(all, map[string]bool{"0001_a": true, "0002_b": true, "0003_c": true}); len(got) != 0 {
		t.Errorf("pending = %+v, want none", got)
	}
	if got := pendingMigrations(all, nil); len(got) != 3 {
		t.Errorf("pending = %+v, want all three", got)
	}
}

func TestExpectedMigrations(t *testing.T) {
	got := ExpectedMigrations()

	if len(got) != 195 {
		t.Errorf("expected %d migrations, want 195 (reference v1.15.2)", len(got))
	}
	if !sort.StringsAreSorted(got) {
		t.Error("expected migrations are not in name order")
	}
	if last := got[len(got)-1]; last != "2024_07_13_091852_clear_unused_allocation_notes" {
		t.Errorf("last migration = %q", last)
	}
	seen := map[string]bool{}
	for _, name := range got {
		if seen[name] || strings.ContainsAny(name, " #\t") || strings.HasSuffix(name, ".php") {
			t.Errorf("bad migration name %q", name)
		}
		seen[name] = true
	}
}

func TestCompareMigrations(t *testing.T) {
	expected := []string{"a", "b", "c"}

	tests := []struct {
		name        string
		applied     []string
		wantMissing []string
		wantUnknown []string
	}{
		{"identical", []string{"a", "b", "c"}, nil, nil},
		{"identical in another order", []string{"c", "a", "b"}, nil, nil},
		{"older schema", []string{"a"}, []string{"b", "c"}, nil},
		{"newer schema", []string{"a", "b", "c", "z", "y"}, nil, []string{"y", "z"}},
		{"both", []string{"a", "x"}, []string{"b", "c"}, []string{"x"}},
		{"empty database", nil, []string{"a", "b", "c"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := CompareMigrations(tt.applied, expected)
			if !reflect.DeepEqual(r.Missing, tt.wantMissing) || !reflect.DeepEqual(r.Unknown, tt.wantUnknown) {
				t.Errorf("missing/unknown = %v/%v, want %v/%v", r.Missing, r.Unknown, tt.wantMissing, tt.wantUnknown)
			}
			if r.OK() != (tt.wantMissing == nil && tt.wantUnknown == nil) {
				t.Errorf("OK() = %v", r.OK())
			}
		})
	}
}

func TestLegacyQueries(t *testing.T) {
	for _, c := range secretmigration.Columns() {
		query, args, ok := legacyQuery(c)
		if !ok {
			t.Errorf("no read statement for %s", c)
			continue
		}
		if !strings.HasPrefix(query, "SELECT ") {
			t.Errorf("statement for %s is not a SELECT: %s", c, query)
		}
		// Statements are fixed text: the only caller supplied value, the settings key, goes in as a parameter.
		if c.SettingsKey != "" {
			if strings.Contains(query, c.SettingsKey) || len(args) != 1 || args[0] != c.SettingsKey {
				t.Errorf("the settings key must be passed as the only parameter, got query %q args %v", query, args)
			}
		} else if len(args) != 0 || strings.Contains(query, "?") {
			t.Errorf("unexpected parameters for %s: %q %v", c, query, args)
		}
	}

	unknown := []secretmigration.Column{
		{Table: "users", Column: "password"},
		{Table: "settings", Column: "value"},
		{Table: "nodes; DROP TABLE nodes", Column: "daemon_token"},
	}
	for _, c := range unknown {
		if _, _, ok := legacyQuery(c); ok {
			t.Errorf("legacyQuery accepted %+v", c)
		}
	}
}
