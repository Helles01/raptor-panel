package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Helles01/raptor-panel/internal/activity"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/db/dbq"
	"github.com/Helles01/raptor-panel/internal/servers"
	"github.com/Helles01/raptor-panel/internal/settings"
)

// AuthzUsers is the user store of the authorizer.
func (d *DB) AuthzUsers() authz.Users { return authzUsers{d} }

// AuthzKeys is the key store of the authorizer.
func (d *DB) AuthzKeys() authz.Keys { return authzKeys{d} }

// AuthzServers is the server store of the authorizer.
func (d *DB) AuthzServers() authz.Servers { return authzServers{d} }

// AuthzNodes is the node store of the authorizer.
func (d *DB) AuthzNodes() authz.Nodes { return authzNodes{d} }

// AuthzSettings is the settings store of the authorizer.
func (d *DB) AuthzSettings() authz.Settings { return authzSettings{d} }

// AuthzAudit is the log of the authorizer.
func (d *DB) AuthzAudit() authz.Audit { return authzAudit{d} }

type (
	authzUsers    struct{ *DB }
	authzKeys     struct{ *DB }
	authzServers  struct{ *DB }
	authzNodes    struct{ *DB }
	authzSettings struct{ *DB }
	authzAudit    struct{ *DB }
)

func (s authzUsers) ByID(ctx context.Context, id uint64) (authz.User, error) {
	row, err := s.q.UserByID(ctx, uint32(id))
	switch {
	case errors.Is(err, sql.ErrNoRows) || id > 1<<32-1:
		return authz.User{}, authz.ErrNoUser
	case err != nil:
		return authz.User{}, fmt.Errorf("find user %d: %w", id, err)
	}
	return authz.User{ID: uint64(row.ID), UUID: row.Uuid, RootAdmin: row.RootAdmin != 0, UsesTOTP: row.UseTotp != 0}, nil
}

// allowedIPs reads the list of an API key. The reference stores a JSON array; a value that is not one is kept as a single entry that no
// address matches, so that a damaged list restricts the key instead of freeing it.
func allowedIPs(stored sql.NullString) []string {
	if !stored.Valid || stored.String == "" || stored.String == "null" {
		return nil
	}
	var list []string
	if err := json.Unmarshal([]byte(stored.String), &list); err != nil {
		return []string{stored.String}
	}
	return list
}

// ByIdentifier finds a key. A key whose time has passed (the reference never reads the column, but a time set in it means the end of the key) is
// not found. The hash of the secret is read from raptor_secrets: the hash itself for a key made by Raptor (token_sha256), or the secret of a key that
// came from the old panel (token), which is hashed here.
func (s authzKeys) ByIdentifier(ctx context.Context, identifier string) (authz.Key, error) {
	row, err := s.q.APIKeyByIdentifier(ctx, sql.NullString{String: identifier, Valid: true})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return authz.Key{}, authz.ErrNoKey
	case err != nil:
		return authz.Key{}, fmt.Errorf("find key: %w", err)
	}
	if row.ExpiresAt.Valid && !row.ExpiresAt.Time.After(s.now()) {
		return authz.Key{}, authz.ErrNoKey
	}

	id := uint64(row.ID)
	hash, ok, err := s.secret(ctx, "api_keys", id, "token_sha256")
	if err != nil {
		return authz.Key{}, err
	}
	if !ok {
		secret, found, err := s.secret(ctx, "api_keys", id, "token")
		if err != nil {
			return authz.Key{}, err
		}
		if !found {
			// A key with no secret of Raptor's cannot be checked, so it is not a key (migrate-secrets has not seen it yet).
			return authz.Key{}, authz.ErrNoKey
		}
		hash = auth.HashKeySecret(secret)
	}

	key := authz.Key{
		ID: id, UserID: uint64(row.UserID), Type: auth.KeyType(row.KeyType), Identifier: row.Identifier.String,
		SecretHash: hash, AllowedIPs: allowedIPs(row.AllowedIps), LastUsedAt: timePtr(row.LastUsedAt),
	}
	if key.Type == auth.KeyApplication {
		levels := []uint8{row.RServers, row.RNodes, row.RAllocations, row.RUsers, row.RLocations, row.RNests, row.REggs, row.RDatabaseHosts, row.RServerDatabases}
		resources := auth.ApplicationResources()
		key.Access = make(map[string]auth.Access, len(resources))
		for i, r := range resources {
			key.Access[r] = auth.Access(levels[i])
		}
	}
	return key, nil
}

