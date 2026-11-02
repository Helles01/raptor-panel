package maintenance

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

var errBoom = errors.New("the database is down: password=hunter2")

type fakeStore struct {
	n   int64
	err error

	orphans         [2]time.Time
	activityBefore  time.Time
	resetsBefore    time.Time
	sessionsIdle    time.Time
	sessionsCreated time.Time
}

func (s *fakeStore) FailOrphans(_ context.Context, createdBefore, at time.Time) (int64, error) {
	s.orphans = [2]time.Time{createdBefore, at}
	return s.n, s.err
}

func (s *fakeStore) DeleteActivityBefore(_ context.Context, before time.Time) (int64, error) {
	s.activityBefore = before
	return s.n, s.err
}

func (s *fakeStore) DeleteResetTokensBefore(_ context.Context, before time.Time) (int64, error) {
	s.resetsBefore = before
	return s.n, s.err
}

func (s *fakeStore) DeleteExpiredSessions(_ context.Context, idleBefore, createdBefore time.Time) (int64, error) {
	s.sessionsIdle, s.sessionsCreated = idleBefore, createdBefore
	return s.n, s.err
}

func TestTheJobsAskForTheRightCutoffs(t *testing.T) {
	s := &fakeStore{n: 3}
	tests := []struct {
		name   string
		job    Job
		every  time.Duration
		report string
		check  func() bool
	}{
		{"backups", PruneOrphanedBackups(s, 6*time.Hour), 30 * time.Minute, "3 backups that never finished were marked as failed",
			func() bool { return s.orphans == [2]time.Time{now.Add(-6 * time.Hour), now} }},
		{"activity", PruneActivity(s, DefaultActivityKeep), 24 * time.Hour, "3 old activity log entries were deleted",
			func() bool { return s.activityBefore.Equal(now.Add(-90 * 24 * time.Hour)) }},
		{"reset tokens", PruneResetTokens(s), time.Hour, "3 expired password reset tokens were deleted",
			func() bool { return s.resetsBefore.Equal(now.Add(-time.Hour)) }},
		{"sessions", PruneSessions(s, 12*time.Hour, 24*time.Hour), time.Hour, "3 expired sessions were deleted",
			func() bool {
				return s.sessionsIdle.Equal(now.Add(-12*time.Hour)) && s.sessionsCreated.Equal(now.Add(-24*time.Hour))
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report, err := tt.job.Run(context.Background(), now)
			if err != nil || report != tt.report || !tt.check() || tt.job.Every != tt.every || tt.job.Name == "" {
				t.Errorf("report %q, err %v, every %v", report, err, tt.job.Every)
			}
			// Nothing to do is nothing to say.
			s.n = 0
			if report, _ := tt.job.Run(context.Background(), now); report != "" {
				t.Errorf("report %q", report)
			}
			s.n = 3
			s.err = errBoom
			if _, err := tt.job.Run(context.Background(), now); !errors.Is(err, errBoom) {
				t.Errorf("error = %v", err)
			}
			s.err = nil
		})
	}
	if DefaultBackupPruneAge != 360*time.Minute || ResetTokenLifetime != time.Hour {
		t.Error("the defaults changed")
	}
}

// clock is a clock that stands still until the test lets it go on.
type clock struct {
	mu      sync.Mutex
	at      time.Time
	waits   chan time.Duration
	release chan struct{}
}

func newClock() *clock {
	return &clock{at: now, waits: make(chan time.Duration, 100), release: make(chan struct{})}
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) Wait(ctx context.Context, d time.Duration) bool {
	select {
	case c.waits <- d:
	case <-ctx.Done():
		return false
	}
	select {
	case <-c.release:
		c.mu.Lock()
		c.at = c.at.Add(d)
		c.mu.Unlock()
		return true
	case <-ctx.Done():
		return false
	}
}

func logger() (*slog.Logger, *bytes.Buffer) {
	b := &bytes.Buffer{}
	return slog.New(slog.NewTextHandler(b, nil)), b
}

