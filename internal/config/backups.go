package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/Helles01/raptor-panel/internal/s3"
)

// Backups says where new backups are kept and, for S3, how to reach the bucket. Backups that were made keep the disk they were made on, so the driver can
// be changed later without losing any (the reference says the same of APP_BACKUP_DRIVER).
type Backups struct {
	// Driver is "wings" (on the node itself, the default) or "s3" (in the bucket of S3).
	Driver string    `yaml:"driver"`
	S3     BackupsS3 `yaml:"s3"`
	// PresignedMinutes is how long the addresses a node sends the parts of an upload to are good for (the reference's BACKUP_PRESIGNED_URL_LIFESPAN, 60).
	PresignedMinutes int `yaml:"presigned_minutes"`
	// MaxPartSize is the size of one part of an upload in bytes (BACKUP_MAX_PART_SIZE, 5 GiB, which is also the most S3 takes).
	MaxPartSize int64 `yaml:"max_part_size"`
}

// BackupsS3 is the bucket. The keys may be in the file (which must be kept out of version control) or in the environment, where they are better kept.
// Settings that are given make S3 available for the backups that are on it, whichever driver makes the new ones.
type BackupsS3 struct {
	// Endpoint is the address of an S3-compatible server; empty is Amazon S3 in Region.
	Endpoint  string `yaml:"endpoint"`
	Region    string `yaml:"region"`
	Bucket    string `yaml:"bucket"`
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
	// PathStyle puts the bucket in the path of the address, which most S3-compatible servers want.
	PathStyle    bool   `yaml:"path_style"`
	StorageClass string `yaml:"storage_class"`
}

func (c *Config) setBackupsDefaults() {
	c.Backups = Backups{Driver: "wings", PresignedMinutes: 60, MaxPartSize: s3.MaxPartSize}
}

func applyBackupsEnv(b *Backups, getenv func(string) string) error {
	override := func(dst *string, name string) {
		if v := getenv(name); v != "" {
			*dst = v
		}
	}
	override(&b.Driver, "RAPTOR_BACKUPS_DRIVER")
	override(&b.S3.Endpoint, "RAPTOR_BACKUPS_S3_ENDPOINT")
	override(&b.S3.Region, "RAPTOR_BACKUPS_S3_REGION")
	override(&b.S3.Bucket, "RAPTOR_BACKUPS_S3_BUCKET")
	override(&b.S3.AccessKey, "RAPTOR_BACKUPS_S3_ACCESS_KEY")
	override(&b.S3.SecretKey, "RAPTOR_BACKUPS_S3_SECRET_KEY")
	override(&b.S3.StorageClass, "RAPTOR_BACKUPS_S3_STORAGE_CLASS")
	if v := getenv("RAPTOR_BACKUPS_S3_PATH_STYLE"); v != "" {
		on, err := strconv.ParseBool(v)
		if err != nil {
			return errors.New("RAPTOR_BACKUPS_S3_PATH_STYLE must be true or false")
		}
		b.S3.PathStyle = on
	}
	if v := getenv("RAPTOR_BACKUPS_PRESIGNED_MINUTES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return errors.New("RAPTOR_BACKUPS_PRESIGNED_MINUTES must be a number")
		}
		b.PresignedMinutes = n
	}
	if v := getenv("RAPTOR_BACKUPS_MAX_PART_SIZE"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return errors.New("RAPTOR_BACKUPS_MAX_PART_SIZE must be a number of bytes")
		}
		b.MaxPartSize = n
	}
	return nil
}

// S3Configured says whether any of the S3 settings is given. A panel with them can reach the backups that are on S3, whatever the driver is.
func (b Backups) S3Configured() bool {
	s := b.S3
	return s.Endpoint != "" || s.Region != "" || s.Bucket != "" || s.AccessKey != "" || s.SecretKey != "" || s.StorageClass != ""
}

// S3Config is the settings of the bucket as the client wants them.
func (b Backups) S3Config() s3.Config {
	return s3.Config{
		Endpoint: b.S3.Endpoint, Region: b.S3.Region, Bucket: b.S3.Bucket, AccessKey: b.S3.AccessKey, SecretKey: b.S3.SecretKey, PathStyle: b.S3.PathStyle,
		StorageClass: b.S3.StorageClass,
	}
}

// Validate checks the driver, the sizes and, when S3 is given or wanted, the bucket.
func (b Backups) Validate() error {
	switch b.Driver {
	case "wings", "s3":
	default:
		return errors.New(`backups.driver must be "wings" or "s3"`)
	}
	if b.PresignedMinutes < 1 || b.PresignedMinutes > int(s3.MaxValidity.Minutes()) {
		return errors.New("backups.presigned_minutes must be between 1 and 10080 (seven days)")
	}
	if b.MaxPartSize < s3.MinPartSize || b.MaxPartSize > s3.MaxPartSize {
		return fmt.Errorf("backups.max_part_size must be between %d (5 MiB) and %d (5 GiB) bytes", s3.MinPartSize, int64(s3.MaxPartSize))
	}
	if b.Driver == "s3" && !b.S3Configured() {
		return errors.New(`backups.driver is "s3" but no backups.s3 settings are given`)
	}
	if b.S3Configured() {
		if err := b.S3Config().Validate(); err != nil {
			return fmt.Errorf("backups.s3: %w", err)
		}
	}
	return nil
}

// String never includes the keys.
func (s BackupsS3) String() string {
	return fmt.Sprintf("backups.s3{endpoint=%q region=%s bucket=%s path_style=%t storage_class=%s access_key=%s secret_key=%s}",
		s.Endpoint, s.Region, s.Bucket, s.PathStyle, s.StorageClass, redacted(s.AccessKey != ""), redacted(s.SecretKey != ""))
}

// GoString keeps %#v from printing the keys.
func (s BackupsS3) GoString() string { return s.String() }

// LogValue keeps slog from printing the keys.
func (s BackupsS3) LogValue() slog.Value { return slog.StringValue(s.String()) }
