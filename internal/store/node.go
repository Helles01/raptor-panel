package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Helles01/raptor-panel/internal/activity"
	"github.com/Helles01/raptor-panel/internal/api/controlapi"
	"github.com/Helles01/raptor-panel/internal/api/remoteapi"
	"github.com/Helles01/raptor-panel/internal/backups"
	"github.com/Helles01/raptor-panel/internal/db/dbq"
	"github.com/Helles01/raptor-panel/internal/outbox"
	"github.com/Helles01/raptor-panel/internal/remote"
	"github.com/Helles01/raptor-panel/internal/servers"
	"github.com/Helles01/raptor-panel/internal/wings"
)

// serverModel, backupModel are how the reference writes the models of a server and of a backup in the activity log.
const (
	serverModel = "Pterodactyl\\Models\\Server"
	backupModel = "Pterodactyl\\Models\\Backup"
)

// ControlTargets finds the node of a server, with its token opened.
func (d *DB) ControlTargets() controlapi.Targets { return controlTargets{d} }

// ControlActivity is the activity log of the control routes.
func (d *DB) ControlActivity() controlapi.ActivityLog { return controlActivity{d} }

// RemoteServers is the server store of the node's API.
func (d *DB) RemoteServers() remoteapi.Servers { return remoteServers{d} }

// RemoteUsers is the user store of the node's SFTP sign-in.
func (d *DB) RemoteUsers() remoteapi.Users { return remoteUsers{d} }

// RemoteActivity stores the activity a node reports.
func (d *DB) RemoteActivity() remoteapi.ActivitySink { return remoteActivity{d} }

// RemoteEvents is the log of what the panel itself writes when a node reports.
func (d *DB) RemoteEvents() remoteapi.EventLog { return remoteEvents{d} }

// RemoteBackups is the backup store of the node's API.
func (d *DB) RemoteBackups() remoteapi.Backups { return remoteBackups{d} }

// RemoteOutbox is the mail of the node's API, as jobs of the outbox.
func (d *DB) RemoteOutbox() remoteapi.Outbox { return outbox.Mailer{Queue: outboxQueue{d}} }

type (
	controlTargets  struct{ *DB }
	controlActivity struct{ *DB }
	remoteServers   struct{ *DB }
	remoteUsers     struct{ *DB }
	remoteActivity  struct{ *DB }
	remoteEvents    struct{ *DB }
	remoteBackups   struct{ *DB }
)

// daemonNode finds how to reach a node, with its token opened. ok is false for a node with no sealed token: it cannot be talked to.
func (d *DB) daemonNode(ctx context.Context, scheme, fqdn string, port uint16, nodeID uint64) (wings.Node, error) {
	token, ok, err := d.secret(ctx, "nodes", nodeID, "daemon_token")
	if err != nil {
		return wings.Node{}, err
	}
	if !ok {
		return wings.Node{}, fmt.Errorf("node %d has no token of Raptor's: run raptor migrate-secrets or make the node again", nodeID)
	}
	return wings.Node{Scheme: scheme, FQDN: fqdn, Port: int(port), Token: token}, nil
}

func (s controlTargets) For(ctx context.Context, serverID uint64) (controlapi.Target, error) {
	row, err := s.q.TargetOfServer(ctx, uint32(serverID))
	switch {
	case errors.Is(err, sql.ErrNoRows) || serverID > 1<<32-1:
		return controlapi.Target{}, controlapi.ErrNotFound
	case err != nil:
		return controlapi.Target{}, fmt.Errorf("find server %d: %w", serverID, err)
	}
	node, err := s.daemonNode(ctx, row.Scheme, row.Fqdn, row.Daemonlisten, uint64(row.NodeID))
	if err != nil {
		return controlapi.Target{}, err
	}
	target := controlapi.Target{UUID: row.Uuid, Node: node}

	t, err := s.q.OpenTransferOfServer(ctx, uint32(serverID))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return target, nil
	case err != nil:
		return controlapi.Target{}, fmt.Errorf("find the transfer of server %d: %w", serverID, err)
	}
	newNode, err := s.q.NodeForDaemon(ctx, t.NewNode)
	if err != nil {
		return controlapi.Target{}, fmt.Errorf("find the node server %d is moving to: %w", serverID, err)
	}
	moving, err := s.daemonNode(ctx, newNode.Scheme, newNode.Fqdn, newNode.Daemonlisten, uint64(newNode.ID))
	if err != nil {
		return controlapi.Target{}, err
	}
	target.Transfer = &controlapi.Transfer{Archived: t.Archived, NewNode: moving}
	return target, nil
}

