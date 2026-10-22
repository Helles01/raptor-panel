package servers

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Helles01/raptor-panel/internal/ids"
	"github.com/Helles01/raptor-panel/internal/nodes"
)

const (
	// MaxResource is the largest memory, disk, swap or CPU value: what the database column holds.
	MaxResource = 1<<31 - 1

	MaxNameLength        = 191
	MaxDescriptionLength = 2000
	MaxImageLength       = 191
	MaxStartupLength     = 8192
	MaxAdditionalAllocs  = 100
)

// FieldError is a refusal that belongs to one field of the request, so that the API can say which.
type FieldError struct{ Field, Message string }

func (e *FieldError) Error() string { return e.Field + ": " + e.Message }

func fieldErr(field, format string, a ...any) error {
	return &FieldError{Field: field, Message: fmt.Sprintf(format, a...)}
}

const maxCPUSetIndex = 4095

var (
	imagePattern  = regexp.MustCompile(`^~?[A-Za-z0-9_./:@-]{1,191}$`)
	cpusetPattern = regexp.MustCompile(`^[0-9]{1,4}(-[0-9]{1,4})?(,[0-9]{1,4}(-[0-9]{1,4})?)*$`)
)

// Limits are the resources a server may use. Memory, disk and swap are in MB, IO is the block IO weight, CPU is a percentage of one
// core (0 for no limit), Threads a CPU set such as "0-3,6" (nil for any).
type Limits struct {
	Memory, Swap, Disk, IO, CPU int64
	Threads                     *string
}

func between(field string, v, lo, hi int64) error {
	if v < lo || v > hi {
		return fieldErr(field, "must be between %d and %d", lo, hi)
	}
	return nil
}

// Validate checks every limit against what the reference allows (memory and disk from 0, swap from -1, IO from 10 to 1000, CPU from 0)
// and against what the database can hold. A threads value must be a real CPU set; the reference accepts any run of digits, dashes
// and commas, which the container engine then refuses when the server starts.
func (l Limits) Validate() error {
	for _, c := range []struct {
		field  string
		v      int64
		lo, hi int64
	}{
		{"limits.memory", l.Memory, 0, MaxResource}, {"limits.swap", l.Swap, -1, MaxResource}, {"limits.disk", l.Disk, 0, MaxResource},
		{"limits.io", l.IO, 10, 1000}, {"limits.cpu", l.CPU, 0, MaxResource},
	} {
		if err := between(c.field, c.v, c.lo, c.hi); err != nil {
			return err
		}
	}
	if l.Threads != nil {
		return ValidateThreads(*l.Threads)
	}
	return nil
}

// ValidateThreads checks a CPU set: numbers and ranges of numbers separated by commas, each range going up.
func ValidateThreads(s string) error {
	if !cpusetPattern.MatchString(s) {
		return fieldErr("limits.threads", "must be a CPU set such as 0-3,6")
	}
	for _, part := range strings.Split(s, ",") {
		lo, hi, isRange := strings.Cut(part, "-")
		a, _ := strconv.Atoi(lo)
		b := a
		if isRange {
			b, _ = strconv.Atoi(hi)
		}
		if a > b || b > maxCPUSetIndex {
			return fieldErr("limits.threads", "must be a CPU set such as 0-3,6")
		}
	}
	return nil
}

// LimitsUpdate is a change to the limits of a server: the fields that are set replace the current ones.
type LimitsUpdate struct {
	Memory, Swap, Disk, IO, CPU *int64
	Threads                     *string
	ClearThreads                bool
}

// Apply returns the limits after the change, checked as a whole.
func (u LimitsUpdate) Apply(current Limits) (Limits, error) {
	out := current
	for _, p := range []struct {
		to   *int64
		from *int64
	}{{&out.Memory, u.Memory}, {&out.Swap, u.Swap}, {&out.Disk, u.Disk}, {&out.IO, u.IO}, {&out.CPU, u.CPU}} {
		if p.from != nil {
			*p.to = *p.from
		}
	}
	switch {
	case u.ClearThreads:
		out.Threads = nil
	case u.Threads != nil:
		out.Threads = u.Threads
	}
	return out, out.Validate()
}

