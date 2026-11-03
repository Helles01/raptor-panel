package backupapi

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api/controlapi"
	"github.com/Helles01/raptor-panel/internal/backups"
	"github.com/Helles01/raptor-panel/internal/wings"
)

func (r *rig) service(t *testing.T) *Service {
	t.Helper()
	s, err := NewService(ServiceDeps{
		Store: r.store, Targets: r.targets, Daemon: r.daemon, ThrottleLimit: 2, ThrottlePeriod: 600 * time.Second,
		Logger: slog.New(slog.NewTextHandler(r.logs, nil)), Now: func() time.Time { return t0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestServiceStartsABackup(t *testing.T) {
	r := newRig(t)
	rec, err := r.service(t).Start(context.Background(), 100, StartParams{Name: "Nightly", Ignored: []string{"*.log", "cache/"}})
	if err != nil || rec.Name != "Nightly" || rec.Disk != "wings" {
		t.Fatalf("rec %+v, err %v", rec, err)
	}
	if got := r.daemon.calls[len(r.daemon.calls)-1]; got.Kind != "create" || got.Ignore != "*.log\ncache/" || got.BackupUUID != rec.UUID {
		t.Errorf("node call = %+v", got)
	}
}

func TestServiceReplacesTheOldestOnlyWhenAsked(t *testing.T) {
	r := newRig(t)
	r.store.limit = ptr(int64(4)) // the four that count are 1, 3, 4 and 5
	s := r.service(t)

	_, err := s.Start(context.Background(), 100, StartParams{Name: "x"})
	var tooMany *backups.TooManyError
	if !errors.As(err, &tooMany) || len(r.store.pruned) != 0 || len(r.store.starts) != 0 {
		t.Fatalf("at the limit without Replace: %v, pruned %v", err, r.store.pruned)
	}

	rec, err := s.Start(context.Background(), 100, StartParams{Name: "Scheduled", Replace: true})
	if err != nil || rec.Name != "Scheduled" {
		t.Fatalf("with Replace: %+v %v", rec, err)
	}
	// The oldest backup that is not locked is the one on S3 (5): its row went in the transaction, its archive is deleted on the node
	// before the new one is made, and the locked one (4) is left alone.
	if len(r.store.pruned) != 1 || r.store.pruned[0] != 5 {
		t.Errorf("pruned = %v", r.store.pruned)
	}
	var kinds []string
	for _, c := range r.daemon.calls {
		kinds = append(kinds, c.Kind)
	}
	if strings.Join(kinds, ",") != "delete,create" || r.daemon.calls[0].BackupUUID != uuidN(5) {
		t.Errorf("node calls = %+v", r.daemon.calls)
	}
}

func TestServiceDoesNotReplaceWhatIsLockedOrWhenThereIsNoLimit(t *testing.T) {
	r := newRig(t)
	r.store.limit = ptr(int64(4))
	for i := range r.store.records {
		r.store.records[i].Locked = true
	}
	_, err := r.service(t).Start(context.Background(), 100, StartParams{Name: "x", Replace: true})
	var tooMany *backups.TooManyError
	if !errors.As(err, &tooMany) || len(r.store.pruned) != 0 {
		t.Errorf("all locked: %v", err)
	}
	r = newRig(t)
	r.store.limit = nil
	if _, err := r.service(t).Start(context.Background(), 100, StartParams{Name: "x", Replace: true}); !errors.As(err, &tooMany) {
		t.Errorf("no limit means no backups: %v", err)
	}
}

func TestServiceThrottlesEveryoneAlike(t *testing.T) {
	r := newRig(t)
	s := r.service(t)
	for i := 0; i < 2; i++ {
		if _, err := s.Start(context.Background(), 100, StartParams{Name: "x", Replace: true}); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
	}
	var throttle *backups.ThrottleError
	if _, err := s.Start(context.Background(), 100, StartParams{Name: "x", Replace: true}); !errors.As(err, &throttle) {
		t.Errorf("the third in the window: %v", err)
	}
}

func TestServiceWhenTheNodeCannotBeTold(t *testing.T) {
	r := newRig(t)
	r.store.limit = ptr(int64(4))
	r.daemon.createErr = &wings.Error{Status: 504, Message: "no reply", Unreachable: true}
	_, err := r.service(t).Start(context.Background(), 100, StartParams{Name: "x", Replace: true})
	var we *wings.Error
	if !errors.As(err, &we) {
		t.Fatalf("err = %v", err)
	}
	// The new row is taken back; the replaced backup is gone all the same, and its archive was asked to go.
	if len(r.store.deleted) != 1 || len(r.store.pruned) != 1 || r.daemon.count("delete") != 1 {
		t.Errorf("deleted %v, pruned %v, node calls %+v", r.store.deleted, r.store.pruned, r.daemon.calls)
	}

	// A node that cannot delete the replaced archive is only logged.
	r = newRig(t)
	r.store.limit = ptr(int64(4))
	r.daemon.deleteErr = &wings.Error{Status: 500, Message: "no", NodeStatus: 500}
	if _, err := r.service(t).Start(context.Background(), 100, StartParams{Name: "x", Replace: true}); err != nil || !strings.Contains(r.logs.String(), "replaced backup was not deleted") {
		t.Errorf("err %v, log %s", err, r.logs.String())
	}

	// A server whose node is not known has nothing taken from it.
	r = newRig(t)
	r.targets.err = controlapi.ErrNotFound
	if _, err := r.service(t).Start(context.Background(), 100, StartParams{Name: "x"}); !errors.Is(err, controlapi.ErrNotFound) || len(r.store.deleted) != 1 {
		t.Errorf("no node: %v, deleted %v", err, r.store.deleted)
	}
}

func TestNewServiceNeedsEverything(t *testing.T) {
	r := newRig(t)
	full := ServiceDeps{Store: r.store, Targets: r.targets, Daemon: r.daemon, Logger: slog.New(slog.NewTextHandler(r.logs, nil))}
	if s, err := NewService(full); err != nil || s.d.Now == nil || s.d.Random == nil {
		t.Fatalf("defaults: %v", err)
	}
	for name, mutate := range map[string]func(*ServiceDeps){
		"no store": func(d *ServiceDeps) { d.Store = nil }, "no targets": func(d *ServiceDeps) { d.Targets = nil },
		"no daemon": func(d *ServiceDeps) { d.Daemon = nil }, "no logger": func(d *ServiceDeps) { d.Logger = nil },
	} {
		d := full
		mutate(&d)
		if _, err := NewService(d); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// The store is told where the throttle window starts, so that it reads only the creation times that count; with the throttle off it reads none.
func TestServiceTellsTheStoreTheWindowOfTheThrottle(t *testing.T) {
	r := newRig(t)
	if _, err := r.service(t).Start(context.Background(), 100, StartParams{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	if want := t0.Add(-600 * time.Second); !r.store.since.Equal(want) {
		t.Errorf("window starts at %v, want %v", r.store.since, want)
	}

	r.store.limit = ptr(int64(50))
	s, err := NewService(ServiceDeps{Store: r.store, Targets: r.targets, Daemon: r.daemon, ThrottlePeriod: 0, Logger: slog.New(slog.NewTextHandler(r.logs, nil)), Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(context.Background(), 100, StartParams{Name: "y"}); err != nil {
		t.Fatal(err)
	}
	if !r.store.since.IsZero() {
		t.Errorf("with the throttle off the window starts at %v", r.store.since)
	}
}
