package remote

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func transfer() *Transfer {
	return &Transfer{OldNode: 1, NewNode: 2, OldAllocation: 10, NewAllocation: 20, OldAdditional: []uint64{11, 12}, NewAdditional: []uint64{21}}
}

func TestOwns(t *testing.T) {
	tests := []struct {
		name   string
		scope  Scope
		server uint64
		want   error
	}{
		{"its own server", Scope{2}, 2, nil},
		{"another node's server", Scope{2}, 3, ErrForbidden},
		{"a scope with no node", Scope{0}, 0, ErrForbidden},
		{"a scope with no node and a real server", Scope{0}, 2, ErrForbidden},
	}
	for _, tt := range tests {
		if err := tt.scope.Owns(tt.server); !errors.Is(err, tt.want) {
			t.Errorf("%s: error = %v, want %v", tt.name, err, tt.want)
		}
	}
}

func TestCanReadDetails(t *testing.T) {
	tests := []struct {
		name     string
		scope    Scope
		server   uint64
		transfer *Transfer
		want     error
	}{
		{"its own server", Scope{1}, 1, nil, nil},
		{"another node's server", Scope{3}, 1, nil, ErrForbidden},
		{"the old node during a transfer", Scope{1}, 1, transfer(), nil},
		{"the new node during a transfer", Scope{2}, 1, transfer(), nil},
		{"a third node during a transfer", Scope{3}, 1, transfer(), ErrForbidden},
		{"a third node that owns the server record during a transfer", Scope{3}, 3, transfer(), ErrForbidden},
		{"no node", Scope{0}, 0, nil, ErrForbidden},
		{"no node during a transfer", Scope{0}, 1, &Transfer{}, ErrForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.scope.CanReadDetails(tt.server, tt.transfer); !errors.Is(err, tt.want) {
				t.Errorf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestTransferValidate(t *testing.T) {
	with := func(f func(*Transfer)) Transfer { x := *transfer(); f(&x); return x }
	tests := []struct {
		name string
		t    Transfer
		ok   bool
	}{
		{"a good transfer", *transfer(), true},
		{"no additional allocations", Transfer{OldNode: 1, NewNode: 2, OldAllocation: 10, NewAllocation: 20}, true},
		{"the same node", with(func(x *Transfer) { x.NewNode = 1 }), false},
		{"no old node", with(func(x *Transfer) { x.OldNode = 0 }), false},
		{"no new node", with(func(x *Transfer) { x.NewNode = 0 }), false},
		{"no old allocation", with(func(x *Transfer) { x.OldAllocation = 0 }), false},
		{"no new allocation", with(func(x *Transfer) { x.NewAllocation = 0 }), false},
		{"an allocation on both sides", with(func(x *Transfer) { x.NewAllocation = 11 }), false},
		{"the primary on both sides", with(func(x *Transfer) { x.NewAllocation = 10 }), false},
		{"a new additional that is an old one", with(func(x *Transfer) { x.NewAdditional = []uint64{12} }), false},
		{"an additional that is the primary", with(func(x *Transfer) { x.OldAdditional = []uint64{10} }), false},
		{"an additional used twice", with(func(x *Transfer) { x.OldAdditional = []uint64{11, 11} }), false},
		{"a zero additional", with(func(x *Transfer) { x.NewAdditional = []uint64{0} }), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.t.Validate(); (err == nil) != tt.ok {
				t.Errorf("Validate() = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestFail(t *testing.T) {
	for _, node := range []uint64{1, 2} {
		got, err := transfer().Fail(Scope{node})
		if err != nil {
			t.Fatalf("node %d: %v", node, err)
		}
		want := TransferResult{Release: []uint64{20, 21}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("node %d: result = %+v, want %+v (the new node's allocations are released, the server does not move)", node, got, want)
		}
	}
	if _, err := transfer().Fail(Scope{3}); !errors.Is(err, ErrForbidden) {
		t.Errorf("a third node: error = %v", err)
	}
	if _, err := transfer().Fail(Scope{0}); !errors.Is(err, ErrForbidden) {
		t.Errorf("no node: error = %v", err)
	}
	if _, err := (*Transfer)(nil).Fail(Scope{1}); !errors.Is(err, ErrNotTransferring) {
		t.Errorf("no transfer: error = %v", err)
	}
}

func TestSucceed(t *testing.T) {
	got, err := transfer().Succeed(Scope{2})
	if err != nil {
		t.Fatal(err)
	}
	want := TransferResult{Successful: true, Release: []uint64{10, 11, 12}, Move: true, NewNode: 2, NewAllocation: 20, DeleteFromOldNode: 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("result = %+v, want %+v", got, want)
	}

	// Only the new node holds the files, so only it can say the transfer worked.
	if _, err := transfer().Succeed(Scope{1}); !errors.Is(err, ErrForbidden) {
		t.Errorf("the old node: error = %v", err)
	}
	if _, err := transfer().Succeed(Scope{3}); !errors.Is(err, ErrForbidden) {
		t.Errorf("a third node: error = %v", err)
	}
	if _, err := transfer().Succeed(Scope{0}); !errors.Is(err, ErrForbidden) {
		t.Errorf("no node: error = %v", err)
	}
	if _, err := (*Transfer)(nil).Succeed(Scope{2}); !errors.Is(err, ErrNotTransferring) {
		t.Errorf("no transfer: error = %v", err)
	}
}

func TestResultsDoNotShareMemoryWithTheTransfer(t *testing.T) {
	x := transfer()
	res, _ := x.Succeed(Scope{2})
	res.Release[1] = 999
	if x.OldAdditional[0] != 11 {
		t.Error("changing the result changed the transfer")
	}
}

func TestParsePaging(t *testing.T) {
	tests := []struct {
		page, perPage string
		wantPage      int
		wantPer       int
	}{
		{"", "", 1, 50},
		{"3", "25", 3, 25},
		{"1", "500", 1, 500},
		{"1", "501", 1, 500},
		{"1", "100000", 1, 500},
		{"0", "10", 1, 10},
		{"-2", "10", 1, 10},
		{"abc", "xyz", 1, 50},
		{"2.5", "1e2", 1, 50},
		{"1", "0", 1, 50},
		{"1", "-5", 1, 50},
		{" 2", "10", 1, 10},
		{"99999999999999999999", "10", 1, 10},
	}
	for _, tt := range tests {
		if p, n := ParsePaging(tt.page, tt.perPage); p != tt.wantPage || n != tt.wantPer {
			t.Errorf("ParsePaging(%q, %q) = %d, %d; want %d, %d", tt.page, tt.perPage, p, n, tt.wantPage, tt.wantPer)
		}
	}
}

func TestWindow(t *testing.T) {
	ip := func(n int) *int { return &n }
	tests := []struct {
		name                string
		total, page, perPag int
		offset              int
		want                ListMeta
	}{
		{"the first page", 120, 1, 50, 0, ListMeta{CurrentPage: 1, From: ip(1), LastPage: 3, PerPage: 50, To: ip(50), Total: 120}},
		{"a middle page", 120, 2, 50, 50, ListMeta{CurrentPage: 2, From: ip(51), LastPage: 3, PerPage: 50, To: ip(100), Total: 120}},
		{"the last, short page", 120, 3, 50, 100, ListMeta{CurrentPage: 3, From: ip(101), LastPage: 3, PerPage: 50, To: ip(120), Total: 120}},
		{"an exact fit", 100, 2, 50, 50, ListMeta{CurrentPage: 2, From: ip(51), LastPage: 2, PerPage: 50, To: ip(100), Total: 100}},
		{"one more than a page", 51, 2, 50, 50, ListMeta{CurrentPage: 2, From: ip(51), LastPage: 2, PerPage: 50, To: ip(51), Total: 51}},
		{"no servers", 0, 1, 50, 0, ListMeta{CurrentPage: 1, LastPage: 1, PerPage: 50, Total: 0}},
		{"a page beyond the end", 120, 9, 50, 400, ListMeta{CurrentPage: 9, LastPage: 3, PerPage: 50, Total: 120}},
		{"one server", 1, 1, 50, 0, ListMeta{CurrentPage: 1, From: ip(1), LastPage: 1, PerPage: 50, To: ip(1), Total: 1}},
		{"a negative total is none", -4, 1, 50, 0, ListMeta{CurrentPage: 1, LastPage: 1, PerPage: 50, Total: 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			offset, meta := Window(tt.total, tt.page, tt.perPag)
			if offset != tt.offset || !reflect.DeepEqual(meta, tt.want) {
				t.Errorf("Window = %d, %+v; want %d, %+v", offset, meta, tt.offset, tt.want)
			}
		})
	}
}

func TestListMetaJSON(t *testing.T) {
	_, meta := Window(0, 1, 50)
	raw, _ := json.Marshal(meta)
	// The numbers are numbers, and an empty page has null for from and to, as Laravel writes it.
	if string(raw) != `{"current_page":1,"from":null,"last_page":1,"per_page":50,"to":null,"total":0}` {
		t.Errorf("meta = %s", raw)
	}
	_, meta = Window(120, 2, 50)
	raw, _ = json.Marshal(meta)
	if string(raw) != `{"current_page":2,"from":51,"last_page":3,"per_page":50,"to":100,"total":120}` {
		t.Errorf("meta = %s", raw)
	}
}

func TestResetStatus(t *testing.T) {
	tests := []struct {
		name      string
		status    string
		installed bool
		want      string
		changed   bool
	}{
		{"a first installation that was interrupted", "installing", false, "install_failed", true},
		{"a reinstallation that was interrupted", "installing", true, "reinstall_failed", true},
		{"a restore that was interrupted", "restoring_backup", true, "", true},
		{"a restore on a server never reported installed", "restoring_backup", false, "", true},

		{"a normal server", "", true, "", false},
		{"a normal server never reported installed", "", false, "", false},
		{"a suspended server stays suspended", "suspended", false, "suspended", false},
		{"a suspended server that was installed", "suspended", true, "suspended", false},
		{"a failed installation stays failed", "install_failed", false, "install_failed", false},
		{"a failed reinstallation stays failed", "reinstall_failed", true, "reinstall_failed", false},
		{"an unknown status is left alone", "unknown", true, "unknown", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, changed := ResetStatus(tt.status, tt.installed); got != tt.want || changed != tt.changed {
				t.Errorf("ResetStatus(%q, %v) = %q, %v; want %q, %v", tt.status, tt.installed, got, changed, tt.want, tt.changed)
			}
		})
	}
}