// FeatureLimits are the limits on what a user can add for a server: databases, extra allocations and backups. nil means none set.
type FeatureLimits struct {
	Databases, Allocations, Backups *int64
}

// Validate checks that each limit is unset or from 0 up to what the database holds.
func (f FeatureLimits) Validate() error {
	for _, c := range []struct {
		field string
		v     *int64
	}{{"feature_limits.databases", f.Databases}, {"feature_limits.allocations", f.Allocations}, {"feature_limits.backups", f.Backups}} {
		if c.v != nil {
			if err := between(c.field, *c.v, 0, MaxResource); err != nil {
				return err
			}
		}
	}
	return nil
}

// NewServer is what an administrator provides for a new server.
type NewServer struct {
	ExternalID  *string
	Name        string
	Description string
	OwnerID     uint64
	EggID       uint64
	Image       string
	Startup     string
	Limits      Limits
	Features    FeatureLimits
	// Allocation is the primary allocation, Additional the others.
	Allocation uint64
	Additional []uint64
}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < ' ' || r == 0x7f })
}

// Validate checks every field against simple allow-lists. The first problem is the error, a *FieldError.
func (n NewServer) Validate() error {
	name := strings.TrimSpace(n.Name)
	switch {
	case name == "" || utf8.RuneCountInString(name) > MaxNameLength:
		return fieldErr("name", "must be 1 to %d characters", MaxNameLength)
	case !utf8.ValidString(name) || hasControl(name):
		return fieldErr("name", "must not contain control characters")
	case utf8.RuneCountInString(n.Description) > MaxDescriptionLength || !utf8.ValidString(n.Description) || strings.ContainsRune(n.Description, 0):
		return fieldErr("description", "must be valid text of at most %d characters", MaxDescriptionLength)
	case n.OwnerID == 0:
		return fieldErr("user", "is required")
	case n.EggID == 0:
		return fieldErr("egg", "is required")
	case !imagePattern.MatchString(n.Image):
		return fieldErr("docker_image", "must be 1 to %d letters, digits and . _ / : @ -", MaxImageLength)
	case strings.TrimSpace(n.Startup) == "" || len(n.Startup) > MaxStartupLength || strings.ContainsRune(n.Startup, 0) || !utf8.ValidString(n.Startup):
		return fieldErr("startup", "must be a command of at most %d bytes", MaxStartupLength)
	}
	if n.ExternalID != nil {
		id := *n.ExternalID
		if l := utf8.RuneCountInString(id); l < 1 || l > 191 || !utf8.ValidString(id) || hasControl(id) {
			return fieldErr("external_id", "must be 1 to 191 characters without control characters")
		}
	}
	if err := n.Limits.Validate(); err != nil {
		return err
	}
	if err := n.Features.Validate(); err != nil {
		return err
	}
	if n.Allocation == 0 {
		return fieldErr("allocation.default", "is required")
	}
	if len(n.Additional) > MaxAdditionalAllocs {
		return fieldErr("allocation.additional", "may not have more than %d items", MaxAdditionalAllocs)
	}
	return nil
}

// AllocationRow is an allocation as stored: where it is, and which server holds it.
type AllocationRow struct {
	ID       uint64
	NodeID   uint64
	ServerID *uint64
}

// CheckNewServerAllocations checks the allocations a new server asks for: they must all exist, be on the server's node, be free, and
// be different. The reference only checks the primary one, and then takes them all by id, so an allocation of another server in the
// additional list is taken away from it. The store that follows must also make the assignment conditional (the rows must still be
// free and on the node when they are updated) and refuse the creation unless every one of them was updated.
func CheckNewServerAllocations(nodeID, primary uint64, additional []uint64, rows map[uint64]AllocationRow) error {
	seen := map[uint64]bool{}
	for _, id := range append([]uint64{primary}, additional...) {
		if id == 0 || seen[id] {
			return fieldErr("allocation", "allocation %d is missing or used twice", id)
		}
		seen[id] = true

		row, ok := rows[id]
		switch {
		case !ok:
			return fieldErr("allocation", "allocation %d does not exist", id)
		case row.NodeID != nodeID:
			return fieldErr("allocation", "allocation %d is not on the node of the server", id)
		case row.ServerID != nil:
			return fieldErr("allocation", "allocation %d is already in use", id)
		}
	}
	return nil
}