func TestSchedulerRunsEveryJobAtOnceAndThenOnItsTime(t *testing.T) {
	c := newClock()
	log, logs := logger()
	var mu sync.Mutex
	ran := map[string][]time.Time{}
	job := func(name string, every time.Duration) Job {
		return Job{Name: name, Every: every, Run: func(_ context.Context, at time.Time) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			ran[name] = append(ran[name], at)
			return "did " + name, nil
		}}
	}
	s, err := NewScheduler([]Job{job("fast", time.Minute), job("slow", time.Hour)}, c, log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { s.Run(ctx); close(finished) }()

	waits := map[time.Duration]bool{}
	for i := 0; i < 2; i++ {
		select {
		case d := <-c.waits:
			waits[d] = true
		case <-time.After(5 * time.Second):
			t.Fatal("a job did not wait for its time")
		}
	}
	if !waits[time.Minute] || !waits[time.Hour] {
		t.Errorf("waits = %v", waits)
	}
	mu.Lock()
	if len(ran["fast"]) != 1 || len(ran["slow"]) != 1 || !ran["fast"][0].Equal(now) {
		t.Errorf("first runs = %v", ran)
	}
	mu.Unlock()

	// Both are let go, one after the other: each runs again, the clock having moved on by what it waited.
	c.release <- struct{}{}
	c.release <- struct{}{}
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		done := len(ran["fast"]) >= 2 && len(ran["slow"]) >= 2
		mu.Unlock()
		if done {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the jobs did not run again")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	if !strings.Contains(logs.String(), "did fast") {
		t.Errorf("log = %s", logs.String())
	}
}

func TestAJobThatFailsOrPanicsStopsNothing(t *testing.T) {
	c := newClock()
	log, logs := logger()
	var mu sync.Mutex
	good := 0
	s, err := NewScheduler([]Job{
		{Name: "fails", Every: time.Minute, Run: func(context.Context, time.Time) (string, error) { return "", errBoom }},
		{Name: "panics", Every: time.Minute, Run: func(context.Context, time.Time) (string, error) { panic("password=hunter2") }},
		{Name: "good", Every: time.Minute, Run: func(context.Context, time.Time) (string, error) { mu.Lock(); good++; mu.Unlock(); return "", nil }},
	}, c, log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { s.Run(ctx); close(finished) }()
	for i := 0; i < 3; i++ {
		select {
		case <-c.waits:
		case <-time.After(5 * time.Second):
			t.Fatal("a job did not get to wait")
		}
	}
	cancel()
	<-finished
	mu.Lock()
	defer mu.Unlock()
	out := logs.String()
	if good != 1 || !strings.Contains(out, "a maintenance job failed") || !strings.Contains(out, "a maintenance job panicked") {
		t.Errorf("good %d, log %s", good, out)
	}
}

func TestAJobThatEndsWithTheContextIsNotAFailure(t *testing.T) {
	c := newClock()
	log, logs := logger()
	ctx, cancel := context.WithCancel(context.Background())
	s, _ := NewScheduler([]Job{{Name: "slow", Every: time.Minute, Run: func(ctx context.Context, _ time.Time) (string, error) {
		cancel()
		return "", ctx.Err()
	}}}, c, log)
	s.Run(ctx)
	if strings.Contains(logs.String(), "failed") {
		t.Errorf("log = %s", logs.String())
	}
}

func TestNewSchedulerChecksTheJobs(t *testing.T) {
	log, _ := logger()
	run := func(context.Context, time.Time) (string, error) { return "", nil }
	for name, tc := range map[string]struct {
		jobs  []Job
		clock bool
		log   bool
	}{
		"no clock":       {nil, false, true},
		"no logger":      {nil, true, false},
		"no name":        {[]Job{{Every: time.Minute, Run: run}}, true, true},
		"nothing to run": {[]Job{{Name: "a", Every: time.Minute}}, true, true},
		"no interval":    {[]Job{{Name: "a", Run: run}}, true, true},
		"a negative one": {[]Job{{Name: "a", Every: -time.Minute, Run: run}}, true, true},
		"the same name":  {[]Job{{Name: "a", Every: time.Minute, Run: run}, {Name: "a", Every: time.Hour, Run: run}}, true, true},
	} {
		var cl *clock
		if tc.clock {
			cl = newClock()
		}
		var lg *slog.Logger
		if tc.log {
			lg = log
		}
		var scheduler *Scheduler
		var err error
		if cl == nil {
			scheduler, err = NewScheduler(tc.jobs, nil, lg)
		} else {
			scheduler, err = NewScheduler(tc.jobs, cl, lg)
		}
		if err == nil {
			t.Errorf("%s: no error (%v)", name, scheduler)
		}
	}
	// Nothing to schedule is fine, and Run returns at once.
	s, err := NewScheduler(nil, newClock(), log)
	if err != nil {
		t.Fatal(err)
	}
	s.Run(context.Background())
}
