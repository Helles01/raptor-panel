package nodes

import (
	"errors"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Helles01/raptor-panel/internal/ids"
)

const (
	// PortFloor is the port that allocations must be above; PortCeil the highest one.
	PortFloor = 1024
	PortCeil  = 65535
	// PortRangeLimit is the most ports one range may hold.
	PortRangeLimit = 1000
	// CIDRMinPrefix is the shortest prefix of a network that may be expanded: /25, 128 addresses.
	CIDRMinPrefix = 25
	// MaxAllocationsPerRequest bounds addresses times ports in one request. The reference has no bound: 128 addresses with several
	// ranges of 1000 ports make hundreds of thousands of rows in one transaction.
	MaxAllocationsPerRequest = 20000

	maxAliasLength = 191
	// MaxNotesLength is the longest note on an allocation.
	MaxNotesLength = 191
)

// The refusals, with the reference's texts.
var (
	ErrCIDR         = errors.New("CIDR notation only allows masks between /25 and /32.")                              //nolint:staticcheck // the reference's text
	ErrPortRange    = errors.New("Ports in an allocation must be greater than 1024 and less than or equal to 65535.") //nolint:staticcheck // the reference's text
	ErrTooManyPorts = errors.New("Adding more than 1000 ports in a single range at once is not supported.")           //nolint:staticcheck // the reference's text
	ErrNoSpace      = errors.New("Cannot assign additional allocation: no more space available on node.")             //nolint:staticcheck // the reference's text
	ErrAutoDisabled = errors.New("Server auto-allocation is not enabled for this instance.")                          //nolint:staticcheck // the reference's text
	ErrAllocLimit   = errors.New("Cannot assign additional allocations to this server: limit has been reached.")      //nolint:staticcheck // the reference's text
	ErrNoLimitSet   = errors.New("You cannot delete allocations for this server: no allocation limit is set.")        //nolint:staticcheck // the reference's text
	ErrPrimary      = errors.New("You cannot delete the primary allocation for this server.")                         //nolint:staticcheck // the reference's text
	ErrNotAnIP      = errors.New("The allocation IP must be an IPv4 address, an IPv6 address, or an IPv4 network such as 192.0.2.0/25.")
	ErrTooMany      = fmt.Errorf("A request may create at most %d allocations.", MaxAllocationsPerRequest)
)

// InvalidMappingError is returned for a port entry that is neither a port nor a range.
type InvalidMappingError struct{ Port string }

func (e *InvalidMappingError) Error() string {
	port := e.Port
	if utf8.RuneCountInString(port) > 40 {
		port = string([]rune(port)[:40]) + "..."
	}
	return "The mapping provided for " + port + " was invalid and could not be processed." //nolint:staticcheck // the reference's text
}

