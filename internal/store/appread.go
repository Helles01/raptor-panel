package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/nodes"
	"github.com/Helles01/raptor-panel/internal/servers"
)

// The records of the Application API are read with one SELECT of fixed columns for each kind, used for a list and for one record alike. The
// name of a table or a column in a list comes from the allow-listed spec of the request (listquery), and every value is an argument.

type scanner interface{ Scan(...any) error }

func at(t sql.NullTime) time.Time { return timeOf(t) }

func nullInt64(n sql.NullInt64) *uint64 {
	if !n.Valid {
		return nil
	}
	v := uint64(n.Int64)
	return &v
}

func rawJSON(s sql.NullString) json.RawMessage {
	if !s.Valid || s.String == "" {
		return nil
	}
	return json.RawMessage(s.String)
}

// --- users

const userSelect = "SELECT `users`.`id`, `users`.`external_id`, `users`.`uuid`, `users`.`username`, `users`.`email`, `users`.`name_first`, `users`.`name_last`, " +
	"`users`.`language`, `users`.`root_admin`, `users`.`use_totp`, `users`.`created_at`, `users`.`updated_at` FROM `users`"

func scanUser(r scanner) (appkit.User, error) {
	var (
		u              appkit.User
		ext, first, ln sql.NullString
		admin, totp    bool
		created, upd   sql.NullTime
	)
	if err := r.Scan(&u.ID, &ext, &u.UUID, &u.Username, &u.Email, &first, &ln, &u.Language, &admin, &totp, &created, &upd); err != nil {
		return u, err
	}
	u.ExternalID, u.FirstName, u.LastName, u.Admin, u.TwoFactor, u.CreatedAt, u.UpdatedAt = nullStr(ext), first.String, ln.String, admin, totp, at(created), at(upd)
	return u, nil
}

// --- locations

const locationSelect = "SELECT `locations`.`id`, `locations`.`short`, `locations`.`long`, `locations`.`created_at`, `locations`.`updated_at` FROM `locations`"

func scanLocation(r scanner) (appkit.Location, error) {
	var (
		l            appkit.Location
		long         sql.NullString
		created, upd sql.NullTime
	)
	if err := r.Scan(&l.ID, &l.Short, &long, &created, &upd); err != nil {
		return l, err
	}
	l.Long, l.CreatedAt, l.UpdatedAt = nullStr(long), at(created), at(upd)
	return l, nil
}

// --- nodes

const nodeSelect = "SELECT `nodes`.`id`, `nodes`.`uuid`, `nodes`.`public`, `nodes`.`name`, `nodes`.`description`, `nodes`.`location_id`, `nodes`.`fqdn`, `nodes`.`scheme`, " +
	"`nodes`.`behind_proxy`, `nodes`.`maintenance_mode`, `nodes`.`memory`, `nodes`.`memory_overallocate`, `nodes`.`disk`, `nodes`.`disk_overallocate`, `nodes`.`upload_size`, " +
	"`nodes`.`daemon_token_id`, `nodes`.`daemonListen`, `nodes`.`daemonSFTP`, `nodes`.`daemonBase`, `nodes`.`created_at`, `nodes`.`updated_at`, " +
	"COALESCE((SELECT SUM(s.`memory`) FROM `servers` s WHERE s.`node_id` = `nodes`.`id`), 0), COALESCE((SELECT SUM(s.`disk`) FROM `servers` s WHERE s.`node_id` = `nodes`.`id`), 0) FROM `nodes`"

func scanNode(r scanner) (appkit.Node, error) {
	var (
		n                  appkit.Node
		public             int
		desc               sql.NullString
		behind, maint      bool
		memory, disk       uint64
		memOver, diskOver  int64
		upload             uint64
		listen, sftp       int
		created, upd       sql.NullTime
		allocMem, allocDsk int64
	)
	s := &n.Settings
	if err := r.Scan(&n.ID, &n.UUID, &public, &s.Name, &desc, &s.LocationID, &s.FQDN, &s.Scheme, &behind, &maint, &memory, &memOver, &disk, &diskOver, &upload,
		&n.TokenID, &listen, &sftp, &s.DaemonBase, &created, &upd, &allocMem, &allocDsk); err != nil {
		return n, err
	}
	s.Description, s.Public, s.BehindProxy, s.MaintenanceMode = desc.String, public != 0, behind, maint
	s.Memory, s.MemoryOverallocate, s.Disk, s.DiskOverallocate, s.UploadSize = int64(memory), memOver, int64(disk), diskOver, int64(upload)
	s.DaemonListen, s.DaemonSFTP = listen, sftp
	n.AllocatedMemory, n.AllocatedDisk, n.CreatedAt, n.UpdatedAt = allocMem, allocDsk, at(created), at(upd)
	return n, nil
}

