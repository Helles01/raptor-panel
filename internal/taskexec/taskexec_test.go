package taskexec

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api/backupapi"
	"github.com/Helles01/raptor-panel/internal/api/controlapi"
	"github.com/Helles01/raptor-panel/internal/backups"
	"github.com/Helles01/raptor-panel/internal/schedules"
	"github.com/Helles01/raptor-panel/internal/wings"
)

const serverUUID = "aaaa1111-2222-4333-8444-555555555555"

var node = wings.Node{Scheme: "https", FQDN: "node1.example.com", Port: 8080, Token: "t"}

var errBoom = errors.New("the database is down: password=hunter2")

type targets struct{ err error }

func (t targets) For(_ context.Context, id uint64) (controlapi.Target, error) {
	if t.err != nil {
		return controlapi.Target{}, t.err
	}
	return controlapi.Target{UUID: serverUUID, Node: node}, nil
}

type call struct {
	Kind, UUID, Arg string
}

type daemon struct {
	mu    sync.Mutex
	calls []call
	err   error
	state string
}

func (d *daemon) rec(kind, uuid, arg string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, call{kind, uuid, arg})
	return d.err
}

func (d *daemon) Power(_ context.Context, _ wings.Node, uuid, action string) error {
	return d.rec("power", uuid, action)
}

func (d *daemon) Command(_ context.Context, _ wings.Node, uuid, command string) error {
	return d.rec("command", uuid, command)
}

func (d *daemon) Details(_ context.Context, _ wings.Node, uuid string) (wings.Details, error) {
	return wings.Details{State: d.state}, d.rec("details", uuid, "")
}

type backupStarter struct {
	got []backupapi.StartParams
	err error
}

func (b *backupStarter) Start(_ context.Context, id uint64, p backupapi.StartParams) (backupapi.Record, error) {
	b.got = append(b.got, p)
	return backupapi.Record{ID: id}, b.err
}

