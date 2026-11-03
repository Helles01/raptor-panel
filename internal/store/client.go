package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Helles01/raptor-panel/internal/activity"
	"github.com/Helles01/raptor-panel/internal/api/clientapi"
	"github.com/Helles01/raptor-panel/internal/servers"
)

// ClientAccounts is the account the client API shows.
func (d *DB) ClientAccounts() clientapi.Accounts { return clientAccounts{d} }

// ClientServers is the server store of the client API.
func (d *DB) ClientServers() clientapi.Servers { return clientServers{d} }

// ClientActivity is the activity store of the client API.
func (d *DB) ClientActivity() clientapi.ActivityLogs { return clientActivity{d} }

type (
	clientAccounts clientServers
	clientServers  struct{ *DB }
	clientActivity struct{ *DB }
)

func (s clientAccounts) ByID(ctx context.Context, id uint64) (clientapi.Account, error) {
	if id == 0 || id > 1<<32-1 {
		return clientapi.Account{}, clientapi.ErrNotFound
	}
	row, err := s.q.AccountByID(ctx, uint32(id))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return clientapi.Account{}, clientapi.ErrNotFound
	case err != nil:
		return clientapi.Account{}, fmt.Errorf("find user %d: %w", id, err)
	}
	return clientapi.Account{
		ID: uint64(row.ID), RootAdmin: row.RootAdmin != 0, Username: row.Username, Email: row.Email,
		FirstName: row.NameFirst.String, LastName: row.NameLast.String, Language: row.Language,
	}, nil
}

// serverColumns are what a row of the client's server list has; the table names are the ones listquery writes in its conditions.
const serverColumns = "`servers`.`id`, `servers`.`uuid`, `servers`.`uuidShort`, `servers`.`owner_id`, `servers`.`name`, `servers`.`description`, `servers`.`status`, " +
	"`servers`.`memory`, `servers`.`swap`, `servers`.`disk`, `servers`.`io`, `servers`.`cpu`, `servers`.`threads`, `servers`.`oom_disabled`, `servers`.`skip_scripts`, " +
	"`servers`.`startup`, `servers`.`image`, `servers`.`database_limit`, `servers`.`allocation_limit`, `servers`.`backup_limit`, `servers`.`allocation_id`, " +
	"`nodes`.`name`, `nodes`.`fqdn`, `nodes`.`daemonSFTP`, `nodes`.`maintenance_mode`, `eggs`.`uuid`, `eggs`.`name`, `eggs`.`features`, " +
	"(SELECT COUNT(*) FROM `server_transfers` t WHERE t.`server_id` = `servers`.`id` AND t.`successful` IS NULL), `subusers`.`permissions`, `subusers`.`id` IS NOT NULL"

// serverFrom joins what a row needs. The subusers join is for the viewer of the list: with no viewer (0) it matches nothing.
const serverFrom = " FROM `servers` JOIN `nodes` ON `nodes`.`id` = `servers`.`node_id` JOIN `eggs` ON `eggs`.`id` = `servers`.`egg_id` " +
	"LEFT JOIN `subusers` ON `subusers`.`server_id` = `servers`.`id` AND `subusers`.`user_id` = ?"

type serverRow struct {
	srv         clientapi.Server
	allocation  uint64
	permissions sql.NullString
	isSubuser   bool
}

func scanServer(rows interface{ Scan(...any) error }) (serverRow, error) {
	var (
		r                                serverRow
		id, owner, memory, disk, io      uint64
		cpu, allocation                  uint64
		status, threads                  sql.NullString
		oom, skip, maintenance           bool
		dbLimit, allocLimit, backupLimit sql.NullInt64
		sftp                             int
		features                         sql.NullString
		transfers                        int64
		swapSigned                       int64
	)
	err := rows.Scan(&id, &r.srv.UUID, &r.srv.ShortID, &owner, &r.srv.Name, &r.srv.Description, &status,
		&memory, &swapSigned, &disk, &io, &cpu, &threads, &oom, &skip,
		&r.srv.Startup, &r.srv.Image, &dbLimit, &allocLimit, &backupLimit, &allocation,
		&r.srv.Node.Name, &r.srv.Node.FQDN, &sftp, &maintenance, &r.srv.Egg.UUID, &r.srv.Egg.Name, &features,
		&transfers, &r.permissions, &r.isSubuser)
	if err != nil {
		return r, err
	}
	s := &r.srv
	s.ID, s.OwnerID, s.Status, s.Transferring = id, owner, status.String, transfers > 0
	s.Memory, s.Swap, s.Disk, s.IO, s.CPU = int64(memory), swapSigned, int64(disk), int64(io), int64(cpu)
	s.Threads, s.OOMDisabled, s.SkipScripts = nullStr(threads), oom, skip
	s.Node.SFTPPort, s.Node.Maintenance = sftp, maintenance
	s.Egg.Features = jsonStrings(features)
	s.DatabaseLimit, s.AllocationLimit, s.BackupLimit = nullInt(dbLimit), nullInt(allocLimit), nullInt(backupLimit)
	r.allocation = allocation
	return r, nil
}

