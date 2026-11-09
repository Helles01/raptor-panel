package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/Helles01/raptor-panel/internal/app"
	"github.com/Helles01/raptor-panel/internal/config"
	"github.com/Helles01/raptor-panel/internal/db"
	"github.com/Helles01/raptor-panel/internal/db/dbq"
	"github.com/Helles01/raptor-panel/internal/doctor"
	"github.com/Helles01/raptor-panel/internal/mail"
	"github.com/Helles01/raptor-panel/internal/secretmigration"
	"github.com/Helles01/raptor-panel/internal/settings"
)

// cmdDoctor looks at a panel before it is started, or while it runs, and says what is wrong with it: the configuration, the key, the address, the frontend
// build, the database and its schema, the old secrets that were not converted, the nodes without a token, the mail and the clock. It changes nothing, and it
// prints no secret. It exits with 1 when something is a failure, so that a script can stop on it; warnings are for a person to read.
func cmdDoctor(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet("doctor", e)
	configPath := configFlag(fs)
	if proceed, err := parseFlags(fs, args); !proceed {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("doctor takes no arguments")
	}
	cfg, err := config.Load(*configPath, e.getenv)
	if err != nil {
		fmt.Fprintf(e.stdout, "FAIL  configuration: %v\n", err)
		return errors.New("the panel has problems that stop it")
	}

	results := doctor.Config(cfg)
	if cfg.HTTP.Validate() == nil {
		results = append(results, doctor.Listen(cfg.HTTP.Listen, net.Listen))
	}
	_, ferr := app.LoadFrontend(cfg.Web.AssetsDir)
	results = append(results, doctor.Frontend(cfg.Web.AssetsDir, ferr))

	if cfg.DB.Validate() != nil {
		results = append(results, doctor.Skipped())
	} else if sqlDB, err := db.Open(ctx, cfg.DB); err != nil {
		results = append(results, doctor.Unreachable(err))
	} else {
		defer sqlDB.Close()
		results = append(results, doctor.Checks(ctx, doctorDB{sqlDB}, time.Now().UTC())...)
	}

	counts := map[doctor.Level]int{}
	for _, r := range results {
		counts[r.Level]++
		fmt.Fprintf(e.stdout, "%-5s %s: %s\n", r.Level, r.Name, r.Detail)
	}
	fmt.Fprintf(e.stderr, "%d ok, %d to look at, %d failed.\n", counts[doctor.OK], counts[doctor.Warn], counts[doctor.Fail])
	if doctor.Worst(results) == doctor.Fail {
		return errors.New("the panel has problems that stop it")
	}
	return nil
}

// doctorDB answers the questions of the checks from the database. Every statement is fixed, and nothing is written.
type doctorDB struct{ db *sql.DB }

func (d doctorDB) Version(ctx context.Context) (string, error) {
	var v string
	err := d.db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&v)
	return v, err
}

func (d doctorDB) Now(ctx context.Context) (time.Time, error) {
	var s string
	if err := d.db.QueryRowContext(ctx, "SELECT DATE_FORMAT(UTC_TIMESTAMP(), '%Y-%m-%dT%H:%i:%sZ')").Scan(&s); err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, s)
}

func (d doctorDB) Schema(ctx context.Context) (doctor.Schema, error) {
	applied, err := db.AppliedLaravelMigrations(ctx, d.db)
	if err != nil {
		return doctor.Schema{}, err
	}
	report := db.CompareMigrations(applied, db.ExpectedMigrations())
	s := doctor.Schema{MissingReference: len(report.Missing), UnknownReference: len(report.Unknown)}
	if s.MissingReference > 0 || s.UnknownReference > 0 {
		return s, nil
	}
	s.PendingOwn, err = db.PendingMigrations(ctx, d.db)
	return s, err
}

func (d doctorDB) PendingSecrets(ctx context.Context) ([]secretmigration.PendingColumn, error) {
	return secretmigration.Pending(ctx, db.NewSecretStore(d.db))
}

func (d doctorDB) Nodes(ctx context.Context) ([]doctor.Node, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT n.id, n.name, EXISTS(SELECT 1 FROM raptor_secrets s WHERE s.table_name = 'nodes' AND s.row_id = n.id AND s.column_name = 'daemon_token') FROM nodes n ORDER BY n.id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []doctor.Node
	for rows.Next() {
		var n doctor.Node
		if err := rows.Scan(&n.ID, &n.Name, &n.HasToken); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (d doctorDB) Admins(ctx context.Context) (n int, err error) {
	err = d.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE root_admin = 1").Scan(&n)
	return n, err
}

func (d doctorDB) Mail(ctx context.Context) (map[string]string, bool, error) {
	q := dbq.New(d.db)
	values := map[string]string{}
	for _, key := range mail.SettingKeys() {
		v, err := q.SettingValue(ctx, settings.StorageKey(key))
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return nil, false, err
		default:
			values[key] = v
		}
	}
	id, err := q.SettingRowID(ctx, settings.StorageKey("mail:mailers:smtp:password"))
	if errors.Is(err, sql.ErrNoRows) {
		return values, false, nil
	} else if err != nil {
		return nil, false, err
	}
	var n int
	err = d.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM raptor_secrets WHERE table_name = 'settings' AND row_id = ? AND column_name = 'value'", id).Scan(&n)
	return values, n > 0, err
}
