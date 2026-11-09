// Package deploy has no code: its test reads the files of this folder and checks them against the code, so that an example cannot say a variable that the panel
// does not read, leave out one that it does, or hold a value that the panel would refuse.
package deploy

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/config"
)

var variablePattern = regexp.MustCompile(`RAPTOR_[A-Z0-9_]+`)

// codeVariables are the environment variables the code reads, found in the source of the commands and of the configuration.
func codeVariables(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, root := range []string{"../internal", "../cmd"} {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range regexp.MustCompile(`"(RAPTOR_[A-Z0-9_]+)"`).FindAllStringSubmatch(string(body), -1) {
				out[m[1]] = true
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// example reads raptor.env.example: the variables it sets, and the ones it shows commented out, with their values.
func example(t *testing.T) (set, shown map[string]string) {
	t.Helper()
	f, err := os.Open("raptor.env.example")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	set, shown = map[string]string{}, map[string]string{}
	line := regexp.MustCompile(`^(#\s*)?(RAPTOR_[A-Z0-9_]+)=(.*)$`)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if m := line.FindStringSubmatch(sc.Text()); m != nil {
			if m[1] == "" {
				set[m[2]] = m[3]
			} else {
				shown[m[2]] = m[3]
			}
		}
	}
	return set, shown
}

func TestEveryVariableTheCodeReadsIsInTheExample(t *testing.T) {
	set, shown := example(t)
	// RAPTOR_TEST_DSN is the tests'. RAPTOR_LEGACY_APP_KEY is mentioned in a sentence and not set on a line, on purpose: it must never be in a file.
	skip := map[string]bool{"RAPTOR_TEST_DSN": true, "RAPTOR_LEGACY_APP_KEY": true}
	var missing []string
	for name := range codeVariables(t) {
		if _, ok := set[name]; ok || skip[name] {
			continue
		}
		if _, ok := shown[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("the code reads these variables and raptor.env.example does not show them: %v", missing)
	}
	body, _ := os.ReadFile("raptor.env.example")
	if !strings.Contains(string(body), "RAPTOR_LEGACY_APP_KEY") || regexp.MustCompile(`(?m)^#?\s*RAPTOR_LEGACY_APP_KEY=`).Match(body) {
		t.Error("RAPTOR_LEGACY_APP_KEY must be explained and must not be a line that could be filled in")
	}

	// And the other way: no variable that the code does not read.
	code := codeVariables(t)
	for _, vars := range []map[string]string{set, shown} {
		for name := range vars {
			if !code[name] {
				t.Errorf("raptor.env.example has %s, which nothing reads", name)
			}
		}
	}
}

// Every value the example gives is one the panel accepts, so that copying it does not start with a refusal. The secrets are left empty.
func TestTheExampleIsAConfigurationThePanelAccepts(t *testing.T) {
	set, shown := example(t)
	vars := map[string]string{}
	for _, m := range []map[string]string{shown, set} {
		for k, v := range m {
			if v != "" {
				vars[k] = v
			}
		}
	}
	// What an operator has to fill in, as a test would.
	vars["RAPTOR_DB_PASSWORD"] = "x"
	vars["RAPTOR_SECRET_KEY"] = "iDsdi+5AzVWiY8W1v9SHn4lUxVtt4BFUkmPSUgPo5RA="
	// The S3 example is complete when its keys are filled in.
	vars["RAPTOR_BACKUPS_S3_ACCESS_KEY"] = "AKIAEXAMPLE"
	vars["RAPTOR_BACKUPS_S3_SECRET_KEY"] = "secret-value"
	delete(vars, "RAPTOR_CONFIG")

	cfg, err := config.Load("", func(k string) string { return vars[k] })
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]interface{ Validate() error }{
		"app": cfg.App, "db": cfg.DB, "http": cfg.HTTP, "wings": cfg.Wings, "log": cfg.Log, "jobs": cfg.Jobs, "web": cfg.Web, "db_hosts": cfg.DBHosts, "backups": cfg.Backups, "features": cfg.Features,
	} {
		if err := v.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := cfg.Secrets.KeyBytes(); err != nil {
		t.Errorf("secrets: %v", err)
	}

	// The defaults the example shows are the defaults of the panel, so that a line that is commented out says what is true.
	def, err := config.Load("", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"RAPTOR_HTTP_LISTEN": def.HTTP.Listen, "RAPTOR_DB_HOST": def.DB.Host, "RAPTOR_APP_TIMEZONE": def.App.Timezone, "RAPTOR_DB_TLS": def.DB.TLS, "RAPTOR_ASSETS_DIR_DEFAULT": "",
		"RAPTOR_LOG_LEVEL": def.Log.Level, "RAPTOR_BACKUPS_DRIVER": def.Backups.Driver, "RAPTOR_DB_HOSTS_TLS": def.DBHosts.TLS,
	} {
		if got, ok := vars[name]; ok && got != want && name != "RAPTOR_LOG_LEVEL" {
			t.Errorf("%s: the example says %q, the default is %q", name, got, want)
		}
	}
	if shown["RAPTOR_LOG_LEVEL"] != def.Log.Level {
		t.Errorf("the log level: the example says %q, the default is %q", shown["RAPTOR_LOG_LEVEL"], def.Log.Level)
	}
}

func TestNoSecretIsInTheExample(t *testing.T) {
	set, shown := example(t)
	for _, name := range []string{"RAPTOR_DB_PASSWORD", "RAPTOR_SECRET_KEY", "RAPTOR_BACKUPS_S3_ACCESS_KEY", "RAPTOR_BACKUPS_S3_SECRET_KEY"} {
		for _, m := range []map[string]string{set, shown} {
			if v, ok := m[name]; ok && v != "" {
				t.Errorf("%s has a value in the example: %q", name, v)
			}
		}
	}
}

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTheUnit(t *testing.T) {
	unit := read(t, "raptor.service")
	for _, line := range []string{
		"User=raptor", "Group=raptor", "EnvironmentFile=/etc/raptor/raptor.env", "ExecStartPre=/opt/raptor/bin/raptor doctor", "ExecStart=/opt/raptor/bin/raptor serve", "Restart=on-failure",
		"NoNewPrivileges=yes", "ProtectSystem=strict", "ProtectHome=yes", "PrivateTmp=yes", "PrivateDevices=yes", "CapabilityBoundingSet=", "MemoryDenyWriteExecute=yes",
		"RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX", "SystemCallFilter=@system-service", "LockPersonality=yes", "UMask=0077",
	} {
		if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(line)).MatchString(unit) {
			t.Errorf("the unit lacks %q", line)
		}
	}
	for _, bad := range []string{"User=root", "PermitRoot", "AmbientCapabilities=CAP", "ProtectSystem=no", "NoNewPrivileges=no"} {
		if strings.Contains(unit, bad) {
			t.Errorf("the unit has %q", bad)
		}
	}
	// What the unit starts is what the commands are called: raptor serve and raptor doctor exist.
	main := read(t, "../cmd/raptor/main.go")
	for _, name := range []string{`"serve"`, `"doctor"`} {
		if !strings.Contains(main, name) {
			t.Errorf("cmd/raptor has no command %s", name)
		}
	}
}

func TestTheProxiesPassOnTheAddressTheyHaveSeen(t *testing.T) {
	cfg, err := config.Load("", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	nginx, caddy := read(t, "nginx.conf.example"), read(t, "Caddyfile.example")
	// They send the panel where it listens by default, and the address they saw, not the one the client claims.
	if !strings.Contains(nginx, "proxy_pass http://"+cfg.HTTP.Listen+";") || !strings.Contains(caddy, "reverse_proxy "+cfg.HTTP.Listen) {
		t.Errorf("a proxy does not go to the default address %s", cfg.HTTP.Listen)
	}
	if !strings.Contains(nginx, "proxy_set_header X-Forwarded-For $remote_addr;") || strings.Contains(nginx, "$proxy_add_x_forwarded_for") {
		t.Error("nginx passes on what the client said about its address")
	}
	if !strings.Contains(caddy, "header_up X-Forwarded-For {remote_host}") {
		t.Error("caddy does not set the address it saw")
	}
	for name, body := range map[string]string{"nginx": nginx, "caddy": caddy} {
		if !strings.Contains(body, "Strict-Transport-Security") {
			t.Errorf("%s: no HSTS", name)
		}
	}
	// And the trusted proxy the examples talk about is one the panel accepts.
	if _, err := (config.HTTP{Listen: "127.0.0.1:8080", TrustedProxies: []string{"127.0.0.1"}}).TrustedPrefixes(); err != nil {
		t.Error(err)
	}
}
