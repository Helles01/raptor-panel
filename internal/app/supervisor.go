// Package app puts the panel together: the loops that run beside the web server, and the server's own life from start to stop. The
// endpoint packages stay unaware of it; they are given what they need and never start anything on their own.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
	"strings"
	"time"
)

// DefaultStopTimeout is how long the loops get to stop once the panel is told to.
const DefaultStopTimeout = 30 * time.Second

// Loop is something that runs for as long as the panel does: the outbox, the scheduler, the housekeeping. Run returns when the context
// ends. A loop that returns before that has failed, whether or not it says why: the panel without its outbox is not the panel.
type Loop struct {
	Name string
	Run  func(ctx context.Context) error
}

// Supervisor runs loops together.
type Supervisor struct {
	// Logger receives what the loops do wrong; it is required.
	Logger *slog.Logger
	// StopTimeout bounds how long the loops may take to stop; DefaultStopTimeout when 0.
	StopTimeout time.Duration
}

// ErrStopTimeout is returned (inside the error) when a loop did not stop in time.
var ErrStopTimeout = errors.New("a loop did not stop in time")

// Run starts the loops and returns when the context has ended and every loop has stopped, with nil if all of them stopped as asked. If a
// loop stops by itself, fails or panics, the others are told to stop, and the error returned says which loop did and why. A loop that is
// still running StopTimeout after being told to stop is given up on: Run returns without it, and the error says so.
func (s Supervisor) Run(ctx context.Context, loops ...Loop) error {
	if s.Logger == nil {
		return errors.New("app: a supervisor needs a logger")
	}
	if err := check(loops); err != nil {
		return err
	}
	stopTimeout := s.StopTimeout
	if stopTimeout <= 0 {
		stopTimeout = DefaultStopTimeout
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type result struct {
		name string
		err  error
	}
	results := make(chan result, len(loops))
	for _, l := range loops {
		go func() {
			err := s.runOne(runCtx, l)
			// Stopping when asked is the loop doing its job; anything else, before or after the request, is a failure.
			switch {
			case err == nil && runCtx.Err() == nil:
				err = errors.New("stopped by itself")
			case errors.Is(err, context.Canceled) && runCtx.Err() != nil:
				err = nil
			}
			results <- result{l.Name, err}
		}()
	}

	var failures []error
	running := map[string]bool{}
	for _, l := range loops {
		running[l.Name] = true
	}
	stopping := runCtx.Done()
	var deadline <-chan time.Time
	for len(running) > 0 {
		select {
		case r := <-results:
			delete(running, r.name)
			if r.err != nil {
				s.Logger.Error("a background loop failed", "loop", r.name, "error", r.err)
				failures = append(failures, fmt.Errorf("%s: %w", r.name, r.err))
				cancel()
			}
		case <-stopping:
			stopping = nil // seen once; from now on only the deadline and the results matter
			t := time.NewTimer(stopTimeout)
			defer t.Stop()
			deadline = t.C
		case <-deadline:
			names := make([]string, 0, len(running))
			for n := range running {
				names = append(names, n)
			}
			sort.Strings(names)
			s.Logger.Error("background loops did not stop in time", "loops", names)
			failures = append(failures, fmt.Errorf("%w: %s", ErrStopTimeout, strings.Join(names, ", ")))
			return errors.Join(failures...)
		}
	}
	return errors.Join(failures...)
}

func check(loops []Loop) error {
	if len(loops) == 0 {
		return errors.New("app: there is nothing to run")
	}
	seen := map[string]bool{}
	for _, l := range loops {
		switch {
		case l.Name == "" || l.Run == nil:
			return errors.New("app: a loop needs a name and something to run")
		case seen[l.Name]:
			return fmt.Errorf("app: two loops are called %s", l.Name)
		}
		seen[l.Name] = true
	}
	return nil
}

func (s Supervisor) runOne(ctx context.Context, l Loop) (err error) {
	defer func() {
		if p := recover(); p != nil {
			// The stack is for the log of the one who runs the panel; the error that is returned carries only the value.
			err = fmt.Errorf("panicked: %v", p)
			s.Logger.Error("a background loop panicked", "loop", l.Name, "stack", string(debug.Stack()))
		}
	}()
	return l.Run(ctx)
}

// Ticking is a loop made of a function that is given the interval, such as outbox.Runner.Run.
func Ticking(name string, interval time.Duration, run func(ctx context.Context, interval time.Duration) error) Loop {
	return Loop{Name: name, Run: func(ctx context.Context) error { return run(ctx, interval) }}
}

// Forever is a loop made of a function that has nothing to say but that it stopped, such as maintenance.Scheduler.Run.
func Forever(name string, run func(ctx context.Context)) Loop {
	return Loop{Name: name, Run: func(ctx context.Context) error { run(ctx); return nil }}
}

// WithSetup runs setup once before the loop; if it fails the loop fails, without having run. It is for the work that must be done before
// the first round, such as the scheduler clearing the marks a crash left.
func WithSetup(l Loop, setup func(ctx context.Context) error) Loop {
	return Loop{Name: l.Name, Run: func(ctx context.Context) error {
		if err := setup(ctx); err != nil {
			return fmt.Errorf("setup: %w", err)
		}
		return l.Run(ctx)
	}}
}
