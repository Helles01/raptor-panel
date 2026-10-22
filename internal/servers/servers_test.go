package servers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/eggs"
)

func ptr[T any](v T) *T { return &v }

func sample() Server {
	return Server{
		UUID:        "a1b2c3d4-0000-4000-8000-000000000001",
		Name:        "Test server",
		Description: "for tests",
		Startup:     "java -Xmx{{SERVER_MEMORY}}M -jar {{SERVER_JARFILE}} --port {{SERVER_PORT}} --ip {{SERVER_IP}}",
		Image:       "ghcr.io/example/java:21",
		Memory:      1024, Swap: 0, Disk: 5000, IO: 500, CPU: 100,
		LocationShort: "eu1",
		Allocation:    Allocation{IP: "10.0.0.5", Port: 25565},
		Allocations:   []Allocation{{"10.0.0.5", 25565}, {"10.0.0.5", 25566}, {"10.0.0.6", 30000}},
		Variables: []Variable{
			{EnvName: "SERVER_JARFILE", Default: "server.jar", UserViewable: true},
			{EnvName: "MC_VERSION", Default: "latest", Value: ptr("1.21"), UserViewable: true},
			{EnvName: "RCON_PASSWORD", Default: "", Value: ptr("s3cret"), UserViewable: false},
		},
		Egg: Egg{UUID: "11111111-2222-4333-8444-555555555555", FileDenylist: []string{"secret.txt"}},
	}
}

func TestEnvironment(t *testing.T) {
	s := sample()
	s.Variables = append(s.Variables, Variable{EnvName: "P_SERVER_UUID", Default: "forged"})

	env := Environment(s)
	want := map[string]any{
		"SERVER_JARFILE":            "server.jar",
		"MC_VERSION":                "1.21",
		"RCON_PASSWORD":             "s3cret",
		"STARTUP":                   s.Startup,
		"P_SERVER_LOCATION":         "eu1",
		"P_SERVER_UUID":             s.UUID, // the panel's value wins over an egg variable of the same name
		"P_SERVER_ALLOCATION_LIMIT": nil,
	}
	if !reflect.DeepEqual(env, want) {
		t.Errorf("Environment = %v\nwant %v", env, want)
	}

	s.AllocationLimit = ptr(int64(5))
	if got := Environment(s)["P_SERVER_ALLOCATION_LIMIT"]; got != int64(5) {
		t.Errorf("allocation limit = %v, want 5", got)
	}
}

func TestConfigShape(t *testing.T) {
	s := sample()
	s.Threads = ptr("0-3")
	raw, err := json.Marshal(Config(s))
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	keys := func(m map[string]any) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		return out
	}
	if len(got) != 11 {
		t.Errorf("top-level keys = %v", keys(got))
	}
	for _, k := range []string{"uuid", "meta", "suspended", "environment", "invocation", "skip_egg_scripts", "build", "container", "allocations", "mounts", "egg"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing %q", k)
		}
	}

	build := got["build"].(map[string]any)
	wantBuild := map[string]any{
		"memory_limit": 1024.0, "swap": 0.0, "io_weight": 500.0, "cpu_limit": 100.0, "threads": "0-3", "disk_space": 5000.0, "oom_disabled": false,
	}
	if !reflect.DeepEqual(build, wantBuild) {
		t.Errorf("build = %v, want %v", build, wantBuild)
	}

	alloc := got["allocations"].(map[string]any)
	if !reflect.DeepEqual(alloc["default"], map[string]any{"ip": "10.0.0.5", "port": 25565.0}) {
		t.Errorf("default = %v", alloc["default"])
	}
	if !reflect.DeepEqual(alloc["mappings"], map[string]any{"10.0.0.5": []any{25565.0, 25566.0}, "10.0.0.6": []any{30000.0}}) {
		t.Errorf("mappings = %v", alloc["mappings"])
	}
	if got["mounts"].([]any) == nil || len(got["mounts"].([]any)) != 0 {
		t.Errorf("mounts = %v, want an empty array", got["mounts"])
	}
	if !reflect.DeepEqual(got["egg"], map[string]any{"id": s.Egg.UUID, "file_denylist": []any{"secret.txt"}}) {
		t.Errorf("egg = %v", got["egg"])
	}
}

