package activity

import (
	"crypto/sha1" //nolint:gosec // the test checks the key the reference makes
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"
)

func u64(n uint64) *uint64 { return &n }

func noHolders(string) []string { return nil }

func baseEntry() Stored {
	return Stored{
		ID: 42, Batch: "7c0b4d3e-0000-4000-8000-000000000001", Event: "server:backup.start", ActorID: u64(5),
		IP: "203.0.113.9", Description: "a note", Properties: map[string]any{"name": "nightly", "locked": false},
		Timestamp: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC),
	}
}

func TestPresentCarriesTheEntry(t *testing.T) {
	got := Present(baseEntry(), Viewer{ID: 5}, noHolders, time.UTC)

	sum := sha1.Sum([]byte("42")) //nolint:gosec // see the import
	if got.ID != hex.EncodeToString(sum[:]) {
		t.Errorf("ID = %q, want the SHA-1 of the row number as the reference makes it", got.ID)
	}
	if got.Batch == nil || *got.Batch != "7c0b4d3e-0000-4000-8000-000000000001" || got.Event != "server:backup.start" || got.IsAPI || got.Description != "a note" {
		t.Errorf("got %+v", got)
	}
	if got.Properties["name"] != "nightly" || got.Properties["locked"] != false {
		t.Errorf("properties = %v", got.Properties)
	}
	if got.Timestamp != "2026-10-23T12:00:00+00:00" {
		t.Errorf("Timestamp = %q", got.Timestamp)
	}
}

func TestPresentJSONShape(t *testing.T) {
	raw, err := json.Marshal(Present(baseEntry(), Viewer{ID: 5}, noHolders, time.UTC))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, k := range []string{"id", "batch", "event", "is_api", "ip", "description", "properties", "has_additional_metadata", "timestamp"} {
		if _, ok := m[k]; !ok {
			t.Errorf("attribute %q is missing from %s", k, raw)
		}
	}
	if len(m) != 9 {
		t.Errorf("%d attributes in %s, want the nine of the reference", len(m), raw)
	}
}

func TestPresentTimestampFollowsTheZone(t *testing.T) {
	budapest, err := time.LoadLocation("Europe/Budapest")
	if err != nil {
		t.Skipf("no time zone data: %v", err)
	}

	got := Present(baseEntry(), Viewer{ID: 5}, noHolders, budapest)

	if got.Timestamp != "2026-10-23T14:00:00+02:00" {
		t.Errorf("Timestamp = %q", got.Timestamp)
	}
}

func TestPresentWithoutZoneOrHolders(t *testing.T) {
	got := Present(baseEntry(), Viewer{ID: 5}, nil, nil)

	if got.Timestamp != "2026-10-23T12:00:00+00:00" || !got.HasAdditionalMetadata {
		t.Errorf("got %+v, want UTC and everything counted as additional", got)
	}
}

func TestPresentBatchAndAPI(t *testing.T) {
	s := baseEntry()
	s.Batch = ""
	s.APIKeyID = u64(3)

	got := Present(s, Viewer{ID: 5}, noHolders, time.UTC)

	if got.Batch != nil || !got.IsAPI {
		t.Errorf("Batch = %v, IsAPI = %v; want no batch and an API entry", got.Batch, got.IsAPI)
	}
}

func TestPresentWhoSeesTheAddress(t *testing.T) {
	tests := []struct {
		name   string
		actor  *uint64
		viewer Viewer
		want   bool
	}{
		{"the actor", u64(5), Viewer{ID: 5}, true},
		{"an administrator", u64(5), Viewer{ID: 9, Admin: true}, true},
		{"another user", u64(5), Viewer{ID: 9}, false},
		{"another user when nobody did it", nil, Viewer{ID: 9}, false},
		{"an administrator when nobody did it", nil, Viewer{ID: 9, Admin: true}, true},
		{"user zero is nobody", nil, Viewer{ID: 0}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := baseEntry()
			s.ActorID = tt.actor

			got := Present(s, tt.viewer, noHolders, time.UTC)

			if (got.IP != nil) != tt.want {
				t.Fatalf("IP = %v, want shown: %v", got.IP, tt.want)
			}
			if tt.want && *got.IP != "203.0.113.9" {
				t.Errorf("IP = %q", *got.IP)
			}
		})
	}
}

