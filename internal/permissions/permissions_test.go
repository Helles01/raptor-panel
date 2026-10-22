package permissions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestTreeShape(t *testing.T) {
	groups := []string{"websocket", "control", "user", "file", "backup", "allocation", "startup", "database", "schedule", "settings", "activity"}
	var got []string
	total := 0
	for _, g := range All() {
		got = append(got, g.Name)
		total += len(g.Keys)
		if g.Description == "" {
			t.Errorf("group %s has no description", g.Name)
		}
		for _, k := range g.Keys {
			if k.Description == "" {
				t.Errorf("%s.%s has no description", g.Name, k.Name)
			}
		}
	}
	if !reflect.DeepEqual(got, groups) {
		t.Errorf("groups = %v, want %v", got, groups)
	}
	if total != 40 || len(Names()) != 40 {
		t.Errorf("%d permissions, want 40", total)
	}
}

func TestNamesAreUniqueAndMatchTheConstants(t *testing.T) {
	names := Names()
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] || !regexp.MustCompile(`^[a-z]+\.[a-z_-]+$`).MatchString(n) {
			t.Errorf("bad or duplicate permission name %q", n)
		}
		seen[n] = true
	}

	constants := []string{
		WebsocketConnect, ControlConsole, ControlStart, ControlStop, ControlRestart, UserCreate, UserRead, UserUpdate, UserDelete,
		FileCreate, FileRead, FileReadContent, FileUpdate, FileDelete, FileArchive, FileSFTP,
		BackupCreate, BackupRead, BackupDelete, BackupDownload, BackupRestore,
		AllocationRead, AllocationCreate, AllocationUpdate, AllocationDelete,
		StartupRead, StartupUpdate, StartupDockerImage,
		DatabaseCreate, DatabaseRead, DatabaseUpdate, DatabaseDelete, DatabaseViewPassword,
		ScheduleCreate, ScheduleRead, ScheduleUpdate, ScheduleDelete, SettingsRename, SettingsReinstall, ActivityRead,
	}
	sort.Strings(constants)
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	if !reflect.DeepEqual(constants, sorted) {
		t.Errorf("the constants and the tree differ:\n constants %v\n tree      %v", constants, sorted)
	}
}

