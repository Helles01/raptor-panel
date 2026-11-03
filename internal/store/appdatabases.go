package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/appserverdbs"
	"github.com/Helles01/raptor-panel/internal/api/databaseapi"
	"github.com/Helles01/raptor-panel/internal/db/dbq"
	"github.com/Helles01/raptor-panel/internal/dbhosts"
)

// The databases of a server are served twice, to the administrator (Application API) and to the owner (client API). The two adapters differ in the types
// and the errors of their packages; what they do is in serverDatabases.
type serverDatabases struct{ *DB }

// AppServerDatabases is the database store of the Application API.
func (d *DB) AppServerDatabases() appserverdbs.Store { return appServerDatabases{serverDatabases{d}} }

// ClientDatabases is the database store of the client API.
func (d *DB) ClientDatabases() databaseapi.Store { return clientDatabases{serverDatabases{d}} }

var (
	errHostGone  = errors.New("store: no such database host")
	errNameTaken = errors.New("store: database name taken")
)

type newDatabase struct {
	serverID, hostID                 uint64
	name, username, password, remote string
}

// create stores the database and seals its password, with the server's row locked so that the limit is checked against the databases it has when the new
// one is stored. The server is locked before anything else is read: see Create of the servers.
func (s serverDatabases) create(ctx context.Context, n newDatabase, check func(limit *int64, count int64) error) (uint64, error) {
	if n.serverID > math.MaxUint32 {
		return 0, appkit.ErrNotFound
	}
	if n.hostID > math.MaxUint32 {
		return 0, errHostGone
	}
	var id uint64
	err := appUsers{s.DB}.retryDeadlock(func() error {
		return s.inTx(ctx, func(q *dbq.Queries) error {
			limit, err := q.LockServerDatabaseLimit(ctx, uint32(n.serverID))
			if errors.Is(err, sql.ErrNoRows) {
				return appkit.ErrNotFound
			} else if err != nil {
				return fmt.Errorf("lock server %d: %w", n.serverID, err)
			}
			if c, err := q.DatabaseHostExists(ctx, uint32(n.hostID)); err != nil {
				return fmt.Errorf("find database host %d: %w", n.hostID, err)
			} else if c == 0 {
				return errHostGone
			}
			count, err := q.CountDatabasesOfServer(ctx, uint32(n.serverID))
			if err != nil {
				return fmt.Errorf("count the databases of server %d: %w", n.serverID, err)
			}
			var max *int64
			if limit.Valid {
				v := int64(limit.Int32)
				max = &v
			}
			if err := check(max, count); err != nil {
				return err
			}
			now := sql.NullTime{Time: s.now().UTC(), Valid: true}
			res, err := q.InsertDatabase(ctx, dbq.InsertDatabaseParams{
				ServerID: uint32(n.serverID), DatabaseHostID: uint32(n.hostID), Database: n.name, Username: n.username, Remote: n.remote, CreatedAt: now, UpdatedAt: now,
			})
			if field, taken := duplicate(err); taken {
				if field == "username" {
					// A name made at random met another: asking again makes another.
					return fmt.Errorf("the user name made for the database is taken on host %d, try again: %w", n.hostID, err)
				}
				return errNameTaken
			}
			if err != nil {
				return fmt.Errorf("store the database: %w", err)
			}
			id = uint64(res)
			return s.seal(ctx, q, "databases", id, "password", n.password)
		})
	})
	return id, err
}

// setPassword seals the new password of the database and notes the change.
func (s serverDatabases) setPassword(ctx context.Context, id uint64, password string) error {
	if id > math.MaxUint32 {
		return appkit.ErrNotFound
	}
	return s.inTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.LockDatabase(ctx, uint32(id)); errors.Is(err, sql.ErrNoRows) {
			return appkit.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock database %d: %w", id, err)
		}
		if err := s.seal(ctx, q, "databases", id, "password", password); err != nil {
			return err
		}
		return q.TouchDatabase(ctx, dbq.TouchDatabaseParams{UpdatedAt: sql.NullTime{Time: s.now().UTC(), Valid: true}, ID: uint32(id)})
	})
}

