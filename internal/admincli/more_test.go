package admincli

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/admin"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/dbhosts"
)

const hostSecret = "Host-acc0unt-pw"

type hostStore struct {
	hosts     map[uint64]admin.HostRecord
	databases map[uint64]int64
	failStore error
}

func (s *hostStore) CreateHost(_ context.Context, h admin.HostRecord) (uint64, error) {
	h.ID = uint64(len(s.hosts) + 10)
	s.hosts[h.ID] = h
	return h.ID, nil
}

func (s *hostStore) ListHosts(context.Context) ([]admin.HostInfo, error) {
	var out []admin.HostInfo
	for _, h := range s.hosts {
		out = append(out, admin.HostInfo{ID: h.ID, Name: h.Name, Host: h.Host, Port: h.Port, Username: h.Username, NodeID: h.NodeID, Databases: s.databases[h.ID]})
	}
	// Largest id first: the command has to put them in order itself.
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func (s *hostStore) FindHost(_ context.Context, id uint64) (admin.HostRecord, error) {
	h, ok := s.hosts[id]
	if !ok {
		return admin.HostRecord{}, admin.ErrNotFound
	}
	return h, nil
}

func (s *hostStore) ChangeHost(_ context.Context, id uint64, change func(admin.HostRecord) (admin.HostRecord, error)) error {
	h, ok := s.hosts[id]
	if !ok {
		return admin.ErrNotFound
	}
	changed, err := change(h)
	if err != nil {
		return err
	}
	if s.failStore != nil {
		return s.failStore
	}
	changed.ID = id
	s.hosts[id] = changed
	return nil
}

func (s *hostStore) DeleteHost(_ context.Context, id uint64, check func(int64) error) error {
	if _, ok := s.hosts[id]; !ok {
		return admin.ErrNotFound
	}
	if err := check(s.databases[id]); err != nil {
		return err
	}
	delete(s.hosts, id)
	return nil
}

type hostAccess struct {
	tested []dbhosts.Conn
	ran    [][]dbhosts.Statement
	err    error
}

func (a *hostAccess) Test(_ context.Context, c dbhosts.Conn) error {
	a.tested = append(a.tested, c)
	return a.err
}

func (a *hostAccess) RunOn(_ context.Context, _ dbhosts.Conn, st []dbhosts.Statement) error {
	a.ran = append(a.ran, st)
	return a.err
}

type dirStore struct {
	users    []admin.User
	keys     []admin.KeyInfo
	revoked  []string
	askedFor []*uint64
	settings map[string]string
}

func (s *dirStore) ListUsers(context.Context) ([]admin.User, error) { return s.users, nil }
func (s *dirStore) FindUser(_ context.Context, ref string) (admin.User, error) {
	for _, u := range s.users {
		if u.Username == ref {
			return u, nil
		}
	}
	return admin.User{}, admin.ErrNotFound
}
func (s *dirStore) ListKeys(_ context.Context, id *uint64) ([]admin.KeyInfo, error) {
	s.askedFor = append(s.askedFor, id)
	var out []admin.KeyInfo
	for _, k := range s.keys {
		if id == nil || k.UserID == *id {
			out = append(out, k)
		}
	}
	return out, nil
}
func (s *dirStore) DeleteKey(_ context.Context, identifier string) (admin.KeyInfo, error) {
	for _, k := range s.keys {
		if k.Identifier == identifier {
			s.revoked = append(s.revoked, identifier)
			return k, nil
		}
	}
	return admin.KeyInfo{}, admin.ErrNotFound
}
func (s *dirStore) ListLocations(context.Context) ([]admin.LocationInfo, error) {
	return []admin.LocationInfo{{ID: 2, Short: "us", Long: "United\x1b[31m States", Nodes: 1}, {ID: 1, Short: "eu", Long: "Europe", Nodes: 3}}, nil
}
func (s *dirStore) ListNodes(context.Context) ([]admin.NodeInfo, error) {
	return []admin.NodeInfo{{ID: 4, UUID: "u-4", Name: "n1", FQDN: "n1.example.com", Scheme: "https", Port: 8080, LocationID: 1, Servers: 7}}, nil
}
func (s *dirStore) Setting(_ context.Context, key string) (string, bool, error) {
	v, ok := s.settings[key]
	return v, ok, nil
}

type moreRun struct {
	t      *testing.T
	hosts  *hostStore
	access *hostAccess
	dir    *dirStore
	cmds   map[string]map[string]Handler
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func newMoreRun(t *testing.T) *moreRun {
	t.Helper()
	node := uint64(1)
	r := &moreRun{
		t: t,
		hosts: &hostStore{
			hosts:     map[uint64]admin.HostRecord{3: {ID: 3, Name: "main", Host: "db.example.com", Port: 3306, Username: "panel", Password: hostSecret, NodeID: &node}},
			databases: map[uint64]int64{},
		},
		access: &hostAccess{},
		dir: &dirStore{
			users: []admin.User{{ID: 2, Username: "bob", Email: "bob@example.com", UUID: "u-2"}, {ID: 1, Username: "ada", Email: "ada@example.com", UUID: "u-1", Admin: true}},
			keys: []admin.KeyInfo{
				{Identifier: "ptlc_abcdefghijk", UserID: 1, Username: "ada", Type: auth.KeyAccount, Description: "my\x1b[2Jkey"},
				{Identifier: "ptla_ABCDEFGHIJK", UserID: 1, Username: "ada", Type: auth.KeyApplication, Description: "deploy", AllowedIPs: []string{"10.0.0.0/8", "192.0.2.1"},
					Access: map[string]auth.Access{"servers": auth.AccessReadWrite, "users": auth.AccessRead}, LastUsedAt: ptr(time.Date(2026, 11, 1, 10, 30, 0, 0, time.UTC))},
				{Identifier: "ptlc_01234567890", UserID: 2, Username: "bob", Type: auth.KeyAccount, Description: "bob's"},
				{Identifier: "ptla_NOACCESSKEY", UserID: 2, Username: "bob", Type: auth.KeyApplication, Description: "empty", Access: map[string]auth.Access{"nests": auth.AccessNone}},
				{Identifier: "ptlc_evilevilevi", UserID: 3, Username: "mal[0mory", Type: auth.KeyAccount},
			},
			settings: map[string]string{"app:name": "Raptor\x1b[1m"},
		},
	}
	hs, err := admin.NewHostService(r.hosts, r.access, nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := admin.NewDirectory(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	r.cmds = All(Services{Hosts: hs, Directory: d})
	return r
}

func ptr[T any](v T) *T { return &v }

func (r *moreRun) do(stdin, group, verb string, args ...string) error {
	r.t.Helper()
	r.stdout.Reset()
	r.stderr.Reset()
	h, ok := r.cmds[group][verb]
	if !ok {
		r.t.Fatalf("no command %s %s", group, verb)
	}
	return h(context.Background(), Env{Stdin: strings.NewReader(stdin), Stdout: &r.stdout, Stderr: &r.stderr}, args)
}

func TestAllMergesTheParts(t *testing.T) {
	names := func(m map[string]map[string]Handler) map[string][]string {
		out := map[string][]string{}
		for g, verbs := range m {
			for v := range verbs {
				out[g] = append(out[g], v)
			}
			sort.Strings(out[g])
		}
		return out
	}
	svc := mustService(t, &store{})
	hs, _ := admin.NewHostService(&hostStore{}, &hostAccess{}, nil)
	dir, _ := admin.NewDirectory(&dirStore{})

	want := map[string][]string{
		"user": {"create", "delete", "disable-2fa", "list"}, "apikey": {"create", "list", "revoke"}, "location": {"create", "list"},
		"node": {"create", "list"}, "nest": {"create"}, "egg": {"import"}, "settings": {"get", "list", "set"},
		"dbhost": {"create", "delete", "list", "rotate-password", "test", "update"},
	}
	if got := names(All(Services{Admin: svc, Hosts: hs, Directory: dir})); !reflect.DeepEqual(got, want) {
		t.Errorf("all = %v", got)
	}
	if got := names(All(Services{})); len(got) != 0 {
		t.Errorf("nothing gave %v", got)
	}
	if got := names(All(Services{Hosts: hs})); !reflect.DeepEqual(got, map[string][]string{"dbhost": want["dbhost"]}) {
		t.Errorf("hosts only gave %v", got)
	}
	if got := names(All(Services{Admin: svc})); got["dbhost"] != nil || got["apikey"] == nil {
		t.Errorf("admin only gave %v", got)
	}
}

func TestDBHostCreate(t *testing.T) {
	r := newMoreRun(t)
	if err := r.do(hostSecret+"\n", "dbhost", "create", "--name", "second", "--host", "10.0.0.7", "--port", "3307", "--username", "panel", "--node-id", "1", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	var got admin.HostRecord
	for id, h := range r.hosts.hosts {
		if id != 3 {
			got = h
		}
	}
	if got.Name != "second" || got.Host != "10.0.0.7" || got.Port != 3307 || got.Password != hostSecret || got.NodeID == nil || *got.NodeID != 1 {
		t.Errorf("stored %+v", got)
	}
	if r.stdout.Len() != 0 || !strings.Contains(r.stderr.String(), "Created database host second") || strings.Contains(r.stderr.String(), hostSecret) {
		t.Errorf("stdout %q, stderr %q", r.stdout.String(), r.stderr.String())
	}

	// No password at all is a decision that has to be made out loud.
	r = newMoreRun(t)
	if err := r.do("", "dbhost", "create", "--name", "local", "--host", "127.0.0.1", "--username", "panel", "--no-password"); err != nil {
		t.Fatal(err)
	}
	if len(r.access.tested) != 1 || r.access.tested[0].Password != "" || r.access.tested[0].Port != 3306 {
		t.Errorf("tested %v", r.access.tested)
	}
}

func TestDBHostCreateUsageErrors(t *testing.T) {
	base := []string{"--name", "n", "--host", "h", "--username", "u"}
	for name, tc := range map[string]struct {
		stdin string
		args  []string
	}{
		"no password choice":    {"", base},
		"both password choices": {"x\n", append([]string{"--password-stdin", "--no-password"}, base...)},
		"no name":               {"", []string{"--host", "h", "--username", "u", "--no-password"}},
		"no host":               {"", []string{"--name", "n", "--username", "u", "--no-password"}},
		"no username":           {"", []string{"--name", "n", "--host", "h", "--no-password"}},
		"stray argument":        {"", append([]string{"--no-password", "extra"}, base...)},
	} {
		r := newMoreRun(t)
		if err := r.do(tc.stdin, "dbhost", "create", tc.args...); !isUsage(err) || len(r.hosts.hosts) != 1 {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	r := newMoreRun(t)
	if err := r.do("\n", "dbhost", "create", append([]string{"--password-stdin"}, base...)...); err == nil || isUsage(err) || len(r.hosts.hosts) != 1 {
		t.Errorf("an empty line was taken as the password: %v", err)
	}
}

func TestDBHostCreateNeedsAHostThatAnswers(t *testing.T) {
	r := newMoreRun(t)
	r.access.err = &dbhosts.HostError{Step: 0, Number: 1045}
	err := r.do(hostSecret+"\n", "dbhost", "create", "--name", "n", "--host", "h", "--username", "u", "--password-stdin")
	if err == nil || len(r.hosts.hosts) != 1 || strings.Contains(err.Error(), hostSecret) {
		t.Errorf("err = %v, hosts %d", err, len(r.hosts.hosts))
	}
}

func TestDBHostList(t *testing.T) {
	r := newMoreRun(t)
	r.hosts.hosts[7] = admin.HostRecord{ID: 7, Name: "ev\x1b[31mil\tname", Host: "10.0.0.7", Port: 3307, Username: "p"}
	r.hosts.databases[3] = 4
	if err := r.do("", "dbhost", "list"); err != nil {
		t.Fatal(err)
	}
	out := r.stdout.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "ID") || !strings.Contains(lines[0], "DATABASES") {
		t.Fatalf("out = %q", out)
	}
	if !strings.HasPrefix(lines[1], "3") || !strings.Contains(lines[1], "db.example.com:3306") || !strings.HasSuffix(strings.TrimSpace(lines[1]), "4") || !strings.HasPrefix(lines[2], "7") {
		t.Errorf("rows = %q", lines[1:])
	}
	if strings.Contains(out, "\x1b") || strings.Contains(out, hostSecret) {
		t.Errorf("out = %q", out)
	}
	// A tab in a name would break the table; it becomes a space.
	if !strings.Contains(out, "il name") {
		t.Errorf("the tab in the name was not made a space: %q", out)
	}
}

func TestDBHostTest(t *testing.T) {
	r := newMoreRun(t)
	if err := r.do("", "dbhost", "test", "3"); err != nil || len(r.access.tested) != 1 || r.access.tested[0].Password != hostSecret {
		t.Fatalf("err = %v, tested %v", err, r.access.tested)
	}
	if err := r.do("", "dbhost", "test", "9"); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("unknown host: %v", err)
	}
	for _, bad := range []string{"0", "abc", "-1", "1.5", ""} {
		if err := r.do("", "dbhost", "test", bad); !isUsage(err) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if err := r.do("", "dbhost", "test"); !isUsage(err) {
		t.Errorf("no argument: %v", err)
	}
}

func TestDBHostUpdate(t *testing.T) {
	r := newMoreRun(t)
	if err := r.do("", "dbhost", "update", "3", "--name", "renamed"); err != nil {
		t.Fatal(err)
	}
	h := r.hosts.hosts[3]
	if h.Name != "renamed" || h.Host != "db.example.com" || h.Port != 3306 || h.Password != hostSecret || h.NodeID == nil {
		t.Errorf("stored %+v", h)
	}

	if err := r.do("new-pass\n", "dbhost", "update", "3", "--host", "h2", "--port", "3310", "--username", "root2", "--password-stdin", "--node-id", "5"); err != nil {
		t.Fatal(err)
	}
	h = r.hosts.hosts[3]
	if h.Host != "h2" || h.Port != 3310 || h.Username != "root2" || h.Password != "new-pass" || h.NodeID == nil || *h.NodeID != 5 {
		t.Errorf("stored %+v", h)
	}
	if err := r.do("", "dbhost", "update", "3", "--no-node"); err != nil || r.hosts.hosts[3].NodeID != nil {
		t.Errorf("unlink: err = %v", err)
	}
	if r.stdout.Len() != 0 {
		t.Errorf("stdout = %q", r.stdout.String())
	}
}

func TestDBHostUpdateRefusals(t *testing.T) {
	for name, args := range map[string][]string{
		"nothing to change": {"3"},
		"both node flags":   {"3", "--node-id", "2", "--no-node"},
		"node id zero":      {"3", "--node-id", "0"},
		"no id":             {"--name", "x"},
		"id zero":           {"0", "--name", "x"},
	} {
		r := newMoreRun(t)
		before := r.hosts.hosts[3]
		if err := r.do("", "dbhost", "update", args...); !isUsage(err) || r.hosts.hosts[3].Name != before.Name {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// A value that was given and is wrong is the domain's refusal, not a missing flag.
	r := newMoreRun(t)
	if err := r.do("", "dbhost", "update", "3", "--port", "0"); err == nil || isUsage(err) || r.hosts.hosts[3].Port != 3306 {
		t.Errorf("port 0: err = %v", err)
	}
	if err := r.do("", "dbhost", "update", "9", "--name", "x"); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("unknown host: %v", err)
	}
	if err := r.do("\n", "dbhost", "update", "3", "--password-stdin"); err == nil || isUsage(err) || r.hosts.hosts[3].Password != hostSecret {
		t.Errorf("empty password line: %v", err)
	}
}

func TestDBHostRotatePassword(t *testing.T) {
	r := newMoreRun(t)
	if err := r.do("", "dbhost", "rotate-password", "3"); err != nil {
		t.Fatal(err)
	}
	password := strings.TrimSpace(r.stdout.String())
	if password == "" || strings.Contains(password, "\n") || r.hosts.hosts[3].Password != password || password == hostSecret {
		t.Errorf("stdout %q, stored %q", r.stdout.String(), r.hosts.hosts[3].Password)
	}
	if strings.Contains(r.stderr.String(), password) || strings.Contains(r.stderr.String(), hostSecret) {
		t.Errorf("stderr = %q", r.stderr.String())
	}
	if len(r.access.ran) != 1 || r.access.ran[0][0].Args[0] != password {
		t.Errorf("the host was told %v", r.access.ran)
	}
}

func TestDBHostRotatePasswordWhenThePanelCannotStoreIt(t *testing.T) {
	r := newMoreRun(t)
	r.hosts.failStore = errors.New("the database is down")
	err := r.do("", "dbhost", "rotate-password", "3")
	given := r.access.ran[0][0].Args[0].(string)
	if err == nil || strings.TrimSpace(r.stdout.String()) != given {
		t.Fatalf("err = %v, stdout %q", err, r.stdout.String())
	}
	if strings.Contains(err.Error(), given) || strings.Contains(r.stderr.String(), given) {
		t.Errorf("the password is in a message: %v / %s", err, r.stderr.String())
	}
	if !strings.Contains(r.stderr.String(), "dbhost update 3 --password-stdin") {
		t.Errorf("stderr = %q", r.stderr.String())
	}
}

func TestDBHostRotatePasswordWhenTheHostRefuses(t *testing.T) {
	r := newMoreRun(t)
	r.access.err = &dbhosts.HostError{Step: 0, Number: 1045}
	if err := r.do("", "dbhost", "rotate-password", "3"); err == nil || r.stdout.Len() != 0 || r.hosts.hosts[3].Password != hostSecret {
		t.Errorf("err = %v, stdout %q", err, r.stdout.String())
	}
}

func TestDBHostDelete(t *testing.T) {
	r := newMoreRun(t)
	r.hosts.databases[3] = 2
	if err := r.do("", "dbhost", "delete", "3"); !errors.Is(err, admin.ErrHostInUse) || len(r.hosts.hosts) != 1 {
		t.Errorf("in use: %v", err)
	}
	r.hosts.databases[3] = 0
	if err := r.do("", "dbhost", "delete", "3"); err != nil || len(r.hosts.hosts) != 0 {
		t.Errorf("err = %v", err)
	}
}

func TestUserNodeAndLocationLists(t *testing.T) {
	r := newMoreRun(t)
	if err := r.do("", "user", "list"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(r.stdout.String(), "\n"), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[1], "1") || !strings.Contains(lines[1], "ada@example.com") || !strings.Contains(lines[1], "yes") || !strings.HasPrefix(lines[2], "2") {
		t.Errorf("users = %q", lines)
	}

	if err := r.do("", "location", "list"); err != nil {
		t.Fatal(err)
	}
	out := r.stdout.String()
	if strings.Contains(out, "\x1b") || strings.Index(out, "eu") > strings.Index(out, "us") {
		t.Errorf("locations = %q", out)
	}

	if err := r.do("", "node", "list"); err != nil {
		t.Fatal(err)
	}
	if out := r.stdout.String(); !strings.Contains(out, "https://n1.example.com:8080") || !strings.Contains(out, "u-4") || !strings.Contains(out, "7") {
		t.Errorf("nodes = %q", out)
	}
	for _, g := range []string{"user", "location", "node"} {
		if err := r.do("", g, "list", "extra"); !isUsage(err) {
			t.Errorf("%s list with an argument: %v", g, err)
		}
	}
}

func TestAPIKeyList(t *testing.T) {
	r := newMoreRun(t)
	if err := r.do("", "apikey", "list"); err != nil {
		t.Fatal(err)
	}
	out := r.stdout.String()
	if a, b, c := strings.Index(out, "ptla_ABCDEFGHIJK"), strings.Index(out, "ptlc_01234567890"), strings.Index(out, "ptlc_abcdefghijk"); !(a < b && b < c) {
		t.Errorf("the keys are not in the order of their identifiers: %d %d %d", a, b, c)
	}
	for _, want := range []string{
		"ptla_ABCDEFGHIJK", "application", "servers=rw users=read", "10.0.0.0/8,192.0.2.1", "2026-11-01 10:30:00",
		"ptlc_abcdefghijk", "account", "never", "any", "ptlc_01234567890",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the list lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("a control character got through: %q", out)
	}
	if strings.Contains(out, "nests") {
		t.Errorf("resources without access are listed:\n%s", out)
	}

	if err := r.do("", "apikey", "list", "--user", "bob"); err != nil {
		t.Fatal(err)
	}
	if out := r.stdout.String(); !strings.Contains(out, "ptlc_01234567890") || strings.Contains(out, "ada") {
		t.Errorf("bob's keys = %q", out)
	}
	if err := r.do("", "apikey", "list", "--user", "nobody"); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
}

func TestAPIKeyRevoke(t *testing.T) {
	r := newMoreRun(t)
	if err := r.do("", "apikey", "revoke", "ptlc_abcdefghijk"); err != nil || !reflect.DeepEqual(r.dir.revoked, []string{"ptlc_abcdefghijk"}) {
		t.Fatalf("err = %v, revoked %v", err, r.dir.revoked)
	}
	if r.stdout.Len() != 0 || !strings.Contains(r.stderr.String(), "Revoked the key ptlc_abcdefghijk of ada") {
		t.Errorf("stdout %q, stderr %q", r.stdout.String(), r.stderr.String())
	}
	if err := r.do("", "apikey", "revoke", "ptlc_zzzzzzzzzzz"); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("unknown key: %v", err)
	}

	// A whole key was pasted: it is refused, and the secret is not echoed anywhere.
	whole := "ptlc_abcdefghijk" + strings.Repeat("S", 32)
	err := r.do("", "apikey", "revoke", whole)
	if err == nil || len(r.dir.revoked) != 1 || strings.Contains(err.Error(), "SSSS") || strings.Contains(r.stderr.String(), "SSSS") || strings.Contains(r.stdout.String(), "SSSS") {
		t.Errorf("err = %v", err)
	}
	if err := r.do("", "apikey", "revoke"); !isUsage(err) {
		t.Errorf("no identifier: %v", err)
	}
}

func TestSettingsGet(t *testing.T) {
	r := newMoreRun(t)
	if err := r.do("", "settings", "get", "app:name"); err != nil || r.stdout.String() != "Raptor?[1m\n" {
		t.Errorf("err = %v, stdout %q", err, r.stdout.String())
	}
	if err := r.do("", "settings", "get", "app:locale"); err == nil || isUsage(err) || r.stdout.Len() != 0 {
		t.Errorf("a setting never set: err = %v, stdout %q", err, r.stdout.String())
	}
	if err := r.do("", "settings", "get", "database:password"); err == nil || r.stdout.Len() != 0 {
		t.Errorf("unknown key: %v", err)
	}
	if err := r.do("", "settings", "get"); !isUsage(err) {
		t.Errorf("no key: %v", err)
	}
}

func TestFlagsMayFollowTheArguments(t *testing.T) {
	r := newMoreRun(t)
	// Before, between and after: the same command.
	for _, args := range [][]string{
		{"--name", "again", "3"},
		{"3", "--name", "again"},
		{"--port", "3311", "3", "--name", "again"},
	} {
		if err := r.do("", "dbhost", "update", args...); err != nil || r.hosts.hosts[3].Name != "again" {
			t.Errorf("%v: err = %v, name %q", args, err, r.hosts.hosts[3].Name)
		}
	}
	// "--" ends the flags: what follows is an argument, even if it looks like a flag.
	if err := r.do("", "apikey", "revoke", "--", "-ptlc_abcdefghij"); err == nil || isUsage(err) || len(r.dir.revoked) != 0 {
		t.Errorf("after --: err = %v", err)
	}
	if err := r.do("", "apikey", "revoke", "--", "ptlc_abcdefghijk"); err != nil || len(r.dir.revoked) != 1 {
		t.Errorf("-- then an identifier: err = %v", err)
	}
	if err := r.do("", "dbhost", "test", "--", "3", "--name"); !isUsage(err) {
		t.Errorf("a flag after --: err = %v", err)
	}
	if err := r.do("", "dbhost", "test", "3", "4"); !isUsage(err) {
		t.Errorf("two arguments: err = %v", err)
	}
}

func TestAPIKeyListShowsAKeyWithoutAccessAndCleansNames(t *testing.T) {
	r := newMoreRun(t)
	if err := r.do("", "apikey", "list", "--user", "bob"); err != nil {
		t.Fatal(err)
	}
	var row string
	for _, l := range strings.Split(r.stdout.String(), "\n") {
		if strings.HasPrefix(l, "ptla_NOACCESSKEY") {
			row = l
		}
	}
	if !strings.Contains(row, "application") || !strings.Contains(row, "none") {
		t.Errorf("row = %q in\n%s", row, r.stdout.String())
	}
}

func TestAPIKeyRevokeCleansTheUserNameItShows(t *testing.T) {
	r := newMoreRun(t)
	if err := r.do("", "apikey", "revoke", "ptlc_evilevilevi"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.stderr.String(), "\x1b") || !strings.Contains(r.stderr.String(), "mal?[0mory") {
		t.Errorf("stderr = %q", r.stderr.String())
	}
}

// After "--" nothing is a flag, so an argument that follows a flag-like word is not read as a flag.
func TestDoubleDashEndsTheFlagsForGood(t *testing.T) {
	r := newMoreRun(t)
	before := r.hosts.hosts[3].Name
	if err := r.do("", "dbhost", "update", "--", "3", "--name", "x"); !isUsage(err) || r.hosts.hosts[3].Name != before {
		t.Errorf("err = %v, name %q", err, r.hosts.hosts[3].Name)
	}
}
