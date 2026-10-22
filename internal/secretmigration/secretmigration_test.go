package secretmigration

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/legacycrypt"
	"github.com/Helles01/raptor-panel/internal/legacycrypt/legacycrypttest"
	"github.com/Helles01/raptor-panel/internal/secrets"
)

var legacyKey = bytes.Repeat([]byte{0x11}, 32)

func laravelEncrypt(t *testing.T, plaintext string) string {
	t.Helper()
	return legacycrypttest.Encrypt(legacyKey, plaintext)
}

// fakeStore keeps the old columns read-only: the only way to change anything is WriteSecrets.
type fakeStore struct {
	legacy        map[string][]Row
	converted     map[string]map[uint64]Write // column -> row -> write
	writeCalls    int
	failWriteWith error
}

func newFakeStore() *fakeStore {
	return &fakeStore{legacy: map[string][]Row{}, converted: map[string]map[uint64]Write{}}
}

func (f *fakeStore) LegacyValues(_ context.Context, c Column) ([]Row, error) {
	return append([]Row(nil), f.legacy[c.String()]...), nil
}

func (f *fakeStore) SourceHashes(_ context.Context, c Column) (map[uint64]string, error) {
	out := map[uint64]string{}
	for id, w := range f.converted[c.String()] {
		out[id] = w.SourceHash
	}
	return out, nil
}

func (f *fakeStore) WriteSecrets(_ context.Context, writes []Write) error {
	f.writeCalls++
	if f.failWriteWith != nil {
		return f.failWriteWith
	}
	for _, w := range writes {
		if f.converted[w.Column.String()] == nil {
			f.converted[w.Column.String()] = map[uint64]Write{}
		}
		f.converted[w.Column.String()][w.RowID] = w
	}
	return nil
}

