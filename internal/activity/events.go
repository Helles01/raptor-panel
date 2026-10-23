package activity

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
)

// The events the panel writes itself. The activity texts of the frontend are keyed by these names and fill their
// placeholders from the properties listed in Specs.
const (
	AuthFail          = "auth:fail"
	AuthSuccess       = "auth:success"
	AuthCheckpoint    = "auth:checkpoint"
	AuthToken         = "auth:token"
	AuthRecoveryToken = "auth:recovery-token"
	AuthPasswordReset = "auth:password-reset"
	AuthResetPassword = "auth:reset-password"
	AuthIPBlocked     = "auth:ip-blocked"
	AuthSFTPFail      = "auth:sftp.fail"

	UserCreate          = "user:user.create"
	UserEmailChanged    = "user:account.email-changed"
	UserPasswordChanged = "user:account.password-changed"
	UserAPIKeyCreate    = "user:api-key.create"
	UserAPIKeyDelete    = "user:api-key.delete"
	UserSSHKeyCreate    = "user:ssh-key.create"
	UserSSHKeyDelete    = "user:ssh-key.delete"
	UserTwoFactorCreate = "user:two-factor.create"
	UserTwoFactorDelete = "user:two-factor.delete"

	ServerReinstall = "server:reinstall"

	ServerBackupStart           = "server:backup.start"
	ServerBackupDownload        = "server:backup.download"
	ServerBackupDelete          = "server:backup.delete"
	ServerBackupRestore         = "server:backup.restore"
	ServerBackupRestoreComplete = "server:backup.restore-complete"
	ServerBackupRestoreFailed   = "server:backup.restore-failed"
	ServerBackupComplete        = "server:backup.complete"
	ServerBackupFail            = "server:backup.fail"
	ServerBackupLock            = "server:backup.lock"
	ServerBackupUnlock          = "server:backup.unlock"

	ServerDatabaseCreate         = "server:database.create"
	ServerDatabaseRotatePassword = "server:database.rotate-password"
	ServerDatabaseDelete         = "server:database.delete"

	ServerFileRead            = "server:file.read"
	ServerFileCopy            = "server:file.copy"
	ServerFileCreateDirectory = "server:file.create-directory"
	ServerFileDecompress      = "server:file.decompress"
	ServerFileCompress        = "server:file.compress"
	ServerFileDelete          = "server:file.delete"
	ServerFileDownload        = "server:file.download"
	ServerFilePull            = "server:file.pull"
	ServerFileRename          = "server:file.rename"
	ServerFileWrite           = "server:file.write"

	ServerSFTPDenied = "server:sftp.denied"

	ServerAllocationCreate  = "server:allocation.create"
	ServerAllocationNotes   = "server:allocation.notes"
	ServerAllocationPrimary = "server:allocation.primary"
	ServerAllocationDelete  = "server:allocation.delete"

	ServerScheduleCreate  = "server:schedule.create"
	ServerScheduleUpdate  = "server:schedule.update"
	ServerScheduleExecute = "server:schedule.execute"
	ServerScheduleDelete  = "server:schedule.delete"
	ServerTaskCreate      = "server:task.create"
	ServerTaskUpdate      = "server:task.update"
	ServerTaskDelete      = "server:task.delete"

	ServerSettingsRename      = "server:settings.rename"
	ServerSettingsDescription = "server:settings.description"
	ServerStartupEdit         = "server:startup.edit"
	ServerStartupImage        = "server:startup.image"

	ServerSubuserCreate = "server:subuser.create"
	ServerSubuserUpdate = "server:subuser.update"
	ServerSubuserDelete = "server:subuser.delete"
)

// Spec is an event with the properties it carries.
type Spec struct {
	Event      string
	Properties []string
}

