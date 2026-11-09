package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-sql-driver/mysql"

	"github.com/Helles01/raptor-panel/internal/admin"
	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/db/dbq"
	"github.com/Helles01/raptor-panel/internal/dbhosts"
	"github.com/Helles01/raptor-panel/internal/nodes"
	"github.com/Helles01/raptor-panel/internal/secrets"
	"github.com/Helles01/raptor-panel/internal/settings"
	"github.com/Helles01/raptor-panel/internal/users"
)

// AdminStore is the storage of the administrator's commands.
func (d *DB) AdminStore() admin.Store { return adminStore{d} }

// AdminDirectory is what the commands that look at the panel read, and the revoking of a key.
func (d *DB) AdminDirectory() admin.DirectoryStore { return adminDirectory{d} }

// AdminHosts is the storage of the database host commands.
func (d *DB) AdminHosts() admin.HostStore { return adminHosts{d} }

// AdminRotation is the storage of the commands that replace a secret.
func (d *DB) AdminRotation() admin.RotationStore { return adminRotation{d} }

// DBHostLookup finds how to reach a database host by its row.
func (d *DB) DBHostLookup() dbhosts.Lookup { return dbHostLookup{d} }

type (
	adminStore     struct{ *DB }
	adminDirectory struct{ *DB }
	adminHosts     struct{ *DB }
	adminRotation  struct{ *DB }
	dbHostLookup   struct{ *DB }
)

// duplicate says which unique key a MariaDB error 1062 is about, as the name of the field in a request.
func duplicate(err error) (field string, ok bool) {
	var me *mysql.MySQLError
	if !errors.As(err, &me) || me.Number != 1062 {
		return "", false
	}
	for _, f := range []string{"username", "email", "external_id", "uuid", "identifier"} {
		if strings.Contains(me.Message, f) {
			return f, true
		}
	}
	return "value", true
}

func (s adminStore) CreateUser(ctx context.Context, u users.Record) (uint64, error) {
	now := sql.NullTime{Time: s.now().UTC(), Valid: true}
	p := dbq.InsertUserParams{
		Uuid: u.UUID, Username: u.Username, Email: u.Email, Password: u.PasswordHash, Language: u.Language, RootAdmin: boolByte(u.Admin),
		NameFirst: sql.NullString{String: u.FirstName, Valid: true}, NameLast: sql.NullString{String: u.LastName, Valid: true}, CreatedAt: now, UpdatedAt: now,
	}
	if u.ExternalID != nil {
		p.ExternalID = sql.NullString{String: *u.ExternalID, Valid: true}
	}
	id, err := s.q.InsertUser(ctx, p)
	if field, ok := duplicate(err); ok {
		return 0, &admin.TakenError{Field: field}
	}
	if err != nil {
		return 0, fmt.Errorf("store the user: %w", err)
	}
	return uint64(id), nil
}

func boolByte(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

// FindUser finds a user by id, address (it has an @) or user name.
func (s adminStore) FindUser(ctx context.Context, ref string) (admin.User, error) {
	return findUser(ctx, s.q, ref)
}

func findUser(ctx context.Context, q *dbq.Queries, ref string) (admin.User, error) {
	var (
		row userRow
		err error
	)
	if id, perr := strconv.ParseUint(ref, 10, 32); perr == nil {
		r, e := q.UserByID(ctx, uint32(id))
		row, err = userRow(r), e
	} else if strings.Contains(ref, "@") {
		r, e := q.UserByEmail(ctx, ref)
		row, err = userRow(r), e
	} else {
		r, e := q.UserByUsername(ctx, ref)
		row, err = userRow(r), e
	}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return admin.User{}, admin.ErrNotFound
	case err != nil:
		return admin.User{}, fmt.Errorf("find user: %w", err)
	}
	return admin.User{ID: uint64(row.ID), UUID: row.Uuid, Username: row.Username, Email: row.Email, Admin: row.RootAdmin != 0}, nil
}

