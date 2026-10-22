// Package permissions holds the subuser permissions of a server: the tree the frontend shows, and the rules for
// assigning and checking them. The names and descriptions are those of the reference panel (app/Models/Permission.php).
package permissions

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
)

// The permission names, as "group.key".
const (
	WebsocketConnect = "websocket.connect"

	ControlConsole = "control.console"
	ControlStart   = "control.start"
	ControlStop    = "control.stop"
	ControlRestart = "control.restart"

	UserCreate = "user.create"
	UserRead   = "user.read"
	UserUpdate = "user.update"
	UserDelete = "user.delete"

	FileCreate      = "file.create"
	FileRead        = "file.read"
	FileReadContent = "file.read-content"
	FileUpdate      = "file.update"
	FileDelete      = "file.delete"
	FileArchive     = "file.archive"
	FileSFTP        = "file.sftp"

	BackupCreate   = "backup.create"
	BackupRead     = "backup.read"
	BackupDelete   = "backup.delete"
	BackupDownload = "backup.download"
	BackupRestore  = "backup.restore"

	AllocationRead   = "allocation.read"
	AllocationCreate = "allocation.create"
	AllocationUpdate = "allocation.update"
	AllocationDelete = "allocation.delete"

	StartupRead        = "startup.read"
	StartupUpdate      = "startup.update"
	StartupDockerImage = "startup.docker-image"

	DatabaseCreate       = "database.create"
	DatabaseRead         = "database.read"
	DatabaseUpdate       = "database.update"
	DatabaseDelete       = "database.delete"
	DatabaseViewPassword = "database.view_password"

	ScheduleCreate = "schedule.create"
	ScheduleRead   = "schedule.read"
	ScheduleUpdate = "schedule.update"
	ScheduleDelete = "schedule.delete"

	SettingsRename    = "settings.rename"
	SettingsReinstall = "settings.reinstall"

	ActivityRead = "activity.read"
)

// Wildcard is held by server owners and administrators: every permission except the admin.* ones.
const Wildcard = "*"

// Key is one permission of a group.
type Key struct {
	Name        string
	Description string
}

// Group is a set of related permissions.
type Group struct {
	Name        string
	Description string
	Keys        []Key
}

// Tree is the ordered list of groups. Its JSON form is an object keyed by group name, in this order, which is what the
// frontend displays: {"control":{"description":"…","keys":{"console":"…"}}}.
type Tree []Group