// AllocationPlan is what a change of a server's allocations does.
type AllocationPlan struct {
	// Assign and Release are the allocations to attach to the server and to detach from it (their notes are cleared too).
	Assign, Release []uint64
	// Default is the primary allocation afterwards.
	Default uint64
}

// ErrNoFallback is returned when the default allocation is removed and no new one is added to take its place.
var ErrNoFallback = errors.New("You are attempting to delete the default allocation for this server but there is no fallback allocation to use.") //nolint:staticcheck // the reference's text

// ErrDefaultNotAssigned is returned when the new default allocation does not belong to the server.
var ErrDefaultNotAssigned = errors.New("The requested default allocation is not currently assigned to this server.") //nolint:staticcheck // the reference's text

// PlanAllocationChange decides what happens when an administrator adds and removes allocations of a server, and perhaps names a new
// default (newDefault, 0 for no change). rows are the allocations involved. Added ones must be on the server's node and free;
// removed ones must be the server's. The reference quietly ignores the ones that are not; here that is an error, so that a request
// that does not do what it says is not answered as if it did. An allocation that is both added and removed stays as it is. Removing the
// default one needs a newly added allocation to take over, which is the lowest id among them.
func PlanAllocationChange(serverID, nodeID, currentDefault uint64, rows map[uint64]AllocationRow, add, remove []uint64, newDefault uint64) (AllocationPlan, error) {
	adding := map[uint64]bool{}
	var assign, release []uint64
	for _, id := range add {
		if adding[id] {
			continue
		}
		adding[id] = true
		row, ok := rows[id]
		if !ok || row.NodeID != nodeID || row.ServerID != nil {
			return AllocationPlan{}, fieldErr("add_allocations", "allocation %d cannot be added: it does not exist, is not on the node, or is in use", id)
		}
		assign = append(assign, id)
	}
	removing := map[uint64]bool{}
	for _, id := range remove {
		if adding[id] || removing[id] {
			continue
		}
		removing[id] = true
		row, ok := rows[id]
		if !ok || row.NodeID != nodeID || row.ServerID == nil || *row.ServerID != serverID {
			return AllocationPlan{}, fieldErr("remove_allocations", "allocation %d cannot be removed: it is not assigned to this server", id)
		}
		release = append(release, id)
	}
	sort.Slice(assign, func(i, j int) bool { return assign[i] < assign[j] })
	sort.Slice(release, func(i, j int) bool { return release[i] < release[j] })

	def := currentDefault
	if newDefault != 0 {
		def = newDefault
	}
	for _, id := range release {
		if id == def {
			if len(assign) == 0 {
				return AllocationPlan{}, ErrNoFallback
			}
			def = assign[0]
			break
		}
	}

	if def != currentDefault {
		released := map[uint64]bool{}
		for _, id := range release {
			released[id] = true
		}
		row, known := rows[def]
		mine := known && row.NodeID == nodeID && row.ServerID != nil && *row.ServerID == serverID && !released[def]
		if !mine && !adding[def] {
			return AllocationPlan{}, ErrDefaultNotAssigned
		}
	}
	return AllocationPlan{Assign: assign, Release: release, Default: def}, nil
}

// NodeUsage is a node with what its servers already use.
type NodeUsage struct {
	ID, LocationID             uint64
	Public                     bool
	Memory, MemoryOverallocate int64
	Disk, DiskOverallocate     int64
	UsedMemory, UsedDisk       int64
}