func (s controlActivity) Log(ctx context.Context, e controlapi.Entry) error {
	var key uint64
	if e.APIKeyID != nil {
		key = *e.APIKeyID
	}
	return s.logActivity(ctx, entry{Event: e.Event, SubjectType: serverModel, Subject: e.ServerID, Actor: e.ActorID, APIKeyID: key, IP: e.IP, Properties: e.Properties})
}

// --- the servers as the node's configuration wants them

// core is what the two ways of reading a server (a page of a node, one by UUID) have in common.
type core struct {
	ID              uint64
	UUID            string
	Name            string
	Description     string
	Status          string
	Startup         string
	Image           string
	Memory, Swap    int64
	Disk, IO, CPU   int64
	Threads         *string
	OOMDisabled     bool
	SkipScripts     bool
	AllocationLimit *int64
	AllocationID    uint64
	LocationShort   string
	EggUUID         string
	ForceOutgoingIP bool
	FileDenylist    sql.NullString
	ConfigFrom      sql.NullInt32
	ConfigFiles     sql.NullString
	ConfigStartup   sql.NullString
	ConfigStop      sql.NullString
	ParentFiles     sql.NullString
	ParentStartup   sql.NullString
	ParentStop      sql.NullString
}

func nullStr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	v := s.String
	return &v
}

func coreOfPage(r dbq.ServersOfNodeRow) core {
	c := core{
		ID: uint64(r.ID), UUID: r.Uuid, Name: r.Name, Description: r.Description, Status: r.Status.String, Startup: r.Startup, Image: r.Image,
		Memory: int64(r.Memory), Swap: int64(r.Swap), Disk: int64(r.Disk), IO: int64(r.Io), CPU: int64(r.Cpu), Threads: nullStr(r.Threads),
		OOMDisabled: r.OomDisabled != 0, SkipScripts: r.SkipScripts, AllocationID: uint64(r.AllocationID), LocationShort: r.LocationShort,
		EggUUID: r.EggUuid, ForceOutgoingIP: r.ForceOutgoingIp, FileDenylist: r.FileDenylist, ConfigFrom: r.ConfigFrom,
		ConfigFiles: r.ConfigFiles, ConfigStartup: r.ConfigStartup, ConfigStop: r.ConfigStop,
		ParentFiles: r.ParentConfigFiles, ParentStartup: r.ParentConfigStartup, ParentStop: r.ParentConfigStop,
	}
	if r.AllocationLimit.Valid {
		v := int64(r.AllocationLimit.Int32)
		c.AllocationLimit = &v
	}
	return c
}

func coreOfOne(r dbq.ServerForNodeByUUIDRow) core {
	c := core{
		ID: uint64(r.ID), UUID: r.Uuid, Name: r.Name, Description: r.Description, Status: r.Status.String, Startup: r.Startup, Image: r.Image,
		Memory: int64(r.Memory), Swap: int64(r.Swap), Disk: int64(r.Disk), IO: int64(r.Io), CPU: int64(r.Cpu), Threads: nullStr(r.Threads),
		OOMDisabled: r.OomDisabled != 0, SkipScripts: r.SkipScripts, AllocationID: uint64(r.AllocationID), LocationShort: r.LocationShort,
		EggUUID: r.EggUuid, ForceOutgoingIP: r.ForceOutgoingIp, FileDenylist: r.FileDenylist, ConfigFrom: r.ConfigFrom,
		ConfigFiles: r.ConfigFiles, ConfigStartup: r.ConfigStartup, ConfigStop: r.ConfigStop,
		ParentFiles: r.ParentConfigFiles, ParentStartup: r.ParentConfigStartup, ParentStop: r.ParentConfigStop,
	}
	if r.AllocationLimit.Valid {
		v := int64(r.AllocationLimit.Int32)
		c.AllocationLimit = &v
	}
	return c
}

// jsonStrings reads a JSON array of strings; a column that is empty or damaged is an empty list.
func jsonStrings(s sql.NullString) []string {
	var out []string
	if s.Valid && json.Unmarshal([]byte(s.String), &out) != nil {
		return nil
	}
	return out
}

