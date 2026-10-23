package nodes

import (
	"errors"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/fielderr"
)

func ptr[T any](v T) *T { return &v }

func storedNode() Settings {
	n := validNode()
	n.Description = "main node"
	n.Public = false
	n.BehindProxy = true
	n.MaintenanceMode = true
	n.UploadSize = 256
	n.DaemonBase = "/srv/wings/volumes"
	return n
}

// sameBody sends the required fields as stored and nothing else.
func sameBody(n Settings) Change {
	return Change{
		Name: n.Name, LocationID: n.LocationID, FQDN: n.FQDN, Scheme: n.Scheme,
		Memory: n.Memory, MemoryOverallocate: n.MemoryOverallocate, Disk: n.Disk, DiskOverallocate: n.DiskOverallocate,
		DaemonSFTP: n.DaemonSFTP, DaemonListen: n.DaemonListen,
	}
}

func TestApplyKeepsWhatIsNotSent(t *testing.T) {
	cur := storedNode()

	got, err := cur.Apply(sameBody(cur))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got != cur {
		t.Errorf("a body with only the required fields changed the node:\n got  %+v\n want %+v", got, cur)
	}
}

func TestApplyKeepsTheDaemonBase(t *testing.T) {
	cur := storedNode()
	cur.DaemonBase = "/mnt/data/pterodactyl"

	got, err := cur.Apply(sameBody(cur))

	if err != nil || got.DaemonBase != "/mnt/data/pterodactyl" {
		t.Errorf("DaemonBase = %q, %v; it must not fall back to the default when it is not sent", got.DaemonBase, err)
	}
}

func TestApplyChangesEverything(t *testing.T) {
	cur := storedNode()
	ch := Change{
		Name: "Node 02", Description: ptr("moved"), LocationID: 3, Public: ptr(true),
		FQDN: "node2.example.com", Scheme: "http", BehindProxy: ptr(false), MaintenanceMode: ptr(false),
		Memory: 32768, MemoryOverallocate: 50, Disk: 1000000, DiskOverallocate: -1,
		UploadSize: ptr(int64(512)), DaemonBase: ptr("/data/volumes"), DaemonSFTP: 2222, DaemonListen: 8443,
	}

	got, err := cur.Apply(ch)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	want := Settings{
		Name: "Node 02", Description: "moved", LocationID: 3, Public: true, FQDN: "node2.example.com", Scheme: "http",
		BehindProxy: false, Memory: 32768, MemoryOverallocate: 50, Disk: 1000000, DiskOverallocate: -1,
		DaemonBase: "/data/volumes", DaemonSFTP: 2222, DaemonListen: 8443, UploadSize: 512, MaintenanceMode: false,
	}
	if got != want {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestApplyClearsTheDescription(t *testing.T) {
	cur := storedNode()
	ch := sameBody(cur)
	ch.Description = ptr("")

	got, err := cur.Apply(ch)

	if err != nil || got.Description != "" {
		t.Errorf("Description = %q, %v, want it cleared", got.Description, err)
	}
}

func TestApplyCanSwitchFlagsOff(t *testing.T) {
	cur := storedNode()
	ch := sameBody(cur)
	ch.Public, ch.BehindProxy, ch.MaintenanceMode = ptr(false), ptr(false), ptr(false)
	cur.Public = true

	got, err := cur.Apply(ch)

	if err != nil || got.Public || got.BehindProxy || got.MaintenanceMode {
		t.Errorf("got %+v, %v, want every flag off", got, err)
	}
}

func TestApplyRefusals(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Change)
		field  string
	}{
		{"name", func(ch *Change) { ch.Name = "a/b" }, "name"},
		{"location", func(ch *Change) { ch.LocationID = 0 }, "location_id"},
		{"host", func(ch *Change) { ch.FQDN = "../../etc" }, "fqdn"},
		{"scheme", func(ch *Change) { ch.Scheme = "gopher" }, "scheme"},
		{"memory", func(ch *Change) { ch.Memory = 0 }, "memory"},
		{"disk overallocation", func(ch *Change) { ch.DiskOverallocate = -2 }, "disk_overallocate"},
		{"daemon base", func(ch *Change) { ch.DaemonBase = ptr("/var/../etc") }, "daemon_base"},
		{"upload size", func(ch *Change) { ch.UploadSize = ptr(int64(0)) }, "upload_size"},
		{"sftp port", func(ch *Change) { ch.DaemonSFTP = 70000 }, "daemon_sftp"},
		{"listen port", func(ch *Change) { ch.DaemonListen = 0 }, "daemon_listen"},
		{"description", func(ch *Change) { ch.Description = ptr("a\x00") }, "description"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cur := storedNode()
			ch := sameBody(cur)
			tt.mutate(&ch)

			got, err := cur.Apply(ch)

			fe, ok := fielderr.Of(err)
			if !ok || fe.Field != tt.field {
				t.Fatalf("error = %v, want one about %q", err, tt.field)
			}
			if got != (Settings{}) {
				t.Errorf("a refused change must return no settings: %+v", got)
			}
		})
	}
}

