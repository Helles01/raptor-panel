package activity

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

const localePath = "../api/web/locales/en/activity.json"

// locale reads the activity texts of the frontend and returns, for every event, the text and the names of its placeholders.
func locale(t *testing.T) map[string][]string {
	t.Helper()
	raw, err := os.ReadFile(localePath)
	if err != nil {
		t.Fatalf("reading the activity texts: %v", err)
	}
	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatalf("activity texts are not JSON: %v", err)
	}

	placeholder := regexp.MustCompile(`\{\{\s*([A-Za-z0-9_-]+)`)
	out := map[string][]string{}
	var walk func(path []string, node any)
	walk = func(path []string, node any) {
		switch v := node.(type) {
		case map[string]any:
			for k, child := range v {
				walk(append(slices.Clone(path), k), child)
			}
		case string:
			last := path[len(path)-1]
			path[len(path)-1] = strings.TrimSuffix(strings.TrimSuffix(last, "_one"), "_other")
			event := path[0] + ":" + strings.Join(path[1:], ".")
			for _, m := range placeholder.FindAllStringSubmatch(v, -1) {
				out[event] = append(out[event], m[1])
			}
			if _, ok := out[event]; !ok {
				out[event] = nil
			}
		}
	}
	walk(nil, tree)
	return out
}

func TestSpecsAreWellFormed(t *testing.T) {
	name := regexp.MustCompile(`^(auth|user|server):[a-z0-9]+([.-][a-z0-9]+)*$`)
	prop := regexp.MustCompile(`^[a-z][a-z_]*$`)
	seen := map[string]bool{}
	for _, s := range Specs {
		if !name.MatchString(s.Event) {
			t.Errorf("event %q has an odd name", s.Event)
		}
		if seen[s.Event] {
			t.Errorf("event %q is listed twice", s.Event)
		}
		seen[s.Event] = true
		dup := map[string]bool{}
		for _, p := range s.Properties {
			if !prop.MatchString(p) || dup[p] {
				t.Errorf("event %s: property %q is odd or repeated", s.Event, p)
			}
			dup[p] = true
		}
	}
}

func TestEveryPanelEventHasATextThatItCanFill(t *testing.T) {
	texts := locale(t)
	for _, s := range Specs {
		holders, ok := texts[s.Event]
		if !ok {
			t.Errorf("event %s has no text in the frontend", s.Event)
			continue
		}
		for _, h := range holders {
			if h == "count" {
				continue // the number of files, which the frontend counts itself
			}
			if !slices.Contains(s.Properties, h) {
				t.Errorf("event %s: the text uses {{%s}} but the event has no such property", s.Event, h)
			}
		}
	}
}

func TestEveryNodeEventHasAText(t *testing.T) {
	texts := locale(t)
	for _, e := range DaemonEvents() {
		if _, ok := texts[e]; !ok {
			t.Errorf("node event %s has no text in the frontend", e)
		}
	}
}

// A text without an event would mean an event the panel forgot to write, or one a node is not let to send.
func TestEveryTextHasAnEvent(t *testing.T) {
	known := map[string]bool{
		// Written by an old panel for uploads, and never since; the text stays for the old entries.
		"server:file.upload": true,
	}
	for _, s := range Specs {
		known[s.Event] = true
	}
	for _, e := range DaemonEvents() {
		known[e] = true
	}

	var orphans []string
	for e := range locale(t) {
		if !known[e] {
			orphans = append(orphans, e)
		}
	}
	sort.Strings(orphans)
	if len(orphans) > 0 {
		t.Errorf("the frontend has texts for events nobody writes: %v", orphans)
	}
}

// The power events and the command are written by the panel for a call of the API and by a node for the console of the browser. No
// other event may be both, or a node could forge it.
func TestPanelAndNodeEventsAreApart(t *testing.T) {
	shared := []string{ServerPowerStart, ServerPowerStop, ServerPowerRestart, ServerPowerKill, ServerConsoleCommand}
	var got []string
	for _, e := range DaemonEvents() {
		if slices.ContainsFunc(Specs, func(s Spec) bool { return s.Event == e }) {
			got = append(got, e)
		}
	}
	if !slices.Equal(got, shared) {
		t.Errorf("events of both the panel and a node: %v, want exactly %v: a node could forge any other", got, shared)
	}
}

