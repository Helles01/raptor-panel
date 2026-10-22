package auth

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxAllowedIPs is how many addresses or ranges one key may list.
	MaxAllowedIPs = 50
	// MaxKeyDescriptionLength is the longest description a key may have.
	MaxKeyDescriptionLength = 500
	// MaxAccountKeys is how many account API keys one user may have.
	MaxAccountKeys = 25

	// LastUsedGranularity is how often the time of the last use of a key is written; a write per request would make every API
	// call a database write.
	LastUsedGranularity = time.Minute
)

// ErrTooManyKeys is returned when a user already has MaxAccountKeys keys.
var ErrTooManyKeys = errors.New("You have reached the account limit for number of API keys.") //nolint:staticcheck // the reference's text

// CheckCanCreateKey applies the limit of keys per account; count is how many the user has, counted while the rows are locked
// so that two requests cannot both pass.
func CheckCanCreateKey(count int) error {
	if count >= MaxAccountKeys {
		return ErrTooManyKeys
	}
	return nil
}

// ValidateKeyDescription checks the description of a key: required, at most 500 characters, no control characters.
func ValidateKeyDescription(s string) error {
	trimmed := strings.TrimSpace(s)
	switch {
	case trimmed == "":
		return errors.New("The description field is required.")
	case utf8.RuneCountInString(s) > MaxKeyDescriptionLength:
		return fmt.Errorf("The description may not be greater than %d characters.", MaxKeyDescriptionLength)
	case !utf8.ValidString(s) || strings.ContainsFunc(s, func(r rune) bool { return r < ' ' || r == 0x7f }):
		return errors.New("The description must not contain control characters.")
	}
	return nil
}

// ShouldTouchLastUsed says whether the time of last use should be written now.
func ShouldTouchLastUsed(last *time.Time, now time.Time) bool {
	return last == nil || now.Sub(*last) >= LastUsedGranularity || now.Before(*last)
}

// AllowedIPs is the list of addresses a key may be used from. An empty list restricts nothing; any other list restricts to what is
// on it, even if none of its entries could be read.
type AllowedIPs struct {
	prefixes   []netip.Prefix
	ranges     [][2]netip.Addr
	restricted bool
}

// Restricted reports whether the key is limited to certain addresses.
func (a AllowedIPs) Restricted() bool { return a.restricted }

// ParseAllowedIPs checks the list for a new key. An entry is one of: an address ("203.0.113.9", "2001:db8::1"), a network
// ("203.0.113.0/24"), a range of two addresses of one family ("203.0.113.1-203.0.113.20"), or an IPv4 address with trailing
// wildcards ("203.0.113.*"). At most 50 entries. The first entry that cannot be read is the error.
func ParseAllowedIPs(entries []string) (AllowedIPs, error) {
	if len(entries) > MaxAllowedIPs {
		return AllowedIPs{}, fmt.Errorf("The allowed ips may not have more than %d items.", MaxAllowedIPs)
	}
	var a AllowedIPs
	a.restricted = len(entries) > 0
	for _, e := range entries {
		p, lo, hi, err := parseEntry(e)
		if err != nil {
			return AllowedIPs{}, err
		}
		a.add(p, lo, hi)
	}
	return a, nil
}

func (a *AllowedIPs) add(p netip.Prefix, lo, hi netip.Addr) {
	if p.IsValid() {
		a.prefixes = append(a.prefixes, p)
		return
	}
	a.ranges = append(a.ranges, [2]netip.Addr{lo, hi})
}

// LoadAllowedIPs reads the list stored with a key. An entry that cannot be read matches nothing and is counted in bad. A list
// of nothing but such entries therefore blocks every address; it does not become an empty list that allows them all. (The
// reference fails every request made with such a key.)
func LoadAllowedIPs(entries []string) (a AllowedIPs, bad int) {
	a.restricted = len(entries) > 0
	for _, e := range entries {
		p, lo, hi, err := parseEntry(e)
		if err != nil {
			bad++
			continue
		}
		a.add(p, lo, hi)
	}
	return a, bad
}

// Allows reports whether a key with this list may be used from the address.
func (a AllowedIPs) Allows(ip netip.Addr) bool {
	if !a.restricted {
		return true
	}
	if !ip.IsValid() {
		return false
	}
	ip = ip.Unmap().WithZone("")

	for _, p := range a.prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	for _, r := range a.ranges {
		if r[0].Compare(ip) <= 0 && ip.Compare(r[1]) <= 0 {
			return true
		}
	}
	return false
}

// DeniedMessage is what a client is told when its address is not allowed.
func DeniedMessage(ip netip.Addr) string {
	return "This IP address (" + ip.String() + ") does not have permission to access the API using these credentials."
}

func shown(s string) string {
	if utf8.RuneCountInString(s) > 40 {
		return string([]rune(s)[:40]) + "..."
	}
	return s
}

// parseEntry reads one entry as a network or as an inclusive range of addresses.
func parseEntry(e string) (netip.Prefix, netip.Addr, netip.Addr, error) {
	bad := func() (netip.Prefix, netip.Addr, netip.Addr, error) {
		return netip.Prefix{}, netip.Addr{}, netip.Addr{}, fmt.Errorf("The allowed ips entry %q is not an address, a network, a range or a wildcard.", shown(e))
	}
	e = strings.TrimSpace(e)
	if e == "" {
		return bad()
	}

	switch {
	case strings.Contains(e, "/"):
		p, err := netip.ParsePrefix(e)
		if err != nil || p.Addr().Zone() != "" {
			return bad()
		}
		return normalize(p), netip.Addr{}, netip.Addr{}, nil

	case strings.Contains(e, "-"):
		from, to, _ := strings.Cut(e, "-")
		lo, err1 := netip.ParseAddr(strings.TrimSpace(from))
		hi, err2 := netip.ParseAddr(strings.TrimSpace(to))
		if err1 != nil || err2 != nil || lo.Zone() != "" || hi.Zone() != "" {
			return bad()
		}
		lo, hi = lo.Unmap(), hi.Unmap()
		if lo.Is4() != hi.Is4() || lo.Compare(hi) > 0 {
			return bad()
		}
		return netip.Prefix{}, lo, hi, nil

	case strings.Contains(e, "*"):
		p, ok := wildcard(e)
		if !ok {
			return bad()
		}
		return p, netip.Addr{}, netip.Addr{}, nil
	}

	a, err := netip.ParseAddr(e)
	if err != nil || a.Zone() != "" {
		return bad()
	}
	a = a.Unmap()
	return netip.Prefix{}, a, a, nil
}

// normalize turns an IPv4-mapped network into the IPv4 network, so that it can match the address of a client.
func normalize(p netip.Prefix) netip.Prefix {
	if a := p.Addr(); a.Is4In6() && p.Bits() >= 96 {
		q, err := a.Unmap().Prefix(p.Bits() - 96)
		if err == nil {
			return q
		}
	}
	return p.Masked()
}

// wildcard reads "203.0.113.*" and the like: four octets, of which the last ones may be *, and none after a * is a number.
func wildcard(e string) (netip.Prefix, bool) {
	parts := strings.Split(e, ".")
	if len(parts) != 4 {
		return netip.Prefix{}, false
	}
	var octets [4]byte
	fixed := 0
	for i, p := range parts {
		if p == "*" {
			continue
		}
		if fixed != i {
			return netip.Prefix{}, false // a number after a *
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 || len(p) > 3 {
			return netip.Prefix{}, false
		}
		octets[i] = byte(n)
		fixed++
	}
	p, err := netip.AddrFrom4(octets).Prefix(fixed * 8)
	return p, err == nil
}
