package config

import (
	"testing"
	"time"
)

// With nothing set the panel does what the reference does.
func TestFeaturesDefaultsAreTheReferences(t *testing.T) {
	cfg, err := Load("", env(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := Features{Databases: true, DatabasesRandomHost: true, HideAdminActivity: false, BackupThrottleLimit: 2, BackupThrottleSeconds: 600, NotifyInstall: true, NotifyReinstall: true}
	if cfg.Features != want {
		t.Errorf("features = %+v, want %+v", cfg.Features, want)
	}
	if err := cfg.Features.Validate(); err != nil {
		t.Errorf("the defaults do not validate: %v", err)
	}
	if cfg.Features.BackupThrottlePeriod() != 600*time.Second {
		t.Errorf("period = %v", cfg.Features.BackupThrottlePeriod())
	}
}

func TestFeaturesFromTheFileAndTheEnvironment(t *testing.T) {
	file := writeFile(t, `
features:
  databases: false
  databases_random_host: false
  hide_admin_activity: true
  backup_throttle_limit: 5
  backup_throttle_seconds: 120
  notify_install: false
  notify_reinstall: false
`)
	cfg, err := Load(file, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := Features{Databases: false, DatabasesRandomHost: false, HideAdminActivity: true, BackupThrottleLimit: 5, BackupThrottleSeconds: 120}
	if cfg.Features != want {
		t.Errorf("from the file: %+v", cfg.Features)
	}

	// The environment wins, in both directions.
	cfg, err = Load(file, env(map[string]string{
		"RAPTOR_FEATURES_DATABASES": "true", "RAPTOR_FEATURES_DATABASES_RANDOM_HOST": "true", "RAPTOR_FEATURES_HIDE_ADMIN_ACTIVITY": "false",
		"RAPTOR_FEATURES_BACKUP_THROTTLE_LIMIT": "9", "RAPTOR_FEATURES_BACKUP_THROTTLE_SECONDS": "0", "RAPTOR_FEATURES_NOTIFY_INSTALL": "true", "RAPTOR_FEATURES_NOTIFY_REINSTALL": "true",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want = Features{Databases: true, DatabasesRandomHost: true, HideAdminActivity: false, BackupThrottleLimit: 9, BackupThrottleSeconds: 0, NotifyInstall: true, NotifyReinstall: true}
	if cfg.Features != want || cfg.Features.BackupThrottlePeriod() != 0 {
		t.Errorf("from the environment: %+v", cfg.Features)
	}
	if err := cfg.Features.Validate(); err != nil {
		t.Errorf("a throttle of 0 seconds is no throttle and is allowed: %v", err)
	}
}

func TestFeaturesRefuseWhatTheyCannotUse(t *testing.T) {
	for name, vars := range map[string]map[string]string{
		"a flag that is not one":        {"RAPTOR_FEATURES_DATABASES": "yes please"},
		"another flag that is not one":  {"RAPTOR_FEATURES_NOTIFY_INSTALL": "2x"},
		"a limit that is not a number":  {"RAPTOR_FEATURES_BACKUP_THROTTLE_LIMIT": "two"},
		"seconds that are not a number": {"RAPTOR_FEATURES_BACKUP_THROTTLE_SECONDS": "ten minutes"},
	} {
		if _, err := Load("", env(vars)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for name, mutate := range map[string]func(*Features){
		"a limit of 0":       func(f *Features) { f.BackupThrottleLimit = 0 },
		"a limit below 0":    func(f *Features) { f.BackupThrottleLimit = -1 },
		"a limit over 1000":  func(f *Features) { f.BackupThrottleLimit = 1001 },
		"seconds below 0":    func(f *Features) { f.BackupThrottleSeconds = -1 },
		"seconds over a day": func(f *Features) { f.BackupThrottleSeconds = 86401 },
	} {
		cfg, _ := Load("", env(nil))
		f := cfg.Features
		mutate(&f)
		if err := f.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