func newDeps(t *testing.T) (*legacycrypt.Decrypter, *secrets.Box) {
	t.Helper()
	legacy, err := legacycrypt.New(legacyKey)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.NewBox(bytes.Repeat([]byte{0x77}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return legacy, box
}

func seed(t *testing.T) *fakeStore {
	f := newFakeStore()
	f.legacy["nodes.daemon_token"] = []Row{{ID: 1, Value: laravelEncrypt(t, "node-token-1")}, {ID: 2, Value: laravelEncrypt(t, "node-token-2")}}
	f.legacy["api_keys.token"] = []Row{{ID: 7, Value: laravelEncrypt(t, "api-token")}}
	f.legacy["users.totp_secret"] = []Row{{ID: 3, Value: laravelEncrypt(t, "JBSWY3DPEHPK3PXP")}}
	f.legacy["settings.value[settings::mail:mailers:smtp:password]"] = []Row{{ID: 11, Value: laravelEncrypt(t, "smtp-pass")}}
	return f
}

func total(r Report) (rows, converted, unchanged int) {
	for _, c := range r.Columns {
		rows += c.Total
		converted += c.Converted
		unchanged += c.Unchanged
	}
	return
}

func TestRunConvertsEverything(t *testing.T) {
	legacy, box := newDeps(t)
	store := seed(t)

	report, err := Run(context.Background(), store, legacy, box, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows, converted, unchanged := total(report)
	if rows != 5 || converted != 5 || unchanged != 0 {
		t.Errorf("rows/converted/unchanged = %d/%d/%d, want 5/5/0", rows, converted, unchanged)
	}

	// Every converted value opens with the context of its own location, and only there.
	checks := []struct {
		col  Column
		id   uint64
		want string
	}{
		{Column{Table: "nodes", Column: "daemon_token"}, 1, "node-token-1"},
		{Column{Table: "nodes", Column: "daemon_token"}, 2, "node-token-2"},
		{Column{Table: "api_keys", Column: "token"}, 7, "api-token"},
		{Column{Table: "users", Column: "totp_secret"}, 3, "JBSWY3DPEHPK3PXP"},
		{Columns()[5], 11, "smtp-pass"},
	}
	for _, c := range checks {
		w, ok := store.converted[c.col.String()][c.id]
		if !ok {
			t.Errorf("%s id=%d was not written", c.col, c.id)
			continue
		}
		got, err := box.Open(w.Sealed, secrets.Context(c.col.Table, c.id, c.col.Column))
		if err != nil || got != c.want {
			t.Errorf("%s id=%d opens to %q, %v; want %q", c.col, c.id, got, err, c.want)
		}
		if _, err := box.Open(w.Sealed, secrets.Context(c.col.Table, c.id+1, c.col.Column)); err == nil {
			t.Errorf("%s id=%d also opens under another row's context", c.col, c.id)
		}
		if strings.Contains(w.Sealed, c.want) || strings.Contains(w.SourceHash, c.want) {
			t.Errorf("%s id=%d: stored value contains the plaintext", c.col, c.id)
		}
	}
	if store.writeCalls != 1 {
		t.Errorf("WriteSecrets called %d times, want exactly one atomic write", store.writeCalls)
	}
}

func TestRunIsIdempotentAndDetectsChanges(t *testing.T) {
	legacy, box := newDeps(t)
	store := seed(t)
	ctx := context.Background()

	if _, err := Run(ctx, store, legacy, box, Options{}); err != nil {
		t.Fatal(err)
	}

	t.Run("second run converts nothing", func(t *testing.T) {
		report, err := Run(ctx, store, legacy, box, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if _, converted, unchanged := total(report); converted != 0 || unchanged != 5 {
			t.Errorf("converted/unchanged = %d/%d, want 0/5", converted, unchanged)
		}
	})

	t.Run("a changed old value is converted again", func(t *testing.T) {
		store.legacy["nodes.daemon_token"][0].Value = laravelEncrypt(t, "rotated-in-old-panel")
		report, err := Run(ctx, store, legacy, box, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if _, converted, _ := total(report); converted != 1 {
			t.Errorf("converted = %d, want 1", converted)
		}
		w := store.converted["nodes.daemon_token"][1]
		if got, _ := box.Open(w.Sealed, secrets.Context("nodes", 1, "daemon_token")); got != "rotated-in-old-panel" {
			t.Errorf("row 1 opens to %q after the change", got)
		}
	})

	t.Run("All converts every row again", func(t *testing.T) {
		report, err := Run(ctx, store, legacy, box, Options{All: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, converted, _ := total(report); converted != 5 {
			t.Errorf("converted = %d, want 5", converted)
		}
	})
}

func TestRunDryRunWritesNothing(t *testing.T) {
	legacy, box := newDeps(t)
	store := seed(t)

	report, err := Run(context.Background(), store, legacy, box, Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !report.DryRun {
		t.Error("report does not say it was a dry run")
	}
	if _, converted, _ := total(report); converted != 5 {
		t.Errorf("a dry run should still count the 5 rows it would convert, got %d", converted)
	}
	if store.writeCalls != 0 || len(store.converted) != 0 {
		t.Errorf("dry run wrote something: %d calls, %v", store.writeCalls, store.converted)
	}
}

func TestRunWithWrongKeyWritesNothing(t *testing.T) {
	_, box := newDeps(t)
	wrong, err := legacycrypt.New(bytes.Repeat([]byte{0x99}, 32))
	if err != nil {
		t.Fatal(err)
	}
	store := seed(t)

	report, err := Run(context.Background(), store, wrong, box, Options{})
	if !errors.Is(err, ErrFailures) {
		t.Fatalf("Run error = %v, want ErrFailures", err)
	}
	if len(report.Failures) != 5 {
		t.Errorf("failures = %d, want 5", len(report.Failures))
	}
	if store.writeCalls != 0 {
		t.Error("a run with failures must not write anything")
	}
}

func TestRunOneBadRowWritesNothing(t *testing.T) {
	legacy, box := newDeps(t)
	store := seed(t)
	store.legacy["users.totp_secret"] = append(store.legacy["users.totp_secret"], Row{ID: 99, Value: "garbage"})

	report, err := Run(context.Background(), store, legacy, box, Options{})
	if !errors.Is(err, ErrFailures) {
		t.Fatalf("Run error = %v, want ErrFailures", err)
	}
	want := []Failure{{Column: Column{Table: "users", Column: "totp_secret"}, RowID: 99}}
	if !reflect.DeepEqual(report.Failures, want) {
		t.Errorf("failures = %+v, want %+v", report.Failures, want)
	}
	if store.writeCalls != 0 {
		t.Error("one bad row must stop the whole run before any write")
	}
}

func TestRunPropagatesWriteError(t *testing.T) {
	legacy, box := newDeps(t)
	store := seed(t)
	store.failWriteWith = errors.New("disk full")

	if _, err := Run(context.Background(), store, legacy, box, Options{}); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("Run error = %v, want the write error", err)
	}
}

func TestColumns(t *testing.T) {
	want := []string{
		"nodes.daemon_token",
		"api_keys.token",
		"users.totp_secret",
		"database_hosts.password",
		"databases.password",
		"settings.value[settings::mail:mailers:smtp:password]",
	}
	var got []string
	for _, c := range Columns() {
		got = append(got, c.String())
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Columns = %v, want %v", got, want)
	}
}
