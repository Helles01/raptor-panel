package store

import (
	"context"

	"github.com/Helles01/raptor-panel/internal/api/allocapi"
	"github.com/Helles01/raptor-panel/internal/api/backupapi"
	"github.com/Helles01/raptor-panel/internal/api/databaseapi"
	"github.com/Helles01/raptor-panel/internal/api/fileapi"
	"github.com/Helles01/raptor-panel/internal/api/scheduleapi"
	"github.com/Helles01/raptor-panel/internal/api/settingsapi"
	"github.com/Helles01/raptor-panel/internal/api/subuserapi"
)

// The routes of a server write their entries to the one activity log, each with its own entry type. An entry has the server as a subject, and the thing it
// is about as another, which is how the activity of a server is listed.
const (
	databaseModel = "Pterodactyl\\Models\\Database"
	allocModel    = "Pterodactyl\\Models\\Allocation"
	scheduleModel = "Pterodactyl\\Models\\Schedule"
	taskModel     = "Pterodactyl\\Models\\Task"
)

func (d *DB) AllocationActivity() allocapi.ActivityLog  { return allocActivity{d} }
func (d *DB) SubuserActivity() subuserapi.ActivityLog   { return subuserActivity{d} }
func (d *DB) ScheduleActivity() scheduleapi.ActivityLog { return scheduleActivity{d} }
func (d *DB) BackupActivity() backupapi.ActivityLog     { return backupActivity{d} }
func (d *DB) DatabaseActivity() databaseapi.ActivityLog { return databaseActivity{d} }
func (d *DB) FileActivity() fileapi.ActivityLog         { return fileActivity{d} }
func (d *DB) SettingsActivity() settingsapi.ActivityLog { return settingsActivity{d} }

type (
	allocActivity    struct{ *DB }
	subuserActivity  struct{ *DB }
	scheduleActivity struct{ *DB }
	backupActivity   struct{ *DB }
	databaseActivity struct{ *DB }
	fileActivity     struct{ *DB }
	settingsActivity struct{ *DB }
)

// keyID is the id of the key a call came with, 0 for none.
func keyID(p *uint64) uint64 {
	if p == nil {
		return 0
	}
	return *p
}

func (s allocActivity) Log(ctx context.Context, e allocapi.Entry) error {
	return s.logActivity(ctx, entry{Event: e.Event, Actor: e.ActorID, APIKeyID: keyID(e.APIKeyID), IP: e.IP, Properties: e.Properties,
		Subjects: []subject{{serverModel, e.ServerID}, {allocModel, e.AllocationID}}})
}

func (s subuserActivity) Log(ctx context.Context, e subuserapi.Entry) error {
	return s.logActivity(ctx, entry{Event: e.Event, Actor: e.ActorID, APIKeyID: keyID(e.APIKeyID), IP: e.IP, Properties: e.Properties,
		Subjects: []subject{{serverModel, e.ServerID}, {userModel, e.SubjectUserID}}})
}

func (s scheduleActivity) Log(ctx context.Context, e scheduleapi.Entry) error {
	return s.logActivity(ctx, entry{Event: e.Event, Actor: e.ActorID, APIKeyID: keyID(e.APIKeyID), IP: e.IP, Properties: e.Properties,
		Subjects: []subject{{serverModel, e.ServerID}, {scheduleModel, e.ScheduleID}, {taskModel, e.TaskID}}})
}

func (s backupActivity) Log(ctx context.Context, e backupapi.Entry) error {
	return s.logActivity(ctx, entry{Event: e.Event, Actor: e.ActorID, APIKeyID: keyID(e.APIKeyID), IP: e.IP, Properties: e.Properties,
		Subjects: []subject{{serverModel, e.ServerID}, {backupModel, e.BackupID}}})
}

func (s databaseActivity) Log(ctx context.Context, e databaseapi.Entry) error {
	return s.logActivity(ctx, entry{Event: e.Event, Actor: e.ActorID, APIKeyID: keyID(e.APIKeyID), IP: e.IP, Properties: e.Properties,
		Subjects: []subject{{serverModel, e.ServerID}, {databaseModel, e.DatabaseID}}})
}

func (s fileActivity) Log(ctx context.Context, e fileapi.Entry) error {
	return s.logActivity(ctx, entry{Event: e.Event, Actor: e.ActorID, APIKeyID: keyID(e.APIKeyID), IP: e.IP, Properties: e.Properties,
		Subjects: []subject{{serverModel, e.ServerID}}})
}

func (s settingsActivity) Log(ctx context.Context, e settingsapi.Entry) error {
	return s.logActivity(ctx, entry{Event: e.Event, Actor: e.ActorID, APIKeyID: keyID(e.APIKeyID), IP: e.IP, Properties: e.Properties,
		Subjects: []subject{{serverModel, e.ServerID}}})
}