// --- allocations

const allocationSelect = "SELECT `allocations`.`id`, `allocations`.`node_id`, `allocations`.`ip`, `allocations`.`ip_alias`, `allocations`.`port`, `allocations`.`notes`, `allocations`.`server_id` FROM `allocations`"

func scanAllocation(r scanner) (appkit.Allocation, error) {
	var (
		a            appkit.Allocation
		alias, notes sql.NullString
		server       sql.NullInt64
	)
	if err := r.Scan(&a.ID, &a.NodeID, &a.IP, &alias, &a.Port, &notes, &server); err != nil {
		return a, err
	}
	a.Alias, a.Notes, a.ServerID = nullStr(alias), nullStr(notes), nullInt64(server)
	return a, nil
}

// --- nests and eggs

const nestSelect = "SELECT `nests`.`id`, `nests`.`uuid`, `nests`.`author`, `nests`.`name`, `nests`.`description`, `nests`.`created_at`, `nests`.`updated_at` FROM `nests`"

func scanNest(r scanner) (appkit.Nest, error) {
	var (
		n            appkit.Nest
		desc         sql.NullString
		created, upd sql.NullTime
	)
	if err := r.Scan(&n.ID, &n.UUID, &n.Author, &n.Name, &desc, &created, &upd); err != nil {
		return n, err
	}
	n.Description, n.CreatedAt, n.UpdatedAt = nullStr(desc), at(created), at(upd)
	return n, nil
}

const eggSelect = "SELECT `eggs`.`id`, `eggs`.`uuid`, `eggs`.`nest_id`, `eggs`.`author`, `eggs`.`name`, `eggs`.`description`, `eggs`.`docker_images`, `eggs`.`config_files`, " +
	"`eggs`.`config_startup`, `eggs`.`config_stop`, `eggs`.`config_logs`, `eggs`.`file_denylist`, `eggs`.`config_from`, `eggs`.`startup`, `eggs`.`script_is_privileged`, " +
	"`eggs`.`script_install`, `eggs`.`script_entry`, `eggs`.`script_container`, `eggs`.`copy_script_from`, `eggs`.`created_at`, `eggs`.`updated_at` FROM `eggs`"

func scanEgg(r scanner) (appkit.Egg, error) {
	var (
		e                                                              appkit.Egg
		desc, images, files, startup, stop, logs, denylist, scriptInst sql.NullString
		startupCmd                                                     sql.NullString
		from, copyFrom                                                 sql.NullInt64
		privileged                                                     bool
		created, upd                                                   sql.NullTime
	)
	if err := r.Scan(&e.ID, &e.UUID, &e.NestID, &e.Author, &e.Name, &desc, &images, &files, &startup, &stop, &logs, &denylist, &from, &startupCmd,
		&privileged, &scriptInst, &e.ScriptEntry, &e.ScriptContainer, &copyFrom, &created, &upd); err != nil {
		return e, err
	}
	e.Description, e.DockerImages = nullStr(desc), orderedAppImages(images.String)
	e.ConfigFiles, e.ConfigStartup, e.ConfigStop, e.ConfigLogs = rawJSON(files), rawJSON(startup), nullStr(stop), rawJSON(logs)
	e.FileDenylist, e.ConfigFrom, e.Startup = jsonStrings(denylist), nullInt64(from), startupCmd.String
	e.ScriptPrivileged, e.ScriptInstall, e.CopyScriptFrom = privileged, nullStr(scriptInst), nullInt64(copyFrom)
	e.CreatedAt, e.UpdatedAt = at(created), at(upd)
	return e, nil
}

// orderedAppImages reads the object of the images in the order of the file.
func orderedAppImages(raw string) []appkit.DockerImage {
	var out []appkit.DockerImage
	for _, im := range orderedImages(raw) {
		out = append(out, appkit.DockerImage{Name: im.Label, Image: im.Image})
	}
	return out
}