// build makes the servers with their allocations, variables and mounts, three queries for any number of servers.
func (d *DB) build(ctx context.Context, cores []core) ([]servers.Server, error) {
	if len(cores) == 0 {
		return nil, nil
	}
	ids := make([]uint32, len(cores))
	nullIDs := make([]sql.NullInt32, len(cores))
	for i, c := range cores {
		ids[i] = uint32(c.ID)
		nullIDs[i] = sql.NullInt32{Int32: int32(c.ID), Valid: true}
	}
	allocs, err := d.q.AllocationsOfServers(ctx, nullIDs)
	if err != nil {
		return nil, fmt.Errorf("read the allocations: %w", err)
	}
	vars, err := d.q.VariablesOfServers(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("read the variables: %w", err)
	}
	mounts, err := d.q.MountsOfServers(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("read the mounts: %w", err)
	}

	out := make([]servers.Server, len(cores))
	at := make(map[uint64]int, len(cores))
	for i, c := range cores {
		files, startup, stop := c.ConfigFiles, c.ConfigStartup, c.ConfigStop
		if c.ConfigFrom.Valid {
			// An egg that takes its process configuration from another has the other's.
			files, startup, stop = c.ParentFiles, c.ParentStartup, c.ParentStop
		}
		out[i] = servers.Server{
			UUID: c.UUID, Name: c.Name, Description: c.Description, Status: c.Status, Startup: c.Startup, Image: c.Image,
			Memory: c.Memory, Swap: c.Swap, Disk: c.Disk, IO: c.IO, CPU: c.CPU, Threads: c.Threads, OOMDisabled: c.OOMDisabled, SkipScripts: c.SkipScripts,
			AllocationLimit: c.AllocationLimit, LocationShort: c.LocationShort,
			Egg: servers.Egg{UUID: c.EggUUID, ForceOutgoingIP: c.ForceOutgoingIP, FileDenylist: jsonStrings(c.FileDenylist), ConfigFiles: files.String, ConfigStartup: startup.String, ConfigStop: stop.String},
		}
		at[c.ID] = i
	}
	for _, a := range allocs {
		i, ok := at[uint64(a.ServerID.Int32)]
		if !ok {
			continue
		}
		al := servers.Allocation{IP: a.Ip, Port: int(a.Port)}
		out[i].Allocations = append(out[i].Allocations, al)
		if uint64(a.ID) == cores[i].AllocationID {
			out[i].Allocation = al
		}
	}
	for _, v := range vars {
		i, ok := at[uint64(v.ServerID)]
		if !ok {
			continue
		}
		out[i].Variables = append(out[i].Variables, servers.Variable{EnvName: v.EnvVariable, Default: v.DefaultValue, Value: nullStr(v.Value), UserViewable: v.UserViewable != 0})
	}
	for _, m := range mounts {
		i, ok := at[uint64(m.ServerID)]
		if !ok {
			continue
		}
		out[i].Mounts = append(out[i].Mounts, servers.Mount{Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly != 0})
	}
	return out, nil
}

