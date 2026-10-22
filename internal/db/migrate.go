package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
)

// Raptor's own tables are created by additive migrations, tracked in raptor_migrations. The Laravel `migrations`
// table and every table of the reference panel are never touched.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

const migrationLock = "raptor_migrations"

var migrationName = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

type migration struct {
	Name      string
	Statement string
}

// loadMigrations reads migrations/*.sql in name order. Every file holds exactly one statement (the driver runs one
// statement per call), so a ";" may only appear as the final character.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}

	var out []migration
	seen := map[string]string{}
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migration file %q must be named NNNN_name.sql", e.Name())
		}
		if other, dup := seen[m[1]]; dup {
			return nil, fmt.Errorf("migration files %q and %q share the number %s", other, e.Name(), m[1])
		}
		seen[m[1]] = e.Name()

		raw, err := fs.ReadFile(fsys, "migrations/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", e.Name(), err)
		}
		stmt := strings.TrimSuffix(strings.TrimSpace(string(raw)), ";")
		if stmt == "" {
			return nil, fmt.Errorf("migration %s is empty", e.Name())
		}
		if strings.Contains(stmt, ";") {
			return nil, fmt.Errorf("migration %s must contain exactly one statement", e.Name())
		}
		out = append(out, migration{Name: strings.TrimSuffix(e.Name(), ".sql"), Statement: stmt})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func pendingMigrations(all []migration, applied map[string]bool) []migration {
	var out []migration
	for _, m := range all {
		if !applied[m.Name] {
			out = append(out, m)
		}
	}
	return out
}

// Migrate applies the Raptor migrations that are not applied yet and returns their names. A database lock keeps two
// processes from migrating at the same time.
func Migrate(ctx context.Context, sqlDB *sql.DB) ([]string, error) {
	all, err := loadMigrations(migrationFiles)
	if err != nil {
		return nil, err
	}

	// GET_LOCK belongs to one connection, so the whole run uses a single one.
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("get database connection: %w", err)
	}
	defer conn.Close()

	var locked sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 30)", migrationLock).Scan(&locked); err != nil {
		return nil, fmt.Errorf("take migration lock: %w", err)
	}
	if !locked.Valid || locked.Int64 != 1 {
		return nil, fmt.Errorf("another process is running migrations (lock %q is held)", migrationLock)
	}
	defer func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), "SELECT RELEASE_LOCK(?)", migrationLock)
	}()

	const createTracking = `CREATE TABLE IF NOT EXISTS raptor_migrations (
		name       VARCHAR(191) NOT NULL,
		applied_at TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (name)
	) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci`
	if _, err := conn.ExecContext(ctx, createTracking); err != nil {
		return nil, fmt.Errorf("create raptor_migrations: %w", err)
	}

	rows, err := conn.QueryContext(ctx, "SELECT name FROM raptor_migrations")
	if err != nil {
		return nil, fmt.Errorf("list applied migrations: %w", err)
	}
	applied := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read applied migration: %w", err)
		}
		applied[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("list applied migrations: %w", err)
	}
	rows.Close()

	var done []string
	for _, m := range pendingMigrations(all, applied) {
		// DDL commits implicitly in MySQL/MariaDB, so there is no transaction to wrap this in; the tracking row is
		// written right after the statement succeeded.
		if _, err := conn.ExecContext(ctx, m.Statement); err != nil {
			return done, fmt.Errorf("apply migration %s: %w", m.Name, err)
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO raptor_migrations (name) VALUES (?)", m.Name); err != nil {
			return done, fmt.Errorf("record migration %s: %w", m.Name, err)
		}
		done = append(done, m.Name)
	}
	return done, nil
}