func TestConfigEmptyValuesAreNotNull(t *testing.T) {
	s := sample()
	s.Allocations = nil
	s.Mounts = nil
	s.Egg.FileDenylist = nil
	s.Threads = nil

	raw, _ := json.Marshal(Config(s))
	text := string(raw)
	for _, want := range []string{`"mounts":[]`, `"mappings":{}`, `"file_denylist":[]`, `"threads":null`} {
		if !strings.Contains(text, want) {
			t.Errorf("%s is missing from %s", want, text)
		}
	}
}

func TestConfigMountsAndSuspension(t *testing.T) {
	s := sample()
	s.Status = "suspended"
	s.OOMDisabled = true
	s.SkipScripts = true
	s.Egg.ForceOutgoingIP = true
	s.Mounts = []Mount{{Source: "/srv/mods", Target: "/mods", ReadOnly: true}}

	c := Config(s)
	if !c.Suspended || !c.SkipEggScripts || !c.Build.OOMDisabled || !c.Container.OOMDisabled || !c.Allocations.ForceOutgoingIP || c.Container.RequiresRebuild {
		t.Errorf("flags are wrong: %+v", c)
	}
	if !reflect.DeepEqual(c.Mounts, []WingsMount{{Source: "/srv/mods", Target: "/mods", ReadOnly: true}}) {
		t.Errorf("mounts = %v", c.Mounts)
	}

	s.Status = "installing"
	if Config(s).Suspended {
		t.Error("an installing server is not suspended")
	}
}

func TestLegacy(t *testing.T) {
	s := sample()
	s.Status = "suspended"
	raw, _ := json.Marshal(Legacy(s))

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["suspended"] != 1.0 || got["rebuild"] != false || got["uuid"] != s.UUID {
		t.Errorf("legacy = %v", got)
	}
	build := got["build"].(map[string]any)
	if build["memory"] != 1024.0 || build["image"] != s.Image || !reflect.DeepEqual(build["default"], map[string]any{"ip": "10.0.0.5", "port": 25565.0}) {
		t.Errorf("build = %v", build)
	}
	if !reflect.DeepEqual(got["service"], map[string]any{"egg": s.Egg.UUID, "skip_scripts": false}) {
		t.Errorf("service = %v", got["service"])
	}
}

