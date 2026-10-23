package schedules

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"
)

// fakeStore keeps schedules and tasks in memory and records what the runner did.
type fakeStore struct {
	mu        sync.Mutex
	schedules map[uint64]*Schedule
	tasks     map[uint64][]Task
	queued    map[uint64]bool
	lastRun   map[uint64]time.Time
	statuses  map[uint64]string // by server id
	calls     []string

	beginFalse    bool
	queueErr      error
	dueErr        error
	tasksErr      map[uint64]error
	progressErr   error
	afterProgress func(calls int) // may change the schedule or server between tasks
	progressCalls int
	resetCalled   bool
}

func newStore() *fakeStore {
	return &fakeStore{
		schedules: map[uint64]*Schedule{}, tasks: map[uint64][]Task{}, queued: map[uint64]bool{},
		lastRun: map[uint64]time.Time{}, statuses: map[uint64]string{}, tasksErr: map[uint64]error{},
	}
}

func (f *fakeStore) add(s Schedule, tasks ...Task) {
	f.schedules[s.ID] = &s
	f.tasks[s.ID] = tasks
}

func (f *fakeStore) note(format string, a ...any) {
	f.calls = append(f.calls, fmt.Sprintf(format, a...))
}

func (f *fakeStore) Due(_ context.Context, now time.Time) ([]Schedule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dueErr != nil {
		return nil, f.dueErr
	}
	var out []Schedule
	for _, id := range f.ids() {
		s := *f.schedules[id]
		s.ServerStatus = f.statuses[s.ServerID]
		if Due(s, len(f.tasks[id]), now) {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeStore) ids() []uint64 {
	var ids []uint64
	for id := range f.schedules {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func (f *fakeStore) Schedule(_ context.Context, id uint64) (Schedule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.schedules[id]
	if !ok {
		return Schedule{}, errors.New("no such schedule")
	}
	out := *s
	out.ServerStatus = f.statuses[s.ServerID]
	return out, nil
}

func (f *fakeStore) Tasks(_ context.Context, id uint64) ([]Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.tasksErr[id]; err != nil {
		return nil, err
	}
	return slices.Clone(f.tasks[id]), nil
}

func (f *fakeStore) Begin(_ context.Context, id uint64, next time.Time, first Task) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.beginFalse || f.schedules[id].Processing {
		return false, nil
	}
	f.schedules[id].Processing = true
	f.schedules[id].NextRunAt = &next
	f.queued[first.ID] = true
	f.note("begin %d first=%d next=%s", id, first.ID, next.Format("15:04"))
	return true, nil
}

func (f *fakeStore) Queue(ctx context.Context, t Task) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.queueErr != nil {
		return f.queueErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queued[t.ID] = true
	return nil
}

func (f *fakeStore) Dequeue(ctx context.Context, t Task) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queued[t.ID] = false
	return nil
}

func (f *fakeStore) Finish(ctx context.Context, id uint64, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.schedules[id].Processing = false
	f.lastRun[id] = at
	f.note("finish %d", id)
	return nil
}

func (f *fakeStore) Progress(_ context.Context, s Schedule) (bool, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.progressCalls++
	if f.afterProgress != nil {
		f.afterProgress(f.progressCalls)
	}
	if f.progressErr != nil {
		return false, "", f.progressErr
	}
	return f.schedules[s.ID].Active, f.statuses[s.ServerID], nil
}

func (f *fakeStore) ResetRunning(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resetCalled = true
	return nil
}

// fakeExec records the actions in order and answers from scripts.
type fakeExec struct {
	mu            sync.Mutex
	actions       []string
	state         string
	stateErr      error
	errs          map[string]error // by "action payload"
	hold          chan struct{}    // when set, every action waits for it
	running, peak int
}

func (e *fakeExec) record(s string) error {
	e.mu.Lock()
	e.actions = append(e.actions, s)
	e.running++
	if e.running > e.peak {
		e.peak = e.running
	}
	hold := e.hold
	err := e.errs[s]
	e.mu.Unlock()
	if hold != nil {
		<-hold
	}
	e.mu.Lock()
	e.running--
	e.mu.Unlock()
	return err
}

func (e *fakeExec) State(context.Context, uint64) (string, error) { return e.state, e.stateErr }
func (e *fakeExec) Power(_ context.Context, _ uint64, a string) error {
	return e.record("power " + a)
}
func (e *fakeExec) Command(_ context.Context, _ uint64, c string) error {
	return e.record("command " + c)
}
func (e *fakeExec) Backup(_ context.Context, _ uint64, ignored []string) error {
	return e.record(fmt.Sprintf("backup %v", ignored))
}

// fakeClock never really waits.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *fakeClock) Wait(ctx context.Context, d time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	c.mu.Lock()
	c.waits = append(c.waits, d)
	c.mu.Unlock()
	return true
}

type rig struct {
	store *fakeStore
	exec  *fakeExec
	clock *fakeClock
	run   *Runner
	logs  []string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{store: newStore(), exec: &fakeExec{state: "running", errs: map[string]error{}}, clock: &fakeClock{now: t0}}
	r.run = NewRunner(r.store, r.exec, r.clock, 4, func(f string, a ...any) { r.logs = append(r.logs, fmt.Sprintf(f, a...)) })
	return r
}

func (r *rig) schedule(t *testing.T, tasks ...Task) {
	t.Helper()
	r.store.add(Schedule{ID: 1, ServerID: 10, Name: "nightly", Cron: everyMinute(t), Active: true, NextRunAt: ago(time.Minute)}, tasks...)
}

func threeTasks() []Task {
	return []Task{
		{ID: 1, Sequence: 1, Action: ActionCommand, Payload: "say hi", TimeOffset: 0},
		{ID: 2, Sequence: 2, Action: ActionPower, Payload: "restart", TimeOffset: 30},
		{ID: 3, Sequence: 3, Action: ActionBackup, Payload: "*.log\ncache/", TimeOffset: 120},
	}
}

func (r *rig) tick(t *testing.T) int {
	t.Helper()
	n, err := r.run.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	r.run.Wait()
	return n
}

func TestTickRunsTheTasksInOrder(t *testing.T) {
	r := newRig(t)
	r.schedule(t, threeTasks()...)

	if n := r.tick(t); n != 1 {
		t.Fatalf("started %d schedules, want 1", n)
	}

	if want := []string{"command say hi", "power restart", "backup [*.log cache/]"}; !reflect.DeepEqual(r.exec.actions, want) {
		t.Errorf("actions = %v, want %v", r.exec.actions, want)
	}
	if want := []time.Duration{30 * time.Second, 120 * time.Second}; !reflect.DeepEqual(r.clock.waits, want) {
		t.Errorf("waits = %v, want %v: each task waits its own offset", r.clock.waits, want)
	}
	s := r.store.schedules[1]
	if s.Processing || !r.store.lastRun[1].Equal(t0) || !s.NextRunAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("schedule = %+v, last run %v; want it finished, run at t0 and next a minute on", s, r.store.lastRun[1])
	}
	for id, q := range r.store.queued {
		if q {
			t.Errorf("task %d is still queued", id)
		}
	}
}

