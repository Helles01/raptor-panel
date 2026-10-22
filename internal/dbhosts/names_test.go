package dbhosts

import (
	"bytes"
	"crypto/rand"
	"errors"
	"math"
	"strings"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func TestUniqueName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		id   uint64
		want string
		ok   bool
	}{
		{"plain", "mydb", 5, "s5_mydb", true},
		{"dashes and underscores", "my_db-2", 12, "s12_my_db-2", true},
		{"the shortest", "abc", 1, "s1_abc", true},
		{"cut to 48 in all", strings.Repeat("a", 48), 5, "s5_" + strings.Repeat("a", 45), true},
		{"cut for a long id", strings.Repeat("a", 48), 123456789012, "s123456789012_" + strings.Repeat("a", 34), true},
		{"the largest id still has room", strings.Repeat("a", 48), 18446744073709551615, "s18446744073709551615_" + strings.Repeat("a", 26), true},
		{"short enough to stay whole", strings.Repeat("a", 40), 5, "s5_" + strings.Repeat("a", 40), true},

		{"too short", "ab", 1, "", false},
		{"empty", "", 1, "", false},
		{"too long", strings.Repeat("a", 49), 1, "", false},
		{"a space", "my db", 1, "", false},
		{"a dot", "my.db", 1, "", false},
		{"a backtick", "my`db", 1, "", false},
		{"a quote", "my'db", 1, "", false},
		{"a semicolon", "my;db", 1, "", false},
		{"a percent sign", "my%db", 1, "", false},
		{"a Unicode letter", "cafédb", 1, "", false},
		{"a newline", "mydb\n", 1, "", false},
		{"a NUL", "my\x00db", 1, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := UniqueName(tt.in, tt.id)
			if (err == nil) != tt.ok || got != tt.want {
				t.Errorf("UniqueName(%q, %d) = %q, %v; want %q, ok=%v", tt.in, tt.id, got, err, tt.want, tt.ok)
			}
			if tt.ok && len(got) > MaxDatabaseNameLength {
				t.Errorf("%q is longer than %d", got, MaxDatabaseNameLength)
			}
		})
	}
}

