package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/db"
	"github.com/Helles01/raptor-panel/internal/legacycrypt"
	"github.com/Helles01/raptor-panel/internal/legacycrypt/legacycrypttest"
	"github.com/Helles01/raptor-panel/internal/secretmigration"
	"github.com/Helles01/raptor-panel/internal/secrets"
)

func TestKeygen(t *testing.T) {
	e, out, errOut := testEnv("", nil)

	if code := run(context.Background(), []string{"keygen"}, e); code != 0 {
		t.Fatalf("exit code = %d, stderr: %s", code, errOut)
	}
	key := strings.TrimSpace(out.String())
	if _, err := secrets.ParseKey(key); err != nil {
		t.Errorf("stdout %q is not a usable key: %v", key, err)
	}
	if strings.Count(out.String(), "\n") != 1 {
		t.Errorf("stdout must hold only the key, got %q", out)
	}
	if strings.Contains(errOut.String(), key) {
		t.Error("the key must not be repeated on stderr")
	}

	e2, _, _ := testEnv("", nil)
	if code := run(context.Background(), []string{"keygen", "extra"}, e2); code != 2 {
		t.Errorf("keygen with an argument: exit code = %d, want 2", code)
	}
}

func TestCommandsRejectBadInvocation(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		vars       map[string]string
		wantCode   int
		wantStderr string
	}{
		{"db needs a subcommand", []string{"db"}, nil, 2, "a command is required"},
		{"db unknown subcommand", []string{"db", "drop"}, nil, 2, `unknown command "drop"`},
		{"unknown flag", []string{"db", "check", "--nope"}, nil, 2, "invalid arguments"},
		{"db check without database name", []string{"db", "check"}, map[string]string{"RAPTOR_DB_USER": "u"}, 1, "db.name"},
		{"db migrate with bad port", []string{"db", "migrate"}, map[string]string{"RAPTOR_DB_PORT": "x"}, 1, "RAPTOR_DB_PORT"},
		{"migrate-secrets without the old key", []string{"migrate-secrets"}, nil, 1, legacyKeyVariable + " is not set"},
		{"migrate-secrets with an invalid old key", []string{"migrate-secrets"}, map[string]string{legacyKeyVariable: "base64:***"}, 1, "not valid base64"},
		{"migrate-secrets with a wrong sized old key", []string{"migrate-secrets"}, map[string]string{legacyKeyVariable: "short"}, 1, "16 or 32 bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, _, errOut := testEnv("", tt.vars)

			code := run(context.Background(), tt.args, e)

			if code != tt.wantCode || !strings.Contains(errOut.String(), tt.wantStderr) {
				t.Errorf("exit code = %d, stderr = %q; want %d containing %q", code, errOut, tt.wantCode, tt.wantStderr)
			}
			if strings.Contains(errOut.String(), "***") {
				t.Errorf("stderr echoes key material: %q", errOut)
			}
		})
	}
}

func TestWriteMigrationReport(t *testing.T) {
	many := make([]string, 25)
	for i := range many {
		many[i] = fmt.Sprintf("m%02d", i)
	}

	tests := []struct {
		name      string
		report    db.MigrationReport
		wantErr   bool
		wantParts []string
		notParts  []string
	}{
		{"ok", db.MigrationReport{}, false, []string{"OK: the database has exactly the expected schema version"}, []string{"missing"}},
		{"missing", db.MigrationReport{Missing: []string{"a", "b"}}, true, []string{"missing", "(2)", "  a", "  b"}, []string{"unknown"}},
		{"unknown", db.MigrationReport{Unknown: []string{"x"}}, true, []string{"unknown", "  x"}, []string{"missing"}},
		{"long lists are cut", db.MigrationReport{Missing: many}, true, []string{"(25)", "m19", "... and 5 more"}, []string{"m20"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer

			err := writeMigrationReport(&out, 195, 190, tt.report)

			if (err != nil) != tt.wantErr {
				t.Errorf("error = %v, wantErr %v", err, tt.wantErr)
			}
			for _, p := range tt.wantParts {
				if !strings.Contains(out.String(), p) {
					t.Errorf("output lacks %q:\n%s", p, out.String())
				}
			}
			for _, p := range tt.notParts {
				if strings.Contains(out.String(), p) {
					t.Errorf("output contains %q:\n%s", p, out.String())
				}
			}
		})
	}
}

type memStore struct {
	legacy map[string][]secretmigration.Row
	writes []secretmigration.Write
}

func (m *memStore) LegacyValues(_ context.Context, c secretmigration.Column) ([]secretmigration.Row, error) {
	return m.legacy[c.String()], nil
}

func (m *memStore) SourceHashes(context.Context, secretmigration.Column) (map[uint64]string, error) {
	return nil, nil
}

func (m *memStore) WriteSecrets(_ context.Context, w []secretmigration.Write) error {
	m.writes = append(m.writes, w...)
	return nil
}

func TestMigrateSecrets(t *testing.T) {
	oldKey := bytes.Repeat([]byte{0x11}, 32)
	legacy, err := legacycrypt.New(oldKey)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.NewBox(bytes.Repeat([]byte{0x77}, 32))
	if err != nil {
		t.Fatal(err)
	}
	const plaintext = "super-secret-node-token"
	good := []secretmigration.Row{{ID: 4, Value: legacycrypttest.Encrypt(oldKey, plaintext)}}

	tests := []struct {
		name       string
		rows       []secretmigration.Row
		opts       secretmigration.Options
		wantErr    string
		wantParts  []string
		wantWrites int
	}{
		{"converts", good, secretmigration.Options{}, "", []string{"nodes.daemon_token: 1 rows, 1 converted, 0 unchanged", "done: the old columns were not modified"}, 1},
		{"dry run", good, secretmigration.Options{DryRun: true}, "", []string{"1 would be converted", "dry run: nothing was written"}, 0},
		{"undecryptable row", []secretmigration.Row{{ID: 9, Value: "garbage"}}, secretmigration.Options{}, "1 value(s) could not be decrypted and nothing was written", []string{"FAILED nodes.daemon_token id=9"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &memStore{legacy: map[string][]secretmigration.Row{"nodes.daemon_token": tt.rows}}
			var out bytes.Buffer

			err := migrateSecrets(context.Background(), &out, store, legacy, box, tt.opts)

			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
			}
			for _, p := range tt.wantParts {
				if !strings.Contains(out.String(), p) {
					t.Errorf("output lacks %q:\n%s", p, out.String())
				}
			}
			if len(store.writes) != tt.wantWrites {
				t.Errorf("writes = %d, want %d", len(store.writes), tt.wantWrites)
			}
			if strings.Contains(out.String(), plaintext) || strings.Contains(fmt.Sprint(err), plaintext) {
				t.Error("the secret value appears in the command output")
			}
		})
	}
}