func TestProperties(t *testing.T) {
	got, err := Properties(ServerStartupEdit, map[string]any{"variable": "MOTD", "old": nil, "new": "hello"})
	if err != nil {
		t.Fatalf("Properties: %v", err)
	}

	var back map[string]any
	if err := json.Unmarshal([]byte(got), &back); err != nil {
		t.Fatalf("result %q is not JSON: %v", got, err)
	}
	if back["variable"] != "MOTD" || back["new"] != "hello" || back["old"] != nil || len(back) != 3 {
		t.Errorf("properties = %v", back)
	}
}

func TestPropertiesWithNothing(t *testing.T) {
	got, err := Properties(ServerReinstall, nil)
	if err != nil || got != "{}" {
		t.Errorf("Properties = %q, %v; an event without properties stores the empty object", got, err)
	}
}

func TestPropertiesKeepNestedValues(t *testing.T) {
	files := []map[string]string{{"from": "a", "to": "b"}}

	got, err := Properties(ServerFileRename, map[string]any{"directory": "/", "files": files})

	if err != nil || !strings.Contains(got, `"files":[{"from":"a","to":"b"}]`) {
		t.Errorf("Properties = %q, %v", got, err)
	}
}

func TestPropertiesRefusals(t *testing.T) {
	tests := []struct {
		name   string
		event  string
		values map[string]any
		want   string
	}{
		{"unknown event", "server:made.up", nil, "not an event of the panel"},
		{"a node event", "server:sftp.write", map[string]any{"files": []string{"a"}}, "not an event of the panel"},
		{"empty event", "", nil, "not an event of the panel"},
		{"missing property", ServerStartupEdit, map[string]any{"variable": "MOTD", "new": "x"}, "lacks properties [old]"},
		{"unknown property", ServerStartupEdit, map[string]any{"variable": "MOTD", "old": "", "new": "x", "secret": "s"}, "unknown ones [secret]"},
		{"properties for an event that has none", ServerReinstall, map[string]any{"why": "x"}, "unknown ones [why]"},
		{"nothing for an event that needs some", UserEmailChanged, nil, "lacks properties [old new]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Properties(tt.event, tt.values)

			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want one containing %q", err, tt.want)
			}
			if got != "" {
				t.Errorf("a refused event must store nothing: %q", got)
			}
		})
	}
}

func TestDaemonEvent(t *testing.T) {
	tests := []struct {
		event string
		ok    bool
	}{
		{"server:power.start", true},
		{"server:power.kill", true},
		{"server:console.command", true},
		{"server:file.uploaded", true},
		{"server:sftp.rename", true},
		{"server:sftp.create-directory", true},
		{"server:power.terminate", false},
		{"server:power.", false},
		{"server:file.write", false},
		{"server:subuser.create", false},
		{"auth:fail", false},
		{"", false},
		{"SERVER:power.start", false},
		{"server:power.start ", false},
		{"server:power.start\n", false},
		{" server:power.start", false},
	}
	for _, tt := range tests {
		if got := DaemonEvent(tt.event); got != tt.ok {
			t.Errorf("DaemonEvent(%q) = %v, want %v", tt.event, got, tt.ok)
		}
	}
}

func TestDaemonEventsReturnsACopy(t *testing.T) {
	list := DaemonEvents()
	list[0] = "server:forged"

	if DaemonEvent("server:forged") || DaemonEvents()[0] == "server:forged" {
		t.Error("changing the returned list changed the list that decides what a node may send")
	}
}

