package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/listquery"
)

// serverSelect is a server of the Application API: what the node is told about it (the same columns the node's API reads) and the ids and limits the
// API adds. The joins give the location of the node, the egg and the egg it takes its process configuration from.
const serverSelect = "SELECT `servers`.`id`, `servers`.`external_id`, `servers`.`uuid`, `servers`.`name`, `servers`.`description`, `servers`.`status`, `servers`.`startup`, " +
	"`servers`.`image`, `servers`.`memory`, `servers`.`swap`, `servers`.`disk`, `servers`.`io`, `servers`.`cpu`, `servers`.`threads`, `servers`.`oom_disabled`, " +
	"`servers`.`skip_scripts`, `servers`.`allocation_limit`, `servers`.`allocation_id`, `locations`.`short`, `eggs`.`uuid`, `eggs`.`force_outgoing_ip`, `eggs`.`file_denylist`, " +
	"`eggs`.`config_from`, `eggs`.`config_files`, `eggs`.`config_startup`, `eggs`.`config_stop`, `parent`.`config_files`, `parent`.`config_startup`, `parent`.`config_stop`, " +
	"`servers`.`owner_id`, `servers`.`node_id`, `servers`.`nest_id`, `servers`.`egg_id`, `servers`.`database_limit`, `servers`.`backup_limit`, `servers`.`created_at`, `servers`.`updated_at` " +
	"FROM `servers` JOIN `nodes` ON `nodes`.`id` = `servers`.`node_id` JOIN `locations` ON `locations`.`id` = `nodes`.`location_id` " +
	"JOIN `eggs` ON `eggs`.`id` = `servers`.`egg_id` LEFT JOIN `eggs` `parent` ON `parent`.`id` = `eggs`.`config_from`"

// appServerRow is a row of serverSelect.
type appServerRow struct {
	core
	external                   sql.NullString
	owner, node, nest, egg     uint64
	databaseLimit, backupLimit sql.NullInt64
	created, updated           sql.NullTime
}

func scanAppServer(r scanner) (appServerRow, error) {
	var (
		row                       appServerRow
		id, memory, disk, io, cpu uint64
		swap                      int64
		status, threads           sql.NullString
		oom, skip, force          bool
		allocLimit                sql.NullInt64
		allocationID              uint64
	)
	c := &row.core
	err := r.Scan(&id, &row.external, &c.UUID, &c.Name, &c.Description, &status, &c.Startup, &c.Image, &memory, &swap, &disk, &io, &cpu, &threads, &oom, &skip,
		&allocLimit, &allocationID, &c.LocationShort, &c.EggUUID, &force, &c.FileDenylist, &c.ConfigFrom, &c.ConfigFiles, &c.ConfigStartup, &c.ConfigStop,
		&c.ParentFiles, &c.ParentStartup, &c.ParentStop, &row.owner, &row.node, &row.nest, &row.egg, &row.databaseLimit, &row.backupLimit, &row.created, &row.updated)
	if err != nil {
		return row, err
	}
	c.ID, c.Status, c.Memory, c.Swap, c.Disk, c.IO, c.CPU, c.Threads = id, status.String, int64(memory), swap, int64(disk), int64(io), int64(cpu), nullStr(threads)
	c.OOMDisabled, c.SkipScripts, c.ForceOutgoingIP, c.AllocationID = oom, skip, force, allocationID
	if allocLimit.Valid {
		v := allocLimit.Int64
		c.AllocationLimit = &v
	}
	return row, nil
}

func nullInt64Ptr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

// serverRecords makes the records of the rows, with the relations the request asked for.
func (d *DB) serverRecords(ctx context.Context, rows []appServerRow, load appkit.Includes) ([]appkit.Server, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	cores := make([]core, len(rows))
	for i, r := range rows {
		cores[i] = r.core
	}
	configs, err := d.build(ctx, cores)
	if err != nil {
		return nil, err
	}
	out := make([]appkit.Server, len(rows))
	for i, r := range rows {
		s := appkit.Server{
			ID: r.ID, ExternalID: nullStr(r.external), ShortID: r.UUID[:8], Config: configs[i],
			OwnerID: r.owner, NodeID: r.node, AllocationID: r.AllocationID, NestID: r.nest, EggID: r.egg,
			DatabaseLimit: nullInt64Ptr(r.databaseLimit), BackupLimit: nullInt64Ptr(r.backupLimit), CreatedAt: at(r.created), UpdatedAt: at(r.updated),
		}
		if err := d.serverRelations(ctx, &s, load); err != nil {
			return nil, err
		}
		out[i] = s
	}
	return out, nil
}