func TestUsername(t *testing.T) {
	got, err := Username(7, bytes.NewReader(bytes.Repeat([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, 10)))
	if err != nil || got != "u7_ABCDEFGHIJ" {
		t.Errorf("Username = %q, %v", got, err)
	}
	if _, err := Username(7, bytes.NewReader(nil)); err == nil {
		t.Error("want an error when no random bytes can be read")
	}
}

func TestPassword(t *testing.T) {
	counts := map[int]int{}
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		p, err := Password(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if len(p) != PasswordLength || !passwordChars.MatchString(p) {
			t.Fatalf("password %q has the wrong shape", p)
		}
		specials := 0
		for _, c := range p {
			if strings.ContainsRune(specialCharacters, c) {
				specials++
			}
		}
		if specials < 2 || specials > 6 {
			t.Fatalf("password %q has %d special characters", p, specials)
		}
		counts[specials]++
		seen[p] = true
	}
	for n := 2; n <= 6; n++ {
		if counts[n] == 0 {
			t.Errorf("never %d special characters in 2000 passwords: %v", n, counts)
		}
	}
	if len(seen) != 2000 {
		t.Errorf("%d different passwords in 2000", len(seen))
	}

	if _, err := Password(bytes.NewReader(nil)); err == nil {
		t.Error("want an error when no random bytes can be read")
	}
	// Enough bytes for the letters and digits but not for the special characters.
	if _, err := Password(bytes.NewReader(bytes.Repeat([]byte{1}, 40))); err == nil {
		t.Error("want an error when the random bytes run out half way")
	}
}

func TestRandomBelowHasNoBias(t *testing.T) {
	// For n=5 the byte 255 would favour 0, so it is skipped: 255 then 7 gives 7 mod 5.
	got, err := randomBelow(bytes.NewReader([]byte{255, 7}), 5)
	if err != nil || got != 2 {
		t.Errorf("randomBelow = %d, %v; want 2", got, err)
	}

	counts := make([]int, 62)
	for i := 0; i < 62000; i++ {
		n, err := randomBelow(rand.Reader, 62)
		if err != nil {
			t.Fatal(err)
		}
		counts[n]++
	}
	for n, c := range counts {
		if math.Abs(float64(c)-1000) > 200 {
			t.Errorf("value %d came up %d times in 62000 draws", n, c)
		}
	}
}

func TestValidateRemote(t *testing.T) {
	for _, ok := range []string{"%", "127.0.0.1", "192.168.%", "10.0.0.0/255.0.0.0", "::1", "db.example.com", "2001:db8::1", "host_name-1"} {
		if err := ValidateRemote(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "a b", "a`b", "a'b", "a;b", "a@b", "a\\b", "a\nb", "a\x00b", "hõst", strings.Repeat("a", 256), "%%'; DROP DATABASE x; --"} {
		if err := ValidateRemote(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestValidateMaxConnections(t *testing.T) {
	for n, ok := range map[int]bool{0: true, 1: true, 100: true, MaxConnectionsLimit: true, -1: false, MaxConnectionsLimit + 1: false} {
		if (ValidateMaxConnections(n) == nil) != ok {
			t.Errorf("ValidateMaxConnections(%d) ok = %v", n, !ok)
		}
	}
}

func TestHostValidate(t *testing.T) {
	good := Host{Name: "Main database", Host: "db-1.example.com", Port: 3306, Username: "panel"}
	with := func(f func(*Host)) Host { h := good; f(&h); return h }

	tests := []struct {
		name string
		h    Host
		ok   bool
	}{
		{"good", good, true},
		{"an address", with(func(h *Host) { h.Host = "10.0.0.5" }), true},
		{"the largest port", with(func(h *Host) { h.Port = 65535 }), true},
		{"a 32 character user", with(func(h *Host) { h.Username = strings.Repeat("u", 32) }), true},
		{"no name", with(func(h *Host) { h.Name = "  " }), false},
		{"a long name", with(func(h *Host) { h.Name = strings.Repeat("n", 192) }), false},
		{"a control character in the name", with(func(h *Host) { h.Name = "a\nb" }), false},
		{"a host with a space", with(func(h *Host) { h.Host = "db 1" }), false},
		{"a host with a slash", with(func(h *Host) { h.Host = "db/1" }), false},
		{"no host", with(func(h *Host) { h.Host = "" }), false},
		{"port 0", with(func(h *Host) { h.Port = 0 }), false},
		{"port 65536", with(func(h *Host) { h.Port = 65536 }), false},
		{"no user", with(func(h *Host) { h.Username = "" }), false},
		{"a 33 character user", with(func(h *Host) { h.Username = strings.Repeat("u", 33) }), false},
		{"a control character in the user", with(func(h *Host) { h.Username = "a\x00" }), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.h.Validate(); (err == nil) != tt.ok {
				t.Errorf("Validate() = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestCheckCanCreate(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		limit   *int64
		current int64
		want    error
	}{
		{"room left", true, ptr(int64(3)), 2, nil},
		{"no limit", true, nil, 1000, nil},
		{"at the limit", true, ptr(int64(3)), 3, ErrTooMany},
		{"over the limit", true, ptr(int64(3)), 5, ErrTooMany},
		{"limit zero", true, ptr(int64(0)), 0, ErrTooMany},
		{"the feature is off", false, nil, 0, ErrFeatureDisabled},
		{"the feature is checked first", false, ptr(int64(0)), 5, ErrFeatureDisabled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := CheckCanCreate(tt.enabled, tt.limit, tt.current); !errors.Is(err, tt.want) {
				t.Errorf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestChooseHost(t *testing.T) {
	node := func(n uint64) *uint64 { return &n }
	hosts := []HostChoice{{ID: 1, NodeID: node(5)}, {ID: 2, NodeID: node(6)}, {ID: 3}, {ID: 4, NodeID: node(5)}, {ID: 5, NodeID: node(7)}}

	for i := 0; i < 200; i++ {
		got, err := ChooseHost(hosts, 5, false, rand.Reader)
		if err != nil || (got.ID != 1 && got.ID != 4) {
			t.Fatalf("with hosts linked to the node, got %+v, %v", got, err)
		}
	}
	seen := map[uint64]bool{}
	for i := 0; i < 500; i++ {
		got, err := ChooseHost(hosts, 5, true, rand.Reader)
		if err != nil || (got.ID != 1 && got.ID != 4) {
			t.Fatalf("a linked host is preferred even when any may be used, got %+v, %v", got, err)
		}
		seen[got.ID] = true
	}
	if len(seen) != 2 {
		t.Errorf("both linked hosts should be chosen over 500 tries, got %v", seen)
	}

	for i := 0; i < 500; i++ {
		got, err := ChooseHost(hosts, 99, true, rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		seen[got.ID] = true
	}
	if len(seen) != 5 {
		t.Errorf("with no linked host any host may be chosen, got %v", seen)
	}

	if _, err := ChooseHost(hosts, 99, false, rand.Reader); !errors.Is(err, ErrNoSuitableHost) {
		t.Errorf("no linked host and no random use: error = %v", err)
	}
	if _, err := ChooseHost(nil, 5, true, rand.Reader); !errors.Is(err, ErrNoSuitableHost) {
		t.Errorf("no hosts at all: error = %v", err)
	}
	if got, err := ChooseHost([]HostChoice{{ID: 3}}, 5, false, rand.Reader); err == nil {
		t.Errorf("a host linked to no node must not match a node: got %+v", got)
	}
	if _, err := ChooseHost(hosts, 5, false, bytes.NewReader(nil)); err == nil {
		t.Error("want an error when no random bytes can be read")
	}
}