func TestPresentHidesAnAddressAmongTheProperties(t *testing.T) {
	s := baseEntry()
	s.Properties = map[string]any{"ip": "198.51.100.7", "useragent": "curl"}

	tests := []struct {
		name   string
		viewer Viewer
		want   any
	}{
		{"the actor", Viewer{ID: 5}, "198.51.100.7"},
		{"an administrator is not the actor", Viewer{ID: 9, Admin: true}, "[hidden]"},
		{"another user", Viewer{ID: 9}, "[hidden]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Present(s, tt.viewer, noHolders, time.UTC)

			if got.Properties["ip"] != tt.want || got.Properties["useragent"] != "curl" {
				t.Errorf("properties = %v, want ip %v and the browser left alone", got.Properties, tt.want)
			}
		})
	}
}

func TestPresentProperties(t *testing.T) {
	tests := []struct {
		name  string
		props map[string]any
		want  map[string]any
	}{
		{"none", nil, map[string]any{}},
		{"empty", map[string]any{}, map[string]any{}},
		{"plain values stay", map[string]any{"name": "x", "n": 3.0, "flag": true, "nothing": nil},
			map[string]any{"name": "x", "n": 3.0, "flag": true, "nothing": nil}},
		{"a list is counted and the one count is called count", map[string]any{"files": []any{"a", "b", "c"}, "directory": "/srv"},
			map[string]any{"files": []any{"a", "b", "c"}, "count": 3, "directory": "/srv/"}},
		{"an empty list", map[string]any{"files": []any{}}, map[string]any{"files": []any{}, "count": 0}},
		{"an object is counted too", map[string]any{"opts": map[string]any{"a": 1.0, "b": 2.0}},
			map[string]any{"opts": map[string]any{"a": 1.0, "b": 2.0}, "count": 2}},
		{"two counts are both kept", map[string]any{"files": []any{"a"}, "old": []any{"x", "y"}},
			map[string]any{"files": []any{"a"}, "files_count": 1, "old": []any{"x", "y"}, "old_count": 2}},
		{"a stored name that ends in _count counts as one", map[string]any{"retry_count": 4.0}, map[string]any{"count": 4.0}},
		{"a stored count is replaced by the one that is made", map[string]any{"count": 7.0, "files": []any{"a"}}, map[string]any{"files": []any{"a"}, "count": 1}},
		{"a stored name that only ends in count is not a count", map[string]any{"discount": 5.0, "files": []any{"a"}}, map[string]any{"discount": 5.0, "files": []any{"a"}, "count": 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := baseEntry()
			s.Properties = tt.props

			got := Present(s, Viewer{ID: 5}, noHolders, time.UTC).Properties

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("properties = %#v\nwant         %#v", got, tt.want)
			}
		})
	}
}

