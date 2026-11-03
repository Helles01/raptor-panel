package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/appservers"
	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/db/dbq"
	"github.com/Helles01/raptor-panel/internal/outbox"
	"github.com/Helles01/raptor-panel/internal/servers"
)

// AppServers is the server store of the Application API.
func (d *DB) AppServers() appservers.Store { return appServers{d} }

type appServers struct{ *DB }

func (s appServers) List(ctx context.Context, q listquery.Query, page, perPage int, load appkit.Includes) ([]appkit.Server, int, error) {
	return s.serverList(ctx, q, "", nil, page, perPage, load)
}

func (s appServers) Find(ctx context.Context, id uint64, load appkit.Includes) (appkit.Server, error) {
	return s.serverByWhere(ctx, "`servers`.`id` = ?", id, load)
}

func (s appServers) FindExternal(ctx context.Context, externalID string, load appkit.Includes) (appkit.Server, error) {
	return s.serverByWhere(ctx, "`servers`.`external_id` = ?", externalID, load)
}

func u32s(ids []uint64) []uint32 {
	out := make([]uint32, len(ids))
	for i, id := range ids {
		out[i] = uint32(id)
	}
	return out
}

// lockRows locks the allocations named, with their state, and returns them by id.
func lockRows(ctx context.Context, q *dbq.Queries, ids []uint64) (map[uint64]servers.AllocationRow, error) {
	rows := map[uint64]servers.AllocationRow{}
	if len(ids) == 0 {
		return rows, nil
	}
	list, err := q.LockAllocationsByID(ctx, u32s(ids))
	if err != nil {
		return nil, fmt.Errorf("lock the allocations: %w", err)
	}
	for _, r := range list {
		row := servers.AllocationRow{ID: uint64(r.ID), NodeID: uint64(r.NodeID)}
		if r.ServerID.Valid {
			v := uint64(r.ServerID.Int32)
			row.ServerID = &v
		}
		rows[row.ID] = row
	}
	return rows, nil
}

// assign gives the allocations to the server, all or none: one that another request took meanwhile ends the transaction.
func assign(ctx context.Context, q *dbq.Queries, serverID uint64, ids []uint64) error {
	if len(ids) == 0 {
		return nil
	}
	n, err := q.AssignAllocations(ctx, dbq.AssignAllocationsParams{ServerID: sql.NullInt32{Int32: int32(serverID), Valid: true}, Ids: u32s(ids)})
	if err != nil {
		return fmt.Errorf("assign the allocations: %w", err)
	}
	if int(n) != len(ids) {
		return appservers.ErrBusy
	}
	return nil
}

func nullInt32(p *int64) sql.NullInt32 {
	if p == nil {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: int32(*p), Valid: true}
}

// zeroIfNil is for a limit the column cannot leave empty (backups): none is 0, as in the reference.
func zeroIfNil(p *int64) uint32 {
	if p == nil {
		return 0
	}
	return uint32(*p)
}

func (s appServers) Create(ctx context.Context, r appservers.CreateRequest) (appkit.Server, error) {
	var id uint64
	err := s.retryDeadlockFn(func() error {
		return s.inTx(ctx, func(q *dbq.Queries) error {
			n := r.Server
			// The allocations are locked before anything is read: a plain read first would fix the transaction's snapshot, and a row another request
			// took after that is refused to be locked.
			wanted := append([]uint64{n.Allocation}, n.Additional...)
			rows, err := lockRows(ctx, q, wanted)
			if err != nil {
				return err
			}
			if c, err := q.UserCount(ctx, uint32(n.OwnerID)); err != nil {
				return fmt.Errorf("find the owner: %w", err)
			} else if c == 0 {
				return appservers.ErrNoOwner
			}
			if _, err := q.EggNest(ctx, uint32(n.EggID)); errors.Is(err, sql.ErrNoRows) {
				return appservers.ErrNoEgg
			} else if err != nil {
				return fmt.Errorf("find the egg: %w", err)
			}
			if err := r.Check(rows); err != nil {
				return err
			}
			primary, ok := rows[n.Allocation]
			if !ok {
				return appservers.ErrBusy
			}
			now := sql.NullTime{Time: s.now().UTC(), Valid: true}
			res, err := q.InsertServer(ctx, dbq.InsertServerParams{
				Uuid: r.UUID, Uuidshort: r.ShortID, ExternalID: nullable(derefString(n.ExternalID)), NodeID: uint32(primary.NodeID), Name: n.Name, Description: n.Description,
				SkipScripts: r.SkipScripts, OwnerID: uint32(n.OwnerID), Memory: uint32(n.Limits.Memory), Swap: int32(n.Limits.Swap), Disk: uint32(n.Limits.Disk),
				Io: uint32(n.Limits.IO), Cpu: uint32(n.Limits.CPU), Threads: sql.NullString{String: derefString(n.Limits.Threads), Valid: n.Limits.Threads != nil},
				OomDisabled: boolByte(r.OOMDisabled), AllocationID: uint32(n.Allocation), NestID: uint32(r.NestID), EggID: uint32(n.EggID), Startup: n.Startup, Image: n.Image,
				AllocationLimit: nullInt32(n.Features.Allocations), DatabaseLimit: nullInt32(n.Features.Databases), BackupLimit: zeroIfNil(n.Features.Backups), CreatedAt: now, UpdatedAt: now,
			})
			if field, taken := duplicate(err); taken {
				return &appkit.TakenError{Field: field}
			}
			if err != nil {
				return fmt.Errorf("store the server: %w", err)
			}
			id = uint64(res)
			if err := assign(ctx, q, id, wanted); err != nil {
				return err
			}
			return s.storeValues(ctx, q, id, n.EggID, r.Values, now)
		})
	})
	if err != nil {
		return appkit.Server{}, err
	}
	return s.Find(ctx, id, nil)
}

