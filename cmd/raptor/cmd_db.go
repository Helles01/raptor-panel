package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Helles01/raptor-panel/internal/db"
	"github.com/Helles01/raptor-panel/internal/secrets"
)

func cmdKeygen(_ context.Context, e *env, args []string) error {
	if len(args) > 0 {
		return usagef("keygen takes no arguments")
	}
	key, err := secrets.GenerateKey()
	if err != nil {
		return err
	}
	fmt.Fprintln(e.stdout, key)
	fmt.Fprintln(e.stderr, "Keep this value secret and set it as RAPTOR_SECRET_KEY (or secrets.key in the ignored config file).")
	fmt.Fprintln(e.stderr, "If it is lost, every secret stored with it becomes unreadable.")
	return nil
}

func cmdDB(ctx context.Context, e *env, args []string) error {
	return dispatch(ctx, e, "raptor db", map[string]handler{
		"check":   cmdDBCheck,
		"migrate": cmdDBMigrate,
	}, args)
}

// cmdDBCheck compares the database's Laravel migrations with the ones Raptor is written against. It only reads.
func cmdDBCheck(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet("db check", e)
	configPath := configFlag(fs)
	if proceed, err := parseFlags(fs, args); !proceed {
		return err
	}

	sqlDB, _, err := openDB(ctx, e, *configPath)
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	applied, err := db.AppliedLaravelMigrations(ctx, sqlDB)
	if err != nil {
		return err
	}
	return writeMigrationReport(e.stdout, len(db.ExpectedMigrations()), len(applied), db.CompareMigrations(applied, db.ExpectedMigrations()))
}

const reportListLimit = 20

func writeMigrationReport(w io.Writer, expected, applied int, r db.MigrationReport) error {
	fmt.Fprintf(w, "reference migrations: %d expected, %d applied\n", expected, applied)
	if r.OK() {
		fmt.Fprintln(w, "OK: the database has exactly the expected schema version")
		return nil
	}

	list := func(title string, names []string) {
		if len(names) == 0 {
			return
		}
		fmt.Fprintf(w, "%s (%d):\n", title, len(names))
		for i, name := range names {
			if i == reportListLimit {
				fmt.Fprintf(w, "  ... and %d more\n", len(names)-reportListLimit)
				break
			}
			fmt.Fprintf(w, "  %s\n", name)
		}
	}
	list("missing: expected but not applied, the schema is older than Raptor expects", r.Missing)
	list("unknown: applied but not known to Raptor, the schema is newer or was changed by something else", r.Unknown)
	return errors.New("schema check failed")
}

// cmdDBMigrate creates Raptor's own tables. It never touches the reference panel's tables.
func cmdDBMigrate(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet("db migrate", e)
	configPath := configFlag(fs)
	if proceed, err := parseFlags(fs, args); !proceed {
		return err
	}

	sqlDB, _, err := openDB(ctx, e, *configPath)
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	done, err := db.Migrate(ctx, sqlDB)
	for _, name := range done {
		fmt.Fprintf(e.stdout, "applied %s\n", name)
	}
	if err != nil {
		return err
	}
	if len(done) == 0 {
		fmt.Fprintln(e.stdout, "nothing to apply, Raptor's tables are up to date")
	}
	return nil
}
