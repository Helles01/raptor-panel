package activity

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

const (
	serverUUID = "1111aaaa-2222-4333-8444-5555bbbbcccc"
	userUUID   = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
)

func event(mod func(*RawEvent)) RawEvent {
	e := RawEvent{
		User: userUUID, Server: serverUUID, Event: "server:sftp.write",
		Metadata: json.RawMessage(`{"files":["a.txt"]}`), IP: "203.0.113.9", Timestamp: "2026-10-22T11:59:00.123456789Z",
	}
	if mod != nil {
		mod(&e)
	}
	return e
}

func props(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("properties %q are not a JSON object: %v", s, err)
	}
	return m
}

func TestNormalizeAGoodEvent(t *testing.T) {
	res, err := Normalize([]RawEvent{event(nil)}, now)
	if err != nil || res.Skipped != 0 || len(res.Entries) != 1 {
		t.Fatalf("result = %+v, %v", res, err)
	}
	got := res.Entries[0]
	want := Entry{
		ServerUUID: serverUUID, UserUUID: userUUID, Event: "server:sftp.write", IP: "203.0.113.9",
		Properties: `{"files":["a.txt"]}`, When: time.Date(2026, 10, 22, 11, 59, 0, 123456789, time.UTC),
	}
	if got != want {
		t.Errorf("entry = %+v\nwant   %+v", got, want)
	}
}

func TestNormalizeSkipsWhatCannotBeUsed(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*RawEvent)
	}{
		{"an event of another family", func(e *RawEvent) { e.Event = "auth:sftp.fail" }},
		{"an administration event", func(e *RawEvent) { e.Event = "admin:server.create" }},
		{"no event", func(e *RawEvent) { e.Event = "" }},
		{"only the prefix", func(e *RawEvent) { e.Event = "server:" }},
		{"the prefix in upper case", func(e *RawEvent) { e.Event = "SERVER:sftp.write" }},
		{"the prefix hidden behind a space", func(e *RawEvent) { e.Event = " server:sftp.write" }},
		{"a space in the name", func(e *RawEvent) { e.Event = "server:sftp write" }},
		{"upper case in the name", func(e *RawEvent) { e.Event = "server:Sftp.write" }},
		{"a newline in the name", func(e *RawEvent) { e.Event = "server:sftp.write\n" }},
		{"a name that is too long", func(e *RawEvent) { e.Event = "server:" + strings.Repeat("a", 190) }},
		{"no server", func(e *RawEvent) { e.Server = "" }},
		{"a server that is not a UUID", func(e *RawEvent) { e.Server = "abcd1234" }},
		{"a server UUID with a brace", func(e *RawEvent) { e.Server = "{" + serverUUID + "}" }},
		{"a user that is not a UUID", func(e *RawEvent) { e.User = "jane" }},
		{"an address that is not an address", func(e *RawEvent) { e.IP = "not an address" }},
		{"an address with a host name", func(e *RawEvent) { e.IP = "client.example.com" }},
		{"an address with a port that is broken", func(e *RawEvent) { e.IP = "203.0.113.9:99999" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := Normalize([]RawEvent{event(tt.mod)}, now)
			if err != nil || res.Skipped != 1 || len(res.Entries) != 0 {
				t.Errorf("result = %+v, %v; want the event skipped", res, err)
			}
		})
	}
}

func TestNormalizeAcceptsWhatItShould(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*RawEvent)
		ip   string
		user string
	}{
		{"no user", func(e *RawEvent) { e.User = "" }, "203.0.113.9", ""},
		{"no address", func(e *RawEvent) { e.IP = "" }, "127.0.0.1", userUUID},
		{"an address of spaces", func(e *RawEvent) { e.IP = "  " }, "127.0.0.1", userUUID},
		{"an address with a port", func(e *RawEvent) { e.IP = "203.0.113.9:51234" }, "203.0.113.9", userUUID},
		{"IPv6", func(e *RawEvent) { e.IP = "2001:DB8::1" }, "2001:db8::1", userUUID},
		{"IPv6 with a port", func(e *RawEvent) { e.IP = "[2001:db8::1]:22" }, "2001:db8::1", userUUID},
		{"IPv4 inside IPv6", func(e *RawEvent) { e.IP = "::ffff:203.0.113.9" }, "203.0.113.9", userUUID},
		{"a zone is dropped", func(e *RawEvent) { e.IP = "fe80::1%eth0" }, "fe80::1", userUUID},
		{"upper-case UUIDs are stored in lower case", func(e *RawEvent) { e.Server = strings.ToUpper(serverUUID); e.User = strings.ToUpper(userUUID) }, "203.0.113.9", userUUID},
		{"the longest event name", func(e *RawEvent) { e.Event = "server:" + strings.Repeat("a", 184) }, "203.0.113.9", userUUID},
		{"a name with dashes, dots and colons", func(e *RawEvent) { e.Event = "server:file.create-directory" }, "203.0.113.9", userUUID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := Normalize([]RawEvent{event(tt.mod)}, now)
			if err != nil || res.Skipped != 0 || len(res.Entries) != 1 {
				t.Fatalf("result = %+v, %v", res, err)
			}
			if e := res.Entries[0]; e.IP != tt.ip || e.UserUUID != tt.user || e.ServerUUID != serverUUID {
				t.Errorf("entry = %+v", e)
			}
		})
	}
}