// retryDeadlockFn is retryDeadlock for the stores that are not about users: a deadlock between requests that lock the same allocations is run again.
func (s appServers) retryDeadlockFn(fn func() error) error {
	return appUsers{s.DB}.retryDeadlock(fn)
}

// storeValues stores the values the server has for the variables of its egg, a row to a variable.
func (s appServers) storeValues(ctx context.Context, q *dbq.Queries, serverID, eggID uint64, values []servers.VariableValue, now sql.NullTime) error {
	owner := sql.NullInt32{Int32: int32(serverID), Valid: true}
	for _, v := range values {
		variable, err := q.VariableOfEgg(ctx, dbq.VariableOfEggParams{EggID: uint32(eggID), EnvVariable: v.EnvName})
		if errors.Is(err, sql.ErrNoRows) {
			continue // a name the egg does not have is not stored
		} else if err != nil {
			return fmt.Errorf("find the variable %s: %w", v.EnvName, err)
		}
		rows, err := q.CountServerVariableRows(ctx, dbq.CountServerVariableRowsParams{ServerID: owner, VariableID: variable})
		if err != nil {
			return fmt.Errorf("look for the value of %s: %w", v.EnvName, err)
		}
		if rows == 0 {
			err = q.InsertServerVariable(ctx, dbq.InsertServerVariableParams{ServerID: owner, VariableID: variable, VariableValue: v.Store, CreatedAt: now, UpdatedAt: now})
		} else {
			_, err = q.UpdateServerVariable(ctx, dbq.UpdateServerVariableParams{VariableValue: v.Store, UpdatedAt: now, ServerID: owner, VariableID: variable})
		}
		if err != nil {
			return fmt.Errorf("store the value of %s: %w", v.EnvName, err)
		}
	}
	return nil
}

// UpdateDetails stores the details; if the owner changed, the orders to take the previous owner's access away are stored in the same transaction.
func (s appServers) UpdateDetails(ctx context.Context, id uint64, d servers.Details, revoke uint64) (appkit.Server, error) {
	err := s.inTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.LockServer(ctx, uint32(id)); errors.Is(err, sql.ErrNoRows) {
			return appkit.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock server %d: %w", id, err)
		}
		if c, err := q.UserCount(ctx, uint32(d.OwnerID)); err != nil {
			return fmt.Errorf("find the owner: %w", err)
		} else if c == 0 {
			return appservers.ErrNoOwner
		}
		err := q.UpdateServerDetails(ctx, dbq.UpdateServerDetailsParams{
			ExternalID: nullable(derefString(d.ExternalID)), OwnerID: uint32(d.OwnerID), Name: d.Name, Description: d.Description,
			UpdatedAt: sql.NullTime{Time: s.now().UTC(), Valid: true}, ID: uint32(id),
		})
		if field, taken := duplicate(err); taken {
			return &appkit.TakenError{Field: field}
		}
		if err != nil {
			return fmt.Errorf("store the details: %w", err)
		}
		if revoke == 0 {
			return nil
		}
		srv, err := q.ServerNodeAndUUID(ctx, uint32(id))
		if err != nil {
			return fmt.Errorf("read server %d: %w", id, err)
		}
		uuid, err := q.UserUUID(ctx, uint32(revoke))
		if errors.Is(err, sql.ErrNoRows) {
			return nil // the previous owner is gone, nothing to take away
		} else if err != nil {
			return fmt.Errorf("find the previous owner: %w", err)
		}
		job, err := outbox.DeauthorizeJob(outbox.Deauthorization{NodeID: uint64(srv.NodeID), UserUUID: uuid, ServerUUIDs: []string{srv.Uuid}})
		if err != nil {
			return err
		}
		return q.EnqueueJob(ctx, dbq.EnqueueJobParams{Kind: job.Kind, Payload: string(job.Payload), RunAt: s.now().UTC()})
	})
	if err != nil {
		return appkit.Server{}, err
	}
	return s.Find(ctx, id, nil)
}

