package nodes

import (
	"bytes"
	"crypto/rand"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func ports(a []Allocation) []int {
	out := make([]int, len(a))
	for i, x := range a {
		out[i] = x.Port
	}
	return out
}

func TestExpandAllocations(t *testing.T) {
	got, err := ExpandAllocations(NewAllocations{IP: "203.0.113.5", Ports: []string{"25565", "25570-25572"}, Alias: "play.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	want := []Allocation{
		{"203.0.113.5", 25565, "play.example.com"}, {"203.0.113.5", 25570, "play.example.com"},
		{"203.0.113.5", 25571, "play.example.com"}, {"203.0.113.5", 25572, "play.example.com"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("allocations = %+v, want %+v", got, want)
	}
}

func TestExpandNetworks(t *testing.T) {
	tests := []struct {
		name  string
		ip    string
		count int
		first string
		last  string
	}{
		{"one address", "203.0.113.5", 1, "203.0.113.5", "203.0.113.5"},
		{"a /32", "203.0.113.5/32", 1, "203.0.113.5", "203.0.113.5"},
		{"a /31", "203.0.113.4/31", 2, "203.0.113.4", "203.0.113.5"},
		{"a /30 given by a host address", "203.0.113.6/30", 4, "203.0.113.4", "203.0.113.7"},
		{"a /25", "203.0.113.0/25", 128, "203.0.113.0", "203.0.113.127"},
		{"a /25 in the upper half", "203.0.113.200/25", 128, "203.0.113.128", "203.0.113.255"},
		{"an IPv6 address", "2001:DB8::1", 1, "2001:db8::1", "2001:db8::1"},
		{"IPv4 as an IPv6-mapped address", "::ffff:203.0.113.5", 1, "203.0.113.5", "203.0.113.5"},
		{"padded", "  203.0.113.5 ", 1, "203.0.113.5", "203.0.113.5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExpandAllocations(NewAllocations{IP: tt.ip, Ports: []string{"25565"}})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tt.count || got[0].IP != tt.first || got[len(got)-1].IP != tt.last {
				t.Errorf("got %d allocations from %s to %s, want %d from %s to %s", len(got), got[0].IP, got[len(got)-1].IP, tt.count, tt.first, tt.last)
			}
		})
	}
}

func TestExpandRefusesAddresses(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want error
	}{
		{"a /24 is too many", "203.0.113.0/24", ErrCIDR},
		{"a /8", "10.0.0.0/8", ErrCIDR},
		{"a /33", "203.0.113.0/33", ErrCIDR},
		{"a /0", "203.0.113.0/0", ErrCIDR},
		{"an empty prefix", "203.0.113.0/", ErrCIDR},
		{"a negative prefix", "203.0.113.0/-25", ErrCIDR},
		{"a plus sign in the prefix", "203.0.113.0/+25", ErrCIDR},
		{"a prefix of letters", "203.0.113.0/ab", ErrCIDR},
		{"an IPv6 network", "2001:db8::/64", ErrCIDR},
		{"an IPv6 network with a short prefix", "2001:db8::/26", ErrCIDR},
		{"a network that is not an address", "example.com/25", ErrCIDR},

		{"a host name", "example.com", ErrNotAnIP},
		{"localhost", "localhost", ErrNotAnIP},
		{"empty", "", ErrNotAnIP},
		{"text", "not an address", ErrNotAnIP},
		{"an address that is too long", "203.0.113.256", ErrNotAnIP},
		{"a zone", "fe80::1%eth0", ErrNotAnIP},
		{"two addresses", "203.0.113.5,203.0.113.6", ErrNotAnIP},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ExpandAllocations(NewAllocations{IP: tt.ip, Ports: []string{"25565"}}); !errors.Is(err, tt.want) {
				t.Errorf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestExpandPorts(t *testing.T) {
	tests := []struct {
		name  string
		specs []string
		want  []int
	}{
		{"one port", []string{"25565"}, []int{25565}},
		{"the lowest port", []string{"1025"}, []int{1025}},
		{"the highest port", []string{"65535"}, []int{65535}},
		{"a range", []string{"25565-25568"}, []int{25565, 25566, 25567, 25568}},
		{"a range of two", []string{"25565-25566"}, []int{25565, 25566}},
		{"a range of one", []string{"25565-25565"}, []int{25565}},
		{"a range written backwards is read forwards", []string{"25568-25565"}, []int{25565, 25566, 25567, 25568}},
		{"the widest range", []string{"25000-25999"}, nil},
		{"a port and a range that overlap", []string{"25566", "25565-25567"}, []int{25566, 25565, 25567}},
		{"the same port twice", []string{"25565", "25565"}, []int{25565}},
		{"a range up to the top", []string{"65530-65535"}, []int{65530, 65531, 65532, 65533, 65534, 65535}},
		{"leading zeros", []string{"025565"}, []int{25565}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExpandAllocations(NewAllocations{IP: "203.0.113.5", Ports: tt.specs})
			if err != nil {
				t.Fatal(err)
			}
			if tt.want == nil {
				if len(got) != 1000 {
					t.Errorf("%d ports, want 1000", len(got))
				}
				return
			}
			if !reflect.DeepEqual(ports(got), tt.want) {
				t.Errorf("ports = %v, want %v", ports(got), tt.want)
			}
		})
	}
}

func TestExpandRefusesPorts(t *testing.T) {
	var invalid *InvalidMappingError
	tests := []struct {
		name   string
		specs  []string
		want   error
		mapped bool
	}{
		{"the floor itself", []string{"1024"}, ErrPortRange, false},
		{"below the floor", []string{"80"}, ErrPortRange, false},
		{"port zero", []string{"0"}, ErrPortRange, false},
		{"above the ceiling", []string{"65536"}, ErrPortRange, false},
		{"a huge port", []string{"123456789"}, ErrPortRange, false},
		{"a range that starts on the floor", []string{"1024-1030"}, ErrPortRange, false},
		{"a range that starts below the floor", []string{"1020-1030"}, ErrPortRange, false},
		{"a range backwards that goes below the floor", []string{"1030-1020"}, ErrPortRange, false},
		{"a range that passes the ceiling", []string{"65530-65536"}, ErrPortRange, false},
		{"a range backwards that passes the ceiling", []string{"65536-65530"}, ErrPortRange, false},
		{"a range of 1001", []string{"25000-26000"}, ErrTooManyPorts, false},
		{"a range of 1001 backwards", []string{"26000-25000"}, ErrTooManyPorts, false},

		{"letters", []string{"abc"}, nil, true},
		{"a port with a space", []string{"25565 "}, nil, true},
		{"a negative port", []string{"-25565"}, nil, true},
		{"a port with a sign", []string{"+25565"}, nil, true},
		{"a decimal port", []string{"25565.5"}, nil, true},
		{"a range with a short end", []string{"25565-80"}, nil, true},
		{"a range of three numbers", []string{"25565-25570-25575"}, nil, true},
		{"a range with an open end", []string{"25565-"}, nil, true},
		{"an empty entry", []string{""}, nil, true},
		{"a list in one entry", []string{"25565,25566"}, nil, true},
		{"a range with spaces", []string{"25565 - 25570"}, nil, true},
		{"a bad entry after good ones", []string{"25565", "x"}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ExpandAllocations(NewAllocations{IP: "203.0.113.5", Ports: tt.specs})
			switch {
			case tt.mapped && !errors.As(err, &invalid):
				t.Errorf("error = %v, want an invalid mapping", err)
			case !tt.mapped && !errors.Is(err, tt.want):
				t.Errorf("error = %v, want %v", err, tt.want)
			}
		})
	}

	if _, err := ExpandAllocations(NewAllocations{IP: "203.0.113.5"}); err == nil {
		t.Error("a request with no ports was accepted")
	}
	if _, err := ExpandAllocations(NewAllocations{IP: "203.0.113.5", Ports: []string{}}); err == nil {
		t.Error("a request with an empty list of ports was accepted")
	}
}

func TestExpandBoundsTheNumberOfRows(t *testing.T) {
	// 128 addresses times 156 ports is 19968; times 157 is over the bound.
	if got, err := ExpandAllocations(NewAllocations{IP: "203.0.113.0/25", Ports: []string{"30000-30155"}}); err != nil || len(got) != 128*156 {
		t.Errorf("a request just under the bound: %d rows, %v", len(got), err)
	}
	if _, err := ExpandAllocations(NewAllocations{IP: "203.0.113.0/25", Ports: []string{"30000-30156"}}); !errors.Is(err, ErrTooMany) {
		t.Errorf("a request over the bound: %v", err)
	}
	// The reference has no bound: a /25 with ten ranges of 1000 ports is 1.28 million rows.
	many := make([]string, 10)
	for i := range many {
		many[i] = "2" + string(rune('0'+i)) + "000-2" + string(rune('0'+i)) + "999"
	}
	if _, err := ExpandAllocations(NewAllocations{IP: "203.0.113.0/25", Ports: many}); !errors.Is(err, ErrTooMany) {
		t.Errorf("a very large request: %v", err)
	}
}

func TestExpandAlias(t *testing.T) {
	for alias, ok := range map[string]bool{
		"": true, "play.example.com": true, "node-1.eu": true, "my_host": true, "2001:db8::1": true, strings.Repeat("a", 191): true,
		strings.Repeat("a", 192): false, "has space": false, "a/b": false, "<script>": false, "a\nb": false, "café": false, "a'b": false,
	} {
		_, err := ExpandAllocations(NewAllocations{IP: "203.0.113.5", Ports: []string{"25565"}, Alias: alias})
		if (err == nil) != ok {
			t.Errorf("alias %q: error = %v, want ok=%v", alias, err, ok)
		}
	}
}

func TestInvalidMappingErrorText(t *testing.T) {
	e := &InvalidMappingError{Port: "abc"}
	if e.Error() != "The mapping provided for abc was invalid and could not be processed." {
		t.Errorf("text = %q", e.Error())
	}
	if got := (&InvalidMappingError{Port: strings.Repeat("x", 200)}).Error(); len(got) > 120 {
		t.Errorf("a long entry must be shortened in the message: %d characters", len(got))
	}
}

func TestErrorTexts(t *testing.T) {
	for err, want := range map[error]string{
		ErrCIDR:         "CIDR notation only allows masks between /25 and /32.",
		ErrPortRange:    "Ports in an allocation must be greater than 1024 and less than or equal to 65535.",
		ErrTooManyPorts: "Adding more than 1000 ports in a single range at once is not supported.",
		ErrNoSpace:      "Cannot assign additional allocation: no more space available on node.",
		ErrAutoDisabled: "Server auto-allocation is not enabled for this instance.",
		ErrAllocLimit:   "Cannot assign additional allocations to this server: limit has been reached.",
		ErrNoLimitSet:   "You cannot delete allocations for this server: no allocation limit is set.",
		ErrPrimary:      "You cannot delete the primary allocation for this server.",
	} {
		if err.Error() != want {
			t.Errorf("%q, want %q", err.Error(), want)
		}
	}
}

func TestValidateNotes(t *testing.T) {
	for notes, ok := range map[string]bool{
		"": true, "for the web panel": true, strings.Repeat("a", 191): true, strings.Repeat("é", 191): true,
		strings.Repeat("a", 192): false, "a\nb": false, "a\x00b": false, "a\xffb": false,
	} {
		if err := ValidateNotes(notes); (err == nil) != ok {
			t.Errorf("ValidateNotes(%q) = %v, want ok=%v", notes, err, ok)
		}
	}
}

func TestCheckCanAddAllocation(t *testing.T) {
	ptr := func(n int64) *int64 { return &n }
	tests := []struct {
		name    string
		enabled bool
		limit   *int64
		count   int64
		want    error
	}{
		{"room left", true, ptr(3), 2, nil},
		{"at the limit", true, ptr(3), 3, ErrAllocLimit},
		{"over the limit", true, ptr(3), 5, ErrAllocLimit},
		{"limit zero", true, ptr(0), 0, ErrAllocLimit},
		{"no limit set is none, not unlimited", true, nil, 0, ErrAllocLimit},
		{"the feature is off", false, ptr(3), 0, ErrAutoDisabled},
		{"the feature is checked first", false, nil, 5, ErrAutoDisabled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := CheckCanAddAllocation(tt.enabled, tt.limit, tt.count); !errors.Is(err, tt.want) {
				t.Errorf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestCheckCanDeleteAllocation(t *testing.T) {
	ptr := func(n int64) *int64 { return &n }
	tests := []struct {
		name  string
		limit *int64
		id    uint64
		prim  uint64
		want  error
	}{
		{"a spare allocation", ptr(3), 7, 5, nil},
		{"the primary", ptr(3), 5, 5, ErrPrimary},
		{"no limit set", nil, 7, 5, ErrNoLimitSet},
		{"limit zero", ptr(0), 7, 5, ErrNoLimitSet},
		{"no limit is checked before the primary", ptr(0), 5, 5, ErrNoLimitSet},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := CheckCanDeleteAllocation(tt.limit, tt.id, tt.prim); !errors.Is(err, tt.want) {
				t.Errorf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestPickPort(t *testing.T) {
	// Every free port is picked sometimes, and no used one ever is.
	used := []int{25001, 25003}
	seen := map[int]bool{}
	for i := 0; i < 400; i++ {
		p, err := PickPort(25000, 25004, used, rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if p == 25001 || p == 25003 || p < 25000 || p > 25004 {
			t.Fatalf("picked %d", p)
		}
		seen[p] = true
	}
	if len(seen) != 3 {
		t.Errorf("picked only %v of the 3 free ports", seen)
	}

	if p, err := PickPort(25000, 25000, nil, rand.Reader); err != nil || p != 25000 {
		t.Errorf("a range of one: %d, %v", p, err)
	}
	if _, err := PickPort(25000, 25002, []int{25000, 25001, 25002}, rand.Reader); !errors.Is(err, ErrNoSpace) {
		t.Errorf("a full range: %v", err)
	}
	if _, err := PickPort(25000, 25002, []int{9, 25000, 25001, 25002, 70000}, rand.Reader); !errors.Is(err, ErrNoSpace) {
		t.Errorf("a full range with ports outside it in the list: %v", err)
	}
	if p, err := PickPort(25000, 25002, []int{9, 70000, 25001}, bytes.NewReader([]byte{0, 0, 0, 1})); err != nil || p != 25002 {
		t.Errorf("ports outside the range are not in the way: %d, %v", p, err)
	}
	for _, bad := range [][2]int{{0, 100}, {-5, 100}, {25005, 25000}, {25000, 65536}} {
		if _, err := PickPort(bad[0], bad[1], nil, rand.Reader); !errors.Is(err, ErrNoSpace) {
			t.Errorf("range %v: error = %v", bad, err)
		}
	}
	if _, err := PickPort(25000, 25002, nil, bytes.NewReader(nil)); err == nil {
		t.Error("want an error when no random bytes can be read")
	}
}

func TestExpandBoundIsExact(t *testing.T) {
	// 20 ranges of 1000 ports on one address is exactly the bound; one more port is over it.
	var specs []string
	for i := 0; i < 20; i++ {
		lo := 10000 + i*1000
		specs = append(specs, strconv.Itoa(lo)+"-"+strconv.Itoa(lo+999))
	}
	if got, err := ExpandAllocations(NewAllocations{IP: "203.0.113.5", Ports: specs}); err != nil || len(got) != MaxAllocationsPerRequest {
		t.Fatalf("exactly the bound: %d rows, %v", len(got), err)
	}
	if _, err := ExpandAllocations(NewAllocations{IP: "203.0.113.5", Ports: append(specs, "40000")}); !errors.Is(err, ErrTooMany) {
		t.Errorf("one over the bound: %v", err)
	}
}