var (
	singlePort   = regexp.MustCompile(`^[0-9]{1,9}$`)
	portRangeRe  = regexp.MustCompile(`^([0-9]{4,5})-([0-9]{4,5})$`)
	aliasPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,191}$`)
)

// NewAllocations is a request to add allocations to a node: an address or an IPv4 network, the ports and ranges of ports ("25565",
// "25570-25580") for each address, and an optional alias that is shown instead of the address.
type NewAllocations struct {
	IP    string
	Ports []string
	Alias string
}

// Allocation is one address and port of a node.
type Allocation struct {
	IP    string
	Port  int
	Alias string
}

// ExpandAllocations turns a request into the allocations to insert (ones that exist already are skipped by the insert).
//
// Unlike the reference it takes only addresses, not host names: the panel does not look anything up in the DNS for an
// administrator's request, and the alias is where a name goes (C-041). An IPv4 network must have a prefix from /25 to /32 and is
// expanded to all its addresses, the network and broadcast ones included, as in the reference; an IPv6 address is taken as it is.
// A range written backwards is read forwards, and the checks of the lowest and highest port apply to the ends of the range (the
// reference checks them in the order written, so "1030-1020" gets past the floor).
func ExpandAllocations(in NewAllocations) ([]Allocation, error) {
	addrs, err := expandAddresses(in.IP)
	if err != nil {
		return nil, err
	}
	if err := ValidateAlias(in.Alias); err != nil {
		return nil, err
	}

	var ports []int
	seen := map[int]bool{}
	for _, spec := range in.Ports {
		lo, hi, err := ParsePorts(spec)
		if err != nil {
			return nil, err
		}
		for p := lo; p <= hi; p++ {
			if !seen[p] {
				seen[p] = true
				ports = append(ports, p)
			}
		}
	}
	if len(ports) == 0 {
		return nil, errors.New("At least one port is required.")
	}
	if len(addrs)*len(ports) > MaxAllocationsPerRequest {
		return nil, ErrTooMany
	}

	out := make([]Allocation, 0, len(addrs)*len(ports))
	for _, a := range addrs {
		for _, p := range ports {
			out = append(out, Allocation{IP: a, Port: p, Alias: in.Alias})
		}
	}
	return out, nil
}

func expandAddresses(spec string) ([]string, error) {
	spec = strings.TrimSpace(spec)
	if base, bits, ok := strings.Cut(spec, "/"); ok {
		n, err := strconv.Atoi(bits)
		if bits == "" || err != nil || strings.ContainsAny(bits, "+-") || n < CIDRMinPrefix || n > 32 {
			return nil, ErrCIDR
		}
		a, err := netip.ParseAddr(base)
		if err != nil || !a.Is4() {
			return nil, ErrCIDR
		}
		prefix, _ := a.Prefix(n)
		var out []string
		for ip := prefix.Addr(); prefix.Contains(ip); ip = ip.Next() {
			out = append(out, ip.String())
		}
		return out, nil
	}

	a, err := netip.ParseAddr(spec)
	if err != nil || a.Zone() != "" {
		return nil, ErrNotAnIP
	}
	return []string{a.Unmap().String()}, nil
}

// ParsePorts reads one entry of a port list: a port, or a range of two ports (written forwards or backwards), checked against the limits of
// an allocation. lo and hi are the ends, lo first.
func ParsePorts(spec string) (lo, hi int, err error) {
	if m := portRangeRe.FindStringSubmatch(spec); m != nil {
		lo, _ = strconv.Atoi(m[1])
		hi, _ = strconv.Atoi(m[2])
		if lo > hi {
			lo, hi = hi, lo
		}
		if hi-lo+1 > PortRangeLimit {
			return 0, 0, ErrTooManyPorts
		}
	} else if singlePort.MatchString(spec) {
		lo, _ = strconv.Atoi(spec)
		hi = lo
	} else {
		return 0, 0, &InvalidMappingError{Port: spec}
	}

	if lo <= PortFloor || hi > PortCeil {
		return 0, 0, ErrPortRange
	}
	return lo, hi, nil
}

// ValidateAlias checks the alias of an allocation: empty for none, otherwise a host name or address.
func ValidateAlias(alias string) error {
	if alias != "" && !aliasPattern.MatchString(alias) {
		return errors.New("The alias must be at most 191 letters, digits, dots, dashes, underscores or colons.")
	}
	return nil
}

// ValidateNotes checks the note a user writes on an allocation: empty for none, otherwise text without control characters.
func ValidateNotes(notes string) error {
	if utf8.RuneCountInString(notes) > MaxNotesLength {
		return fmt.Errorf("The notes may not be greater than %d characters.", MaxNotesLength)
	}
	if !utf8.ValidString(notes) || strings.ContainsFunc(notes, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return errors.New("The notes must not contain control characters.")
	}
	return nil
}

// CheckCanAddAllocation applies the checks before a user adds an allocation to their server. limit is the server's allocation limit;
// none (null) and 0 both mean no allocations, as in the reference. count is how many it has, counted while the rows are locked.
func CheckCanAddAllocation(featureEnabled bool, limit *int64, count int64) error {
	if !featureEnabled {
		return ErrAutoDisabled
	}
	if limit == nil || count >= *limit {
		return ErrAllocLimit
	}
	return nil
}

// CheckCanDeleteAllocation applies the checks before a user removes an allocation from their server. The allocation must belong to the
// server: whoever resolves it from the request must look it up among the server's own.
func CheckCanDeleteAllocation(limit *int64, allocationID, primaryID uint64) error {
	if limit == nil || *limit == 0 {
		return ErrNoLimitSet
	}
	if allocationID == primaryID {
		return ErrPrimary
	}
	return nil
}

// PickPort chooses a port from start to end that is not in used, at random. It is how an allocation is made when a server has to
// get one and none is free: the range is the one the administrator set for it.
func PickPort(start, end int, used []int, r io.Reader) (int, error) {
	if start < 1 || end > PortCeil || start > end {
		return 0, ErrNoSpace
	}
	taken := make(map[int]bool, len(used))
	for _, p := range used {
		taken[p] = true
	}

	free := make([]int, 0, end-start+1)
	for p := start; p <= end; p++ {
		if !taken[p] {
			free = append(free, p)
		}
	}
	if len(free) == 0 {
		return 0, ErrNoSpace
	}
	i, err := ids.RandomBelow(r, len(free))
	if err != nil {
		return 0, err
	}
	return free[i], nil
}
