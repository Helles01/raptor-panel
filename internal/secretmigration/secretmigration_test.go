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
	legacy          map[string][]Row
	converted       map[string]map[uint64]Write // column -> row -> write
	writeCalls      int
	failWriteWith   error
	neutraliseCalls int
	failNeutralise  error
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

func pendingOf(t *testing.T, f *fakeStore) map[string][2]int {
	t.Helper()
	got, err := Pending(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][2]int{}
	for _, p := range got {
		out[p.Column.String()] = [2]int{p.Total, p.Pending}
	}
	return out
}

func TestPendingCountsTheValuesThatAreNotConvertedWithoutDecryptingAny(t *testing.T) {
	f := seed(t)
	got := pendingOf(t, f)
	if got["nodes.daemon_token"] != [2]int{2, 2} || got["api_keys.token"] != [2]int{1, 1} || got["users.totp_secret"] != [2]int{1, 1} || got["database_hosts.password"] != [2]int{0, 0} {
		t.Fatalf("before: %v", got)
	}
	if len(got) != len(Columns()) {
		t.Errorf("%d columns of %d", len(got), len(Columns()))
	}

	legacy, box := newDeps(t)
	if _, err := Run(context.Background(), f, legacy, box, Options{}); err != nil {
		t.Fatal(err)
	}
	for col, p := range pendingOf(t, f) {
		if p[1] != 0 {
			t.Errorf("after the conversion %s has %d pending", col, p[1])
		}
	}

	// A value that changed in the old place since it was converted is pending again, and only that one.
	f.legacy["nodes.daemon_token"][1] = Row{ID: 2, Value: laravelEncrypt(t, "a new token")}
	if got := pendingOf(t, f); got["nodes.daemon_token"] != [2]int{2, 1} {
		t.Errorf("after a change: %v", got["nodes.daemon_token"])
	}

	// A column whose old values are gone (as after they were neutralised) has nothing pending, and a store that fails is an error, not a zero.
	f.legacy["nodes.daemon_token"] = nil
	if got := pendingOf(t, f); got["nodes.daemon_token"] != [2]int{0, 0} {
		t.Errorf("with nothing in the old place: %v", got["nodes.daemon_token"])
	}
}

type failingStore struct{ *fakeStore }

func (failingStore) LegacyValues(context.Context, Column) ([]Row, error) {
	return nil, errors.New("down")
}

func TestPendingFailsWhenTheStoreFails(t *testing.T) {
	if _, err := Pending(context.Background(), failingStore{newFakeStore()}); err == nil {
		t.Error("an error of the store was taken for nothing pending")
	}
}

// --- Finalize

func (f *fakeStore) SealedValues(_ context.Context, c Column) (map[uint64]string, error) {
	out := map[uint64]string{}
	for id, w := range f.converted[c.String()] {
		out[id] = w.Sealed
	}
	return out, nil
}

func (f *fakeStore) Neutralise(_ context.Context, rows []NeutraliseRow) error {
	f.neutraliseCalls++
	if f.failNeutralise != nil {
		return f.failNeutralise
	}
	for _, r := range rows {
		kept := f.legacy[r.Column.String()][:0:0]
		for _, row := range f.legacy[r.Column.String()] {
			if row.ID != r.ID {
				kept = append(kept, row)
			}
		}
		f.legacy[r.Column.String()] = kept
	}
	return nil
}

func converted(t *testing.T) (*fakeStore, *secrets.Box) {
	t.Helper()
	f := seed(t)
	legacy, box := newDeps(t)
	if _, err := Run(context.Background(), f, legacy, box, Options{}); err != nil {
		t.Fatal(err)
	}
	return f, box
}

func oldValues(f *fakeStore) int {
	n := 0
	for _, rows := range f.legacy {
		n += len(rows)
	}
	return n
}

func TestFinalizeMakesTheOldValuesEmptyWhenEveryOneIsConvertedAndReadable(t *testing.T) {
	f, box := converted(t)
	before := oldValues(f)
	// A dry run says what would be done and does nothing.
	rep, err := Finalize(context.Background(), f, box, FinalizeOptions{DryRun: true})
	if err != nil || !rep.DryRun || len(rep.Problems) != 0 || oldValues(f) != before || f.neutraliseCalls != 0 {
		t.Fatalf("dry run: %+v, %v, %d old values", rep, err, oldValues(f))
	}
	neutral := 0
	for _, c := range rep.Columns {
		neutral += c.Neutralised
	}
	if neutral != before {
		t.Errorf("the dry run counts %d, there are %d", neutral, before)
	}

	rep, err = Finalize(context.Background(), f, box, FinalizeOptions{})
	if err != nil || len(rep.Problems) != 0 || oldValues(f) != 0 || f.neutraliseCalls != 1 {
		t.Fatalf("finalize: %+v, %v, %d old values left", rep, err, oldValues(f))
	}
	// The converted copies were not touched: what was sealed is still what the panel reads.
	for col, rows := range f.converted {
		for id, w := range rows {
			if _, err := box.Open(w.Sealed, secrets.Context(w.Column.Table, id, w.Column.Column)); err != nil {
				t.Errorf("%s id=%d cannot be opened after finalizing: %v", col, id, err)
			}
		}
	}
	// Run again there is nothing left, and nothing to do.
	rep, err = Finalize(context.Background(), f, box, FinalizeOptions{})
	if err != nil || f.neutraliseCalls != 1 {
		t.Errorf("again: %+v, %v, calls %d", rep, err, f.neutraliseCalls)
	}
}