// UpdateBuild changes the build with the server and the allocations involved locked, so that what Change decides from is what is stored when
// it is stored; an allocation that another request took meanwhile ends the whole change.
func (s appServers) UpdateBuild(ctx context.Context, id uint64, r appservers.BuildRequest) (appkit.Server, error) {
	err := s.retryDeadlockFn(func() error {
		return s.inTx(ctx, func(q *dbq.Queries) error {
			if _, err := q.LockServer(ctx, uint32(id)); errors.Is(err, sql.ErrNoRows) {
				return appkit.ErrNotFound
			} else if err != nil {
				return fmt.Errorf("lock server %d: %w", id, err)
			}
			rows, err := lockRows(ctx, q, r.Rows)
			if err != nil {
				return err
			}
			cur, err := s.serverByWhere(ctx, "`servers`.`id` = ?", id, nil)
			if err != nil {
				return err
			}
			change, err := r.Change(cur, rows)
			if err != nil {
				return err
			}
			l, f := change.Limits, change.Features
			err = q.UpdateServerBuild(ctx, dbq.UpdateServerBuildParams{
				Memory: uint32(l.Memory), Swap: int32(l.Swap), Disk: uint32(l.Disk), Io: uint32(l.IO), Cpu: uint32(l.CPU),
				Threads: sql.NullString{String: derefString(l.Threads), Valid: l.Threads != nil}, OomDisabled: boolByte(change.OOMDisabled),
				DatabaseLimit: nullInt32(f.Databases), AllocationLimit: nullInt32(f.Allocations), BackupLimit: zeroIfNil(f.Backups),
				AllocationID: uint32(change.Plan.Default), UpdatedAt: sql.NullTime{Time: s.now().UTC(), Valid: true}, ID: uint32(id),
			})
			if err != nil {
				return fmt.Errorf("store the build: %w", err)
			}
			if err := assign(ctx, q, id, change.Plan.Assign); err != nil {
				return err
			}
			if len(change.Plan.Release) > 0 {
				err := q.ReleaseAllocationsOfServer(ctx, dbq.ReleaseAllocationsOfServerParams{Ids: u32s(change.Plan.Release), ServerID: sql.NullInt32{Int32: int32(id), Valid: true}})
				if err != nil {
					return fmt.Errorf("release the allocations: %w", err)
				}
			}
			return nil
		})
	})
	if err != nil {
		return appkit.Server{}, err
	}
	return s.Find(ctx, id, nil)
}

func (s appServers) Egg(ctx context.Context, id uint64) (appservers.EggInfo, error) {
	if id == 0 || id > 1<<32-1 {
		return appservers.EggInfo{}, appservers.ErrNoEgg
	}
	nest, err := s.q.EggNest(ctx, uint32(id))
	if errors.Is(err, sql.ErrNoRows) {
		return appservers.EggInfo{}, appservers.ErrNoEgg
	} else if err != nil {
		return appservers.EggInfo{}, fmt.Errorf("find egg %d: %w", id, err)
	}
	vars, err := s.q.EggVariablesForRules(ctx, uint32(id))
	if err != nil {
		return appservers.EggInfo{}, fmt.Errorf("read the variables of egg %d: %w", id, err)
	}
	info := appservers.EggInfo{NestID: uint64(nest)}
	for _, v := range vars {
		info.Variables = append(info.Variables, servers.EggVariable{EnvName: v.EnvVariable, Name: v.Name, Rules: v.Rules.String, UserViewable: v.UserViewable != 0, UserEditable: v.UserEditable != 0})
	}
	return info, nil
}