func (s remoteServers) List(ctx context.Context, nodeID uint64, offset, limit int) (remoteapi.ServerPage, error) {
	total, err := s.q.CountServersOfNode(ctx, uint32(nodeID))
	if err != nil {
		return remoteapi.ServerPage{}, fmt.Errorf("count the servers of node %d: %w", nodeID, err)
	}
	rows, err := s.q.ServersOfNode(ctx, dbq.ServersOfNodeParams{NodeID: uint32(nodeID), Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return remoteapi.ServerPage{}, fmt.Errorf("read the servers of node %d: %w", nodeID, err)
	}
	cores := make([]core, len(rows))
	for i, r := range rows {
		cores[i] = coreOfPage(r)
	}
	list, err := s.build(ctx, cores)
	if err != nil {
		return remoteapi.ServerPage{}, err
	}
	return remoteapi.ServerPage{Total: int(total), Servers: list}, nil
}

func remoteTransfer(t dbq.OpenTransferOfServerRow) *remote.Transfer {
	return &remote.Transfer{
		OldNode: uint64(t.OldNode), NewNode: uint64(t.NewNode), OldAllocation: uint64(t.OldAllocation), NewAllocation: uint64(t.NewAllocation),
		OldAdditional: jsonIDs(t.OldAdditionalAllocations), NewAdditional: jsonIDs(t.NewAdditionalAllocations),
	}
}

// jsonIDs reads a JSON array of numbers; a column that is empty or damaged is an empty list.
func jsonIDs(s sql.NullString) []uint64 {
	var out []uint64
	if s.Valid && json.Unmarshal([]byte(s.String), &out) != nil {
		return nil
	}
	return out
}

// reach finds a server by UUID and the transfer it is in, and says whether the node may see it: it is on the node, or the transfer moves it to or
// from the node. A server the node may not see is not there.
func (s remoteServers) reach(ctx context.Context, nodeID uint64, uuid string) (dbq.ServerForNodeByUUIDRow, *remote.Transfer, error) {
	row, err := s.q.ServerForNodeByUUID(ctx, uuid)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return row, nil, remoteapi.ErrNotFound
	case err != nil:
		return row, nil, fmt.Errorf("find server %q: %w", uuid, err)
	}
	var transfer *remote.Transfer
	t, err := s.q.OpenTransferOfServer(ctx, row.ID)
	switch {
	case err == nil:
		transfer = remoteTransfer(t)
	case !errors.Is(err, sql.ErrNoRows):
		return row, nil, fmt.Errorf("find the transfer of server %q: %w", uuid, err)
	}
	if uint64(row.NodeID) != nodeID && (transfer == nil || (transfer.OldNode != nodeID && transfer.NewNode != nodeID)) {
		return row, nil, remoteapi.ErrNotFound
	}
	return row, transfer, nil
}

func (s remoteServers) Reachable(ctx context.Context, nodeID uint64, uuid string) (remoteapi.Detail, error) {
	row, transfer, err := s.reach(ctx, nodeID, uuid)
	if err != nil {
		return remoteapi.Detail{}, err
	}
	list, err := s.build(ctx, []core{coreOfOne(row)})
	if err != nil {
		return remoteapi.Detail{}, err
	}
	return remoteapi.Detail{NodeID: uint64(row.NodeID), Transfer: transfer, Server: list[0]}, nil
}

func (s remoteServers) Transfer(ctx context.Context, nodeID uint64, uuid string) (remoteapi.TransferInfo, error) {
	row, transfer, err := s.reach(ctx, nodeID, uuid)
	if err != nil {
		return remoteapi.TransferInfo{}, err
	}
	return remoteapi.TransferInfo{ServerID: uint64(row.ID), Transfer: transfer}, nil
}

// Own finds a server that is on the node itself.
func (s remoteServers) Own(ctx context.Context, nodeID uint64, uuid string) (remoteapi.Held, error) {
	row, err := s.q.ServerForNodeByUUID(ctx, uuid)
	switch {
	case errors.Is(err, sql.ErrNoRows) || uint64(row.NodeID) != nodeID:
		return remoteapi.Held{}, remoteapi.ErrNotFound
	case err != nil:
		return remoteapi.Held{}, fmt.Errorf("find server %q: %w", uuid, err)
	}
	return remoteapi.Held{
		ID: uint64(row.ID), UUID: row.Uuid, NodeID: uint64(row.NodeID), Name: row.Name, Status: row.Status.String, InstalledBefore: row.InstalledAt.Valid,
		OwnerEmail: row.OwnerEmail, OwnerUsername: row.OwnerUsername,
	}, nil
}

func (s remoteServers) SFTP(ctx context.Context, nodeID uint64, shortID string, userID uint64) (remoteapi.SFTPServer, error) {
	row, err := s.q.SFTPServer(ctx, dbq.SFTPServerParams{NodeID: uint32(nodeID), Uuidshort: shortID})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return remoteapi.SFTPServer{}, remoteapi.ErrNotFound
	case err != nil:
		return remoteapi.SFTPServer{}, fmt.Errorf("find server %q: %w", shortID, err)
	}
	out := remoteapi.SFTPServer{
		ID: uint64(row.ID), UUID: row.Uuid, NodeID: uint64(row.NodeID), OwnerID: uint64(row.OwnerID),
		State: servers.State{Status: row.Status.String, NodeMaintenance: row.MaintenanceMode, Transferring: row.Transfers > 0},
	}
	raw, err := s.q.SubuserPermissions(ctx, dbq.SubuserPermissionsParams{ServerID: row.ID, UserID: uint32(userID)})
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return remoteapi.SFTPServer{}, fmt.Errorf("find the subuser entry: %w", err)
	default:
		// A list that cannot be read is a subuser with no right at all.
		out.Subuser = []string{}
		if raw.Valid {
			var perms []string
			if json.Unmarshal([]byte(raw.String), &perms) == nil {
				out.Subuser = perms
			}
		}
	}
	return out, nil
}

