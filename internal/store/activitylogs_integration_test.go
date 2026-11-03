//go:build integration

package store_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/Helles01/raptor-panel/internal/api/allocapi"
	"github.com/Helles01/raptor-panel/internal/api/backupapi"
	"github.com/Helles01/raptor-panel/internal/api/databaseapi"
	"github.com/Helles01/raptor-panel/internal/api/fileapi"
	"github.com/Helles01/raptor-panel/internal/api/scheduleapi"
	"github.com/Helles01/raptor-panel/internal/api/settingsapi"
	"github.com/Helles01/raptor-panel/internal/api/subuserapi"
)

// Every route of a server writes its entries with the server as a subject, and the thing the entry is about as another one; the key it was made with is kept.
func TestServerActivityLogs(t *testing.T) {
	f := clientFx(t)
	r := f.r
	key := uint64(7)
	r.exec("INSERT INTO api_keys (id, user_id, key_type, identifier, token) VALUES (7, 1, 1, 'ptlc_key0000001', '')")
	props := `{"ip":"203.0.113.9","name":"x"}`
	db := r.db
	log := []struct {
		name     string
		do       func(context.Context) error
		subjects []string // the type and the id of every subject, in order
	}{
		{"allocation", func(c context.Context) error {
			return db.AllocationActivity().Log(c, allocapi.Entry{Event: "server:allocation.create", ServerID: 1, AllocationID: 5, ActorID: 1, APIKeyID: &key, IP: "203.0.113.9", Properties: props})
		}, []string{"Server:1", "Allocation:5"}},
		{"subuser", func(c context.Context) error {
			return db.SubuserActivity().Log(c, subuserapi.Entry{Event: "server:subuser.create", ServerID: 1, SubjectUserID: 2, ActorID: 1, APIKeyID: &key, IP: "203.0.113.9", Properties: props})
		}, []string{"Server:1", "User:2"}},
		{"schedule", func(c context.Context) error {
			return db.ScheduleActivity().Log(c, scheduleapi.Entry{Event: "server:task.create", ServerID: 1, ScheduleID: 3, TaskID: 4, ActorID: 1, APIKeyID: &key, IP: "203.0.113.9", Properties: props})
		}, []string{"Server:1", "Schedule:3", "Task:4"}},
		{"schedule without a task", func(c context.Context) error {
			return db.ScheduleActivity().Log(c, scheduleapi.Entry{Event: "server:schedule.create", ServerID: 1, ScheduleID: 3, ActorID: 1, IP: "203.0.113.9", Properties: props})
		}, []string{"Server:1", "Schedule:3"}},
		{"backup", func(c context.Context) error {
			return db.BackupActivity().Log(c, backupapi.Entry{Event: "server:backup.start", ServerID: 1, BackupID: 6, ActorID: 1, APIKeyID: &key, IP: "203.0.113.9", Properties: props})
		}, []string{"Server:1", "Backup:6"}},
		{"database", func(c context.Context) error {
			return db.DatabaseActivity().Log(c, databaseapi.Entry{Event: "server:database.create", ServerID: 1, DatabaseID: 8, ActorID: 1, APIKeyID: &key, IP: "203.0.113.9", Properties: props})
		}, []string{"Server:1", "Database:8"}},
		{"file", func(c context.Context) error {
			return db.FileActivity().Log(c, fileapi.Entry{Event: "server:file.read", ServerID: 1, ActorID: 1, APIKeyID: &key, IP: "203.0.113.9", Properties: props})
		}, []string{"Server:1"}},
		{"settings", func(c context.Context) error {
			return db.SettingsActivity().Log(c, settingsapi.Entry{Event: "server:startup.edit", ServerID: 1, ActorID: 1, APIKeyID: &key, IP: "203.0.113.9", Properties: props})
		}, []string{"Server:1"}},
	}
	for _, c := range log {
		r.truncate("activity_logs", "activity_log_subjects")
		if err := c.do(r.ctx); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var event, ip, properties, actorType string
		var actor, apiKey *uint64
		if err := r.sql.QueryRow("SELECT event, ip, properties, actor_type, actor_id, api_key_id FROM activity_logs").Scan(&event, &ip, &properties, &actorType, &actor, &apiKey); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if ip != "203.0.113.9" || properties != props || actorType != "Pterodactyl\\Models\\User" || actor == nil || *actor != 1 {
			t.Errorf("%s: entry = %s %s %s %s %v", c.name, event, ip, properties, actorType, actor)
		}
		if wantKey := c.name != "schedule without a task"; wantKey != (apiKey != nil) || (apiKey != nil && *apiKey != 7) {
			t.Errorf("%s: key %v", c.name, apiKey)
		}
		rows, err := r.sql.Query("SELECT subject_type, subject_id FROM activity_log_subjects ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for rows.Next() {
			var kind string
			var id uint64
			rows.Scan(&kind, &id)
			got = append(got, kind[len("Pterodactyl\\Models\\"):]+":"+strconv.FormatUint(id, 10))
		}
		rows.Close()
		if len(got) != len(c.subjects) {
			t.Errorf("%s: subjects %v, want %v", c.name, got, c.subjects)
			continue
		}
		for i := range got {
			if got[i] != c.subjects[i] {
				t.Errorf("%s: subjects %v, want %v", c.name, got, c.subjects)
				break
			}
		}
	}
}
