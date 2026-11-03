package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/go-sql-driver/mysql"
)

// PendingMigrations lists the Raptor migrations that are not applied yet, in the order they apply, without applying anything. A database that
// has never been migrated has no table that says what was applied, and so everything is pending.
func PendingMigrations(ctx context.Context, sqlDB *sql.DB) ([]string, error) {
	all, err := loadMigrations(migrationFiles)
	if err != nil {
		return nil, err
	}
	applied := map[string]bool{}
	rows, err := sqlDB.QueryContext(ctx, "SELECT name FROM raptor_migrations")
	var me *mysql.MySQLError
	switch {
	case errors.As(err, &me) && me.Number == 1146: // ER_NO_SUCH_TABLE
	case err != nil:
		return nil, fmt.Errorf("list applied migrations: %w", err)
	default:
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return nil, fmt.Errorf("read applied migration: %w", err)
			}
			applied[name] = true
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("list applied migrations: %w", err)
		}
	}
	var names []string
	for _, m := range pendingMigrations(all, applied) {
		names = append(names, m.Name)
	}
	return names, nil
}