func (s remoteServers) InstallScript(ctx context.Context, serverID uint64) (remoteapi.InstallScript, error) {
	r, err := s.q.InstallScriptOfServer(ctx, uint32(serverID))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return remoteapi.InstallScript{}, remoteapi.ErrNotFound
	case err != nil:
		return remoteapi.InstallScript{}, fmt.Errorf("read the install script of server %d: %w", serverID, err)
	}
	// An egg that copies its script from another uses the other's container, entrypoint and script.
	if r.CopyScriptFrom.Valid {
		return remoteapi.InstallScript{ContainerImage: r.CopyContainer.String, Entrypoint: r.CopyEntry.String, Script: r.CopyScript.String}, nil
	}
	return remoteapi.InstallScript{ContainerImage: r.ScriptContainer, Entrypoint: r.ScriptEntry, Script: r.ScriptInstall.String}, nil
}

// Installed stores the result of an installation in one statement that holds only while the server still has the status the report was made against.
func (s remoteServers) Installed(ctx context.Context, serverID uint64, from string, o servers.InstallOutcome, at time.Time) (bool, error) {
	status := sql.NullString{String: o.Status, Valid: o.Status != ""}
	when := sql.NullTime{Time: at.UTC(), Valid: true}
	var n int64
	var err error
	if o.SetInstalledAt {
		n, err = s.q.SetInstallStatusAndTime(ctx, dbq.SetInstallStatusAndTimeParams{Status: status, InstalledAt: when, UpdatedAt: when, ID: uint32(serverID), FromStatus: sql.NullString{String: from, Valid: true}})
	} else {
		n, err = s.q.SetInstallStatus(ctx, dbq.SetInstallStatusParams{Status: status, UpdatedAt: when, ID: uint32(serverID), FromStatus: sql.NullString{String: from, Valid: true}})
	}
	if err != nil {
		return false, fmt.Errorf("store the result of the installation of server %d: %w", serverID, err)
	}
	return n == 1, nil
}

func (s remoteServers) Resettable(ctx context.Context, nodeID uint64) ([]remoteapi.ResetCandidate, error) {
	rows, err := s.q.ResettableServersOfNode(ctx, uint32(nodeID))
	if err != nil {
		return nil, fmt.Errorf("read the servers of node %d that were busy: %w", nodeID, err)
	}
	out := make([]remoteapi.ResetCandidate, len(rows))
	for i, r := range rows {
		c := remoteapi.ResetCandidate{ID: uint64(r.ID), Status: r.Status.String, InstalledBefore: r.InstalledAt.Valid}
		if c.Status == servers.StatusRestoringBackup {
			b, err := s.q.LastSuccessfulBackup(ctx, r.ID)
			switch {
			case errors.Is(err, sql.ErrNoRows):
			case err != nil:
				return nil, fmt.Errorf("find the backup server %d was restoring: %w", r.ID, err)
			default:
				c.Restoring = &remoteapi.BackupRef{ID: b.ID, Name: b.Name}
			}
		}
		out[i] = c
	}
	return out, nil
}

// Reset applies the changes in one transaction; a server that has another status than the one the change was made against is left alone.
func (s remoteServers) Reset(ctx context.Context, nodeID uint64, changes []remoteapi.ResetChange) error {
	return s.inTx(ctx, func(q *dbq.Queries) error {
		now := s.now().UTC()
		for _, c := range changes {
			n, err := q.ResetServerStatus(ctx, dbq.ResetServerStatusParams{
				Status: sql.NullString{String: c.To, Valid: c.To != ""}, UpdatedAt: sql.NullTime{Time: now, Valid: true},
				ID: uint32(c.ServerID), NodeID: uint32(nodeID), Status_2: sql.NullString{String: c.From, Valid: true},
			})
			if err != nil {
				return fmt.Errorf("reset server %d: %w", c.ServerID, err)
			}
			if n != 1 || c.RestoreFailed == nil {
				continue
			}
			props, err := activity.Properties(activity.ServerBackupRestoreFailed, map[string]any{"name": c.RestoreFailed.Name})
			if err != nil {
				return fmt.Errorf("make the entry: %w", err)
			}
			id, err := q.InsertActivityLog(ctx, dbq.InsertActivityLogParams{Event: activity.ServerBackupRestoreFailed, Properties: props, Timestamp: now})
			if err != nil {
				return fmt.Errorf("write the activity log: %w", err)
			}
			for _, sub := range []struct {
				kind string
				id   uint64
			}{{serverModel, c.ServerID}, {backupModel, c.RestoreFailed.ID}} {
				if err := q.InsertActivitySubject(ctx, dbq.InsertActivitySubjectParams{ActivityLogID: uint64(id), SubjectType: sub.kind, SubjectID: sub.id}); err != nil {
					return fmt.Errorf("write the subject of the activity log: %w", err)
				}
			}
		}
		return nil
	})
}

