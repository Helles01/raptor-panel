package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/dbhosts"
)

type dbRotStore struct {
	dbs       []ServerDatabase
	listErr   error
	setErr    map[uint64]error
	passwords map[uint64]string
	asked     []*uint64
	order     *[]string
}

func (s *dbRotStore) Databases(_ context.Context, hostID *uint64) ([]ServerDatabase, error) {
	s.asked = append(s.asked, hostID)
	if s.listErr != nil {
		return nil, s.listErr
	}
	var out []ServerDatabase
	for _, d := range s.dbs {
		if hostID == nil || d.HostID == *hostID {
			out = append(out, d)
		}
	}
	return out, nil
}

func (s *dbRotStore) SetPassword(_ context.Context, id uint64, password string) error {
	if s.order != nil {
		*s.order = append(*s.order, fmt.Sprintf("store %d", id))
	}
	if err := s.setErr[id]; err != nil {
		return err
	}
	if s.passwords == nil {
		s.passwords = map[uint64]string{}
	}
	s.passwords[id] = password
	return nil
}

type dbRotHosts struct {
	ran     []uint64
	runs    [][]dbhosts.Statement
	failFor map[uint64]error
	order   *[]string
	cancel  func()
	after   int
}

func (h *dbRotHosts) Run(_ context.Context, hostID uint64, st []dbhosts.Statement) error {
	h.ran = append(h.ran, hostID)
	h.runs = append(h.runs, st)
	if h.order != nil {
		*h.order = append(*h.order, fmt.Sprintf("host %d", hostID))
	}
	if h.cancel != nil && len(h.ran) == h.after {
		h.cancel()
	}
	return h.failFor[hostID]
}

func newDBRotation(t *testing.T) (*DatabaseRotation, *dbRotStore, *dbRotHosts) {
	t.Helper()
	st := &dbRotStore{dbs: []ServerDatabase{
		{ID: 1, ServerID: 10, HostID: 1, Name: "s10_world", Username: "u10_abc", Remote: "%", MaxConnections: 0},
		{ID: 2, ServerID: 10, HostID: 2, Name: "s10_stats", Username: "u10_def", Remote: "10.0.0.%", MaxConnections: 5},
		{ID: 3, ServerID: 11, HostID: 1, Name: "s11_data", Username: "u11_ghi", Remote: "%"},
	}}
	h := &dbRotHosts{}
	r, err := NewDatabaseRotation(st, h, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r, st, h
}

func TestNewDatabaseRotationNeedsWhatItUses(t *testing.T) {
	if _, err := NewDatabaseRotation(nil, &dbRotHosts{}, nil); err == nil {
		t.Error("no store")
	}
	if _, err := NewDatabaseRotation(&dbRotStore{}, nil, nil); err == nil {
		t.Error("no hosts")
	}
	if r, err := NewDatabaseRotation(&dbRotStore{}, &dbRotHosts{}, nil); err != nil || r.random == nil {
		t.Errorf("default random: %v", err)
	}
}

func TestRotateAllWantsTheGoAhead(t *testing.T) {
	r, st, h := newDBRotation(t)
	_, err := r.RotateAll(context.Background(), nil, false)
	var nc *DatabasesNotConfirmedError
	if !errors.As(err, &nc) || nc.Count != 3 || len(h.ran) != 0 || len(st.passwords) != 0 {
		t.Fatalf("err = %v, host runs %d, passwords %v", err, len(h.ran), st.passwords)
	}
	for _, want := range []string{"--yes", "game servers"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the answer lacks %q: %v", want, err)
		}
	}
	// With one host, the count is of that host's databases.
	one := uint64(1)
	if _, err := r.RotateAll(context.Background(), &one, false); !errors.As(err, &nc) || nc.Count != 2 {
		t.Errorf("one host: %v", err)
	}
}

func TestRotateAllChangesTheHostThenThePanel(t *testing.T) {
	r, st, h := newDBRotation(t)
	var order []string
	st.order, h.order = &order, &order
	rep, err := r.RotateAll(context.Background(), nil, true)
	if err != nil || rep.Total != 3 || rep.Changed != 3 || len(rep.Failed) != 0 {
		t.Fatalf("report = %+v, err = %v", rep, err)
	}
	if strings.Join(order, ",") != "host 1,store 1,host 2,store 2,host 1,store 3" {
		t.Errorf("order = %v", order)
	}
	// What the host was told to set is what the panel stored, for each, and no two databases got the same one.
	seen := map[string]bool{}
	for i, id := range []uint64{1, 2, 3} {
		pw := st.passwords[id]
		if len(pw) < 8 || seen[pw] {
			t.Errorf("database %d: password %q", id, pw)
		}
		seen[pw] = true
		told := false
		for _, s := range h.runs[i] {
			for _, a := range s.Args {
				if a == pw {
					told = true
				}
			}
		}
		if !told {
			t.Errorf("database %d: the statements do not carry the password that was stored", id)
		}
	}
	// The statements are about the database that was meant.
	var all strings.Builder
	for _, s := range h.runs[1] {
		all.WriteString(s.SQL + "\n")
	}
	if !strings.Contains(all.String(), "ALTER USER") {
		t.Errorf("statements = %s", all.String())
	}
}