func nullInt(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

// withRelations adds the allocations and the variables, one query each for any number of servers, and the permissions the viewer holds.
func (s clientServers) withRelations(ctx context.Context, rows []serverRow) ([]clientapi.Server, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]uint32, len(rows))
	nullIDs := make([]sql.NullInt32, len(rows))
	at := make(map[uint64]int, len(rows))
	for i, r := range rows {
		ids[i], nullIDs[i], at[r.srv.ID] = uint32(r.srv.ID), sql.NullInt32{Int32: int32(r.srv.ID), Valid: true}, i
	}
	allocs, err := s.q.ClientAllocationsOfServers(ctx, nullIDs)
	if err != nil {
		return nil, fmt.Errorf("read the allocations: %w", err)
	}
	vars, err := s.q.ClientVariablesOfServers(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("read the variables: %w", err)
	}
	for _, a := range allocs {
		i, ok := at[uint64(a.ServerID.Int32)]
		if !ok {
			continue
		}
		al := clientapi.Allocation{ID: uint64(a.ID), IP: a.Ip, IPAlias: nullStr(a.IpAlias), Port: int(a.Port), Notes: nullStr(a.Notes)}
		rows[i].srv.Allocations = append(rows[i].srv.Allocations, al)
		if al.ID == rows[i].allocation {
			rows[i].srv.Primary = al
		}
	}
	for _, v := range vars {
		i, ok := at[uint64(v.ServerID)]
		if !ok {
			continue
		}
		rows[i].srv.Variables = append(rows[i].srv.Variables, clientapi.Variable{
			Name: v.Name, Description: v.Description, EnvName: v.EnvVariable, Default: v.DefaultValue, ServerValue: nullStr(v.Value),
			UserViewable: v.UserViewable != 0, UserEditable: v.UserEditable != 0, Rules: v.Rules.String,
		})
	}
	out := make([]clientapi.Server, len(rows))
	for i, r := range rows {
		out[i] = r.srv
		if r.isSubuser {
			out[i].SubuserPermissions = []string{}
			if r.permissions.Valid {
				var perms []string
				if json.Unmarshal([]byte(r.permissions.String), &perms) == nil {
					out[i].SubuserPermissions = perms
				}
			}
		}
	}
	return out, nil
}

// scopeCondition is the part of the WHERE that says which servers the viewer's list is about, with its arguments.
func scopeCondition(viewer uint64, scope servers.ListScope) (cond string, args []any) {
	switch scope {
	case servers.ScopeOwner:
		return "`servers`.`owner_id` = ?", []any{viewer}
	case servers.ScopeAdminOthers:
		return "(`servers`.`owner_id` <> ? AND `servers`.`id` NOT IN (SELECT `server_id` FROM `subusers` WHERE `user_id` = ?))", []any{viewer, viewer}
	case servers.ScopeAdminAll:
		return "1 = 1", nil
	}
	return "(`servers`.`owner_id` = ? OR `servers`.`id` IN (SELECT `server_id` FROM `subusers` WHERE `user_id` = ?))", []any{viewer, viewer}
}

