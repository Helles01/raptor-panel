package admin

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/dbhosts"
)

const hostPassword = "Old-p4ssword"

type hostStore struct {
	hosts     map[uint64]HostRecord
	databases map[uint64]int64
	next      uint64
	nodes     map[uint64]bool
	failStore error // makes every write after the closure fail, as a commit that does not go through
}

func newHostStore() *hostStore {
	return &hostStore{
		hosts:     map[uint64]HostRecord{3: {ID: 3, Name: "main", Host: "db.example.com", Port: 3306, Username: "panel", Password: hostPassword}},
		databases: map[uint64]int64{},
		next:      10,
		nodes:     map[uint64]bool{1: true},
	}
}

func (s *hostStore) CreateHost(_ context.Context, h HostRecord) (uint64, error) {
	if h.NodeID != nil && !s.nodes[*h.NodeID] {
		return 0, ErrNotFound
	}
	if s.failStore != nil {
		return 0, s.failStore
	}
	s.next++
	h.ID = s.next
	s.hosts[h.ID] = h
	return h.ID, nil
}

func (s *hostStore) ListHosts(context.Context) ([]HostInfo, error) {
	var out []HostInfo
	for _, h := range s.hosts {
		out = append(out, HostInfo{ID: h.ID, Name: h.Name, Host: h.Host, Port: h.Port, Username: h.Username, NodeID: h.NodeID, Databases: s.databases[h.ID]})
	}
	return out, nil
}

func (s *hostStore) FindHost(_ context.Context, id uint64) (HostRecord, error) {
	h, ok := s.hosts[id]
	if !ok {
		return HostRecord{}, ErrNotFound
	}
	return h, nil
}

func (s *hostStore) ChangeHost(_ context.Context, id uint64, change func(HostRecord) (HostRecord, error)) error {
	h, ok := s.hosts[id]
	if !ok {
		return ErrNotFound
	}
	changed, err := change(h)
	if err != nil {
		return err
	}
	if changed.NodeID != nil && !s.nodes[*changed.NodeID] {
		return ErrNotFound
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
		return ErrNotFound
	}
	if err := check(s.databases[id]); err != nil {
		return err
	}
	delete(s.hosts, id)
	return nil
}

type hostAccess struct {
	tested  []dbhosts.Conn
	ran     []dbhosts.Conn
	stmts   [][]dbhosts.Statement
	testErr error
	runErr  error
}

func (a *hostAccess) Test(_ context.Context, c dbhosts.Conn) error {
	a.tested = append(a.tested, c)
	return a.testErr
}

func (a *hostAccess) RunOn(_ context.Context, c dbhosts.Conn, st []dbhosts.Statement) error {
	a.ran = append(a.ran, c)
	a.stmts = append(a.stmts, st)
	return a.runErr
}

