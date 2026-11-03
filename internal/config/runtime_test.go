package config

import (
	"strings"
	"testing"
	"time"
)

func TestRuntimeDefaults(t *testing.T) {
	cfg, err := Load("", env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Log != (Log{Level: "info", Format: "text"}) {
		t.Errorf("log = %+v", cfg.Log)
	}
	if cfg.Jobs != (Jobs{OutboxSeconds: 5, ScheduleSeconds: 60, BackupPruneMinutes: 360, ActivityKeepDays: 90}) {
		t.Errorf("jobs = %+v", cfg.Jobs)
	}
	if cfg.Web.AssetsDir != "web/public/assets" || cfg.DBHosts.TLS != "false" {
		t.Errorf("web %+v, db hosts %+v", cfg.Web, cfg.DBHosts)
	}
	for name, err := range map[string]error{"log": cfg.Log.Validate(), "jobs": cfg.Jobs.Validate(), "web": cfg.Web.Validate(), "db hosts": cfg.DBHosts.Validate()} {
		if err != nil {
			t.Errorf("the defaults of %s do not pass: %v", name, err)
		}
	}
}

func TestRuntimeFromFileKeepsTheRestOfTheDefaults(t *testing.T) {
	path := writeFile(t, "log:\n  level: debug\njobs:\n  outbox_seconds: 2\n  activity_keep_days: 0\nweb:\n  assets_dir: /srv/raptor/assets\ndb_hosts:\n  tls: preferred\n")
	cfg, err := Load(path, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Log.Level != "debug" || cfg.Log.Format != "text" {
		t.Errorf("log = %+v", cfg.Log)
	}
	if cfg.Jobs.OutboxSeconds != 2 || cfg.Jobs.ScheduleSeconds != 60 || cfg.Jobs.BackupPruneMinutes != 360 || cfg.Jobs.ActivityKeepDays != 0 {
		t.Errorf("jobs = %+v", cfg.Jobs)
	}
	if cfg.Web.AssetsDir != "/srv/raptor/assets" || cfg.DBHosts.TLS != "preferred" {
		t.Errorf("web %+v, db hosts %+v", cfg.Web, cfg.DBHosts)
	}
}

func TestRuntimeEnvironmentWins(t *testing.T) {
	path := writeFile(t, "log:\n  level: debug\n  format: json\nweb:\n  assets_dir: /from/file\ndb_hosts:\n  tls: \"true\"\n")
	cfg, err := Load(path, env(map[string]string{
		"RAPTOR_LOG_LEVEL": "error", "RAPTOR_LOG_FORMAT": "text", "RAPTOR_ASSETS_DIR": "/from/env", "RAPTOR_DB_HOSTS_TLS": "skip-verify",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Log != (Log{Level: "error", Format: "text"}) || cfg.Web.AssetsDir != "/from/env" || cfg.DBHosts.TLS != "skip-verify" {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestRuntimeFileRefusesUnknownKeys(t *testing.T) {
	for _, body := range []string{"jobs:\n  outbox_second: 1\n", "log:\n  levl: debug\n", "web:\n  dir: x\n"} {
		if _, err := Load(writeFile(t, body), env(nil)); err == nil {
			t.Errorf("%q accepted", body)
		}
	}
}

func TestRuntimeValidate(t *testing.T) {
	good := Jobs{OutboxSeconds: 5, ScheduleSeconds: 60, BackupPruneMinutes: 360, ActivityKeepDays: 90}
	with := func(f func(*Jobs)) Jobs { j := good; f(&j); return j }
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"log level":            {Log{Level: "trace", Format: "text"}.Validate(), "log.level"},
		"log level empty":      {Log{Format: "text"}.Validate(), "log.level"},
		"log level case":       {Log{Level: "INFO", Format: "text"}.Validate(), "log.level"},
		"log format":           {Log{Level: "info", Format: "xml"}.Validate(), "log.format"},
		"outbox zero":          {with(func(j *Jobs) { j.OutboxSeconds = 0 }).Validate(), "outbox_seconds"},
		"outbox long":          {with(func(j *Jobs) { j.OutboxSeconds = 3601 }).Validate(), "outbox_seconds"},
		"schedule short":       {with(func(j *Jobs) { j.ScheduleSeconds = 4 }).Validate(), "schedule_seconds"},
		"schedule long":        {with(func(j *Jobs) { j.ScheduleSeconds = 3601 }).Validate(), "schedule_seconds"},
		"prune negative":       {with(func(j *Jobs) { j.BackupPruneMinutes = -1 }).Validate(), "backup_prune_minutes"},
		"prune a year and one": {with(func(j *Jobs) { j.BackupPruneMinutes = 525601 }).Validate(), "backup_prune_minutes"},
		"keep negative":        {with(func(j *Jobs) { j.ActivityKeepDays = -1 }).Validate(), "activity_keep_days"},
		"keep too long":        {with(func(j *Jobs) { j.ActivityKeepDays = 3651 }).Validate(), "activity_keep_days"},
		"assets empty":         {Web{}.Validate(), "web.assets_dir"},
		"assets control":       {Web{AssetsDir: "a\nb"}.Validate(), "web.assets_dir"},
		"assets delete":        {Web{AssetsDir: "a\x7fb"}.Validate(), "web.assets_dir"},
		"db hosts tls":         {DBHosts{TLS: "yes"}.Validate(), "db_hosts.tls"},
		"db hosts tls empty":   {DBHosts{}.Validate(), "db_hosts.tls"},
	} {
		if tc.err == nil || !strings.Contains(tc.err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", name, tc.err, tc.want)
		}
	}
	// The edges that are allowed.
	for name, err := range map[string]error{
		"outbox 1":       with(func(j *Jobs) { j.OutboxSeconds = 1 }).Validate(),
		"outbox 3600":    with(func(j *Jobs) { j.OutboxSeconds = 3600 }).Validate(),
		"schedule 5":     with(func(j *Jobs) { j.ScheduleSeconds = 5 }).Validate(),
		"schedule 3600":  with(func(j *Jobs) { j.ScheduleSeconds = 3600 }).Validate(),
		"prune off":      with(func(j *Jobs) { j.BackupPruneMinutes = 0 }).Validate(),
		"prune a year":   with(func(j *Jobs) { j.BackupPruneMinutes = 525600 }).Validate(),
		"keep off":       with(func(j *Jobs) { j.ActivityKeepDays = 0 }).Validate(),
		"keep ten years": with(func(j *Jobs) { j.ActivityKeepDays = 3650 }).Validate(),
		"log json warn":  Log{Level: "warn", Format: "json"}.Validate(),
		"tls preferred":  DBHosts{TLS: "preferred"}.Validate(),
		"tls skip":       DBHosts{TLS: "skip-verify"}.Validate(),
		"tls true":       DBHosts{TLS: "true"}.Validate(),
		"tls false":      DBHosts{TLS: "false"}.Validate(),
		"log error":      Log{Level: "error", Format: "text"}.Validate(),
		"log debug":      Log{Level: "debug", Format: "text"}.Validate(),
	} {
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestJobsDurations(t *testing.T) {
	j := Jobs{OutboxSeconds: 5, ScheduleSeconds: 60, BackupPruneMinutes: 360, ActivityKeepDays: 90}
	if j.Outbox() != 5*time.Second || j.Schedules() != time.Minute || j.BackupPruneAge() != 6*time.Hour || j.ActivityKeep() != 90*24*time.Hour {
		t.Errorf("durations = %v %v %v %v", j.Outbox(), j.Schedules(), j.BackupPruneAge(), j.ActivityKeep())
	}
	off := Jobs{}
	if off.BackupPruneAge() != 0 || off.ActivityKeep() != 0 {
		t.Error("an age of 0 is not 0")
	}
}