func TestTickWaitsTheOffsetOfTheFirstTaskToo(t *testing.T) {
	r := newRig(t)
	r.schedule(t, Task{ID: 1, Sequence: 1, Action: ActionCommand, Payload: "x", TimeOffset: 45})

	r.tick(t)

	if want := []time.Duration{45 * time.Second}; !reflect.DeepEqual(r.clock.waits, want) {
		t.Errorf("waits = %v, want %v", r.clock.waits, want)
	}
}

func TestTickSkipsWhatIsNotDue(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*rig)
	}{
		{"switched off", func(r *rig) { r.store.schedules[1].Active = false }},
		{"running", func(r *rig) { r.store.schedules[1].Processing = true }},
		{"not yet", func(r *rig) { x := t0.Add(time.Minute); r.store.schedules[1].NextRunAt = &x }},
		{"server suspended", func(r *rig) { r.store.statuses[10] = "suspended" }},
		{"no tasks", func(r *rig) { r.store.tasks[1] = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			r.schedule(t, threeTasks()...)
			tt.mutate(r)

			if n := r.tick(t); n != 0 || len(r.exec.actions) != 0 {
				t.Errorf("started %d, ran %v; want nothing", n, r.exec.actions)
			}
		})
	}
}

func TestTickRunsOnlyWhenOnline(t *testing.T) {
	tests := []struct {
		name     string
		state    string
		stateErr error
		wantRun  bool
	}{
		{"running", "running", nil, true},
		{"offline", "offline", nil, false},
		{"stopping", "stopping", nil, false},
		{"unreachable", "running", errors.Join(errors.New("down"), ErrNode), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			r.schedule(t, threeTasks()...)
			r.store.schedules[1].OnlyWhenOnline = true
			r.exec.state, r.exec.stateErr = tt.state, tt.stateErr

			r.tick(t)

			if ran := len(r.exec.actions) > 0; ran != tt.wantRun {
				t.Errorf("ran = %v, want %v", ran, tt.wantRun)
			}
			s := r.store.schedules[1]
			if s.Processing || r.store.lastRun[1].IsZero() || !s.NextRunAt.Equal(t0.Add(time.Minute)) {
				t.Errorf("a skipped run must still finish, with its time recorded and the next one set: %+v", s)
			}
			if r.store.queued[1] {
				t.Error("the first task is still queued after a skipped run")
			}
		})
	}
}

