package app

import (
	"reflect"

	"github.com/Helles01/raptor-panel/internal/admin"
	"github.com/Helles01/raptor-panel/internal/api/accountapi"
	"github.com/Helles01/raptor-panel/internal/api/allocapi"
	"github.com/Helles01/raptor-panel/internal/api/appallocs"
	"github.com/Helles01/raptor-panel/internal/api/applocations"
	"github.com/Helles01/raptor-panel/internal/api/appnests"
	"github.com/Helles01/raptor-panel/internal/api/appnodes"
	"github.com/Helles01/raptor-panel/internal/api/appserverdbs"
	"github.com/Helles01/raptor-panel/internal/api/appservers"
	"github.com/Helles01/raptor-panel/internal/api/appusers"
	"github.com/Helles01/raptor-panel/internal/api/authapi"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/backupapi"
	"github.com/Helles01/raptor-panel/internal/api/clientapi"
	"github.com/Helles01/raptor-panel/internal/api/controlapi"
	"github.com/Helles01/raptor-panel/internal/api/databaseapi"
	"github.com/Helles01/raptor-panel/internal/api/fileapi"
	"github.com/Helles01/raptor-panel/internal/api/pageapi"
	"github.com/Helles01/raptor-panel/internal/api/remoteapi"
	"github.com/Helles01/raptor-panel/internal/api/scheduleapi"
	"github.com/Helles01/raptor-panel/internal/api/settingsapi"
	"github.com/Helles01/raptor-panel/internal/api/subuserapi"
	"github.com/Helles01/raptor-panel/internal/auth/session"
	"github.com/Helles01/raptor-panel/internal/dbhosts"
	"github.com/Helles01/raptor-panel/internal/maintenance"
	"github.com/Helles01/raptor-panel/internal/outbox"
	"github.com/Helles01/raptor-panel/internal/schedules"
	"github.com/Helles01/raptor-panel/internal/secretmigration"
)

// Stores is everything the panel keeps in its database, as the interfaces the endpoint packages ask for: one field for each interface,
// named after its package and its name. The SQL layer fills them in; Missing says which are not filled in. It is the list that
// docs/store-contract.md describes, and a package that gets a new interface is listed here by hand, because a forgotten store would only
// show as a refused start.
type Stores struct {
	AdminDirectoryStore        admin.DirectoryStore
	AdminHostStore             admin.HostStore
	AdminStore                 admin.Store
	AccountapiActivityLog      accountapi.ActivityLog
	AccountapiNodes            accountapi.Nodes
	AccountapiStore            accountapi.Store
	AllocapiActivityLog        allocapi.ActivityLog
	AllocapiStore              allocapi.Store
	AppallocsStore             appallocs.Store
	ApplocationsStore          applocations.Store
	AppnestsStore              appnests.Store
	AppnodesStore              appnodes.Store
	AppserverdbsStore          appserverdbs.Store
	AppserversStore            appservers.Store
	AppusersActivityLog        appusers.ActivityLog
	AppusersOutbox             appusers.Outbox
	AppusersStore              appusers.Store
	AuthapiActivityLog         authapi.ActivityLog
	AuthapiNodes               authapi.Nodes
	AuthapiOutbox              authapi.Outbox
	AuthapiRecoveryTokens      authapi.RecoveryTokens
	AuthapiResets              authapi.Resets
	AuthapiUsers               authapi.Users
	AuthzAudit                 authz.Audit
	AuthzKeys                  authz.Keys
	AuthzNodes                 authz.Nodes
	AuthzServers               authz.Servers
	AuthzSettings              authz.Settings
	AuthzUsers                 authz.Users
	BackupapiActivityLog       backupapi.ActivityLog
	BackupapiStore             backupapi.Store
	ClientapiAccounts          clientapi.Accounts
	ClientapiActivityLogs      clientapi.ActivityLogs
	ClientapiServers           clientapi.Servers
	ControlapiActivityLog      controlapi.ActivityLog
	ControlapiTargets          controlapi.Targets
	DatabaseapiActivityLog     databaseapi.ActivityLog
	DatabaseapiStore           databaseapi.Store
	FileapiActivityLog         fileapi.ActivityLog
	PageapiSettings            pageapi.Settings
	PageapiUsers               pageapi.Users
	RemoteapiActivitySink      remoteapi.ActivitySink
	RemoteapiBackups           remoteapi.Backups
	RemoteapiEventLog          remoteapi.EventLog
	RemoteapiOldNodes          remoteapi.OldNodes
	RemoteapiOutbox            remoteapi.Outbox
	RemoteapiServers           remoteapi.Servers
	RemoteapiUsers             remoteapi.Users
	ScheduleapiActivityLog     scheduleapi.ActivityLog
	ScheduleapiStore           scheduleapi.Store
	SettingsapiActivityLog     settingsapi.ActivityLog
	SettingsapiStore           settingsapi.Store
	SubuserapiActivityLog      subuserapi.ActivityLog
	SubuserapiOutbox           subuserapi.Outbox
	SubuserapiStore            subuserapi.Store
	SessionStore               session.Store
	DbhostsLookup              dbhosts.Lookup
	MaintenanceActivityStore   maintenance.ActivityStore
	MaintenanceBackupStore     maintenance.BackupStore
	MaintenanceResetTokenStore maintenance.ResetTokenStore
	MaintenanceSessionStore    maintenance.SessionStore
	OutboxNodes                outbox.Nodes
	OutboxQueue                outbox.Queue
	OutboxStore                outbox.Store
	SchedulesStore             schedules.Store
	SecretmigrationStore       secretmigration.Store
}

// Missing returns the names of the fields that are not set, in the order of the struct.
func (s Stores) Missing() []string {
	var out []string
	v := reflect.ValueOf(s)
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).IsNil() {
			out = append(out, v.Type().Field(i).Name)
		}
	}
	return out
}
