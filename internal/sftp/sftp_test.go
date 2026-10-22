package sftp

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/servers"
)

const (
	edKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBhgpejLQl2Ae5qAX7iRAbunnQxYZbuWBxF3zxC/xher test-ed"
	edFP  = "/jWxgXv75WMhCGdCuGjOluuSlDlO1hmb5tYUR/rDQ1U"
	p256  = "ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBAru94FXOnpKbtWTYt39fx+ZCIskuyBCO5fqLZVVTMCTmQCaS9Qz0wUAmA1kWcjoMJsdW7yHcS19jHiRquxCytA= test-p256"
	p256F = "71G2esI8je867ZPzZSCfQPoDtBF+amAO34DSO6wBDW4"
)

func TestRequestMethod(t *testing.T) {
	tests := []struct {
		name string
		r    Request
		want string
		ok   bool
	}{
		{"a password, no type", Request{Username: "jane.abcd1234", Password: "secret"}, TypePassword, true},
		{"a password", Request{Type: "password", Username: "jane.abcd1234", Password: "secret"}, TypePassword, true},
		{"a public key", Request{Type: "public_key", Username: "jane.abcd1234", Password: edKey + "\n"}, TypePublicKey, true},
		{"a long key", Request{Type: "public_key", Username: "u.abcd1234", Password: strings.Repeat("a", 16384)}, TypePublicKey, true},

		{"another type", Request{Type: "keyboard-interactive", Username: "u.abcd1234", Password: "x"}, "", false},
		{"a type in upper case", Request{Type: "PASSWORD", Username: "u.abcd1234", Password: "x"}, "", false},
		{"no username", Request{Password: "x"}, "", false},
		{"no password", Request{Username: "u.abcd1234"}, "", false},
		{"a username that is too long", Request{Username: strings.Repeat("a", 256), Password: "x"}, "", false},
		{"a credential that is too long", Request{Username: "u.abcd1234", Password: strings.Repeat("a", 16385)}, "", false},
		{"a public key request with nothing in it", Request{Type: "public_key", Username: "u.abcd1234"}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.r.Method()
			if (err == nil) != tt.ok || got != tt.want {
				t.Errorf("Method() = %q, %v; want %q, ok=%v", got, err, tt.want, tt.ok)
			}
		})
	}
}

func TestRequestDecodesWhatTheNodeSends(t *testing.T) {
	body := `{"type":"public_key","username":"jane.abcd1234","password":"k","ip":"203.0.113.9:51234","session_id":"AAAA","client_version":"U1NILTIuMA=="}`
	var r Request
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	if r != (Request{Type: "public_key", Username: "jane.abcd1234", Password: "k", IP: "203.0.113.9:51234"}) {
		t.Errorf("request = %+v", r)
	}
}

func TestParseUsername(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  Login
		err   error
		login bool
	}{
		{"a user and a server", "jane.abcd1234", Login{"jane", "abcd1234"}, nil, true},
		{"a user name with dots", "jane.doe.abcd1234", Login{"jane.doe", "abcd1234"}, nil, true},
		{"dashes and underscores", "j_a-n.e.abcd1234", Login{"j_a-n.e", "abcd1234"}, nil, true},
		{"an upper-case server id", "jane.ABCD1234", Login{"jane", "ABCD1234"}, nil, true},
		{"a server id that looks like a user name", "jane.abcdefgh", Login{"jane", "abcdefgh"}, nil, true},
		{"the longest user name", strings.Repeat("a", 191) + ".abcd1234", Login{strings.Repeat("a", 191), "abcd1234"}, nil, true},

		{"no dot", "janeabcd1234", Login{}, ErrBadServerID, false},
		{"no server id", "jane.", Login{}, ErrBadServerID, false},
		{"a server id that is too short", "jane.abc1234", Login{}, ErrBadServerID, false},
		{"a server id that is too long", "jane.abcd12345", Login{}, ErrBadServerID, false},
		{"a server id with a dash", "jane.abcd-234", Login{}, ErrBadServerID, false},
		{"a full UUID", "jane.123e4567-e89b-42d3-a456-426614174000", Login{}, ErrBadServerID, false},
		{"empty", "", Login{}, ErrBadServerID, false},
		{"only a dot", ".", Login{}, ErrBadServerID, false},

		{"no user name", ".abcd1234", Login{}, ErrRejected, false},
		{"a space in the user name", "ja ne.abcd1234", Login{}, ErrRejected, false},
		{"a newline in the user name", "jane\n.abcd1234", Login{}, ErrRejected, false},
		{"a NUL in the user name", "ja\x00ne.abcd1234", Login{}, ErrRejected, false},
		{"a user name that is too long", strings.Repeat("a", 192) + ".abcd1234", Login{}, ErrRejected, false},
		{"a Unicode user name", "jáne.abcd1234", Login{}, ErrRejected, false},
		{"a quote in the user name", "ja'ne.abcd1234", Login{}, ErrRejected, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseUsername(tt.in)
			if !errors.Is(err, tt.err) || got != tt.want {
				t.Errorf("ParseUsername(%q) = %+v, %v; want %+v, %v", tt.in, got, err, tt.want, tt.err)
			}
		})
	}
}

