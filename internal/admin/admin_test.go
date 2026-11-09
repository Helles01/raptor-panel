package admin

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/fielderr"
	"github.com/Helles01/raptor-panel/internal/nodes"
	"github.com/Helles01/raptor-panel/internal/users"
	"go.yaml.in/yaml/v3"
)

var errBoom = errors.New("the database is down: password=hunter2")

type store struct {
	users        map[string]User // by reference
	created      []users.Record
	keys         []NewKey
	keyCount     int
	owned        int64
	deleted      []uint64
	no2fa        []uint64
	locations    [][2]string
	nodes        []NewNode
	nests        [][4]string
	eggs         []NewEgg
	settings     map[string]string
	mailPassword string
	mailCleared  int
	twoFactor    int64
	resets       int
	passwords    map[uint64]string
	usersCount   int64
	allPasswords []string
	taken        string
	noLoc        bool
	noNest       bool
	fail         error
}

func (s *store) CreateUser(_ context.Context, u users.Record) (uint64, error) {
	if s.fail != nil {
		return 0, s.fail
	}
	if s.taken != "" {
		return 0, &TakenError{Field: s.taken}
	}
	s.created = append(s.created, u)
	return 40 + uint64(len(s.created)), nil
}

func (s *store) FindUser(_ context.Context, ref string) (User, error) {
	if s.fail != nil {
		return User{}, s.fail
	}
	if u, ok := s.users[strings.ToLower(ref)]; ok {
		return u, nil
	}
	return User{}, ErrNotFound
}

func (s *store) DeleteUser(_ context.Context, id uint64, check func(int64) error) error {
	if err := check(s.owned); err != nil {
		return err
	}
	s.deleted = append(s.deleted, id)
	return s.fail
}

func (s *store) DisableTwoFactor(_ context.Context, id uint64) error {
	s.no2fa = append(s.no2fa, id)
	return s.fail
}

func (s *store) CreateKey(_ context.Context, k NewKey, check func(int) error) error {
	if err := check(s.keyCount); err != nil {
		return err
	}
	s.keys = append(s.keys, k)
	return s.fail
}

func (s *store) CreateLocation(_ context.Context, short, long string) (uint64, error) {
	if s.taken == "short" {
		return 0, &TakenError{Field: "short"}
	}
	s.locations = append(s.locations, [2]string{short, long})
	return 3, s.fail
}

func (s *store) CreateNode(_ context.Context, n NewNode) (uint64, error) {
	if s.noLoc {
		return 0, ErrNotFound
	}
	s.nodes = append(s.nodes, n)
	return 5, s.fail
}

func (s *store) CreateNest(_ context.Context, uuid, name, description, author string) (uint64, error) {
	s.nests = append(s.nests, [4]string{uuid, name, description, author})
	return 6, s.fail
}

func (s *store) ImportEgg(_ context.Context, e NewEgg) (uint64, error) {
	if s.noNest {
		return 0, ErrNotFound
	}
	s.eggs = append(s.eggs, e)
	return 7, s.fail
}

func (s *store) SetSetting(_ context.Context, key, value string) error {
	if s.settings == nil {
		s.settings = map[string]string{}
	}
	s.settings[key] = value
	return s.fail
}

// twoFactor is the number of users who have a second factor, and resets counts the times they were all reset.
func (s *store) TwoFactorUsers(context.Context) (int64, error) {
	if s.fail != nil {
		return 0, s.fail
	}
	return s.twoFactor, nil
}

func (s *store) DisableAllTwoFactor(context.Context) (int64, error) {
	if s.fail != nil {
		return 0, s.fail
	}
	n := s.twoFactor
	s.twoFactor = 0
	s.resets++
	return n, nil
}

func (s *store) CountUsers(context.Context) (int64, error) { return s.usersCount, s.fail }

func (s *store) SetPassword(_ context.Context, id uint64, hash string) error {
	if s.fail != nil {
		return s.fail
	}
	if s.passwords == nil {
		s.passwords = map[uint64]string{}
	}
	s.passwords[id] = hash
	return nil
}

func (s *store) SetAllPasswords(_ context.Context, hash string) (int64, error) {
	if s.fail != nil {
		return 0, s.fail
	}
	s.allPasswords = append(s.allPasswords, hash)
	return s.usersCount, nil
}