// CompleteTransfer ends the transfer that is running, with the server's row and the allocations changed in the same transaction. It is false
// when no transfer is running (somebody else ended it first).
func (s remoteServers) CompleteTransfer(ctx context.Context, serverID uint64, r remote.TransferResult) (bool, error) {
	done := false
	err := s.inTx(ctx, func(q *dbq.Queries) error {
		t, err := q.LockOpenTransfer(ctx, uint32(serverID))
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil
		case err != nil:
			return fmt.Errorf("lock the transfer of server %d: %w", serverID, err)
		}
		now := sql.NullTime{Time: s.now().UTC(), Valid: true}
		if err := q.FinishTransfer(ctx, dbq.FinishTransferParams{Successful: sql.NullBool{Bool: r.Successful, Valid: true}, UpdatedAt: now, ID: t.ID}); err != nil {
			return fmt.Errorf("end the transfer of server %d: %w", serverID, err)
		}
		if r.Move {
			if err := q.MoveServer(ctx, dbq.MoveServerParams{NodeID: uint32(r.NewNode), AllocationID: uint32(r.NewAllocation), UpdatedAt: now, ID: uint32(serverID)}); err != nil {
				return fmt.Errorf("move server %d: %w", serverID, err)
			}
		}
		if len(r.Release) > 0 {
			ids := make([]uint32, len(r.Release))
			for i, id := range r.Release {
				ids[i] = uint32(id)
			}
			if err := q.ReleaseAllocations(ctx, ids); err != nil {
				return fmt.Errorf("release the allocations of server %d: %w", serverID, err)
			}
		}
		done = true
		return nil
	})
	return done, err
}

// --- users, activity, backups

func (s remoteUsers) ByUsername(ctx context.Context, username string) (remoteapi.SFTPUser, error) {
	row, err := s.q.UserForSFTP(ctx, username)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return remoteapi.SFTPUser{}, remoteapi.ErrNotFound
	case err != nil:
		return remoteapi.SFTPUser{}, fmt.Errorf("find user: %w", err)
	}
	return remoteapi.SFTPUser{ID: uint64(row.ID), UUID: row.Uuid, Admin: row.RootAdmin != 0, PasswordHash: row.Password}, nil
}

func (s remoteUsers) Fingerprints(ctx context.Context, userID uint64) ([]string, error) {
	list, err := s.q.FingerprintsOfUser(ctx, uint32(userID))
	if err != nil {
		return nil, fmt.Errorf("read the SSH keys of user %d: %w", userID, err)
	}
	return list, nil
}

func (s remoteActivity) Servers(ctx context.Context, nodeID uint64, uuids []string) (map[string]uint64, error) {
	out := map[string]uint64{}
	if len(uuids) == 0 {
		return out, nil
	}
	rows, err := s.q.ServerIDsOfNode(ctx, dbq.ServerIDsOfNodeParams{NodeID: uint32(nodeID), Uuids: uuids})
	if err != nil {
		return nil, fmt.Errorf("find the servers of node %d: %w", nodeID, err)
	}
	for _, r := range rows {
		out[r.Uuid] = uint64(r.ID)
	}
	return out, nil
}

func (s remoteActivity) Users(ctx context.Context, uuids []string) (map[string]uint64, error) {
	out := map[string]uint64{}
	if len(uuids) == 0 {
		return out, nil
	}
	rows, err := s.q.UserIDsByUUID(ctx, uuids)
	if err != nil {
		return nil, fmt.Errorf("find the users: %w", err)
	}
	for _, r := range rows {
		out[r.Uuid] = uint64(r.ID)
	}
	return out, nil
}

