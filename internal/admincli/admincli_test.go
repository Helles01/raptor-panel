package admincli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/Helles01/raptor-panel/internal/admin"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/users"
)

type store struct {
	users    map[string]admin.User
	created  []users.Record
	keys     []admin.NewKey
	deleted  []uint64
	no2fa    []uint64
	locs     [][2]string
	nodes    []admin.NewNode
	nests    [][4]string
	eggs     []admin.NewEgg
	settings map[string]string
	// mailPassword is what SetMailPassword was given last; mailCleared counts ClearMailPassword.
	mailPassword string
	mailCleared  int
	twoFactor    int64
	resets       int
}

func (s *store) CreateUser(_ context.Context, u users.Record) (uint64, error) {
	s.created = append(s.created, u)
	return 40 + uint64(len(s.created)), nil
}
func (s *store) FindUser(_ context.Context, ref string) (admin.User, error) {
	if u, ok := s.users[ref]; ok {
		return u, nil
	}
	return admin.User{}, admin.ErrNotFound
}
func (s *store) DeleteUser(_ context.Context, id uint64, check func(int64) error) error {
	s.deleted = append(s.deleted, id)
	return check(0)
}
func (s *store) DisableTwoFactor(_ context.Context, id uint64) error {
	s.no2fa = append(s.no2fa, id)
	return nil
}
func (s *store) CreateKey(_ context.Context, k admin.NewKey, check func(int) error) error {
	s.keys = append(s.keys, k)
	return check(0)
}
func (s *store) CreateLocation(_ context.Context, short, long string) (uint64, error) {
	s.locs = append(s.locs, [2]string{short, long})
	return 3, nil
}
func (s *store) CreateNode(_ context.Context, n admin.NewNode) (uint64, error) {
	s.nodes = append(s.nodes, n)
	return 5, nil
}
func (s *store) CreateNest(_ context.Context, uuid, name, description, author string) (uint64, error) {
	s.nests = append(s.nests, [4]string{uuid, name, description, author})
	return 6, nil
}
func (s *store) ImportEgg(_ context.Context, e admin.NewEgg) (uint64, error) {
	s.eggs = append(s.eggs, e)
	return 7, nil
}
func (s *store) TwoFactorUsers(context.Context) (int64, error) { return s.twoFactor, nil }

func (s *store) DisableAllTwoFactor(context.Context) (int64, error) {
	n := s.twoFactor
	s.twoFactor = 0
	s.resets++
	return n, nil
}

func (s *store) SetMailPassword(_ context.Context, password string) error {
	s.mailPassword = password
	return nil
}

func (s *store) ClearMailPassword(context.Context) error {
	s.mailCleared++
	return nil
}

func (s *store) SetSetting(_ context.Context, key, value string) error {
	if s.settings == nil {
		s.settings = map[string]string{}
	}
	s.settings[key] = value
	return nil
}

