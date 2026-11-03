package db

import (
	"strings"
	"testing"
)

func TestBaselineHoldsTheTablesOfThePanel(t *testing.T) {
	stmts, err := baselineStatements(baselineFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 35 {
		t.Errorf("%d tables, the reference's dump has 35", len(stmts))
	}
	seen := map[string]bool{}
	for _, s := range stmts {
		if seen[s.Table] {
			t.Errorf("%s is made twice", s.Table)
		}
		seen[s.Table] = true
		if !strings.HasPrefix(s.SQL, "CREATE TABLE `"+s.Table+"` (") || strings.Contains(s.SQL, ";") || !strings.Contains(s.SQL, "ENGINE=InnoDB") {
			t.Errorf("the statement for %s is %.60q ...", s.Table, s.SQL)
		}
	}
	for _, want := range []string{"users", "servers", "nodes", "allocations", "api_keys", "migrations", "sessions", "settings", "schedules", "tasks", "backups", "databases", "database_hosts", "subusers", "activity_logs", "eggs", "egg_variables", "nests", "locations"} {
		if !seen[want] {
			t.Errorf("the baseline has no table %s", want)
		}
	}
	if got := BaselineTables(); len(got) != 35 || got[0] != stmts[0].Table {
		t.Errorf("BaselineTables = %v", got)
	}
}

// The migrations table is made by the baseline and filled by InitBaseline; its columns must be the ones the insert uses.
func TestBaselineMigrationsTableHasTheColumnsInitBaselineWrites(t *testing.T) {
	stmts, err := baselineStatements(baselineFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range stmts {
		if s.Table != "migrations" {
			continue
		}
		for _, col := range []string{"`id`", "`migration`", "`batch`"} {
			if !strings.Contains(s.SQL, col) {
				t.Errorf("migrations has no column %s: %s", col, s.SQL)
			}
		}
		return
	}
	t.Error("no migrations table")
}

func TestBaselineStatementsRefuseWhatCannotBeRunOneAtATime(t *testing.T) {
	for name, file := range map[string]string{
		"nothing":    "-- only a comment\n",
		"two in one": "CREATE TABLE `a` (\n  `id` int\n); DROP TABLE `b`;\n",
		"no end":     "CREATE TABLE `a` (\n  `id` int\n",
	} {
		if _, err := baselineStatements(file); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	got, err := baselineStatements("-- c\nCREATE TABLE `a` (\n  `id` int\n) ENGINE=InnoDB;\n\nCREATE TABLE `b` (\n  `id` int\n) ENGINE=InnoDB;\n")
	if err != nil || len(got) != 2 || got[0].Table != "a" || got[1].Table != "b" || strings.HasSuffix(got[0].SQL, ";") {
		t.Errorf("got %+v, err %v", got, err)
	}
}

func TestEveryExpectedMigrationIsWrittenOnce(t *testing.T) {
	names := ExpectedMigrations()
	if len(names) != 195 {
		t.Errorf("%d migrations", len(names))
	}
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Errorf("%s twice", n)
		}
		seen[n] = true
	}
}
