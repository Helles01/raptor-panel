package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var errBoom = errors.New("the queue is gone: password=hunter2")

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// waitsForStop is a loop that does nothing until it is told to stop.
func waitsForStop(name string, stopped *atomic.Int32) Loop {
	return Loop{Name: name, Run: func(ctx context.Context) error {
		<-ctx.Done()
		stopped.Add(1)
		return nil
	}}
}

// finish runs Run in the background and returns what it answers, failing the test if it does not answer.
func finish(t *testing.T, s Supervisor, ctx context.Context, loops ...Loop) <-chan error {
	t.Helper()
	out := make(chan error, 1)
	go func() { out <- s.Run(ctx, loops...) }()
	return out
}

func answer(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
		return nil
	}
}

func TestCleanStop(t *testing.T) {
	var stopped atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	ch := finish(t, Supervisor{Logger: quiet()}, ctx, waitsForStop("a", &stopped), waitsForStop("b", &stopped), waitsForStop("c", &stopped))
	select {
	case err := <-ch:
		t.Fatalf("Run returned before it was told to: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	if err := answer(t, ch); err != nil {
		t.Errorf("err = %v", err)
	}
	if stopped.Load() != 3 {
		t.Errorf("%d loops had stopped when Run returned", stopped.Load())
	}
}

// A loop that answers "cancelled" to being cancelled has done what it was asked.
func TestCancelledIsNotAFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	l := Loop{Name: "a", Run: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}
	ch := finish(t, Supervisor{Logger: quiet()}, ctx, l)
	cancel()
	if err := answer(t, ch); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestOneFailureStopsTheOthers(t *testing.T) {
	var stopped atomic.Int32
	failing := Loop{Name: "outbox", Run: func(context.Context) error { return errBoom }}
	var logs bytes.Buffer
	s := Supervisor{Logger: slog.New(slog.NewTextHandler(&logs, nil))}
	err := answer(t, finish(t, s, context.Background(), waitsForStop("a", &stopped), failing, waitsForStop("b", &stopped)))
	if !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "outbox") {
		t.Errorf("err = %v", err)
	}
	if stopped.Load() != 2 {
		t.Errorf("%d of the other loops had stopped", stopped.Load())
	}
	if !strings.Contains(logs.String(), "loop=outbox") {
		t.Errorf("logs = %s", logs.String())
	}
}

func TestALoopThatReturnsByItselfHasFailed(t *testing.T) {
	var stopped atomic.Int32
	quits := Loop{Name: "scheduler", Run: func(context.Context) error { return nil }}
	err := answer(t, finish(t, Supervisor{Logger: quiet()}, context.Background(), waitsForStop("a", &stopped), quits))
	if err == nil || !strings.Contains(err.Error(), "scheduler: stopped by itself") || stopped.Load() != 1 {
		t.Errorf("err = %v, stopped %d", err, stopped.Load())
	}
}

func TestAPanicIsAFailure(t *testing.T) {
	var stopped atomic.Int32
	var logs bytes.Buffer
	boom := Loop{Name: "mail", Run: func(context.Context) error { panic("index out of range") }}
	err := answer(t, finish(t, Supervisor{Logger: slog.New(slog.NewTextHandler(&logs, nil))}, context.Background(), waitsForStop("a", &stopped), boom))
	if err == nil || !strings.Contains(err.Error(), "mail: panicked: index out of range") || stopped.Load() != 1 {
		t.Errorf("err = %v, stopped %d", err, stopped.Load())
	}
	if strings.Contains(err.Error(), "goroutine") {
		t.Errorf("the stack is in the error: %v", err)
	}
	if !strings.Contains(logs.String(), "stack") {
		t.Errorf("the stack is not in the log: %s", logs.String())
	}
}

func TestErrorAfterTheRequestToStopIsStillAFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	l := Loop{Name: "a", Run: func(ctx context.Context) error { <-ctx.Done(); return errBoom }}
	ch := finish(t, Supervisor{Logger: quiet()}, ctx, l)
	cancel()
	if err := answer(t, ch); !errors.Is(err, errBoom) {
		t.Errorf("err = %v", err)
	}
}

func TestSeveralFailuresAreAllReported(t *testing.T) {
	gate := make(chan struct{})
	mk := func(name string, err error) Loop {
		return Loop{Name: name, Run: func(ctx context.Context) error { <-gate; return err }}
	}
	other := errors.New("other")
	ch := finish(t, Supervisor{Logger: quiet()}, context.Background(), mk("a", errBoom), mk("b", other))
	close(gate)
	err := answer(t, ch)
	if !errors.Is(err, errBoom) || !errors.Is(err, other) {
		t.Errorf("err = %v", err)
	}
}

