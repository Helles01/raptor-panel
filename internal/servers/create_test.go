package servers

import (
	"bytes"
	"crypto/rand"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/nodes"
)

func i64(n int64) *int64 { return &n }

func good() NewServer {
	return NewServer{
		Name: "My server", Description: "for tests", OwnerID: 3, EggID: 5,
		Image: "ghcr.io/pterodactyl/yolks:java_21", Startup: "java -jar {{SERVER_JARFILE}}",
		Limits:     Limits{Memory: 1024, Swap: 0, Disk: 5000, IO: 500, CPU: 100},
		Features:   FeatureLimits{Databases: i64(2), Allocations: i64(0), Backups: i64(3)},
		Allocation: 10,
	}
}

func fieldOf(err error) string {
	var fe *FieldError
	if errors.As(err, &fe) {
		return fe.Field
	}
	return ""
}

func TestNewServerValidate(t *testing.T) {
	with := func(f func(*NewServer)) NewServer { n := good(); f(&n); return n }
	tests := []struct {
		name  string
		n     NewServer
		field string // the field of the error, empty for none
	}{
		{"a good server", good(), ""},
		{"an external id", with(func(n *NewServer) { n.ExternalID = ptr("crm-42") }), ""},
		{"the longest name", with(func(n *NewServer) { n.Name = strings.Repeat("a", 191) }), ""},
		{"a name with spaces round it", with(func(n *NewServer) { n.Name = "  x  " }), ""},
		{"a plain image", with(func(n *NewServer) { n.Image = "itzg/minecraft-server" }), ""},
		{"an image with a digest", with(func(n *NewServer) { n.Image = "ghcr.io/a/b@sha256:abc123" }), ""},
		{"an image that is not pulled", with(func(n *NewServer) { n.Image = "~local/image" }), ""},
		{"additional allocations", with(func(n *NewServer) { n.Additional = []uint64{11, 12} }), ""},
		{"no feature limits", with(func(n *NewServer) { n.Features = FeatureLimits{} }), ""},

		{"no name", with(func(n *NewServer) { n.Name = "" }), "name"},
		{"a name of spaces", with(func(n *NewServer) { n.Name = "   " }), "name"},
		{"a long name", with(func(n *NewServer) { n.Name = strings.Repeat("a", 192) }), "name"},
		{"a newline in the name", with(func(n *NewServer) { n.Name = "a\nb" }), "name"},
		{"a long description", with(func(n *NewServer) { n.Description = strings.Repeat("a", 2001) }), "description"},
		{"a NUL in the description", with(func(n *NewServer) { n.Description = "a\x00" }), "description"},
		{"no owner", with(func(n *NewServer) { n.OwnerID = 0 }), "user"},
		{"no egg", with(func(n *NewServer) { n.EggID = 0 }), "egg"},
		{"no image", with(func(n *NewServer) { n.Image = "" }), "docker_image"},
		{"a space in the image", with(func(n *NewServer) { n.Image = "my image" }), "docker_image"},
		{"a shell character in the image", with(func(n *NewServer) { n.Image = "img;rm -rf /" }), "docker_image"},
		{"a long image", with(func(n *NewServer) { n.Image = strings.Repeat("a", 192) }), "docker_image"},
		{"no startup", with(func(n *NewServer) { n.Startup = "" }), "startup"},
		{"a startup of spaces", with(func(n *NewServer) { n.Startup = "  " }), "startup"},
		{"a long startup", with(func(n *NewServer) { n.Startup = strings.Repeat("a", 8193) }), "startup"},
		{"a NUL in the startup", with(func(n *NewServer) { n.Startup = "a\x00b" }), "startup"},
		{"an empty external id", with(func(n *NewServer) { n.ExternalID = ptr("") }), "external_id"},
		{"a long external id", with(func(n *NewServer) { n.ExternalID = ptr(strings.Repeat("a", 192)) }), "external_id"},
		{"a control character in the external id", with(func(n *NewServer) { n.ExternalID = ptr("a\tb") }), "external_id"},
		{"memory below zero", with(func(n *NewServer) { n.Limits.Memory = -1 }), "limits.memory"},
		{"no primary allocation", with(func(n *NewServer) { n.Allocation = 0 }), "allocation.default"},
		{"a negative feature limit", with(func(n *NewServer) { n.Features.Backups = i64(-1) }), "feature_limits.backups"},
		{"too many additional allocations", with(func(n *NewServer) { n.Additional = make([]uint64, 101) }), "allocation.additional"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.n.Validate()
			if tt.field == "" && err != nil {
				t.Fatalf("error = %v", err)
			}
			if tt.field != "" && fieldOf(err) != tt.field {
				t.Errorf("error = %v, want one for %q", err, tt.field)
			}
		})
	}
}

