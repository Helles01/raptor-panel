package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Helles01/raptor-panel/internal/db/dbq"
	"github.com/Helles01/raptor-panel/internal/secretmigration"
)

// SecretStore implements secretmigration.Store on MariaDB/MySQL. The old Laravel columns are read with fixed
// statements that take no caller supplied text; the only table written is raptor_secrets.
type SecretStore struct {
	db *sql.DB
	q  *dbq.Queries
}

// NewSecretStore returns a SecretStore on an open database.
func NewSecretStore(sqlDB *sql.DB) *SecretStore {
	return &SecretStore{db: sqlDB, q: dbq.New(sqlDB)}
}

var _ secretmigration.Store = (*SecretStore)(nil)

// legacyQuery returns the fixed read statement of an old column. The settings statement takes its key as parameter.
func legacyQuery(c secretmigration.Column) (query string, args []any, ok bool) {
	switch c.Table + "." + c.Column {
	case "nodes.daemon_token":
		return "SELECT id, daemon_token FROM nodes WHERE daemon_token IS NOT NULL AND daemon_token <> '' ORDER BY id", nil, true
	case "api_keys.token":
		return "SELECT id, token FROM api_keys WHERE token IS NOT NULL AND token <> '' ORDER BY id", nil, true
	case "users.totp_secret":
		return "SELECT id, totp_secret FROM users WHERE totp_secret IS NOT NULL AND totp_secret <> '' ORDER BY id", nil, true
	case "database_hosts.password":
		return "SELECT id, password FROM database_hosts WHERE password IS NOT NULL AND password <> '' ORDER BY id", nil, true
	case "databases.password":
		return "SELECT id, password FROM `databases` WHERE password IS NOT NULL AND password <> '' ORDER BY id", nil, true
	case "settings.value":
		if c.SettingsKey == "" {
			return "", nil, false
		}
		return "SELECT id, `value` FROM settings WHERE `key` = ? AND `value` IS NOT NULL AND `value` <> '' ORDER BY id", []any{c.SettingsKey}, true
	}
	return "", nil, false
}

// LegacyValues implements secretmigration.Store.
func (s *SecretStore) LegacyValues(ctx context.Context, c secretmigration.Column) ([]secretmigration.Row, error) {
	query, args, ok := legacyQuery(c)
	if !ok {
		return nil, fmt.Errorf("no read statement for %s", c)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []secretmigration.Row
	for rows.Next() {
		var r secretmigration.Row
		if err := rows.Scan(&r.ID, &r.Value); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SourceHashes implements secretmigration.Store.
func (s *SecretStore) SourceHashes(ctx context.Context, c secretmigration.Column) (map[uint64]string, error) {
	rows, err := s.q.ListSecretHashes(ctx, dbq.ListSecretHashesParams{TableName: c.Table, ColumnName: c.Column})
	if err != nil {
		return nil, err
	}
	out := make(map[uint64]string, len(rows))
	for _, r := range rows {
		out[r.RowID] = r.SourceHash.String
	}
	return out, nil
}

// WriteSecrets implements secretmigration.Store. All writes happen in one transaction.
func (s *SecretStore) WriteSecrets(ctx context.Context, writes []secretmigration.Write) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	q := s.q.WithTx(tx)
	for _, w := range writes {
		err := q.UpsertSecret(ctx, dbq.UpsertSecretParams{
			TableName:  w.Column.Table,
			RowID:      w.RowID,
			ColumnName: w.Column.Column,
			Ciphertext: w.Sealed,
			SourceHash: sql.NullString{String: w.SourceHash, Valid: true},
		})
		if err != nil {
			return fmt.Errorf("write %s id=%d: %w", w.Column, w.RowID, err)
		}
	}
	return tx.Commit()
}

var _ secretmigration.FinalStore = (*SecretStore)(nil)

// SealedValues implements secretmigration.FinalStore.
func (s *SecretStore) SealedValues(ctx context.Context, c secretmigration.Column) (map[uint64]string, error) {
	rows, err := s.q.ListSealedSecrets(ctx, dbq.ListSealedSecretsParams{TableName: c.Table, ColumnName: c.Column})
	if err != nil {
		return nil, err
	}
	out := make(map[uint64]string, len(rows))
	for _, r := range rows {
		out[r.RowID] = r.Ciphertext
	}
	return out, nil
}

// neutraliseQuery returns the fixed statement that makes the old value of a row empty, only while the row holds the value the hash was made of (SHA2 of the
// stored text is the hex SHA-256 the conversion recorded). The column that may be NULL is made NULL; the others are made empty, which is what
// LegacyValues leaves out.
func neutraliseQuery(c secretmigration.Column) (query string, extra []any, ok bool) {
	switch c.Table + "." + c.Column {
	case "nodes.daemon_token":
		return "UPDATE nodes SET daemon_token = '' WHERE id = ? AND SHA2(daemon_token, 256) = ?", nil, true
	case "api_keys.token":
		return "UPDATE api_keys SET token = '' WHERE id = ? AND SHA2(token, 256) = ?", nil, true
	case "users.totp_secret":
		return "UPDATE users SET totp_secret = NULL WHERE id = ? AND SHA2(totp_secret, 256) = ?", nil, true
	case "database_hosts.password":
		return "UPDATE database_hosts SET password = '' WHERE id = ? AND SHA2(password, 256) = ?", nil, true
	case "databases.password":
		return "UPDATE `databases` SET password = '' WHERE id = ? AND SHA2(password, 256) = ?", nil, true
	case "settings.value":
		if c.SettingsKey == "" {
			return "", nil, false
		}
		return "UPDATE settings SET `value` = '' WHERE id = ? AND `key` = ? AND SHA2(`value`, 256) = ?", []any{c.SettingsKey}, true
	}
	return "", nil, false
}

// Neutralise implements secretmigration.FinalStore: every row in one transaction, and a row that no longer holds the converted value stops it all.
func (s *SecretStore) Neutralise(ctx context.Context, rows []secretmigration.NeutraliseRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()
	for _, r := range rows {
		query, extra, ok := neutraliseQuery(r.Column)
		if !ok {
			return fmt.Errorf("no statement for %s", r.Column)
		}
		args := []any{r.ID}
		args = append(args, extra...)
		args = append(args, r.SourceHash)
		res, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("make %s id=%d empty: %w", r.Column, r.ID, err)
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return fmt.Errorf("%s id=%d changed while this ran: run migrate-secrets again and then this", r.Column, r.ID)
		}
	}
	return tx.Commit()
}