func keys(ts []Throttle) []string {
	out := make([]string, len(ts))
	for i, th := range ts {
		out[i] = th.Key
	}
	return out
}

func TestThrottles(t *testing.T) {
	ts := Throttles("Jane", "203.0.113.9:51234", 7)
	want := []Throttle{
		{"sftp|user-client|jane|203.0.113.9", 5, time.Minute},
		{"sftp|user|jane", 30, 10 * time.Minute},
		{"sftp|client|203.0.113.9", 60, 10 * time.Minute},
	}
	if !reflect.DeepEqual(ts, want) {
		t.Errorf("Throttles = %+v, want %+v", ts, want)
	}
}

func TestThrottleClients(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want string // the client part of the key
	}{
		{"IPv4 with a port", "203.0.113.9:51234", "203.0.113.9"},
		{"IPv4 without a port", "203.0.113.9", "203.0.113.9"},
		{"IPv6 with a port", "[2001:db8:1:2:3:4:5:6]:22", "2001:db8:1:2::/64"},
		{"IPv6 without a port", "2001:db8:1:2:3:4:5:6", "2001:db8:1:2::/64"},
		{"another address in the same /64", "[2001:db8:1:2:ffff::1]:99", "2001:db8:1:2::/64"},
		{"IPv4 inside IPv6", "[::ffff:203.0.113.9]:22", "203.0.113.9"},
		{"a zone is dropped", "[fe80::1%eth0]:22", "fe80::/64"},
		{"garbage", "not an address", "node-7"},
		{"empty", "", "node-7"},
		{"a port only", ":22", "node-7"},
		{"a host name", "client.example.com:22", "node-7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := Throttles("jane", tt.addr, 7)
			if want := "sftp|client|" + tt.want; ts[2].Key != want {
				t.Errorf("client key = %q, want %q", ts[2].Key, want)
			}
			if want := "sftp|user-client|jane|" + tt.want; ts[0].Key != want {
				t.Errorf("user-and-client key = %q, want %q", ts[0].Key, want)
			}
		})
	}
}

func TestThrottlesSeparateWhatTheyShouldSeparate(t *testing.T) {
	a := keys(Throttles("jane", "203.0.113.9:1", 7))
	sameAll := keys(Throttles("JANE", "203.0.113.9:55555", 7))
	otherUser := keys(Throttles("john", "203.0.113.9:1", 7))
	otherClient := keys(Throttles("jane", "203.0.113.10:1", 7))

	if !reflect.DeepEqual(a, sameAll) {
		t.Error("the same user from the same address must share counters, whatever the case and the port")
	}
	// A different user from the same address shares only the per-address counter.
	if a[0] == otherUser[0] || a[1] == otherUser[1] || a[2] != otherUser[2] {
		t.Errorf("another user: %v vs %v", a, otherUser)
	}
	// The same user from another address shares only the per-user counter.
	if a[0] == otherClient[0] || a[1] != otherClient[1] || a[2] == otherClient[2] {
		t.Errorf("another client: %v vs %v", a, otherClient)
	}
	// The key of the reference, which was the server and the address of the request, is nowhere in these.
	for _, k := range a {
		if strings.Contains(k, "abcd1234") {
			t.Errorf("key %q holds a server id", k)
		}
	}

	// Two nodes that send nothing readable do not share a counter.
	if Throttles("jane", "", 1)[2].Key == Throttles("jane", "", 2)[2].Key {
		t.Error("different nodes share the key of an unreadable address")
	}
}