// remove deletes the database of the server, with its sealed password.
func (s serverDatabases) remove(ctx context.Context, serverID, id uint64) error {
	if serverID > math.MaxUint32 || id > math.MaxUint32 {
		return appkit.ErrNotFound
	}
	return s.inTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.LockDatabaseOfServer(ctx, dbq.LockDatabaseOfServerParams{ID: uint32(id), ServerID: uint32(serverID)}); errors.Is(err, sql.ErrNoRows) {
			return appkit.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock database %d: %w", id, err)
		}
		if err := q.DeleteSecretsOfRow(ctx, dbq.DeleteSecretsOfRowParams{TableName: "databases", RowID: id}); err != nil {
			return fmt.Errorf("remove the password of database %d: %w", id, err)
		}
		if _, err := q.DeleteDatabaseRow(ctx, uint32(id)); err != nil {
			return fmt.Errorf("delete database %d: %w", id, err)
		}
		return nil
	})
}

// password opens the sealed password of the database; one that has none sealed was not migrated.
func (s serverDatabases) password(ctx context.Context, id uint64) (string, error) {
	p, ok, err := s.secret(ctx, "databases", id, "password")
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("database %d has no password of Raptor's: run raptor migrate-secrets", id)
	}
	return p, nil
}

// --- the Application API

type appServerDatabases struct{ serverDatabases }

func (s appServerDatabases) Exists(ctx context.Context, serverID uint64) error {
	var n int
	if err := s.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM `servers` WHERE `id` = ?", serverID).Scan(&n); err != nil {
		return fmt.Errorf("find server %d: %w", serverID, err)
	}
	if n == 0 {
		return appkit.ErrNotFound
	}
	return nil
}

// relations fills the host and the password of a database where the request asked for them.
func (s appServerDatabases) relations(ctx context.Context, d *appkit.ServerDatabase, load appkit.Includes) error {
	if load["host"] {
		h, err := one(ctx, s.DB, scanHost, hostSelect+" WHERE `database_hosts`.`id` = ?", d.HostID)
		if err != nil {
			return fmt.Errorf("read the host of database %d: %w", d.ID, err)
		}
		d.Host = &h
	}
	if load["password"] {
		p, err := s.password(ctx, d.ID)
		if err != nil {
			return err
		}
		d.Password = &p
	}
	return nil
}

func (s appServerDatabases) List(ctx context.Context, serverID uint64, load appkit.Includes) ([]appkit.ServerDatabase, error) {
	list, err := all(ctx, s.DB, scanDatabase, databaseSelect+" WHERE `databases`.`server_id` = ? ORDER BY `databases`.`id`", serverID)
	if err != nil {
		return nil, fmt.Errorf("read the databases of server %d: %w", serverID, err)
	}
	for i := range list {
		if err := s.relations(ctx, &list[i], load); err != nil {
			return nil, err
		}
	}
	return list, nil
}

func (s appServerDatabases) Find(ctx context.Context, serverID, databaseID uint64, load appkit.Includes) (appkit.ServerDatabase, error) {
	d, err := one(ctx, s.DB, scanDatabase, databaseSelect+" WHERE `databases`.`id` = ? AND `databases`.`server_id` = ?", databaseID, serverID)
	if err != nil {
		return d, err
	}
	return d, s.relations(ctx, &d, load)
}

func (s appServerDatabases) Create(ctx context.Context, r appserverdbs.CreateRequest) (appkit.ServerDatabase, error) {
	n := r.Database
	id, err := s.create(ctx, newDatabase{serverID: n.ServerID, hostID: n.HostID, name: n.Name, username: n.Username, password: n.Password, remote: n.Remote}, r.Check)
	switch {
	case errors.Is(err, errNameTaken):
		return appkit.ServerDatabase{}, appserverdbs.ErrNameTaken
	case errors.Is(err, errHostGone):
		return appkit.ServerDatabase{}, appserverdbs.ErrNoHost
	case err != nil:
		return appkit.ServerDatabase{}, err
	}
	return s.Find(ctx, n.ServerID, id, nil)
}

