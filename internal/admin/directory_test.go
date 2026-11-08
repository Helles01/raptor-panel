package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/auth"
)

type directoryStore struct {
	users    []User
	keys     []KeyInfo
	deleted  []string
	asked    []*uint64
	settings map[string]string
	reads    int
	// kinds is what DeleteKeys was asked to remove, in order; deleteErr is what it answers.
	kinds     []*auth.KeyType
	deleteErr error
}

func (s *directoryStore) ListUsers(context.Context) ([]User, error) { return s.users, nil }

func (s *directoryStore) FindUser(_ context.Context, ref string) (User, error) {
	for _, u := range s.users {
		if u.Username == ref {
			return u, nil
		}
	}
	return User{}, ErrNotFound
}

func (s *directoryStore) ListKeys(_ context.Context, userID *uint64) ([]KeyInfo, error) {
	s.asked = append(s.asked, userID)
	var out []KeyInfo
	for _, k := range s.keys {
		if userID == nil || k.UserID == *userID {
			out = append(out, k)
		}
	}
	return out, nil
}

func (s *directoryStore) DeleteKey(_ context.Context, identifier string) (KeyInfo, error) {
	for _, k := range s.keys {
		if k.Identifier == identifier {
			s.deleted = append(s.deleted, identifier)
			return k, nil
		}
	}
	return KeyInfo{}, ErrNotFound
}

func (s *directoryStore) DeleteKeys(_ context.Context, kind *auth.KeyType) (int64, error) {
	s.kinds = append(s.kinds, kind)
	if s.deleteErr != nil {
		return 0, s.deleteErr
	}
	var kept []KeyInfo
	var n int64
	for _, k := range s.keys {
		if kind == nil || k.Type == *kind {
			n++
		} else {
			kept = append(kept, k)
		}
	}
	s.keys = kept
	return n, nil
}

func (s *directoryStore) ListLocations(context.Context) ([]LocationInfo, error) {
	return []LocationInfo{{ID: 1, Short: "eu", Nodes: 2}}, nil
}

func (s *directoryStore) ListNodes(context.Context) ([]NodeInfo, error) {
	return []NodeInfo{{ID: 4, Name: "n1", FQDN: "n1.example.com", Port: 8080}}, nil
}

func (s *directoryStore) Setting(_ context.Context, key string) (string, bool, error) {
	s.reads++
	v, ok := s.settings[key]
	return v, ok, nil
}

func newDirectory(t *testing.T) (*Directory, *directoryStore) {
	t.Helper()
	st := &directoryStore{
		users: []User{{ID: 1, Username: "ada"}, {ID: 2, Username: "bob"}},
		keys: []KeyInfo{
			{Identifier: "ptlc_abcdefghijk", UserID: 1, Type: auth.KeyAccount},
			{Identifier: "ptla_ABCDEFGHIJK", UserID: 1, Type: auth.KeyApplication},
			{Identifier: "ptlc_01234567890", UserID: 2, Type: auth.KeyAccount},
		},
		settings: map[string]string{"app:name": "Raptor"},
	}
	d, err := NewDirectory(st)
	if err != nil {
		t.Fatal(err)
	}
	return d, st
}

func TestDirectoryKeys(t *testing.T) {
	d, st := newDirectory(t)
	all, err := d.Keys(context.Background(), "")
	if err != nil || len(all) != 3 || st.asked[0] != nil {
		t.Fatalf("all = %v, err = %v, asked %v", all, err, st.asked)
	}
	mine, err := d.Keys(context.Background(), "ada")
	if err != nil || len(mine) != 2 || st.asked[1] == nil || *st.asked[1] != 1 {
		t.Fatalf("ada's = %v, err = %v", mine, err)
	}
	if _, err := d.Keys(context.Background(), "nobody"); !errors.Is(err, ErrNotFound) || len(st.asked) != 2 {
		t.Errorf("unknown user: err = %v, lists asked %d", err, len(st.asked))
	}
}

func TestRevokeKey(t *testing.T) {
	d, st := newDirectory(t)
	k, err := d.RevokeKey(context.Background(), "ptla_ABCDEFGHIJK")
	if err != nil || k.UserID != 1 || len(st.deleted) != 1 {
		t.Fatalf("k = %v, err = %v", k, err)
	}
	if _, err := d.RevokeKey(context.Background(), "ptlc_zzzzzzzzzzz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown key: %v", err)
	}
}

// The command takes the identifier; a whole key, secret included, is refused before anything is looked up, and is not repeated.
func TestRevokeKeyRefusesWhatIsNotAnIdentifier(t *testing.T) {
	d, st := newDirectory(t)
	secret := strings.Repeat("s", 32)
	for name, id := range map[string]string{
		"empty":        "",
		"whole key":    "ptlc_abcdefghijk" + secret,
		"too short":    "ptlc_abcdefghij",
		"too long":     "ptlc_abcdefghijkl",
		"wrong prefix": "ptlx_abcdefghijk",
		"upper prefix": "PTLC_abcdefghijk",
		"quote":        "ptlc_abcdefghi'k",
		"space":        "ptlc_abcdefghi k",
		"newline":      "ptlc_abcdefghij\n",
		"no prefix":    "abcdefghijklmnop",
		"text before":  "xptlc_abcdefghijk",
		"text after":   "ptlc_abcdefghijk\nx",
	} {
		_, err := d.RevokeKey(context.Background(), id)
		if err == nil || errors.Is(err, ErrNotFound) || len(st.deleted) != 0 || strings.Contains(err.Error(), secret) {
			t.Errorf("%s: err = %v, deleted %v", name, err, st.deleted)
		}
	}
}

