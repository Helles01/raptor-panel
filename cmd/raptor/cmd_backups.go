package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/Helles01/raptor-panel/internal/config"
	"github.com/Helles01/raptor-panel/internal/s3"
)

func cmdBackups(ctx context.Context, e *env, args []string) error {
	return dispatch(ctx, e, "raptor backups", map[string]handler{
		"check": cmdBackupsCheck,
	}, args)
}

// cmdBackupsCheck tries the S3 settings against the bucket: it makes an object of its own, sends it the way a node does, reads it the way a browser does, and
// removes it. It needs no database. The first time it is run with new settings is the moment to find a wrong key, region, endpoint or permission, and not the first
// backup.
func cmdBackupsCheck(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet("backups check", e)
	configPath := configFlag(fs)
	if proceed, err := parseFlags(fs, args); !proceed {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("backups check takes no arguments")
	}
	cfg, err := config.Load(*configPath, e.getenv)
	if err != nil {
		return err
	}
	if err := cfg.Backups.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	if !cfg.Backups.S3Configured() {
		return errors.New("no S3 settings are given (RAPTOR_BACKUPS_S3_*), so there is nothing to check")
	}
	client, err := s3.New(cfg.Backups.S3Config(), s3.Options{})
	if err != nil {
		return err
	}

	fmt.Fprintf(e.stderr, "Trying %v with an object of its own, which is removed again.\n", cfg.Backups.S3)
	var failed *s3.Step
	steps := client.Check(ctx)
	for i, s := range steps {
		if s.Err != nil {
			fmt.Fprintf(e.stdout, "FAIL  %s: %v\n", s.Name, s.Err)
			failed = &steps[i]
			continue
		}
		fmt.Fprintf(e.stdout, "ok    %s\n", s.Name)
	}
	if failed != nil {
		return fmt.Errorf("the check stopped at %q", failed.Name)
	}
	fmt.Fprintf(e.stderr, "Everything worked. New backups are kept on %s.\n", map[string]string{"wings": "the node (set backups.driver to s3 to keep them in the bucket)", "s3": "S3"}[cfg.Backups.Driver])
	return nil
}
