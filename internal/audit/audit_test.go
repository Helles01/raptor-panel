package audit

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/nodes"
	"github.com/Helles01/raptor-panel/internal/servers"
)

var now = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func run(in Input) Report { return Run(in, now) }

var goodHash = func() string {
	h, err := auth.HashPassword("a long enough password")
	if err != nil {
		panic(err)
	}
	return h
}()

func find(r Report, name string) Section {
	for _, s := range r.Sections {
		if s.Name == name {
			return s
		}
	}
	return Section{}
}

func messages(s Section) string {
	var parts []string
	for _, f := range s.Findings {
		parts = append(parts, string(f.Severity)+" "+f.Ref+": "+f.Message)
	}
	return strings.Join(parts, "\n")
}

func TestRunWithNothing(t *testing.T) {
	r := run(Input{})

	want := []string{"egg_variables", "egg_images", "schedules", "api_keys", "subusers", "servers", "nodes", "locations", "allocations", "users", "recovery_tokens", "settings"}
	if len(r.Sections) != len(want) {
		t.Fatalf("%d sections, want %d", len(r.Sections), len(want))
	}
	for i, name := range want {
		if r.Sections[i].Name != name || r.Sections[i].Checked != 0 || len(r.Sections[i].Findings) != 0 {
			t.Errorf("section %d = %+v, want %s with nothing", i, r.Sections[i], name)
		}
	}
	if r.HasErrors() || r.Count(Warning) != 0 {
		t.Error("nothing to check must find nothing")
	}
}

func TestEggVariables(t *testing.T) {
	r := run(Input{EggVariables: []EggVariable{
		{ID: 1, Egg: "Paper", EnvName: "SERVER_JARFILE", Rules: "required|string|max:20"},
		{ID: 2, Egg: "Paper", EnvName: "BROKEN", Rules: "required|no_such_rule"},
		{ID: 3, Egg: "Paper", EnvName: "SERVER_MEMORY", Rules: "required|string"},
		{ID: 4, Egg: "Paper", EnvName: "with space", Rules: "string"},
	}})

	s := find(r, "egg_variables")
	if s.Checked != 4 || len(s.Findings) != 3 {
		t.Fatalf("checked %d, findings:\n%s", s.Checked, messages(s))
	}
	if s.Findings[0].Severity != Error || !strings.Contains(s.Findings[0].Ref, "egg_variables#2 (Paper BROKEN)") || !strings.Contains(s.Findings[0].Message, "nobody can set") {
		t.Errorf("first finding = %+v", s.Findings[0])
	}
	if s.Findings[1].Severity != Warning || !strings.Contains(s.Findings[1].Message, "reserved") {
		t.Errorf("second finding = %+v", s.Findings[1])
	}
	if s.Findings[2].Severity != Warning || !strings.Contains(s.Findings[2].Message, "1 to 191") {
		t.Errorf("third finding = %+v", s.Findings[2])
	}
}

func TestEggImages(t *testing.T) {
	r := run(Input{EggImages: []EggImages{
		{ID: 1, Egg: "Paper", JSON: `{"Java 17":"ghcr.io/pterodactyl/yolks:java_17","Java 21":"ghcr.io/pterodactyl/yolks:java_21"}`},
		{ID: 2, Egg: "Bad", JSON: `not json`},
		{ID: 3, Egg: "Empty", JSON: `{}`},
		{ID: 4, Egg: "Null", JSON: `null`},
		{ID: 5, Egg: "List", JSON: `["a"]`},
		{ID: 6, Egg: "Odd", JSON: `{"b":"has space","a":"semi;colon","c":"fine:1"}`},
	}})

	s := find(r, "egg_images")
	var errs, warns int
	for _, f := range s.Findings {
		if f.Severity == Error {
			errs++
		} else {
			warns++
		}
	}
	if s.Checked != 6 || errs != 4 || warns != 2 {
		t.Fatalf("checked %d, %d errors, %d warnings:\n%s", s.Checked, errs, warns, messages(s))
	}
	if !strings.Contains(s.Findings[0].Message, "not a JSON object") {
		t.Errorf("the first finding must be about the broken JSON: %s", s.Findings[0].Message)
	}
	last := s.Findings[len(s.Findings)-2:]
	if !strings.Contains(last[0].Message, `"a"`) || !strings.Contains(last[1].Message, `"b"`) {
		t.Errorf("the images of one egg must be reported in the order of their names:\n%s", messages(s))
	}
}