const eggVariableSelect = "SELECT `egg_variables`.`id`, `egg_variables`.`egg_id`, `egg_variables`.`name`, `egg_variables`.`description`, `egg_variables`.`env_variable`, " +
	"`egg_variables`.`default_value`, `egg_variables`.`user_viewable`, `egg_variables`.`user_editable`, `egg_variables`.`rules`, `egg_variables`.`created_at`, `egg_variables`.`updated_at` FROM `egg_variables`"

func scanEggVariable(r scanner) (appkit.EggVariable, error) {
	var (
		v            appkit.EggVariable
		rules        sql.NullString
		view, edit   bool
		created, upd sql.NullTime
	)
	if err := r.Scan(&v.ID, &v.EggID, &v.Name, &v.Description, &v.EnvVariable, &v.DefaultValue, &view, &edit, &rules, &created, &upd); err != nil {
		return v, err
	}
	v.UserViewable, v.UserEditable, v.Rules, v.CreatedAt, v.UpdatedAt = view, edit, rules.String, at(created), at(upd)
	return v, nil
}

// --- server databases

const databaseSelect = "SELECT `databases`.`id`, `databases`.`server_id`, `databases`.`database_host_id`, `databases`.`database`, `databases`.`username`, `databases`.`remote`, " +
	"`databases`.`max_connections`, `databases`.`created_at`, `databases`.`updated_at` FROM `databases`"

func scanDatabase(r scanner) (appkit.ServerDatabase, error) {
	var (
		d            appkit.ServerDatabase
		maxConn      sql.NullInt64
		created, upd sql.NullTime
	)
	if err := r.Scan(&d.ID, &d.ServerID, &d.HostID, &d.Name, &d.Username, &d.Remote, &maxConn, &created, &upd); err != nil {
		return d, err
	}
	d.MaxConnections, d.CreatedAt, d.UpdatedAt = int(maxConn.Int64), at(created), at(upd)
	return d, nil
}

const hostSelect = "SELECT `database_hosts`.`id`, `database_hosts`.`name`, `database_hosts`.`host`, `database_hosts`.`port`, `database_hosts`.`username`, `database_hosts`.`node_id`, " +
	"`database_hosts`.`created_at`, `database_hosts`.`updated_at` FROM `database_hosts`"

func scanHost(r scanner) (appkit.DatabaseHost, error) {
	var (
		h            appkit.DatabaseHost
		node         sql.NullInt64
		created, upd sql.NullTime
	)
	if err := r.Scan(&h.ID, &h.Name, &h.Host, &h.Port, &h.Username, &node, &created, &upd); err != nil {
		return h, err
	}
	h.NodeID, h.CreatedAt, h.UpdatedAt = nullInt64(node), at(created), at(upd)
	return h, nil
}

// --- the generic readers

// all reads every row of a SELECT.
func all[T any](ctx context.Context, d *DB, scan func(scanner) (T, error), query string, args ...any) ([]T, error) {
	rows, err := d.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// one reads one row, or answers appkit.ErrNotFound.
func one[T any](ctx context.Context, d *DB, scan func(scanner) (T, error), query string, args ...any) (T, error) {
	v, err := scan(d.sql.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return v, appkit.ErrNotFound
	}
	return v, err
}

// pageOf reads one page of a list: the total of the rows the condition leaves, and the rows of the page in the order given. from is what follows
// "FROM `table`" in the select (joins), where the condition without its WHERE, and order a complete ORDER BY clause.
func pageOf[T any](ctx context.Context, d *DB, scan func(scanner) (T, error), table, selectSQL, where, order string, args []any, page, perPage int) ([]T, int, error) {
	if where != "" {
		where = " WHERE " + where
	}
	var total int
	if err := d.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+table+"`"+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count the %s: %w", table, err)
	}
	if perPage < 1 {
		perPage = 50
	}
	if page < 1 {
		page = 1
	}
	rows, err := all(ctx, d, scan, selectSQL+where+" "+order+" LIMIT ? OFFSET ?", append(append([]any{}, args...), perPage, (page-1)*perPage)...)
	if err != nil {
		return nil, 0, fmt.Errorf("read the %s: %w", table, err)
	}
	return rows, total, nil
}

func inList(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func idArgs(ids []uint64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// --- nodes' settings from a record, for a node that is made or changed

var _ = nodes.Settings{}
var _ = servers.Server{}
