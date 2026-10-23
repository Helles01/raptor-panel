package schedules

import (
	"errors"
	"slices"
	"time"
)

// Schedule is what the scheduler needs to know about a schedule to decide whether and how it runs.
type Schedule struct {
	ID       uint64
	ServerID uint64
	Name     string
	Cron     Cron
	// Active is the switch of the owner. Processing is set while a run is going on.
	Active         bool
	Processing     bool
	OnlyWhenOnline bool
	NextRunAt      *time.Time
	// ServerStatus is the status of the server: empty in the normal state.
	ServerStatus string
}

// ErrNoTasks is a schedule that has nothing to run. The text is the reference's.
var ErrNoTasks = errors.New("Cannot process schedule for task execution: no tasks are registered.") //nolint:staticcheck // the reference's text

// Due says whether the scheduler starts a schedule now: it is switched on, not running, its server is in the normal
// state, its time has come and it has a task. A schedule without a next time never starts.
func Due(s Schedule, tasks int, now time.Time) bool {
	return s.Active && !s.Processing && s.ServerStatus == "" && s.NextRunAt != nil && !s.NextRunAt.After(now) && tasks > 0
}

// Start is what beginning a run stores: the schedule is marked as running and its next time is worked out from now (also for a
// run started by hand), and the first task is queued.
type Start struct {
	NextRunAt time.Time
	First     Task
}

// PlanStart decides how a run begins. The first task is the one with the lowest sequence, whatever order tasks come in.
func PlanStart(s Schedule, tasks []Task, now time.Time) (Start, error) {
	first, ok := First(tasks)
	if !ok {
		return Start{}, ErrNoTasks
	}
	next, err := s.Cron.Next(now)
	if err != nil {
		return Start{}, err
	}
	return Start{NextRunAt: next, First: first}, nil
}

// First returns the task with the lowest sequence.
func First(tasks []Task) (Task, bool) {
	if len(tasks) == 0 {
		return Task{}, false
	}
	return slices.MinFunc(tasks, func(a, b Task) int { return a.Sequence - b.Sequence }), true
}

// NextTask returns the task that follows the one at sequence after: the lowest sequence that is greater.
func NextTask(tasks []Task, after int) (Task, bool) {
	var next Task
	found := false
	for _, t := range tasks {
		if t.Sequence > after && (!found || t.Sequence < next.Sequence) {
			next, found = t, true
		}
	}
	return next, found
}

// The state of a server as the node reports it, as far as a schedule cares.
const (
	stateOffline  = "offline"
	stateStopping = "stopping"
)

// OnlineGate is the check of a schedule that runs only while its server is up. state is what the node answered ("" when it
// said nothing, which counts as offline) and reachable says whether it answered at all. A schedule without the setting always
// runs. A run that does not pass ends at once, quietly: it is not an error and it has no effect but the time it was due.
func OnlineGate(onlyWhenOnline bool, state string, reachable bool) bool {
	if !onlyWhenOnline {
		return true
	}
	if state == "" {
		state = stateOffline
	}
	return reachable && state != stateOffline && state != stateStopping
}

// Gate is what is decided before a queued task runs.
type Gate int

const (
	// GoOn: run the task.
	GoOn Gate = iota
	// EndInactive: the owner switched the schedule off after it was queued, so the run ends. A run started by hand goes on.
	EndInactive
	// EndBusy: the server is suspended, installing or in another state that is not the normal one, so the run ends.
	EndBusy
)

// BeforeTask decides whether a queued task runs now.
func BeforeTask(active, manual bool, serverStatus string) Gate {
	switch {
	case !active && !manual:
		return EndInactive
	case serverStatus != "":
		return EndBusy
	}
	return GoOn
}

// Failure is how a task's action went wrong.
type Failure int

const (
	// NoFailure: it worked.
	NoFailure Failure = iota
	// NodeFailure: the node could not be reached or refused the action. A task with ContinueOnFailure goes on after it.
	NodeFailure
	// PanelFailure: something on the panel's side failed, such as a backup limit. It always ends the run.
	PanelFailure
)

// ErrNode marks an error as the node's: the node could not be reached or answered with an error. The code that talks to the
// node wraps what it returns with it, so that Classify can tell it from a failure of the panel.
var ErrNode = errors.New("the node could not do it")

// Classify turns the error of an action into a Failure.
func Classify(err error) Failure {
	switch {
	case err == nil:
		return NoFailure
	case errors.Is(err, ErrNode):
		return NodeFailure
	}
	return PanelFailure
}

// AfterTask says whether the run goes on to the next task after this one finished with f.
func AfterTask(t Task, f Failure) bool {
	switch f {
	case NoFailure:
		return true
	case NodeFailure:
		return t.ContinueOnFailure
	}
	return false
}