func TestAFailureOfTheNodeEndsTheRunUnlessTheTaskGoesOn(t *testing.T) {
	nodeDown := errors.Join(errors.New("503"), ErrNode)
	tests := []struct {
		name     string
		cont     bool
		err      error
		wantRuns int
	}{
		{"node down", false, nodeDown, 1},
		{"node down, task goes on", true, nodeDown, 3},
		{"panel failure, task goes on", true, errors.New("backup limit"), 1},
		{"panel failure", false, errors.New("backup limit"), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			tasks := threeTasks()
			tasks[0].ContinueOnFailure = tt.cont
			r.schedule(t, tasks...)
			r.exec.errs["command say hi"] = tt.err

			r.tick(t)

			if len(r.exec.actions) != tt.wantRuns {
				t.Errorf("ran %v, want %d actions", r.exec.actions, tt.wantRuns)
			}
			if r.store.schedules[1].Processing || r.store.queued[1] {
				t.Error("the run must be finished and the task not queued")
			}
		})
	}
}

func TestTheRunEndsWhenTheScheduleIsSwitchedOffMeanwhile(t *testing.T) {
	r := newRig(t)
	r.schedule(t, threeTasks()...)
	r.store.afterProgress = func(calls int) {
		if calls == 2 {
			r.store.schedules[1].Active = false
		}
	}

	r.tick(t)

	if want := []string{"command say hi"}; !reflect.DeepEqual(r.exec.actions, want) {
		t.Errorf("actions = %v, want only the first: %v", r.exec.actions, want)
	}
	if r.store.schedules[1].Processing {
		t.Error("the run must be finished")
	}
}

func TestTheRunEndsWhenTheServerIsSuspendedMeanwhile(t *testing.T) {
	r := newRig(t)
	r.schedule(t, threeTasks()...)
	r.store.afterProgress = func(calls int) {
		if calls == 2 {
			r.store.statuses[10] = "suspended"
		}
	}

	r.tick(t)

	if len(r.exec.actions) != 1 || r.store.schedules[1].Processing {
		t.Errorf("actions = %v, processing = %v", r.exec.actions, r.store.schedules[1].Processing)
	}
}

func TestExecuteRunsTheFirstTaskAtOnce(t *testing.T) {
	r := newRig(t)
	r.schedule(t, Task{ID: 1, Sequence: 1, Action: ActionCommand, Payload: "a", TimeOffset: 60}, Task{ID: 2, Sequence: 2, Action: ActionCommand, Payload: "b", TimeOffset: 10})
	r.store.schedules[1].NextRunAt = ago(-time.Hour) // not due

	if err := r.run.Execute(context.Background(), 1); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	r.run.Wait()

	if want := []string{"command a", "command b"}; !reflect.DeepEqual(r.exec.actions, want) {
		t.Errorf("actions = %v, want %v", r.exec.actions, want)
	}
	if want := []time.Duration{10 * time.Second}; !reflect.DeepEqual(r.clock.waits, want) {
		t.Errorf("waits = %v, want %v: the first task of a run by hand does not wait", r.clock.waits, want)
	}
}

func TestExecuteWorksOnASwitchedOffSchedule(t *testing.T) {
	r := newRig(t)
	r.schedule(t, threeTasks()...)
	r.store.schedules[1].Active = false

	if err := r.run.Execute(context.Background(), 1); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	r.run.Wait()

	if len(r.exec.actions) != 3 {
		t.Errorf("actions = %v, want all three: a run by hand does not care for the switch", r.exec.actions)
	}
}