func TestPresentDirectoryIsNormalized(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/", "/"},
		{"", "/"},
		{"srv", "/srv/"},
		{"/srv", "/srv/"},
		{"/srv/", "/srv/"},
		{"//srv//", "/srv/"},
		{"/srv/world", "/srv/world/"},
		{"/a//b", "/a/b/"},
		{"a///b", "/a//b/"},
	}
	for _, tt := range tests {
		s := baseEntry()
		s.Properties = map[string]any{"directory": tt.in}

		got := Present(s, Viewer{ID: 5}, noHolders, time.UTC).Properties["directory"]

		if got != tt.want {
			t.Errorf("directory %q = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPresentDirectoryOnlyForText(t *testing.T) {
	s := baseEntry()
	s.Properties = map[string]any{"directory": 5.0}

	if got := Present(s, Viewer{ID: 5}, noHolders, time.UTC).Properties["directory"]; got != 5.0 {
		t.Errorf("directory = %v, want a number left alone", got)
	}
}

func TestPresentHasAdditionalMetadata(t *testing.T) {
	shown := func(string) []string { return []string{"name", "files.0"} }
	tests := []struct {
		name  string
		props map[string]any
		want  bool
	}{
		{"nothing stored", nil, false},
		{"only what the text shows", map[string]any{"name": "x"}, false},
		{"the address and the browser", map[string]any{"name": "x", "ip": "1.1.1.1", "useragent": "u", "using_sftp": true}, false},
		{"something more", map[string]any{"name": "x", "locked": true}, true},
		{"a list the text shows one of", map[string]any{"files": []any{"a", "b"}}, true},
		{"only the address", map[string]any{"ip": "1.1.1.1"}, false},
		{"only an unknown key", map[string]any{"why": "x"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := baseEntry()
			s.Properties = tt.props

			if got := Present(s, Viewer{ID: 5}, shown, time.UTC).HasAdditionalMetadata; got != tt.want {
				t.Errorf("HasAdditionalMetadata = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPresentAsksTheHoldersOfTheRightEvent(t *testing.T) {
	var asked string
	holders := func(event string) []string { asked = event; return nil }

	Present(baseEntry(), Viewer{ID: 5}, holders, time.UTC)

	if asked != "server:backup.start" {
		t.Errorf("asked about %q", asked)
	}
}

func TestPresentDoesNotChangeTheStoredProperties(t *testing.T) {
	s := baseEntry()
	s.Properties = map[string]any{"ip": "198.51.100.7", "files": []any{"a"}, "directory": "srv"}

	Present(s, Viewer{ID: 9}, noHolders, time.UTC)

	if s.Properties["ip"] != "198.51.100.7" || s.Properties["directory"] != "srv" || len(s.Properties) != 3 {
		t.Errorf("the stored properties were changed: %v", s.Properties)
	}
}

func TestHiddenEvents(t *testing.T) {
	list := HiddenEvents()

	if !slices.Equal(list, []string{"server:file.upload"}) {
		t.Errorf("HiddenEvents = %v", list)
	}
	list[0] = "x"
	if HiddenEvents()[0] != "server:file.upload" {
		t.Error("the returned list can be changed from outside")
	}
}

func TestParseTexts(t *testing.T) {
	raw := `{"server":{"reinstall":"Reinstalled","backup":{"start":"Started {{name}}","restore":"Restored {{ name }} (deleted files: {{truncate}})"},
		"file":{"rename_one":"Renamed {{directory}}{{files.0.from}} to {{files.0.to}}","rename_other":"Renamed {{count}} files in {{directory}}","read":"Viewed {{file}}"}},
		"auth":{"sftp":{"fail":"Failed"}}}`

	got, err := ParseTexts([]byte(raw))
	if err != nil {
		t.Fatalf("ParseTexts: %v", err)
	}

	want := map[string][]string{
		"server:reinstall":      nil,
		"server:backup.start":   {"name"},
		"server:backup.restore": {"name", "truncate"},
		"server:file.rename":    nil,
		"server:file.read":      {"file"},
		"auth:sftp.fail":        nil,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %#v\nwant %#v", got, want)
	}
}

func TestParseTextsKeepsTheWholeName(t *testing.T) {
	got, err := ParseTexts([]byte(`{"server":{"file":{"read":"{{files.0.from}} and {{files.0.from}} and {{a-b}}"}}}`))
	if err != nil || !reflect.DeepEqual(got["server:file.read"], []string{"files.0.from", "a-b"}) {
		t.Errorf("got %#v, %v", got, err)
	}
}

func TestParseTextsRefusesBrokenJSON(t *testing.T) {
	if _, err := ParseTexts([]byte(`{"server":`)); err == nil {
		t.Error("a broken file must be an error")
	}
	if got, err := ParseTexts([]byte(`{"top":"text"}`)); err != nil || len(got) != 0 {
		t.Errorf("a text outside any group = %v, %v; want it ignored", got, err)
	}
}

func TestTheFrontendTextsParse(t *testing.T) {
	raw, err := os.ReadFile(localePath)
	if err != nil {
		t.Fatalf("reading the texts: %v", err)
	}

	got, err := ParseTexts(raw)
	if err != nil {
		t.Fatalf("ParseTexts: %v", err)
	}

	if len(got) < 60 {
		t.Errorf("only %d events", len(got))
	}
	if !slices.Equal(got["server:startup.edit"], []string{"variable", "old", "new"}) {
		t.Errorf("server:startup.edit shows %v", got["server:startup.edit"])
	}
	if len(got["server:file.rename"]) != 0 || len(got["server:sftp.write"]) != 0 {
		t.Error("an event that has only a singular and a plural text shows nothing, as in the reference")
	}
	if !slices.Equal(got["server:file.read"], []string{"file"}) {
		t.Errorf("server:file.read shows %v", got["server:file.read"])
	}
}