// List returns one page of the viewer's servers. The names in the query are from the specs of the request and the values travel as arguments.
func (s clientServers) List(ctx context.Context, l clientapi.ServerList) (clientapi.ServerPage, error) {
	if l.Scope == servers.ScopeNone {
		return clientapi.ServerPage{}, nil
	}
	cond, args := scopeCondition(l.Viewer, l.Scope)
	if where, wargs := l.Query.Where(); where != "" {
		cond += " AND " + where
		args = append(args, wargs...)
	}

	var total int
	countArgs := append([]any{l.Viewer}, args...)
	if err := s.sql.QueryRowContext(ctx, "SELECT COUNT(*)"+serverFrom+" WHERE "+cond, countArgs...).Scan(&total); err != nil {
		return clientapi.ServerPage{}, fmt.Errorf("count the servers: %w", err)
	}
	perPage, page := l.PerPage, l.Page
	if perPage < 1 {
		perPage = 50
	}
	if page < 1 {
		page = 1
	}
	listArgs := append(append([]any{l.Viewer}, args...), perPage, (page-1)*perPage)
	rows, err := s.sql.QueryContext(ctx, "SELECT "+serverColumns+serverFrom+" WHERE "+cond+" "+l.Query.OrderBy("id")+" LIMIT ? OFFSET ?", listArgs...)
	if err != nil {
		return clientapi.ServerPage{}, fmt.Errorf("read the servers: %w", err)
	}
	defer rows.Close()
	var found []serverRow
	for rows.Next() {
		r, err := scanServer(rows)
		if err != nil {
			return clientapi.ServerPage{}, fmt.Errorf("read a server: %w", err)
		}
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		return clientapi.ServerPage{}, fmt.Errorf("read the servers: %w", err)
	}
	rows.Close()
	list, err := s.withRelations(ctx, found)
	if err != nil {
		return clientapi.ServerPage{}, err
	}
	return clientapi.ServerPage{Total: total, Servers: list}, nil
}

func (s clientServers) One(ctx context.Context, id uint64) (clientapi.Server, error) {
	r, err := scanServer(s.sql.QueryRowContext(ctx, "SELECT "+serverColumns+serverFrom+" WHERE `servers`.`id` = ?", 0, id))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return clientapi.Server{}, clientapi.ErrNotFound
	case err != nil:
		return clientapi.Server{}, fmt.Errorf("read server %d: %w", id, err)
	}
	list, err := s.withRelations(ctx, []serverRow{r})
	if err != nil {
		return clientapi.Server{}, err
	}
	return list[0], nil
}

func (s clientServers) Subusers(ctx context.Context, serverIDs []uint64) (map[uint64][]clientapi.Subuser, error) {
	out := map[uint64][]clientapi.Subuser{}
	if len(serverIDs) == 0 {
		return out, nil
	}
	ids := make([]uint32, len(serverIDs))
	for i, id := range serverIDs {
		ids[i] = uint32(id)
	}
	rows, err := s.q.SubusersOfServers(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("read the subusers: %w", err)
	}
	for _, r := range rows {
		perms := []string{}
		if r.Permissions.Valid {
			var list []string
			if json.Unmarshal([]byte(r.Permissions.String), &list) == nil {
				perms = list
			}
		}
		out[uint64(r.ServerID)] = append(out[uint64(r.ServerID)], clientapi.Subuser{
			User:        clientapi.Person{UUID: r.Uuid, Username: r.Username, Email: r.Email, UsesTOTP: r.UseTotp != 0, CreatedAt: timeOf(r.CreatedAt)},
			Permissions: perms,
		})
	}
	return out, nil
}

// --- the activity lists

const activityColumns = "`activity_logs`.`id`, `activity_logs`.`batch`, `activity_logs`.`event`, `activity_logs`.`ip`, `activity_logs`.`description`, " +
	"`activity_logs`.`actor_type`, `activity_logs`.`actor_id`, `activity_logs`.`api_key_id`, `activity_logs`.`properties`, `activity_logs`.`timestamp`"

func (s clientActivity) ForUser(ctx context.Context, userID uint64, l clientapi.ActivityList) (clientapi.ActivityPage, error) {
	return s.page(ctx, userModel, userID, "", nil, l)
}

// ForServer lists the entries about a server. With hideAdmins the entries of an administrator are left out, unless the administrator owns the
// server or is one of its subusers.
func (s clientActivity) ForServer(ctx context.Context, serverID uint64, hideAdmins bool, l clientapi.ActivityList) (clientapi.ActivityPage, error) {
	if !hideAdmins {
		return s.page(ctx, serverModel, serverID, "", nil, l)
	}
	cond := "NOT EXISTS (SELECT 1 FROM `users` u WHERE u.`id` = `activity_logs`.`actor_id` AND `activity_logs`.`actor_type` = ? AND u.`root_admin` = 1 " +
		"AND u.`id` <> (SELECT `owner_id` FROM `servers` WHERE `id` = ?) AND u.`id` NOT IN (SELECT `user_id` FROM `subusers` WHERE `server_id` = ?))"
	return s.page(ctx, serverModel, serverID, cond, []any{userModel, serverID, serverID}, l)
}