// Store writes the entries a node reported with their subjects, all or none.
func (s remoteActivity) Store(ctx context.Context, entries []remoteapi.StoredEntry) error {
	return s.inTx(ctx, func(q *dbq.Queries) error {
		for _, e := range entries {
			p := dbq.InsertActivityLogParams{Event: e.Event, Ip: e.IP, Properties: e.Properties, Timestamp: e.When.UTC()}
			if e.UserID != 0 {
				p.ActorType = sql.NullString{String: userModel, Valid: true}
				p.ActorID = sql.NullInt64{Int64: int64(e.UserID), Valid: true}
			}
			id, err := q.InsertActivityLog(ctx, p)
			if err != nil {
				return fmt.Errorf("write the activity log: %w", err)
			}
			if err := q.InsertActivitySubject(ctx, dbq.InsertActivitySubjectParams{ActivityLogID: uint64(id), SubjectType: serverModel, SubjectID: e.ServerID}); err != nil {
				return fmt.Errorf("write the subject of the activity log: %w", err)
			}
		}
		return nil
	})
}

func (s remoteEvents) Log(ctx context.Context, e remoteapi.Event) error {
	return s.logActivity(ctx, entry{
		Event: e.Name, Actor: e.Actor, IP: e.IP, Properties: e.Properties,
		Subjects: []subject{{userModel, e.UserSubject}, {serverModel, e.ServerSubject}, {backupModel, e.BackupSubject}},
	})
}

func (s remoteBackups) OnNode(ctx context.Context, nodeID uint64, uuid string) (remoteapi.NodeBackup, error) {
	row, err := s.q.BackupOfNode(ctx, dbq.BackupOfNodeParams{Uuid: uuid, NodeID: uint32(nodeID)})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return remoteapi.NodeBackup{}, remoteapi.ErrNotFound
	case err != nil:
		return remoteapi.NodeBackup{}, fmt.Errorf("find backup %q: %w", uuid, err)
	}
	return remoteapi.NodeBackup{
		ID: row.ID, ServerID: uint64(row.ServerID), NodeID: uint64(row.NodeID), ServerUUID: row.ServerUuid, UUID: row.Uuid, Name: row.Name, Disk: row.Disk, UploadID: row.UploadID.String,
		Completed: row.CompletedAt.Valid, Successful: row.IsSuccessful, Locked: row.IsLocked != 0,
	}, nil
}

// SetUploadID keeps the id of the upload in one statement that holds only while the backup has not ended.
func (s remoteBackups) SetUploadID(ctx context.Context, backupID uint64, uploadID string) (bool, error) {
	n, err := s.q.SetBackupUploadID(ctx, dbq.SetBackupUploadIDParams{
		UploadID: sql.NullString{String: uploadID, Valid: uploadID != ""}, UpdatedAt: sql.NullTime{Time: s.now().UTC(), Valid: true}, ID: backupID,
	})
	if err != nil {
		return false, fmt.Errorf("store the upload of backup %d: %w", backupID, err)
	}
	return n == 1, nil
}

// Complete ends a backup in one statement that holds only while it has not ended, so a second report changes nothing.
func (s remoteBackups) Complete(ctx context.Context, backupID uint64, r backups.Result, at time.Time) (bool, error) {
	when := sql.NullTime{Time: at.UTC(), Valid: true}
	p := dbq.CompleteBackupParams{
		IsSuccessful: r.Successful, IsLocked: boolByte(r.Locked), Bytes: uint64(r.Bytes), CompletedAt: when, UpdatedAt: when, ID: backupID,
		Checksum: sql.NullString{String: r.Checksum, Valid: r.Checksum != ""},
	}
	n, err := s.q.CompleteBackup(ctx, p)
	if err != nil {
		return false, fmt.Errorf("store the end of backup %d: %w", backupID, err)
	}
	return n == 1, nil
}

func (s remoteBackups) FinishRestore(ctx context.Context, serverID uint64) (bool, error) {
	n, err := s.q.FinishRestoreOfServer(ctx, dbq.FinishRestoreOfServerParams{UpdatedAt: sql.NullTime{Time: s.now().UTC(), Valid: true}, ID: uint32(serverID)})
	if err != nil {
		return false, fmt.Errorf("finish the restore of server %d: %w", serverID, err)
	}
	return n == 1, nil
}