func TestExecuteRefusesARunningSchedule(t *testing.T) {
	r := newRig(t)
	r.schedule(t, threeTasks()...)
	r.store.schedules[1].Processing = true

	err := r.run.Execute(context.Background(), 1)

	if !errors.Is(err, ErrAlreadyRunning) || len(r.exec.actions) != 0 {
		t.Errorf("err = %v, actions = %v", err, r.exec.actions)
	}
}

func TestExecuteWhenAnotherRunBeganFirst(t *testing.T) {
	r := newRig(t)
	r.schedule(t, threeTasks()...)
	r.store.beginFalse = true

	if err := r.run.Execute(context.Background(), 1); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("err = %v, want ErrAlreadyRunning", err)
	}
}

func TestExecuteErrors(t *testing.T) {
	r := newRig(t)
	r.schedule(t)

	if err := r.run.Execute(context.Background(), 1); !errors.Is(err, ErrNoTasks) {
		t.Errorf("without tasks: err = %v, want ErrNoTasks", err)
	}
	if err := r.run.Execute(context.Background(), 99); err == nil {
		t.Error("an unknown schedule must be an error")
	}
	r.store.tasksErr[1] = errors.New("db down")
	if err := r.run.Execute(context.Background(), 1); err == nil {
		t.Error("a failing store must be an error")
	}
}

func TestExecuteWithAnOnlineScheduleThatIsOffline(t *testing.T) {
	r := newRig(t)
	r.schedule(t, threeTasks()...)
	r.store.schedules[1].OnlyWhenOnline = true
	r.exec.state = "offline"

	if err := r.run.Execute(context.Background(), 1); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	r.run.Wait()

	if len(r.exec.actions) != 0 || r.store.schedules[1].Processing {
		t.Errorf("actions = %v; the setting also holds for a run by hand", r.exec.actions)
	}
}

func TestBeginRefusedMeansNoRun(t *testing.T) {
	r := newRig(t)
	r.schedule(t, threeTasks()...)
	r.store.beginFalse = true

	if n := r.tick(t); n != 0 || len(r.exec.actions) != 0 {
		t.Errorf("started %d, ran %v; another run got there first", n, r.exec.actions)
	}
}

func TestOneBadScheduleDoesNotHoldBackTheOthers(t *testing.T) {
	r := newRig(t)
	r.schedule(t, threeTasks()...)
	r.store.add(Schedule{ID: 2, ServerID: 11, Cron: everyMinute(t), Active: true, NextRunAt: ago(time.Minute)}, Task{ID: 9, Sequence: 1, Action: ActionCommand, Payload: "ok"})
	r.store.tasksErr[1] = errors.New("db hiccup")

	n := r.tick(t)

	if n != 1 || !slices.Contains(r.exec.actions, "command ok") {
		t.Errorf("started %d, ran %v; schedule 2 must have run", n, r.exec.actions)
	}
	if len(r.logs) == 0 {
		t.Error("the failure of schedule 1 was not logged")
	}
}

func TestTickReportsAFailingStore(t *testing.T) {
	r := newRig(t)
	r.store.dueErr = errors.New("db down")

	if _, err := r.run.Tick(context.Background()); err == nil {
		t.Error("Tick must report that it could not look for due schedules")
	}
}

func TestTasksAreReadAgainBetweenSteps(t *testing.T) {
	r := newRig(t)
	r.schedule(t, Task{ID: 1, Sequence: 1, Action: ActionCommand, Payload: "first"})
	r.store.afterProgress = func(calls int) {
		if calls == 1 {
			r.store.tasks[1] = append(r.store.tasks[1], Task{ID: 2, Sequence: 2, Action: ActionCommand, Payload: "added meanwhile"})
		}
	}

	r.tick(t)

	if want := []string{"command first", "command added meanwhile"}; !reflect.DeepEqual(r.exec.actions, want) {
		t.Errorf("actions = %v, want %v", r.exec.actions, want)
	}
}