func (s appServerDatabases) SetPassword(ctx context.Context, databaseID uint64, password string) error {
	return s.setPassword(ctx, databaseID, password)
}

func (s appServerDatabases) Delete(ctx context.Context, serverID, databaseID uint64) error {
	return s.remove(ctx, serverID, databaseID)
}

// --- the client API

type clientDatabases struct{ serverDatabases }

const recordSelect = "SELECT `databases`.`id`, `databases`.`database_host_id`, `database_hosts`.`host`, `database_hosts`.`port`, `databases`.`database`, `databases`.`username`, " +
	"`databases`.`remote`, `databases`.`max_connections` FROM `databases` JOIN `database_hosts` ON `database_hosts`.`id` = `databases`.`database_host_id`"

func scanRecord(r scanner) (databaseapi.Record, error) {
	var (
		rec     databaseapi.Record
		maxConn sql.NullInt64
	)
	if err := r.Scan(&rec.ID, &rec.HostID, &rec.HostAddress, &rec.HostPort, &rec.Name, &rec.Username, &rec.Remote, &maxConn); err != nil {
		return rec, err
	}
	if maxConn.Valid {
		v := int(maxConn.Int64)
		rec.MaxConnections = &v
	}
	return rec, nil
}

func (s clientDatabases) List(ctx context.Context, serverID uint64) ([]databaseapi.Record, error) {
	list, err := all(ctx, s.DB, scanRecord, recordSelect+" WHERE `databases`.`server_id` = ? ORDER BY `databases`.`id`", serverID)
	if err != nil {
		return nil, fmt.Errorf("read the databases of server %d: %w", serverID, err)
	}
	return list, nil
}

func (s clientDatabases) Find(ctx context.Context, serverID, databaseID uint64) (databaseapi.Record, error) {
	rec, err := one(ctx, s.DB, scanRecord, recordSelect+" WHERE `databases`.`id` = ? AND `databases`.`server_id` = ?", databaseID, serverID)
	if errors.Is(err, appkit.ErrNotFound) {
		return rec, databaseapi.ErrNotFound
	}
	return rec, err
}

func (s clientDatabases) Password(ctx context.Context, databaseID uint64) (string, error) {
	return s.password(ctx, databaseID)
}

func (s clientDatabases) Hosts(ctx context.Context) ([]dbhosts.HostChoice, error) {
	rows, err := s.q.DatabaseHostChoices(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the database hosts: %w", err)
	}
	out := make([]dbhosts.HostChoice, len(rows))
	for i, r := range rows {
		out[i] = dbhosts.HostChoice{ID: uint64(r.ID), NodeID: nullInt64(sql.NullInt64{Int64: int64(r.NodeID.Int32), Valid: r.NodeID.Valid})}
	}
	return out, nil
}

func (s clientDatabases) Create(ctx context.Context, r databaseapi.CreateRequest) (databaseapi.Record, error) {
	n := r.Database
	id, err := s.create(ctx, newDatabase{serverID: n.ServerID, hostID: n.HostID, name: n.Name, username: n.Username, password: n.Password, remote: n.Remote}, r.Check)
	switch {
	case errors.Is(err, errNameTaken):
		return databaseapi.Record{}, databaseapi.ErrNameTaken
	case errors.Is(err, errHostGone):
		return databaseapi.Record{}, fmt.Errorf("database host %d is gone: %w", n.HostID, err)
	case err != nil:
		return databaseapi.Record{}, err
	}
	return s.Find(ctx, n.ServerID, id)
}

func (s clientDatabases) SetPassword(ctx context.Context, databaseID uint64, password string) error {
	if err := s.setPassword(ctx, databaseID, password); errors.Is(err, appkit.ErrNotFound) {
		return databaseapi.ErrNotFound
	} else {
		return err
	}
}

func (s clientDatabases) Delete(ctx context.Context, serverID, databaseID uint64) error {
	if err := s.remove(ctx, serverID, databaseID); errors.Is(err, appkit.ErrNotFound) {
		return databaseapi.ErrNotFound
	} else {
		return err
	}
}
