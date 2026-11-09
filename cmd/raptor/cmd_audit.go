package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/Helles01/raptor-panel/internal/audit"
	"github.com/Helles01/raptor-panel/internal/db"
)

// cmdAudit checks the data in the database against what Raptor expects of it (internal/audit), and prints what it finds: the rows that Raptor cannot use at all
// are errors, the ones it still reads but would refuse to write again are warnings. It changes nothing and deletes nothing; a person reads the report and decides.
// It is run on the imported database before the panel is started for the first time (docs/cutover.md), and again whenever somebody wants to know. It exits with 1
// when there is an error, so that a script can stop on it, and with 0 when there are only warnings.
func cmdAudit(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet("audit", e)
	configPath := configFlag(fs)
	asJSON := fs.Bool("json", false, "write the report as JSON, for a program, instead of text")
	if proceed, err := parseFlags(fs, args); !proceed {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("audit takes no arguments")
	}
	sqlDB, _, err := openDB(ctx, e, *configPath)
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	return runAudit(ctx, e.stdout, e.stderr, db.NewAuditSource(sqlDB), time.Now().UTC(), *asJSON)
}

// auditSource is where the rows come from.
type auditSource interface {
	Load(ctx context.Context) (audit.Input, error)
}

func runAudit(ctx context.Context, stdout, stderr io.Writer, src auditSource, now time.Time, asJSON bool) error {
	in, err := src.Load(ctx)
	if err != nil {
		return fmt.Errorf("read the data to check: %w", err)
	}
	report := audit.Run(in, now)
	if asJSON {
		data, err := report.JSON()
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, string(data))
	} else {
		fmt.Fprint(stdout, report.Text())
	}
	fmt.Fprintf(stderr, "%d error(s), %d warning(s).\n", report.Count(audit.Error), report.Count(audit.Warning))
	if report.HasErrors() {
		return fmt.Errorf("%d error(s) found: rows that Raptor cannot use (see the report)", report.Count(audit.Error))
	}
	return nil
}