func TestRotateAllOnOneHost(t *testing.T) {
	r, st, h := newDBRotation(t)
	two := uint64(2)
	rep, err := r.RotateAll(context.Background(), &two, true)
	if err != nil || rep.Total != 1 || rep.Changed != 1 || len(h.ran) != 1 || h.ran[0] != 2 || len(st.passwords) != 1 || st.passwords[2] == "" {
		t.Errorf("report = %+v, err = %v, host runs %v", rep, err, h.ran)
	}
	if st.asked[0] == nil || *st.asked[0] != 2 {
		t.Errorf("the store was asked for %v", st.asked)
	}
}

// One database is changed on its own, and the others are not touched: this is how a database whose store failed is put right.
func TestRotateDatabaseChangesOnlyThatOne(t *testing.T) {
	r, st, h := newDBRotation(t)
	var nc *DatabasesNotConfirmedError
	if _, err := r.RotateDatabase(context.Background(), 3, false); !errors.As(err, &nc) || nc.Count != 1 || len(h.ran) != 0 || len(st.passwords) != 0 {
		t.Fatalf("not confirmed: %v, host runs %v", err, h.ran)
	}
	rep, err := r.RotateDatabase(context.Background(), 3, true)
	if err != nil || rep.Total != 1 || rep.Changed != 1 || len(h.ran) != 1 || h.ran[0] != 1 || len(st.passwords) != 1 || st.passwords[3] == "" {
		t.Errorf("report = %+v, err = %v, host runs %v, passwords %v", rep, err, h.ran, st.passwords)
	}
	// A database that is not there is not found, and nothing is run.
	h.ran = nil
	if _, err := r.RotateDatabase(context.Background(), 99, true); !errors.Is(err, ErrNotFound) || len(h.ran) != 0 {
		t.Errorf("a database that is not there: %v, host runs %v", err, h.ran)
	}
	st.listErr = errBoom
	if _, err := r.RotateDatabase(context.Background(), 3, true); !errors.Is(err, errBoom) {
		t.Errorf("a store that fails: %v", err)
	}
}

// A database that cannot be changed does not stop the others, and the report says how far it got.
func TestRotateAllReportsWhatFailedAndGoesOn(t *testing.T) {
	r, st, h := newDBRotation(t)
	h.failFor = map[uint64]error{2: errors.New("the host answered 1045 (28000)\x1b[31m\nsecret=hunter2")}
	st.setErr = map[uint64]error{3: errors.New("deadlock")}
	rep, err := r.RotateAll(context.Background(), nil, true)
	if err != nil || rep.Total != 3 || rep.Changed != 1 || len(rep.Failed) != 2 {
		t.Fatalf("report = %+v, err = %v", rep, err)
	}
	f2, f3 := rep.Failed[0], rep.Failed[1]
	if f2.ID != 2 || f2.Stage != "host" || f2.HostID != 2 || strings.ContainsAny(f2.Cause, "\x1b\n") {
		t.Errorf("the host's refusal: %+v", f2)
	}
	if f3.ID != 3 || f3.Stage != "store" || !strings.Contains(f3.Cause, "deadlock") {
		t.Errorf("the store's refusal: %+v", f3)
	}
	// Database 1 was done; 2 was not touched in the panel; 3 got its password on the host (the store refused it).
	if st.passwords[1] == "" || st.passwords[2] != "" || st.passwords[3] != "" {
		t.Errorf("passwords stored: %v", st.passwords)
	}
	if len(h.ran) != 3 {
		t.Errorf("host runs: %v", h.ran)
	}
}

func TestRotateAllRefusesWhatItCannotChange(t *testing.T) {
	r, st, h := newDBRotation(t)
	// A name that cannot be put in a statement is not guessed at: it is reported, and the host is not asked.
	st.dbs[0].Name = "bad name; DROP DATABASE x"
	rep, err := r.RotateAll(context.Background(), nil, true)
	if err != nil || rep.Changed != 2 || len(rep.Failed) != 1 || rep.Failed[0].ID != 1 || rep.Failed[0].Stage != "host" {
		t.Fatalf("report = %+v, err = %v", rep, err)
	}
	for _, id := range h.ran {
		_ = id
	}
	if len(h.ran) != 2 {
		t.Errorf("the host was asked %d times", len(h.ran))
	}

	// A store that cannot list is an error, not an empty report.
	r, st, _ = newDBRotation(t)
	st.listErr = errBoom
	if _, err := r.RotateAll(context.Background(), nil, true); !errors.Is(err, errBoom) {
		t.Errorf("listing: %v", err)
	}
	if _, err := r.RotateAll(context.Background(), nil, false); !errors.Is(err, errBoom) {
		t.Errorf("listing, not confirmed: %v", err)
	}
}

func TestRotateAllStopsWhenAskedTo(t *testing.T) {
	r, st, h := newDBRotation(t)
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel, h.after = cancel, 1
	rep, err := r.RotateAll(ctx, nil, true)
	if !errors.Is(err, context.Canceled) || rep.Changed != 1 || len(st.passwords) != 1 {
		t.Errorf("report = %+v, err = %v", rep, err)
	}
}
