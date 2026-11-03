package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"

	"github.com/Helles01/raptor-panel/internal/admin"
	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/appusers"
	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/db/dbq"
	"github.com/Helles01/raptor-panel/internal/nodes"
	"github.com/Helles01/raptor-panel/internal/users"
)

// inRawTx runs fn in a transaction for the statements that are built at run time (a number of rows at once).
func (d *DB) inRawTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Update stores the changes and what follows from them (the sessions, the reset links, the application keys, the orders to the nodes) in one
// transaction, so that a password that changes never leaves a session or a token of the old one alive.
func (s appUsers) update(ctx context.Context, id uint64, u users.Update, fx users.Effects) (appkit.User, error) {
	err := s.inTx(ctx, func(q *dbq.Queries) error {
		oldEmail, err := q.UserEmail(ctx, uint32(id))
		if errors.Is(err, sql.ErrNoRows) {
			return appkit.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock user %d: %w", id, err)
		}
		if err := s.checkExternalID(ctx, q, u.ExternalID, id); err != nil {
			return err
		}
		now := sql.NullTime{Time: s.now().UTC(), Valid: true}
		_, err = q.UpdateUserFields(ctx, dbq.UpdateUserFieldsParams{
			ExternalID: nullable(derefString(u.ExternalID)), Email: u.Email, Username: u.Username, NameFirst: sql.NullString{String: u.FirstName, Valid: true},
			NameLast: sql.NullString{String: u.LastName, Valid: true}, Language: u.Language, RootAdmin: boolByte(u.Admin), UpdatedAt: now, ID: uint32(id),
		})
		if field, taken := duplicate(err); taken {
			return &appkit.TakenError{Field: field}
		}
		if err != nil {
			return fmt.Errorf("store the user: %w", err)
		}
		if u.PasswordHash != "" {
			if _, err := q.SetUserPassword(ctx, dbq.SetUserPasswordParams{Password: u.PasswordHash, UpdatedAt: now, ID: uint32(id)}); err != nil {
				return fmt.Errorf("store the password: %w", err)
			}
		}
		if fx.EmailChanged || fx.PasswordChanged {
			// Links asked for the old address must not work for an address the user no longer has.
			if _, err := q.DeletePasswordResets(ctx, oldEmail); err != nil {
				return fmt.Errorf("remove the reset links of the old address: %w", err)
			}
		}
		if fx.PasswordChanged {
			if _, err := q.DeletePasswordResets(ctx, u.Email); err != nil {
				return fmt.Errorf("remove the reset links: %w", err)
			}
			if err := q.DeleteSessionsForUser(ctx, sql.NullInt32{Int32: int32(id), Valid: true}); err != nil {
				return fmt.Errorf("end the sessions of user %d: %w", id, err)
			}
			if err := s.deauthorizeIn(ctx, q, id); err != nil {
				return err
			}
		}
		if fx.AdminRevoked {
			if err := q.DeleteApplicationKeySecretsOfUser(ctx, uint32(id)); err != nil {
				return fmt.Errorf("remove the secrets of the application keys: %w", err)
			}
			if err := q.DeleteApplicationKeysOfUser(ctx, uint32(id)); err != nil {
				return fmt.Errorf("remove the application keys: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return appkit.User{}, err
	}
	return s.Find(ctx, id, nil)
}

// Delete counts the servers of the user with their row locked, as the command does.
func (s appUsers) Delete(ctx context.Context, id uint64, check func(ownedServers int64) error) error {
	err := adminStore{s.DB}.DeleteUser(ctx, id, check)
	if errors.Is(err, admin.ErrNotFound) {
		return appkit.ErrNotFound
	}
	return err
}

func (s appUsersActivity) Log(ctx context.Context, e appusers.Entry) error {
	var key uint64
	if e.APIKeyID != nil {
		key = *e.APIKeyID
	}
	return s.logActivity(ctx, entry{Event: e.Event, Subject: e.SubjectUserID, Actor: e.ActorID, APIKeyID: key, IP: e.IP, Properties: e.Properties})
}

// --- locations

func (s appLocations) withRelations(ctx context.Context, l *appkit.Location, load appkit.Includes) error {
	var err error
	if load["nodes"] {
		if l.Nodes, err = all(ctx, s.DB, scanNode, nodeSelect+" WHERE `nodes`.`location_id` = ? ORDER BY `nodes`.`id`", l.ID); err != nil {
			return fmt.Errorf("read the nodes of location %d: %w", l.ID, err)
		}
	}
	if load["servers"] {
		if l.Servers, err = s.serversOf(ctx, "`nodes`.`location_id` = ?", l.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s appLocations) List(ctx context.Context, q listquery.Query, page, perPage int, load appkit.Includes) ([]appkit.Location, int, error) {
	where, args := q.Where()
	list, total, err := pageOf(ctx, s.DB, scanLocation, "locations", locationSelect, where, q.OrderBy("id"), args, page, perPage)
	if err != nil {
		return nil, 0, err
	}
	for i := range list {
		if err := s.withRelations(ctx, &list[i], load); err != nil {
			return nil, 0, err
		}
	}
	return list, total, nil
}

func (s appLocations) Find(ctx context.Context, id uint64, load appkit.Includes) (appkit.Location, error) {
	l, err := one(ctx, s.DB, scanLocation, locationSelect+" WHERE `locations`.`id` = ?", id)
	if err != nil {
		return l, err
	}
	return l, s.withRelations(ctx, &l, load)
}

func (s appLocations) Create(ctx context.Context, l nodes.Location) (appkit.Location, error) {
	now := sql.NullTime{Time: s.now().UTC(), Valid: true}
	id, err := s.q.InsertLocation(ctx, dbq.InsertLocationParams{Short: l.Short, Long: nullable(l.Long), CreatedAt: now, UpdatedAt: now})
	if _, taken := duplicate(err); taken {
		return appkit.Location{}, &appkit.TakenError{Field: "short"}
	}
	if err != nil {
		return appkit.Location{}, fmt.Errorf("store the location: %w", err)
	}
	return s.Find(ctx, uint64(id), nil)
}

func (s appLocations) Update(ctx context.Context, id uint64, l nodes.Location) (appkit.Location, error) {
	err := s.inTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.LockLocation(ctx, uint32(id)); errors.Is(err, sql.ErrNoRows) {
			return appkit.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock location %d: %w", id, err)
		}
		_, err := q.UpdateLocation(ctx, dbq.UpdateLocationParams{Short: l.Short, Long: nullable(l.Long), UpdatedAt: sql.NullTime{Time: s.now().UTC(), Valid: true}, ID: uint32(id)})
		if _, taken := duplicate(err); taken {
			return &appkit.TakenError{Field: "short"}
		}
		if err != nil {
			return fmt.Errorf("store the location: %w", err)
		}
		return nil
	})
	if err != nil {
		return appkit.Location{}, err
	}
	return s.Find(ctx, id, nil)
}

// Delete counts the nodes of the location with its row locked.
func (s appLocations) Delete(ctx context.Context, id uint64, check func(nodes int64) error) error {
	return s.inTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.LockLocation(ctx, uint32(id)); errors.Is(err, sql.ErrNoRows) {
			return appkit.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock location %d: %w", id, err)
		}
		n, err := q.CountNodesOfLocation(ctx, uint32(id))
		if err != nil {
			return fmt.Errorf("count the nodes of location %d: %w", id, err)
		}
		if err := check(n); err != nil {
			return err
		}
		return q.DeleteLocation(ctx, uint32(id))
	})
}

// --- allocations

func (s appAllocations) Exists(ctx context.Context, nodeID uint64) error {
	if nodeID == 0 || nodeID > 1<<32-1 {
		return appkit.ErrNotFound
	}
	n, err := s.q.NodeExists(ctx, uint32(nodeID))
	switch {
	case err != nil:
		return fmt.Errorf("find node %d: %w", nodeID, err)
	case n == 0:
		return appkit.ErrNotFound
	}
	return nil
}

func (s appAllocations) List(ctx context.Context, nodeID uint64, q listquery.Query, page, perPage int, load appkit.Includes) ([]appkit.Allocation, int, error) {
	where, args := "`allocations`.`node_id` = ?", []any{nodeID}
	if w, a := q.Where(); w != "" {
		where, args = where+" AND "+w, append(args, a...)
	}
	list, total, err := pageOf(ctx, s.DB, scanAllocation, "allocations", allocationSelect, where, q.OrderBy("id"), args, page, perPage)
	if err != nil {
		return nil, 0, err
	}
	for i := range list {
		if load["node"] {
			n, err := one(ctx, s.DB, scanNode, nodeSelect+" WHERE `nodes`.`id` = ?", list[i].NodeID)
			if err != nil && !errors.Is(err, appkit.ErrNotFound) {
				return nil, 0, fmt.Errorf("read the node of allocation %d: %w", list[i].ID, err)
			}
			if err == nil {
				list[i].Node = &n
			}
		}
		if load["server"] && list[i].ServerID != nil {
			srv, err := s.serverByWhere(ctx, "`servers`.`id` = ?", *list[i].ServerID, nil)
			if err != nil && !errors.Is(err, appkit.ErrNotFound) {
				return nil, 0, err
			}
			if err == nil {
				list[i].Server = &srv
			}
		}
	}
	return list, total, nil
}

// Add stores the allocations in one transaction, a few hundred rows to a statement; one the node has already (the same address and port) is left
// as it is.
func (s appAllocations) Add(ctx context.Context, nodeID uint64, list []nodes.Allocation) error {
	const chunk = 500
	return s.inRawTx(ctx, func(tx *sql.Tx) error {
		now := s.now().UTC()
		for start := 0; start < len(list); start += chunk {
			end := min(start+chunk, len(list))
			values := make([]string, 0, end-start)
			args := make([]any, 0, (end-start)*6)
			for _, a := range list[start:end] {
				values = append(values, "(?, ?, ?, ?, ?, ?)")
				args = append(args, nodeID, a.IP, nullable(a.Alias), a.Port, now, now)
			}
			if _, err := tx.ExecContext(ctx, "INSERT IGNORE INTO `allocations` (`node_id`, `ip`, `ip_alias`, `port`, `created_at`, `updated_at`) VALUES "+strings.Join(values, ", "), args...); err != nil {
				return fmt.Errorf("store the allocations: %w", err)
			}
		}
		return nil
	})
}

// Delete removes a free allocation of the node; the allocation is locked while check looks at who holds it.
func (s appAllocations) Delete(ctx context.Context, nodeID, allocationID uint64, check func(serverID *uint64) error) error {
	return s.inTx(ctx, func(q *dbq.Queries) error {
		row, err := q.LockAllocation(ctx, dbq.LockAllocationParams{ID: uint32(allocationID), NodeID: uint32(nodeID)})
		if errors.Is(err, sql.ErrNoRows) {
			return appkit.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock allocation %d: %w", allocationID, err)
		}
		var holder *uint64
		if row.ServerID.Valid {
			v := uint64(row.ServerID.Int32)
			holder = &v
		}
		if err := check(holder); err != nil {
			return err
		}
		return q.DeleteAllocation(ctx, row.ID)
	})
}

// --- nests and eggs

func (s appNests) nestRelations(ctx context.Context, n *appkit.Nest, load appkit.Includes) error {
	var err error
	if load["eggs"] {
		if n.Eggs, err = all(ctx, s.DB, scanEgg, eggSelect+" WHERE `eggs`.`nest_id` = ? ORDER BY `eggs`.`id`", n.ID); err != nil {
			return fmt.Errorf("read the eggs of nest %d: %w", n.ID, err)
		}
	}
	if load["servers"] {
		if n.Servers, err = s.serversOf(ctx, "`servers`.`nest_id` = ?", n.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s appNests) eggRelations(ctx context.Context, e *appkit.Egg, load appkit.Includes) error {
	var err error
	if load["nest"] {
		n, err := one(ctx, s.DB, scanNest, nestSelect+" WHERE `nests`.`id` = ?", e.NestID)
		if err != nil && !errors.Is(err, appkit.ErrNotFound) {
			return fmt.Errorf("read the nest of egg %d: %w", e.ID, err)
		}
		if err == nil {
			e.Nest = &n
		}
	}
	if load["servers"] {
		if e.Servers, err = s.serversOf(ctx, "`servers`.`egg_id` = ?", e.ID); err != nil {
			return err
		}
	}
	if load["variables"] {
		if e.Variables, err = all(ctx, s.DB, scanEggVariable, eggVariableSelect+" WHERE `egg_variables`.`egg_id` = ? ORDER BY `egg_variables`.`id`", e.ID); err != nil {
			return fmt.Errorf("read the variables of egg %d: %w", e.ID, err)
		}
	}
	if load["config"] && e.ConfigFrom != nil {
		p, err := one(ctx, s.DB, scanEgg, eggSelect+" WHERE `eggs`.`id` = ?", *e.ConfigFrom)
		if err != nil && !errors.Is(err, appkit.ErrNotFound) {
			return fmt.Errorf("read the egg %d takes its configuration from: %w", e.ID, err)
		}
		if err == nil {
			e.Inherited = &appkit.ConfigSet{Files: p.ConfigFiles, Startup: p.ConfigStartup, Logs: p.ConfigLogs, Stop: p.ConfigStop}
		}
	}
	if load["script"] && e.CopyScriptFrom != nil {
		p, err := one(ctx, s.DB, scanEgg, eggSelect+" WHERE `eggs`.`id` = ?", *e.CopyScriptFrom)
		if err != nil && !errors.Is(err, appkit.ErrNotFound) {
			return fmt.Errorf("read the egg %d takes its script from: %w", e.ID, err)
		}
		if err == nil {
			e.InheritedScript = &appkit.ScriptSet{Install: p.ScriptInstall, Entry: p.ScriptEntry, Container: p.ScriptContainer}
		}
	}
	return nil
}

func (s appNests) ListNests(ctx context.Context, page, perPage int, load appkit.Includes) ([]appkit.Nest, int, error) {
	list, total, err := pageOf(ctx, s.DB, scanNest, "nests", nestSelect, "", "ORDER BY `nests`.`id` ASC", nil, page, perPage)
	if err != nil {
		return nil, 0, err
	}
	for i := range list {
		if err := s.nestRelations(ctx, &list[i], load); err != nil {
			return nil, 0, err
		}
	}
	return list, total, nil
}

func (s appNests) FindNest(ctx context.Context, id uint64, load appkit.Includes) (appkit.Nest, error) {
	n, err := one(ctx, s.DB, scanNest, nestSelect+" WHERE `nests`.`id` = ?", id)
	if err != nil {
		return n, err
	}
	return n, s.nestRelations(ctx, &n, load)
}

func (s appNests) ListEggs(ctx context.Context, nestID uint64, load appkit.Includes) ([]appkit.Egg, error) {
	if _, err := s.FindNest(ctx, nestID, nil); err != nil {
		return nil, err
	}
	list, err := all(ctx, s.DB, scanEgg, eggSelect+" WHERE `eggs`.`nest_id` = ? ORDER BY `eggs`.`id`", nestID)
	if err != nil {
		return nil, fmt.Errorf("read the eggs of nest %d: %w", nestID, err)
	}
	for i := range list {
		if err := s.eggRelations(ctx, &list[i], load); err != nil {
			return nil, err
		}
	}
	return list, nil
}

func (s appNests) FindEgg(ctx context.Context, nestID, eggID uint64, load appkit.Includes) (appkit.Egg, error) {
	e, err := one(ctx, s.DB, scanEgg, eggSelect+" WHERE `eggs`.`id` = ? AND `eggs`.`nest_id` = ?", eggID, nestID)
	if err != nil {
		return e, err
	}
	return e, s.eggRelations(ctx, &e, load)
}

// checkExternalID answers a *appkit.TakenError if another user has the external id. The check and the write are in one transaction, and the read
// locks the place in the index, so that two requests with the same value cannot both pass.
func (s appUsers) checkExternalID(ctx context.Context, q *dbq.Queries, external *string, selfID uint64) error {
	if external == nil {
		return nil
	}
	holders, err := q.ExternalIDHolders(ctx, dbq.ExternalIDHoldersParams{ExternalID: sql.NullString{String: *external, Valid: true}, ID: uint32(selfID)})
	if err != nil {
		return fmt.Errorf("look for the external id: %w", err)
	}
	if len(holders) > 0 {
		return &appkit.TakenError{Field: "external_id"}
	}
	return nil
}

// retryDeadlock runs fn again when the database chose it as the victim of a deadlock (MariaDB 1213), which two requests that lock the same place in
// the index can cause; the second time the other request has finished and the answer is the right one.
func (s appUsers) retryDeadlock(fn func() error) error {
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		if err = fn(); !isDeadlock(err) {
			return err
		}
	}
	return err
}

func isDeadlock(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && (me.Number == 1213 || me.Number == 1205)
}

func (s appUsers) Create(ctx context.Context, r appusers.CreateRequest) (appkit.User, error) {
	var out appkit.User
	err := s.retryDeadlock(func() (err error) { out, err = s.create(ctx, r); return })
	return out, err
}

func (s appUsers) Update(ctx context.Context, id uint64, u users.Update, fx users.Effects) (appkit.User, error) {
	var out appkit.User
	err := s.retryDeadlock(func() (err error) { out, err = s.update(ctx, id, u, fx); return })
	return out, err
}
