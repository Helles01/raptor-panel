package secretmigration

import (
	"context"
	"errors"
	"fmt"

	"github.com/Helles01/raptor-panel/internal/secrets"
)

// The last step of the cutover (plan 13.2, step 4): once every old value has a converted copy that can be read, the old values are made empty, so that nobody who
// has the APP_KEY of the old panel and a copy of the database can read anything with it. This is the one place that changes an old column, and it is
// careful in three ways: it only does it when every old value is converted (nothing is destroyed that exists nowhere else), it opens every converted copy with
// the key of the panel first (so a wrong RAPTOR_SECRET_KEY cannot lead to destroying the only readable copy), and it changes a row only while the row still holds
// the value that was converted.

// FinalStore is a Store that can also read the converted copies and make the old values empty.
type FinalStore interface {
	Store
	// SealedValues returns, per row, the converted copy of a column, as it is stored.
	SealedValues(ctx context.Context, c Column) (map[uint64]string, error)
	// Neutralise makes the old value of each row empty (NULL where the column allows it), in one transaction, and only while the row still holds the value the
	// hash was made of. A row that does not is an error and nothing is changed.
	Neutralise(ctx context.Context, rows []NeutraliseRow) error
}

// NeutraliseRow is an old value to make empty, with the hash of what it holds now.
type NeutraliseRow struct {
	Column     Column
	ID         uint64
	SourceHash string
}

// FinalizeOptions controls Finalize.
type FinalizeOptions struct {
	// DryRun checks everything and changes nothing.
	DryRun bool
}

// Problem is an old value that is not ready to be made empty. It never carries the value.
type Problem struct {
	Column Column
	RowID  uint64
	Reason string
}

// FinalizeColumn counts the old values of a column.
type FinalizeColumn struct {
	Column Column
	Total  int
	// Neutralised is how many were made empty, or with DryRun how many would be.
	Neutralised int
}

// FinalizeReport is the outcome of Finalize.
type FinalizeReport struct {
	DryRun   bool
	Columns  []FinalizeColumn
	Problems []Problem
}

// ErrNotReady is returned, with nothing changed, when an old value has no converted copy that is up to date and can be read.
var ErrNotReady = errors.New("some old values are not converted, or their converted copy cannot be read")

// Finalize makes the old values empty when every one of them has a converted copy that the panel can open.
func Finalize(ctx context.Context, store FinalStore, box *secrets.Box, opts FinalizeOptions) (FinalizeReport, error) {
	report := FinalizeReport{DryRun: opts.DryRun}
	var rows []NeutraliseRow
	for _, col := range Columns() {
		legacy, err := store.LegacyValues(ctx, col)
		if err != nil {
			return report, fmt.Errorf("read %s: %w", col, err)
		}
		hashes, err := store.SourceHashes(ctx, col)
		if err != nil {
			return report, fmt.Errorf("read converted hashes of %s: %w", col, err)
		}
		sealed, err := store.SealedValues(ctx, col)
		if err != nil {
			return report, fmt.Errorf("read the converted copies of %s: %w", col, err)
		}
		result := FinalizeColumn{Column: col, Total: len(legacy)}
		for _, row := range legacy {
			hash := sourceHash(row.Value)
			switch {
			case hashes[row.ID] != hash:
				report.Problems = append(report.Problems, Problem{Column: col, RowID: row.ID, Reason: "not converted (or changed since): run migrate-secrets"})
			case sealed[row.ID] == "":
				report.Problems = append(report.Problems, Problem{Column: col, RowID: row.ID, Reason: "the converted copy is missing"})
			default:
				if _, err := box.Open(sealed[row.ID], secrets.Context(col.Table, row.ID, col.Column)); err != nil {
					report.Problems = append(report.Problems, Problem{Column: col, RowID: row.ID, Reason: "the converted copy cannot be opened with this key"})
					continue
				}
				result.Neutralised++
				rows = append(rows, NeutraliseRow{Column: col, ID: row.ID, SourceHash: hash})
			}
		}
		report.Columns = append(report.Columns, result)
	}
	if len(report.Problems) > 0 {
		for i := range report.Columns {
			report.Columns[i].Neutralised = 0
		}
		return report, ErrNotReady
	}
	if opts.DryRun || len(rows) == 0 {
		return report, nil
	}
	if err := store.Neutralise(ctx, rows); err != nil {
		for i := range report.Columns {
			report.Columns[i].Neutralised = 0
		}
		return report, fmt.Errorf("make the old values empty: %w", err)
	}
	return report, nil
}
