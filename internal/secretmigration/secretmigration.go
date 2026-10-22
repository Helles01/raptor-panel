// Package secretmigration converts the reference panel's Laravel-encrypted columns into Raptor's own secret format.
//
// The conversion is one way and additive: the old columns are only read, the results are written next to them in the
// raptor_secrets table. Nothing in this package can modify an old column.
package secretmigration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/Helles01/raptor-panel/internal/legacycrypt"
	"github.com/Helles01/raptor-panel/internal/secrets"
)

// Column identifies one Laravel-encrypted column.
type Column struct {
	Table  string
	Column string
	// SettingsKey is set only for the key/value settings table: the key whose value is encrypted.
	SettingsKey string
}

func (c Column) String() string {
	if c.SettingsKey != "" {
		return c.Table + "." + c.Column + "[" + c.SettingsKey + "]"
	}
	return c.Table + "." + c.Column
}

// Columns lists every column the reference panel encrypts with Laravel's Crypt.
func Columns() []Column {
	return []Column{
		{Table: "nodes", Column: "daemon_token"},
		{Table: "api_keys", Column: "token"},
		{Table: "users", Column: "totp_secret"},
		{Table: "database_hosts", Column: "password"},
		{Table: "databases", Column: "password"},
		{Table: "settings", Column: "value", SettingsKey: "settings::mail:mailers:smtp:password"},
	}
}

// Row is one encrypted value of an old column.
type Row struct {
	ID    uint64
	Value string
}

// Write is one converted value for the raptor_secrets table.
type Write struct {
	Column     Column
	RowID      uint64
	Sealed     string
	SourceHash string
}

// Store gives read-only access to the old columns and write access to raptor_secrets only.
type Store interface {
	// LegacyValues returns the non-empty encrypted values of a column.
	LegacyValues(ctx context.Context, c Column) ([]Row, error)
	// SourceHashes returns, per row, the hash of the old value a conversion was made from.
	SourceHashes(ctx context.Context, c Column) (map[uint64]string, error)
	// WriteSecrets stores all writes atomically: either every one of them or none.
	WriteSecrets(ctx context.Context, writes []Write) error
}

// Options controls a run.
type Options struct {
	// DryRun decrypts and seals everything but writes nothing.
	DryRun bool
	// All converts every row again, instead of only rows that are new or changed.
	All bool
}

// Failure is a row that could not be decrypted. It never carries the value.
type Failure struct {
	Column Column
	RowID  uint64
}

// ColumnResult counts the rows of one column.
type ColumnResult struct {
	Column    Column
	Total     int
	Converted int // with DryRun: would be converted
	Unchanged int
}

// Report is the outcome of a run.
type Report struct {
	DryRun   bool
	Columns  []ColumnResult
	Failures []Failure
}

// ErrFailures is returned, with nothing written, when at least one value could not be decrypted.
var ErrFailures = errors.New("some values could not be decrypted")

// Run converts all columns. If any value cannot be decrypted nothing is written and ErrFailures is returned, so a
// wrong APP_KEY can never leave a half converted state.
func Run(ctx context.Context, store Store, legacy *legacycrypt.Decrypter, box *secrets.Box, opts Options) (Report, error) {
	report := Report{DryRun: opts.DryRun}
	var writes []Write

	for _, col := range Columns() {
		rows, err := store.LegacyValues(ctx, col)
		if err != nil {
			return report, fmt.Errorf("read %s: %w", col, err)
		}
		hashes, err := store.SourceHashes(ctx, col)
		if err != nil {
			return report, fmt.Errorf("read converted hashes of %s: %w", col, err)
		}

		result := ColumnResult{Column: col, Total: len(rows)}
		for _, row := range rows {
			hash := sourceHash(row.Value)
			if !opts.All && hashes[row.ID] == hash {
				result.Unchanged++
				continue
			}

			plaintext, err := legacy.Decrypt(row.Value)
			if err != nil {
				report.Failures = append(report.Failures, Failure{Column: col, RowID: row.ID})
				continue
			}
			sealed, err := box.Seal(plaintext, secrets.Context(col.Table, row.ID, col.Column))
			if err != nil {
				return report, fmt.Errorf("seal %s id=%d: %w", col, row.ID, err)
			}

			result.Converted++
			writes = append(writes, Write{Column: col, RowID: row.ID, Sealed: sealed, SourceHash: hash})
		}
		report.Columns = append(report.Columns, result)
	}

	if len(report.Failures) > 0 {
		return report, ErrFailures
	}
	if opts.DryRun || len(writes) == 0 {
		return report, nil
	}
	if err := store.WriteSecrets(ctx, writes); err != nil {
		return report, fmt.Errorf("write converted secrets: %w", err)
	}
	return report, nil
}

// sourceHash identifies an old ciphertext without revealing anything about it.
func sourceHash(legacyCiphertext string) string {
	sum := sha256.Sum256([]byte(legacyCiphertext))
	return hex.EncodeToString(sum[:])
}
