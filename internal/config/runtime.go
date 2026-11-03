package config

import (
	"errors"
	"strings"
	"time"
)

// Log configures the panel's own log.
type Log struct {
	// Level is debug, info, warn or error.
	Level string `yaml:"level"`
	// Format is text (for a terminal) or json (for a log collector).
	Format string `yaml:"format"`
}

// Jobs configures the loops that run beside the web server.
type Jobs struct {
	// OutboxSeconds is how often the outbox (mail, orders to nodes) is looked at.
	OutboxSeconds int `yaml:"outbox_seconds"`
	// ScheduleSeconds is how often the schedules that are due are started (the reference's cron runs them every minute).
	ScheduleSeconds int `yaml:"schedule_seconds"`
	// BackupPruneMinutes marks a backup that never reported as failed after this many minutes (the reference's
	// backups:prune-age, 360); 0 turns it off.
	BackupPruneMinutes int `yaml:"backup_prune_minutes"`
	// ActivityKeepDays deletes the activity log older than this many days (the reference's activity.prune_days, 90); 0 turns it off.
	ActivityKeepDays int `yaml:"activity_keep_days"`
}

// Web says where the frontend build is.
type Web struct {
	// AssetsDir is the folder with the build of the frontend: manifest.json and the files it names.
	AssetsDir string `yaml:"assets_dir"`
}

// DBHosts configures how the panel talks to the hosts it makes the databases of the servers on.
type DBHosts struct {
	// TLS is one of "false", "true", "skip-verify" or "preferred", for every host.
	TLS string `yaml:"tls"`
}

func (c *Config) setRuntimeDefaults() {
	c.Log = Log{Level: "info", Format: "text"}
	c.Jobs = Jobs{OutboxSeconds: 5, ScheduleSeconds: 60, BackupPruneMinutes: 360, ActivityKeepDays: 90}
	c.Web = Web{AssetsDir: "web/public/assets"}
	c.DBHosts = DBHosts{TLS: "false"}
}

// Validate checks the log settings.
func (l Log) Validate() error {
	switch l.Level {
	case "debug", "info", "warn", "error":
	default:
		return errors.New(`log.level must be one of "debug", "info", "warn", "error"`)
	}
	switch l.Format {
	case "text", "json":
	default:
		return errors.New(`log.format must be "text" or "json"`)
	}
	return nil
}

// Validate checks the intervals and ages.
func (j Jobs) Validate() error {
	switch {
	case j.OutboxSeconds < 1 || j.OutboxSeconds > 3600:
		return errors.New("jobs.outbox_seconds must be between 1 and 3600")
	case j.ScheduleSeconds < 5 || j.ScheduleSeconds > 3600:
		return errors.New("jobs.schedule_seconds must be between 5 and 3600")
	case j.BackupPruneMinutes < 0 || j.BackupPruneMinutes > 525600:
		return errors.New("jobs.backup_prune_minutes must be 0 (off) or at most 525600")
	case j.ActivityKeepDays < 0 || j.ActivityKeepDays > 3650:
		return errors.New("jobs.activity_keep_days must be 0 (off) or at most 3650")
	}
	return nil
}

// Outbox is the interval of the outbox as a duration.
func (j Jobs) Outbox() time.Duration { return time.Duration(j.OutboxSeconds) * time.Second }

// Schedules is the interval of the scheduler as a duration.
func (j Jobs) Schedules() time.Duration { return time.Duration(j.ScheduleSeconds) * time.Second }

// BackupPruneAge is the age of the backups that are marked failed; 0 for none.
func (j Jobs) BackupPruneAge() time.Duration {
	return time.Duration(j.BackupPruneMinutes) * time.Minute
}

// ActivityKeep is how long the activity log is kept; 0 for ever.
func (j Jobs) ActivityKeep() time.Duration { return time.Duration(j.ActivityKeepDays) * 24 * time.Hour }

// Validate checks the folder of the frontend build: a path, not a pattern or a list, without a control character.
func (w Web) Validate() error {
	if w.AssetsDir == "" || strings.ContainsFunc(w.AssetsDir, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return errors.New("web.assets_dir must be a folder name without control characters")
	}
	return nil
}

// Validate checks the TLS mode for database hosts.
func (d DBHosts) Validate() error {
	switch d.TLS {
	case "false", "true", "skip-verify", "preferred":
		return nil
	}
	return errors.New(`db_hosts.tls must be one of "false", "true", "skip-verify", "preferred"`)
}