func TestNormalizeMetadata(t *testing.T) {
	big := `{"x":"` + strings.Repeat("a", MaxMetadataBytes) + `"}`
	tests := []struct {
		name string
		in   string
		want map[string]any
	}{
		{"an object", `{"a":1,"b":{"c":[1,2]}}`, map[string]any{"a": 1.0, "b": map[string]any{"c": []any{1.0, 2.0}}}},
		{"an empty object", `{}`, map[string]any{}},
		{"null", `null`, map[string]any{}},
		{"absent", ``, map[string]any{}},
		{"white space", `  `, map[string]any{}},
		{"an array", `["a"]`, map[string]any{"metadata_dropped": "not an object"}},
		{"a string", `"text"`, map[string]any{"metadata_dropped": "not an object"}},
		{"a number", `5`, map[string]any{"metadata_dropped": "not an object"}},
		{"too large", big, map[string]any{"metadata_dropped": "too large"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := Normalize([]RawEvent{event(func(e *RawEvent) { e.Metadata = json.RawMessage(tt.in) })}, now)
			if err != nil || len(res.Entries) != 1 {
				t.Fatalf("result = %+v, %v: the event itself must be kept", res, err)
			}
			if got := props(t, res.Entries[0].Properties); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("properties = %v, want %v", got, tt.want)
			}
		})
	}

	// An object at the size limit is kept.
	exact := `{"x":"` + strings.Repeat("a", MaxMetadataBytes-8) + `"}`
	if len(exact) != MaxMetadataBytes {
		t.Fatalf("test object is %d bytes", len(exact))
	}
	res, _ := Normalize([]RawEvent{event(func(e *RawEvent) { e.Metadata = json.RawMessage(exact) })}, now)
	if props(t, res.Entries[0].Properties)["metadata_dropped"] != nil {
		t.Error("an object of exactly the limit was dropped")
	}
}

func TestNormalizeTimestamps(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		want     time.Time
		original bool
	}{
		{"with nanoseconds", "2026-10-22T11:00:00.123456789Z", time.Date(2026, 10, 22, 11, 0, 0, 123456789, time.UTC), false},
		{"whole seconds", "2026-10-22T11:00:00Z", time.Date(2026, 10, 22, 11, 0, 0, 0, time.UTC), false},
		{"an offset", "2026-10-22T13:00:00+02:00", time.Date(2026, 10, 22, 11, 0, 0, 0, time.UTC), false},
		{"an offset with a fraction", "2026-10-22T13:00:00.5+02:00", time.Date(2026, 10, 22, 11, 0, 0, 500000000, time.UTC), false},
		{"a few days old", "2026-10-18T11:00:00Z", time.Date(2026, 10, 18, 11, 0, 0, 0, time.UTC), false},
		{"a little ahead of the clock", "2026-10-22T12:09:00Z", time.Date(2026, 10, 22, 12, 9, 0, 0, time.UTC), false},
		{"exactly at the skew limit", "2026-10-22T12:10:00Z", time.Date(2026, 10, 22, 12, 10, 0, 0, time.UTC), false},

		{"too far ahead", "2026-10-22T12:10:01Z", now, true},
		{"years ahead", "2099-01-01T00:00:00Z", now, true},
		{"not a time", "yesterday", now, true},
		{"empty", "", now, true},
		{"a date only", "2026-10-22", now, true},
		{"a time without a zone", "2026-10-22T11:00:00", now, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := Normalize([]RawEvent{event(func(e *RawEvent) { e.Timestamp = tt.in })}, now)
			if err != nil || len(res.Entries) != 1 {
				t.Fatalf("result = %+v, %v", res, err)
			}
			e := res.Entries[0]
			if !e.When.Equal(tt.want) || e.When.Location() != time.UTC {
				t.Errorf("when = %v, want %v in UTC", e.When, tt.want)
			}
			p := props(t, e.Properties)
			if _, has := p["original_timestamp"]; has != tt.original {
				t.Errorf("original_timestamp present = %v, want %v (%v)", has, tt.original, p)
			}
			if tt.original && p["original_timestamp"] != tt.in {
				t.Errorf("original_timestamp = %v, want %q", p["original_timestamp"], tt.in)
			}
			if p["files"] == nil {
				t.Error("the metadata of the event was lost")
			}
		})
	}
}