func TestLimitsValidate(t *testing.T) {
	base := Limits{Memory: 1024, Swap: 0, Disk: 5000, IO: 500, CPU: 100}
	with := func(f func(*Limits)) Limits { l := base; f(&l); return l }
	tests := []struct {
		name  string
		l     Limits
		field string
	}{
		{"typical", base, ""},
		{"no limits", Limits{IO: 500}, ""},
		{"unlimited swap", with(func(l *Limits) { l.Swap = -1 }), ""},
		{"the largest values", Limits{Memory: MaxResource, Swap: MaxResource, Disk: MaxResource, IO: 1000, CPU: MaxResource}, ""},
		{"the lowest IO", with(func(l *Limits) { l.IO = 10 }), ""},

		{"negative memory", with(func(l *Limits) { l.Memory = -1 }), "limits.memory"},
		{"memory the database cannot hold", with(func(l *Limits) { l.Memory = MaxResource + 1 }), "limits.memory"},
		{"swap below -1", with(func(l *Limits) { l.Swap = -2 }), "limits.swap"},
		{"negative disk", with(func(l *Limits) { l.Disk = -1 }), "limits.disk"},
		{"disk the database cannot hold", with(func(l *Limits) { l.Disk = MaxResource + 1 }), "limits.disk"},
		{"IO below 10", with(func(l *Limits) { l.IO = 9 }), "limits.io"},
		{"IO above 1000", with(func(l *Limits) { l.IO = 1001 }), "limits.io"},
		{"negative CPU", with(func(l *Limits) { l.CPU = -1 }), "limits.cpu"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.l.Validate()
			if tt.field == "" && err != nil {
				t.Fatalf("error = %v", err)
			}
			if tt.field != "" && fieldOf(err) != tt.field {
				t.Errorf("error = %v, want one for %q", err, tt.field)
			}
		})
	}
}

func TestValidateThreads(t *testing.T) {
	for s, ok := range map[string]bool{
		"0": true, "0-3": true, "0,2,4": true, "0-3,6": true, "0-3,6-7,10": true, "12": true, "4095": true, "0-4095": true,
		"": false, "-": false, "---": false, ",": false, ",,,": false, "0-": false, "-3": false, "3-0": false, "0--3": false, "0-3-5": false,
		"0,": false, ",0": false, "0 ,1": false, "a": false, "0-3,x": false, "4096": false, "0-4096": false, "00000": false, "1.5": false,
	} {
		if err := ValidateThreads(s); (err == nil) != ok {
			t.Errorf("ValidateThreads(%q) = %v, want ok=%v", s, err, ok)
		}
	}
	l := Limits{IO: 500, Threads: ptr("---")}
	if fieldOf(l.Validate()) != "limits.threads" {
		t.Error("a bad CPU set was accepted in Limits")
	}
}