func TestRedactURL(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"https://example.com/files/server.jar", "https://example.com/files/server.jar"},
		{"http://example.com", "http://example.com"},
		{"https://user:s3cret@example.com/a.zip", "https://example.com/a.zip"},
		{"https://example.com/a.zip?token=abc&X-Amz-Signature=def", "https://example.com/a.zip"},
		{"https://example.com/a.zip#frag", "https://example.com/a.zip"},
		{"https://example.com:8443/a.zip?x=1", "https://example.com:8443/a.zip"},
		{"https://[2001:db8::1]:8443/a?x=1", "https://[2001:db8::1]:8443/a"},
		{"https://example.com/a%20b.zip?x=1", "https://example.com/a%20b.zip"},
		{"ftp://example.com/a.zip", ""},
		{"file:///etc/passwd", ""},
		{"javascript:alert(1)", ""},
		{"//example.com/a", ""},
		{"example.com/a", ""},
		{"https:///a", ""},
		{"https://exa mple.com/a", ""},
		{"", ""},
		{"%zz", ""},
	}
	for _, tt := range tests {
		if got := RedactURL(tt.in); got != tt.want {
			t.Errorf("RedactURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTheFrontendTextsAreRead(t *testing.T) {
	texts := locale(t)

	if len(texts) < 60 {
		t.Errorf("only %d events found in the frontend texts; the walk over the file is broken", len(texts))
	}
	if got := texts["server:startup.edit"]; !slices.Equal(got, []string{"variable", "old", "new"}) {
		t.Errorf("placeholders of server:startup.edit = %v", got)
	}
	if _, ok := texts["server:file.compress"]; !ok {
		t.Error("the plural forms (_one and _other) must count as one event")
	}
}

func TestPropertiesAllowTheRequestMetadata(t *testing.T) {
	values := WithRequestMetadata(map[string]any{}, "203.0.113.9", "Mozilla/5.0")

	got, err := Properties(AuthSuccess, values)
	if err != nil {
		t.Fatalf("Properties: %v", err)
	}

	var back map[string]any
	if err := json.Unmarshal([]byte(got), &back); err != nil || back["ip"] != "203.0.113.9" || back["useragent"] != "Mozilla/5.0" || len(back) != 2 {
		t.Errorf("properties = %s, %v", got, err)
	}
}

func TestPropertiesAllowTheMetadataOnEveryEvent(t *testing.T) {
	for _, s := range Specs {
		values := map[string]any{}
		for _, p := range s.Properties {
			values[p] = "x"
		}
		values["ip"], values["useragent"], values["using_sftp"] = "1.1.1.1", "ua", true

		if _, err := Properties(s.Event, values); err != nil {
			t.Errorf("%s: %v", s.Event, err)
		}
	}
}

func TestPropertiesStillRefuseOtherExtras(t *testing.T) {
	_, err := Properties(AuthSuccess, map[string]any{"ip": "1.1.1.1", "password": "secret"})

	if err == nil || !strings.Contains(err.Error(), "unknown ones [password]") {
		t.Errorf("error = %v", err)
	}
}

func TestWithRequestMetadata(t *testing.T) {
	in := map[string]any{"name": "x"}

	out := WithRequestMetadata(in, "10.0.0.1", "agent")

	if len(in) != 1 {
		t.Error("the values that were passed in were changed")
	}
	if out["name"] != "x" || out["ip"] != "10.0.0.1" || out["useragent"] != "agent" || len(out) != 3 {
		t.Errorf("out = %v", out)
	}
	if got := WithRequestMetadata(nil, "10.0.0.1", ""); got["useragent"] != "" || len(got) != 2 {
		t.Errorf("without values or a browser: %v", got)
	}
}

func TestWithRequestMetadataCutsTheBrowser(t *testing.T) {
	long := strings.Repeat("a", MaxUserAgentLength+10)
	if got := WithRequestMetadata(nil, "1.1.1.1", long)["useragent"]; got != long[:MaxUserAgentLength] {
		t.Errorf("the browser was not cut to %d bytes: %d", MaxUserAgentLength, len(got.(string)))
	}
	exact := strings.Repeat("a", MaxUserAgentLength)
	if got := WithRequestMetadata(nil, "1.1.1.1", exact)["useragent"]; got != exact {
		t.Error("a browser of exactly the limit must be kept whole")
	}

	// A character that would be split is dropped whole.
	multi := strings.Repeat("a", MaxUserAgentLength-1) + "é"
	got := WithRequestMetadata(nil, "1.1.1.1", multi)["useragent"].(string)
	if got != strings.Repeat("a", MaxUserAgentLength-1) || !utf8.ValidString(got) {
		t.Errorf("a split character must be dropped: %q", got[len(got)-3:])
	}
}
