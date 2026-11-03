package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

	"github.com/Helles01/raptor-panel/internal/api/allocapi"
	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/clientapi"
	"github.com/Helles01/raptor-panel/internal/db/dbq"
)

// ClientAllocations is the allocation store of the client API: what the owner of a server does with its allocations.
func (d *DB) ClientAllocations() allocapi.Store { return clientAllocations{d} }

type clientAllocations struct{ *DB }

// fits says whether every id is one the database can hold; an id out of range would otherwise be cut to another row's.
func fits(ids ...uint64) bool {
	for _, id := range ids {
		if id > math.MaxUint32 {
			return false
		}
	}
	return true
}

func clientAllocation(a appkit.Allocation) clientapi.Allocation {
	return clientapi.Allocation{ID: a.ID, IP: a.IP, IPAlias: a.Alias, Port: a.Port, Notes: a.Notes}
}

func (s clientAllocations) List(ctx context.Context, serverID uint64) ([]clientapi.Allocation, uint64, error) {
	if !fits(serverID) {
		return nil, 0, allocapi.ErrNotFound
	}
	info, err := s.q.ServerAllocationInfo(ctx, uint32(serverID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, allocapi.ErrNotFound
	} else if err != nil {
		return nil, 0, fmt.Errorf("read server %d: %w", serverID, err)
	}
	list, err := all(ctx, s.DB, scanAllocation, allocationSelect+" WHERE `allocations`.`server_id` = ? ORDER BY `allocations`.`id`", serverID)
	if err != nil {
		return nil, 0, fmt.Errorf("read the allocations of server %d: %w", serverID, err)
	}
	out := make([]clientapi.Allocation, len(list))
	for i, a := range list {
		out[i] = clientAllocation(a)
	}
	return out, uint64(info.AllocationID), nil
}

func (s clientAllocations) Limit(ctx context.Context, serverID uint64) (*int64, uint64, error) {
	if !fits(serverID) {
		return nil, 0, allocapi.ErrNotFound
	}
	info, err := s.q.ServerAllocationInfo(ctx, uint32(serverID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, allocapi.ErrNotFound
	} else if err != nil {
		return nil, 0, fmt.Errorf("read server %d: %w", serverID, err)
	}
	var limit *int64
	if info.AllocationLimit.Valid {
		v := int64(info.AllocationLimit.Int32)
		limit = &v
	}
	return limit, uint64(info.AllocationID), nil
}

func (s clientAllocations) Find(ctx context.Context, serverID, allocationID uint64) (clientapi.Allocation, error) {
	a, err := one(ctx, s.DB, scanAllocation, allocationSelect+" WHERE `allocations`.`id` = ? AND `allocations`.`server_id` = ?", allocationID, serverID)
	if errors.Is(err, appkit.ErrNotFound) {
		return clientapi.Allocation{}, allocapi.ErrNotFound
	}
	return clientAllocation(a), err
}

// Assign gives the server a free allocation on its address, picked at random, or makes a new one with the port the request chooses. The server's row is
// locked first, so that the limit is checked against what the server has when it gets the allocation. The free ones are read with a lock that covers the node's
// allocations at the address, so requests for one address take turns: two cannot take the same free allocation, nor choose the same new port.
func (s clientAllocations) Assign(ctx context.Context, r allocapi.AssignRequest) (clientapi.Allocation, error) {
	if !fits(r.ServerID) {
		return clientapi.Allocation{}, allocapi.ErrNotFound
	}
	var id uint64
	err := appUsers{s.DB}.retryDeadlock(func() error {
		return s.inTx(ctx, func(q *dbq.Queries) (err error) { id, err = s.assign(ctx, q, r); return })
	})
	if err != nil {
		return clientapi.Allocation{}, err
	}
	return s.Find(ctx, r.ServerID, id)
}

func (s clientAllocations) assign(ctx context.Context, q *dbq.Queries, r allocapi.AssignRequest) (uint64, error) {
	info, err := q.LockServerAllocationInfo(ctx, uint32(r.ServerID))
	if errors.Is(err, sql.ErrNoRows) {
		return 0, allocapi.ErrNotFound
	} else if err != nil {
		return 0, fmt.Errorf("lock server %d: %w", r.ServerID, err)
	}
	owner := sql.NullInt32{Int32: int32(r.ServerID), Valid: true}
	count, err := q.CountAllocationsOfServer(ctx, owner)
	if err != nil {
		return 0, fmt.Errorf("count the allocations of server %d: %w", r.ServerID, err)
	}
	var limit *int64
	if info.AllocationLimit.Valid {
		v := int64(info.AllocationLimit.Int32)
		limit = &v
	}
	if err := r.Check(limit, count); err != nil {
		return 0, err
	}
	ip, err := q.AllocationIP(ctx, info.AllocationID)
	if err != nil {
		return 0, fmt.Errorf("read the primary allocation of server %d: %w", r.ServerID, err)
	}
	free, err := q.PickFreeAllocation(ctx, dbq.PickFreeAllocationParams{NodeID: info.NodeID, Ip: ip})
	switch {
	case err == nil:
		n, err := q.AssignOneAllocation(ctx, dbq.AssignOneAllocationParams{ServerID: owner, ID: free})
		if err != nil {
			return 0, fmt.Errorf("assign allocation %d: %w", free, err)
		}
		if n != 1 {
			return 0, fmt.Errorf("allocation %d was not free although it was locked", free)
		}
		return uint64(free), nil
	case !errors.Is(err, sql.ErrNoRows):
		return 0, fmt.Errorf("look for a free allocation: %w", err)
	}

	ports, err := q.PortsOfNodeAddress(ctx, dbq.PortsOfNodeAddressParams{NodeID: info.NodeID, Ip: ip})
	if err != nil {
		return 0, fmt.Errorf("read the ports of node %d: %w", info.NodeID, err)
	}
	used := make([]int, len(ports))
	for i, p := range ports {
		used[i] = int(p)
	}
	port, err := r.Port(used)
	if err != nil {
		return 0, err
	}
	now := sql.NullTime{Time: s.now().UTC(), Valid: true}
	made, err := q.InsertAllocationFor(ctx, dbq.InsertAllocationForParams{NodeID: info.NodeID, Ip: ip, Port: uint32(port), ServerID: owner, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return 0, fmt.Errorf("make the allocation: %w", err)
	}
	return uint64(made), nil
}

// SetNotes stores the note of one of the server's allocations; an empty one is no note, as the reference turns empty text into null.
func (s clientAllocations) SetNotes(ctx context.Context, serverID, allocationID uint64, notes *string) error {
	if !fits(serverID, allocationID) {
		return allocapi.ErrNotFound
	}
	return s.inTx(ctx, func(q *dbq.Queries) error {
		if err := s.lockOwn(ctx, q, serverID, allocationID); err != nil {
			return err
		}
		return q.SetAllocationNotes(ctx, dbq.SetAllocationNotesParams{Notes: nullable(derefString(notes)), ID: uint32(allocationID)})
	})
}

// lockOwn locks the allocation if it is the server's.
func (s clientAllocations) lockOwn(ctx context.Context, q *dbq.Queries, serverID, allocationID uint64) error {
	_, err := q.LockAllocationOfServer(ctx, dbq.LockAllocationOfServerParams{ID: uint32(allocationID), ServerID: sql.NullInt32{Int32: int32(serverID), Valid: true}})
	if errors.Is(err, sql.ErrNoRows) {
		return allocapi.ErrNotFound
	} else if err != nil {
		return fmt.Errorf("lock allocation %d: %w", allocationID, err)
	}
	return nil
}

// SetPrimary makes one of the server's allocations its primary one. The server is locked before the allocation (as in Release), so that an allocation
// is never given up and made primary at the same time.
func (s clientAllocations) SetPrimary(ctx context.Context, serverID, allocationID uint64) error {
	if !fits(serverID, allocationID) {
		return allocapi.ErrNotFound
	}
	return s.inTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.LockServerAllocationInfo(ctx, uint32(serverID)); errors.Is(err, sql.ErrNoRows) {
			return allocapi.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock server %d: %w", serverID, err)
		}
		if err := s.lockOwn(ctx, q, serverID, allocationID); err != nil {
			return err
		}
		return q.SetServerPrimaryAllocation(ctx, dbq.SetServerPrimaryAllocationParams{AllocationID: uint32(allocationID), UpdatedAt: sql.NullTime{Time: s.now().UTC(), Valid: true}, ID: uint32(serverID)})
	})
}

// Release gives the allocation up if it is the server's and not its primary one, with the server locked so that the primary one is the one that is read.
func (s clientAllocations) Release(ctx context.Context, serverID, allocationID uint64) (bool, error) {
	if !fits(serverID, allocationID) {
		return false, nil
	}
	released := false
	err := s.inTx(ctx, func(q *dbq.Queries) error {
		info, err := q.LockServerAllocationInfo(ctx, uint32(serverID))
		if errors.Is(err, sql.ErrNoRows) {
			return allocapi.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock server %d: %w", serverID, err)
		}
		if err := s.lockOwn(ctx, q, serverID, allocationID); errors.Is(err, allocapi.ErrNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		if uint64(info.AllocationID) == allocationID {
			return nil
		}
		err = q.ReleaseAllocationsOfServer(ctx, dbq.ReleaseAllocationsOfServerParams{Ids: []uint32{uint32(allocationID)}, ServerID: sql.NullInt32{Int32: int32(serverID), Valid: true}})
		if err != nil {
			return fmt.Errorf("release allocation %d: %w", allocationID, err)
		}
		released = true
		return nil
	})
	return released, err
}
