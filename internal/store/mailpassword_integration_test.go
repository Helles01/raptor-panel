//go:build integration

package store_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Helles01/raptor-panel/internal/settings"
)

const mailPasswordKey = "mail:mailers:smtp:password"

func TestAdminMailPasswordOnAFreshInstallation(t *testing.T) {
	r := newRig(t)
	resetAdminTables(r)
	svc := r.service()
	count := func(q string, args ...any) (n int) { r.sql.QueryRow(q, args...).Scan(&n); return }
	rows := func() int {
		return count("SELECT COUNT(*) FROM settings WHERE `key` = ?", settings.StorageKey(mailPasswordKey))
	}

	// Clearing what is not there changes nothing, and makes no row.
	if err := svc.ClearMailPassword(r.ctx); err != nil || rows() != 0 {
		t.Fatalf("clear on nothing: %v, %d rows", err, rows())
	}
	if _, pw, err := r.db.MailSettings().Mail(r.ctx); err != nil || pw != "" {
		t.Fatalf("no password: %q, %v", pw, err)
	}

	if err := svc.SetMailPassword(r.ctx, "first-pass"); err != nil {
		t.Fatal(err)
	}
	if _, pw, err := r.db.MailSettings().Mail(r.ctx); err != nil || pw != "first-pass" {
		t.Fatalf("password = %q, %v", pw, err)
	}
	// The row of the setting is empty, and what is sealed is not the password.
	var value, sealed string
	r.sql.QueryRow("SELECT `value` FROM settings WHERE `key` = ?", settings.StorageKey(mailPasswordKey)).Scan(&value)
	r.sql.QueryRow("SELECT ciphertext FROM raptor_secrets WHERE table_name = 'settings' AND column_name = 'value'").Scan(&sealed)
	if value != "" || sealed == "" || strings.Contains(sealed, "first-pass") {
		t.Errorf("the column holds %q, the sealed value is %q", value, sealed)
	}

	// A second password replaces the first: one row, one sealed value.
	if err := svc.SetMailPassword(r.ctx, "second-pass"); err != nil {
		t.Fatal(err)
	}
	if _, pw, _ := r.db.MailSettings().Mail(r.ctx); pw != "second-pass" || rows() != 1 || count("SELECT COUNT(*) FROM raptor_secrets WHERE table_name = 'settings'") != 1 {
		t.Errorf("password %q, %d rows, %d sealed", pw, rows(), count("SELECT COUNT(*) FROM raptor_secrets WHERE table_name = 'settings'"))
	}

	if err := svc.ClearMailPassword(r.ctx); err != nil {
		t.Fatal(err)
	}
	if _, pw, err := r.db.MailSettings().Mail(r.ctx); err != nil || pw != "" || count("SELECT COUNT(*) FROM raptor_secrets WHERE table_name = 'settings'") != 0 {
		t.Errorf("after clearing: %q, %v", pw, err)
	}
}

// The row that came from the old panel holds its encrypted password in the column, and the column is not touched: the new password goes beside it.
func TestAdminMailPasswordNextToTheOldPanelsValue(t *testing.T) {
	r := newRig(t)
	resetAdminTables(r)
	svc := r.service()
	const old = "eyJpdiI6Im9sZCJ9"
	r.exec("INSERT INTO settings (`key`, `value`) VALUES (?, ?)", settings.StorageKey(mailPasswordKey), old)
	// An unrelated setting beside it.
	r.exec("INSERT INTO settings (`key`, `value`) VALUES ('settings::mail:mailers:smtp:host', 'smtp.example.com')")

	if err := svc.SetMailPassword(r.ctx, "new-pass"); err != nil {
		t.Fatal(err)
	}
	values, pw, err := r.db.MailSettings().Mail(r.ctx)
	if err != nil || pw != "new-pass" || values["mail:mailers:smtp:host"] != "smtp.example.com" {
		t.Fatalf("values %v, password %q, %v", values, pw, err)
	}
	var value string
	r.sql.QueryRow("SELECT `value` FROM settings WHERE `key` = ?", settings.StorageKey(mailPasswordKey)).Scan(&value)
	if value != old {
		t.Errorf("the old value was changed to %q", value)
	}

	if err := svc.ClearMailPassword(r.ctx); err != nil {
		t.Fatal(err)
	}
	r.sql.QueryRow("SELECT `value` FROM settings WHERE `key` = ?", settings.StorageKey(mailPasswordKey)).Scan(&value)
	if _, pw, _ := r.db.MailSettings().Mail(r.ctx); pw != "" || value != old {
		t.Errorf("after clearing: password %q, column %q", pw, value)
	}
}

// Two first passwords that arrive together must not make two rows, nor lose the row one of them made.
func TestAdminMailPasswordFirstSetUnderLoad(t *testing.T) {
	r := newRig(t)
	resetAdminTables(r)
	svc := r.service()

	const runs = 8
	var wg sync.WaitGroup
	errs := make(chan error, runs)
	for i := 0; i < runs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := svc.SetMailPassword(r.ctx, fmt.Sprintf("pass-%d", i)); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	var rows, sealed int
	r.sql.QueryRow("SELECT COUNT(*) FROM settings WHERE `key` = ?", settings.StorageKey(mailPasswordKey)).Scan(&rows)
	r.sql.QueryRow("SELECT COUNT(*) FROM raptor_secrets WHERE table_name = 'settings'").Scan(&sealed)
	_, pw, err := r.db.MailSettings().Mail(r.ctx)
	if err != nil || rows != 1 || sealed != 1 || !strings.HasPrefix(pw, "pass-") {
		t.Errorf("%d rows, %d sealed, password %q, %v", rows, sealed, pw, err)
	}
}
