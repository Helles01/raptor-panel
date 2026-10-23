package schedules

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Helles01/raptor-panel/internal/backups"
)

// ErrAlreadyRunning refuses to start a schedule by hand while a run of it is going on. The reference lets two runs of the
// same schedule go side by side (C-052).
var ErrAlreadyRunning = errors.New("this schedule is already running")

// Store is how the scheduler reads and writes schedules. Each method is one statement or one transaction.
type Store interface {
	// Due returns the schedules that may start now: the ones for which Due holds, found with a query on the database.
	Due(ctx context.Context, now time.Time) ([]Schedule, error)
	// Schedule returns one schedule, for a run started by hand.
	Schedule(ctx context.Context, id uint64) (Schedule, error)
	// Tasks returns the tasks of a schedule.
	Tasks(ctx context.Context, scheduleID uint64) ([]Task, error)
	// Begin marks the schedule as running, stores its next time and queues the first task, in one transaction. It returns false
	// when the schedule was already running (a conditional update), so that two runs can never start side by side.
	Begin(ctx context.Context, scheduleID uint64, nextRun time.Time, first Task) (bool, error)
	// Queue and Dequeue mark a task as waiting to run, or not.
	Queue(ctx context.Context, t Task) error
	Dequeue(ctx context.Context, t Task) error
	// Finish marks the schedule as not running and stores the time of the run.
	Finish(ctx context.Context, scheduleID uint64, at time.Time) error
	// Progress reads what is needed before a task runs: whether the schedule is switched on, and the status of its server.
	Progress(ctx context.Context, s Schedule) (active bool, serverStatus string, err error)
	// ResetRunning clears the running marks of every schedule and task. At start no run can be going on, since the runs live in
	// this process, so a mark that is set belongs to a run that a stop or a crash cut short.
	ResetRunning(ctx context.Context) error
}

// Executor does what the tasks say. An error that comes from the node (it could not be reached, or it refused) must wrap ErrNode.
type Executor interface {
	// State is the state of the server as the node reports it.
	State(ctx context.Context, serverID uint64) (string, error)
	Power(ctx context.Context, serverID uint64, action string) error
	Command(ctx context.Context, serverID uint64, command string) error
	// Backup makes a backup that leaves out the given files. It is the one action that is not only a call to the node.
	Backup(ctx context.Context, serverID uint64, ignored []string) error
}

// Clock is the time the scheduler runs by, so that tests need not wait.
type Clock interface {
	Now() time.Time
	// Wait blocks for d, or until ctx ends, and reports whether the whole time passed.
	Wait(ctx context.Context, d time.Duration) bool
}

// DefaultConcurrency is how many actions run against nodes at the same moment.
const DefaultConcurrency = 16

// Runner starts the schedules that are due and runs their tasks, one after another, each after its own wait. It holds no state
// of the schedules: every run is marked in the Store.
type Runner struct {
	store Store
	exec  Executor
	clock Clock
	// logf receives what went wrong and could not be reported to anyone; it may be nil.
	logf func(format string, args ...any)

	slots chan struct{}
	wg    sync.WaitGroup
}

// NewRunner returns a Runner. concurrency bounds the actions that run at once (DefaultConcurrency when it is below 1).
func NewRunner(store Store, exec Executor, clock Clock, concurrency int, logf func(format string, args ...any)) *Runner {
	if concurrency < 1 {
		concurrency = DefaultConcurrency
	}
	return &Runner{store: store, exec: exec, clock: clock, logf: logf, slots: make(chan struct{}, concurrency)}
}

func (r *Runner) log(format string, args ...any) {
	if r.logf != nil {
		r.logf(format, args...)
	}
}

// Recover clears the marks of runs that a stop or a crash cut short. Call it once, before the first Tick.
func (r *Runner) Recover(ctx context.Context) error {
	if err := r.store.ResetRunning(ctx); err != nil {
		return fmt.Errorf("reset running schedules: %w", err)
	}
	return nil
}

// Tick starts every schedule that is due and returns how many it started. A schedule that cannot be started is logged and the
// others still are: one bad schedule must not hold back the rest (the reference learned that, issue 2609).
func (r *Runner) Tick(ctx context.Context) (int, error) {
	due, err := r.store.Due(ctx, r.clock.Now())
	if err != nil {
		return 0, fmt.Errorf("find due schedules: %w", err)
	}
	started := 0
	for _, s := range due {
		ok, err := r.start(ctx, s, false)
		if err != nil {
			r.log("schedule %d: %v", s.ID, err)
			continue
		}
		if ok {
			started++
		}
	}
	return started, nil
}