// Specs lists the events the panel writes, with the properties the reference gives each of them. An event that is not
// here is not written by the panel, and a property that is not listed is not stored.
var Specs = []Spec{
	{AuthFail, nil}, {AuthSuccess, nil}, {AuthCheckpoint, nil}, {AuthToken, nil}, {AuthRecoveryToken, nil},
	{AuthPasswordReset, nil}, {AuthResetPassword, nil},
	{AuthIPBlocked, []string{"identifier"}},
	{AuthSFTPFail, []string{"method"}},

	{UserCreate, []string{"email", "username", "admin"}},
	{UserEmailChanged, []string{"old", "new"}},
	{UserPasswordChanged, nil},
	{UserAPIKeyCreate, []string{"identifier"}},
	{UserAPIKeyDelete, []string{"identifier"}},
	{UserSSHKeyCreate, []string{"fingerprint"}},
	{UserSSHKeyDelete, []string{"fingerprint"}},
	{UserTwoFactorCreate, nil},
	{UserTwoFactorDelete, nil},

	{ServerReinstall, nil},

	{ServerBackupStart, []string{"name", "locked"}},
	{ServerBackupDownload, []string{"name"}},
	{ServerBackupDelete, []string{"name", "failed"}},
	{ServerBackupRestore, []string{"name", "truncate"}},
	{ServerBackupRestoreComplete, []string{"name"}},
	{ServerBackupRestoreFailed, []string{"name"}},
	{ServerBackupComplete, []string{"name"}},
	{ServerBackupFail, []string{"name"}},
	{ServerBackupLock, []string{"name"}},
	{ServerBackupUnlock, []string{"name"}},

	{ServerDatabaseCreate, []string{"name"}},
	{ServerDatabaseRotatePassword, []string{"name"}},
	{ServerDatabaseDelete, []string{"name"}},

	{ServerFileRead, []string{"file"}},
	{ServerFileCopy, []string{"file"}},
	{ServerFileCreateDirectory, []string{"directory", "name"}},
	{ServerFileDecompress, []string{"directory", "files"}},
	{ServerFileCompress, []string{"directory", "files"}},
	{ServerFileDelete, []string{"directory", "files"}},
	{ServerFileDownload, []string{"file"}},
	{ServerFilePull, []string{"directory", "url"}},
	{ServerFileRename, []string{"directory", "files"}},
	{ServerFileWrite, []string{"file"}},

	{ServerSFTPDenied, nil},

	{ServerAllocationCreate, []string{"allocation"}},
	{ServerAllocationNotes, []string{"allocation", "old", "new"}},
	{ServerAllocationPrimary, []string{"allocation"}},
	{ServerAllocationDelete, []string{"allocation"}},

	{ServerScheduleCreate, []string{"name"}},
	{ServerScheduleUpdate, []string{"name", "active"}},
	{ServerScheduleExecute, []string{"name"}},
	{ServerScheduleDelete, []string{"name"}},
	{ServerTaskCreate, []string{"name", "action", "payload"}},
	{ServerTaskUpdate, []string{"name", "action", "payload"}},
	{ServerTaskDelete, []string{"name"}},

	{ServerSettingsRename, []string{"old", "new"}},
	{ServerSettingsDescription, []string{"old", "new"}},
	{ServerStartupEdit, []string{"variable", "old", "new"}},
	{ServerStartupImage, []string{"old", "new"}},

	{ServerSubuserCreate, []string{"email", "permissions"}},
	{ServerSubuserUpdate, []string{"email", "old", "new", "revoked"}},
	{ServerSubuserDelete, []string{"email", "revoked"}},
}

// Properties returns the JSON object to store for an event the panel writes. The values must be exactly the
// properties of the event: a missing one would leave a hole in the text the user reads, and an extra one would store
// what no screen shows.
func Properties(event string, values map[string]any) (string, error) {
	i := slices.IndexFunc(Specs, func(s Spec) bool { return s.Event == event })
	if i < 0 {
		return "", fmt.Errorf("activity: %q is not an event of the panel", event)
	}
	want := Specs[i].Properties

	var missing, extra []string
	for _, p := range want {
		if _, ok := values[p]; !ok {
			missing = append(missing, p)
		}
	}
	for k := range values {
		if !slices.Contains(want, k) {
			extra = append(extra, k)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		sort.Strings(extra)
		return "", fmt.Errorf("activity: event %s lacks properties %v and has unknown ones %v", event, missing, extra)
	}

	if values == nil {
		values = map[string]any{}
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("activity: encode properties of %s: %w", event, err)
	}
	return string(raw), nil
}

// daemonEvents are the events a node reports, as Wings v1.13.4 sends them. A node may report only these: what else
// carries the name of a panel event (a subuser added, a backup deleted) would be a forged record of something the panel
// did, in the log that servers' owners read (C-034).
var daemonEvents = []string{
	"server:power.start", "server:power.stop", "server:power.restart", "server:power.kill",
	"server:console.command",
	"server:file.uploaded",
	"server:sftp.write", "server:sftp.create", "server:sftp.create-directory", "server:sftp.rename", "server:sftp.delete",
}

// DaemonEvent reports whether a node may report the event.
func DaemonEvent(event string) bool { return slices.Contains(daemonEvents, event) }

// DaemonEvents returns the events a node may report.
func DaemonEvents() []string { return slices.Clone(daemonEvents) }

// RedactURL is the form of a URL that goes into the log: the scheme, host and path. The user name, password and query
// are left out, because a link to a file often carries a credential or a signature there, and everyone who may read the
// log of the server would see it. A text that is not an http or https URL is not repeated at all.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	clean := url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}
	return clean.String()
}
