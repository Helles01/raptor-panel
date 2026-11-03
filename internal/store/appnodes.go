package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Helles01/raptor-panel/internal/admin"
	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/appnodes"
	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/db/dbq"
	"github.com/Helles01/raptor-panel/internal/nodes"
	"github.com/Helles01/raptor-panel/internal/servers"
	"github.com/Helles01/raptor-panel/internal/wings"
)

// AppNodes is the node store of the Application API.
func (d *DB) AppNodes() appnodes.Store { return appNodes{d} }

type appNodes struct{ *DB }

func (s appNodes) withRelations(ctx context.Context, n *appkit.Node, load appkit.Includes) error {
	var err error
	if load["allocations"] {
		if n.Allocations, err = all(ctx, s.DB, scanAllocation, allocationSelect+" WHERE `allocations`.`node_id` = ? ORDER BY `allocations`.`id`", n.ID); err != nil {
			return fmt.Errorf("read the allocations of node %d: %w", n.ID, err)
		}
	}
	if load["location"] {
		l, err := one(ctx, s.DB, scanLocation, locationSelect+" WHERE `locations`.`id` = ?", n.Settings.LocationID)
		if err != nil && !errors.Is(err, appkit.ErrNotFound) {
			return fmt.Errorf("read the location of node %d: %w", n.ID, err)
		}
		if err == nil {
			n.Location = &l
		}
	}
	if load["servers"] {
		if n.Servers, err = s.serversOf(ctx, "`servers`.`node_id` = ?", n.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s appNodes) List(ctx context.Context, q listquery.Query, page, perPage int, load appkit.Includes) ([]appkit.Node, int, error) {
	where, args := q.Where()
	list, total, err := pageOf(ctx, s.DB, scanNode, "nodes", nodeSelect, where, q.OrderBy("id"), args, page, perPage)
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

func (s appNodes) Find(ctx context.Context, id uint64, load appkit.Includes) (appkit.Node, error) {
	n, err := one(ctx, s.DB, scanNode, nodeSelect+" WHERE `nodes`.`id` = ?", id)
	if err != nil {
		return n, err
	}
	return n, s.withRelations(ctx, &n, load)
}

// Create stores the node and seals its token in the same transaction.
func (s appNodes) Create(ctx context.Context, r appnodes.CreateRequest) (appkit.Node, error) {
	id, err := adminStore{s.DB}.CreateNode(ctx, admin.NewNode{UUID: r.UUID, Settings: r.Settings, Credentials: r.Credentials})
	if errors.Is(err, admin.ErrNotFound) {
		return appkit.Node{}, appnodes.ErrNoLocation
	}
	if err != nil {
		return appkit.Node{}, err
	}
	return s.Find(ctx, id, nil)
}

// Update stores the settings, and new credentials if there are some: the new token replaces the old sealed one in the same transaction, so that the
// token id the authorizer looks up and the token it checks never disagree.
func (s appNodes) Update(ctx context.Context, id uint64, set nodes.Settings, creds *nodes.Credentials) (appkit.Node, error) {
	err := s.inTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.LockNode(ctx, uint32(id)); errors.Is(err, sql.ErrNoRows) {
			return appkit.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock node %d: %w", id, err)
		}
		if c, err := q.LocationExists(ctx, uint32(set.LocationID)); err != nil {
			return fmt.Errorf("find location %d: %w", set.LocationID, err)
		} else if c == 0 {
			return appnodes.ErrNoLocation
		}
		now := sql.NullTime{Time: s.now().UTC(), Valid: true}
		err := q.UpdateNodeSettings(ctx, dbq.UpdateNodeSettingsParams{
			Public: uint16(boolByte(set.Public)), Name: set.Name, Description: nullable(set.Description), LocationID: uint32(set.LocationID), Fqdn: set.FQDN,
			Scheme: set.Scheme, BehindProxy: set.BehindProxy, MaintenanceMode: set.MaintenanceMode, Memory: uint32(set.Memory), MemoryOverallocate: int32(set.MemoryOverallocate),
			Disk: uint32(set.Disk), DiskOverallocate: int32(set.DiskOverallocate), UploadSize: uint32(set.UploadSize), Daemonlisten: uint16(set.DaemonListen),
			Daemonsftp: uint16(set.DaemonSFTP), Daemonbase: set.DaemonBase, UpdatedAt: now, ID: uint32(id),
		})
		if err != nil {
			return fmt.Errorf("store the node: %w", err)
		}
		if creds == nil {
			return nil
		}
		if err := q.UpdateNodeTokenID(ctx, dbq.UpdateNodeTokenIDParams{DaemonTokenID: creds.TokenID, UpdatedAt: now, ID: uint32(id)}); err != nil {
			return fmt.Errorf("store the token id: %w", err)
		}
		return s.seal(ctx, q, "nodes", id, "daemon_token", creds.Token)
	})
	if err != nil {
		return appkit.Node{}, err
	}
	return s.Find(ctx, id, nil)
}

// Delete counts the servers of the node with its row locked, and removes the node with its sealed token.
func (s appNodes) Delete(ctx context.Context, id uint64, check func(servers int64) error) error {
	return s.inTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.LockNode(ctx, uint32(id)); errors.Is(err, sql.ErrNoRows) {
			return appkit.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock node %d: %w", id, err)
		}
		n, err := q.CountServersOnNode(ctx, uint32(id))
		if err != nil {
			return fmt.Errorf("count the servers of node %d: %w", id, err)
		}
		if err := check(n); err != nil {
			return err
		}
		if err := q.DeleteSecretsOfRow(ctx, dbq.DeleteSecretsOfRowParams{TableName: "nodes", RowID: id}); err != nil {
			return fmt.Errorf("remove the secrets of node %d: %w", id, err)
		}
		return q.DeleteNode(ctx, uint32(id))
	})
}