func TestSchedules(t *testing.T) {
	r := run(Input{Schedules: []Schedule{
		{ID: 1, Name: "Nightly", Minute: "0", Hour: "3", DayOfMonth: "*", Month: "*", DayOfWeek: "*", Tasks: []ScheduleTask{
			{ID: 10, Action: "command", Payload: "say hi", Sequence: 1},
			{ID: 11, Action: "power", Payload: "restart", TimeOffset: 30, Sequence: 2},
		}},
		{ID: 2, Name: "Never", Minute: "0", Hour: "0", DayOfMonth: "31", Month: "2", DayOfWeek: "*"},
		{ID: 3, Name: "Words", Minute: "often", Hour: "*", DayOfMonth: "*", Month: "*", DayOfWeek: "*"},
		{ID: 4, Name: "", Minute: "*", Hour: "*", DayOfMonth: "*", Month: "*", DayOfWeek: "*"},
		{ID: 5, Name: "Tasks", Minute: "*", Hour: "*", DayOfMonth: "*", Month: "*", DayOfWeek: "*", Tasks: []ScheduleTask{
			{ID: 20, Action: "reboot", Payload: "x", Sequence: 1},
			{ID: 21, Action: "power", Payload: "explode", Sequence: 2},
			{ID: 22, Action: "command", Payload: "ok", Sequence: 2},
			{ID: 23, Action: "command", Payload: "slow", TimeOffset: 5000, Sequence: 3},
		}},
	}})

	s := find(r, "schedules")
	got := messages(s)
	for _, want := range []string{
		"error schedules#2 (Never): the cron fields",
		"never match a date",
		"error schedules#3 (Words): the cron fields",
		"are not an expression Raptor can run",
		"warning schedules#4: the name: name: is required",
		`error schedules#5 (Tasks), task 20: the action "reboot" is not known`,
		"warning schedules#5 (Tasks), task 21: the power action must be",
		"warning schedules#5 (Tasks), task 22: the position 2 is used by another task",
		"warning schedules#5 (Tasks), task 23: the time offset must be between",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "schedules#1") {
		t.Errorf("the good schedule was reported:\n%s", got)
	}
	if s.Checked != 5 {
		t.Errorf("checked %d, want 5", s.Checked)
	}
}

func TestAPIKeys(t *testing.T) {
	r := run(Input{APIKeys: []APIKey{
		{ID: 1, Identifier: "good", AllowedIPs: []string{"203.0.113.0/24", "198.51.100.7"}},
		{ID: 2, Identifier: "open"},
		{ID: 3, Identifier: "none-readable", AllowedIPs: []string{"nonsense", "300.1.1.1"}},
		{ID: 4, Identifier: "some-readable", AllowedIPs: []string{"203.0.113.9", "nonsense"}},
		{ID: 5, Identifier: "empty-entry", AllowedIPs: []string{""}},
	}})

	s := find(r, "api_keys")
	if len(s.Findings) != 3 {
		t.Fatalf("findings:\n%s", messages(s))
	}
	if s.Findings[0].Severity != Error || !strings.Contains(s.Findings[0].Message, "none of the 2") || !strings.Contains(s.Findings[0].Ref, "none-readable") {
		t.Errorf("finding 0 = %+v", s.Findings[0])
	}
	if s.Findings[1].Severity != Warning || !strings.Contains(s.Findings[1].Message, "1 of the 2") {
		t.Errorf("finding 1 = %+v", s.Findings[1])
	}
	if s.Findings[2].Severity != Error || !strings.Contains(s.Findings[2].Ref, "empty-entry") {
		t.Errorf("finding 2 = %+v", s.Findings[2])
	}
}

func TestSubusers(t *testing.T) {
	r := run(Input{Subusers: []Subuser{
		{ID: 1, ServerID: 5, Permissions: []string{"control.console", "websocket.connect"}},
		{ID: 2, ServerID: 5, Permissions: []string{"control.console", "file.teleport", "nope"}},
		{ID: 3, ServerID: 6},
	}})

	s := find(r, "subusers")
	if len(s.Findings) != 1 || s.Findings[0].Severity != Warning || !strings.Contains(s.Findings[0].Message, "file.teleport, nope") || !strings.Contains(s.Findings[0].Ref, "subusers#2 (server 5)") {
		t.Errorf("findings:\n%s", messages(s))
	}
}

func validServer() Server {
	return Server{ID: 1, Name: "Survival", Image: "ghcr.io/pterodactyl/yolks:java_17", Startup: "java -jar server.jar",
		Limits: servers.Limits{Memory: 1024, Swap: 0, Disk: 5000, IO: 500, CPU: 0}}
}

