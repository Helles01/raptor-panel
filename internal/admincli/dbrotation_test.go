package admincli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/admin"
	"github.com/Helles01/raptor-panel/internal/dbhosts"
)

type rotDBStore struct {
	dbs       []admin.ServerDatabase
	passwords map[uint64]string
	setFail   map[uint64]error
}

func (s *rotDBStore) Databases(_ context.Context, host *uint64) ([]admin.ServerDatabase, error) {
	var out []admin.ServerDatabase
	for _, d := range s.dbs {
		if host == nil || d.HostID == *host {
			out = append(out, d)
		}
	}
	return out, nil
}

func (s *rotDBStore) SetPassword(_ context.Context, id uint64, pw string) error {
	if err := s.setFail[id]; err != nil {
		return err
	}
	if s.passwords == nil {
		s.passwords = map[uint64]string{}
	}
	s.passwords[id] = pw
	return nil
}

type rotDBHosts struct {
	ran  []uint64
	fail map[uint64]error
}

func (h *rotDBHosts) Run(_ context.Context, host uint64, _ []dbhosts.Statement) error {
	h.ran = append(h.ran, host)
	return h.fail[host]
}

type rotRun struct {
	t      *testing.T
	st     *rotDBStore
	hosts  *rotDBHosts
	cmds   map[string]map[string]Handler
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func newRotRun(t *testing.T) *rotRun {
	t.Helper()
	st := &rotDBStore{dbs: []admin.ServerDatabase{
		{ID: 1, ServerID: 10, HostID: 1, Name: "s10_world", Username: "u10_abc", Remote: "%"},
		{ID: 2, ServerID: 10, HostID: 2, Name: "s10_stats", Username: "u10_def", Remote: "%"},
		{ID: 3, ServerID: 11, HostID: 1, Name: "s11_data", Username: "u11_ghi", Remote: "%"},
	}}
	h := &rotDBHosts{}
	svc, err := admin.NewDatabaseRotation(st, h, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &rotRun{t: t, st: st, hosts: h, cmds: All(Services{Databases: svc})}
}

func (r *rotRun) do(args ...string) error {
	r.t.Helper()
	r.stdout.Reset()
	r.stderr.Reset()
	return r.cmds["dbhost"]["rotate-all"](context.Background(), Env{Stdin: strings.NewReader(""), Stdout: &r.stdout, Stderr: &r.stderr}, args)
}

func TestDbhostRotateAllWantsYes(t *testing.T) {
	r := newRotRun(t)
	err := r.do()
	var nc *admin.DatabasesNotConfirmedError
	if !errors.As(err, &nc) || nc.Count != 3 || isUsage(err) || len(r.hosts.ran) != 0 || len(r.st.passwords) != 0 || r.stdout.Len() != 0 {
		t.Fatalf("err = %v, host runs %v", err, r.hosts.ran)
	}
	if err := r.do("--yes"); err != nil || len(r.hosts.ran) != 3 || len(r.st.passwords) != 3 || r.stdout.Len() != 0 {
		t.Fatalf("err = %v, host runs %v", err, r.hosts.ran)
	}
	if !strings.Contains(r.stderr.String(), "Gave 3 of 3 database(s) a new password") {
		t.Errorf("stderr = %q", r.stderr.String())
	}
	// The new passwords are nowhere in what the command says: they are in the panel and on the host, and are shown to the owners by the panel when they ask.
	for _, pw := range r.st.passwords {
		if strings.Contains(r.stderr.String()+r.stdout.String(), pw) {
			t.Errorf("a password is printed")
		}
	}
}

func TestDbhostRotateAllOnOneHost(t *testing.T) {
	r := newRotRun(t)
	if err := r.do("--host", "1", "--yes"); err != nil || len(r.hosts.ran) != 2 || len(r.st.passwords) != 2 || r.st.passwords[2] != "" {
		t.Fatalf("err = %v, host runs %v, passwords %v", err, r.hosts.ran, r.st.passwords)
	}
	var nc *admin.DatabasesNotConfirmedError
	if err := r.do("--host", "2"); !errors.As(err, &nc) || nc.Count != 1 {
		t.Errorf("the count of one host: %v", err)
	}
}

func TestDbhostRotateAllSaysWhatFailed(t *testing.T) {
	r := newRotRun(t)
	r.hosts.fail = map[uint64]error{2: errors.New("the host refused: 1045")}
	err := r.do("--yes")
	if err == nil || isUsage(err) || !strings.Contains(err.Error(), "1 database(s) were not changed") {
		t.Fatalf("err = %v", err)
	}
	said := r.stderr.String()
	if !strings.Contains(said, "Gave 2 of 3 database(s)") || !strings.Contains(said, "database 2 (s10_stats, host 2): nothing was changed: the host refused: 1045") {
		t.Errorf("stderr = %q", said)
	}
	if r.st.passwords[1] == "" || r.st.passwords[2] != "" || r.st.passwords[3] == "" {
		t.Errorf("passwords = %v", r.st.passwords)
	}
}

// The host has the new password and the panel could not keep it: the command names the one database to do again, not the whole run, and doing that touches
// only it.
func TestDbhostRotateAllSaysHowToPutRightWhatTheStoreLost(t *testing.T) {
	r := newRotRun(t)
	r.st.setFail = map[uint64]error{2: errors.New("the store is down")}
	err := r.do("--yes")
	if err == nil || isUsage(err) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(r.stderr.String(), "run `dbhost rotate-all --database 2 --yes`") {
		t.Errorf("stderr = %q", r.stderr.String())
	}
	r.st.setFail = nil
	r.hosts.ran = nil
	before := map[uint64]string{1: r.st.passwords[1], 3: r.st.passwords[3]}
	if err := r.do("--database", "2", "--yes"); err != nil || len(r.hosts.ran) != 1 || r.st.passwords[2] == "" || r.st.passwords[1] != before[1] || r.st.passwords[3] != before[3] {
		t.Errorf("err = %v, host runs %v, passwords %v", err, r.hosts.ran, r.st.passwords)
	}
	var nc *admin.DatabasesNotConfirmedError
	if err := r.do("--database", "3"); !errors.As(err, &nc) || nc.Count != 1 {
		t.Errorf("the count of one database: %v", err)
	}
	if err := r.do("--database", "99", "--yes"); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("a database that is not there: %v", err)
	}
}

func TestDbhostRotateAllRefusesWhatItCannotDo(t *testing.T) {
	for name, args := range map[string][]string{
		"a database of 0":       {"--database", "0", "--yes"},
		"a host and a database": {"--host", "1", "--database", "1", "--yes"},
		"a host of 0":           {"--host", "0", "--yes"},
		"a host that is no id":  {"--host", "first", "--yes"},
		"a negative host":       {"--host", "-1", "--yes"},
		"an argument":           {"everything"},
		"a flag nobody knows":   {"--all", "--yes"},
	} {
		r := newRotRun(t)
		if err := r.do(args...); !isUsage(err) || len(r.hosts.ran) != 0 || len(r.st.passwords) != 0 {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
