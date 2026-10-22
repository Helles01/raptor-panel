package auth

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func ip(s string) netip.Addr { return netip.MustParseAddr(s) }

func TestAllowedIPsMatching(t *testing.T) {
	tests := []struct {
		name    string
		entries []string
		allowed []string
		denied  []string
	}{
		{"one address", []string{"203.0.113.9"}, []string{"203.0.113.9"}, []string{"203.0.113.10", "203.0.113.8", "2001:db8::1"}},
		{"one IPv6 address", []string{"2001:db8::1"}, []string{"2001:db8::1", "2001:0db8:0:0:0:0:0:1"}, []string{"2001:db8::2", "203.0.113.9"}},
		{"a network", []string{"203.0.113.0/24"}, []string{"203.0.113.0", "203.0.113.77", "203.0.113.255"}, []string{"203.0.112.255", "203.0.114.0"}},
		{"a network given by a host address", []string{"203.0.113.77/24"}, []string{"203.0.113.1", "203.0.113.200"}, []string{"203.0.114.1"}},
		{"a single-host network", []string{"203.0.113.9/32"}, []string{"203.0.113.9"}, []string{"203.0.113.10"}},
		{"an IPv6 network", []string{"2001:db8:1::/48"}, []string{"2001:db8:1::1", "2001:db8:1:ffff::1"}, []string{"2001:db8:2::1"}},
		{"a range", []string{"203.0.113.10-203.0.113.20"}, []string{"203.0.113.10", "203.0.113.15", "203.0.113.20"}, []string{"203.0.113.9", "203.0.113.21"}},
		{"a range with spaces", []string{"203.0.113.10 - 203.0.113.20"}, []string{"203.0.113.15"}, []string{"203.0.113.21"}},
		{"a range of one", []string{"203.0.113.10-203.0.113.10"}, []string{"203.0.113.10"}, []string{"203.0.113.11"}},
		{"an IPv6 range", []string{"2001:db8::1-2001:db8::ff"}, []string{"2001:db8::80"}, []string{"2001:db8::100", "0.0.0.5", "203.0.113.15"}},
		{"an IPv4 range does not match IPv6", []string{"0.0.0.1-255.255.255.254"}, []string{"203.0.113.15"}, []string{"::1", "2001:db8::15", "::5"}},
		{"a wildcard", []string{"203.0.113.*"}, []string{"203.0.113.0", "203.0.113.255"}, []string{"203.0.114.1"}},
		{"two wildcards", []string{"203.0.*.*"}, []string{"203.0.0.1", "203.0.255.255"}, []string{"203.1.0.1"}},
		{"three wildcards", []string{"203.*.*.*"}, []string{"203.255.1.1"}, []string{"204.0.0.1"}},
		{"all wildcards", []string{"*.*.*.*"}, []string{"1.2.3.4", "255.255.255.255"}, []string{"2001:db8::1"}},
		{"several entries", []string{"198.51.100.1", "203.0.113.0/24", "2001:db8::1"}, []string{"198.51.100.1", "203.0.113.5", "2001:db8::1"}, []string{"198.51.100.2", "2001:db8::2"}},
		{"IPv4 given as an IPv4-mapped address", []string{"::ffff:203.0.113.9"}, []string{"203.0.113.9"}, []string{"203.0.113.10"}},
		{"a client that is an IPv4-mapped address", []string{"203.0.113.9"}, []string{"::ffff:203.0.113.9"}, []string{"::ffff:203.0.113.10"}},
		{"a mapped network", []string{"::ffff:203.0.113.0/120"}, []string{"203.0.113.77"}, []string{"203.0.114.1"}},
		{"a client with a zone", []string{"fe80::1"}, []string{"fe80::1%eth0"}, []string{"fe80::2%eth0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			list, err := ParseAllowedIPs(tt.entries)
			if err != nil {
				t.Fatal(err)
			}
			if !list.Restricted() {
				t.Error("a list with entries must restrict")
			}
			for _, a := range tt.allowed {
				if !list.Allows(ip(a)) {
					t.Errorf("%s must be allowed", a)
				}
			}
			for _, d := range tt.denied {
				if list.Allows(ip(d)) {
					t.Errorf("%s must be denied", d)
				}
			}
		})
	}
}

func TestAnEmptyListRestrictsNothing(t *testing.T) {
	for _, entries := range [][]string{nil, {}} {
		list, err := ParseAllowedIPs(entries)
		if err != nil || list.Restricted() {
			t.Fatalf("an empty list: %+v, %v", list, err)
		}
		for _, a := range []string{"203.0.113.9", "2001:db8::1"} {
			if !list.Allows(ip(a)) {
				t.Errorf("%s was refused by an empty list", a)
			}
		}
	}
	// An address that cannot be read is allowed by an empty list only because nothing is restricted.
	if !(AllowedIPs{}).Allows(netip.Addr{}) {
		t.Error("a key without a restriction must work whatever the address")
	}
}

func TestARestrictedListRefusesAnAddressThatCannotBeRead(t *testing.T) {
	list, _ := ParseAllowedIPs([]string{"*.*.*.*"})
	if list.Allows(netip.Addr{}) {
		t.Error("an unreadable client address was allowed by a restricted list")
	}
}