func TestServers(t *testing.T) {
	bad := validServer()
	bad.ID = 2
	bad.Name = "Bad"
	bad.Limits.IO = 5
	spaced := validServer()
	spaced.ID = 3
	spaced.Name = "Spaced"
	spaced.Image = "has space"

	r := run(Input{Servers: []Server{validServer(), bad, spaced}})

	s := find(r, "servers")
	if s.Checked != 3 || len(s.Findings) != 2 {
		t.Fatalf("findings:\n%s", messages(s))
	}
	for _, f := range s.Findings {
		if f.Severity != Warning || !strings.Contains(f.Message, "refused until") {
			t.Errorf("finding = %+v", f)
		}
	}
	if !strings.Contains(s.Findings[0].Message, "io") || !strings.Contains(s.Findings[1].Message, "docker_image") {
		t.Errorf("findings:\n%s", messages(s))
	}
}

func validNode() Node {
	n := nodes.DefaultNewNode()
	n.Name = "Node 1"
	n.LocationID = 1
	n.FQDN = "node1.example.com"
	n.Memory = 8192
	n.Disk = 100000
	return Node{ID: 1, Settings: n}
}

func TestNodesAndLocations(t *testing.T) {
	bad := validNode()
	bad.ID = 2
	bad.Settings.DaemonBase = "/var/../etc"

	r := run(Input{
		Nodes:     []Node{validNode(), bad},
		Locations: []Location{{ID: 1, Short: "eu", Long: "Europe"}, {ID: 2, Short: "", Long: "x"}, {ID: 3, Short: "us", Long: "a\x07"}},
	})

	n := find(r, "nodes")
	if len(n.Findings) != 1 || !strings.Contains(n.Findings[0].Message, "daemon_base") || !strings.Contains(n.Findings[0].Ref, "nodes#2 (Node 1)") {
		t.Errorf("nodes:\n%s", messages(n))
	}
	l := find(r, "locations")
	if l.Checked != 3 || len(l.Findings) != 2 {
		t.Errorf("locations:\n%s", messages(l))
	}
}

func TestAllocations(t *testing.T) {
	r := run(Input{Allocations: []Allocation{
		{ID: 1, NodeID: 1, IP: "10.0.0.5", Port: 25565, Alias: "mc.example.com"},
		{ID: 2, NodeID: 1, IP: "2001:db8::1", Port: 1},
		{ID: 3, NodeID: 1, IP: "node.example.com", Port: 25565},
		{ID: 4, NodeID: 1, IP: "10.0.0.5", Port: 0},
		{ID: 5, NodeID: 1, IP: "10.0.0.5", Port: 70000},
		{ID: 6, NodeID: 1, IP: "10.0.0.5", Port: 25566, Alias: "bad alias!"},
		{ID: 7, NodeID: 1, IP: "10.0.0.5", Port: 65535},
		{ID: 8, NodeID: 1, IP: "10.0.0.5", Port: 65536},
	}})

	s := find(r, "allocations")
	want := []struct {
		sev Severity
		in  string
	}{
		{Error, "allocations#3"},
		{Error, "allocations#4"},
		{Error, "allocations#5"},
		{Warning, "allocations#6"},
		{Error, "allocations#8"},
	}
	if len(s.Findings) != len(want) {
		t.Fatalf("findings:\n%s", messages(s))
	}
	for i, w := range want {
		if s.Findings[i].Severity != w.sev || !strings.Contains(s.Findings[i].Ref, w.in) {
			t.Errorf("finding %d = %+v, want %s in %s", i, s.Findings[i], w.sev, w.in)
		}
	}
}

func TestUsers(t *testing.T) {
	r := run(Input{Users: []User{
		{ID: 1, Username: "ada", Email: "ada@example.com", PasswordHash: goodHash},
		{ID: 2, Username: "reference", Email: "r@example.com", PasswordHash: "$2y" + goodHash[3:], UsesTOTP: true, HasTOTPSecret: true},
		{ID: 3, Username: "nohash", Email: "n@example.com", PasswordHash: "plain-text"},
		{ID: 4, Username: "locked", Email: "l@example.com", PasswordHash: goodHash, UsesTOTP: true},
		{ID: 5, Username: "Capitals", Email: "c@example.com", PasswordHash: goodHash},
		{ID: 6, Username: "a", Email: "a@example.com", PasswordHash: goodHash},
		{ID: 7, Username: "mailbad", Email: "not an address", PasswordHash: goodHash},
	}})

	s := find(r, "users")
	got := messages(s)
	for _, want := range []string{
		"error users#3 (nohash): the stored password is not a bcrypt hash",
		"error users#4 (locked): two-factor authentication is on but there is no secret",
		"warning users#5 (Capitals): the name has capital letters",
		"warning users#6 (a): username:",
		"warning users#7 (mailbad): email:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "users#1") || strings.Contains(got, "users#2 ") {
		t.Errorf("a good user was reported:\n%s", got)
	}
	if len(s.Findings) != 5 {
		t.Errorf("%d findings:\n%s", len(s.Findings), got)
	}
}