// UpdateStartup stores the startup settings, the egg and its nest, and the values of the variables of the egg, in one transaction.
func (s appServers) UpdateStartup(ctx context.Context, id uint64, set servers.StartupSettings, nestID uint64, values []servers.VariableValue) (appkit.Server, error) {
	err := s.inTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.LockServer(ctx, uint32(id)); errors.Is(err, sql.ErrNoRows) {
			return appkit.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock server %d: %w", id, err)
		}
		now := sql.NullTime{Time: s.now().UTC(), Valid: true}
		err := q.UpdateServerStartup(ctx, dbq.UpdateServerStartupParams{Startup: set.Startup, EggID: uint32(set.EggID), NestID: uint32(nestID), Image: set.Image, SkipScripts: set.SkipScripts, UpdatedAt: now, ID: uint32(id)})
		if err != nil {
			return fmt.Errorf("store the startup: %w", err)
		}
		return s.storeValues(ctx, q, id, set.EggID, values, now)
	})
	if err != nil {
		return appkit.Server{}, err
	}
	return s.Find(ctx, id, nil)
}

func (s appServers) Usage(ctx context.Context) ([]servers.NodeUsage, error) {
	list, err := all(ctx, s.DB, scanNode, nodeSelect+" ORDER BY `nodes`.`id`")
	if err != nil {
		return nil, fmt.Errorf("read the nodes: %w", err)
	}
	out := make([]servers.NodeUsage, len(list))
	for i, n := range list {
		out[i] = nodeUsage(n)
	}
	return out, nil
}

func (s appServers) Allocations(ctx context.Context, nodeIDs []uint64) ([]servers.Candidate, error) {
	if len(nodeIDs) == 0 {
		return nil, nil
	}
	rows, err := s.q.AllocationsOfNodes(ctx, u32s(nodeIDs))
	if err != nil {
		return nil, fmt.Errorf("read the allocations: %w", err)
	}
	out := make([]servers.Candidate, len(rows))
	for i, r := range rows {
		out[i] = servers.Candidate{ID: uint64(r.ID), NodeID: uint64(r.NodeID), IP: r.Ip, Port: int(r.Port), Used: r.ServerID.Valid}
	}
	return out, nil
}

func (s appServers) Transferring(ctx context.Context, id uint64) (bool, error) {
	_, err := s.q.OpenTransferOfServer(ctx, uint32(id))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("look for a transfer of server %d: %w", id, err)
	}
	return true, nil
}

// SetStatus changes the status in one statement that holds only while the server has the status the change was made from.
func (s appServers) SetStatus(ctx context.Context, id uint64, from, to string) (bool, error) {
	return settingsStore{s.DB}.SetStatus(ctx, id, from, to)
}

func (s appServers) Databases(ctx context.Context, serverID uint64) ([]appkit.ServerDatabase, error) {
	list, err := all(ctx, s.DB, scanDatabase, databaseSelect+" WHERE `databases`.`server_id` = ? ORDER BY `databases`.`id`", serverID)
	if err != nil {
		return nil, fmt.Errorf("read the databases of server %d: %w", serverID, err)
	}
	return list, nil
}

// DeleteDatabase removes the row of a database and its sealed password.
func (s appServers) DeleteDatabase(ctx context.Context, id uint64) error {
	return s.inTx(ctx, func(q *dbq.Queries) error {
		if err := q.DeleteSecretsOfRow(ctx, dbq.DeleteSecretsOfRowParams{TableName: "databases", RowID: id}); err != nil {
			return fmt.Errorf("remove the password of database %d: %w", id, err)
		}
		if _, err := q.DeleteDatabaseRow(ctx, uint32(id)); err != nil {
			return fmt.Errorf("delete database %d: %w", id, err)
		}
		return nil
	})
}

// Delete removes the server: its allocations are released with their notes cleared, and the rest of its rows go with it through the database's cascades.
// A server that still has databases is not removed: the rows of a database are removed after the database itself is dropped on its host (DeleteDatabase),
// and removing them here would leave it there with nothing that points to it.
func (s appServers) Delete(ctx context.Context, id uint64) error {
	return s.inTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.LockServer(ctx, uint32(id)); errors.Is(err, sql.ErrNoRows) {
			return appkit.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock server %d: %w", id, err)
		}
		dbs, err := q.DatabaseIDsOfServer(ctx, uint32(id))
		if err != nil {
			return fmt.Errorf("find the databases of server %d: %w", id, err)
		}
		if len(dbs) > 0 {
			return fmt.Errorf("server %d still has %d databases, which are removed first", id, len(dbs))
		}
		if err := q.ReleaseAllAllocationsOfServer(ctx, sql.NullInt32{Int32: int32(id), Valid: true}); err != nil {
			return fmt.Errorf("release the allocations: %w", err)
		}
		return q.DeleteServerRow(ctx, uint32(id))
	})
}