func (s authzKeys) Touch(ctx context.Context, id uint64, at time.Time) error {
	if err := s.q.TouchAPIKey(ctx, dbq.TouchAPIKeyParams{LastUsedAt: nullTime(&at), ID: uint32(id)}); err != nil {
		return fmt.Errorf("touch key %d: %w", id, err)
	}
	return nil
}

// Find resolves the {server} of a route: the first eight characters of the UUID, or the whole UUID.
func (s authzServers) Find(ctx context.Context, ref string) (authz.Server, error) {
	var (
		id, ownerID, nodeID uint32
		uuid, short, node   string
		status              sql.NullString
		maintenance         bool
		transfers           int64
	)
	switch len(ref) {
	case 8:
		row, err := s.q.ServerByShortUUID(ctx, ref)
		if err != nil {
			return authz.Server{}, noServer(err, ref)
		}
		id, ownerID, nodeID, uuid, short, node, status, maintenance, transfers = row.ID, row.OwnerID, row.NodeID, row.Uuid, row.Uuidshort, row.NodeName, row.Status, row.MaintenanceMode, row.Transfers
	case 36:
		row, err := s.q.ServerByUUID(ctx, ref)
		if err != nil {
			return authz.Server{}, noServer(err, ref)
		}
		id, ownerID, nodeID, uuid, short, node, status, maintenance, transfers = row.ID, row.OwnerID, row.NodeID, row.Uuid, row.Uuidshort, row.NodeName, row.Status, row.MaintenanceMode, row.Transfers
	default:
		return authz.Server{}, authz.ErrNoServer
	}
	return authz.Server{
		ID: uint64(id), UUID: uuid, ShortID: short, OwnerID: uint64(ownerID), NodeID: uint64(nodeID), NodeName: node,
		State: servers.State{Status: status.String, NodeMaintenance: maintenance, Transferring: transfers > 0},
	}, nil
}

func noServer(err error, ref string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return authz.ErrNoServer
	}
	return fmt.Errorf("find server %q: %w", ref, err)
}

// SubuserPermissions returns the permissions of a subuser. A list that cannot be read gives none, which is no right at all.
func (s authzServers) SubuserPermissions(ctx context.Context, serverID, userID uint64) ([]string, bool, error) {
	raw, err := s.q.SubuserPermissions(ctx, dbq.SubuserPermissionsParams{ServerID: uint32(serverID), UserID: uint32(userID)})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("find the subuser %d of server %d: %w", userID, serverID, err)
	}
	var perms []string
	if raw.Valid && json.Unmarshal([]byte(raw.String), &perms) != nil {
		perms = nil
	}
	return perms, true, nil
}

// ByTokenID finds a node by the first half of its token and gives the token, opened from raptor_secrets.
func (s authzNodes) ByTokenID(ctx context.Context, tokenID string) (authz.NodeCredentials, error) {
	row, err := s.q.NodeByTokenID(ctx, tokenID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return authz.NodeCredentials{}, authz.ErrNoNode
	case err != nil:
		return authz.NodeCredentials{}, fmt.Errorf("find node: %w", err)
	}
	token, ok, err := s.secret(ctx, "nodes", uint64(row.ID), "daemon_token")
	if err != nil {
		return authz.NodeCredentials{}, err
	}
	if !ok {
		return authz.NodeCredentials{}, authz.ErrNoNode
	}
	return authz.NodeCredentials{Node: authz.Node{ID: uint64(row.ID), UUID: row.Uuid}, Token: token}, nil
}

// TwoFactorLevel is the requirement of the panel: 0 none, 1 administrators, 2 everybody. A panel that was never set has none, as the
// reference has; a value that is not one of the three is an error, not a guess.
func (s authzSettings) TwoFactorLevel(ctx context.Context) (int, error) {
	v, err := s.q.SettingValue(ctx, settings.StorageKey("pterodactyl:auth:2fa_required"))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("read the two-factor setting: %w", err)
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > 2 {
		return 0, fmt.Errorf("the two-factor setting is %q, not 0, 1 or 2", v)
	}
	return n, nil
}

// IPBlocked records that a key was used from an address it may not be used from.
func (s authzAudit) IPBlocked(ctx context.Context, userID uint64, keyIdentifier, ip, userAgent string) error {
	props, err := activity.Properties(activity.AuthIPBlocked, activity.WithRequestMetadata(map[string]any{"identifier": keyIdentifier}, ip, userAgent))
	if err != nil {
		return fmt.Errorf("make the entry: %w", err)
	}
	return s.logActivity(ctx, entry{Event: activity.AuthIPBlocked, Subject: userID, IP: ip, Properties: props})
}