func TestFinalizeRefusesWhileAnythingIsNotConverted(t *testing.T) {
	f, box := converted(t)
	// A value that was never converted: a new row in the old place.
	f.legacy["api_keys.token"] = append(f.legacy["api_keys.token"], Row{ID: 99, Value: laravelEncrypt(t, "never converted")})
	before := oldValues(f)
	rep, err := Finalize(context.Background(), f, box, FinalizeOptions{})
	if !errors.Is(err, ErrNotReady) || oldValues(f) != before || f.neutraliseCalls != 0 {
		t.Fatalf("err = %v, old values %d -> %d", err, before, oldValues(f))
	}
	if len(rep.Problems) != 1 || rep.Problems[0].RowID != 99 || !strings.Contains(rep.Problems[0].Reason, "migrate-secrets") {
		t.Errorf("problems = %+v", rep.Problems)
	}
	for _, c := range rep.Columns {
		if c.Neutralised != 0 {
			t.Errorf("%s says %d would be done although nothing is", c.Column, c.Neutralised)
		}
	}

	// A value that changed in the old place after it was converted.
	f, box = converted(t)
	f.legacy["nodes.daemon_token"][0] = Row{ID: 1, Value: laravelEncrypt(t, "changed since")}
	if _, err := Finalize(context.Background(), f, box, FinalizeOptions{}); !errors.Is(err, ErrNotReady) || f.neutraliseCalls != 0 {
		t.Errorf("a changed value: %v", err)
	}

	// A converted copy that is missing.
	f, box = converted(t)
	delete(f.converted["users.totp_secret"], 3)
	f.converted["users.totp_secret"][3] = Write{Column: f.converted["nodes.daemon_token"][1].Column, RowID: 3, Sealed: "", SourceHash: sourceHash(f.legacy["users.totp_secret"][0].Value)}
	rep, err = Finalize(context.Background(), f, box, FinalizeOptions{})
	if !errors.Is(err, ErrNotReady) || len(rep.Problems) != 1 || !strings.Contains(rep.Problems[0].Reason, "missing") {
		t.Errorf("a missing copy: %v, %+v", err, rep.Problems)
	}
}

// The key of the panel is not the one the copies were sealed with: that is the case in which the old values would be the only ones that can be read.
func TestFinalizeDoesNotTrustACopyItCannotOpen(t *testing.T) {
	f, _ := converted(t)
	other, err := secrets.NewBox(bytes.Repeat([]byte{0x55}, 32))
	if err != nil {
		t.Fatal(err)
	}
	before := oldValues(f)
	rep, err := Finalize(context.Background(), f, other, FinalizeOptions{})
	if !errors.Is(err, ErrNotReady) || oldValues(f) != before || f.neutraliseCalls != 0 || len(rep.Problems) != before {
		t.Fatalf("err = %v, old values %d -> %d, problems %d", err, before, oldValues(f), len(rep.Problems))
	}
	for _, p := range rep.Problems {
		if !strings.Contains(p.Reason, "cannot be opened") {
			t.Errorf("problem = %+v", p)
		}
	}

	// A copy that was moved to another row does not open either: it is bound to its place.
	f, box := converted(t)
	w := f.converted["nodes.daemon_token"][1]
	f.converted["nodes.daemon_token"][2] = Write{Column: w.Column, RowID: 2, Sealed: w.Sealed, SourceHash: f.converted["nodes.daemon_token"][2].SourceHash}
	if _, err := Finalize(context.Background(), f, box, FinalizeOptions{}); !errors.Is(err, ErrNotReady) {
		t.Errorf("a copy of another row: %v", err)
	}
}

func TestFinalizeWhenTheStoreFails(t *testing.T) {
	f, box := converted(t)
	f.failNeutralise = errors.New("a row changed")
	rep, err := Finalize(context.Background(), f, box, FinalizeOptions{})
	if err == nil || errors.Is(err, ErrNotReady) || !strings.Contains(err.Error(), "make the old values empty") {
		t.Fatalf("err = %v", err)
	}
	for _, c := range rep.Columns {
		if c.Neutralised != 0 {
			t.Errorf("%s says %d were done", c.Column, c.Neutralised)
		}
	}
	if _, err := Finalize(context.Background(), failingFinal{f}, box, FinalizeOptions{}); err == nil {
		t.Error("a store that cannot read was taken for one with nothing to do")
	}
}

type failingFinal struct{ *fakeStore }

func (failingFinal) SealedValues(context.Context, Column) (map[uint64]string, error) {
	return nil, errors.New("down")
}