func newHosts(t *testing.T) (*HostService, *hostStore, *hostAccess) {
	t.Helper()
	st, ac := newHostStore(), &hostAccess{}
	s, err := NewHostService(st, ac, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, st, ac
}

func goodHost() HostInput {
	return HostInput{Name: "second", Host: "10.0.0.7", Port: 3307, Username: "panel", Password: "p@ss word"}
}

func TestCreateHost(t *testing.T) {
	s, st, ac := newHosts(t)
	node := uint64(1)
	in := goodHost()
	in.NodeID = &node
	id, err := s.CreateHost(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	got := st.hosts[id]
	if got.Name != "second" || got.Host != "10.0.0.7" || got.Port != 3307 || got.Username != "panel" || got.Password != "p@ss word" || got.NodeID == nil || *got.NodeID != 1 {
		t.Errorf("stored %+v", got)
	}
	want := dbhosts.Conn{Host: "10.0.0.7", Port: 3307, Username: "panel", Password: "p@ss word"}
	if len(ac.tested) != 1 || ac.tested[0] != want {
		t.Errorf("tested %v", ac.tested)
	}
}

func TestCreateHostRefusals(t *testing.T) {
	with := func(f func(*HostInput)) HostInput { in := goodHost(); f(&in); return in }
	for name, in := range map[string]HostInput{
		"no name":      with(func(in *HostInput) { in.Name = " " }),
		"bad host":     with(func(in *HostInput) { in.Host = "db.example.com; DROP" }),
		"port zero":    with(func(in *HostInput) { in.Port = 0 }),
		"long user":    with(func(in *HostInput) { in.Username = strings.Repeat("u", 33) }),
		"bad password": with(func(in *HostInput) { in.Password = "a\nb" }),
	} {
		s, st, ac := newHosts(t)
		if _, err := s.CreateHost(context.Background(), in); err == nil || len(ac.tested) != 0 || len(st.hosts) != 1 {
			t.Errorf("%s: err = %v, tested %d, hosts %d", name, err, len(ac.tested), len(st.hosts))
		}
	}
}

func TestCreateHostNeedsAHostThatAnswers(t *testing.T) {
	s, st, ac := newHosts(t)
	ac.testErr = &dbhosts.HostError{Step: 0, Number: 1045}
	_, err := s.CreateHost(context.Background(), goodHost())
	var he *dbhosts.HostError
	if !errors.As(err, &he) || len(st.hosts) != 1 || strings.Contains(err.Error(), "p@ss word") {
		t.Errorf("err = %v, hosts %d", err, len(st.hosts))
	}
}

func TestCreateHostForANodeThatIsNotThere(t *testing.T) {
	s, st, _ := newHosts(t)
	node := uint64(99)
	in := goodHost()
	in.NodeID = &node
	if _, err := s.CreateHost(context.Background(), in); !errors.Is(err, ErrNotFound) || len(st.hosts) != 1 {
		t.Errorf("err = %v, hosts %d", err, len(st.hosts))
	}
}

func TestListAndTestHost(t *testing.T) {
	s, st, ac := newHosts(t)
	st.databases[3] = 2
	list, err := s.ListHosts(context.Background())
	if err != nil || len(list) != 1 || list[0].Databases != 2 || list[0].Name != "main" {
		t.Fatalf("list = %v, err = %v", list, err)
	}
	if err := s.TestHost(context.Background(), 3); err != nil || len(ac.tested) != 1 || ac.tested[0].Password != hostPassword {
		t.Errorf("err = %v, tested %v", err, ac.tested)
	}
	if err := s.TestHost(context.Background(), 4); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown host: %v", err)
	}
	ac.testErr = errors.New("refused")
	if err := s.TestHost(context.Background(), 3); err == nil {
		t.Error("a host that refuses passed")
	}
}

func ptr[T any](v T) *T { return &v }

func TestUpdateHostKeepsWhatIsNotSent(t *testing.T) {
	s, st, ac := newHosts(t)
	if err := s.UpdateHost(context.Background(), 3, HostPatch{Name: ptr("renamed")}); err != nil {
		t.Fatal(err)
	}
	h := st.hosts[3]
	if h.Name != "renamed" || h.Host != "db.example.com" || h.Port != 3306 || h.Username != "panel" || h.Password != hostPassword || h.NodeID != nil {
		t.Errorf("stored %+v", h)
	}
	if len(ac.tested) != 1 || ac.tested[0].Host != "db.example.com" {
		t.Errorf("tested %v", ac.tested)
	}

	// Everything at once, and the connection tested is the new one.
	err := s.UpdateHost(context.Background(), 3, HostPatch{
		Host: ptr("10.0.0.9"), Port: ptr(3310), Username: ptr("root2"), Password: ptr("new pass"), NodeSet: true, NodeID: ptr(uint64(1)),
	})
	if err != nil {
		t.Fatal(err)
	}
	h = st.hosts[3]
	want := dbhosts.Conn{Host: "10.0.0.9", Port: 3310, Username: "root2", Password: "new pass"}
	if h.Conn() != want || h.NodeID == nil || *h.NodeID != 1 || ac.tested[1] != want {
		t.Errorf("stored %+v, tested %v", h, ac.tested)
	}

	// Unlinking is asked for, not guessed from a missing value.
	if err := s.UpdateHost(context.Background(), 3, HostPatch{NodeSet: true}); err != nil || st.hosts[3].NodeID != nil {
		t.Errorf("unlink: err = %v, node %v", err, st.hosts[3].NodeID)
	}
	if err := s.UpdateHost(context.Background(), 3, HostPatch{NodeSet: true, NodeID: ptr(uint64(1))}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateHost(context.Background(), 3, HostPatch{Name: ptr("again")}); err != nil || st.hosts[3].NodeID == nil {
		t.Errorf("a patch without a node changed the node: err = %v", err)
	}
}

func TestUpdateHostRefusals(t *testing.T) {
	for name, p := range map[string]HostPatch{
		"bad port":     {Port: ptr(70000)},
		"bad host":     {Host: ptr("a b")},
		"empty name":   {Name: ptr("")},
		"bad password": {Password: ptr("x\x00y")},
	} {
		s, st, ac := newHosts(t)
		before := st.hosts[3]
		if err := s.UpdateHost(context.Background(), 3, p); err == nil || st.hosts[3] != before || len(ac.tested) != 0 {
			t.Errorf("%s: err = %v", name, err)
		}
	}

	s, st, ac := newHosts(t)
	before := st.hosts[3]
	ac.testErr = errors.New("refused")
	if err := s.UpdateHost(context.Background(), 3, HostPatch{Host: ptr("elsewhere")}); err == nil || st.hosts[3] != before {
		t.Errorf("a host that refuses was saved: %v", err)
	}
	ac.testErr = nil
	if err := s.UpdateHost(context.Background(), 8, HostPatch{Name: ptr("x")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown host: %v", err)
	}
	if err := s.UpdateHost(context.Background(), 3, HostPatch{NodeSet: true, NodeID: ptr(uint64(55))}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown node: %v", err)
	}
}

func TestRotatePassword(t *testing.T) {
	s, st, ac := newHosts(t)
	password, err := s.RotatePassword(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := (dbhosts.Host{Name: "x", Host: "h", Port: 1, Username: "u"}).Validate(); err != nil || password == "" || password == hostPassword {
		t.Fatalf("password %q", password)
	}
	if st.hosts[3].Password != password {
		t.Errorf("stored %q, given %q", st.hosts[3].Password, password)
	}
	// The host is reached with the old password and told the new one as an argument.
	if len(ac.ran) != 1 || ac.ran[0].Password != hostPassword || ac.ran[0].Host != "db.example.com" {
		t.Fatalf("ran on %v", ac.ran)
	}
	want, _ := dbhosts.ChangeOwnPassword(password)
	if !reflect.DeepEqual(ac.stmts[0], want) {
		t.Errorf("statements = %v", ac.stmts[0])
	}
	for _, st := range ac.stmts[0] {
		if strings.Contains(st.SQL, password) {
			t.Errorf("the password is in the text of %q", st.SQL)
		}
	}
	// A second rotation gives another one.
	again, err := s.RotatePassword(context.Background(), 3)
	if err != nil || again == password || ac.ran[1].Password != password {
		t.Errorf("again = %q, err = %v, signed in with %q", again, err, ac.ran[1].Password)
	}
}

func TestRotatePasswordWhenTheHostRefuses(t *testing.T) {
	s, st, ac := newHosts(t)
	ac.runErr = &dbhosts.HostError{Step: 1, Total: 1, Number: 1045}
	_, err := s.RotatePassword(context.Background(), 3)
	var mismatch *HostMismatchError
	if err == nil || errors.As(err, &mismatch) || st.hosts[3].Password != hostPassword {
		t.Errorf("err = %v, stored %q", err, st.hosts[3].Password)
	}
	if _, err := s.RotatePassword(context.Background(), 9); !errors.Is(err, ErrNotFound) || len(ac.ran) != 1 {
		t.Errorf("unknown host: err = %v, ran %d", err, len(ac.ran))
	}
}

// If the host took the new password and the store did not, the panel is locked out; the one who runs the command must get the password.
func TestRotatePasswordWhenTheStoreFailsAfterTheHostChanged(t *testing.T) {
	s, st, ac := newHosts(t)
	st.failStore = errBoom
	_, err := s.RotatePassword(context.Background(), 3)
	var mismatch *HostMismatchError
	if !errors.As(err, &mismatch) || !errors.Is(err, errBoom) || mismatch.HostID != 3 {
		t.Fatalf("err = %v", err)
	}
	given, _ := ac.stmts[0][0].Args[0].(string)
	if given == "" || mismatch.NewPassword() != given {
		t.Errorf("the error holds %q, the host got %q", mismatch.NewPassword(), given)
	}
	if strings.Contains(err.Error(), given) {
		t.Errorf("the text of the error holds the password: %v", err)
	}
	if st.hosts[3].Password != hostPassword {
		t.Errorf("stored %q", st.hosts[3].Password)
	}
}

func TestDeleteHost(t *testing.T) {
	s, st, _ := newHosts(t)
	st.databases[3] = 1
	if err := s.DeleteHost(context.Background(), 3); !errors.Is(err, ErrHostInUse) || len(st.hosts) != 1 {
		t.Errorf("one database: err = %v, hosts %d", err, len(st.hosts))
	}
	st.databases[3] = 2
	err := s.DeleteHost(context.Background(), 3)
	if !errors.Is(err, ErrHostInUse) || !strings.Contains(err.Error(), "2") || len(st.hosts) != 1 {
		t.Errorf("err = %v, hosts %d", err, len(st.hosts))
	}
	st.databases[3] = 0
	if err := s.DeleteHost(context.Background(), 3); err != nil || len(st.hosts) != 0 {
		t.Errorf("err = %v, hosts %d", err, len(st.hosts))
	}
	if err := s.DeleteHost(context.Background(), 3); !errors.Is(err, ErrNotFound) {
		t.Errorf("gone: %v", err)
	}
}

func TestNewHostServiceNeedsItsParts(t *testing.T) {
	if _, err := NewHostService(nil, &hostAccess{}, nil); err == nil {
		t.Error("no store accepted")
	}
	if _, err := NewHostService(newHostStore(), nil, nil); err == nil {
		t.Error("no access accepted")
	}
}