func TestApplyDoesNotChangeTheReceiver(t *testing.T) {
	cur := storedNode()
	before := cur
	ch := sameBody(cur)
	ch.Name = "Other"

	if _, err := cur.Apply(ch); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if cur != before {
		t.Error("Apply changed the stored settings it was called on")
	}
}

func TestDeleteChecks(t *testing.T) {
	id, first := uint64(7), uint64(1)
	tests := []struct {
		name string
		err  error
		want error
	}{
		{"node without servers", CheckCanDeleteNode(0), nil},
		{"node with a server", CheckCanDeleteNode(1), ErrNodeHasServers},
		{"location without nodes", CheckCanDeleteLocation(0), nil},
		{"location with one node", CheckCanDeleteLocation(1), ErrLocationHasNodes},
		{"location with nodes", CheckCanDeleteLocation(3), ErrLocationHasNodes},
		{"free allocation", CheckCanDeleteAllocationOfNode(nil), nil},
		{"allocation in use", CheckCanDeleteAllocationOfNode(&id), ErrAllocationInUse},
		{"allocation of the first server", CheckCanDeleteAllocationOfNode(&first), ErrAllocationInUse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !errors.Is(tt.err, tt.want) {
				t.Errorf("error = %v, want %v", tt.err, tt.want)
			}
		})
	}
}

func TestDeleteRefusalsKeepTheReferencesText(t *testing.T) {
	for err, part := range map[error]string{
		ErrNodeHasServers:   "no servers linked",
		ErrLocationHasNodes: "active nodes",
		ErrAllocationInUse:  "currently assigned",
	} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("%q does not contain %q", err, part)
		}
	}
}

func TestChangeLocation(t *testing.T) {
	cur := Location{Short: "eu", Long: "Europe"}
	tests := []struct {
		name  string
		short string
		long  *string
		want  Location
		field string
	}{
		{"short only keeps the long name", "eu-west", nil, Location{"eu-west", "Europe"}, ""},
		{"both", "us", ptr("United States"), Location{"us", "United States"}, ""},
		{"long cleared", "eu", ptr(""), Location{"eu", ""}, ""},
		{"empty short", "", nil, Location{}, "short"},
		{"short too long", strings.Repeat("a", 61), nil, Location{}, "short"},
		{"long too long", "eu", ptr(strings.Repeat("a", 192)), Location{}, "long"},
		{"control character in long", "eu", ptr("a\x07"), Location{}, "long"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ChangeLocation(cur, tt.short, tt.long)

			if tt.field == "" {
				if err != nil || got != tt.want {
					t.Errorf("got %+v, %v, want %+v", got, err, tt.want)
				}
				return
			}
			if fe, ok := fielderr.Of(err); !ok || fe.Field != tt.field || got != (Location{}) {
				t.Errorf("got %+v, %v, want an error about %q", got, err, tt.field)
			}
		})
	}
}

func TestValidateLocationFields(t *testing.T) {
	tests := []struct {
		short, long, field string
	}{
		{"", "", "short"},
		{"a\x07", "", "short"},
		{"ok", strings.Repeat("a", 192), "long"},
		{"ok", "a\x7f", "long"},
	}
	for _, tt := range tests {
		fe, ok := fielderr.Of(ValidateLocation(tt.short, tt.long))
		if !ok || fe.Field != tt.field {
			t.Errorf("ValidateLocation(%q, %q) = %+v, want field %q", tt.short, tt.long, fe, tt.field)
		}
	}
}