func TestDirectorySetting(t *testing.T) {
	d, st := newDirectory(t)
	v, ok, err := d.Setting(context.Background(), "app:name")
	if err != nil || !ok || v != "Raptor" {
		t.Errorf("v = %q, ok = %v, err = %v", v, ok, err)
	}
	if _, ok, err := d.Setting(context.Background(), "app:locale"); err != nil || ok {
		t.Errorf("a setting that was never set: ok = %v, err = %v", ok, err)
	}
	reads := st.reads
	for _, key := range []string{"", "app:debug", "database:password", "../app:name"} {
		if _, _, err := d.Setting(context.Background(), key); err == nil {
			t.Errorf("%q was read", key)
		}
	}
	if st.reads != reads {
		t.Error("the store was asked for a key that is not a setting")
	}
}

func TestDirectoryLists(t *testing.T) {
	d, _ := newDirectory(t)
	if us, err := d.Users(context.Background()); err != nil || len(us) != 2 {
		t.Errorf("users = %v, err = %v", us, err)
	}
	if ls, err := d.Locations(context.Background()); err != nil || len(ls) != 1 || ls[0].Nodes != 2 {
		t.Errorf("locations = %v, err = %v", ls, err)
	}
	if ns, err := d.Nodes(context.Background()); err != nil || len(ns) != 1 || ns[0].FQDN != "n1.example.com" {
		t.Errorf("nodes = %v, err = %v", ns, err)
	}
	if _, err := NewDirectory(nil); err == nil {
		t.Error("no store accepted")
	}
}

// Without the go-ahead nothing is removed, and the answer says how many keys would go.
func TestRevokeKeysWantsTheGoAhead(t *testing.T) {
	for kind, want := range map[string]int64{"": 3, "account": 2, "application": 1} {
		d, st := newDirectory(t)
		n, err := d.RevokeKeys(context.Background(), kind, false)
		var nc *NotConfirmedError
		if !errors.As(err, &nc) || nc.Count != want || n != 0 {
			t.Fatalf("kind %q: n = %d, err = %v", kind, n, err)
		}
		if len(st.kinds) != 0 || len(st.keys) != 3 {
			t.Errorf("kind %q: keys were removed (%d left, asked %v)", kind, len(st.keys), st.kinds)
		}
		if !strings.Contains(err.Error(), "--yes") {
			t.Errorf("the answer does not say how to go ahead: %v", err)
		}
	}
}

func TestRevokeKeys(t *testing.T) {
	d, st := newDirectory(t)
	n, err := d.RevokeKeys(context.Background(), "account", true)
	if err != nil || n != 2 || len(st.keys) != 1 || st.keys[0].Type != auth.KeyApplication {
		t.Fatalf("account keys: n = %d, err = %v, left %v", n, err, st.keys)
	}
	if len(st.kinds) != 1 || st.kinds[0] == nil || *st.kinds[0] != auth.KeyAccount {
		t.Errorf("asked %v", st.kinds)
	}

	n, err = d.RevokeKeys(context.Background(), "", true)
	if err != nil || n != 1 || len(st.keys) != 0 || st.kinds[1] != nil {
		t.Fatalf("every key: n = %d, err = %v, asked %v", n, err, st.kinds)
	}
	// Nothing left is not an error: the command can be run again.
	if n, err := d.RevokeKeys(context.Background(), "", true); err != nil || n != 0 {
		t.Errorf("again: n = %d, err = %v", n, err)
	}

	d, st = newDirectory(t)
	if n, err := d.RevokeKeys(context.Background(), "application", true); err != nil || n != 1 || len(st.keys) != 2 {
		t.Errorf("application keys: n = %d, err = %v", n, err)
	}
}

func TestRevokeKeysRefusesAKindItDoesNotKnow(t *testing.T) {
	for _, kind := range []string{"Account", "all", "ptlc", " account", "applications"} {
		d, st := newDirectory(t)
		if _, err := d.RevokeKeys(context.Background(), kind, true); err == nil || len(st.kinds) != 0 || len(st.keys) != 3 {
			t.Errorf("kind %q: err = %v, asked %v", kind, err, st.kinds)
		}
	}
}

func TestRevokeKeysPassesTheStoresErrorsOn(t *testing.T) {
	d, st := newDirectory(t)
	st.deleteErr = errBoom
	if _, err := d.RevokeKeys(context.Background(), "", true); !errors.Is(err, errBoom) {
		t.Errorf("err = %v", err)
	}
}