// ErrNoViableNode is returned when no node can take the server.
var ErrNoViableNode = errors.New("No nodes satisfying the requirements specified for automatic deployment could be found.") //nolint:staticcheck // the reference's text

func fits(used, want, capacity, overallocate int64) bool {
	// -1 switches the check off, as the node form says. The reference's query would read -1 as -1 %, a node with 99 % of its room.
	if overallocate == -1 {
		return true
	}
	// used + want must not exceed capacity plus its overallocation in per cent, compared without dividing.
	return (used+want)*100 <= capacity*(100+overallocate)
}

// Fits reports whether a server of this size can be added to the node without exceeding its memory and disk, overallocation included.
func (n NodeUsage) Fits(memory, disk int64) bool {
	return fits(n.UsedMemory, memory, n.Memory, n.MemoryOverallocate) && fits(n.UsedDisk, disk, n.Disk, n.DiskOverallocate)
}

// FindViableNodes lists the public nodes, in the locations asked for (all if none), that can take a server of this size.
func FindViableNodes(nodes []NodeUsage, locations []uint64, memory, disk int64) ([]NodeUsage, error) {
	inLocation := map[uint64]bool{}
	for _, l := range locations {
		inLocation[l] = true
	}

	var out []NodeUsage
	for _, n := range nodes {
		if !n.Public || (len(locations) > 0 && !inLocation[n.LocationID]) || !n.Fits(memory, disk) {
			continue
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, ErrNoViableNode
	}
	return out, nil
}

// PortSpec is a port or a range of ports, Lo to Hi.
type PortSpec struct{ Lo, Hi int }

// ParsePortSpecs reads the ports of an automatic deployment: each entry a port or a range of two ports. The reference skips an entry
// it cannot read; here it is an error. The limits of an allocation apply to the ports.
func ParsePortSpecs(specs []string) ([]PortSpec, error) {
	out := make([]PortSpec, 0, len(specs))
	for _, s := range specs {
		lo, hi, err := nodes.ParsePorts(s)
		if err != nil {
			return nil, err
		}
		out = append(out, PortSpec{Lo: lo, Hi: hi})
	}
	return out, nil
}

// Candidate is an allocation that automatic deployment may choose among: used or free.
type Candidate struct {
	ID, NodeID uint64
	IP         string
	Port       int
	Used       bool
}

// ErrNoViableAllocation is returned when no free allocation fits.
var ErrNoViableAllocation = errors.New("No allocations satisfying the requirements for automatic deployment were found.") //nolint:staticcheck // the reference's text

// SelectAllocation picks a free allocation at random from the candidates on the given nodes (any if none) and with one of the given
// ports (any if none). With dedicated set, an address on which any server already holds an allocation is not used, so that the server
// has the address to itself.
func SelectAllocation(all []Candidate, onNodes []uint64, ports []PortSpec, dedicated bool, r io.Reader) (Candidate, error) {
	nodeSet := map[uint64]bool{}
	for _, n := range onNodes {
		nodeSet[n] = true
	}
	type addr struct {
		node uint64
		ip   string
	}
	busy := map[addr]bool{}
	if dedicated {
		for _, c := range all {
			if c.Used {
				busy[addr{c.NodeID, c.IP}] = true
			}
		}
	}

	var pool []Candidate
	for _, c := range all {
		if c.Used || (len(nodeSet) > 0 && !nodeSet[c.NodeID]) || busy[addr{c.NodeID, c.IP}] {
			continue
		}
		if len(ports) > 0 && !inPorts(c.Port, ports) {
			continue
		}
		pool = append(pool, c)
	}
	if len(pool) == 0 {
		return Candidate{}, ErrNoViableAllocation
	}

	i, err := ids.RandomBelow(r, len(pool))
	if err != nil {
		return Candidate{}, err
	}
	return pool[i], nil
}

func inPorts(port int, specs []PortSpec) bool {
	for _, s := range specs {
		if port >= s.Lo && port <= s.Hi {
			return true
		}
	}
	return false
}
