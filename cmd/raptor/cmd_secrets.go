package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Helles01/raptor-panel/internal/db"
	"github.com/Helles01/raptor-panel/internal/legacycrypt"
	"github.com/Helles01/raptor-panel/internal/secretmigration"
	"github.com/Helles01/raptor-panel/internal/secrets"
)

const legacyKeyVariable = "RAPTOR_LEGACY_APP_KEY"

// cmdMigrateSecrets converts the reference panel's Laravel-encrypted columns into Raptor's format. The old columns
// are only read; the results go to the raptor_secrets table (run `raptor db migrate` first).
func cmdMigrateSecrets(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet("migrate-secrets", e)
	configPath := configFlag(fs)
	dryRun := fs.Bool("dry-run", false, "decrypt and seal everything but write nothing (also checks that the old APP_KEY is right)")
	all := fs.Bool("all", false, "convert every row again, not only new or changed ones")
	if proceed, err := parseFlags(fs, args); !proceed {
		return err
	}

	// The old key is accepted from the environment only, never from a file, and is not logged.
	rawLegacyKey := e.getenv(legacyKeyVariable)
	if rawLegacyKey == "" {
		return fmt.Errorf("%s is not set: pass the old panel's APP_KEY in the environment for this run only", legacyKeyVariable)
	}
	appKey, err := legacycrypt.ParseAppKey(rawLegacyKey)
	if err != nil {
		return fmt.Errorf("%s: %w", legacyKeyVariable, err)
	}
	legacy, err := legacycrypt.New(appKey)
	if err != nil {
		return fmt.Errorf("%s: %w", legacyKeyVariable, err)
	}

	sqlDB, cfg, err := openDB(ctx, e, *configPath)
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	key, err := cfg.Secrets.KeyBytes()
	if err != nil {
		return err
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		return err
	}

	return migrateSecrets(ctx, e.stdout, db.NewSecretStore(sqlDB), legacy, box, secretmigration.Options{DryRun: *dryRun, All: *all})
}

func migrateSecrets(ctx context.Context, w io.Writer, store secretmigration.Store, legacy *legacycrypt.Decrypter, box *secrets.Box, opts secretmigration.Options) error {
	report, err := secretmigration.Run(ctx, store, legacy, box, opts)
	writeSecretsReport(w, report)

	switch {
	case errors.Is(err, secretmigration.ErrFailures):
		return fmt.Errorf("%d value(s) could not be decrypted and nothing was written; if every row failed, the old APP_KEY is probably wrong", len(report.Failures))
	case err != nil:
		return err
	case report.DryRun:
		fmt.Fprintln(w, "dry run: nothing was written")
	default:
		fmt.Fprintln(w, "done: the old columns were not modified")
	}
	return nil
}

func writeSecretsReport(w io.Writer, r secretmigration.Report) {
	verb := "converted"
	if r.DryRun {
		verb = "would be converted"
	}
	for _, c := range r.Columns {
		fmt.Fprintf(w, "%s: %d rows, %d %s, %d unchanged\n", c.Column, c.Total, c.Converted, verb, c.Unchanged)
	}
	for _, f := range r.Failures {
		fmt.Fprintf(w, "FAILED %s id=%d\n", f.Column, f.RowID)
	}
}