// DeleteUser counts the servers of the user with their row locked, so that none can be made between the count and the delete.
func (s adminStore) DeleteUser(ctx context.Context, id uint64, check func(ownedServers int64) error) error {
	return s.inTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.LockUser(ctx, uint32(id)); errors.Is(err, sql.ErrNoRows) {
			return admin.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock user %d: %w", id, err)
		}
		owned, err := q.CountOwnedServers(ctx, uint32(id))
		if err != nil {
			return fmt.Errorf("count the servers of user %d: %w", id, err)
		}
		if err := check(owned); err != nil {
			return err
		}
		if err := q.DeleteKeySecretsOfUser(ctx, uint32(id)); err != nil {
			return fmt.Errorf("remove the secrets of the keys of user %d: %w", id, err)
		}
		if err := q.DeleteSecretsOfRow(ctx, dbq.DeleteSecretsOfRowParams{TableName: "users", RowID: id}); err != nil {
			return fmt.Errorf("remove the secrets of user %d: %w", id, err)
		}
		if _, err := q.DeleteUser(ctx, uint32(id)); err != nil {
			return fmt.Errorf("delete user %d: %w", id, err)
		}
		return nil
	})
}

func (s adminStore) DisableTwoFactor(ctx context.Context, id uint64) error {
	return s.inTx(ctx, func(q *dbq.Queries) error {
		n, err := q.ClearTwoFactor(ctx, uint32(id))
		switch {
		case err != nil:
			return fmt.Errorf("switch two-factor authentication off for user %d: %w", id, err)
		case n == 0:
			// A row that already had it off changes nothing, so look whether the user is there at all.
			if _, err := q.LockUser(ctx, uint32(id)); errors.Is(err, sql.ErrNoRows) {
				return admin.ErrNotFound
			} else if err != nil {
				return fmt.Errorf("find user %d: %w", id, err)
			}
		}
		if err := q.DeleteRecoveryTokensOfUser(ctx, uint32(id)); err != nil {
			return fmt.Errorf("remove the recovery tokens of user %d: %w", id, err)
		}
		if err := q.DeleteSecret(ctx, dbq.DeleteSecretParams{TableName: "users", RowID: id, ColumnName: "totp_secret"}); err != nil {
			return fmt.Errorf("remove the second factor secret of user %d: %w", id, err)
		}
		return nil
	})
}

// TwoFactorUsers counts the users who have a second factor.
func (s adminStore) TwoFactorUsers(ctx context.Context) (int64, error) {
	n, err := s.q.CountUsersWithTwoFactor(ctx)
	if err != nil {
		return 0, fmt.Errorf("count the users with a second factor: %w", err)
	}
	return n, nil
}

