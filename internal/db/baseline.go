package db

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// baselineFile is the structure of the reference panel's tables (internal/db/schema/0000_baseline.sql), the same file sqlc reads.
//
//go:embed schema/0000_baseline.sql
var baselineFile string

var baselineTable = regexp.MustCompile("^CREATE TABLE `([a-z_]+)` \\(")

// baselineStatement is one statement of the baseline and the table it makes.
type baselineStatement struct {
	Table string
	SQL   string
}

// baselineStatements splits the baseline into its CREATE TABLE statements. The driver runs one statement per call, so each is checked to hold
// exactly one: the ";" that ends it is the only one.
func baselineStatements(file string) ([]baselineStatement, error) {
	var out []baselineStatement
	var cur []string
	table := ""
	for _, line := range strings.Split(file, "\n") {
		if table == "" {
			if m := baselineTable.FindStringSubmatch(line); m != nil {
				table, cur = m[1], []string{line}
			}
			continue
		}
		cur = append(cur, line)
		if strings.HasSuffix(line, ";") {
			stmt := strings.TrimSuffix(strings.Join(cur, "\n"), ";")
			if strings.Contains(stmt, ";") {
				return nil, fmt.Errorf("the baseline statement for %s holds more than one statement", table)
			}
			out = append(out, baselineStatement{Table: table, SQL: stmt})
			table, cur = "", nil
		}
	}
	if table != "" {
		return nil, fmt.Errorf("the baseline statement for %s does not end", table)
	}
	if len(out) == 0 {
		return nil, errors.New("the baseline holds no tables")
	}
	return out, nil
}

// BaselineTables names the tables of the baseline, in file order.
func BaselineTables() []string {
	stmts, err := baselineStatements(baselineFile)
	if err != nil {
		return nil
	}
	names := make([]string, len(stmts))
	for i, s := range stmts {
		names[i] = s.Table
	}
	return names
}

// InitBaseline makes the tables of the reference panel in an EMPTY database, and writes the migrations the reference panel's own `migrations`
// table would hold, so that `raptor db check` agrees and a database made this way is the one the old panel would have made. It is for a clean
// install; a database that has any of the tables is refused and left alone. It returns how many tables it made.
func InitBaseline(ctx context.Context, sqlDB *sql.DB) (int, error) {
	stmts, err := baselineStatements(baselineFile)
	if err != nil {
		return 0, err
	}

	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("get database connection: %w", err)
	}
	defer conn.Close()

	var locked sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 30)", migrationLock).Scan(&locked); err != nil {
		return 0, fmt.Errorf("take migration lock: %w", err)
	}
	if !locked.Valid || locked.Int64 != 1 {
		return 0, fmt.Errorf("another process is running migrations (lock %q is held)", migrationLock)
	}
	defer func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), "SELECT RELEASE_LOCK(?)", migrationLock)
	}()

	rows, err := conn.QueryContext(ctx, "SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE()")
	if err != nil {
		return 0, fmt.Errorf("list the tables of the database: %w", err)
	}
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return 0, fmt.Errorf("read a table name: %w", err)
		}
		have[strings.ToLower(name)] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("list the tables of the database: %w", err)
	}
	rows.Close()
	var clash []string
	for _, s := range stmts {
		if have[s.Table] {
			clash = append(clash, s.Table)
		}
	}
	if len(clash) > 0 {
		return 0, fmt.Errorf("the database already has tables of the panel (%s): it is not empty, so nothing was made; use `raptor db check` for a database that is in use", strings.Join(clash, ", "))
	}

	// The tables refer to each other in file order, not in the order of their references, so the checks are off while they are made.
	if _, err := conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		return 0, fmt.Errorf("switch foreign key checks off: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(context.WithoutCancel(ctx), "SET FOREIGN_KEY_CHECKS = 1") }()

	made := 0
	for _, s := range stmts {
		if _, err := conn.ExecContext(ctx, s.SQL); err != nil {
			return made, fmt.Errorf("make table %s (%d of %d made; drop them before trying again): %w", s.Table, made, len(stmts), err)
		}
		made++
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return made, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	for _, name := range ExpectedMigrations() {
		if _, err := tx.ExecContext(ctx, "INSERT INTO migrations (migration, batch) VALUES (?, 1)", name); err != nil {
			return made, fmt.Errorf("record migration %s: %w", name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return made, fmt.Errorf("record the migrations: %w", err)
	}
	return made, nil
}