func (s *store) SetMailPassword(_ context.Context, password string) error {

	if s.fail != nil {
		return s.fail
	}
	s.mailPassword = password
	return nil
}

func (s *store) ClearMailPassword(context.Context) error {
	s.mailCleared++
	return s.fail
}

func newService(t *testing.T) (*Service, *store) {
	t.Helper()
	st := &store{users: map[string]User{
		"ann":             {ID: 1, UUID: "u-1", Username: "ann", Email: "ann@example.com", Admin: true},
		"ann@example.com": {ID: 1, UUID: "u-1", Username: "ann", Email: "ann@example.com", Admin: true},
		"2":               {ID: 2, UUID: "u-2", Username: "ben", Email: "ben@example.com"},
		"ben":             {ID: 2, UUID: "u-2", Username: "ben", Email: "ben@example.com"},
	}}
	s, err := New(st, "https://panel.example.com", []string{"en", "hu"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, st
}

func TestNewNeedsAStoreAndAnAddress(t *testing.T) {
	if _, err := New(nil, "https://panel.example.com", nil, nil); err == nil {
		t.Error("no store")
	}
	if _, err := New(&store{}, "", nil, nil); err == nil {
		t.Error("no address")
	}
	if s, err := New(&store{}, "https://panel.example.com", nil, nil); err != nil || s.random == nil {
		t.Errorf("default random: %v", err)
	}
}

func TestCreateUser(t *testing.T) {
	s, st := newService(t)
	got, err := s.CreateUser(context.Background(), UserInput{Email: "new@example.com", Username: "NewUser", FirstName: "New", LastName: "User", Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	// A password that was not given is made, and given back once; the record holds its hash.
	rec := st.created[0]
	if got.ID != 41 || got.Username != "newuser" || got.UUID != rec.UUID || len(got.Password) != 24 || !auth.VerifyPassword(rec.PasswordHash, got.Password) || !rec.Admin || rec.Language != "en" {
		t.Errorf("created = %+v, record %+v", got, rec)
	}

	got, err = s.CreateUser(context.Background(), UserInput{Email: "pw@example.com", Username: "haspw", FirstName: "H", LastName: "P", Password: "a long and secret password", Language: "hu"})
	if err != nil || got.Password != "" || !auth.VerifyPassword(st.created[1].PasswordHash, "a long and secret password") || st.created[1].Language != "hu" {
		t.Errorf("with a password: %+v %v", got, err)
	}

	for name, in := range map[string]UserInput{
		"a bad address":      {Email: "nobody", Username: "someone", FirstName: "S", LastName: "O"},
		"a bad user name":    {Email: "a@example.com", Username: "a b", FirstName: "S", LastName: "O"},
		"a language unknown": {Email: "a@example.com", Username: "someone", FirstName: "S", LastName: "O", Language: "xx"},
		"a short password":   {Email: "a@example.com", Username: "someone", FirstName: "S", LastName: "O", Password: "short"},
	} {
		before := len(st.created)
		_, err := s.CreateUser(context.Background(), in)
		if _, ok := fielderr.Of(err); !ok || len(st.created) != before {
			t.Errorf("%s: %v", name, err)
		}
	}

	st.taken = "email"
	var te *TakenError
	if _, err := s.CreateUser(context.Background(), UserInput{Email: "a@example.com", Username: "someone", FirstName: "S", LastName: "O"}); !errors.As(err, &te) || te.Field != "email" || !strings.Contains(err.Error(), "email is already in use") {
		t.Errorf("taken: %v", err)
	}
	st.taken, st.fail = "", errBoom
	if _, err := s.CreateUser(context.Background(), UserInput{Email: "a@example.com", Username: "someone", FirstName: "S", LastName: "O"}); !errors.Is(err, errBoom) {
		t.Errorf("store fails: %v", err)
	}
}

func TestDeleteUserAndDisableTwoFactor(t *testing.T) {
	s, st := newService(t)
	u, err := s.DeleteUser(context.Background(), "ben")
	if err != nil || u.ID != 2 || !reflect.DeepEqual(st.deleted, []uint64{2}) {
		t.Errorf("delete: %+v %v", u, err)
	}
	// A user who owns servers is not deleted.
	st.owned = 2
	if _, err := s.DeleteUser(context.Background(), "ann"); !errors.Is(err, users.ErrHasServers) || len(st.deleted) != 1 {
		t.Errorf("owner: %v", err)
	}
	if _, err := s.DeleteUser(context.Background(), "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}

	u, err = s.DisableTwoFactor(context.Background(), "ANN@example.com")
	if err != nil || u.ID != 1 || !reflect.DeepEqual(st.no2fa, []uint64{1}) {
		t.Errorf("disable: %+v %v", u, err)
	}
	if _, err := s.DisableTwoFactor(context.Background(), "nobody"); !errors.Is(err, ErrNotFound) || len(st.no2fa) != 1 {
		t.Errorf("unknown: %v", err)
	}
}

func TestCreateKey(t *testing.T) {
	s, st := newService(t)
	k, err := s.CreateKey(context.Background(), KeyInput{User: "ann", Application: true, Description: "  billing  ", Access: []string{"servers=rw", "users=read"}, AllowedIPs: []string{"203.0.113.0/24"}})
	if err != nil {
		t.Fatal(err)
	}
	stored := st.keys[0]
	if !regexp.MustCompile(`^ptla_[A-Za-z0-9]{11}[A-Za-z0-9]{32}$`).MatchString(k.Token) || k.Token != k.Identifier+strings.TrimPrefix(k.Token, k.Identifier) || len(k.Identifier) != 16 {
		t.Fatalf("key = %+v", k)
	}
	secret := strings.TrimPrefix(k.Token, k.Identifier)
	if stored.UserID != 1 || stored.Type != auth.KeyApplication || stored.Identifier != k.Identifier || stored.SecretHash != auth.HashKeySecret(secret) || stored.Description != "billing" ||
		!reflect.DeepEqual(stored.AllowedIPs, []string{"203.0.113.0/24"}) {
		t.Errorf("stored = %+v", stored)
	}
	if stored.Access["servers"] != auth.AccessReadWrite || stored.Access["users"] != auth.AccessRead || stored.Access["nodes"] != auth.AccessNone {
		t.Errorf("access = %v", stored.Access)
	}
	// The secret itself is kept nowhere.
	if strings.Contains(stored.SecretHash, secret) {
		t.Error("the secret is in what is stored")
	}

	// A key of an account.
	k, err = s.CreateKey(context.Background(), KeyInput{User: "ben", Description: "mine"})
	if err != nil || !strings.HasPrefix(k.Token, "ptlc_") || st.keys[1].Type != auth.KeyAccount || st.keys[1].Access != nil {
		t.Errorf("account key: %+v %v", k, err)
	}
}

func TestCreateKeyRefusals(t *testing.T) {
	tests := []struct {
		name  string
		in    KeyInput
		count int
	}{
		{"no description", KeyInput{User: "ann"}, 0},
		{"a description with a control character", KeyInput{User: "ann", Description: "a\x00b"}, 0},
		{"a description too long", KeyInput{User: "ann", Description: strings.Repeat("a", 501)}, 0},
		{"a list of addresses that is wrong", KeyInput{User: "ann", Description: "x", AllowedIPs: []string{"not an address"}}, 0},
		{"a level that is wrong", KeyInput{User: "ann", Application: true, Description: "x", Access: []string{"servers=sometimes"}}, 0},
		{"a resource that is not one", KeyInput{User: "ann", Application: true, Description: "x", Access: []string{"stars=rw"}}, 0},
		{"levels on an account key", KeyInput{User: "ann", Description: "x", Access: []string{"servers=rw"}}, 0},
		{"an application key for someone who is no administrator", KeyInput{User: "ben", Application: true, Description: "x"}, 0},
		{"a user that is not there", KeyInput{User: "nobody", Description: "x"}, 0},
		{"too many keys", KeyInput{User: "ann", Description: "x"}, auth.MaxAccountKeys},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, st := newService(t)
			st.keyCount = tt.count
			if k, err := s.CreateKey(context.Background(), tt.in); err == nil || len(st.keys) != 0 || k.Token != "" {
				t.Errorf("key %+v, err %v", k, err)
			}
		})
	}
	s, st := newService(t)
	st.fail = errBoom
	if _, err := s.CreateKey(context.Background(), KeyInput{User: "ann", Description: "x"}); !errors.Is(err, errBoom) {
		t.Errorf("store fails: %v", err)
	}
}

func TestCreateLocation(t *testing.T) {
	s, st := newService(t)
	if id, err := s.CreateLocation(context.Background(), "us.nyc", "New York"); err != nil || id != 3 || !reflect.DeepEqual(st.locations, [][2]string{{"us.nyc", "New York"}}) {
		t.Errorf("location: %d %v %v", id, err, st.locations)
	}
	for _, short := range []string{"", strings.Repeat("a", 61), "a\x00b"} {
		if _, err := s.CreateLocation(context.Background(), short, ""); err == nil {
			t.Errorf("short %q accepted", short)
		}
	}
	if _, err := s.CreateLocation(context.Background(), "ok", strings.Repeat("a", 192)); err == nil {
		t.Error("a long name that is too long")
	}
	st.taken = "short"
	var te *TakenError
	if _, err := s.CreateLocation(context.Background(), "us.nyc", ""); !errors.As(err, &te) {
		t.Errorf("taken: %v", err)
	}
}

func TestCreateNode(t *testing.T) {
	s, st := newService(t)
	cfg := nodes.DefaultNewNode()
	cfg.Name, cfg.LocationID, cfg.FQDN, cfg.Memory, cfg.Disk = "node-1", 3, "node1.example.com", 8192, 100000
	got, err := s.CreateNode(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	n := st.nodes[0]
	if got.ID != 5 || got.UUID != n.UUID || n.Settings != cfg || len(n.Credentials.TokenID) != 16 || len(n.Credentials.Token) != 64 {
		t.Fatalf("created = %+v, stored %+v", got, n)
	}
	// The file is what Wings needs: the identity, the token the panel stored, and the address of the panel.
	var file map[string]any
	if err := yaml.Unmarshal(got.Config, &file); err != nil {
		t.Fatal(err)
	}
	if file["uuid"] != n.UUID || file["token_id"] != n.Credentials.TokenID || file["token"] != n.Credentials.Token || file["remote"] != "https://panel.example.com" {
		t.Errorf("config = %v", file)
	}

	bad := cfg
	bad.Memory = 0
	if _, err := s.CreateNode(context.Background(), bad); err == nil || len(st.nodes) != 1 {
		t.Errorf("bad node: %v", err)
	}
	st.noLoc = true
	if _, err := s.CreateNode(context.Background(), cfg); !errors.Is(err, ErrNotFound) {
		t.Errorf("location: %v", err)
	}
}

func TestCreateNest(t *testing.T) {
	s, st := newService(t)
	if id, err := s.CreateNest(context.Background(), " Minecraft ", "Servers", "support@example.com"); err != nil || id != 6 || st.nests[0][1] != "Minecraft" || st.nests[0][3] != "support@example.com" || len(st.nests[0][0]) != 36 {
		t.Errorf("nest: %d %v %v", id, err, st.nests)
	}
	for name, args := range map[string][3]string{
		"no name": {"", "d", "a@example.com"}, "a name too long": {strings.Repeat("a", 192), "d", "a@example.com"},
		"no author": {"n", "d", ""}, "an author that is no address": {"n", "d", "Parker"}, "a description too long": {"n", strings.Repeat("a", 10001), "a@example.com"},
	} {
		if _, err := s.CreateNest(context.Background(), args[0], args[1], args[2]); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

const paperEgg = `{
  "meta": {"version": "PTDL_v2", "update_url": null},
  "name": "Paper", "author": "parker@example.com", "description": "Minecraft Paper",
  "docker_images": {"Java 21": "ghcr.io/example/yolks:java_21"},
  "startup": "java -jar {{SERVER_JARFILE}}",
  "config": {"files": "{}", "startup": "{\"done\": \"ready\"}", "logs": "{}", "stop": "stop"},
  "scripts": {"installation": {"script": "echo install", "container": "alpine:3.4", "entrypoint": "ash"}},
  "variables": [{"name": "Server Jar File", "description": "jar", "env_variable": "SERVER_JARFILE", "default_value": "server.jar", "user_viewable": true, "user_editable": true, "rules": "required|string|max:20"}]
}`

func TestImportEgg(t *testing.T) {
	s, st := newService(t)
	got, err := s.ImportEgg(context.Background(), 6, strings.NewReader(paperEgg))
	if err != nil || got.ID != 7 || got.Name != "Paper" || got.Variables != 1 {
		t.Fatalf("imported = %+v %v", got, err)
	}
	e := st.eggs[0]
	if e.NestID != 6 || len(e.UUID) != 36 || e.Egg.Name != "Paper" || len(e.Egg.DockerImages) != 1 {
		t.Errorf("stored = %+v", e)
	}
	for name, file := range map[string]string{"not JSON": "x", "no version": `{"name":"x"}`, "an egg with a rule that cannot be applied": strings.Replace(paperEgg, "required|string|max:20", "", 1)} {
		before := len(st.eggs)
		if _, err := s.ImportEgg(context.Background(), 6, strings.NewReader(file)); err == nil || len(st.eggs) != before {
			t.Errorf("%s: %v", name, err)
		}
	}
	st.noNest = true
	if _, err := s.ImportEgg(context.Background(), 99, strings.NewReader(paperEgg)); !errors.Is(err, ErrNotFound) {
		t.Errorf("nest: %v", err)
	}
}

func TestSetSetting(t *testing.T) {
	s, st := newService(t)
	if err := s.SetSetting(context.Background(), "app:name", "Raptor"); err != nil || st.settings["app:name"] != "Raptor" {
		t.Errorf("set: %v %v", err, st.settings)
	}
	for key, value := range map[string]string{"app:secret_key": "x", "nonsense": "x", "app:name": "", "mail:from:address": "not an address", "app:name\x00": "x"} {
		if err := s.SetSetting(context.Background(), key, value); err == nil {
			t.Errorf("%q = %q accepted", key, value)
		}
	}
	if _, bad := st.settings["nonsense"]; bad || len(st.settings) != 1 {
		t.Errorf("settings = %v", st.settings)
	}
}

func TestSetMailPassword(t *testing.T) {
	s, st := newService(t)
	if err := s.SetMailPassword(context.Background(), "s3cret pass"); err != nil || st.mailPassword != "s3cret pass" {
		t.Fatalf("err = %v, stored %q", err, st.mailPassword)
	}
	// The limit counts characters, not bytes.
	for name, p := range map[string]string{"1000 letters": strings.Repeat("a", 1000), "1000 characters of two bytes": strings.Repeat("é", 1000)} {
		if err := s.SetMailPassword(context.Background(), p); err != nil || st.mailPassword != p {
			t.Errorf("%s: %v", name, err)
		}
	}

	st.mailPassword = "kept"
	for name, p := range map[string]string{
		"empty": "", "1001 letters": strings.Repeat("a", 1001), "line end": "pass\nword", "carriage return": "pass\rword", "NUL": "pass\x00word",
		"tab": "pass\tword", "DEL": "pass\x7fword", "escape": "pass\x1b[2Jword",
	} {
		err := s.SetMailPassword(context.Background(), p)
		if err == nil || st.mailPassword != "kept" {
			t.Errorf("%s: err = %v, stored %q", name, err, st.mailPassword)
			continue
		}
		// The password that was refused is not repeated in the error.
		if p != "" && strings.Contains(err.Error(), p) {
			t.Errorf("%s: the error repeats the password: %v", name, err)
		}
	}

	st.fail = errBoom
	if err := s.SetMailPassword(context.Background(), "fine"); !errors.Is(err, errBoom) {
		t.Errorf("a store that fails: %v", err)
	}
}

func TestClearMailPassword(t *testing.T) {
	s, st := newService(t)
	if err := s.ClearMailPassword(context.Background()); err != nil || st.mailCleared != 1 {
		t.Fatalf("err = %v, cleared %d", err, st.mailCleared)
	}
	st.fail = errBoom
	if err := s.ClearMailPassword(context.Background()); !errors.Is(err, errBoom) {
		t.Errorf("a store that fails: %v", err)
	}
}

func TestResetAllTwoFactor(t *testing.T) {
	s, st := newService(t)
	st.twoFactor = 5

	// Without the go-ahead nothing changes, and the answer says how many would lose theirs.
	n, err := s.ResetAllTwoFactor(context.Background(), false)
	var nc *TwoFactorNotConfirmedError
	if !errors.As(err, &nc) || nc.Count != 5 || n != 0 || st.resets != 0 || st.twoFactor != 5 {
		t.Fatalf("not confirmed: n = %d, err = %v, resets %d", n, err, st.resets)
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("the answer does not say how to go ahead: %v", err)
	}

	n, err = s.ResetAllTwoFactor(context.Background(), true)
	if err != nil || n != 5 || st.resets != 1 || st.twoFactor != 0 {
		t.Fatalf("confirmed: n = %d, err = %v, resets %d", n, err, st.resets)
	}
	// Run again there is nobody left, which is no error.
	if n, err := s.ResetAllTwoFactor(context.Background(), true); err != nil || n != 0 {
		t.Errorf("again: n = %d, err = %v", n, err)
	}

	st.fail = errBoom
	for _, confirmed := range []bool{false, true} {
		if _, err := s.ResetAllTwoFactor(context.Background(), confirmed); !errors.Is(err, errBoom) {
			t.Errorf("a store that fails (confirmed %v): %v", confirmed, err)
		}
	}
}

func TestSetPassword(t *testing.T) {
	ctx := context.Background()
	s, st := newService(t)

	// A password that is given is the one the user has afterwards, and nothing of it is returned.
	u, made, err := s.SetPassword(ctx, "ben", "a long enough password")
	if err != nil || made != "" || u.ID != 2 || !auth.VerifyPassword(st.passwords[2], "a long enough password") {
		t.Fatalf("given: user %+v, made %q, err %v, stored %q", u, made, err, st.passwords[2])
	}
	// A user is found by the address as well, and a password that is left out is made and returned once.
	u, made, err = s.SetPassword(ctx, "ann@example.com", "")
	if err != nil || u.ID != 1 || len(made) != generatedPasswordLength || !auth.VerifyPassword(st.passwords[1], made) {
		t.Fatalf("made: user %+v, made %q, err %v", u, made, err)
	}

	// What is refused changes nothing: a password that is too short or too long, a user that is not there, a store that fails.
	before := len(st.passwords)
	for name, tc := range map[string]struct{ ref, password string }{
		"too short": {"ben", "short"},
		"too long":  {"ben", strings.Repeat("x", auth.MaxPasswordBytes+1)},
		"nobody":    {"nobody", "a long enough password"},
	} {
		if _, _, err := s.SetPassword(ctx, tc.ref, tc.password); err == nil || len(st.passwords) != before {
			t.Errorf("%s: err = %v, passwords %d", name, err, len(st.passwords))
		}
	}
	st.fail = errBoom
	if _, _, err := s.SetPassword(ctx, "ben", "a long enough password"); !errors.Is(err, errBoom) {
		t.Errorf("a store that fails: %v", err)
	}
}

func TestResetAllPasswords(t *testing.T) {
	ctx := context.Background()
	s, st := newService(t)
	st.usersCount = 4

	// Without the go-ahead nothing changes, and the answer says how many would lose theirs and how to go ahead.
	n, err := s.ResetAllPasswords(ctx, false)
	var nc *PasswordsNotConfirmedError
	if !errors.As(err, &nc) || nc.Count != 4 || n != 0 || len(st.allPasswords) != 0 || !strings.Contains(err.Error(), "--yes") || !strings.Contains(err.Error(), "set-password") {
		t.Fatalf("not confirmed: n = %d, err = %v", n, err)
	}

	n, err = s.ResetAllPasswords(ctx, true)
	if err != nil || n != 4 || len(st.allPasswords) != 1 {
		t.Fatalf("confirmed: n = %d, err = %v, stored %d", n, err, len(st.allPasswords))
	}
	// What every account has is a hash like any other (so nothing else about the account looks wrong), of a text that is kept nowhere: it matches no password.
	hash := st.allPasswords[0]
	if !auth.ValidHash(hash) {
		t.Fatalf("not a hash the panel can check: %q", hash)
	}
	for _, guess := range []string{"", "password", "a long enough password", strings.Repeat("a", 48)} {
		if auth.VerifyPassword(hash, guess) {
			t.Errorf("%q matches", guess)
		}
	}
	// A second run makes another text, so two runs never leave the same hash.
	if _, err := s.ResetAllPasswords(ctx, true); err != nil || len(st.allPasswords) != 2 || st.allPasswords[1] == hash {
		t.Errorf("again: err = %v, hashes equal %v", err, len(st.allPasswords) == 2 && st.allPasswords[1] == hash)
	}

	st.fail = errBoom
	for _, confirmed := range []bool{false, true} {
		if _, err := s.ResetAllPasswords(ctx, confirmed); !errors.Is(err, errBoom) {
			t.Errorf("a store that fails (confirmed %v): %v", confirmed, err)
		}
	}
}