// DisableAllTwoFactor removes every second factor in one transaction: the switch and the old secret of each user, the secrets kept for them, and every recovery
// token. The count is taken in the same transaction, before anything changes.
func (s adminStore) DisableAllTwoFactor(ctx context.Context) (int64, error) {
	var n int64
	err := s.inTx(ctx, func(q *dbq.Queries) (err error) {
		if n, err = q.CountUsersWithTwoFactor(ctx); err != nil {
			return fmt.Errorf("count the users with a second factor: %w", err)
		}
		if _, err = q.ClearAllTwoFactor(ctx); err != nil {
			return fmt.Errorf("switch two-factor authentication off: %w", err)
		}
		if err = q.DeleteAllRecoveryTokens(ctx); err != nil {
			return fmt.Errorf("remove the recovery tokens: %w", err)
		}
		if err = q.DeleteAllSealedTotpSecrets(ctx); err != nil {
			return fmt.Errorf("remove the sealed second factor secrets: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// seal stores a secret in raptor_secrets, bound to its place.
func (d *DB) seal(ctx context.Context, q *dbq.Queries, table string, id uint64, column, value string) error {
	sealed, err := d.box.Seal(value, secrets.Context(table, id, column))
	if err != nil {
		return fmt.Errorf("seal %s.%s of row %d: %w", table, column, id, err)
	}
	if err := q.UpsertSecret(ctx, dbq.UpsertSecretParams{TableName: table, RowID: id, ColumnName: column, Ciphertext: sealed}); err != nil {
		return fmt.Errorf("store %s.%s of row %d: %w", table, column, id, err)
	}
	return nil
}

// CreateKey counts the keys of the user with their row locked, so that two requests cannot both pass the limit.
func (s adminStore) CreateKey(ctx context.Context, k admin.NewKey, check func(count int) error) error {
	return s.inTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.LockUser(ctx, uint32(k.UserID)); errors.Is(err, sql.ErrNoRows) {
			return admin.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock user %d: %w", k.UserID, err)
		}
		count, err := q.CountKeysOfUser(ctx, uint32(k.UserID))
		if err != nil {
			return fmt.Errorf("count the keys of user %d: %w", k.UserID, err)
		}
		if err := check(int(count)); err != nil {
			return err
		}

		now := sql.NullTime{Time: s.now().UTC(), Valid: true}
		p := dbq.InsertAPIKeyParams{
			UserID: uint32(k.UserID), KeyType: uint8(k.Type), Identifier: sql.NullString{String: k.Identifier, Valid: true},
			Memo: sql.NullString{String: k.Description, Valid: true}, CreatedAt: now, UpdatedAt: now,
		}
		if len(k.AllowedIPs) > 0 {
			list, err := json.Marshal(k.AllowedIPs)
			if err != nil {
				return fmt.Errorf("write the allowed addresses: %w", err)
			}
			p.AllowedIps = sql.NullString{String: string(list), Valid: true}
		}
		p.RServers, p.RNodes, p.RAllocations, p.RUsers = uint8(k.Access["servers"]), uint8(k.Access["nodes"]), uint8(k.Access["allocations"]), uint8(k.Access["users"])
		p.RLocations, p.RNests, p.REggs = uint8(k.Access["locations"]), uint8(k.Access["nests"]), uint8(k.Access["eggs"])
		p.RDatabaseHosts, p.RServerDatabases = uint8(k.Access["database_hosts"]), uint8(k.Access["server_databases"])
		id, err := q.InsertAPIKey(ctx, p)
		if _, taken := duplicate(err); taken {
			return &admin.TakenError{Field: "identifier"}
		}
		if err != nil {
			return fmt.Errorf("store the key: %w", err)
		}
		return s.seal(ctx, q, "api_keys", uint64(id), "token_sha256", k.SecretHash)
	})
}

func (s adminStore) CreateLocation(ctx context.Context, short, long string) (uint64, error) {
	now := sql.NullTime{Time: s.now().UTC(), Valid: true}
	id, err := s.q.InsertLocation(ctx, dbq.InsertLocationParams{Short: short, Long: sql.NullString{String: long, Valid: long != ""}, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return 0, fmt.Errorf("store the location: %w", err)
	}
	return uint64(id), nil
}

func (s adminStore) CreateNode(ctx context.Context, n admin.NewNode) (uint64, error) {
	var id uint64
	err := s.inTx(ctx, func(q *dbq.Queries) error {
		if c, err := q.LocationExists(ctx, uint32(n.Settings.LocationID)); err != nil {
			return fmt.Errorf("find location %d: %w", n.Settings.LocationID, err)
		} else if c == 0 {
			return admin.ErrNotFound
		}
		now := sql.NullTime{Time: s.now().UTC(), Valid: true}
		set := n.Settings
		res, err := q.InsertNode(ctx, dbq.InsertNodeParams{
			Uuid: n.UUID, Public: uint16(boolByte(set.Public)), Name: set.Name, Description: sql.NullString{String: set.Description, Valid: set.Description != ""},
			LocationID: uint32(set.LocationID), Fqdn: set.FQDN, Scheme: set.Scheme, BehindProxy: set.BehindProxy, MaintenanceMode: set.MaintenanceMode,
			Memory: uint32(set.Memory), MemoryOverallocate: int32(set.MemoryOverallocate), Disk: uint32(set.Disk), DiskOverallocate: int32(set.DiskOverallocate),
			UploadSize: uint32(set.UploadSize), DaemonTokenID: n.Credentials.TokenID, Daemonlisten: uint16(set.DaemonListen), Daemonsftp: uint16(set.DaemonSFTP),
			Daemonbase: set.DaemonBase, CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			return fmt.Errorf("store the node: %w", err)
		}
		id = uint64(res)
		return s.seal(ctx, q, "nodes", id, "daemon_token", n.Credentials.Token)
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (s adminStore) CreateNest(ctx context.Context, uuid, name, description, author string) (uint64, error) {
	now := sql.NullTime{Time: s.now().UTC(), Valid: true}
	id, err := s.q.InsertNest(ctx, dbq.InsertNestParams{Uuid: uuid, Author: author, Name: name, Description: sql.NullString{String: description, Valid: description != ""}, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return 0, fmt.Errorf("store the nest: %w", err)
	}
	return uint64(id), nil
}

func jsonList(list []string) (sql.NullString, error) {
	if list == nil {
		list = []string{}
	}
	b, err := json.Marshal(list)
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}

func nullable(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

// ImportEgg stores the egg and its variables in one transaction.
func (s adminStore) ImportEgg(ctx context.Context, n admin.NewEgg) (uint64, error) {
	e := n.Egg
	images, err := e.DockerImagesJSON()
	if err != nil {
		return 0, err
	}
	features, err := jsonList(e.Features)
	if err != nil {
		return 0, err
	}
	denylist, err := jsonList(e.FileDenylist)
	if err != nil {
		return 0, err
	}
	var id uint64
	err = s.inTx(ctx, func(q *dbq.Queries) error {
		if c, err := q.NestExists(ctx, uint32(n.NestID)); err != nil {
			return fmt.Errorf("find nest %d: %w", n.NestID, err)
		} else if c == 0 {
			return admin.ErrNotFound
		}
		now := sql.NullTime{Time: s.now().UTC(), Valid: true}
		res, err := q.InsertEgg(ctx, dbq.InsertEggParams{
			Uuid: n.UUID, NestID: uint32(n.NestID), Author: e.Author, Name: e.Name, Description: nullable(e.Description), Features: features,
			DockerImages: sql.NullString{String: images, Valid: true}, FileDenylist: denylist, UpdateUrl: nullable(e.UpdateURL),
			ConfigFiles: nullable(e.ConfigFiles), ConfigStartup: nullable(e.ConfigStartup), ConfigLogs: nullable(e.ConfigLogs), ConfigStop: nullable(e.ConfigStop),
			Startup: nullable(e.Startup), ScriptContainer: e.ScriptContainer, ScriptEntry: e.ScriptEntry, ScriptInstall: nullable(e.ScriptInstall), CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			return fmt.Errorf("store the egg: %w", err)
		}
		id = uint64(res)
		for _, v := range e.Variables {
			err := q.InsertEggVariable(ctx, dbq.InsertEggVariableParams{
				EggID: uint32(id), Name: v.Name, Description: v.Description, EnvVariable: v.EnvVariable, DefaultValue: v.DefaultValue,
				UserViewable: boolByte(v.UserViewable), UserEditable: boolByte(v.UserEditable), Rules: nullable(v.Rules), CreatedAt: now, UpdatedAt: now,
			})
			if err != nil {
				return fmt.Errorf("store the variable %s: %w", v.EnvVariable, err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (s adminStore) SetSetting(ctx context.Context, key, value string) error {
	if err := s.q.UpsertSetting(ctx, dbq.UpsertSettingParams{Key: settings.StorageKey(key), Value: value}); err != nil {
		return fmt.Errorf("store setting %s: %w", key, err)
	}
	return nil
}

// SetMailPassword seals the password of the mail server under the row of its setting, in one transaction with the making of the row.
func (s adminStore) SetMailPassword(ctx context.Context, password string) error {
	key := settings.StorageKey(smtpPassword)
	return s.inTx(ctx, func(q *dbq.Queries) error {
		id, err := q.EnsureSettingRow(ctx, key)
		if err != nil {
			return fmt.Errorf("make the setting of the mail password: %w", err)
		}
		return s.seal(ctx, q, "settings", uint64(id), "value", password)
	})
}

// ClearMailPassword removes the sealed password. The row of the setting stays: it may hold the old panel's value, which is not touched.
func (s adminStore) ClearMailPassword(ctx context.Context) error {
	return s.inTx(ctx, func(q *dbq.Queries) error {
		id, err := q.SettingRowIDForUpdate(ctx, settings.StorageKey(smtpPassword))
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		} else if err != nil {
			return fmt.Errorf("find the setting of the mail password: %w", err)
		}
		if err := q.DeleteSecret(ctx, dbq.DeleteSecretParams{TableName: "settings", RowID: uint64(id), ColumnName: "value"}); err != nil {
			return fmt.Errorf("remove the sealed mail password: %w", err)
		}
		return nil
	})
}

// --- what the commands look at

func (s adminDirectory) ListUsers(ctx context.Context) ([]admin.User, error) {
	rows, err := s.q.ListUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list the users: %w", err)
	}
	out := make([]admin.User, len(rows))
	for i, r := range rows {
		out[i] = admin.User{ID: uint64(r.ID), UUID: r.Uuid, Username: r.Username, Email: r.Email, Admin: r.RootAdmin != 0}
	}
	return out, nil
}

func (s adminDirectory) FindUser(ctx context.Context, ref string) (admin.User, error) {
	return findUser(ctx, s.q, ref)
}

// keyInfo makes the listing of a key from the columns of the join.
func keyInfo(id, userID uint32, username string, kind uint8, identifier, allowed, memo sql.NullString, last, created sql.NullTime, levels [9]uint8) admin.KeyInfo {
	k := admin.KeyInfo{
		Identifier: identifier.String, UserID: uint64(userID), Username: username, Type: auth.KeyType(kind), Description: memo.String,
		AllowedIPs: allowedIPs(allowed), LastUsedAt: timePtr(last), CreatedAt: timeOf(created),
	}
	if k.Type == auth.KeyApplication {
		resources := auth.ApplicationResources()
		k.Access = make(map[string]auth.Access, len(resources))
		for i, r := range resources {
			k.Access[r] = auth.Access(levels[i])
		}
	}
	return k
}

func (s adminDirectory) ListKeys(ctx context.Context, userID *uint64) ([]admin.KeyInfo, error) {
	var uid uint32
	if userID != nil {
		uid = uint32(*userID)
	}
	// With no user the first parameter is 0 and every key is listed; with one, only the keys of that user.
	rows, err := s.q.ListAPIKeys(ctx, dbq.ListAPIKeysParams{Column1: int64(uid), UserID: uid})
	if err != nil {
		return nil, fmt.Errorf("list the keys: %w", err)
	}
	out := make([]admin.KeyInfo, len(rows))
	for i, r := range rows {
		out[i] = keyInfo(r.ID, r.UserID, r.Username, r.KeyType, r.Identifier, r.AllowedIps, r.Memo, r.LastUsedAt, r.CreatedAt,
			[9]uint8{r.RServers, r.RNodes, r.RAllocations, r.RUsers, r.RLocations, r.RNests, r.REggs, r.RDatabaseHosts, r.RServerDatabases})
	}
	return out, nil
}

// DeleteKey removes a key and its sealed hash or secret.
func (s adminDirectory) DeleteKey(ctx context.Context, identifier string) (admin.KeyInfo, error) {
	var info admin.KeyInfo
	err := s.inTx(ctx, func(q *dbq.Queries) error {
		r, err := q.APIKeyForRevoke(ctx, sql.NullString{String: identifier, Valid: true})
		if errors.Is(err, sql.ErrNoRows) {
			return admin.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("find the key: %w", err)
		}
		info = keyInfo(r.ID, r.UserID, r.Username, r.KeyType, r.Identifier, r.AllowedIps, r.Memo, r.LastUsedAt, r.CreatedAt,
			[9]uint8{r.RServers, r.RNodes, r.RAllocations, r.RUsers, r.RLocations, r.RNests, r.REggs, r.RDatabaseHosts, r.RServerDatabases})
		if err := q.DeleteSecretsOfRow(ctx, dbq.DeleteSecretsOfRowParams{TableName: "api_keys", RowID: uint64(r.ID)}); err != nil {
			return fmt.Errorf("remove the secrets of the key: %w", err)
		}
		if err := q.DeleteAPIKey(ctx, r.ID); err != nil {
			return fmt.Errorf("delete the key: %w", err)
		}
		return nil
	})
	return info, err
}

// DeleteKeys removes the keys of one kind, or all of them, and the secrets sealed for them. The secrets go first, while the rows they are
// found by are still there; both happen in one transaction.
func (s adminDirectory) DeleteKeys(ctx context.Context, kind *auth.KeyType) (int64, error) {
	var n int64
	err := s.inTx(ctx, func(q *dbq.Queries) (err error) {
		if kind == nil {
			if err = q.DeleteSecretsOfAllAPIKeys(ctx); err != nil {
				return fmt.Errorf("remove the secrets of the keys: %w", err)
			}
			n, err = q.DeleteAllAPIKeys(ctx)
		} else {
			if err = q.DeleteSecretsOfAPIKeysOfType(ctx, uint8(*kind)); err != nil {
				return fmt.Errorf("remove the secrets of the keys: %w", err)
			}
			n, err = q.DeleteAPIKeysOfType(ctx, uint8(*kind))
		}
		if err != nil {
			return fmt.Errorf("delete the keys: %w", err)
		}
		return nil
	})
	return n, err
}

func (s adminDirectory) ListLocations(ctx context.Context) ([]admin.LocationInfo, error) {
	rows, err := s.q.ListLocations(ctx)
	if err != nil {
		return nil, fmt.Errorf("list the locations: %w", err)
	}
	out := make([]admin.LocationInfo, len(rows))
	for i, r := range rows {
		out[i] = admin.LocationInfo{ID: uint64(r.ID), Short: r.Short, Long: r.Long.String, Nodes: r.Nodes}
	}
	return out, nil
}

func (s adminDirectory) ListNodes(ctx context.Context) ([]admin.NodeInfo, error) {
	rows, err := s.q.ListNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list the nodes: %w", err)
	}
	out := make([]admin.NodeInfo, len(rows))
	for i, r := range rows {
		out[i] = admin.NodeInfo{ID: uint64(r.ID), UUID: r.Uuid, Name: r.Name, FQDN: r.Fqdn, Scheme: r.Scheme, Port: int(r.Daemonlisten), LocationID: uint64(r.LocationID), Servers: r.Servers}
	}
	return out, nil
}

func (s adminDirectory) Setting(ctx context.Context, key string) (string, bool, error) {
	v, err := s.q.SettingValue(ctx, settings.StorageKey(key))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("read setting %s: %w", key, err)
	}
	return v, true, nil
}

// --- the database hosts

func (s adminHosts) record(ctx context.Context, id, port uint32, name, host, user string, node sql.NullInt32) (admin.HostRecord, error) {
	password, _, err := s.secret(ctx, "database_hosts", uint64(id), "password")
	if err != nil {
		return admin.HostRecord{}, err
	}
	h := admin.HostRecord{ID: uint64(id), Name: name, Host: host, Port: int(port), Username: user, Password: password}
	if node.Valid {
		n := uint64(node.Int32)
		h.NodeID = &n
	}
	return h, nil
}

func nodeParam(n *uint64) sql.NullInt32 {
	if n == nil {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: int32(*n), Valid: true}
}

func (s adminHosts) CreateHost(ctx context.Context, h admin.HostRecord) (uint64, error) {
	var id uint64
	err := s.inTx(ctx, func(q *dbq.Queries) error {
		now := sql.NullTime{Time: s.now().UTC(), Valid: true}
		res, err := q.InsertDatabaseHost(ctx, dbq.InsertDatabaseHostParams{
			Name: h.Name, Host: h.Host, Port: uint32(h.Port), Username: h.Username, NodeID: nodeParam(h.NodeID), CreatedAt: now, UpdatedAt: now,
		})
		if isForeignKey(err) {
			return admin.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("store the database host: %w", err)
		}
		id = uint64(res)
		return s.seal(ctx, q, "database_hosts", id, "password", h.Password)
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// isForeignKey says whether the error is a refusal because a row that is pointed at is not there (MariaDB 1452).
func isForeignKey(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1452
}

func (s adminHosts) ListHosts(ctx context.Context) ([]admin.HostInfo, error) {
	rows, err := s.q.ListDatabaseHosts(ctx)
	if err != nil {
		return nil, fmt.Errorf("list the database hosts: %w", err)
	}
	out := make([]admin.HostInfo, len(rows))
	for i, r := range rows {
		info := admin.HostInfo{ID: uint64(r.ID), Name: r.Name, Host: r.Host, Port: int(r.Port), Username: r.Username, Databases: r.Databases}
		if r.NodeID.Valid {
			n := uint64(r.NodeID.Int32)
			info.NodeID = &n
		}
		out[i] = info
	}
	return out, nil
}

func (s adminHosts) FindHost(ctx context.Context, id uint64) (admin.HostRecord, error) {
	r, err := s.q.DatabaseHostByID(ctx, uint32(id))
	if errors.Is(err, sql.ErrNoRows) {
		return admin.HostRecord{}, admin.ErrNotFound
	} else if err != nil {
		return admin.HostRecord{}, fmt.Errorf("find database host %d: %w", id, err)
	}
	return s.record(ctx, r.ID, r.Port, r.Name, r.Host, r.Username, r.NodeID)
}

// ChangeHost keeps the row locked while change runs, which signs in to the host: nobody else changes it meanwhile.
func (s adminHosts) ChangeHost(ctx context.Context, id uint64, change func(current admin.HostRecord) (admin.HostRecord, error)) error {
	return s.inTx(ctx, func(q *dbq.Queries) error {
		r, err := q.LockDatabaseHost(ctx, uint32(id))
		if errors.Is(err, sql.ErrNoRows) {
			return admin.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock database host %d: %w", id, err)
		}
		current, err := s.record(ctx, r.ID, r.Port, r.Name, r.Host, r.Username, r.NodeID)
		if err != nil {
			return err
		}
		next, err := change(current)
		if err != nil {
			return err
		}
		err = q.UpdateDatabaseHost(ctx, dbq.UpdateDatabaseHostParams{
			Name: next.Name, Host: next.Host, Port: uint32(next.Port), Username: next.Username, NodeID: nodeParam(next.NodeID),
			UpdatedAt: sql.NullTime{Time: s.now().UTC(), Valid: true}, ID: uint32(id),
		})
		if isForeignKey(err) {
			return admin.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("update database host %d: %w", id, err)
		}
		return s.seal(ctx, q, "database_hosts", id, "password", next.Password)
	})
}

func (s adminHosts) DeleteHost(ctx context.Context, id uint64, check func(databases int64) error) error {
	return s.inTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.LockDatabaseHost(ctx, uint32(id)); errors.Is(err, sql.ErrNoRows) {
			return admin.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock database host %d: %w", id, err)
		}
		n, err := q.CountDatabasesOnHost(ctx, uint32(id))
		if err != nil {
			return fmt.Errorf("count the databases on host %d: %w", id, err)
		}
		if err := check(n); err != nil {
			return err
		}
		if _, err := q.DeleteDatabaseHost(ctx, uint32(id)); err != nil {
			return fmt.Errorf("delete database host %d: %w", id, err)
		}
		if err := q.DeleteSecretsOfRow(ctx, dbq.DeleteSecretsOfRowParams{TableName: "database_hosts", RowID: id}); err != nil {
			return fmt.Errorf("remove the secrets of database host %d: %w", id, err)
		}
		return nil
	})
}

func (s dbHostLookup) Conn(ctx context.Context, hostID uint64) (dbhosts.Conn, error) {
	h, err := adminHosts{s.DB}.FindHost(ctx, hostID)
	if errors.Is(err, admin.ErrNotFound) {
		return dbhosts.Conn{}, dbhosts.ErrNoHost
	} else if err != nil {
		return dbhosts.Conn{}, err
	}
	return h.Conn(), nil
}

// NodeConfig reads what the configuration file of a node needs, apart from the token: a node that was migrated and has no sealed token yet
// can be rotated all the same, which is the way out of an old token that counts as compromised.
func (s adminRotation) NodeConfig(ctx context.Context, id uint64) (nodes.WingsNode, error) {
	if id == 0 || id > 1<<32-1 {
		return nodes.WingsNode{}, admin.ErrNotFound
	}
	row, err := s.q.NodeForConfig(ctx, uint32(id))
	if errors.Is(err, sql.ErrNoRows) {
		return nodes.WingsNode{}, admin.ErrNotFound
	} else if err != nil {
		return nodes.WingsNode{}, fmt.Errorf("read node %d: %w", id, err)
	}
	mounts, err := s.q.AllowedMountsOfNode(ctx, uint32(id))
	if err != nil {
		return nodes.WingsNode{}, fmt.Errorf("read the mounts of node %d: %w", id, err)
	}
	return nodes.WingsNode{
		UUID: row.Uuid, FQDN: row.Fqdn, Scheme: row.Scheme, BehindProxy: row.BehindProxy, DaemonBase: row.Daemonbase, DaemonSFTP: int(row.Daemonsftp),
		DaemonListen: int(row.Daemonlisten), UploadSize: int64(row.UploadSize), AllowedMounts: mounts,
	}, nil
}

// ReplaceNodeToken changes the token id and the sealed token of a node in one transaction, with the row locked, so that the token id the
// authorizer looks up and the token it checks never disagree.
func (s adminRotation) ReplaceNodeToken(ctx context.Context, id uint64, creds nodes.Credentials) error {
	if id == 0 || id > 1<<32-1 {
		return admin.ErrNotFound
	}
	return s.inTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.LockNode(ctx, uint32(id)); errors.Is(err, sql.ErrNoRows) {
			return admin.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock node %d: %w", id, err)
		}
		now := sql.NullTime{Time: s.now().UTC(), Valid: true}
		if err := q.UpdateNodeTokenID(ctx, dbq.UpdateNodeTokenIDParams{DaemonTokenID: creds.TokenID, UpdatedAt: now, ID: uint32(id)}); err != nil {
			return fmt.Errorf("store the token id: %w", err)
		}
		return s.seal(ctx, q, "nodes", id, "daemon_token", creds.Token)
	})
}

// AdminDatabases is the storage of the rotation of the passwords of server databases.
func (d *DB) AdminDatabases() admin.DatabaseRotationStore { return adminDatabases{d} }

type adminDatabases struct{ *DB }

// Databases lists the databases of servers with what is needed to change their passwords, all of them or the ones on a host.
func (s adminDatabases) Databases(ctx context.Context, hostID *uint64) ([]admin.ServerDatabase, error) {
	query := "SELECT `id`, `server_id`, `database_host_id`, `database`, `username`, `remote`, `max_connections` FROM `databases`"
	var args []any
	if hostID != nil {
		query += " WHERE `database_host_id` = ?"
		args = append(args, *hostID)
	}
	rows, err := s.sql.QueryContext(ctx, query+" ORDER BY `id`", args...)
	if err != nil {
		return nil, fmt.Errorf("list the databases of the servers: %w", err)
	}
	defer rows.Close()
	var out []admin.ServerDatabase
	for rows.Next() {
		var d admin.ServerDatabase
		var maxConn sql.NullInt64
		if err := rows.Scan(&d.ID, &d.ServerID, &d.HostID, &d.Name, &d.Username, &d.Remote, &maxConn); err != nil {
			return nil, fmt.Errorf("read a database: %w", err)
		}
		d.MaxConnections = int(maxConn.Int64)
		out = append(out, d)
	}
	return out, rows.Err()
}

// SetPassword seals a new password for the database, with its row locked.
func (s adminDatabases) SetPassword(ctx context.Context, id uint64, password string) error {
	if err := (serverDatabases{s.DB}).setPassword(ctx, id, password); errors.Is(err, appkit.ErrNotFound) {
		return admin.ErrNotFound
	} else {
		return err
	}
}

// SetPassword changes the password of a user, with the row locked, and ends what the old one had opened: the sessions, and the reset links asked for with the
// address of the user.
func (s adminStore) SetPassword(ctx context.Context, id uint64, hash string) error {
	return s.inTx(ctx, func(q *dbq.Queries) error {
		email, err := q.LockUserEmail(ctx, uint32(id))
		if errors.Is(err, sql.ErrNoRows) {
			return admin.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("find user %d: %w", id, err)
		}
		if _, err := q.SetUserPassword(ctx, dbq.SetUserPasswordParams{Password: hash, UpdatedAt: sql.NullTime{Time: s.now().UTC(), Valid: true}, ID: uint32(id)}); err != nil {
			return fmt.Errorf("store the password of user %d: %w", id, err)
		}
		if err := q.DeleteSessionsForUser(ctx, sql.NullInt32{Int32: int32(id), Valid: true}); err != nil {
			return fmt.Errorf("end the sessions of user %d: %w", id, err)
		}
		if _, err := q.DeletePasswordResets(ctx, email); err != nil {
			return fmt.Errorf("remove the reset links of user %d: %w", id, err)
		}
		return nil
	})
}

// CountUsers counts the users.
func (s adminStore) CountUsers(ctx context.Context) (int64, error) {
	n, err := s.q.CountUsers(ctx)
	if err != nil {
		return 0, fmt.Errorf("count the users: %w", err)
	}
	return n, nil
}

// SetAllPasswords gives every user the same password hash and ends every session and reset link, in one transaction. The number is taken in the same
// transaction, from the rows that were changed.
func (s adminStore) SetAllPasswords(ctx context.Context, hash string) (int64, error) {
	var n int64
	err := s.inTx(ctx, func(q *dbq.Queries) (err error) {
		if n, err = q.SetAllPasswords(ctx, dbq.SetAllPasswordsParams{Password: hash, UpdatedAt: sql.NullTime{Time: s.now().UTC(), Valid: true}}); err != nil {
			return fmt.Errorf("take the passwords: %w", err)
		}
		if err = q.DeleteAllSessions(ctx); err != nil {
			return fmt.Errorf("end the sessions: %w", err)
		}
		if err = q.DeleteAllPasswordResets(ctx); err != nil {
			return fmt.Errorf("remove the reset links: %w", err)
		}
		return nil
	})
	return n, err
}