func TestLimitsUpdate(t *testing.T) {
	cur := Limits{Memory: 1024, Swap: 0, Disk: 5000, IO: 500, CPU: 100, Threads: ptr("0-1")}

	got, err := LimitsUpdate{Memory: i64(2048)}.Apply(cur)
	if err != nil || got.Memory != 2048 || got.Disk != 5000 || *got.Threads != "0-1" {
		t.Errorf("a change of memory: %+v, %v", got, err)
	}
	got, err = LimitsUpdate{Swap: i64(-1), Disk: i64(0), IO: i64(10), CPU: i64(0)}.Apply(cur)
	if err != nil || got.Swap != -1 || got.Disk != 0 || got.IO != 10 || got.CPU != 0 || got.Memory != 1024 {
		t.Errorf("several changes: %+v, %v", got, err)
	}
	got, err = LimitsUpdate{Threads: ptr("2-3")}.Apply(cur)
	if err != nil || *got.Threads != "2-3" {
		t.Errorf("a new CPU set: %+v, %v", got, err)
	}
	got, err = LimitsUpdate{ClearThreads: true}.Apply(cur)
	if err != nil || got.Threads != nil {
		t.Errorf("clearing the CPU set: %+v, %v", got, err)
	}
	got, err = LimitsUpdate{}.Apply(cur)
	if err != nil || !reflect.DeepEqual(got, cur) {
		t.Errorf("no change: %+v, %v", got, err)
	}

	if _, err := (LimitsUpdate{IO: i64(5)}).Apply(cur); fieldOf(err) != "limits.io" {
		t.Errorf("a bad value: %v", err)
	}
	if _, err := (LimitsUpdate{Threads: ptr("x")}).Apply(cur); fieldOf(err) != "limits.threads" {
		t.Errorf("a bad CPU set: %v", err)
	}
	// The result is checked as a whole: an existing value that is not valid is not hidden by a change elsewhere.
	if _, err := (LimitsUpdate{Memory: i64(1)}).Apply(Limits{IO: 5}); fieldOf(err) != "limits.io" {
		t.Errorf("an invalid current value: %v", err)
	}
	if cur.Memory != 1024 || *cur.Threads != "0-1" {
		t.Error("Apply changed the limits it was given")
	}
}

func TestFeatureLimits(t *testing.T) {
	for _, f := range []FeatureLimits{{}, {Databases: i64(0)}, {Allocations: i64(MaxResource)}, {Databases: i64(1), Allocations: i64(2), Backups: i64(3)}} {
		if err := f.Validate(); err != nil {
			t.Errorf("%+v: %v", f, err)
		}
	}
	for field, f := range map[string]FeatureLimits{
		"feature_limits.databases":   {Databases: i64(-1)},
		"feature_limits.allocations": {Allocations: i64(MaxResource + 1)},
		"feature_limits.backups":     {Backups: i64(-5)},
	} {
		if fieldOf(f.Validate()) != field {
			t.Errorf("%+v: want an error for %s", f, field)
		}
	}
}

func row(id, node uint64, server uint64) AllocationRow {
	r := AllocationRow{ID: id, NodeID: node}
	if server != 0 {
		r.ServerID = &server
	}
	return r
}

func rows(rs ...AllocationRow) map[uint64]AllocationRow {
	m := map[uint64]AllocationRow{}
	for _, r := range rs {
		m[r.ID] = r
	}
	return m
}

