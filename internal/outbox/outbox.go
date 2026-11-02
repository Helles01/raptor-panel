// Package outbox runs the jobs the panel owes the world after a change it has made: a mail to send, a node to tell that a user lost their
// access. A job is stored in the same transaction as the change (the store of the change does that), so that a crash between the two
// cannot lose it; this package takes the jobs out one by one, does them, and tries again later when one fails. A job may therefore run
// more than once, and its handler must do no harm the second time.
package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Job is a job as the store holds it.
type Job struct {
	ID   uint64
	Kind string
	// Payload is JSON, as the helper for the kind made it.
	Payload []byte
	// Attempts is how many times the job was tried before this one.
	Attempts int
}

// Store holds the jobs. All methods must be safe to call from several processes at once.
type Store interface {
	// Claim returns up to limit jobs that are due at now and not taken, oldest first, and keeps them from being returned again before
	// the lease has passed, so that a process that dies with a job does not lose it and two processes do not do it together.
	Claim(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]Job, error)
	// Done removes a job that was done.
	Done(ctx context.Context, id uint64) error
	// Retry puts a job back to be tried at the time, after a failure with the text given (which holds no secret).
	Retry(ctx context.Context, id uint64, attempts int, at time.Time, failure string) error
	// Fail sets a job aside, for good, after a failure with the text given. A person looks at what is set aside.
	Fail(ctx context.Context, id uint64, attempts int, failure string) error
}

// Handler does a job. An error makes the runner try again later, unless it was made with Permanent.
type Handler func(ctx context.Context, job Job) error

type permanent struct{ err error }

func (p permanent) Error() string { return p.err.Error() }
func (p permanent) Unwrap() error { return p.err }

// Permanent marks a failure that trying again cannot cure, such as a mail to an address the server says does not exist.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanent{err}
}

// IsPermanent reports whether the error was made with Permanent.
func IsPermanent(err error) bool {
	var p permanent
	return errors.As(err, &p)
}

// Options are the settings of a Runner. Zero values are the defaults.
type Options struct {
	// Batch is how many jobs are taken at once, 20 by default.
	Batch int
	// Lease is how long a taken job is kept from the others, 5 minutes by default; it must be longer than JobTimeout.
	Lease time.Duration
	// JobTimeout is how long one job may take, 30 seconds by default.
	JobTimeout time.Duration
	// MaxAttempts is how many times a job is tried before it is set aside, 10 by default.
	MaxAttempts int
	// Backoff is the wait before the next try, after the given number of tries. By default 30 seconds doubled for every try, at most an hour.
	Backoff func(attempts int) time.Duration
	// Now defaults to time.Now.
	Now func() time.Time
}

// Runner takes jobs from a store and gives each to the handler of its kind.
type Runner struct {
	store    Store
	handlers map[string]Handler
	log      *slog.Logger
	o        Options
}

// NewRunner returns a runner. A kind that has no handler is set aside when it is met, not retried: no later try could help.
func NewRunner(store Store, handlers map[string]Handler, log *slog.Logger, o Options) (*Runner, error) {
	if store == nil || log == nil {
		return nil, errors.New("outbox: a store and a logger are required")
	}
	if o.Batch <= 0 {
		o.Batch = 20
	}
	if o.JobTimeout <= 0 {
		o.JobTimeout = 30 * time.Second
	}
	if o.Lease <= 0 {
		o.Lease = 5 * time.Minute
	}
	if o.Lease <= o.JobTimeout {
		return nil, errors.New("outbox: the lease must be longer than the time a job may take")
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 10
	}
	if o.Backoff == nil {
		o.Backoff = defaultBackoff
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	copied := make(map[string]Handler, len(handlers))
	for k, h := range handlers {
		copied[k] = h
	}
	return &Runner{store: store, handlers: copied, log: log, o: o}, nil
}

func defaultBackoff(attempts int) time.Duration {
	d := 30 * time.Second
	for i := 1; i < attempts && d < time.Hour; i++ {
		d *= 2
	}
	return min(d, time.Hour)
}

// Tick takes the jobs that are due and does them, one after the other. It returns how many it took. A failure of the store ends it.
func (r *Runner) Tick(ctx context.Context) (int, error) {
	jobs, err := r.store.Claim(ctx, r.o.Now(), r.o.Batch, r.o.Lease)
	if err != nil {
		return 0, fmt.Errorf("claim jobs: %w", err)
	}
	for _, job := range jobs {
		if ctx.Err() != nil {
			// The job is left to the lease: it is due again when the lease has passed.
			return len(jobs), ctx.Err()
		}
		if err := r.run(ctx, job); err != nil {
			return len(jobs), err
		}
	}
	return len(jobs), nil
}

// run does one job and records the outcome. Only a failure to record it is returned.
func (r *Runner) run(ctx context.Context, job Job) error {
	h, ok := r.handlers[job.Kind]
	if !ok {
		r.log.Error("a job of a kind nobody handles was set aside", "job", job.ID, "kind", job.Kind)
		return r.store.Fail(ctx, job.ID, job.Attempts, "no handler for the kind")
	}

	jobCtx, cancel := context.WithTimeout(ctx, r.o.JobTimeout)
	defer cancel()
	err := call(jobCtx, h, job)
	if err == nil {
		return r.store.Done(ctx, job.ID)
	}

	attempts := job.Attempts + 1
	failure := err.Error()
	if IsPermanent(err) || attempts >= r.o.MaxAttempts {
		r.log.Error("a job was set aside", "job", job.ID, "kind", job.Kind, "attempts", attempts, "permanent", IsPermanent(err), "error", err)
		return r.store.Fail(ctx, job.ID, attempts, failure)
	}
	at := r.o.Now().Add(r.o.Backoff(attempts))
	r.log.Warn("a job failed and is tried again later", "job", job.ID, "kind", job.Kind, "attempts", attempts, "again_at", at, "error", err)
	return r.store.Retry(ctx, job.ID, attempts, at, failure)
}

// call runs a handler and turns a panic into an error, so that one bad job cannot stop the others.
func call(ctx context.Context, h Handler, job Job) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("the handler panicked: %v", p)
		}
	}()
	return h(ctx, job)
}

// Run does Tick every interval until the context ends. A store that fails is logged and tried again at the next tick.
func (r *Runner) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return errors.New("outbox: the interval must be positive")
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := r.Tick(ctx); err != nil && ctx.Err() == nil {
			r.log.Error("the outbox could not be worked through", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}