func TestParseAllowedIPsRefuses(t *testing.T) {
	long := strings.Repeat("1", 101)
	for _, entry := range []string{
		"", "   ", "not an address", "203.0.113", "203.0.113.256", "203.0.113.9.9", "203.0.113.0/33", "203.0.113.0/-1", "203.0.113.0/", "/24",
		"2001:db8::/129", "203.0.113.10-203.0.113.5", "203.0.113.10-2001:db8::1", "203.0.113.10-", "-203.0.113.10", "203.0.113.1-203.0.113.9-203.0.113.20",
		"203.0.*.5", "*.0.0.0", "203.*", "203.0.113.**", "203.0.113.*.*.*", "2001:db8::*", "*",
		"203.0.113.9;", "203.0.113.\n9", "203.0.113. 9", "203.0.113.0/255.255.255.0", "fe80::1%eth0", "fe80::/64%eth0", "localhost", "example.com", long,
		"203.0.113.9, 198.51.100.1", "0x7f.0.0.1", "01.2.3.4",
	} {
		_, err := ParseAllowedIPs([]string{entry})
		if err == nil {
			t.Errorf("%q was accepted", entry)
		}
	}
}

func TestParseAllowedIPsIgnoresSurroundingSpace(t *testing.T) {
	for _, entry := range []string{" 203.0.113.9 ", "203.0.113.9\n", "\t203.0.113.9"} {
		list, err := ParseAllowedIPs([]string{entry})
		if err != nil || !list.Allows(ip("203.0.113.9")) {
			t.Errorf("%q: %v", entry, err)
		}
	}
}

func TestParseAllowedIPsLimit(t *testing.T) {
	entries := make([]string, MaxAllowedIPs)
	for i := range entries {
		entries[i] = "203.0.113.9"
	}
	if _, err := ParseAllowedIPs(entries); err != nil {
		t.Errorf("exactly %d entries: %v", MaxAllowedIPs, err)
	}
	if _, err := ParseAllowedIPs(append(entries, "203.0.113.9")); err == nil || !strings.Contains(err.Error(), "may not have more than 50 items") {
		t.Errorf("one more than the limit: %v", err)
	}
}

func TestErrorsNameTheEntryButNotAtLength(t *testing.T) {
	_, err := ParseAllowedIPs([]string{"203.0.113.9", "bogus"})
	if err == nil || !strings.Contains(err.Error(), `"bogus"`) {
		t.Errorf("error = %v, want it to name the entry", err)
	}
	_, err = ParseAllowedIPs([]string{strings.Repeat("x", 500)})
	if err == nil || len(err.Error()) > 200 {
		t.Errorf("an entry of 500 characters must be shortened in the message: %d", len(err.Error()))
	}
}

func TestLoadAllowedIPsNeverBecomesUnrestricted(t *testing.T) {
	// What the old panel stored may hold anything. The reference fails every request made with such a key; here the key is held to
	// the entries that can be read, and a list that has none blocks every address.
	list, bad := LoadAllowedIPs([]string{"garbage", "also bad"})
	if bad != 2 || !list.Restricted() {
		t.Fatalf("a list of unreadable entries: bad %d, restricted %v", bad, list.Restricted())
	}
	for _, a := range []string{"203.0.113.9", "127.0.0.1", "2001:db8::1"} {
		if list.Allows(ip(a)) {
			t.Errorf("%s was allowed by a list of unreadable entries", a)
		}
	}

	list, bad = LoadAllowedIPs([]string{"garbage", "203.0.113.9", "203.0.113.0/24x"})
	if bad != 2 || !list.Allows(ip("203.0.113.9")) || list.Allows(ip("203.0.113.10")) {
		t.Errorf("a mixed list: bad %d", bad)
	}

	list, bad = LoadAllowedIPs(nil)
	if bad != 0 || list.Restricted() {
		t.Errorf("no entries: bad %d, restricted %v", bad, list.Restricted())
	}
}

func TestDeniedMessage(t *testing.T) {
	got := DeniedMessage(ip("203.0.113.9"))
	if got != "This IP address (203.0.113.9) does not have permission to access the API using these credentials." {
		t.Errorf("message = %q", got)
	}
}

func TestCheckCanCreateKey(t *testing.T) {
	for count, want := range map[int]error{0: nil, 1: nil, 24: nil, 25: ErrTooManyKeys, 26: ErrTooManyKeys, 1000: ErrTooManyKeys} {
		if err := CheckCanCreateKey(count); !errors.Is(err, want) {
			t.Errorf("CheckCanCreateKey(%d) = %v, want %v", count, err, want)
		}
	}
	if ErrTooManyKeys.Error() != "You have reached the account limit for number of API keys." {
		t.Errorf("text = %q", ErrTooManyKeys.Error())
	}
}

func TestValidateKeyDescription(t *testing.T) {
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{"plain", "Deploy script", true},
		{"one character", "a", true},
		{"the longest", strings.Repeat("a", 500), true},
		{"multi-byte characters count once", strings.Repeat("é", 500), true},
		{"empty", "", false},
		{"spaces only", "  \t", false},
		{"too long", strings.Repeat("a", 501), false},
		{"a newline", "a\nb", false},
		{"a NUL", "a\x00b", false},
		{"invalid UTF-8", "a\xffb", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateKeyDescription(tt.in); (err == nil) != tt.ok {
				t.Errorf("ValidateKeyDescription(%q) = %v, want ok=%v", tt.in, err, tt.ok)
			}
		})
	}
}

func TestShouldTouchLastUsed(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { v := now.Add(-d); return &v }
	future := now.Add(time.Hour)
	tests := []struct {
		name string
		last *time.Time
		want bool
	}{
		{"never used", nil, true},
		{"used a moment ago", at(time.Second), false},
		{"used 59 seconds ago", at(59 * time.Second), false},
		{"used a minute ago", at(time.Minute), true},
		{"used a day ago", at(24 * time.Hour), true},
		{"a time in the future is overwritten", &future, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldTouchLastUsed(tt.last, now); got != tt.want {
				t.Errorf("ShouldTouchLastUsed = %v, want %v", got, tt.want)
			}
		})
	}
}