// Execute starts a schedule by hand, whether or not it is switched on. The first task runs at once and the rest after their waits.
// It returns when the run has begun; the run goes on in the background. The permission to do this is for the caller to check.
func (r *Runner) Execute(ctx context.Context, scheduleID uint64) error {
	s, err := r.store.Schedule(ctx, scheduleID)
	if err != nil {
		return fmt.Errorf("load schedule: %w", err)
	}
	if s.Processing {
		return ErrAlreadyRunning
	}
	ok, err := r.start(ctx, s, true)
	if err != nil {
		return err
	}
	if !ok {
		return ErrAlreadyRunning
	}
	return nil
}

// Run calls Tick every interval until ctx ends, then waits for the runs in progress, which stop at their next wait.
func (r *Runner) Run(ctx context.Context, interval time.Duration) {
	for {
		if _, err := r.Tick(ctx); err != nil {
			r.log("%v", err)
		}
		if !r.clock.Wait(ctx, interval) {
			r.wg.Wait()
			return
		}
	}
}

// Wait blocks until the runs that have started are over.
func (r *Runner) Wait() { r.wg.Wait() }

// start begins a run. It returns false when another run got there first.
func (r *Runner) start(ctx context.Context, s Schedule, manual bool) (bool, error) {
	tasks, err := r.store.Tasks(ctx, s.ID)
	if err != nil {
		return false, fmt.Errorf("load tasks: %w", err)
	}
	plan, err := PlanStart(s, tasks, r.clock.Now())
	if err != nil {
		return false, err
	}
	began, err := r.store.Begin(ctx, s.ID, plan.NextRunAt, plan.First)
	if err != nil || !began {
		return false, err
	}

	if s.OnlyWhenOnline {
		state, stateErr := r.exec.State(ctx, s.ServerID)
		if !OnlineGate(true, state, stateErr == nil) {
			r.end(ctx, s, plan.First)
			return true, nil
		}
	}

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.chain(ctx, s, plan.First, manual)
	}()
	return true, nil
}

// chain runs the tasks of one run in order.
func (r *Runner) chain(ctx context.Context, s Schedule, t Task, manual bool) {
	first := true
	for {
		if wait := time.Duration(t.TimeOffset) * time.Second; wait > 0 && !(manual && first) {
			if !r.clock.Wait(ctx, wait) {
				return // stopping: the marks stay, and Recover clears them at the next start
			}
		}
		first = false

		active, status, err := r.store.Progress(ctx, s)
		if err != nil {
			r.log("schedule %d: read progress: %v", s.ID, err)
			r.end(ctx, s, t)
			return
		}
		if BeforeTask(active, manual, status) != GoOn {
			r.end(ctx, s, t)
			return
		}

		err = r.do(ctx, s, t)
		if err != nil {
			r.log("schedule %d, task %d: %v", s.ID, t.ID, err)
		}
		if derr := r.store.Dequeue(ctx, t); derr != nil {
			r.log("schedule %d, task %d: unqueue: %v", s.ID, t.ID, derr)
		}
		if !AfterTask(t, Classify(err)) {
			r.finish(ctx, s)
			return
		}

		// The tasks are read again, as the reference does, so that a task that was added or removed meanwhile counts.
		tasks, err := r.store.Tasks(ctx, s.ID)
		if err != nil {
			r.log("schedule %d: load tasks: %v", s.ID, err)
			r.finish(ctx, s)
			return
		}
		next, ok := NextTask(tasks, t.Sequence)
		if !ok {
			r.finish(ctx, s)
			return
		}
		if err := r.store.Queue(ctx, next); err != nil {
			r.log("schedule %d, task %d: queue: %v", s.ID, next.ID, err)
			r.finish(ctx, s)
			return
		}
		t = next
	}
}

// do carries out one task, holding one of the slots for the actions against nodes.
func (r *Runner) do(ctx context.Context, s Schedule, t Task) error {
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	case <-ctx.Done():
		return ctx.Err()
	}

	switch t.Action {
	case ActionPower:
		return r.exec.Power(ctx, s.ServerID, t.Payload)
	case ActionCommand:
		return r.exec.Command(ctx, s.ServerID, t.Payload)
	case ActionBackup:
		ignored, err := backups.ParseIgnored(t.Payload)
		if err != nil {
			return fmt.Errorf("the files to leave out of the backup: %w", err)
		}
		return r.exec.Backup(ctx, s.ServerID, ignored)
	}
	return fmt.Errorf("invalid task action %q", t.Action)
}

// end stops a run before its task: the task is no longer waiting, and the run is over.
func (r *Runner) end(ctx context.Context, s Schedule, t Task) {
	if err := r.store.Dequeue(ctx, t); err != nil {
		r.log("schedule %d, task %d: unqueue: %v", s.ID, t.ID, err)
	}
	r.finish(ctx, s)
}

func (r *Runner) finish(ctx context.Context, s Schedule) {
	if err := r.store.Finish(ctx, s.ID, r.clock.Now()); err != nil {
		r.log("schedule %d: finish: %v", s.ID, err)
	}
}