func (s clientActivity) page(ctx context.Context, subjectType string, subjectID uint64, extra string, extraArgs []any, l clientapi.ActivityList) (clientapi.ActivityPage, error) {
	cond := "EXISTS (SELECT 1 FROM `activity_log_subjects` s WHERE s.`activity_log_id` = `activity_logs`.`id` AND s.`subject_type` = ? AND s.`subject_id` = ?)"
	args := []any{subjectType, subjectID}
	if extra != "" {
		cond += " AND " + extra
		args = append(args, extraArgs...)
	}
	if len(l.Exclude) > 0 {
		cond += " AND `activity_logs`.`event` NOT IN (" + strings.TrimSuffix(strings.Repeat("?,", len(l.Exclude)), ",") + ")"
		for _, e := range l.Exclude {
			args = append(args, e)
		}
	}
	if where, wargs := l.Query.Where(); where != "" {
		cond += " AND " + where
		args = append(args, wargs...)
	}

	var total int
	if err := s.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM `activity_logs` WHERE "+cond, args...).Scan(&total); err != nil {
		return clientapi.ActivityPage{}, fmt.Errorf("count the activity: %w", err)
	}
	perPage, page := l.PerPage, l.Page
	if perPage < 1 {
		perPage = 25
	}
	if page < 1 {
		page = 1
	}
	rows, err := s.sql.QueryContext(ctx, "SELECT "+activityColumns+" FROM `activity_logs` WHERE "+cond+" "+l.Query.OrderBy("id")+" LIMIT ? OFFSET ?",
		append(args, perPage, (page-1)*perPage)...)
	if err != nil {
		return clientapi.ActivityPage{}, fmt.Errorf("read the activity: %w", err)
	}
	defer rows.Close()

	var entries []clientapi.ActivityEntry
	var actors []uint32
	for rows.Next() {
		var (
			e                      activity.Stored
			batch, desc, actorType sql.NullString
			actorID                sql.NullInt64
			apiKey                 sql.NullInt64
			props                  string
			ts                     time.Time
		)
		if err := rows.Scan(&e.ID, &batch, &e.Event, &e.IP, &desc, &actorType, &actorID, &apiKey, &props, &ts); err != nil {
			return clientapi.ActivityPage{}, fmt.Errorf("read an activity entry: %w", err)
		}
		e.Batch, e.Description, e.Timestamp = batch.String, desc.String, ts.UTC()
		_ = json.Unmarshal([]byte(props), &e.Properties)
		if actorID.Valid && actorType.String == userModel {
			a := uint64(actorID.Int64)
			e.ActorID = &a
			actors = append(actors, uint32(a))
		}
		if apiKey.Valid {
			k := uint64(apiKey.Int64)
			e.APIKeyID = &k
		}
		entries = append(entries, clientapi.ActivityEntry{Stored: e})
	}
	if err := rows.Err(); err != nil {
		return clientapi.ActivityPage{}, fmt.Errorf("read the activity: %w", err)
	}
	rows.Close()

	if l.WithActors && len(actors) > 0 {
		people, err := s.q.PeopleByID(ctx, actors)
		if err != nil {
			return clientapi.ActivityPage{}, fmt.Errorf("read the people behind the activity: %w", err)
		}
		byID := make(map[uint64]clientapi.Person, len(people))
		for _, p := range people {
			byID[uint64(p.ID)] = clientapi.Person{UUID: p.Uuid, Username: p.Username, Email: p.Email, UsesTOTP: p.UseTotp != 0, CreatedAt: timeOf(p.CreatedAt)}
		}
		for i := range entries {
			if entries[i].ActorID == nil {
				continue
			}
			if p, ok := byID[*entries[i].ActorID]; ok {
				person := p
				entries[i].Actor = &person
			}
		}
	}
	return clientapi.ActivityPage{Total: total, Entries: entries}, nil
}