// MarshalJSON writes the tree as an object that keeps the order of the groups and keys.
func (t Tree) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, g := range t {
		if i > 0 {
			b.WriteByte(',')
		}
		name, _ := json.Marshal(g.Name)
		desc, _ := json.Marshal(g.Description)
		b.Write(name)
		b.WriteString(`:{"description":`)
		b.Write(desc)
		b.WriteString(`,"keys":{`)
		for j, k := range g.Keys {
			if j > 0 {
				b.WriteByte(',')
			}
			kn, _ := json.Marshal(k.Name)
			kd, _ := json.Marshal(k.Description)
			b.Write(kn)
			b.WriteByte(':')
			b.Write(kd)
		}
		b.WriteString("}}")
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// All returns the permission tree.
func All() Tree {
	return Tree{
		{"websocket", "Allows the user to connect to the server websocket, giving them access to view console output and realtime server stats.", []Key{
			{"connect", "Allows a user to connect to the websocket instance for a server to stream the console."},
		}},
		{"control", "Permissions that control a user's ability to control the power state of a server, or send commands.", []Key{
			{"console", "Allows a user to send commands to the server instance via the console."},
			{"start", "Allows a user to start the server if it is stopped."},
			{"stop", "Allows a user to stop a server if it is running."},
			{"restart", "Allows a user to perform a server restart. This allows them to start the server if it is offline, but not put the server in a completely stopped state."},
		}},
		{"user", "Permissions that allow a user to manage other subusers on a server. They will never be able to edit their own account, or assign permissions they do not have themselves.", []Key{
			{"create", "Allows a user to create new subusers for the server."},
			{"read", "Allows the user to view subusers and their permissions for the server."},
			{"update", "Allows a user to modify other subusers."},
			{"delete", "Allows a user to delete a subuser from the server."},
		}},
		{"file", "Permissions that control a user's ability to modify the filesystem for this server.", []Key{
			{"create", "Allows a user to create additional files and folders via the Panel or direct upload."},
			{"read", "Allows a user to view the contents of a directory, but not view the contents of or download files."},
			{"read-content", "Allows a user to view the contents of a given file. This will also allow the user to download files."},
			{"update", "Allows a user to update the contents of an existing file or directory."},
			{"delete", "Allows a user to delete files or directories."},
			{"archive", "Allows a user to archive the contents of a directory as well as decompress existing archives on the system."},
			{"sftp", "Allows a user to connect to SFTP and manage server files using the other assigned file permissions."},
		}},
		{"backup", "Permissions that control a user's ability to generate and manage server backups.", []Key{
			{"create", "Allows a user to create new backups for this server."},
			{"read", "Allows a user to view all backups that exist for this server."},
			{"delete", "Allows a user to remove backups from the system."},
			{"download", "Allows a user to download a backup for the server. Danger: this allows a user to access all files for the server in the backup."},
			{"restore", "Allows a user to restore a backup for the server. Danger: this allows the user to delete all of the server files in the process."},
		}},
		{"allocation", "Permissions that control a user's ability to modify the port allocations for this server.", []Key{
			{"read", "Allows a user to view all allocations currently assigned to this server. Users with any level of access to this server can always view the primary allocation."},
			{"create", "Allows a user to assign additional allocations to the server."},
			{"update", "Allows a user to change the primary server allocation and attach notes to each allocation."},
			{"delete", "Allows a user to delete an allocation from the server."},
		}},
		{"startup", "Permissions that control a user's ability to view this server's startup parameters.", []Key{
			{"read", "Allows a user to view the startup variables for a server."},
			{"update", "Allows a user to modify the startup variables for the server."},
			{"docker-image", "Allows a user to modify the Docker image used when running the server."},
		}},
		{"database", "Permissions that control a user's access to the database management for this server.", []Key{
			{"create", "Allows a user to create a new database for this server."},
			{"read", "Allows a user to view the database associated with this server."},
			{"update", "Allows a user to rotate the password on a database instance. If the user does not have the view_password permission they will not see the updated password."},
			{"delete", "Allows a user to remove a database instance from this server."},
			{"view_password", "Allows a user to view the password associated with a database instance for this server."},
		}},
		{"schedule", "Permissions that control a user's access to the schedule management for this server.", []Key{
			{"create", "Allows a user to create new schedules for this server."},
			{"read", "Allows a user to view schedules and the tasks associated with them for this server."},
			{"update", "Allows a user to update schedules and schedule tasks for this server."},
			{"delete", "Allows a user to delete schedules for this server."},
		}},
		{"settings", "Permissions that control a user's access to the settings for this server.", []Key{
			{"rename", "Allows a user to rename this server and change the description of it."},
			{"reinstall", "Allows a user to trigger a reinstall of this server."},
		}},
		{"activity", "Permissions that control a user's access to the server activity logs.", []Key{
			{"read", "Allows a user to view the activity logs for the server."},
		}},
	}
}

// Names returns every assignable permission as "group.key", in tree order.
func Names() []string {
	var out []string
	for _, g := range All() {
		for _, k := range g.Keys {
			out = append(out, g.Name+"."+k.Name)
		}
	}
	return out
}

// Valid reports whether name is an assignable permission. The wildcard and the admin.* permissions are not.
func Valid(name string) bool {
	for _, n := range Names() {
		if n == name {
			return true
		}
	}
	return false
}

// Normalize makes the permission set to store for a subuser from what a client sent: only known permissions, no
// duplicates, in the order sent, and websocket.connect is always included (a user without it could not even open the
// console).
func Normalize(requested []string) []string {
	valid := map[string]bool{}
	for _, n := range Names() {
		valid[n] = true
	}

	seen := map[string]bool{}
	out := []string{}
	for _, p := range requested {
		if valid[p] && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if !seen[WebsocketConnect] {
		out = append(out, WebsocketConnect)
	}
	return out
}

// Has reports whether a held permission list contains wanted. The wildcard covers everything except admin.* names, as
// in Wings.
func Has(held []string, wanted string) bool {
	for _, h := range held {
		if h == wanted || (h == Wildcard && !strings.HasPrefix(wanted, "admin")) {
			return true
		}
	}
	return false
}

// CanAssign reports whether someone holding held may give a subuser the permissions in requested: only ones they hold
// themselves, so a name that does not exist is refused too. Whoever holds the wildcard (owners and administrators) may
// ask for anything; what cannot be assigned is then dropped by Normalize, which is the order the reference works in.
func CanAssign(held, requested []string) bool {
	if slices.Contains(held, Wildcard) {
		return true
	}
	for _, p := range requested {
		if !Has(held, p) {
			return false
		}
	}
	return true
}