func TestRecoveryTokens(t *testing.T) {
	r := run(Input{RecoveryTokens: []RecoveryToken{
		{ID: 1, UserID: 3, Token: goodHash},
		{ID: 2, UserID: 3, Token: "ABCDEFGHIJ"},
	}})

	s := find(r, "recovery_tokens")
	if len(s.Findings) != 1 || s.Findings[0].Severity != Error || !strings.Contains(s.Findings[0].Ref, "recovery_tokens#2 (user 3)") {
		t.Errorf("findings:\n%s", messages(s))
	}
}

func TestSettings(t *testing.T) {
	r := run(Input{Settings: []Setting{
		{Key: "app:name", Value: "My Panel"},
		{Key: "app:name", Value: ""},
		{Key: "mail:mailers:smtp:port", Value: "abc"},
		{Key: "something:unknown", Value: "whatever"},
		{Key: "mail:mailers:smtp:password", Value: "secret"},
	}})

	s := find(r, "settings")
	if s.Checked != 5 || len(s.Findings) != 2 {
		t.Fatalf("checked %d, findings:\n%s", s.Checked, messages(s))
	}
	if s.Findings[0].Ref != "settings.app:name" || s.Findings[1].Ref != "settings.mail:mailers:smtp:port" {
		t.Errorf("findings:\n%s", messages(s))
	}
}

func TestReportCountsAndText(t *testing.T) {
	r := run(Input{
		Users:          []User{{ID: 3, Username: "nohash", Email: "n@example.com", PasswordHash: "x"}},
		RecoveryTokens: []RecoveryToken{{ID: 1, UserID: 3, Token: "x"}},
		Subusers:       []Subuser{{ID: 1, ServerID: 1, Permissions: []string{"nope"}}},
	})

	if r.Count(Error) != 2 || r.Count(Warning) != 1 || !r.HasErrors() {
		t.Errorf("errors %d, warnings %d", r.Count(Error), r.Count(Warning))
	}
	text := r.Text()
	for _, want := range []string{
		"users: 1 checked, 1 errors, 0 warnings\n",
		"  error   users#3 (nohash): the stored password",
		"subusers: 1 checked, 0 errors, 1 warnings\n",
		"  warning subusers#1 (server 1): permissions that do not exist: nope",
		"egg_variables: 0 checked, 0 errors, 0 warnings\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
}

func TestReportWithOnlyWarningsHasNoErrors(t *testing.T) {
	r := run(Input{Subusers: []Subuser{{ID: 1, ServerID: 1, Permissions: []string{"nope"}}}})

	if r.HasErrors() {
		t.Error("a warning is not an error")
	}
}

func TestReportJSON(t *testing.T) {
	r := run(Input{RecoveryTokens: []RecoveryToken{{ID: 1, UserID: 3, Token: "x"}}})

	raw, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var back struct {
		Sections []struct {
			Name     string `json:"name"`
			Checked  int    `json:"checked"`
			Findings []struct {
				Section  string `json:"section"`
				Ref      string `json:"ref"`
				Severity string `json:"severity"`
				Message  string `json:"message"`
			} `json:"findings"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("the report is not JSON: %v\n%s", err, raw)
	}
	if len(back.Sections) != 12 || back.Sections[10].Name != "recovery_tokens" || back.Sections[10].Checked != 1 ||
		len(back.Sections[10].Findings) != 1 || back.Sections[10].Findings[0].Severity != "error" {
		t.Errorf("report = %s", raw)
	}
	if !strings.Contains(string(raw), `"findings": []`) {
		t.Errorf("a section without findings must show an empty list, not null:\n%s", raw)
	}
}

// The rows come from a database that somebody else may have made: a name with an escape sequence in it must not reach the terminal of whoever reads the report.
// The JSON form quotes it, and the text form replaces it.
func TestTextDoesNotCarryControlCharacters(t *testing.T) {
	r := run(Input{Users: []User{{ID: 1, Username: "evil\x1b]0;owned\x07\nname", Email: "e@example.com", PasswordHash: "x"}}})
	text := r.Text()
	if strings.ContainsAny(text, "\x1b\x07\x00\r") {
		t.Fatalf("text carries control characters: %q", text)
	}
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if strings.Contains(line, "evil") && !strings.HasPrefix(line, "  error") && !strings.HasPrefix(line, "  warning") {
			t.Errorf("a name broke out of its line: %q", line)
		}
		if strings.HasPrefix(line, "name") {
			t.Errorf("the rest of a name started a line of its own: %q", line)
		}
	}
	data, err := r.JSON()
	if err != nil || strings.ContainsAny(string(data), "\x1b\x07") {
		t.Errorf("json: %v, %q", err, data)
	}
}
