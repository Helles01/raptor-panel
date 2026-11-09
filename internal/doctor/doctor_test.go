package doctor

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/config"
	"github.com/Helles01/raptor-panel/internal/secretmigration"
)

func env(vars map[string]string) func(string) string { return func(k string) string { return vars[k] } }

// goodConfig is a configuration that is fine: a key, an address, a database.
func goodConfig(t *testing.T, extra map[string]string) config.Config {
	t.Helper()
	vars := map[string]string{
		"RAPTOR_SECRET_KEY": "iDsdi+5AzVWiY8W1v9SHn4lUxVtt4BFUkmPSUgPo5RA=", "RAPTOR_APP_URL": "https://panel.example.com", "RAPTOR_DB_NAME": "raptor", "RAPTOR_DB_USER": "raptor",
		"RAPTOR_TRUSTED_PROXIES": "10.0.0.2",
	}
	for k, v := range extra {
		vars[k] = v
	}
	cfg, err := config.Load("", env(vars))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func find(rs []Result, name string) (Result, bool) {
	for _, r := range rs {
		if r.Name == name {
			return r, true
		}
	}
	return Result{}, false
}

func want(t *testing.T, rs []Result, name string, level Level, contains string) {
	t.Helper()
	r, ok := find(rs, name)
	if !ok {
		t.Errorf("no result named %q in %+v", name, rs)
		return
	}
	if r.Level != level || !strings.Contains(r.Detail, contains) {
		t.Errorf("%s = %v %q, want %v containing %q", name, r.Level, r.Detail, level, contains)
	}
}

func TestConfigOfAGoodPanel(t *testing.T) {
	rs := Config(goodConfig(t, nil))
	if Worst(rs) != OK {
		t.Fatalf("results = %+v", rs)
	}
	want(t, rs, "configuration", OK, "valid")
	want(t, rs, "secret key", OK, "")
	want(t, rs, "address", OK, "https://panel.example.com")
	want(t, rs, "trusted proxies", OK, "1 trusted")
	want(t, rs, "backups on S3", OK, "backups are kept on the nodes")
}

func TestConfigSaysWhatIsWrong(t *testing.T) {
	cfg := goodConfig(t, map[string]string{"RAPTOR_SECRET_KEY": "not a key"})
	rs := Config(cfg)
	want(t, rs, "secret key", Fail, "not a key `raptor keygen` makes")
	if strings.Contains(rs[0].Detail+rs[1].Detail, "not a key") && !strings.Contains(rs[1].Detail, "RAPTOR_SECRET_KEY") {
		t.Errorf("the value of the key is repeated")
	}
	for _, r := range rs {
		if strings.Contains(r.Detail, "not a key\"") || strings.Contains(r.Detail, "= not a key") {
			t.Errorf("the key is shown: %+v", r)
		}
	}

	if r := Config(goodConfig(t, map[string]string{"RAPTOR_SECRET_KEY": ""})); true {
		want(t, r, "secret key", Fail, "is not set")
	}
	// A section that does not validate is named, with what is wrong, and the others are still looked at.
	rs = Config(goodConfig(t, map[string]string{"RAPTOR_LOG_LEVEL": "chatty", "RAPTOR_BACKUPS_DRIVER": "ftp"}))
	want(t, rs, "configuration: log", Fail, "log.level")
	want(t, rs, "configuration: backups", Fail, "backups.driver")
	if _, ok := find(rs, "configuration"); ok {
		t.Error("it says the configuration is valid")
	}
	want(t, rs, "secret key", OK, "")
}

func TestConfigWarnsOfWhatIsLikelyASlip(t *testing.T) {
	rs := Config(goodConfig(t, map[string]string{"RAPTOR_APP_URL": "http://panel.example.com"}))
	want(t, rs, "address", Warn, "not marked Secure")
	// Loopback over http is how a test machine is run.
	want(t, Config(goodConfig(t, map[string]string{"RAPTOR_APP_URL": "http://127.0.0.1:8000"})), "address", OK, "http://127.0.0.1:8000")
	want(t, Config(goodConfig(t, map[string]string{"RAPTOR_APP_URL": "http://localhost"})), "address", OK, "")

	// https and no proxy listed: every client would look like the proxy.
	rs = Config(goodConfig(t, map[string]string{"RAPTOR_TRUSTED_PROXIES": ""}))
	want(t, rs, "trusted proxies", Warn, "RAPTOR_TRUSTED_PROXIES")
	want(t, Config(goodConfig(t, map[string]string{"RAPTOR_TRUSTED_PROXIES": "", "RAPTOR_APP_URL": "http://127.0.0.1"})), "trusted proxies", OK, "none")

	rs = Config(goodConfig(t, map[string]string{"RAPTOR_WEB_RELOAD": "true", "RAPTOR_WINGS_INSECURE_TLS": "true"}))
	want(t, rs, "development options", Warn, "web.reload")
	want(t, rs, "development options", Warn, "wings.insecure_tls")
	if _, ok := find(Config(goodConfig(t, nil)), "development options"); ok {
		t.Error("development options are reported on a panel that has none")
	}

	rs = Config(goodConfig(t, map[string]string{
		"RAPTOR_BACKUPS_S3_REGION": "eu-west-1", "RAPTOR_BACKUPS_S3_BUCKET": "my-backups", "RAPTOR_BACKUPS_S3_ACCESS_KEY": "AKIAEXAMPLE", "RAPTOR_BACKUPS_S3_SECRET_KEY": "secret-value", "RAPTOR_BACKUPS_DRIVER": "s3",
	}))
	want(t, rs, "backups on S3", OK, "the bucket")
	for _, r := range rs {
		if strings.Contains(r.Detail, "secret-value") || strings.Contains(r.Detail, "AKIAEXAMPLE") {
			t.Errorf("a key is shown: %+v", r)
		}
	}
}

func TestListen(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	// A free address is free, and the check does not keep it.
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := free.Addr().String()
	free.Close()
	if r := Listen(addr, net.Listen); r.Level != OK {
		t.Errorf("a free address: %+v", r)
	}
	if again, err := net.Listen("tcp", addr); err != nil {
		t.Errorf("the check kept the address: %v", err)
	} else {
		again.Close()
	}
	// One that is held is a warning, and says so.
	if r := Listen(l.Addr().String(), net.Listen); r.Level != Warn || !strings.Contains(r.Detail, "something already holds it") {
		t.Errorf("a held address: %+v", r)
	}
}

func TestFrontend(t *testing.T) {
	if r := Frontend("web/public/assets", nil); r.Level != OK {
		t.Errorf("a good build: %+v", r)
	}
	if r := Frontend("web/public/assets", errors.New("the frontend build has no manifest.json")); r.Level != Fail || !strings.Contains(r.Detail, "no manifest.json") {
		t.Errorf("a broken build: %+v", r)
	}
}

// fakeDB answers the questions of the checks with what a test sets.
type fakeDB struct {
	version      string
	versionErr   error
	now          time.Time
	schema       Schema
	schemaErr    error
	pending      []secretmigration.PendingColumn
	pendingErr   error
	nodes        []Node
	nodesErr     error
	admins       int
	adminsErr    error
	mail         map[string]string
	mailPassword bool
	mailErr      error
	asked        []string
}

func (f *fakeDB) Version(context.Context) (string, error) {
	f.asked = append(f.asked, "version")
	return f.version, f.versionErr
}
func (f *fakeDB) Now(context.Context) (time.Time, error) {
	f.asked = append(f.asked, "now")
	return f.now, nil
}
func (f *fakeDB) Schema(context.Context) (Schema, error) {
	f.asked = append(f.asked, "schema")
	return f.schema, f.schemaErr
}
func (f *fakeDB) PendingSecrets(context.Context) ([]secretmigration.PendingColumn, error) {
	f.asked = append(f.asked, "secrets")
	return f.pending, f.pendingErr
}
func (f *fakeDB) Nodes(context.Context) ([]Node, error) {
	f.asked = append(f.asked, "nodes")
	return f.nodes, f.nodesErr
}
func (f *fakeDB) Admins(context.Context) (int, error) {
	f.asked = append(f.asked, "admins")
	return f.admins, f.adminsErr
}
func (f *fakeDB) Mail(context.Context) (map[string]string, bool, error) {
	f.asked = append(f.asked, "mail")
	return f.mail, f.mailPassword, f.mailErr
}

var clock = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func goodDB() *fakeDB {
	return &fakeDB{
		version: "11.8.9-MariaDB", now: clock, admins: 2, nodes: []Node{{ID: 1, Name: "Node 1", HasToken: true}},
		mail:         map[string]string{"mail:mailers:smtp:host": "smtp.example.com", "mail:mailers:smtp:port": "587", "mail:mailers:smtp:encryption": "tls", "mail:mailers:smtp:username": "mailer"},
		mailPassword: true,
	}
}

func TestChecksOfAGoodDatabase(t *testing.T) {
	rs := Checks(context.Background(), goodDB(), clock)
	if Worst(rs) != OK {
		t.Fatalf("results = %+v", rs)
	}
	want(t, rs, "database connection", OK, "11.8.9-MariaDB")
	want(t, rs, "clock", OK, "agree")
	want(t, rs, "schema", OK, "up to date")
	want(t, rs, "administrators", OK, "2")
	want(t, rs, "old secrets", OK, "no value")
	want(t, rs, "nodes", OK, "1, each with a token")
	want(t, rs, "mail", OK, "smtp.example.com:587")
}

func TestChecksStopWhereTheyMust(t *testing.T) {
	// A database that cannot be reached ends the checks with one failure, which does not carry what the driver said beyond a cut.
	db := goodDB()
	db.versionErr = errors.New("dial tcp 127.0.0.1:3306: connectex: refused" + strings.Repeat("x", 500))
	rs := Checks(context.Background(), db, clock)
	if len(rs) != 1 || rs[0].Level != Fail || len(rs[0].Detail) > 320 || len(db.asked) != 1 {
		t.Errorf("results = %+v, asked %v", rs, db.asked)
	}

	for name, tc := range map[string]struct {
		schema Schema
		detail string
	}{
		"a missing migration":    {Schema{MissingReference: 3}, "3 migrations missing"},
		"an unknown migration":   {Schema{UnknownReference: 1}, "1 unknown"},
		"a migration of our own": {Schema{PendingOwn: []string{"0003_x"}}, "raptor db migrate"},
	} {
		db := goodDB()
		db.schema = tc.schema
		rs := Checks(context.Background(), db, clock)
		want(t, rs, "schema", Fail, tc.detail)
		// What is looked at after the schema is not looked at when the schema is wrong: the questions would be about tables that are not as they should be.
		for _, q := range db.asked {
			if q == "admins" || q == "secrets" || q == "nodes" || q == "mail" {
				t.Errorf("%s: %q was asked of a database with a wrong schema", name, q)
			}
		}
	}
	db = goodDB()
	db.schemaErr = errors.New("table is gone")
	want(t, Checks(context.Background(), db, clock), "schema", Fail, "table is gone")
}

func TestChecksSayWhatIsWrongWithTheData(t *testing.T) {
	db := goodDB()
	db.admins = 0
	db.pending = []secretmigration.PendingColumn{
		{Column: secretmigration.Column{Table: "nodes", Column: "daemon_token"}, Total: 3, Pending: 2},
		{Column: secretmigration.Column{Table: "users", Column: "totp_secret"}, Total: 5, Pending: 5},
		{Column: secretmigration.Column{Table: "api_keys", Column: "token"}, Total: 4, Pending: 0},
		{Column: secretmigration.Column{Table: "settings", Column: "value", SettingsKey: "settings::mail:mailers:smtp:password"}, Total: 1, Pending: 1},
	}
	db.nodes = []Node{{ID: 2, Name: "Two", HasToken: false}, {ID: 1, Name: "One\x1b[31m", HasToken: true}}
	rs := Checks(context.Background(), db, clock)
	want(t, rs, "administrators", Fail, "raptor user create --admin")
	want(t, rs, "old secrets: nodes.daemon_token", Warn, "2 of 3 are not converted")
	want(t, rs, "old secrets: users.totp_secret", Warn, "disable-2fa")
	want(t, rs, "old secrets: settings.value[settings::mail:mailers:smtp:password]", Warn, "mail is sent without a password")
	if _, ok := find(rs, "old secrets: api_keys.token"); ok {
		t.Error("a column with nothing pending is reported")
	}
	if _, ok := find(rs, "old secrets"); ok {
		t.Error("it says nothing is waiting")
	}
	want(t, rs, "node 2", Warn, "raptor node rotate-token 2")
	for _, r := range rs {
		if strings.ContainsRune(r.Detail, 0x1b) {
			t.Errorf("a control character reaches the terminal: %q", r.Detail)
		}
	}
	if _, ok := find(rs, "node 1"); ok {
		t.Error("a node with a token is reported")
	}

	db = goodDB()
	db.nodes = nil
	want(t, Checks(context.Background(), db, clock), "nodes", Warn, "no node")
}

func TestChecksOfTheClockAndTheErrorsOfTheQuestions(t *testing.T) {
	db := goodDB()
	db.now = clock.Add(-20 * time.Minute)
	want(t, Checks(context.Background(), db, clock), "clock", Warn, "20m0s")
	db.now = clock.Add(4 * time.Minute)
	want(t, Checks(context.Background(), db, clock), "clock", OK, "")

	for name, f := range map[string]func(*fakeDB){
		"admins":  func(d *fakeDB) { d.adminsErr = errBoom },
		"secrets": func(d *fakeDB) { d.pendingErr = errBoom },
		"nodes":   func(d *fakeDB) { d.nodesErr = errBoom },
		"mail":    func(d *fakeDB) { d.mailErr = errBoom },
	} {
		db := goodDB()
		f(db)
		rs := Checks(context.Background(), db, clock)
		if Worst(rs) != Fail {
			t.Errorf("%s: an error of the question is not a failure: %+v", name, rs)
		}
	}
}

var errBoom = errors.New("the database is down")

func TestMailCheck(t *testing.T) {
	cases := map[string]struct {
		values   map[string]string
		password bool
		level    Level
		detail   string
	}{
		"nothing set":            {map[string]string{}, false, Warn, "no mail server is set up"},
		"a host and no port":     {map[string]string{"mail:mailers:smtp:host": "smtp.example.com"}, false, Fail, "the settings are wrong"},
		"a way nobody knows":     {map[string]string{"mail:mailers:smtp:host": "h", "mail:mailers:smtp:port": "25", "mail:mailers:smtp:encryption": "magic"}, false, Fail, "the settings are wrong"},
		"a user and no password": {map[string]string{"mail:mailers:smtp:host": "h", "mail:mailers:smtp:port": "587", "mail:mailers:smtp:encryption": "tls", "mail:mailers:smtp:username": "u"}, false, Warn, "set-mail-password"},
		"a user in the clear":    {map[string]string{"mail:mailers:smtp:host": "h", "mail:mailers:smtp:port": "25", "mail:mailers:smtp:username": "u"}, true, Warn, "without encryption"},
		"no user, no encryption": {map[string]string{"mail:mailers:smtp:host": "localhost", "mail:mailers:smtp:port": "25"}, false, OK, "localhost:25"},
		"a user with a password": {map[string]string{"mail:mailers:smtp:host": "h", "mail:mailers:smtp:port": "465", "mail:mailers:smtp:encryption": "ssl", "mail:mailers:smtp:username": "u"}, true, OK, "h:465"},
	}
	for name, tc := range cases {
		db := goodDB()
		db.mail, db.mailPassword = tc.values, tc.password
		r, ok := find(Checks(context.Background(), db, clock), "mail")
		if !ok || r.Level != tc.level || !strings.Contains(r.Detail, tc.detail) {
			t.Errorf("%s: %+v, want %v containing %q", name, r, tc.level, tc.detail)
		}
	}
}

func TestWorstAndLevels(t *testing.T) {
	if Worst(nil) != OK || Worst([]Result{{Level: OK}, {Level: Warn}}) != Warn || Worst([]Result{{Level: Fail}, {Level: Warn}}) != Fail {
		t.Error("Worst")
	}
	if OK.String() != "ok" || Warn.String() != "warn" || Fail.String() != "FAIL" {
		t.Error("the names of the levels")
	}
}
