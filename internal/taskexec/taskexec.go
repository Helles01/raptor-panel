// Package taskexec does what the tasks of a schedule say: it asks a node for the state of a server, sends it a power signal or a console
// command, and makes a backup of it, for the runner of internal/schedules. It also starts a run by hand for the endpoint that does so, and
// is the clock the runner goes by in production.
package taskexec

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Helles01/raptor-panel/internal/api/backupapi"
	"github.com/Helles01/raptor-panel/internal/api/controlapi"
	"github.com/Helles01/raptor-panel/internal/backups"
	"github.com/Helles01/raptor-panel/internal/schedules"
	"github.com/Helles01/raptor-panel/internal/wings"
)

// Daemon is the calls to a node that tasks make. controlapi.NewDaemon is the one that talks to a real Wings.
type Daemon interface {
	Power(ctx context.Context, n wings.Node, serverUUID, action string) error
	Command(ctx context.Context, n wings.Node, serverUUID, command string) error
	Details(ctx context.Context, n wings.Node, serverUUID string) (wings.Details, error)
}

// Backups starts backups. *backupapi.Service is one: a backup made by a schedule has the limit, the throttle and the locking of any other.
type Backups interface {
	Start(ctx context.Context, serverID uint64, p backupapi.StartParams) (backupapi.Record, error)
}

// Executor carries out the actions of tasks. It is a schedules.Executor.
type Executor struct {
	targets controlapi.Targets
	daemon  Daemon
	backups Backups
	now     func() time.Time
	loc     *time.Location
}

// New returns an Executor. The names of the backups it makes are written in the time zone of the panel (UTC when loc is nil); now
// defaults to time.Now.
func New(targets controlapi.Targets, daemon Daemon, b Backups, loc *time.Location, now func() time.Time) (*Executor, error) {
	if targets == nil || daemon == nil || b == nil {
		return nil, errors.New("taskexec: the target store, the daemon and the backups are required")
	}
	if loc == nil {
		loc = time.UTC
	}
	if now == nil {
		now = time.Now
	}
	return &Executor{targets: targets, daemon: daemon, backups: b, now: now, loc: loc}, nil
}

var _ schedules.Executor = (*Executor)(nil)

// fromNode marks an error as the node's, so that the runner knows a task with "continue on failure" may go on after it. Both the mark and
// the cause can be found in the result.
func fromNode(err error) error { return fmt.Errorf("%w: %w", schedules.ErrNode, err) }

// State is the state of the server as the node reports it. A node that cannot be asked is an error, which the runner takes for a node
// that is not there.
func (e *Executor) State(ctx context.Context, serverID uint64) (string, error) {
	t, err := e.targets.For(ctx, serverID)
	if err != nil {
		return "", err
	}
	d, err := e.daemon.Details(ctx, t.Node, t.UUID)
	if err != nil {
		return "", fromNode(err)
	}
	return d.State, nil
}

// Power sends a power signal.
func (e *Executor) Power(ctx context.Context, serverID uint64, action string) error {
	t, err := e.targets.For(ctx, serverID)
	if err != nil {
		return err
	}
	if err := e.daemon.Power(ctx, t.Node, t.UUID, action); err != nil {
		return fromNode(err)
	}
	return nil
}

// Command sends a command to the console.
func (e *Executor) Command(ctx context.Context, serverID uint64, command string) error {
	t, err := e.targets.For(ctx, serverID)
	if err != nil {
		return err
	}
	if err := e.daemon.Command(ctx, t.Node, t.UUID, command); err != nil {
		return fromNode(err)
	}
	return nil
}

// Backup makes a backup that leaves out the given files. A server at its backup limit has its oldest unlocked backup replaced. What goes
// wrong on the panel (no room, the throttle) ends the run; what goes wrong on the node is the node's.
func (e *Executor) Backup(ctx context.Context, serverID uint64, ignored []string) error {
	name, err := backups.Name("", e.now().In(e.loc))
	if err != nil {
		return err
	}
	_, err = e.backups.Start(ctx, serverID, backupapi.StartParams{Name: name, Ignored: ignored, Replace: true})
	var we *wings.Error
	if errors.As(err, &we) {
		return fromNode(err)
	}
	return err
}

// Starter starts a schedule by hand for the endpoint that does so. The run goes on after the request, so it keeps the context of the
// process, which the endpoint's context is not.
type Starter struct {
	runner *schedules.Runner
	ctx    context.Context
}

// NewStarter returns a Starter that starts runs with the context of the process.
func NewStarter(ctx context.Context, r *schedules.Runner) Starter {
	return Starter{runner: r, ctx: ctx}
}

// Start starts a run; schedules.ErrAlreadyRunning says one is going on.
func (s Starter) Start(scheduleID uint64) error { return s.runner.Execute(s.ctx, scheduleID) }

// SystemClock is the real time, for the runner.
type SystemClock struct{}

// Now is the time now.
func (SystemClock) Now() time.Time { return time.Now() }

// Wait blocks for d, or until the context ends, and says whether the whole time passed.
func (SystemClock) Wait(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
