package config

import (
	"fmt"
	"strings"
	"testing"
)

const (
	secretKeyValue = "s3-secret-key-Value/1+2"
	accessKeyValue = "AKIA-access-id-0001"
)

func TestBackupsDefaults(t *testing.T) {
	cfg, err := Load("", env(nil))
	if err != nil {
		t.Fatal(err)
	}
	b := cfg.Backups
	if b.Driver != "wings" || b.PresignedMinutes != 60 || b.MaxPartSize != 5<<30 || b.S3Configured() {
		t.Errorf("defaults = %+v", b)
	}
	if err := b.Validate(); err != nil {
		t.Errorf("the defaults do not validate: %v", err)
	}
}

func TestBackupsFromTheEnvironmentWinOverTheFile(t *testing.T) {
	file := writeFile(t, `
backups:
  driver: wings
  presigned_minutes: 30
  max_part_size: 10485760
  s3:
    region: from-file-1
    bucket: file-bucket
    access_key: file-access
    secret_key: file-secret
`)
	cfg, err := Load(file, env(map[string]string{
		"RAPTOR_BACKUPS_DRIVER":            "s3",
		"RAPTOR_BACKUPS_S3_REGION":         "eu-central-1",
		"RAPTOR_BACKUPS_S3_BUCKET":         "env-bucket",
		"RAPTOR_BACKUPS_S3_ENDPOINT":       "http://127.0.0.1:9000",
		"RAPTOR_BACKUPS_S3_ACCESS_KEY":     accessKeyValue,
		"RAPTOR_BACKUPS_S3_SECRET_KEY":     secretKeyValue,
		"RAPTOR_BACKUPS_S3_STORAGE_CLASS":  "STANDARD_IA",
		"RAPTOR_BACKUPS_S3_PATH_STYLE":     "true",
		"RAPTOR_BACKUPS_PRESIGNED_MINUTES": "90",
		"RAPTOR_BACKUPS_MAX_PART_SIZE":     "104857600",
	}))
	if err != nil {
		t.Fatal(err)
	}
	b := cfg.Backups
	want := BackupsS3{Endpoint: "http://127.0.0.1:9000", Region: "eu-central-1", Bucket: "env-bucket", AccessKey: accessKeyValue, SecretKey: secretKeyValue, PathStyle: true, StorageClass: "STANDARD_IA"}
	if b.Driver != "s3" || b.S3 != want || b.PresignedMinutes != 90 || b.MaxPartSize != 104857600 {
		t.Errorf("backups = %+v", b)
	}
	if err := b.Validate(); err != nil {
		t.Errorf("validate: %v", err)
	}
	c := b.S3Config()
	if c.Bucket != "env-bucket" || c.Region != "eu-central-1" || c.AccessKey != accessKeyValue || !c.PathStyle || c.Endpoint != "http://127.0.0.1:9000" || c.StorageClass != "STANDARD_IA" {
		t.Errorf("client settings = %v", c)
	}

	// From the file alone, the file's values are used.
	cfg, err = Load(file, env(nil))
	if err != nil || cfg.Backups.S3.Bucket != "file-bucket" || cfg.Backups.PresignedMinutes != 30 || cfg.Backups.MaxPartSize != 10485760 {
		t.Errorf("from the file: %+v, %v", cfg.Backups, err)
	}
}

func TestBackupsEnvironmentThatIsNotUnderstood(t *testing.T) {
	for name, vars := range map[string]map[string]string{
		"a path style that is no boolean": {"RAPTOR_BACKUPS_S3_PATH_STYLE": "maybe"},
		"minutes that are no number":      {"RAPTOR_BACKUPS_PRESIGNED_MINUTES": "an hour"},
		"a part size that is no number":   {"RAPTOR_BACKUPS_MAX_PART_SIZE": "5GB"},
	} {
		if _, err := Load("", env(vars)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestBackupsValidate(t *testing.T) {
	good := Backups{
		Driver: "s3", PresignedMinutes: 60, MaxPartSize: 5 << 30,
		S3: BackupsS3{Region: "eu-central-1", Bucket: "my-backups", AccessKey: accessKeyValue, SecretKey: secretKeyValue},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("a good configuration: %v", err)
	}
	// The default driver with the S3 settings given is fine too: it reaches the backups that are on S3.
	withWings := good
	withWings.Driver = "wings"
	if err := withWings.Validate(); err != nil {
		t.Errorf("wings with S3 settings: %v", err)
	}
	// Without S3 settings and with the node's own disk nothing about S3 is asked.
	plain := Backups{Driver: "wings", PresignedMinutes: 60, MaxPartSize: 5 << 30}
	if err := plain.Validate(); err != nil {
		t.Errorf("plain: %v", err)
	}

	for name, mutate := range map[string]func(*Backups){
		"a driver nobody knows":     func(b *Backups) { b.Driver = "ftp" },
		"a driver in capitals":      func(b *Backups) { b.Driver = "S3" },
		"no driver":                 func(b *Backups) { b.Driver = "" },
		"s3 without settings":       func(b *Backups) { b.S3 = BackupsS3{} },
		"minutes 0":                 func(b *Backups) { b.PresignedMinutes = 0 },
		"minutes over seven days":   func(b *Backups) { b.PresignedMinutes = 10081 },
		"a part size under 5 MiB":   func(b *Backups) { b.MaxPartSize = 5<<20 - 1 },
		"a part size over 5 GiB":    func(b *Backups) { b.MaxPartSize = 5<<30 + 1 },
		"a part size of 0":          func(b *Backups) { b.MaxPartSize = 0 },
		"a bucket without a region": func(b *Backups) { b.S3.Region = "" },
		"no secret key":             func(b *Backups) { b.S3.SecretKey = "" },
		"a bucket with capitals":    func(b *Backups) { b.S3.Bucket = "MyBackups" },
		"an endpoint with a path":   func(b *Backups) { b.S3.Endpoint = "https://h.example.com/s3" },
		"a storage class in small":  func(b *Backups) { b.S3.StorageClass = "standard" },
	} {
		b := good
		mutate(&b)
		err := b.Validate()
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), secretKeyValue) || strings.Contains(err.Error(), accessKeyValue) {
			t.Errorf("%s: the error repeats a key: %v", name, err)
		}
	}

	// S3 settings that are only partly given are refused even when the driver is the node's: they would fail at the first backup that is on S3.
	partial := plain
	partial.S3.Bucket = "my-backups"
	if err := partial.Validate(); err == nil {
		t.Error("a bucket with nothing else was accepted")
	}
}

func TestBackupsNeverPrintTheKeys(t *testing.T) {
	cfg, err := Load("", env(map[string]string{"RAPTOR_BACKUPS_S3_ACCESS_KEY": accessKeyValue, "RAPTOR_BACKUPS_S3_SECRET_KEY": secretKeyValue, "RAPTOR_BACKUPS_S3_BUCKET": "my-backups"}))
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]string{
		"%v of the section":  fmt.Sprintf("%v", cfg.Backups),
		"%+v of the section": fmt.Sprintf("%+v", cfg.Backups),
		"%#v of the section": fmt.Sprintf("%#v", cfg.Backups),
		"%+v of the whole":   fmt.Sprintf("%+v", cfg),
		"the bucket":         cfg.Backups.S3.String(),
		"the log value":      cfg.Backups.S3.LogValue().String(),
		"the client's":       fmt.Sprintf("%+v", cfg.Backups.S3Config()),
	} {
		if strings.Contains(s, secretKeyValue) || strings.Contains(s, accessKeyValue) {
			t.Errorf("%s prints a key: %s", name, s)
		}
	}
}