func TestCheckKey(t *testing.T) {
	registered := []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", edFP}
	tests := []struct {
		name       string
		offered    string
		registered []string
		want       KeyOutcome
	}{
		{"a registered key", edKey, registered, KeyMatch},
		{"with the newline the node adds", edKey + "\n", registered, KeyMatch},
		{"a valid key that is not registered", p256, registered, KeyUnknown},
		{"a valid key and nothing registered", edKey, nil, KeyUnknown},
		{"text that is not a key", "hello", registered, KeyInvalid},
		{"empty", "", registered, KeyInvalid},
		{"a key with options", `command="x" ` + edKey, registered, KeyInvalid},
		{"the fingerprint with its prefix does not match", edKey, []string{"SHA256:" + edFP}, KeyUnknown},
		{"the fingerprint with padding does not match", p256, []string{p256F + "="}, KeyUnknown},
		{"the second registered key", p256, []string{edFP, p256F}, KeyMatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CheckKey(tt.offered, tt.registered); got != tt.want {
				t.Errorf("CheckKey = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestAuthorize(t *testing.T) {
	const serverUUID = "abcd1234-0000-4000-8000-000000000001"
	owner := User{ID: 10, UUID: "owner-uuid"}
	admin := User{ID: 99, UUID: "admin-uuid", Admin: true}
	sub := User{ID: 20, UUID: "sub-uuid"}
	access := func(f func(*Access)) Access {
		a := Access{ServerUUID: serverUUID, OwnerID: 10}
		if f != nil {
			f(&a)
		}
		return a
	}
	withSub := func(p ...string) func(*Access) { return func(a *Access) { a.Subuser = p } }

	adminPerms := []string{"*", "admin.websocket.errors", "admin.websocket.install", "admin.websocket.transfer"}
	tests := []struct {
		name string
		u    User
		a    Access
		want []string
		err  error
	}{
		{"the owner", owner, access(nil), []string{"*"}, nil},
		{"an administrator", admin, access(nil), adminPerms, nil},
		{"an administrator who is a subuser too", admin, access(withSub("file.sftp")), adminPerms, nil},
		{"a subuser with the SFTP right", sub, access(withSub("file.sftp", "file.read", "websocket.connect")), []string{"file.sftp", "file.read", "websocket.connect"}, nil},

		{"a subuser without the SFTP right", sub, access(withSub("file.read", "websocket.connect")), nil, ErrDenied},
		{"a user with no access at all", sub, access(nil), nil, ErrDenied},
		{"the right of a similar name", sub, access(withSub("file.sftp ", "file", "sftp")), nil, ErrDenied},
		{"a user id of 0 is not the owner", User{UUID: "x"}, Access{ServerUUID: serverUUID}, nil, ErrDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, err := Authorize(tt.u, tt.a)
			if !errors.Is(err, tt.err) {
				t.Fatalf("error = %v, want %v", err, tt.err)
			}
			if tt.err != nil {
				return
			}
			if g.User != tt.u.UUID || g.Server != serverUUID || !reflect.DeepEqual(g.Permissions, tt.want) {
				t.Errorf("grant = %+v, want permissions %v", g, tt.want)
			}
		})
	}
}

func TestAuthorizeChecksTheServerState(t *testing.T) {
	owner := User{ID: 10, UUID: "owner-uuid"}
	sub := User{ID: 20, UUID: "sub-uuid"}

	states := []struct {
		name  string
		state servers.State
	}{
		{"suspended", servers.State{Status: "suspended"}},
		{"on a node under maintenance", servers.State{NodeMaintenance: true}},
		{"installing", servers.State{Status: "installing"}},
		{"restoring a backup", servers.State{Status: "restoring_backup"}},
		{"being transferred", servers.State{Transferring: true}},
	}
	for _, tt := range states {
		t.Run(tt.name, func(t *testing.T) {
			// Not even the owner may connect.
			_, err := Authorize(owner, Access{OwnerID: 10, State: tt.state})
			var se *servers.StateError
			if !errors.As(err, &se) || se.Message == "" {
				t.Errorf("error = %v, want a state error", err)
			}

			// The right to use SFTP is judged first: someone without it learns nothing about the state.
			_, err = Authorize(sub, Access{OwnerID: 10, State: tt.state, Subuser: []string{"file.read"}})
			if !errors.Is(err, ErrDenied) {
				t.Errorf("error = %v, want ErrDenied", err)
			}

			// With the right they hear the state.
			_, err = Authorize(sub, Access{OwnerID: 10, State: tt.state, Subuser: []string{"file.sftp"}})
			if !errors.As(err, &se) {
				t.Errorf("error = %v, want a state error", err)
			}
		})
	}
}

func TestGrantJSON(t *testing.T) {
	raw, err := json.Marshal(Grant{User: "u", Server: "s", Permissions: []string{"*"}})
	if err != nil || string(raw) != `{"user":"u","server":"s","permissions":["*"]}` {
		t.Errorf("Grant = %s, %v", raw, err)
	}
}
