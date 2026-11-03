//go:build integration

package store_test

import (
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api/allocapi"
)

var errNoRoom = errors.New("no room")

// addAllocations makes allocations on a node, free, on the address and the ports given.
func addAllocations(f *fx, node uint64, ip string, ports ...int) []uint64 {
	f.r.t.Helper()
	var ids []uint64
	for _, p := range ports {
		res, err := f.r.sql.Exec("INSERT INTO allocations (node_id, ip, port) VALUES (?, ?, ?)", node, ip, p)
		if err != nil {
			f.r.t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		ids = append(ids, uint64(id))
	}
	return ids
}

// anyLimit is the check of a server that may have as many allocations as it likes.
func anyLimit(*int64, int64) error { return nil }

// firstFree is the choice of a port that takes the first one of a range that is not in use.
func firstFree(start int) func([]int) (int, error) {
	return func(used []int) (int, error) {
		for p := start; ; p++ {
			if !slices.Contains(used, p) {
				return p, nil
			}
		}
	}
}

func TestClientAllocationsReadAndAssign(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.ClientAllocations()
	r.exec("UPDATE servers SET allocation_limit = 4 WHERE id = 1")

	list, primary, err := st.List(r.ctx, 1)
	if err != nil || len(list) != 1 || list[0].IP != "10.0.0.1" || list[0].Port != 25561 || primary != list[0].ID {
		t.Fatalf("list = %+v primary %d, %v", list, primary, err)
	}
	limit, prim, err := st.Limit(r.ctx, 1)
	if err != nil || limit == nil || *limit != 4 || prim != primary {
		t.Errorf("limit = %v %d, %v", limit, prim, err)
	}
	r.exec("UPDATE servers SET allocation_limit = NULL WHERE id = 2")
	if limit, _, _ := st.Limit(r.ctx, 2); limit != nil {
		t.Errorf("a server without a limit has %d", *limit)
	}
	for _, id := range []uint64{99, 1 << 40} {
		if _, _, err := st.List(r.ctx, id); !errors.Is(err, allocapi.ErrNotFound) {
			t.Errorf("list of server %d: %v", id, err)
		}
		if _, _, err := st.Limit(r.ctx, id); !errors.Is(err, allocapi.ErrNotFound) {
			t.Errorf("limit of server %d: %v", id, err)
		}
	}
	if _, err := st.Find(r.ctx, 2, primary); !errors.Is(err, allocapi.ErrNotFound) {
		t.Errorf("an allocation of another server: %v", err)
	}

	// A free allocation on the server's address is taken; those on another address or on another node are not.
	free := addAllocations(f, f.node1, "10.0.0.1", 26000, 26001)
	addAllocations(f, f.node1, "10.0.0.2", 26002)
	addAllocations(f, f.node2, "10.0.0.1", 26003)
	var seenLimit *int64
	var seenCount int64 = -1
	portCalled := false
	req := allocapi.AssignRequest{ServerID: 1,
		Check: func(l *int64, c int64) error { seenLimit, seenCount = l, c; return nil },
		Port:  func([]int) (int, error) { portCalled = true; return 0, errNoRoom }}
	got, err := st.Assign(r.ctx, req)
	if err != nil || !slices.Contains(free, got.ID) || got.IP != "10.0.0.1" || portCalled {
		t.Fatalf("assigned = %+v, %v (port called: %v)", got, err, portCalled)
	}
	if seenLimit == nil || *seenLimit != 4 || seenCount != 1 {
		t.Errorf("the check saw %v and %d", seenLimit, seenCount)
	}
	if _, err := st.Assign(r.ctx, req); err != nil {
		t.Fatal(err)
	}
	if list, _, _ := st.List(r.ctx, 1); len(list) != 3 {
		t.Errorf("%d allocations after two additions", len(list))
	}

	// None free on the address: the request chooses a port out of those the node has on it; an error of the choice ends the request.
	var used []int
	_, err = st.Assign(r.ctx, allocapi.AssignRequest{ServerID: 1, Check: anyLimit, Port: func(u []int) (int, error) { used = u; return 0, errNoRoom }})
	slices.Sort(used)
	if !errors.Is(err, errNoRoom) || !slices.Equal(used, []int{25561, 25562, 26000, 26001}) {
		t.Errorf("the ports offered: %v, err %v", used, err)
	}
	made, err := st.Assign(r.ctx, allocapi.AssignRequest{ServerID: 1, Check: anyLimit, Port: firstFree(27000)})
	if err != nil || made.Port != 27000 || made.IP != "10.0.0.1" || made.Notes != nil || made.IPAlias != nil {
		t.Fatalf("made = %+v, %v", made, err)
	}
	var holder uint64
	var node uint64
	r.sql.QueryRow("SELECT server_id, node_id FROM allocations WHERE id = ?", made.ID).Scan(&holder, &node)
	if holder != 1 || node != f.node1 {
		t.Errorf("the new allocation is held by %d on node %d", holder, node)
	}

	// The check and the choice end a request without a trace.
	var before int
	r.sql.QueryRow("SELECT COUNT(*) FROM allocations").Scan(&before)
	errLimited := errors.New("limit")
	if _, err := st.Assign(r.ctx, allocapi.AssignRequest{ServerID: 1, Check: func(*int64, int64) error { return errLimited }, Port: firstFree(28000)}); !errors.Is(err, errLimited) {
		t.Errorf("the check's error: %v", err)
	}
	var after int
	r.sql.QueryRow("SELECT COUNT(*) FROM allocations").Scan(&after)
	if after != before {
		t.Errorf("%d allocations were made by requests that were refused", after-before)
	}
	if _, err := st.Assign(r.ctx, allocapi.AssignRequest{ServerID: 99, Check: anyLimit, Port: firstFree(28000)}); !errors.Is(err, allocapi.ErrNotFound) {
		t.Errorf("server 99: %v", err)
	}
}

// Of many requests at once for a server that may have two more, two are granted: the limit is checked with the server's row locked.
func TestClientAllocationsLimitUnderLoad(t *testing.T) {
	f := clientFx(t)
	st := f.r.db.ClientAllocations()
	f.r.exec("UPDATE servers SET allocation_limit = 3 WHERE id = 2")
	free := addAllocations(f, f.node1, "10.0.0.1", 26100, 26101, 26102, 26103)
	wins := once(t, 16, func() (bool, error) {
		_, err := st.Assign(f.r.ctx, allocapi.AssignRequest{ServerID: 2,
			Check: func(l *int64, c int64) error {
				if c >= *l {
					return errNoRoom
				}
				return nil
			}, Port: firstFree(29000)})
		if errors.Is(err, errNoRoom) {
			return false, nil
		}
		return err == nil, err
	})
	var held int
	f.r.sql.QueryRow("SELECT COUNT(*) FROM allocations WHERE server_id = 2").Scan(&held)
	if wins != 2 || held != 3 {
		t.Errorf("%d requests were granted, the server holds %d (limit 3, free %v)", wins, held, free)
	}
}

// Two servers that make a new allocation on one address at the same moment get different ports: the requests take turns, so the second chooses among the ports
// the first left.
func TestClientAllocationsSamePortChosenTwice(t *testing.T) {
	f := clientFx(t)
	st := f.r.db.ClientAllocations()
	choose := func(used []int) (int, error) {
		time.Sleep(150 * time.Millisecond) // long enough for the other request to have read the ports too, if it could
		for p := 30000; ; p++ {
			if !slices.Contains(used, p) {
				return p, nil
			}
		}
	}
	var server atomic.Int32
	wins := once(t, 2, func() (bool, error) {
		_, err := st.Assign(f.r.ctx, allocapi.AssignRequest{ServerID: uint64(server.Add(1)), Check: anyLimit, Port: choose})
		return err == nil, err
	})
	var distinct int
	f.r.sql.QueryRow("SELECT COUNT(DISTINCT port) FROM allocations WHERE port >= 30000").Scan(&distinct)
	if wins != 2 || distinct != 2 {
		t.Errorf("%d granted, %d distinct new ports", wins, distinct)
	}
}

func TestClientAllocationsNotesPrimaryRelease(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.ClientAllocations()
	extra := addAllocations(f, f.node1, "10.0.0.1", 26200, 26201)
	r.exec("UPDATE allocations SET server_id = 1 WHERE id IN (?, ?)", extra[0], extra[1])
	_, primary, _ := st.List(r.ctx, 1)
	_, other, _ := st.List(r.ctx, 2)

	note := func(id uint64) (n *string) {
		var s *string
		r.sql.QueryRow("SELECT notes FROM allocations WHERE id = ?", id).Scan(&s)
		return s
	}
	hello := "hello"
	if err := st.SetNotes(r.ctx, 1, extra[0], &hello); err != nil {
		t.Fatal(err)
	}
	if n := note(extra[0]); n == nil || *n != "hello" {
		t.Errorf("note = %v", n)
	}
	if err := st.SetNotes(r.ctx, 1, extra[0], nil); err != nil || note(extra[0]) != nil {
		t.Errorf("cleared: %v %v", err, note(extra[0]))
	}
	empty := ""
	st.SetNotes(r.ctx, 1, extra[0], &hello)
	if err := st.SetNotes(r.ctx, 1, extra[0], &empty); err != nil || note(extra[0]) != nil {
		t.Errorf("an empty note is no note: %v %v", err, note(extra[0]))
	}
	for _, c := range []struct{ server, alloc uint64 }{{1, other}, {2, extra[0]}, {99, extra[0]}, {1, 1 << 40}} {
		if err := st.SetNotes(r.ctx, c.server, c.alloc, &hello); !errors.Is(err, allocapi.ErrNotFound) {
			t.Errorf("notes of allocation %d of server %d: %v", c.alloc, c.server, err)
		}
	}

	// Primary: only one of the server's own.
	if err := st.SetPrimary(r.ctx, 1, other); !errors.Is(err, allocapi.ErrNotFound) {
		t.Errorf("another server's allocation as primary: %v", err)
	}
	if err := st.SetPrimary(r.ctx, 1, extra[0]); err != nil {
		t.Fatal(err)
	}
	if _, now, _ := st.List(r.ctx, 1); now != extra[0] {
		t.Errorf("primary is %d, not %d", now, extra[0])
	}
	if err := st.SetPrimary(r.ctx, 99, extra[0]); !errors.Is(err, allocapi.ErrNotFound) {
		t.Errorf("server 99: %v", err)
	}

	// Release: not the primary one, not another's, only the server's own; the note goes with it.
	st.SetNotes(r.ctx, 1, extra[1], &hello)
	for _, c := range []struct {
		server, alloc uint64
		name          string
	}{{1, extra[0], "the primary one"}, {2, extra[1], "another server's"}, {1, 1 << 40, "out of range"}} {
		if ok, err := st.Release(r.ctx, c.server, c.alloc); ok || err != nil {
			t.Errorf("released %s: %v %v", c.name, ok, err)
		}
	}
	if ok, err := st.Release(r.ctx, 1, extra[1]); !ok || err != nil {
		t.Fatalf("released the server's own: %v %v", ok, err)
	}
	var holder *uint64
	r.sql.QueryRow("SELECT server_id FROM allocations WHERE id = ?", extra[1]).Scan(&holder)
	if holder != nil || note(extra[1]) != nil {
		t.Errorf("after release: held by %v, note %v", holder, note(extra[1]))
	}
	if ok, _ := st.Release(r.ctx, 1, extra[1]); ok {
		t.Error("released twice")
	}
	if ok, err := st.Release(r.ctx, 1, primary); !ok || err != nil {
		t.Errorf("the allocation that is not primary any more: %v %v", ok, err)
	}
	if _, err := st.Release(r.ctx, 99, extra[0]); !errors.Is(err, allocapi.ErrNotFound) {
		t.Errorf("server 99: %v", err)
	}
}

// Making an allocation primary and giving it up at the same moment: whichever comes first, the primary allocation of the server is one the server holds.
func TestClientAllocationsPrimaryAndReleaseTogether(t *testing.T) {
	for round := 0; round < 4; round++ {
		f := clientFx(t)
		st := f.r.db.ClientAllocations()
		b := addAllocations(f, f.node1, "10.0.0.1", 26300)[0]
		f.r.exec("UPDATE allocations SET server_id = 1 WHERE id = ?", b)
		var turn atomic.Int32
		once(t, 20, func() (bool, error) {
			if turn.Add(1)%2 == 0 {
				err := st.SetPrimary(f.r.ctx, 1, b)
				if errors.Is(err, allocapi.ErrNotFound) {
					return false, nil
				}
				return err == nil, err
			}
			ok, err := st.Release(f.r.ctx, 1, b)
			return ok, err
		})
		var sound int
		f.r.sql.QueryRow("SELECT COUNT(*) FROM servers s JOIN allocations a ON a.id = s.allocation_id WHERE s.id = 1 AND a.server_id = s.id").Scan(&sound)
		if sound != 1 {
			t.Fatalf("round %d: the primary allocation of server 1 is not one it holds", round)
		}
	}
}
