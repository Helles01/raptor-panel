package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"

	"github.com/Helles01/raptor-panel/internal/api/healthapi"
	"github.com/Helles01/raptor-panel/internal/app"
	"github.com/Helles01/raptor-panel/internal/config"
	"github.com/Helles01/raptor-panel/internal/db"
	"github.com/Helles01/raptor-panel/internal/logging"
	"github.com/Helles01/raptor-panel/internal/secrets"
	"github.com/Helles01/raptor-panel/internal/store"
)

// cmdServe runs the panel: the web server and the loops beside it (the outbox, the scheduler, the housekeeping) until it is asked to stop.
func cmdServe(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet("serve", e)
	configPath := configFlag(fs)
	if proceed, err := parseFlags(fs, args); !proceed {
		return err
	}
	return serve(ctx, e, *configPath, nil)
}

// running is a panel that has been made: what it needs to run, and to be closed.
type running struct {
	cfg    config.Config
	logger *slog.Logger
	panel  app.Panel
	close  func()
}

// serve is cmdServe with a hook for the address that was opened (a test asks where the panel listens).
func serve(ctx context.Context, e *env, configPath string, ready func(net.Addr)) error {
	r, err := openPanel(ctx, e, configPath)
	if err != nil {
		return err
	}
	defer r.close()

	ctx, stop := app.Signals(ctx)
	defer stop()
	r.logger.Info("the panel is starting", "listen", r.cfg.HTTP.Listen, "url", r.cfg.App.URL)
	return app.Run(ctx, app.Options{Handler: r.panel.Handler, Loops: r.panel.Loops, Logger: r.logger, Listen: r.cfg.HTTP.Listen, Ready: ready})
}

// openPanel reads and checks the configuration, opens the database, checks its schema and builds the panel. The caller closes it.
func openPanel(ctx context.Context, e *env, configPath string) (*running, error) {
	cfg, err := config.Load(configPath, e.getenv)
	if err != nil {
		return nil, err
	}
	for _, v := range []interface{ Validate() error }{cfg.App, cfg.DB, cfg.HTTP, cfg.Wings, cfg.Log, cfg.Jobs, cfg.Web, cfg.DBHosts, cfg.Backups, cfg.Features} {
		if err := v.Validate(); err != nil {
			return nil, fmt.Errorf("invalid configuration: %w", err)
		}
	}
	key, err := cfg.Secrets.KeyBytes()
	if err != nil {
		return nil, err
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		return nil, err
	}
	logger, err := logging.New(cfg.Log, e.stderr)
	if err != nil {
		return nil, err
	}

	sqlDB, err := db.Open(ctx, cfg.DB)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*running, error) {
		_ = sqlDB.Close()
		return nil, err
	}
	if err := checkSchema(ctx, sqlDB); err != nil {
		return fail(err)
	}
	st, err := store.New(sqlDB, box)
	if err != nil {
		return fail(err)
	}
	frontend, err := app.LoadFrontend(cfg.Web.AssetsDir)
	if err != nil {
		return fail(err)
	}
	panel, err := app.Build(ctx, app.Input{
		Config: cfg, Logger: logger, Stores: storesOf(st), Frontend: frontend, SecretKey: key,
		Ready: map[string]healthapi.Check{"database": func(ctx context.Context) error { return sqlDB.PingContext(ctx) }},
	})
	if err != nil {
		return fail(err)
	}
	return &running{cfg: cfg, logger: logger, panel: panel, close: func() { _ = sqlDB.Close() }}, nil
}

// checkSchema refuses to start on a database that is not the one Raptor is written against: with the reference's migrations missing or not
// known, or with Raptor's own migrations pending. It says what to do about it and changes nothing.
func checkSchema(ctx context.Context, sqlDB *sql.DB) error {
	applied, err := db.AppliedLaravelMigrations(ctx, sqlDB)
	if err != nil {
		return err
	}
	if report := db.CompareMigrations(applied, db.ExpectedMigrations()); !report.OK() {
		return fmt.Errorf("the database does not have the schema Raptor is written against (%d expected migrations are missing, %d are not known): run \"raptor db check\" for the list", len(report.Missing), len(report.Unknown))
	}
	pending, err := db.PendingMigrations(ctx, sqlDB)
	if err != nil {
		return err
	}
	if len(pending) > 0 {
		return fmt.Errorf("raptor's own tables are not up to date (%d migrations pending): run \"raptor db migrate\"", len(pending))
	}
	return nil
}