func TestAnUnreadableProgressEndsTheRun(t *testing.T) {
	r := newRig(t)
	r.schedule(t, threeTasks()...)
	r.store.progressErr = errors.New("db down")

	r.tick(t)

	if len(r.exec.actions) != 0 || r.store.schedules[1].Processing {
		t.Errorf("actions = %v, processing = %v; the run must end without hanging", r.exec.actions, r.store.schedules[1].Processing)
	}
}

func TestATaskThatCannotBeCarriedOut(t *testing.T) {
	tests := []struct {
		name string
		task Task
	}{
		{"an unknown action", Task{ID: 1, Sequence: 1, Action: "reboot", Payload: "x"}},
		{"files to leave out that are not allowed", Task{ID: 1, Sequence: 1, Action: ActionBackup, Payload: "ok\x00bad"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			r.schedule(t, tt.task, Task{ID: 2, Sequence: 2, Action: ActionCommand, Payload: "after"})

			r.tick(t)

			if len(r.exec.actions) != 0 || r.store.schedules[1].Processing {
				t.Errorf("actions = %v; the run must end at the bad task", r.exec.actions)
			}
		})
	}
}

func TestRecover(t *testing.T) {
	r := newRig(t)

	if err := r.run.Recover(context.Background()); err != nil || !r.store.resetCalled {
		t.Errorf("Recover = %v, reset called %v", err, r.store.resetCalled)
	}
}

type failingReset struct{ *fakeStore }

func (failingReset) ResetRunning(context.Context) error { return errors.New("db down") }

func TestRecoverReportsAFailure(t *testing.T) {
	r := newRig(t)
	run := NewRunner(failingReset{r.store}, r.exec, r.clock, 1, nil)

	if err := run.Recover(context.Background()); err == nil {
		t.Error("a failing reset must be an error")
	}
}

func TestAStoppedRunLeavesItsMarks(t *testing.T) {
	r := newRig(t)
	r.schedule(t, threeTasks()...)
	ctx, cancel := context.WithCancel(context.Background())
	r.store.afterProgress = func(calls int) {
		if calls == 1 {
			cancel() // while the first task is about to run: the wait before the second sees it
		}
	}

	if _, err := r.run.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	r.run.Wait()

	if !r.store.schedules[1].Processing {
		t.Error("a run that is cut short must leave its mark, for Recover to clear at the next start")
	}
}

func TestTheActionsAgainstNodesAreBounded(t *testing.T) {
	r := newRig(t)
	r.run = NewRunner(r.store, r.exec, r.clock, 2, nil)
	r.exec.hold = make(chan struct{})
	for i := uint64(1); i <= 6; i++ {
		r.store.add(Schedule{ID: i, ServerID: i, Cron: everyMinute(t), Active: true, NextRunAt: ago(time.Minute)},
			Task{ID: i, Sequence: 1, Action: ActionCommand, Payload: fmt.Sprint("c", i)})
	}

	n, err := r.run.Tick(context.Background())
	if err != nil || n != 6 {
		t.Fatalf("Tick = %d, %v", n, err)
	}
	// Let the first two actions enter, then release everything.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.exec.mu.Lock()
		running := r.exec.running
		r.exec.mu.Unlock()
		if running == 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(r.exec.hold)
	r.run.Wait()

	if r.exec.peak != 2 || len(r.exec.actions) != 6 {
		t.Errorf("peak = %d, actions = %d; want at most 2 at once and all 6 done", r.exec.peak, len(r.exec.actions))
	}
}

