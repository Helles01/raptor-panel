package db

import (
	"bufio"
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"sort"
	"strings"
)

// expectedMigrationsFile lists the Laravel migrations of the reference panel version Raptor is written against.
//
//go:embed expected_migrations.txt
var expectedMigrationsFile string

// ExpectedMigrations returns the Laravel migration names a database must have applied, in order.
func ExpectedMigrations() []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(expectedMigrationsFile))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// MigrationReport is the difference between the migrations a database has applied and the expected ones.
type MigrationReport struct {
	// Missing are expected migrations the database has not applied: its schema is older than Raptor expects.
	Missing []string
	// Unknown are applied migrations Raptor does not know: its schema is newer, or changed by something else.
	Unknown []string
}

// OK reports whether the database has exactly the expected migrations.
func (r MigrationReport) OK() bool { return len(r.Missing) == 0 && len(r.Unknown) == 0 }

// CompareMigrations compares the applied migration names with the expected ones.
func CompareMigrations(applied, expected []string) MigrationReport {
	have := make(map[string]bool, len(applied))
	for _, name := range applied {
		have[name] = true
	}
	want := make(map[string]bool, len(expected))
	for _, name := range expected {
		want[name] = true
	}

	var r MigrationReport
	for name := range want {
		if !have[name] {
			r.Missing = append(r.Missing, name)
		}
	}
	for name := range have {
		if !want[name] {
			r.Unknown = append(r.Unknown, name)
		}
	}
	sort.Strings(r.Missing)
	sort.Strings(r.Unknown)
	return r
}

// AppliedLaravelMigrations reads the names in the reference panel's `migrations` table. It only reads.
func AppliedLaravelMigrations(ctx context.Context, sqlDB *sql.DB) ([]string, error) {
	rows, err := sqlDB.QueryContext(ctx, "SELECT migration FROM migrations ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("read the migrations table: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("read migration name: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the migrations table: %w", err)
	}
	return names, nil
}
