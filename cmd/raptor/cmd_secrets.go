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
	finalize := fs.Bool("finalize", false, "the last step of the cutover: make the old encrypted values empty, once every one is converted and the converted copy can be read (without --yes it only checks)")
	yes := fs.Bool("yes", false, "with --finalize: really make the old values empty")
	backup := fs.Bool("backup-taken", false, "with --finalize --yes: say that a fresh backup of the database was made, because this cannot be taken back")
	if proceed, err := parseFlags(fs, args); !proceed {
		return err
	}
	if !*finalize && (*yes || *backup) {
		return usagef("--yes and --backup-taken belong to --finalize")
	}
	if *finalize && (*dryRun || *all) {
		return usagef("--finalize does not go with --dry-run or --all: without --yes it only checks")
	}
	if *finalize {
		return cmdFinalizeSecrets(ctx, e, *configPath, *yes, *backup)
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

// cmdFinalizeSecrets is the last step of the cutover: the old encrypted values are made empty, so that the APP_KEY of the old panel and a copy of the database
// no longer read anything. It needs no old key, only the key of this panel, and it refuses unless every old value is converted and its converted copy opens.
func cmdFinalizeSecrets(ctx context.Context, e *env, configPath string, yes, backupTaken bool) error {
	if yes && !backupTaken {
		return errors.New("make a fresh backup of the database first and say so with --backup-taken: making the old values empty cannot be taken back")
	}
	sqlDB, cfg, err := openDB(ctx, e, configPath)
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
	return finalizeSecrets(ctx, e.stdout, db.NewSecretStore(sqlDB), box, !yes)
}

func finalizeSecrets(ctx context.Context, w io.Writer, store secretmigration.FinalStore, box *secrets.Box, dryRun bool) error {
	report, err := secretmigration.Finalize(ctx, store, box, secretmigration.FinalizeOptions{DryRun: dryRun})
	verb := "made empty"
	if report.DryRun {
		verb = "would be made empty"
	}
	for _, c := range report.Columns {
		fmt.Fprintf(w, "%s: %d old values, %d %s\n", c.Column, c.Total, c.Neutralised, verb)
	}
	for _, p := range report.Problems {
		fmt.Fprintf(w, "NOT READY %s id=%d: %s\n", p.Column, p.RowID, p.Reason)
	}
	switch {
	case errors.Is(err, secretmigration.ErrNotReady):
		return fmt.Errorf("%d old value(s) are not ready and nothing was changed", len(report.Problems))
	case err != nil:
		return err
	case report.DryRun:
		fmt.Fprintln(w, "checked: every old value is converted and its converted copy opens; nothing was changed. Make a backup of the database, then run this again with --yes --backup-taken")
	default:
		fmt.Fprintln(w, "done: the old values are empty. Destroy the APP_KEY of the old panel and the copies of the old machine")
	}
	return nil
}