func TestTreeJSONKeepsTheOrder(t *testing.T) {
	raw, err := json.Marshal(All())
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(raw) {
		t.Fatalf("invalid JSON: %s", raw)
	}

	text := string(raw)
	// The frontend lists groups and keys in the order of the object, so the order is part of the contract.
	order := []string{`"websocket":`, `"control":`, `"user":`, `"file":`, `"backup":`, `"allocation":`, `"startup":`, `"database":`, `"schedule":`, `"settings":`, `"activity":`}
	last := -1
	for _, group := range order {
		at := strings.Index(text, group)
		if at <= last {
			t.Errorf("group %s is out of order in %s", group, text[:100])
		}
		last = at
	}
	if i, j := strings.Index(text, `"console":`), strings.Index(text, `"start":`); i < 0 || j < i {
		t.Error("control keys are out of order")
	}

	var decoded map[string]struct {
		Description string            `json:"description"`
		Keys        map[string]string `json:"keys"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["control"].Keys["stop"] != "Allows a user to stop a server if it is running." || len(decoded) != 11 {
		t.Errorf("decoded = %v", decoded)
	}
	if !strings.Contains(decoded["control"].Description, "user's ability") {
		t.Errorf("apostrophes must survive: %q", decoded["control"].Description)
	}
}

func TestValid(t *testing.T) {
	for _, ok := range []string{"websocket.connect", "file.read-content", "database.view_password", "startup.docker-image"} {
		if !Valid(ok) {
			t.Errorf("%q must be valid", ok)
		}
	}
	for _, bad := range []string{"", "*", "admin.websocket.errors", "file", "file.", "file.read-content ", "FILE.READ", "file.nope", "control.kill", "../x"} {
		if Valid(bad) {
			t.Errorf("%q must not be valid", bad)
		}
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"websocket.connect is always added", []string{"control.start"}, []string{"control.start", "websocket.connect"}},
		{"nothing sent", nil, []string{"websocket.connect"}},
		{"already there", []string{"websocket.connect", "file.read"}, []string{"websocket.connect", "file.read"}},
		{"unknown names are dropped", []string{"control.start", "root.access", "*", "admin.websocket.errors", ""}, []string{"control.start", "websocket.connect"}},
		{"duplicates are dropped", []string{"file.read", "file.read", "file.update"}, []string{"file.read", "file.update", "websocket.connect"}},
		{"order sent is kept", []string{"schedule.read", "backup.read"}, []string{"schedule.read", "backup.read", "websocket.connect"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Normalize(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Normalize(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestHas(t *testing.T) {
	tests := []struct {
		held   []string
		wanted string
		want   bool
	}{
		{[]string{"file.read"}, "file.read", true},
		{[]string{"file.read"}, "file.update", false},
		{[]string{"*"}, "file.update", true},
		{[]string{"*"}, "admin.websocket.errors", false},
		{[]string{"*", "admin.websocket.errors"}, "admin.websocket.errors", true},
		{nil, "file.read", false},
		{[]string{"file.read"}, "", false},
		{[]string{"file"}, "file.read", false},
		{[]string{"file.read-content"}, "file.read", false},
	}
	for _, tt := range tests {
		if got := Has(tt.held, tt.wanted); got != tt.want {
			t.Errorf("Has(%v, %q) = %v, want %v", tt.held, tt.wanted, got, tt.want)
		}
	}
}

func TestCanAssign(t *testing.T) {
	subuser := []string{"user.create", "file.read", "file.update", "websocket.connect"}
	tests := []struct {
		name      string
		held      []string
		requested []string
		want      bool
	}{
		{"a subset", subuser, []string{"file.read", "websocket.connect"}, true},
		{"the same set", subuser, subuser, true},
		{"nothing", subuser, nil, true},
		{"more than held", subuser, []string{"file.read", "backup.delete"}, false},
		{"owner may assign anything assignable", []string{"*"}, Names(), true},
		// An owner's request is not refused for odd names: Normalize drops them (see TestNormalize).
		{"owner asking for names that do not exist", []string{"*"}, []string{"file.read", "root.access"}, true},
		{"owner asking for the wildcard", []string{"*"}, []string{"*"}, true},
		// The wildcard does not cover admin.* in Has, but an owner's request is not refused for it either.
		{"owner asking for an admin permission", []string{"*"}, []string{"admin.websocket.errors"}, true},
		{"subuser asking for a name that does not exist", subuser, []string{"file.read", "root.access"}, false},
		{"subuser asking for the wildcard", subuser, []string{"*"}, false},
		{"subuser asking for an admin permission", subuser, []string{"admin.websocket.errors"}, false},
		{"empty held", nil, []string{"file.read"}, false},
		{"empty held, nothing asked", nil, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanAssign(tt.held, tt.requested); got != tt.want {
				t.Errorf("CanAssign = %v, want %v", got, tt.want)
			}
		})
	}
}

// The reference's permission model is the source of the names; when it is present, they must agree.
func TestMatchesTheReference(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "reference", "app", "Models", "Permission.php"))
	if err != nil {
		t.Skip("reference/ is not present")
	}
	re := regexp.MustCompile(`const ACTION_[A-Z_]+ = '([a-z_.-]+)';`)
	var fromReference []string
	for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
		fromReference = append(fromReference, m[1])
	}
	sort.Strings(fromReference)

	mine := Names()
	sort.Strings(mine)
	if !reflect.DeepEqual(mine, fromReference) {
		t.Errorf("permissions differ from the reference:\n mine      %v\n reference %v", mine, fromReference)
	}
}
