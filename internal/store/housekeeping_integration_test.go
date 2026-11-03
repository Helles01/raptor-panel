//go:build integration

package store_test

import (
	"fmt"
	"testing"
	"time"
)

func TestMaintenanceActivityChunks(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.MaintenanceActivity()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-90 * 24 * time.Hour)

	// 5,203 old entries (one more chunk than a statement deletes), two new ones, and subjects that go with the entries through the foreign key.
	for from := 0; from < 5203; from += 1000 {
		values, args := "", []any{}
		for i := from; i < from+1000 && i < 5203; i++ {
			if values != "" {
				values += ","
			}
			values += "('server:old', '', '{}', ?)"
			args = append(args, cutoff.Add(-time.Duration(i+1)*time.Minute))
		}
		r.exec("INSERT INTO activity_logs (event, ip, properties, `timestamp`) VALUES "+values, args...)
	}
	r.exec("INSERT INTO activity_logs (event, ip, properties, `timestamp`) VALUES ('server:new', '', '{}', ?), ('server:edge', '', '{}', ?)", now, cutoff)
	r.exec("INSERT INTO activity_log_subjects (activity_log_id, subject_id, subject_type) SELECT id, 1, 'Pterodactyl\\Models\\Server' FROM activity_logs")

	n, err := st.DeleteActivityBefore(r.ctx, cutoff)
	if err != nil || n != 5203 {
		t.Fatalf("deleted %d, %v", n, err)
	}
	if left := countRows(r, "activity_logs"); left != 2 {
		t.Errorf("%d entries are left", left)
	}
	if left := countRows(r, "activity_log_subjects"); left != 2 {
		t.Errorf("%d subjects are left", left)
	}
	if n, _ := st.DeleteActivityBefore(r.ctx, cutoff); n != 0 {
		t.Errorf("deleted %d the second time", n)
	}
}

func TestMaintenanceResetTokens(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.MaintenanceResetTokens()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	r.exec("INSERT INTO password_resets (email, token, created_at) VALUES ('a@example.com', 'old', ?), ('b@example.com', 'edge', ?), ('c@example.com', 'new', ?)",
		now.Add(-2*time.Hour), now.Add(-time.Hour), now)
	n, err := st.DeleteResetTokensBefore(r.ctx, now.Add(-time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("deleted %d, %v", n, err)
	}
	if countWhere(r, "password_resets", "token = 'old'") != 0 || countRows(r, "password_resets") != 2 {
		t.Error("the wrong tokens went")
	}
}

func TestMaintenanceSessions(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.MaintenanceSessions()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	idle, lifetime := now.Add(-2*time.Hour), now.Add(-24*time.Hour)
	payload := func(created time.Time) string {
		return fmt.Sprintf(`{"created_at":%q}`, created.UTC().Format(time.RFC3339Nano))
	}
	ins := func(id string, last time.Time, payload string) {
		t.Helper()
		r.exec("INSERT INTO sessions (id, payload, last_activity) VALUES (?, ?, ?)", id, payload, last.Unix())
	}
	ins("fresh", now, payload(now.Add(-time.Hour)))
	ins("idle", idle.Add(-time.Minute), payload(now.Add(-time.Hour)))
	ins("aged", now, payload(lifetime.Add(-time.Minute)))
	ins("aged-nanoseconds", now, payload(lifetime.Add(-time.Second).Add(123456789*time.Nanosecond)))
	ins("edge", now, payload(lifetime))
	ins("laravel", now, "YTozOntzOjY6Il90b2tlbiI7fQ==") // a payload that is not Raptor's has no creation time: only the idle rule reaches it
	ins("laravel-idle", idle.Add(-time.Hour), "YTozOntzOjY6Il90b2tlbiI7fQ==")

	n, err := st.DeleteExpiredSessions(r.ctx, idle, lifetime)
	if err != nil || n != 4 {
		t.Fatalf("deleted %d, %v", n, err)
	}
	for _, id := range []string{"fresh", "edge", "laravel"} {
		if countWhere(r, "sessions", "id = '"+id+"'") != 1 {
			t.Errorf("session %s was deleted", id)
		}
	}
	for _, id := range []string{"idle", "aged", "aged-nanoseconds", "laravel-idle"} {
		if countWhere(r, "sessions", "id = '"+id+"'") != 0 {
			t.Errorf("session %s is left", id)
		}
	}
}