func TestOriginalTimestampIsBounded(t *testing.T) {
	res, _ := Normalize([]RawEvent{event(func(e *RawEvent) { e.Timestamp = strings.Repeat("é", 100) })}, now)
	got := props(t, res.Entries[0].Properties)["original_timestamp"].(string)
	if len(got) > 64 || got == "" {
		t.Errorf("original_timestamp has %d bytes", len(got))
	}
	for _, r := range got {
		if r == '�' {
			t.Errorf("the cut left a broken character in %q", got)
		}
	}
}

func TestOneBadEventDoesNotHoldBackTheOthers(t *testing.T) {
	events := make([]RawEvent, 100)
	for i := range events {
		events[i] = event(func(e *RawEvent) { e.Event = fmt.Sprintf("server:sftp.write-%d", i) })
	}
	events[40] = event(func(e *RawEvent) { e.IP = "garbage" })
	events[41] = event(func(e *RawEvent) { e.Event = "auth:login" })

	res, err := Normalize(events, now)
	if err != nil || res.Skipped != 2 || len(res.Entries) != 98 {
		t.Fatalf("result: %d entries, %d skipped, %v; want 98 and 2", len(res.Entries), res.Skipped, err)
	}
	// Order is kept.
	if res.Entries[39].Event != "server:sftp.write-39" || res.Entries[40].Event != "server:sftp.write-42" {
		t.Errorf("order changed around the skipped events: %s, %s", res.Entries[39].Event, res.Entries[40].Event)
	}
}

func TestNormalizeBatchLimit(t *testing.T) {
	events := make([]RawEvent, MaxBatch+1)
	if _, err := Normalize(events, now); !errors.Is(err, ErrTooMany) {
		t.Errorf("error = %v, want ErrTooMany", err)
	}
	if res, err := Normalize(events[:MaxBatch], now); err != nil || res.Skipped != MaxBatch {
		t.Errorf("a full batch: %d skipped, %v", res.Skipped, err)
	}
	if res, err := Normalize(nil, now); err != nil || len(res.Entries) != 0 || res.Entries == nil {
		t.Errorf("an empty batch: %+v, %v", res, err)
	}
}

func TestProcess(t *testing.T) {
	good := `{"user":"` + userUUID + `","server":"` + serverUUID + `","event":"server:power.start","metadata":{"a":1},"ip":"203.0.113.9","timestamp":"2026-10-22T11:00:00Z"}`
	tests := []struct {
		name    string
		body    string
		entries int
		skipped int
		err     bool
	}{
		{"a good event", `{"data":[` + good + `]}`, 1, 0, false},
		{"an empty list", `{"data":[]}`, 0, 0, false},
		{"a null user", strings.Replace(`{"data":[`+good+`]}`, `"user":"`+userUUID+`"`, `"user":null`, 1), 1, 0, false},
		{"a user of the wrong type", strings.Replace(`{"data":[`+good+`]}`, `"user":"`+userUUID+`"`, `"user":5`, 1), 0, 1, false},
		{"an element that is not an object", `{"data":[5,"x",null,` + good + `]}`, 1, 3, false},
		{"a metadata field of any type", strings.Replace(`{"data":[`+good+`]}`, `{"a":1}`, `"text"`, 1), 1, 0, false},
		{"extra fields are ignored", strings.Replace(`{"data":[`+good+`]}`, `"ip"`, `"extra":1,"ip"`, 1), 1, 0, false},

		{"not JSON", `nope`, 0, 0, true},
		{"no data", `{}`, 0, 0, true},
		{"data is null", `{"data":null}`, 0, 0, true},
		{"data is not a list", `{"data":{}}`, 0, 0, true},
		{"the body is a list", `[` + good + `]`, 0, 0, true},
		{"empty", ``, 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := Process([]byte(tt.body), now)
			if (err != nil) != tt.err {
				t.Fatalf("error = %v, want error=%v", err, tt.err)
			}
			if !tt.err && (len(res.Entries) != tt.entries || res.Skipped != tt.skipped) {
				t.Errorf("%d entries, %d skipped; want %d and %d", len(res.Entries), res.Skipped, tt.entries, tt.skipped)
			}
		})
	}

	items := strings.Repeat(good+",", MaxBatch) + good
	if _, err := Process([]byte(`{"data":[`+items+`]}`), now); !errors.Is(err, ErrTooMany) {
		t.Errorf("too many events: error = %v", err)
	}
}