func TestALoopThatWillNotStopIsGivenUpOn(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var stopped atomic.Int32
	stuck := Loop{Name: "stuck", Run: func(context.Context) error { <-release; return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	ch := finish(t, Supervisor{Logger: quiet(), StopTimeout: 40 * time.Millisecond}, ctx, stuck, waitsForStop("fine", &stopped))
	cancel()
	err := answer(t, ch)
	if !errors.Is(err, ErrStopTimeout) || !strings.Contains(err.Error(), "stuck") || strings.Contains(err.Error(), "fine") {
		t.Errorf("err = %v", err)
	}
	if time.Since(start) > 5*time.Second || stopped.Load() != 1 {
		t.Errorf("after %v, %d stopped", time.Since(start), stopped.Load())
	}
}

func TestTheTimeLimitStartsWhenTheRequestComes(t *testing.T) {
	// A loop that runs for a long time before it is asked to stop is not "late".
	ctx, cancel := context.WithCancel(context.Background())
	var stopped atomic.Int32
	ch := finish(t, Supervisor{Logger: quiet(), StopTimeout: 80 * time.Millisecond}, ctx, waitsForStop("a", &stopped))
	time.Sleep(200 * time.Millisecond)
	cancel()
	if err := answer(t, ch); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestLoopsAreChecked(t *testing.T) {
	ok := Loop{Name: "a", Run: func(ctx context.Context) error { <-ctx.Done(); return nil }}
	for name, loops := range map[string][]Loop{
		"none":      nil,
		"no name":   {{Run: ok.Run}},
		"no run":    {{Name: "a"}},
		"duplicate": {ok, ok},
	} {
		if err := (Supervisor{Logger: quiet()}).Run(context.Background(), loops...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := (Supervisor{}).Run(context.Background(), ok); err == nil {
		t.Error("a supervisor without a logger ran")
	}
}

func TestAdapters(t *testing.T) {
	var gotInterval time.Duration
	tick := Ticking("outbox", 5*time.Second, func(ctx context.Context, d time.Duration) error {
		gotInterval = d
		<-ctx.Done()
		return nil
	})
	forever := Forever("maintenance", func(ctx context.Context) { <-ctx.Done() })
	var order []string
	setup := WithSetup(Forever("schedules", func(ctx context.Context) { order = append(order, "run"); <-ctx.Done() }),
		func(context.Context) error { order = append(order, "setup"); return nil })

	ctx, cancel := context.WithCancel(context.Background())
	ch := finish(t, Supervisor{Logger: quiet()}, ctx, tick, forever, setup)
	time.Sleep(30 * time.Millisecond)
	cancel()
	if err := answer(t, ch); err != nil {
		t.Fatal(err)
	}
	if gotInterval != 5*time.Second || strings.Join(order, ",") != "setup,run" {
		t.Errorf("interval %v, order %v", gotInterval, order)
	}
	if tick.Name != "outbox" || forever.Name != "maintenance" || setup.Name != "schedules" {
		t.Error("an adapter changed the name")
	}
}

func TestSetupThatFailsKeepsTheLoopFromRunning(t *testing.T) {
	ran := false
	l := WithSetup(Forever("schedules", func(ctx context.Context) { ran = true; <-ctx.Done() }),
		func(context.Context) error { return errBoom })
	err := answer(t, finish(t, Supervisor{Logger: quiet()}, context.Background(), l))
	if !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "schedules: setup") || ran {
		t.Errorf("err = %v, ran %v", err, ran)
	}
}

// A loop that is cancelled by something other than the panel has not been asked to stop.
func TestCancelledByItselfIsAFailure(t *testing.T) {
	var stopped atomic.Int32
	l := Loop{Name: "a", Run: func(context.Context) error { return context.Canceled }}
	err := answer(t, finish(t, Supervisor{Logger: quiet()}, context.Background(), l, waitsForStop("b", &stopped)))
	if !errors.Is(err, context.Canceled) || stopped.Load() != 1 {
		t.Errorf("err = %v, stopped %d", err, stopped.Load())
	}
}

func TestTheDefaultTimeLimitIsGenerous(t *testing.T) {
	slow := Loop{Name: "slow", Run: func(ctx context.Context) error {
		<-ctx.Done()
		time.Sleep(60 * time.Millisecond)
		return nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	ch := finish(t, Supervisor{Logger: quiet()}, ctx, slow)
	cancel()
	if err := answer(t, ch); err != nil {
		t.Errorf("a loop that took 60 ms to stop was given up on: %v", err)
	}
	if DefaultStopTimeout < 10*time.Second {
		t.Errorf("DefaultStopTimeout = %v", DefaultStopTimeout)
	}
}
