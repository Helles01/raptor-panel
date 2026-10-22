package auth

import (
	"bytes"
	"crypto/rand"
	"reflect"
	"strings"
	"testing"
)

func TestNewAPIKey(t *testing.T) {
	tests := []struct {
		name       string
		keyType    KeyType
		wantPrefix string
		wantErr    bool
	}{
		{"account key", KeyAccount, "ptlc_", false},
		{"application key", KeyApplication, "ptla_", false},
		{"unsupported type", KeyType(3), "", true},
		{"zero type", KeyType(0), "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			identifier, secret, err := NewAPIKey(rand.Reader, tt.keyType)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if len(identifier) != IdentifierLength || !strings.HasPrefix(identifier, tt.wantPrefix) {
				t.Errorf("identifier = %q", identifier)
			}
			if len(secret) != SecretLength {
				t.Errorf("secret = %q, want %d characters", secret, SecretLength)
			}

			// The full key must split back into the same two parts.
			gotID, gotSecret, ok := SplitKey(identifier + secret)
			if !ok || gotID != identifier || gotSecret != secret {
				t.Errorf("SplitKey(identifier+secret) = %q, %q, %v", gotID, gotSecret, ok)
			}
		})
	}
}

func TestNewAPIKeyIsRandom(t *testing.T) {
	id1, s1, _ := NewAPIKey(rand.Reader, KeyApplication)
	id2, s2, _ := NewAPIKey(rand.Reader, KeyApplication)
	if id1 == id2 || s1 == s2 {
		t.Error("two generated keys share a part")
	}
}

func TestNewAPIKeyReadError(t *testing.T) {
	if _, _, err := NewAPIKey(bytes.NewReader([]byte{1, 2, 3}), KeyAccount); err == nil {
		t.Error("a failing random source must be an error")
	}
}

func TestSplitKey(t *testing.T) {
	good := "ptla_ABCDEFGHIJK" + strings.Repeat("x", 32)

	tests := []struct {
		name string
		key  string
		ok   bool
	}{
		{"valid", good, true},
		{"too short", good[:47], false},
		{"too long", good + "x", false},
		{"empty", "", false},
		{"punctuation in the secret", good[:47] + "!", false},
		{"underscore in the secret", good[:47] + "_", false},
		{"space in the identifier", "ptla ABCDEFGHIJK" + strings.Repeat("x", 32), false},
		{"newline at the end", good[:47] + "\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, ok := SplitKey(tt.key)
			if ok != tt.ok {
				t.Errorf("SplitKey ok = %v, want %v", ok, tt.ok)
			}
		})
	}
}

func TestParseAccess(t *testing.T) {
	tests := []struct {
		in      string
		want    Access
		wantErr bool
	}{
		{"none", AccessNone, false},
		{"read", AccessRead, false},
		{"rw", AccessReadWrite, false},
		{" RW ", AccessReadWrite, false},
		{"readwrite", AccessReadWrite, false},
		{"write", 0, true},
		{"", 0, true},
		{"3", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseAccess(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("ParseAccess(%q) = %v, %v; want %v, error %v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestAccessAllows(t *testing.T) {
	const write Access = 2
	tests := []struct {
		access      Access
		read, write bool
	}{
		{AccessNone, false, false},
		{AccessRead, true, false},
		{AccessReadWrite, true, true},
	}
	for _, tt := range tests {
		if tt.access.Allows(AccessRead) != tt.read || tt.access.Allows(write) != tt.write {
			t.Errorf("Access(%d): read=%v write=%v, want %v/%v", tt.access, tt.access.Allows(AccessRead), tt.access.Allows(write), tt.read, tt.write)
		}
	}
}

func TestParseResourceAccess(t *testing.T) {
	none := func() map[string]Access {
		m := map[string]Access{}
		for _, r := range ApplicationResources() {
			m[r] = AccessNone
		}
		return m
	}
	with := func(kv ...any) map[string]Access {
		m := none()
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1].(Access)
		}
		return m
	}

	tests := []struct {
		name    string
		specs   []string
		want    map[string]Access
		wantErr string
	}{
		{"nothing given", nil, none(), ""},
		{"two resources", []string{"servers=rw", "users=read"}, with("servers", AccessReadWrite, "users", AccessRead), ""},
		{"explicit none", []string{"nodes=none"}, none(), ""},
		{"no equals sign", []string{"servers"}, nil, "resource=level"},
		{"unknown resource", []string{"admins=rw"}, nil, `unknown resource "admins"`},
		{"unknown level", []string{"servers=all"}, nil, "none, read or rw"},
		{"duplicate", []string{"servers=read", "servers=rw"}, nil, "given twice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseResourceAccess(tt.specs)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseResourceAccess: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApplicationResourcesMatchTheReference(t *testing.T) {
	// The r_* columns of the reference panel's api_keys table, in AdminAcl order.
	want := []string{"servers", "nodes", "allocations", "users", "locations", "nests", "eggs", "database_hosts", "server_databases"}
	if got := ApplicationResources(); !reflect.DeepEqual(got, want) {
		t.Errorf("ApplicationResources = %v, want %v", got, want)
	}
}
