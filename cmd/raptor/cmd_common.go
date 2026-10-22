package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"

	"github.com/Helles01/raptor-panel/internal/config"
	"github.com/Helles01/raptor-panel/internal/db"
)

func newFlagSet(name string, e *env) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	return fs
}

func configFlag(fs *flag.FlagSet) *string {
	return fs.String("config", "", "path of the YAML config file (default: $RAPTOR_CONFIG; RAPTOR_* variables always apply)")
}

// parseFlags parses args. It reports proceed=false when the user only asked for help.
func parseFlags(fs *flag.FlagSet, args []string) (proceed bool, err error) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return false, nil
		}
		return false, usagef("invalid arguments")
	}
	return true, nil
}

// openDB loads and validates the configuration and connects to the database.
func openDB(ctx context.Context, e *env, configPath string) (*sql.DB, config.Config, error) {
	cfg, err := config.Load(configPath, e.getenv)
	if err != nil {
		return nil, config.Config{}, err
	}
	if err := cfg.DB.Validate(); err != nil {
		return nil, config.Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	sqlDB, err := db.Open(ctx, cfg.DB)
	if err != nil {
		return nil, config.Config{}, err
	}
	return sqlDB, cfg, nil
}