func TestCheckNewServerAllocations(t *testing.T) {
	table := rows(row(10, 1, 0), row(11, 1, 0), row(12, 1, 0), row(20, 2, 0), row(30, 1, 99))
	tests := []struct {
		name       string
		primary    uint64
		additional []uint64
		ok         bool
	}{
		{"one free allocation", 10, nil, true},
		{"with additional ones", 10, []uint64{11, 12}, true},
		{"the primary is in use", 30, nil, false},
		{"an additional one is in use by another server", 10, []uint64{11, 30}, false},
		{"an allocation of another node", 10, []uint64{20}, false},
		{"a primary of another node", 20, nil, false},
		{"an allocation that does not exist", 10, []uint64{999}, false},
		{"a primary that does not exist", 999, nil, false},
		{"no primary", 0, nil, false},
		{"an additional one that is the primary", 10, []uint64{10}, false},
		{"an additional one twice", 10, []uint64{11, 11}, false},
		{"a zero among the additional ones", 10, []uint64{0}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckNewServerAllocations(1, tt.primary, tt.additional, table)
			if (err == nil) != tt.ok {
				t.Errorf("error = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestPlanAllocationChange(t *testing.T) {
	const server, node = 7, 1
	// Server 7 holds 10 (its default) and 11; 12 and 13 are free on its node; 20 is free on another node; 30 belongs to server 99.
	table := rows(row(10, node, server), row(11, node, server), row(12, node, 0), row(13, node, 0), row(20, 2, 0), row(30, node, 99))

	tests := []struct {
		name   string
		add    []uint64
		remove []uint64
		def    uint64
		want   AllocationPlan
		err    error
		isFld  bool
	}{
		{"nothing", nil, nil, 0, AllocationPlan{Default: 10}, nil, false},
		{"add one", []uint64{12}, nil, 0, AllocationPlan{Assign: []uint64{12}, Default: 10}, nil, false},
		{"add two, in any order", []uint64{13, 12}, nil, 0, AllocationPlan{Assign: []uint64{12, 13}, Default: 10}, nil, false},
		{"add one twice", []uint64{12, 12}, nil, 0, AllocationPlan{Assign: []uint64{12}, Default: 10}, nil, false},
		{"remove one", nil, []uint64{11}, 0, AllocationPlan{Release: []uint64{11}, Default: 10}, nil, false},
		{"remove one twice", nil, []uint64{11, 11}, 0, AllocationPlan{Release: []uint64{11}, Default: 10}, nil, false},
		{"add and remove", []uint64{12}, []uint64{11}, 0, AllocationPlan{Assign: []uint64{12}, Release: []uint64{11}, Default: 10}, nil, false},
		{"remove the default with a new one added", []uint64{13, 12}, []uint64{10}, 0, AllocationPlan{Assign: []uint64{12, 13}, Release: []uint64{10}, Default: 12}, nil, false},
		{"remove the default and name another that is the server's", nil, []uint64{10}, 11, AllocationPlan{Release: []uint64{10}, Default: 11}, nil, false},
		{"change the default to one it holds", nil, nil, 11, AllocationPlan{Default: 11}, nil, false},
		{"change the default to one that is added", []uint64{12}, nil, 12, AllocationPlan{Assign: []uint64{12}, Default: 12}, nil, false},
		{"name the current default", nil, nil, 10, AllocationPlan{Default: 10}, nil, false},
		{"one that is added and removed stays as it is", []uint64{12}, []uint64{12}, 0, AllocationPlan{Assign: []uint64{12}, Default: 10}, nil, false},

		{"remove the default with nothing to take over", nil, []uint64{10}, 0, AllocationPlan{}, ErrNoFallback, false},
		{"remove the named default", nil, []uint64{11}, 11, AllocationPlan{}, ErrNoFallback, false},
		{"name a default that is not the server's", nil, nil, 30, AllocationPlan{}, ErrDefaultNotAssigned, false},
		{"name a default that does not exist", nil, nil, 999, AllocationPlan{}, ErrDefaultNotAssigned, false},
		{"name a default that is being released", nil, []uint64{11}, 12, AllocationPlan{}, ErrDefaultNotAssigned, false},
		{"add one that is another server's", []uint64{30}, nil, 0, AllocationPlan{}, nil, true},
		{"add one of another node", []uint64{20}, nil, 0, AllocationPlan{}, nil, true},
		{"add one the server already has", []uint64{11}, nil, 0, AllocationPlan{}, nil, true},
		{"add one that does not exist", []uint64{999}, nil, 0, AllocationPlan{}, nil, true},
		{"remove one that is another server's", nil, []uint64{30}, 0, AllocationPlan{}, nil, true},
		{"remove one that is free", nil, []uint64{12}, 0, AllocationPlan{}, nil, true},
		{"remove one that does not exist", nil, []uint64{999}, 0, AllocationPlan{}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PlanAllocationChange(server, node, 10, table, tt.add, tt.remove, tt.def)
			switch {
			case tt.isFld && fieldOf(err) == "":
				t.Fatalf("error = %v, want a field error", err)
			case !tt.isFld && !errors.Is(err, tt.err):
				t.Fatalf("error = %v, want %v", err, tt.err)
			}
			if err == nil && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("plan = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestFits(t *testing.T) {
	node := NodeUsage{ID: 1, Public: true, Memory: 1000, MemoryOverallocate: 0, Disk: 10000, DiskOverallocate: 0, UsedMemory: 600, UsedDisk: 5000}
	with := func(f func(*NodeUsage)) NodeUsage { n := node; f(&n); return n }
	tests := []struct {
		name         string
		n            NodeUsage
		memory, disk int64
		want         bool
	}{
		{"room for both", node, 400, 5000, true},
		{"memory one over", node, 401, 5000, false},
		{"disk one over", node, 400, 5001, false},
		{"nothing needed", node, 0, 0, true},
		{"20 percent overallocation of memory", with(func(n *NodeUsage) { n.MemoryOverallocate = 20 }), 600, 5000, true},
		{"20 percent overallocation, one over", with(func(n *NodeUsage) { n.MemoryOverallocate = 20 }), 601, 5000, false},
		{"50 percent overallocation of disk", with(func(n *NodeUsage) { n.DiskOverallocate = 50 }), 400, 10000, true},
		{"overallocation of memory does not help disk", with(func(n *NodeUsage) { n.MemoryOverallocate = 500 }), 400, 5001, false},
		{"memory checking off", with(func(n *NodeUsage) { n.MemoryOverallocate = -1 }), 1 << 30, 5000, true},
		{"disk checking off", with(func(n *NodeUsage) { n.DiskOverallocate = -1 }), 400, 1 << 40, true},
		{"memory checking off does not switch off disk", with(func(n *NodeUsage) { n.MemoryOverallocate = -1 }), 1 << 30, 5001, false},
		{"already over, nothing added", with(func(n *NodeUsage) { n.UsedMemory = 2000 }), 0, 0, false},
		{"a node with no room", with(func(n *NodeUsage) { n.Memory = 0; n.UsedMemory = 0 }), 1, 0, false},
		{"a node with no room but nothing needed", with(func(n *NodeUsage) { n.Memory = 0; n.UsedMemory = 0 }), 0, 0, true},
		{"large numbers do not overflow", NodeUsage{Memory: MaxResource, MemoryOverallocate: 1000000, Disk: MaxResource, UsedMemory: 1 << 40}, 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.n.Fits(tt.memory, tt.disk); got != tt.want {
				t.Errorf("Fits(%d, %d) = %v, want %v", tt.memory, tt.disk, got, tt.want)
			}
		})
	}
}

func TestFindViableNodes(t *testing.T) {
	mk := func(id, loc uint64, public bool, used int64) NodeUsage {
		return NodeUsage{ID: id, LocationID: loc, Public: public, Memory: 1000, Disk: 1000, UsedMemory: used, UsedDisk: 0}
	}
	all := []NodeUsage{mk(1, 10, true, 0), mk(2, 10, true, 900), mk(3, 20, true, 0), mk(4, 20, false, 0), mk(5, 30, true, 500)}
	ids := func(ns []NodeUsage) []uint64 {
		var out []uint64
		for _, n := range ns {
			out = append(out, n.ID)
		}
		return out
	}

	got, err := FindViableNodes(all, nil, 200, 100)
	if err != nil || !reflect.DeepEqual(ids(got), []uint64{1, 3, 5}) {
		t.Errorf("any location: %v, %v (a private node and a full one must be left out)", ids(got), err)
	}
	got, err = FindViableNodes(all, []uint64{20}, 200, 100)
	if err != nil || !reflect.DeepEqual(ids(got), []uint64{3}) {
		t.Errorf("one location: %v, %v", ids(got), err)
	}
	got, err = FindViableNodes(all, []uint64{10, 30}, 200, 100)
	if err != nil || !reflect.DeepEqual(ids(got), []uint64{1, 5}) {
		t.Errorf("two locations: %v, %v", ids(got), err)
	}
	if _, err := FindViableNodes(all, nil, 5000, 100); !errors.Is(err, ErrNoViableNode) {
		t.Errorf("a server too big for any node: %v", err)
	}
	if _, err := FindViableNodes(all, []uint64{40}, 1, 1); !errors.Is(err, ErrNoViableNode) {
		t.Errorf("a location with no nodes: %v", err)
	}
	if _, err := FindViableNodes(nil, nil, 1, 1); !errors.Is(err, ErrNoViableNode) {
		t.Errorf("no nodes at all: %v", err)
	}
	if _, err := FindViableNodes([]NodeUsage{mk(4, 20, false, 0)}, nil, 1, 1); !errors.Is(err, ErrNoViableNode) {
		t.Errorf("only a private node: %v", err)
	}
}

func TestParsePortSpecs(t *testing.T) {
	got, err := ParsePortSpecs([]string{"25565", "30000-30010", "30020-30015"})
	want := []PortSpec{{25565, 25565}, {30000, 30010}, {30015, 30020}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("ParsePortSpecs = %+v, %v; want %+v", got, err, want)
	}
	if got, err := ParsePortSpecs(nil); err != nil || got == nil || len(got) != 0 {
		t.Errorf("no ports: %#v, %v", got, err)
	}
	for _, bad := range []string{"abc", "80", "1024", "65536", "25000-26000", "25565-", "", "1-2"} {
		if _, err := ParsePortSpecs([]string{"25565", bad}); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if _, err := ParsePortSpecs([]string{"25000-26000"}); !errors.Is(err, nodes.ErrTooManyPorts) {
		t.Errorf("a range of 1001: %v", err)
	}
}

func TestSelectAllocation(t *testing.T) {
	all := []Candidate{
		{ID: 1, NodeID: 1, IP: "10.0.0.1", Port: 25565},
		{ID: 2, NodeID: 1, IP: "10.0.0.1", Port: 25566, Used: true},
		{ID: 3, NodeID: 1, IP: "10.0.0.2", Port: 25565},
		{ID: 4, NodeID: 2, IP: "10.0.0.1", Port: 25565},
		{ID: 5, NodeID: 2, IP: "10.0.0.3", Port: 30000},
		{ID: 6, NodeID: 1, IP: "10.0.0.2", Port: 40000},
	}
	pick := func(nodesFilter []uint64, ports []PortSpec, dedicated bool) map[uint64]bool {
		seen := map[uint64]bool{}
		for i := 0; i < 300; i++ {
			c, err := SelectAllocation(all, nodesFilter, ports, dedicated, rand.Reader)
			if err != nil {
				return seen
			}
			seen[c.ID] = true
		}
		return seen
	}
	set := func(ids ...uint64) map[uint64]bool {
		m := map[uint64]bool{}
		for _, id := range ids {
			m[id] = true
		}
		return m
	}

	if got := pick(nil, nil, false); !reflect.DeepEqual(got, set(1, 3, 4, 5, 6)) {
		t.Errorf("anywhere: %v (a used allocation must never be picked)", got)
	}
	if got := pick([]uint64{1}, nil, false); !reflect.DeepEqual(got, set(1, 3, 6)) {
		t.Errorf("on node 1: %v", got)
	}
	if got := pick(nil, []PortSpec{{25565, 25565}}, false); !reflect.DeepEqual(got, set(1, 3, 4)) {
		t.Errorf("on port 25565: %v", got)
	}
	if got := pick(nil, []PortSpec{{30000, 30000}, {40000, 40000}}, false); !reflect.DeepEqual(got, set(5, 6)) {
		t.Errorf("on two ports: %v", got)
	}
	if got := pick(nil, []PortSpec{{25565, 30000}}, false); !reflect.DeepEqual(got, set(1, 3, 4, 5)) {
		t.Errorf("in a range: %v", got)
	}
	// Dedicated: 10.0.0.1 on node 1 is in use (allocation 2), so allocation 1 is out; the same address on node 2 is a different one.
	if got := pick(nil, nil, true); !reflect.DeepEqual(got, set(3, 4, 5, 6)) {
		t.Errorf("dedicated: %v", got)
	}
	if got := pick([]uint64{1}, nil, true); !reflect.DeepEqual(got, set(3, 6)) {
		t.Errorf("dedicated on node 1: %v", got)
	}

	for _, tt := range []struct {
		name  string
		nodes []uint64
		ports []PortSpec
		ded   bool
	}{
		{"a node with none", []uint64{9}, nil, false},
		{"a port with none", nil, []PortSpec{{1, 1}}, false},
		{"dedicated with no free address", []uint64{1}, []PortSpec{{25566, 25566}}, true},
	} {
		if _, err := SelectAllocation(all, tt.nodes, tt.ports, tt.ded, rand.Reader); !errors.Is(err, ErrNoViableAllocation) {
			t.Errorf("%s: error = %v", tt.name, err)
		}
	}
	if _, err := SelectAllocation(nil, nil, nil, false, rand.Reader); !errors.Is(err, ErrNoViableAllocation) {
		t.Errorf("no allocations: %v", err)
	}
	if _, err := SelectAllocation(all, nil, nil, false, bytes.NewReader(nil)); err == nil {
		t.Error("want an error when no random bytes can be read")
	}
}