// serverRelations fills what the request asked to be attached to a server. The records attached carry no relations of their own.
func (d *DB) serverRelations(ctx context.Context, s *appkit.Server, load appkit.Includes) error {
	var err error
	if load["allocations"] {
		if s.Allocations, err = all(ctx, d, scanAllocation, allocationSelect+" WHERE `allocations`.`server_id` = ? ORDER BY `allocations`.`id`", s.ID); err != nil {
			return fmt.Errorf("read the allocations of server %d: %w", s.ID, err)
		}
	}
	if load["subusers"] {
		if s.Subusers, err = d.subusersOf(ctx, s.ID); err != nil {
			return err
		}
	}
	if load["user"] {
		u, err := one(ctx, d, scanUser, userSelect+" WHERE `users`.`id` = ?", s.OwnerID)
		if err != nil && err != appkit.ErrNotFound {
			return fmt.Errorf("read the owner of server %d: %w", s.ID, err)
		}
		if err == nil {
			s.User = &u
		}
	}
	if load["nest"] {
		n, err := one(ctx, d, scanNest, nestSelect+" WHERE `nests`.`id` = ?", s.NestID)
		if err != nil && err != appkit.ErrNotFound {
			return fmt.Errorf("read the nest of server %d: %w", s.ID, err)
		}
		if err == nil {
			s.Nest = &n
		}
	}
	if load["egg"] {
		e, err := one(ctx, d, scanEgg, eggSelect+" WHERE `eggs`.`id` = ?", s.EggID)
		if err != nil && err != appkit.ErrNotFound {
			return fmt.Errorf("read the egg of server %d: %w", s.ID, err)
		}
		if err == nil {
			s.Egg = &e
		}
	}
	if load["variables"] {
		if s.Variables, err = d.serverVariables(ctx, s.ID, s.EggID); err != nil {
			return err
		}
	}
	if load["location"] {
		l, err := one(ctx, d, scanLocation, locationSelect+" JOIN `nodes` ON `nodes`.`location_id` = `locations`.`id` WHERE `nodes`.`id` = ?", s.NodeID)
		if err != nil && err != appkit.ErrNotFound {
			return fmt.Errorf("read the location of server %d: %w", s.ID, err)
		}
		if err == nil {
			s.Location = &l
		}
	}
	if load["node"] {
		n, err := one(ctx, d, scanNode, nodeSelect+" WHERE `nodes`.`id` = ?", s.NodeID)
		if err != nil && err != appkit.ErrNotFound {
			return fmt.Errorf("read the node of server %d: %w", s.ID, err)
		}
		if err == nil {
			s.Node = &n
		}
	}
	if load["databases"] {
		if s.Databases, err = all(ctx, d, scanDatabase, databaseSelect+" WHERE `databases`.`server_id` = ? ORDER BY `databases`.`id`", s.ID); err != nil {
			return fmt.Errorf("read the databases of server %d: %w", s.ID, err)
		}
	}
	return nil
}

func (d *DB) subusersOf(ctx context.Context, serverID uint64) ([]appkit.Subuser, error) {
	rows, err := d.sql.QueryContext(ctx, "SELECT `id`, `user_id`, `server_id`, `permissions`, `created_at`, `updated_at` FROM `subusers` WHERE `server_id` = ? ORDER BY `id`", serverID)
	if err != nil {
		return nil, fmt.Errorf("read the subusers of server %d: %w", serverID, err)
	}
	defer rows.Close()
	var out []appkit.Subuser
	for rows.Next() {
		var (
			s            appkit.Subuser
			perms        sql.NullString
			created, upd sql.NullTime
		)
		if err := rows.Scan(&s.ID, &s.UserID, &s.ServerID, &perms, &created, &upd); err != nil {
			return nil, err
		}
		s.Permissions, s.CreatedAt, s.UpdatedAt = jsonStrings(perms), at(created), at(upd)
		out = append(out, s)
	}
	return out, rows.Err()
}

// serverVariables are the variables of the egg of a server with the values the server has for them.
func (d *DB) serverVariables(ctx context.Context, serverID, eggID uint64) ([]appkit.ServerVariable, error) {
	vars, err := all(ctx, d, scanEggVariable, eggVariableSelect+" WHERE `egg_variables`.`egg_id` = ? ORDER BY `egg_variables`.`id`", eggID)
	if err != nil {
		return nil, fmt.Errorf("read the variables of the egg: %w", err)
	}
	values := map[uint64]string{}
	rows, err := d.sql.QueryContext(ctx, "SELECT `variable_id`, `variable_value` FROM `server_variables` WHERE `server_id` = ?", serverID)
	if err != nil {
		return nil, fmt.Errorf("read the values of server %d: %w", serverID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uint64
		var v string
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		values[id] = v
	}
	out := make([]appkit.ServerVariable, len(vars))
	for i, v := range vars {
		out[i] = appkit.ServerVariable{Variable: v}
		if val, ok := values[v.ID]; ok {
			value := val
			out[i].ServerValue = &value
		}
	}
	return out, rows.Err()
}

// serverList reads servers by a condition: the fixed one the caller gives and the filters of the request.
func (d *DB) serverList(ctx context.Context, q listquery.Query, fixed string, fixedArgs []any, page, perPage int, load appkit.Includes) ([]appkit.Server, int, error) {
	where, args := fixed, fixedArgs
	if w, a := q.Where(); w != "" {
		if where != "" {
			where += " AND "
		}
		where, args = where+w, append(append([]any{}, args...), a...)
	}
	rows, total, err := pageOf(ctx, d, scanAppServer, "servers", serverSelect, where, q.OrderBy("id"), args, page, perPage)
	if err != nil {
		return nil, 0, err
	}
	list, err := d.serverRecords(ctx, rows, load)
	return list, total, err
}

// serverByWhere reads one server, or answers appkit.ErrNotFound.
func (d *DB) serverByWhere(ctx context.Context, where string, arg any, load appkit.Includes) (appkit.Server, error) {
	row, err := one(ctx, d, scanAppServer, serverSelect+" WHERE "+where, arg)
	if err != nil {
		return appkit.Server{}, err
	}
	list, err := d.serverRecords(ctx, []appServerRow{row}, load)
	if err != nil {
		return appkit.Server{}, err
	}
	return list[0], nil
}

// serversOf reads the servers that a user, a node, a location, a nest or an egg has: the relation "servers" of a record.
func (d *DB) serversOf(ctx context.Context, where string, arg any) ([]appkit.Server, error) {
	rows, err := all(ctx, d, scanAppServer, serverSelect+" WHERE "+where+" ORDER BY `servers`.`id`", arg)
	if err != nil {
		return nil, fmt.Errorf("read the servers: %w", err)
	}
	return d.serverRecords(ctx, rows, nil)
}
