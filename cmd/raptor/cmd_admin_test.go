package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func runCmd(t *testing.T, stdin string, vars map[string]string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	e := &env{stdin: strings.NewReader(stdin), stdout: &out, stderr: &errOut, getenv: func(k string) string { return vars[k] }}
	code = run(context.Background(), args, e)
	return code, out.String(), errOut.String()
}

func TestAdminCommandsAreRegistered(t *testing.T) {
	table := rootCommands()
	for _, name := range []string{"user", "apikey", "location", "node", "nest", "egg", "settings", "dbhost", "db", "keygen", "version", "migrate-secrets"} {
		if table[name] == nil {
			t.Errorf("raptor %s is not a command", name)
		}
	}
}

// Help and a mistyped name never need the database.
func TestAdminCommandsNeedNoDatabaseToExplainThemselves(t *testing.T) {
	for group, verbs := range adminGroups() {
		code, _, stderr := runCmd(t, "", nil, group, "help")
		if code != 0 {
			t.Errorf("raptor %s help = %d", group, code)
		}
		for verb := range verbs {
			if !strings.Contains(stderr, verb) {
				t.Errorf("raptor %s help does not list %s:\n%s", group, verb, stderr)
			}
		}
		if code, _, stderr := runCmd(t, "", nil, group); code != 2 || !strings.Contains(stderr, "a command is required") {
			t.Errorf("raptor %s = %d %q", group, code, stderr)
		}
		if code, _, stderr := runCmd(t, "", nil, group, "nonsense"); code != 2 || !strings.Contains(stderr, "unknown command") {
			t.Errorf("raptor %s nonsense = %d %q", group, code, stderr)
		}
	}
}

func TestAdminCommandsRefuseAnIncompleteConfiguration(t *testing.T) {
	base := map[string]string{"RAPTOR_DB_NAME": "x", "RAPTOR_DB_USER": "u", "RAPTOR_APP_URL": "https://panel.example.com"}
	with := func(extra map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	for name, tc := range map[string]struct {
		vars map[string]string
		want string
	}{
		"no secret key":  {base, "secrets.key is not set"},
		"bad port":       {with(map[string]string{"RAPTOR_DB_PORT": "x"}), "RAPTOR_DB_PORT"},
		"no app url":     {map[string]string{"RAPTOR_DB_NAME": "x", "RAPTOR_DB_USER": "u"}, "app.url"},
		"bad host tls":   {with(map[string]string{"RAPTOR_DB_HOSTS_TLS": "maybe", "RAPTOR_SECRET_KEY": "x"}), "db_hosts.tls"},
		"bad secret key": {with(map[string]string{"RAPTOR_SECRET_KEY": "not a key"}), "key"},
	} {
		code, _, stderr := runCmd(t, "", tc.vars, "user", "list")
		if code != 1 || !strings.Contains(stderr, tc.want) {
			t.Errorf("%s: code %d, %q", name, code, stderr)
		}
		if strings.Contains(stderr, "not a key") && !strings.Contains(tc.want, "key") {
			t.Errorf("%s: the secret key is in the message: %q", name, stderr)
		}
	}
}