// Config gathers what the configuration file of a node needs, with the token opened.
func (s appNodes) Config(ctx context.Context, id uint64) (nodes.WingsNode, nodes.Credentials, error) {
	row, err := s.q.NodeForConfig(ctx, uint32(id))
	if errors.Is(err, sql.ErrNoRows) || id > 1<<32-1 {
		return nodes.WingsNode{}, nodes.Credentials{}, appkit.ErrNotFound
	} else if err != nil {
		return nodes.WingsNode{}, nodes.Credentials{}, fmt.Errorf("read node %d: %w", id, err)
	}
	mounts, err := s.q.AllowedMountsOfNode(ctx, uint32(id))
	if err != nil {
		return nodes.WingsNode{}, nodes.Credentials{}, fmt.Errorf("read the mounts of node %d: %w", id, err)
	}
	token, ok, err := s.secret(ctx, "nodes", id, "daemon_token")
	if err != nil {
		return nodes.WingsNode{}, nodes.Credentials{}, err
	}
	if !ok {
		return nodes.WingsNode{}, nodes.Credentials{}, fmt.Errorf("node %d has no token of Raptor's: run raptor migrate-secrets or make the node again", id)
	}
	return nodes.WingsNode{
		UUID: row.Uuid, FQDN: row.Fqdn, Scheme: row.Scheme, BehindProxy: row.BehindProxy, DaemonBase: row.Daemonbase, DaemonSFTP: int(row.Daemonsftp),
		DaemonListen: int(row.Daemonlisten), UploadSize: int64(row.UploadSize), AllowedMounts: mounts,
	}, nodes.Credentials{TokenID: row.DaemonTokenID, Token: token}, nil
}

// Connection is how to reach the node as it is stored now.
func (s appNodes) Connection(ctx context.Context, id uint64) (wings.Node, error) {
	row, err := s.q.NodeForDaemon(ctx, uint32(id))
	if errors.Is(err, sql.ErrNoRows) || id > 1<<32-1 {
		return wings.Node{}, appkit.ErrNotFound
	} else if err != nil {
		return wings.Node{}, fmt.Errorf("read node %d: %w", id, err)
	}
	return s.daemonNode(ctx, row.Scheme, row.Fqdn, row.Daemonlisten, id)
}

// nodeUsage is the part of a node the choice of a place for a server needs.
func nodeUsage(n appkit.Node) servers.NodeUsage {
	return servers.NodeUsage{
		ID: n.ID, LocationID: n.Settings.LocationID, Public: n.Settings.Public, Memory: n.Settings.Memory, MemoryOverallocate: n.Settings.MemoryOverallocate,
		Disk: n.Settings.Disk, DiskOverallocate: n.Settings.DiskOverallocate, UsedMemory: n.AllocatedMemory, UsedDisk: n.AllocatedDisk,
	}
}

// Candidates lists every node with what its servers hold.
func (s appNodes) Candidates(ctx context.Context) ([]appnodes.Candidate, error) {
	list, err := all(ctx, s.DB, scanNode, nodeSelect+" ORDER BY `nodes`.`id`")
	if err != nil {
		return nil, fmt.Errorf("read the nodes: %w", err)
	}
	out := make([]appnodes.Candidate, len(list))
	for i, n := range list {
		out[i] = appnodes.Candidate{Usage: nodeUsage(n), Node: n}
	}
	return out, nil
}