func TestStartupCommand(t *testing.T) {
	s := sample()
	tests := []struct {
		name    string
		mutate  func(*Server)
		hideAll bool
		want    string
	}{
		{"values shown", nil, false, "java -Xmx1024M -jar server.jar --port 25565 --ip 10.0.0.5"},
		{"hide everything", nil, true, "java -Xmx1024M -jar [hidden] --port 25565 --ip 10.0.0.5"},
		{"hidden variable", func(s *Server) { s.Startup = "run --pw {{RCON_PASSWORD}} --v {{MC_VERSION}}" }, false, "run --pw [hidden] --v 1.21"},
		{"unknown placeholder is kept", func(s *Server) { s.Startup = "run {{NOPE}} {{ SERVER_IP }}" }, false, "run {{NOPE}} {{ SERVER_IP }}"},
		{"no placeholders", func(s *Server) { s.Startup = "./start.sh" }, false, "./start.sh"},
		{"a value is not expanded again", func(s *Server) {
			s.Startup = "run {{MC_VERSION}}"
			s.Variables[1].Value = ptr("{{RCON_PASSWORD}}")
		}, false, "run {{RCON_PASSWORD}}"},
		{"a variable cannot override a built-in", func(s *Server) {
			s.Startup = "run {{SERVER_PORT}}"
			s.Variables = append(s.Variables, Variable{EnvName: "SERVER_PORT", Default: "1", UserViewable: true})
		}, false, "run 25565"},
		{"repeated placeholder", func(s *Server) { s.Startup = "{{SERVER_IP}} {{SERVER_IP}}" }, false, "10.0.0.5 10.0.0.5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := s
			srv.Variables = append([]Variable(nil), s.Variables...)
			if tt.mutate != nil {
				tt.mutate(&srv)
			}
			if got := StartupCommand(srv, tt.hideAll); got != tt.want {
				t.Errorf("StartupCommand = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEggStop(t *testing.T) {
	tests := []struct {
		in   string
		want EggStop
	}{
		{"stop", EggStop{"command", "stop"}},
		{"^C", EggStop{"signal", "C"}},
		{"^SIGINT", EggStop{"signal", "SIGINT"}},
		{"^sigterm", EggStop{"signal", "SIGTERM"}},
		{"", EggStop{"command", ""}},
		{"end ^C", EggStop{"command", "end ^C"}},
		{"^", EggStop{"signal", ""}},
	}
	for _, tt := range tests {
		if got := eggStop(tt.in); got != tt.want {
			t.Errorf("eggStop(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestEggStartup(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want EggStartup
		err  bool
	}{
		{"a string becomes a list", `{"done": ")! For help"}`, EggStartup{Done: []string{")! For help"}, UserInteraction: []string{}}, false},
		{"a list stays", `{"done": ["a","b"], "strip_ansi": true}`, EggStartup{Done: []string{"a", "b"}, UserInteraction: []string{}, StripANSI: true}, false},
		{"non-strings in the list are dropped", `{"done": ["a", 1, null]}`, EggStartup{Done: []string{"a"}, UserInteraction: []string{}}, false},
		{"empty", ``, EggStartup{Done: []string{}, UserInteraction: []string{}}, false},
		{"null", `null`, EggStartup{Done: []string{}, UserInteraction: []string{}}, false},
		{"empty object", `{}`, EggStartup{Done: []string{}, UserInteraction: []string{}}, false},
		{"strip_ansi must be a boolean", `{"strip_ansi": "yes"}`, EggStartup{Done: []string{}, UserInteraction: []string{}}, false},
		{"malformed", `{"done": `, EggStartup{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := eggStartup(tt.raw)
			if (err != nil) != tt.err {
				t.Fatalf("error = %v, want error %v", err, tt.err)
			}
			if !tt.err && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// replacementsOf runs the egg file configuration of sample() with the given JSON and returns the files.
func filesOf(t *testing.T, mutate func(*Server), configFiles string) []EggConfigFile {
	t.Helper()
	s := sample()
	if mutate != nil {
		mutate(&s)
	}
	s.Egg.ConfigFiles = configFiles
	p, err := EggProcessConfiguration(s)
	if err != nil {
		t.Fatal(err)
	}
	return p.Configs
}

func TestEggFilesPlaceholders(t *testing.T) {
	files := filesOf(t, nil, `{
		"server.properties": {"parser": "properties", "find": {
			"server-ip": "0.0.0.0",
			"server-port": "{{server.build.default.port}}",
			"motd": "{{server.uuid}} on {{env.MC_VERSION}} at {{server.build.default.ip}}",
			"max-memory": "{{server.build.memory}}",
			"query": "{{config.docker.interface}} {{config.anything}}",
			"unknown": "{{foo}} {{server.nope}} {{env.NOPE}} {{}}",
			"legacy": "{{env.SERVER_MEMORY}} {{env.SERVER_IP}} {{env.SERVER_PORT}} {{server.build.env.SERVER_PORT}} {{config.docker.interface}}"
		}}
	}`)
	if len(files) != 1 || files[0].File != "server.properties" || files[0].Parser != "properties" {
		t.Fatalf("files = %+v", files)
	}

	got := map[string]any{}
	var order []string
	for _, r := range files[0].Replace {
		got[r.Match] = r.ReplaceWith
		order = append(order, r.Match)
	}
	want := map[string]any{
		"server-ip":   "0.0.0.0",
		"server-port": "25565",
		"motd":        "a1b2c3d4-0000-4000-8000-000000000001 on 1.21 at 10.0.0.5",
		"max-memory":  "1024",
		"query":       "{{config.docker.network.interface}} {{config.anything}}",
		"unknown":     "{{foo}}   {{}}", // two unresolved placeholders became empty, their spaces stay
		"legacy":      "{{server.build.memory}} {{server.build.default.ip}} {{server.build.default.port}} {{server.build.default.port}} {{config.docker.network.interface}}",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("replacements differ:\n got  %v\n want %v", got, want)
	}
	// The order of the egg file is the order of the replacements.
	if !reflect.DeepEqual(order, []string{"server-ip", "server-port", "motd", "max-memory", "query", "unknown", "legacy"}) {
		t.Errorf("order = %v", order)
	}
}

func TestEggFilesIfValueAndScalars(t *testing.T) {
	files := filesOf(t, nil, `{
		"config.yml": {"parser": "yaml", "find": {
			"online-mode": false,
			"max-players": 20,
			"big": 12345678901234567890,
			"ratio": 1.50,
			"nothing": null,
			"mode": {"survival": "{{env.MC_VERSION}}", "creative": 1, "": "empty"}
		}}
	}`)
	raw, err := json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"parser":"yaml","file":"config.yml","replace":[` +
		`{"match":"online-mode","replace_with":false},` +
		`{"match":"max-players","replace_with":20},` +
		`{"match":"big","replace_with":12345678901234567890},` +
		`{"match":"ratio","replace_with":1.50},` +
		`{"match":"nothing","replace_with":null},` +
		`{"match":"mode","if_value":"survival","replace_with":"1.21"},` +
		`{"match":"mode","if_value":"creative","replace_with":1},` +
		`{"match":"mode","replace_with":"empty"}]}]`
	if string(raw) != want {
		t.Errorf("got  %s\nwant %s", raw, want)
	}
}

func TestEggFilesPlaceholdersAreExpandedOnce(t *testing.T) {
	// A server value that looks like a placeholder must not be expanded, or whoever can edit a variable could read others.
	files := filesOf(t, func(s *Server) {
		s.Variables[1].Value = ptr("{{env.RCON_PASSWORD}}")
	}, `{"a.cfg": {"parser": "file", "find": {"version": "{{env.MC_VERSION}}", "pw": "{{env.RCON_PASSWORD}}"}}}`)

	got := map[string]any{}
	for _, r := range files[0].Replace {
		got[r.Match] = r.ReplaceWith
	}
	if got["version"] != "{{env.RCON_PASSWORD}}" || got["pw"] != "s3cret" {
		t.Errorf("replacements = %v", got)
	}
}

func TestEggFilesSkippedEntries(t *testing.T) {
	files := filesOf(t, nil, `{
		"no-find.txt": {"parser": "file"},
		"null-find.txt": {"parser": "file", "find": null},
		"no-parser.txt": {"find": {"a": "b"}},
		"empty-parser.txt": {"parser": "", "find": {"a": "b"}},
		"number.txt": 5,
		"list.txt": [1],
		"string-find.txt": {"parser": "file", "find": "x"},
		"list-find.txt": {"parser": "file", "find": ["x"]},
		"list-value.txt": {"parser": "file", "find": {"a": ["x"], "b": {"if": ["x"]}, "c": "kept"}},
		"ok.txt": {"parser": "file", "find": {"a": "b"}, "extra": "ignored"}
	}`)

	var names []string
	for _, f := range files {
		names = append(names, f.File)
		if f.Replace == nil {
			t.Errorf("%s: Replace is nil, which would be null in the JSON", f.File)
		}
	}
	if !reflect.DeepEqual(names, []string{"string-find.txt", "list-find.txt", "list-value.txt", "ok.txt"}) {
		t.Errorf("files = %v", names)
	}
	for _, f := range files {
		switch f.File {
		case "string-find.txt", "list-find.txt":
			if len(f.Replace) != 0 {
				t.Errorf("%s: replace = %v, want none", f.File, f.Replace)
			}
		case "list-value.txt":
			// arrays and objects cannot be written into a file; the entries that can are kept
			if !reflect.DeepEqual(f.Replace, []Replacement{{Match: "c", ReplaceWith: "kept"}}) {
				t.Errorf("list-value.txt: replace = %v", f.Replace)
			}
		}
	}
}

func TestEggFilesNoConfiguration(t *testing.T) {
	for _, raw := range []string{"", "  ", "null", "{}"} {
		if files := filesOf(t, nil, raw); files == nil || len(files) != 0 {
			t.Errorf("%q: files = %#v, want an empty list", raw, files)
		}
	}
}

func TestEggFilesErrors(t *testing.T) {
	deep := strings.Repeat(`{"a":`, 40) + `1` + strings.Repeat(`}`, 40)
	for name, raw := range map[string]string{
		"malformed":       `{"a": `,
		"not an object":   `[1, 2]`,
		"a string":        `"text"`,
		"trailing data":   `{} {}`,
		"trailing text":   `{} x`,
		"too deeply":      `{"f": {"parser": "p", "find": ` + deep + `}}`,
		"bare value":      `5`,
		"unquoted key":    `{a: 1}`,
		"duplicate close": `{"a": 1}}`,
	} {
		s := sample()
		s.Egg.ConfigFiles = raw
		if _, err := EggProcessConfiguration(s); err == nil {
			t.Errorf("%s: want an error for %q", name, raw)
		}
	}
}

func TestEggProcessConfigurationErrorsNameTheField(t *testing.T) {
	s := sample()
	s.Egg.ConfigStartup = `{`
	if _, err := EggProcessConfiguration(s); err == nil || !strings.Contains(err.Error(), "startup") {
		t.Errorf("error = %v", err)
	}
	s = sample()
	s.Egg.ConfigFiles = `{`
	if _, err := EggProcessConfiguration(s); err == nil || !strings.Contains(err.Error(), "file configuration") {
		t.Errorf("error = %v", err)
	}
}

func TestParseOrderedDuplicateKeys(t *testing.T) {
	v, err := parseOrdered(`{"a": 1, "b": 2, "a": 3}`)
	if err != nil {
		t.Fatal(err)
	}
	o := v.(*object)
	if !reflect.DeepEqual(o.keys, []string{"a", "b"}) || o.vals["a"] != json.Number("3") {
		t.Errorf("keys %v, a = %v: the first position and the last value are kept", o.keys, o.vals["a"])
	}
}

func TestPHPString(t *testing.T) {
	tests := []struct {
		in   any
		want string
	}{
		{"x", "x"}, {json.Number("512"), "512"}, {true, "1"}, {false, ""}, {nil, ""}, {map[string]any{}, ""}, {[]any{1}, ""},
	}
	for _, tt := range tests {
		if got := phpString(tt.in); got != tt.want {
			t.Errorf("phpString(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestLookup(t *testing.T) {
	doc := map[string]any{"a": map[string]any{"b": []any{"x", map[string]any{"c": "deep"}}}, "n": nil}
	tests := []struct {
		path string
		want any
	}{
		{"a.b.0", "x"}, {"a.b.1.c", "deep"}, {"a.b.2", nil}, {"a.b.-1", nil}, {"a.b.x", nil}, {"a.z", nil}, {"n", nil}, {"n.x", nil}, {"", nil},
	}
	for _, tt := range tests {
		if got := lookup(doc, tt.path); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("lookup(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

// The seeded eggs of the reference are real input: each must build, and the placeholders in them must be resolved.
func TestReferenceEggs(t *testing.T) {
	paths, _ := filepath.Glob(filepath.Join("..", "..", "reference", "database", "Seeders", "eggs", "*", "*.json"))
	if len(paths) == 0 {
		t.Skip("reference/ is not present")
	}

	checked := 0
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		egg, err := eggs.Parse(f)
		f.Close()
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(path), err)
			continue
		}

		s := sample()
		s.Egg.ConfigFiles, s.Egg.ConfigStartup, s.Egg.ConfigStop = egg.ConfigFiles, egg.ConfigStartup, egg.ConfigStop
		p, err := EggProcessConfiguration(s)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(path), err)
			continue
		}
		raw, _ := json.Marshal(p)
		// Of the placeholders the panel fills in, only the ones it rewrote for the node may remain.
		for _, left := range placeholder.FindAllString(string(raw), -1) {
			filledByPanel := strings.HasPrefix(left, "{{server.") || strings.HasPrefix(left, "{{env.")
			if filledByPanel && !strings.HasPrefix(left, "{{server.build.") {
				t.Errorf("%s: %s was not resolved", filepath.Base(path), left)
			}
		}
		checked++
	}
	if checked < 14 {
		t.Errorf("only %d reference eggs were checked, want 14", checked)
	}
}

func TestVanillaEgg(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "reference", "database", "Seeders", "eggs", "minecraft", "egg-vanilla-minecraft.json"))
	if err != nil {
		t.Skip("reference/ is not present")
	}
	egg, err := eggs.Parse(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}

	s := sample()
	s.Egg.ConfigFiles, s.Egg.ConfigStartup, s.Egg.ConfigStop = egg.ConfigFiles, egg.ConfigStartup, egg.ConfigStop
	p, err := EggProcessConfiguration(s)
	if err != nil {
		t.Fatal(err)
	}

	out, _ := json.Marshal(p)
	want := `{"startup":{"done":[")! For help, type "],"user_interaction":[],"strip_ansi":false},` +
		`"stop":{"type":"command","value":"stop"},` +
		`"configs":[{"parser":"properties","file":"server.properties","replace":[` +
		`{"match":"server-ip","replace_with":"0.0.0.0"},` +
		`{"match":"server-port","replace_with":"25565"},` +
		`{"match":"query.port","replace_with":"25565"}]}]}`
	if string(out) != want {
		t.Errorf("got  %s\nwant %s", out, want)
	}
}
