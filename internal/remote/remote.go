// Package remote holds the rules of the API a node calls: which servers a node may reach, how a transfer between two nodes ends,
// how the list of a node's servers is paged, and what a restart of a node resets. It touches no database.
package remote

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/Helles01/raptor-panel/internal/servers"
)

var (
	// ErrForbidden is answered with 403.
	ErrForbidden = errors.New("Requesting node does not have permission to access this server.") //nolint:staticcheck // the reference's text
	// ErrNotTransferring is answered with 409.
	ErrNotTransferring = errors.New("Server is not being transferred.") //nolint:staticcheck // the reference's text
)

// Scope is the node that is calling. Every endpoint of the node API works on the servers of this node only, so the lookup of a
// server for a node must never find another node's server in the first place; these checks are what that lookup is held to.
type Scope struct{ NodeID uint64 }

// Owns checks that a server is on the calling node: for installation, backups, activity and SFTP.
func (s Scope) Owns(serverNodeID uint64) error {
	if s.NodeID == 0 || serverNodeID != s.NodeID {
		return ErrForbidden
	}
	return nil
}

// CanReadDetails checks the node may read a server's configuration. While the server is being transferred both nodes may, the
// old one to keep it running and the new one to prepare it; otherwise only the node it is on.
func (s Scope) CanReadDetails(serverNodeID uint64, t *Transfer) error {
	if s.NodeID == 0 {
		return ErrForbidden
	}
	if t != nil {
		if s.NodeID == t.OldNode || s.NodeID == t.NewNode {
			return nil
		}
		return ErrForbidden
	}
	return s.Owns(serverNodeID)
}

// Transfer is a server moving from one node to another, with the allocations on each side.
type Transfer struct {
	OldNode, NewNode             uint64
	OldAllocation, NewAllocation uint64
	OldAdditional, NewAdditional []uint64
}

// Validate checks a transfer before it is stored: two different nodes, and allocations that are all different. If an
// allocation were on both sides, the end of the transfer would free the one the server is left with.
func (t Transfer) Validate() error {
	if t.OldNode == 0 || t.NewNode == 0 || t.OldNode == t.NewNode {
		return errors.New("a transfer needs two different nodes")
	}
	if t.OldAllocation == 0 || t.NewAllocation == 0 {
		return errors.New("a transfer needs a primary allocation on each node")
	}

	all := append([]uint64{t.OldAllocation, t.NewAllocation}, t.OldAdditional...)
	all = append(all, t.NewAdditional...)

	seen := map[uint64]bool{}
	for _, id := range all {
		if id == 0 || seen[id] {
			return fmt.Errorf("allocation %d is missing or used twice in the transfer", id)
		}
		seen[id] = true
	}
	return nil
}

// TransferResult is what the end of a transfer does to the database.
type TransferResult struct {
	Successful bool
	// Release lists the allocations to detach from the server.
	Release []uint64
	// Move is set when the server changes node: the new node and primary allocation.
	Move              bool
	NewNode           uint64
	NewAllocation     uint64
	DeleteFromOldNode uint64
}

func ids(primary uint64, additional []uint64) []uint64 {
	return append([]uint64{primary}, additional...)
}

// Fail ends a transfer that failed. Either node may report it. The allocations of the new node are released.
func (t *Transfer) Fail(s Scope) (TransferResult, error) {
	if t == nil {
		return TransferResult{}, ErrNotTransferring
	}
	if s.NodeID == 0 || (s.NodeID != t.NewNode && s.NodeID != t.OldNode) {
		return TransferResult{}, ErrForbidden
	}
	return TransferResult{Release: ids(t.NewAllocation, t.NewAdditional)}, nil
}

// Succeed ends a transfer that worked. Only the new node may report it: it is the one that holds the files. The old
// allocations are released, the server moves, and the old node is told to delete its copy.
func (t *Transfer) Succeed(s Scope) (TransferResult, error) {
	if t == nil {
		return TransferResult{}, ErrNotTransferring
	}
	if s.NodeID == 0 || s.NodeID != t.NewNode {
		return TransferResult{}, ErrForbidden
	}
	return TransferResult{
		Successful: true, Release: ids(t.OldAllocation, t.OldAdditional),
		Move: true, NewNode: t.NewNode, NewAllocation: t.NewAllocation, DeleteFromOldNode: t.OldNode,
	}, nil
}

const (
	defaultPerPage = 50
	// MaxPerPage bounds how many servers a node may ask for at once. The reference takes any number.
	MaxPerPage = 500
)

// ParsePaging reads the page and per_page of a request. Anything that is not a whole number in range means the default.
func ParsePaging(page, perPage string) (int, int) {
	p, err := strconv.Atoi(page)
	if err != nil || p < 1 {
		p = 1
	}
	n, err := strconv.Atoi(perPage)
	if err != nil || n < 1 {
		n = defaultPerPage
	}
	return p, min(n, MaxPerPage)
}

// ListMeta is the "meta" of the list of servers, in the form of Laravel's paginator, which the node reads.
type ListMeta struct {
	CurrentPage int  `json:"current_page"`
	From        *int `json:"from"`
	LastPage    int  `json:"last_page"`
	PerPage     int  `json:"per_page"`
	To          *int `json:"to"`
	Total       int  `json:"total"`
}

// Window returns the rows to read for a page, and the meta to answer with.
func Window(total, page, perPage int) (offset int, meta ListMeta) {
	total = max(total, 0)
	last := max((total+perPage-1)/perPage, 1)
	offset = (page - 1) * perPage

	meta = ListMeta{CurrentPage: page, LastPage: last, PerPage: perPage, Total: total}
	if offset < total {
		from, to := offset+1, min(offset+perPage, total)
		meta.From, meta.To = &from, &to
	}
	return offset, meta
}

// ResetStatus is the status a server gets when its node restarts and asks the panel to reset the state of its servers. A backup
// restore that was running is over, and the server is normal again. An installation that was running did not finish: the
// server is marked as failed (a reinstallation if it had been installed before), where the reference makes it look normal
// (C-036). Nothing else changes; a suspended server stays suspended.
func ResetStatus(status string, installedBefore bool) (string, bool) {
	switch {
	case status == servers.StatusRestoringBackup:
		return "", true
	case status == servers.StatusInstalling && installedBefore:
		return servers.StatusReinstallFailed, true
	case status == servers.StatusInstalling:
		return servers.StatusInstallFailed, true
	}
	return status, false
}