func TestRunTicksUntilStopped(t *testing.T) {
	r := newRig(t)
	r.schedule(t, Task{ID: 1, Sequence: 1, Action: ActionCommand, Payload: "x"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	ticks := 0
	r.store.afterProgress = func(int) {}
	ready := func() bool { r.exec.mu.Lock(); defer r.exec.mu.Unlock(); return len(r.exec.actions) > 0 }
	clock := &countingClock{fakeClock: r.clock, stopAfter: 3, ready: ready, cancel: cancel, ticks: &ticks}
	r.run = NewRunner(r.store, r.exec, clock, 1, nil)

	go func() { r.run.Run(ctx, time.Minute); close(done) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop when its context ended")
	}
	if ticks != 3 {
		t.Errorf("waited %d times between ticks, want 3", ticks)
	}
	if len(r.exec.actions) != 1 {
		t.Errorf("actions = %v; the schedule runs once and is then not due until its next time", r.exec.actions)
	}
}

// countingClock cancels the context after a number of waits of the loop (those of a full interval).
type countingClock struct {
	*fakeClock
	stopAfter int
	// ready says that what the test waits for has happened; the loop is stopped only then.
	ready  func() bool
	cancel context.CancelFunc
	ticks  *int
	mu2    sync.Mutex
}

func (c *countingClock) Wait(ctx context.Context, d time.Duration) bool {
	if d != time.Minute {
		return c.fakeClock.Wait(ctx, d)
	}
	c.mu2.Lock()
	*c.ticks++
	n := *c.ticks
	c.mu2.Unlock()
	if n >= c.stopAfter {
		for i := 0; i < 5000 && !c.ready(); i++ {
			time.Sleep(time.Millisecond)
		}
		c.cancel()
		return false
	}
	return true
}

func TestAFailureToQueueTheNextTaskEndsTheRun(t *testing.T) {
	r := newRig(t)
	r.schedule(t, threeTasks()...)
	r.store.queueErr = errors.New("db down")

	r.tick(t)

	if want := []string{"command say hi"}; !reflect.DeepEqual(r.exec.actions, want) {
		t.Errorf("actions = %v, want only the first: %v", r.exec.actions, want)
	}
	if r.store.schedules[1].Processing {
		t.Error("the run must be finished")
	}
}

func TestZeroConcurrencyMeansTheDefault(t *testing.T) {
	r := newRig(t)
	r.run = NewRunner(r.store, r.exec, r.clock, 0, nil)
	r.exec.hold = make(chan struct{})
	for i := uint64(1); i <= 6; i++ {
		r.store.add(Schedule{ID: i, ServerID: i, Cron: everyMinute(t), Active: true, NextRunAt: ago(time.Minute)},
			Task{ID: i, Sequence: 1, Action: ActionCommand, Payload: fmt.Sprint("c", i)})
	}

	if _, err := r.run.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.exec.mu.Lock()
		running := r.exec.running
		r.exec.mu.Unlock()
		if running == 6 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(r.exec.hold)
	r.run.Wait()

	if r.exec.peak != 6 {
		t.Errorf("peak = %d, want all 6 at once: %d are allowed by default", r.exec.peak, DefaultConcurrency)
	}
}

// sleepingClock really waits for the offsets of tasks, until the context ends.
type sleepingClock struct{ *fakeClock }

func (c sleepingClock) Wait(ctx context.Context, d time.Duration) bool {
	if d == time.Minute {
		return c.fakeClock.Wait(ctx, d)
	}
	<-ctx.Done()
	return false
}

func TestARunThatIsStoppedWhileWaitingDoesNotGoOn(t *testing.T) {
	r := newRig(t)
	r.schedule(t, threeTasks()...)
	r.run = NewRunner(r.store, r.exec, sleepingClock{r.clock}, 4, nil)
	ctx, cancel := context.WithCancel(context.Background())

	if _, err := r.run.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.exec.mu.Lock()
		n := len(r.exec.actions)
		r.exec.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	r.run.Wait()

	if want := []string{"command say hi"}; !reflect.DeepEqual(r.exec.actions, want) {
		t.Errorf("actions = %v, want %v: the second task was waiting when the run was stopped", r.exec.actions, want)
	}
	if !r.store.schedules[1].Processing {
		t.Error("a run that is stopped must leave its mark, for Recover to clear")
	}
}

func TestRunWaitsForTheRunsInProgressBeforeItReturns(t *testing.T) {
	r := newRig(t)
	r.schedule(t, Task{ID: 1, Sequence: 1, Action: ActionCommand, Payload: "slow"})
	r.exec.hold = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	ticks := 0
	// The loop is stopped only once the task is being carried out.
	ready := func() bool { r.exec.mu.Lock(); defer r.exec.mu.Unlock(); return r.exec.running > 0 }
	clock := &countingClock{fakeClock: r.clock, stopAfter: 1, ready: ready, cancel: cancel, ticks: &ticks}
	r.run = NewRunner(r.store, r.exec, clock, 1, nil)
	done := make(chan struct{})

	go func() { r.run.Run(ctx, time.Minute); close(done) }()

	select {
	case <-done:
		t.Fatal("Run returned while a task was still being carried out")
	case <-time.After(100 * time.Millisecond):
	}
	close(r.exec.hold)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return once the task was done")
	}
}
