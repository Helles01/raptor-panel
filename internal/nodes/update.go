package nodes

import (
	"errors"
)

// Settings are the fields of a node an admin can edit, the same set as NewNode.
type Settings = NewNode

// Change is the body of PATCH /api/application/nodes/{id}. As in the reference the name, location, host, scheme,
// sizes and ports are needed every time. The optional fields are nil when they were not sent and keep their stored
// value; for Description a pointer to "" clears it.
//
// DaemonBase is the one that differs from the reference (C-044): there an update without it puts back the default
// path, and the node would no longer find the files of its servers.
type Change struct {
	Name               string
	Description        *string
	LocationID         uint64
	Public             *bool
	FQDN               string
	Scheme             string
	BehindProxy        *bool
	MaintenanceMode    *bool
	Memory             int64
	MemoryOverallocate int64
	Disk               int64
	DiskOverallocate   int64
	UploadSize         *int64
	DaemonBase         *string
	DaemonSFTP         int
	DaemonListen       int
}

// Apply returns the settings after the change, checked as a whole.
func (n Settings) Apply(ch Change) (Settings, error) {
	n.Name = ch.Name
	n.LocationID = ch.LocationID
	n.FQDN = ch.FQDN
	n.Scheme = ch.Scheme
	n.Memory = ch.Memory
	n.MemoryOverallocate = ch.MemoryOverallocate
	n.Disk = ch.Disk
	n.DiskOverallocate = ch.DiskOverallocate
	n.DaemonSFTP = ch.DaemonSFTP
	n.DaemonListen = ch.DaemonListen

	if ch.Description != nil {
		n.Description = *ch.Description
	}
	if ch.Public != nil {
		n.Public = *ch.Public
	}
	if ch.BehindProxy != nil {
		n.BehindProxy = *ch.BehindProxy
	}
	if ch.MaintenanceMode != nil {
		n.MaintenanceMode = *ch.MaintenanceMode
	}
	if ch.UploadSize != nil {
		n.UploadSize = *ch.UploadSize
	}
	if ch.DaemonBase != nil {
		n.DaemonBase = *ch.DaemonBase
	}

	if err := n.Validate(); err != nil {
		return Settings{}, err
	}
	return n, nil
}

var (
	// ErrNodeHasServers and the two errors below carry the reference's texts.
	ErrNodeHasServers   = errors.New("A node must have no servers linked to it in order to be deleted.")                                                         //nolint:staticcheck // the reference's text
	ErrLocationHasNodes = errors.New("Cannot delete a location that has active nodes attached to it.")                                                           //nolint:staticcheck // the reference's text
	ErrAllocationInUse  = errors.New("A server is currently assigned to this allocation. An allocation can only be deleted if no server is currently assigned.") //nolint:staticcheck // the reference's text
)

// CheckCanDeleteNode refuses a node that still has servers.
func CheckCanDeleteNode(servers int64) error {
	if servers > 0 {
		return ErrNodeHasServers
	}
	return nil
}

// CheckCanDeleteLocation refuses a location that still has nodes.
func CheckCanDeleteLocation(nodes int64) error {
	if nodes > 0 {
		return ErrLocationHasNodes
	}
	return nil
}

// CheckCanDeleteAllocationOfNode refuses an allocation a server is using. serverID is the allocation's server, nil
// when it is free. It is the admin's check; the one that guards a user's own allocations is CheckCanDeleteAllocation.
func CheckCanDeleteAllocationOfNode(serverID *uint64) error {
	if serverID != nil {
		return ErrAllocationInUse
	}
	return nil
}

// Location is the pair of names of a location.
type Location struct{ Short, Long string }

// ChangeLocation returns a location after PATCH /api/application/locations/{id}: the short name is needed every
// time, and the long one keeps its value when nil and is cleared by a pointer to "".
func ChangeLocation(cur Location, short string, long *string) (Location, error) {
	next := Location{Short: short, Long: cur.Long}
	if long != nil {
		next.Long = *long
	}
	if err := ValidateLocation(next.Short, next.Long); err != nil {
		return Location{}, err
	}
	return next, nil
}