func newExecutor(t *testing.T) (*Executor, *daemon, *backupStarter) {
	t.Helper()
	d, b := &daemon{state: "running"}, &backupStarter{}
	loc := time.FixedZone("CEST", 2*3600)
	e, err := New(targets{}, d, b, loc, func() time.Time { return time.Date(2026, 11, 2, 10, 30, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	return e, d, b
}

func TestState(t *testing.T) {
	e, d, _ := newExecutor(t)
	state, err := e.State(context.Background(), 7)
	if err != nil || state != "running" || d.calls[0] != (call{"details", serverUUID, ""}) {
		t.Errorf("state %q, err %v, calls %+v", state, err, d.calls)
	}
	d.err = &wings.Error{Status: 504, Message: "no reply", Unreachable: true}
	if _, err := e.State(context.Background(), 7); !errors.Is(err, schedules.ErrNode) {
		t.Errorf("unreachable: %v", err)
	}
}

func TestPowerAndCommand(t *testing.T) {
	e, d, _ := newExecutor(t)
	if err := e.Power(context.Background(), 7, "restart"); err != nil {
		t.Fatal(err)
	}
	if err := e.Command(context.Background(), 7, "say hello"); err != nil {
		t.Fatal(err)
	}
	if len(d.calls) != 2 || d.calls[0] != (call{"power", serverUUID, "restart"}) || d.calls[1] != (call{"command", serverUUID, "say hello"}) {
		t.Errorf("calls = %+v", d.calls)
	}
	// Whatever the node does wrong is the node's, and the cause is not lost.
	d.err = &wings.Error{Status: 502, Message: "the server must be online", NodeStatus: 502}
	for name, err := range map[string]error{"power": e.Power(context.Background(), 7, "stop"), "command": e.Command(context.Background(), 7, "x")} {
		var we *wings.Error
		if schedules.Classify(err) != schedules.NodeFailure || !errors.As(err, &we) || we.Status != 502 {
			t.Errorf("%s: %v (%v)", name, err, schedules.Classify(err))
		}
	}
}

func TestAServerWithoutANodeIsThePanelsTrouble(t *testing.T) {
	d := &daemon{}
	e, err := New(targets{err: controlapi.ErrNotFound}, d, &backupStarter{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"power": e.Power(context.Background(), 7, "start"), "command": e.Command(context.Background(), 7, "x"), "backup": e.Backup(context.Background(), 7, nil),
		"state": func() error { _, err := e.State(context.Background(), 7); return err }(),
	} {
		if name != "backup" && (!errors.Is(err, controlapi.ErrNotFound) || schedules.Classify(err) != schedules.PanelFailure) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(d.calls) != 0 {
		t.Errorf("the node was called: %+v", d.calls)
	}
}

func TestBackup(t *testing.T) {
	e, _, b := newExecutor(t)
	if err := e.Backup(context.Background(), 7, []string{"*.log", "cache/"}); err != nil {
		t.Fatal(err)
	}
	// The name carries the time in the panel's zone, the files to leave out go on, and a full server loses its oldest backup.
	got := b.got[0]
	if got.Name != "Backup at 2026-11-02 12:30:00" || len(got.Ignored) != 2 || got.Ignored[1] != "cache/" || !got.Replace || got.Locked {
		t.Errorf("params = %+v", got)
	}
}

func TestBackupFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want schedules.Failure
	}{
		"no room":        {&backups.TooManyError{Limit: 3}, schedules.PanelFailure},
		"the throttle":   {&backups.ThrottleError{Limit: 2, Period: time.Minute, RetryAfter: time.Second}, schedules.PanelFailure},
		"the store":      {errBoom, schedules.PanelFailure},
		"a node says no": {&wings.Error{Status: 500, Message: "no", NodeStatus: 500}, schedules.NodeFailure},
		"no node":        {&wings.Error{Status: 504, Message: "no reply", Unreachable: true}, schedules.NodeFailure},
	} {
		e, _, b := newExecutor(t)
		b.err = tc.err
		err := e.Backup(context.Background(), 7, nil)
		if err == nil || schedules.Classify(err) != tc.want || !errors.Is(err, tc.err) {
			t.Errorf("%s: %v (%v)", name, err, schedules.Classify(err))
		}
	}
}

func TestNewNeedsEverything(t *testing.T) {
	d, b := &daemon{}, &backupStarter{}
	for name, args := range map[string][3]any{"no targets": {nil, d, b}, "no daemon": {targets{}, nil, b}, "no backups": {targets{}, d, nil}} {
		var tg controlapi.Targets
		var dm Daemon
		var bk Backups
		if args[0] != nil {
			tg = args[0].(controlapi.Targets)
		}
		if args[1] != nil {
			dm = args[1].(Daemon)
		}
		if args[2] != nil {
			bk = args[2].(Backups)
		}
		if _, err := New(tg, dm, bk, nil, nil); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	e, err := New(targets{}, d, b, nil, nil)
	if err != nil || e.loc != time.UTC || e.now == nil {
		t.Errorf("defaults: %v", err)
	}
}

// ---- the starter and the clock ----

type schedStore struct {
	mu         sync.Mutex
	processing bool
	loadErr    error
	started    chan struct{}
}

func (s *schedStore) Due(context.Context, time.Time) ([]schedules.Schedule, error) { return nil, nil }
func (s *schedStore) Schedule(_ context.Context, id uint64) (schedules.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cron, _ := schedules.Parse("*", "*", "*", "*", "*")
	return schedules.Schedule{ID: id, ServerID: 7, Processing: s.processing, Cron: cron, Active: true}, s.loadErr
}
func (s *schedStore) Tasks(context.Context, uint64) ([]schedules.Task, error) {
	return []schedules.Task{{ID: 1, Sequence: 1, Action: schedules.ActionCommand, Payload: "say hi"}}, nil
}
func (s *schedStore) Begin(context.Context, uint64, time.Time, schedules.Task) (bool, error) {
	close(s.started)
	return true, nil
}
func (s *schedStore) Queue(context.Context, schedules.Task) error   { return nil }
func (s *schedStore) Dequeue(context.Context, schedules.Task) error { return nil }
func (s *schedStore) Finish(context.Context, uint64, time.Time) error {
	return nil
}
func (s *schedStore) Progress(context.Context, schedules.Schedule) (bool, string, error) {
	return true, "", nil
}
func (s *schedStore) ResetRunning(context.Context) error { return nil }

func TestStarterStartsARunWithTheContextOfTheProcess(t *testing.T) {
	e, d, _ := newExecutor(t)
	st := &schedStore{started: make(chan struct{})}
	runner := schedules.NewRunner(st, e, SystemClock{}, 2, nil)
	process, stop := context.WithCancel(context.Background())
	defer stop()
	starter := NewStarter(process, runner)

	if err := starter.Start(5); err != nil {
		t.Fatal(err)
	}
	<-st.started
	runner.Wait()
	// The run went on after the call returned: the command reached the node.
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.calls) != 1 || d.calls[0] != (call{"command", serverUUID, "say hi"}) {
		t.Errorf("calls = %+v", d.calls)
	}
}

func TestStarterAnswersWhatTheRunnerSays(t *testing.T) {
	e, _, _ := newExecutor(t)
	st := &schedStore{started: make(chan struct{}), processing: true}
	starter := NewStarter(context.Background(), schedules.NewRunner(st, e, SystemClock{}, 2, nil))
	if err := starter.Start(5); !errors.Is(err, schedules.ErrAlreadyRunning) {
		t.Errorf("running: %v", err)
	}
	st.processing, st.loadErr = false, errBoom
	if err := starter.Start(5); !errors.Is(err, errBoom) {
		t.Errorf("store: %v", err)
	}
}

func TestSystemClock(t *testing.T) {
	var c SystemClock
	if d := time.Since(c.Now()); d < 0 || d > time.Second {
		t.Errorf("Now is %v off", d)
	}
	if !c.Wait(context.Background(), 5*time.Millisecond) || !c.Wait(context.Background(), 0) {
		t.Error("a wait that passed was not whole")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if c.Wait(ctx, time.Hour) || c.Wait(ctx, 0) {
		t.Error("a wait for a stopped context passed")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if c.Wait(ctx, time.Hour) || time.Since(start) > 2*time.Second {
		t.Errorf("the wait was not cut short: %v", time.Since(start))
	}
}