type run struct {
	t      *testing.T
	st     *store
	cmds   map[string]map[string]Handler
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func newRun(t *testing.T) *run {
	t.Helper()
	st := &store{users: map[string]admin.User{"ann": {ID: 1, UUID: "u-1", Username: "ann", Email: "ann@example.com", Admin: true}, "ben": {ID: 2, Username: "ben"}}}
	svc, err := admin.New(st, "https://panel.example.com", []string{"en", "hu"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &run{t: t, st: st, cmds: Commands(svc)}
}

func (r *run) do(stdin string, group, verb string, args ...string) error {
	r.t.Helper()
	r.stdout.Reset()
	r.stderr.Reset()
	return r.cmds[group][verb](context.Background(), Env{Stdin: strings.NewReader(stdin), Stdout: &r.stdout, Stderr: &r.stderr}, args)
}

func isUsage(err error) bool { var u UsageError; return errors.As(err, &u) }

func TestTheCommandsThereAre(t *testing.T) {
	cmds := newRun(t).cmds
	got := map[string][]string{}
	for g, verbs := range cmds {
		for v := range verbs {
			got[g] = append(got[g], v)
		}
		sort.Strings(got[g])
	}
	want := map[string][]string{
		"user": {"create", "delete", "disable-2fa", "reset-2fa"}, "apikey": {"create"}, "location": {"create"}, "node": {"create"}, "nest": {"create"}, "egg": {"import"}, "settings": {"list", "set", "set-mail-password"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %v", got)
	}
}

func TestUserCreateMakesAPasswordAndShowsItOnce(t *testing.T) {
	r := newRun(t)
	if err := r.do("", "user", "create", "--email", "new@example.com", "--username", "NewUser", "--first-name", "New", "--last-name", "User", "--admin"); err != nil {
		t.Fatal(err)
	}
	pw := strings.TrimSpace(r.stdout.String())
	rec := r.st.created[0]
	if len(pw) != 24 || !auth.VerifyPassword(rec.PasswordHash, pw) || !rec.Admin || rec.Username != "newuser" {
		t.Errorf("password %q, record %+v", pw, rec)
	}
	// What the command says of itself goes to the other stream, and never carries the password.
	if !strings.Contains(r.stderr.String(), "Created user newuser (id 41") || strings.Contains(r.stderr.String(), pw) || strings.Count(r.stdout.String(), "\n") != 1 {
		t.Errorf("stderr %q, stdout %q", r.stderr.String(), r.stdout.String())
	}
}

func TestUserCreateReadsAPasswordFromTheStandardInput(t *testing.T) {
	r := newRun(t)
	args := []string{"--email", "pw@example.com", "--username", "haspw", "--first-name", "H", "--last-name", "P", "--password-stdin"}
	if err := r.do("a long and secret password\r\nignored\n", "user", "create", args...); err != nil {
		t.Fatal(err)
	}
	if !auth.VerifyPassword(r.st.created[0].PasswordHash, "a long and secret password") || r.stdout.Len() != 0 || strings.Contains(r.stderr.String(), "secret") {
		t.Errorf("stdout %q, stderr %q", r.stdout.String(), r.stderr.String())
	}
	// The last line need not end in a line break.
	if err := r.do("a long and secret password", "user", "create", args...); err != nil {
		t.Errorf("no line break: %v", err)
	}
	for _, in := range []string{"", "\n"} {
		before := len(r.st.created)
		err := r.do(in, "user", "create", args...)
		if in == "" && (err == nil || len(r.st.created) != before) {
			t.Errorf("empty input: %v", err)
		}
		if in == "\n" && (err == nil || len(r.st.created) != before) {
			t.Errorf("an empty line: %v", err)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	r := newRun(t)
	for name, c := range map[string]struct {
		group, verb string
		args        []string
	}{
		"no flags":             {"user", "create", nil},
		"a flag missing":       {"user", "create", []string{"--email", "a@example.com", "--username", "a", "--first-name", "A"}},
		"a flag that is none":  {"user", "create", []string{"--password", "x"}},
		"an argument too many": {"user", "create", []string{"--email", "a@example.com", "--username", "a", "--first-name", "A", "--last-name", "B", "extra"}},
		"help":                 {"user", "create", []string{"-h"}},
		"delete without one":   {"user", "delete", nil},
		"delete with two":      {"user", "delete", []string{"a", "b"}},
		"2fa without one":      {"user", "disable-2fa", nil},
		"key without a user":   {"apikey", "create", []string{"--description", "x"}},
		"key without text":     {"apikey", "create", []string{"--user", "ann"}},
		"location without it":  {"location", "create", nil},
		"node without a field": {"node", "create", []string{"--name", "n"}},
		"nest without author":  {"nest", "create", []string{"--name", "n"}},
		"egg without a nest":   {"egg", "import", []string{"file.json"}},
		"egg without a file":   {"egg", "import", []string{"--nest-id", "1"}},
		"set with one":         {"settings", "set", []string{"app:name"}},
		"list with one":        {"settings", "list", []string{"x"}},
	} {
		if err := r.do("", c.group, c.verb, c.args...); !isUsage(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := r.do("", "user", "create", "-h"); !strings.Contains(err.Error(), "usage: raptor user create") || !strings.Contains(err.Error(), "-email") {
		t.Errorf("the usage = %v", err)
	}
	if len(r.st.created)+len(r.st.keys)+len(r.st.deleted)+len(r.st.locs) != 0 {
		t.Error("something was made")
	}
}

func TestDeleteAndDisableTwoFactor(t *testing.T) {
	r := newRun(t)
	if err := r.do("", "user", "delete", "ben"); err != nil || !reflect.DeepEqual(r.st.deleted, []uint64{2}) || !strings.Contains(r.stderr.String(), "Deleted user ben") {
		t.Errorf("delete: %v %q", err, r.stderr.String())
	}
	if err := r.do("", "user", "disable-2fa", "ann"); err != nil || !reflect.DeepEqual(r.st.no2fa, []uint64{1}) || !strings.Contains(r.stderr.String(), "off for ann") {
		t.Errorf("disable: %v %q", err, r.stderr.String())
	}
	for _, verb := range []string{"delete", "disable-2fa"} {
		if err := r.do("", "user", verb, "nobody"); !errors.Is(err, admin.ErrNotFound) || isUsage(err) {
			t.Errorf("%s of nobody: %v", verb, err)
		}
	}
}

func TestApikeyCreate(t *testing.T) {
	r := newRun(t)
	if err := r.do("", "apikey", "create", "--user", "ann", "--application", "--description", "billing", "--access", "servers=rw", "--access", "users=read", "--allow-ip", "203.0.113.0/24", "--allow-ip", "198.51.100.7"); err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(r.stdout.String())
	k := r.st.keys[0]
	if !strings.HasPrefix(token, "ptla_") || !strings.HasPrefix(token, k.Identifier) || k.Type != auth.KeyApplication || k.Access["servers"] != auth.AccessReadWrite || k.Access["users"] != auth.AccessRead ||
		!reflect.DeepEqual(k.AllowedIPs, []string{"203.0.113.0/24", "198.51.100.7"}) || k.Description != "billing" {
		t.Errorf("token %q, key %+v", token, k)
	}
	// The identifier is public and may be said on the other stream; the secret half is on the output only.
	secret := strings.TrimPrefix(token, k.Identifier)
	if !strings.Contains(r.stderr.String(), k.Identifier) || strings.Contains(r.stderr.String(), secret) || k.SecretHash != auth.HashKeySecret(secret) {
		t.Errorf("stderr %q", r.stderr.String())
	}
	// A key of an account.
	if err := r.do("", "apikey", "create", "--user", "ben", "--description", "mine"); err != nil || !strings.HasPrefix(r.stdout.String(), "ptlc_") {
		t.Errorf("account key: %v %q", err, r.stdout.String())
	}
	if err := r.do("", "apikey", "create", "--user", "ben", "--description", "x", "--application"); err == nil || isUsage(err) || r.stdout.Len() != 0 {
		t.Errorf("an application key for a user who is no administrator: %v", err)
	}
}

func TestLocationAndNest(t *testing.T) {
	r := newRun(t)
	if err := r.do("", "location", "create", "--short", "eu.ams", "--long", "Amsterdam"); err != nil || !reflect.DeepEqual(r.st.locs, [][2]string{{"eu.ams", "Amsterdam"}}) || !strings.Contains(r.stderr.String(), "Created location eu.ams (id 3)") {
		t.Errorf("location: %v %v %q", err, r.st.locs, r.stderr.String())
	}
	if err := r.do("", "location", "create", "--short", ""); err == nil || isUsage(err) {
		t.Errorf("a short name of nothing: %v", err)
	}
	if err := r.do("", "nest", "create", "--name", " Minecraft ", "--author", "support@example.com", "--description", "Servers"); err != nil || r.st.nests[0][1] != "Minecraft" || !strings.Contains(r.stderr.String(), "Created nest Minecraft (id 6)") {
		t.Errorf("nest: %v %v %q", err, r.st.nests, r.stderr.String())
	}
	if err := r.do("", "nest", "create", "--name", "x", "--author", "nobody"); err == nil || isUsage(err) {
		t.Errorf("a bad author: %v", err)
	}
}

func TestNodeCreate(t *testing.T) {
	r := newRun(t)
	if err := r.do("", "node", "create", "--name", "node-1", "--location-id", "3", "--fqdn", "node1.example.com", "--memory", "8192", "--disk", "100000", "--memory-overallocate", "-1",
		"--sftp-port", "2222", "--public=false", "--behind-proxy"); err != nil {
		t.Fatal(err)
	}
	n := r.st.nodes[0]
	s := n.Settings
	if s.Name != "node-1" || s.LocationID != 3 || s.Memory != 8192 || s.Disk != 100000 || s.MemoryOverallocate != -1 || s.DaemonSFTP != 2222 || s.Public || !s.BehindProxy || s.Scheme != "https" || s.DaemonListen != 8080 {
		t.Errorf("settings = %+v", s)
	}
	// The standard output is the file the node needs, token included, and only that.
	var file map[string]any
	if err := yaml.Unmarshal(r.stdout.Bytes(), &file); err != nil {
		t.Fatalf("stdout is not YAML: %v\n%s", err, r.stdout.String())
	}
	if file["uuid"] != n.UUID || file["token"] != n.Credentials.Token || file["remote"] != "https://panel.example.com" {
		t.Errorf("config = %v", file)
	}
	if strings.Contains(r.stderr.String(), n.Credentials.Token) || !strings.Contains(r.stderr.String(), "Created node node-1 (id 5") {
		t.Errorf("stderr = %q", r.stderr.String())
	}
	// Whatever the rules refuse is said, and nothing is printed.
	if err := r.do("", "node", "create", "--name", "n", "--location-id", "1", "--fqdn", "x.example.com", "--memory", "0", "--disk", "10"); err == nil || isUsage(err) || r.stdout.Len() != 0 {
		t.Errorf("a memory of nothing: %v", err)
	}
}

const egg = `{"meta":{"version":"PTDL_v2","update_url":null},"name":"Paper","author":"parker@example.com","description":"x","docker_images":{"Java 21":"ghcr.io/example/yolks:java_21"},
"startup":"java -jar {{SERVER_JARFILE}}","config":{"files":"{}","startup":"{\"done\": \"ready\"}","logs":"{}","stop":"stop"},
"scripts":{"installation":{"script":"echo","container":"alpine:3.4","entrypoint":"ash"}},
"variables":[{"name":"Jar","description":"","env_variable":"SERVER_JARFILE","default_value":"server.jar","user_viewable":true,"user_editable":true,"rules":"required|string"}]}`

type file struct{ io.Reader }

func (file) Close() error { return nil }

func TestEggImport(t *testing.T) {
	r := newRun(t)
	r.cmds = Commands(mustService(t, r.st))
	var opened string
	open := func(p string) (io.ReadCloser, error) { opened = p; return file{strings.NewReader(egg)}, nil }
	run := func(stdin string, openFn func(string) (io.ReadCloser, error), args ...string) error {
		r.stdout.Reset()
		r.stderr.Reset()
		return r.cmds["egg"]["import"](context.Background(), Env{Stdin: strings.NewReader(stdin), Stdout: &r.stdout, Stderr: &r.stderr, Open: openFn}, args)
	}
	if err := run("", open, "--nest-id", "6", "paper.json"); err != nil || opened != "paper.json" || r.st.eggs[0].NestID != 6 || !strings.Contains(r.stderr.String(), "Imported the egg Paper (id 7) with 1 variable(s)") {
		t.Errorf("file: %v opened %q, stderr %q", err, opened, r.stderr.String())
	}
	opened = ""
	if err := run(egg, open, "--nest-id", "6", "-"); err != nil || opened != "" || len(r.st.eggs) != 2 {
		t.Errorf("stdin: %v opened %q", err, opened)
	}
	if err := run("", func(string) (io.ReadCloser, error) { return nil, errors.New("no such file") }, "--nest-id", "6", "x.json"); err == nil || isUsage(err) || !strings.Contains(err.Error(), "open the egg file") {
		t.Errorf("open fails: %v", err)
	}
	if err := run("not an egg", open, "--nest-id", "6", "-"); err == nil || isUsage(err) || len(r.st.eggs) != 2 {
		t.Errorf("a bad egg: %v", err)
	}
	// Without an opener the real files are used.
	if err := run("", nil, "--nest-id", "6", "this-file-is-not-there.json"); err == nil || !strings.Contains(err.Error(), "open the egg file") {
		t.Errorf("a file that is not there: %v", err)
	}
}

func mustService(t *testing.T, st *store) *admin.Service {
	t.Helper()
	s, err := admin.New(st, "https://panel.example.com", []string{"en"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSettings(t *testing.T) {
	r := newRun(t)
	if err := r.do("", "settings", "set", "app:name", "Raptor"); err != nil || r.st.settings["app:name"] != "Raptor" || !strings.Contains(r.stderr.String(), "Set app:name") {
		t.Errorf("set: %v %v", err, r.st.settings)
	}
	if err := r.do("", "settings", "set", "nonsense", "x"); err == nil || isUsage(err) {
		t.Errorf("an unknown setting: %v", err)
	}
	if err := r.do("", "settings", "list"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(r.stdout.String()), "\n")
	if !sort.StringsAreSorted(lines) || len(lines) < 5 || !strings.Contains(r.stdout.String(), "app:name\n") || strings.Contains(r.stdout.String(), "password") {
		t.Errorf("list = %v", lines)
	}
}

func TestSettingsSetMailPassword(t *testing.T) {
	const secret = "Sm7p-pass w0rd"
	r := newRun(t)
	if err := r.do(secret+"\nignored second line\n", "settings", "set-mail-password", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	// Only the first line is the password, and it is nowhere in what the command says.
	if r.st.mailPassword != secret {
		t.Errorf("stored %q", r.st.mailPassword)
	}
	if strings.Contains(r.stdout.String()+r.stderr.String(), secret) || r.stdout.Len() != 0 || len(r.st.settings) != 0 {
		t.Errorf("stdout %q, stderr %q, settings %v", r.stdout.String(), r.stderr.String(), r.st.settings)
	}

	// The end of the line is not part of it, with either kind of line end.
	if err := r.do("windows-line\r\n", "settings", "set-mail-password", "--password-stdin"); err != nil || r.st.mailPassword != "windows-line" {
		t.Errorf("err = %v, stored %q", err, r.st.mailPassword)
	}

	if err := r.do("", "settings", "set-mail-password", "--clear"); err != nil || r.st.mailCleared != 1 || r.stdout.Len() != 0 {
		t.Errorf("clear: err = %v, cleared %d", err, r.st.mailCleared)
	}
}

func TestSettingsSetMailPasswordRefusesWhatItCannotDo(t *testing.T) {
	for name, tc := range map[string]struct {
		stdin string
		args  []string
		usage bool
	}{
		"neither flag":                {"pw\n", nil, true},
		"both flags":                  {"pw\n", []string{"--password-stdin", "--clear"}, true},
		"the password as an argument": {"", []string{"hunter2"}, true},
		"a flag nobody knows":         {"pw\n", []string{"--password-stdin", "--password", "hunter2"}, true},
		"nothing on the input":        {"", []string{"--password-stdin"}, false},
		"an empty line":               {"\n", []string{"--password-stdin"}, false},
		"a control character":         {"pass\x1bword\n", []string{"--password-stdin"}, false},
	} {
		r := newRun(t)
		err := r.do(tc.stdin, "settings", "set-mail-password", tc.args...)
		if err == nil || isUsage(err) != tc.usage {
			t.Errorf("%s: err = %v", name, err)
		}
		if r.st.mailPassword != "" || r.st.mailCleared != 0 {
			t.Errorf("%s: something was done", name)
		}
		if strings.Contains(r.stdout.String()+r.stderr.String()+fmt.Sprint(err), "hunter2") {
			t.Errorf("%s: the password is repeated", name)
		}
	}
}

func TestUserReset2FAWantsYes(t *testing.T) {
	r := newRun(t)
	r.st.twoFactor = 7
	err := r.do("", "user", "reset-2fa", "--all")
	var nc *admin.TwoFactorNotConfirmedError
	if !errors.As(err, &nc) || nc.Count != 7 || isUsage(err) || r.st.resets != 0 || r.st.twoFactor != 7 || r.stdout.Len() != 0 {
		t.Fatalf("err = %v, resets %d", err, r.st.resets)
	}

	if err := r.do("", "user", "reset-2fa", "--all", "--yes"); err != nil || r.st.resets != 1 || r.st.twoFactor != 0 {
		t.Fatalf("err = %v, resets %d", err, r.st.resets)
	}
	if !strings.Contains(r.stderr.String(), "Removed the second factor of 7 user(s)") || !strings.Contains(r.stderr.String(), "pterodactyl:auth:2fa_required") || r.stdout.Len() != 0 {
		t.Errorf("stderr = %q, stdout = %q", r.stderr.String(), r.stdout.String())
	}
	if _, set := r.st.settings["pterodactyl:auth:2fa_required"]; set {
		t.Error("the requirement was changed without --require")
	}
}

func TestUserReset2FARequire(t *testing.T) {
	for require, want := range map[string]string{"none": "0", "admins": "1", "everyone": "2"} {
		r := newRun(t)
		r.st.twoFactor = 2
		if err := r.do("", "user", "reset-2fa", "--all", "--yes", "--require", require); err != nil {
			t.Fatalf("%s: %v", require, err)
		}
		if r.st.settings["pterodactyl:auth:2fa_required"] != want || !strings.Contains(r.stderr.String(), "required of: "+require) {
			t.Errorf("%s: setting %q, stderr %q", require, r.st.settings["pterodactyl:auth:2fa_required"], r.stderr.String())
		}
	}
}

func TestUserReset2FARefusesWhatItCannotDo(t *testing.T) {
	for name, args := range map[string][]string{
		"without --all":                 {"--yes"},
		"nothing at all":                {},
		"a user instead of --all":       {"ann"},
		"--require with a word unknown": {"--all", "--yes", "--require", "sometimes"},
		"--require without --yes":       {"--all", "--require", "everyone"},
		"a flag nobody knows":           {"--all", "--yes", "--force"},
	} {
		r := newRun(t)
		r.st.twoFactor = 3
		if err := r.do("", "user", "reset-2fa", args...); !isUsage(err) {
			t.Errorf("%s: err = %v", name, err)
		}
		if r.st.resets != 0 || r.st.twoFactor != 3 || len(r.st.settings) != 0 {
			t.Errorf("%s: something was changed", name)
		}
	}
}
