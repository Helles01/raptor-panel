package main

import (
	"strings"
	"testing"
)

// The checks that need no database: a configuration that is wrong is said to be wrong, nothing is changed, and no key is printed.
func TestDoctorWithoutADatabase(t *testing.T) {
	vars := map[string]string{
		"RAPTOR_SECRET_KEY": "iDsdi+5AzVWiY8W1v9SHn4lUxVtt4BFUkmPSUgPo5RA=", "RAPTOR_APP_URL": "http://127.0.0.1:8000", "RAPTOR_DB_NAME": "", "RAPTOR_DB_USER": "raptor",
		"RAPTOR_DB_PASSWORD": "db-password-value", "RAPTOR_HTTP_LISTEN": "127.0.0.1:1", "RAPTOR_ASSETS_DIR": t.TempDir(),
		"RAPTOR_BACKUPS_S3_SECRET_KEY": "s3-secret-value", "RAPTOR_BACKUPS_S3_ACCESS_KEY": "AKIAEXAMPLE", "RAPTOR_BACKUPS_S3_BUCKET": "bkt", "RAPTOR_BACKUPS_S3_REGION": "eu-west-1",
	}
	code, stdout, stderr := runCmd(t, "", vars, "doctor")
	if code != 1 {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	for _, want := range []string{
		"FAIL  configuration: db: db.name must be set",
		"ok    secret key:",
		"FAIL  frontend build:",
		"warn  database: not looked at",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stderr, "failed") || !strings.Contains(stderr, "the panel has problems that stop it") {
		t.Errorf("stderr = %q", stderr)
	}
	for _, secret := range []string{"db-password-value", "s3-secret-value", "AKIAEXAMPLE", "iDsdi+5AzVWiY8W1v9SHn4lUxVtt4BFUkmPSUgPo5RA"} {
		if strings.Contains(stdout+stderr, secret) {
			t.Errorf("a secret is printed: %q", secret)
		}
	}
}

func TestDoctorTakesNoArguments(t *testing.T) {
	if code, _, _ := runCmd(t, "", nil, "doctor", "everything"); code != 2 {
		t.Errorf("exit %d", code)
	}
	if rootCommands()["doctor"] == nil {
		t.Error("raptor doctor is not a command")
	}
}

// A configuration that cannot even be read is a failure with a line of its own.
func TestDoctorWhenTheConfigurationCannotBeRead(t *testing.T) {
	code, stdout, _ := runCmd(t, "", map[string]string{"RAPTOR_DB_PORT": "many"}, "doctor")
	if code != 1 || !strings.Contains(stdout, "FAIL  configuration: RAPTOR_DB_PORT must be a number") {
		t.Errorf("exit %d\n%s", code, stdout)
	}
}
