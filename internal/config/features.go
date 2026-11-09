package config

import (
	"errors"
	"strconv"
	"time"
)

// Features are the switches of the reference that decide what the panel offers and says, at the values the reference has them (its environment variable in
// brackets). Nothing here is a secret, and nothing here is needed for a panel to run: the defaults are the reference's.
type Features struct {
	// Databases lets users make databases for their servers (PTERODACTYL_CLIENT_DATABASES_ENABLED, true).
	Databases bool `yaml:"databases"`
	// DatabasesRandomHost lets a database be made on a host picked at random when none is linked to the node of the server
	// (PTERODACTYL_CLIENT_DATABASES_ALLOW_RANDOM, true).
	DatabasesRandomHost bool `yaml:"databases_random_host"`
	// HideAdminActivity leaves the entries of administrators out of the activity log that users see (APP_ACTIVITY_HIDE_ADMIN, false).
	HideAdminActivity bool `yaml:"hide_admin_activity"`
	// BackupThrottleLimit backups may be started in BackupThrottleSeconds for one server (BACKUP_THROTTLE_LIMIT, 2); a period of 0 turns the throttle off
	// (BACKUP_THROTTLE_PERIOD, 600 seconds).
	BackupThrottleLimit   int `yaml:"backup_throttle_limit"`
	BackupThrottleSeconds int `yaml:"backup_throttle_seconds"`
	// NotifyInstall and NotifyReinstall send the owner a mail when the installation, or a reinstallation, of a server has finished
	// (PTERODACTYL_SEND_INSTALL_NOTIFICATION and PTERODACTYL_SEND_REINSTALL_NOTIFICATION, both true).
	NotifyInstall   bool `yaml:"notify_install"`
	NotifyReinstall bool `yaml:"notify_reinstall"`
}

func (c *Config) setFeaturesDefaults() {
	c.Features = Features{
		Databases: true, DatabasesRandomHost: true, BackupThrottleLimit: 2, BackupThrottleSeconds: 600, NotifyInstall: true, NotifyReinstall: true,
	}
}

func applyFeaturesEnv(f *Features, getenv func(string) string) error {
	for _, b := range []struct {
		dst  *bool
		name string
	}{
		{&f.Databases, "RAPTOR_FEATURES_DATABASES"}, {&f.DatabasesRandomHost, "RAPTOR_FEATURES_DATABASES_RANDOM_HOST"},
		{&f.HideAdminActivity, "RAPTOR_FEATURES_HIDE_ADMIN_ACTIVITY"}, {&f.NotifyInstall, "RAPTOR_FEATURES_NOTIFY_INSTALL"},
		{&f.NotifyReinstall, "RAPTOR_FEATURES_NOTIFY_REINSTALL"},
	} {
		if v := getenv(b.name); v != "" {
			on, err := strconv.ParseBool(v)
			if err != nil {
				return errors.New(b.name + " must be true or false")
			}
			*b.dst = on
		}
	}
	for _, n := range []struct {
		dst  *int
		name string
	}{{&f.BackupThrottleLimit, "RAPTOR_FEATURES_BACKUP_THROTTLE_LIMIT"}, {&f.BackupThrottleSeconds, "RAPTOR_FEATURES_BACKUP_THROTTLE_SECONDS"}} {
		if v := getenv(n.name); v != "" {
			i, err := strconv.Atoi(v)
			if err != nil {
				return errors.New(n.name + " must be a number")
			}
			*n.dst = i
		}
	}
	return nil
}

// Validate checks the numbers.
func (f Features) Validate() error {
	switch {
	case f.BackupThrottleLimit < 1 || f.BackupThrottleLimit > 1000:
		return errors.New("features.backup_throttle_limit must be between 1 and 1000")
	case f.BackupThrottleSeconds < 0 || f.BackupThrottleSeconds > 86400:
		return errors.New("features.backup_throttle_seconds must be 0 (no throttle) or at most 86400")
	}
	return nil
}

// BackupThrottlePeriod is the throttle's window as a duration; 0 for none.
func (f Features) BackupThrottlePeriod() time.Duration {
	return time.Duration(f.BackupThrottleSeconds) * time.Second
}
