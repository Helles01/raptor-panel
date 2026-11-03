package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func runAsync(t *testing.T, o Options) (addr string, stop func() error) {
	t.Helper()
	ready := make(chan net.Addr, 1)
	o.Ready = func(a net.Addr) { ready <- a }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, o) }()
	select {
	case a := <-ready:
		addr = a.String()
	case err := <-done:
		cancel()
		t.Fatalf("Run returned before it was listening: %v", err)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("Run did not start listening")
	}
	return addr, func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not stop")
			return nil
		}
	}
}

func TestRunServesAndRunsTheLoopsAndStopsThemAll(t *testing.T) {
	var started, stopped atomic.Int32
	loop := Loop{Name: "outbox", Run: func(ctx context.Context) error {
		started.Add(1)
		<-ctx.Done()
		stopped.Add(1)
		return nil
	}}
	addr, stop := runAsync(t, Options{Handler: hello(), Logger: quiet(), Listen: "127.0.0.1:0", Loops: []Loop{loop}})
	if code, body := get(t, "http://"+addr+"/page"); code != 200 || body != "hello /page" {
		t.Errorf("%d %q", code, body)
	}
	if err := stop(); err != nil {
		t.Errorf("err = %v", err)
	}
	if started.Load() != 1 || stopped.Load() != 1 {
		t.Errorf("the loop started %d times and stopped %d", started.Load(), stopped.Load())
	}
}

func TestAFailingLoopStopsTheWebServer(t *testing.T) {
	failing := Loop{Name: "scheduler", Run: func(context.Context) error { return errBoom }}
	ready := make(chan net.Addr, 1)
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), Options{Handler: hello(), Logger: quiet(), Listen: "127.0.0.1:0", Loops: []Loop{failing}, Ready: func(a net.Addr) { ready <- a }})
	}()
	addr := (<-ready).String()
	select {
	case err := <-done:
		if !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "scheduler") {
			t.Errorf("err = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	if conn, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		conn.Close()
		t.Error("the web server was left running")
	}
}

func TestAPortThatIsTakenStopsThePanelBeforeAnyLoopRuns(t *testing.T) {
	ln := listen(t)
	var ran atomic.Bool
	loop := Loop{Name: "outbox", Run: func(ctx context.Context) error { ran.Store(true); <-ctx.Done(); return nil }}
	err := Run(context.Background(), Options{Handler: hello(), Logger: quiet(), Listen: ln.Addr().String(), Loops: []Loop{loop}})
	if err == nil || ran.Load() {
		t.Errorf("err = %v, the loop ran: %v", err, ran.Load())
	}
}

func TestRunNeedsItsParts(t *testing.T) {
	for name, o := range map[string]Options{
		"handler": {Logger: quiet(), Listen: "127.0.0.1:0"},
		"logger":  {Handler: hello(), Listen: "127.0.0.1:0"},
		"listen":  {Handler: hello(), Logger: quiet(), Listen: "nonsense"},
	} {
		if err := Run(context.Background(), o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTheLimitsOfTheWebServerAndTheSupervisorAreUsed(t *testing.T) {
	// A request that is still running when the panel is told to stop, and a loop that will not stop: both limits come from the options.
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	slow := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { close(entered); <-release })
	addr, stop := runAsync(t, Options{
		Handler: slow, Logger: quiet(), Listen: "127.0.0.1:0",
		Web: Web{ShutdownTimeout: 30 * time.Millisecond},
	})
	go func() { _, _ = (&http.Client{Timeout: 5 * time.Second}).Get("http://" + addr) }()
	<-entered
	began := time.Now()
	err := stop()
	if err == nil || !strings.Contains(err.Error(), "cut off") || time.Since(began) > 5*time.Second {
		t.Errorf("err = %v after %v", err, time.Since(began))
	}
}

func TestSignalsEndWhenTheParentDoes(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	ctx, release := Signals(parent)
	defer release()
	select {
	case <-ctx.Done():
		t.Fatal("the context ended by itself")
	default:
	}
	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Error("the context did not end with its parent")
	}
}

func TestTheTimeToStopComesFromTheOptions(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	stuck := Loop{Name: "stuck", Run: func(context.Context) error { <-release; return nil }}
	_, stop := runAsync(t, Options{
		Handler: hello(), Logger: quiet(), Listen: "127.0.0.1:0", Loops: []Loop{stuck},
		Supervisor: Supervisor{StopTimeout: 30 * time.Millisecond},
	})
	began := time.Now()
	err := stop()
	if !errors.Is(err, ErrStopTimeout) || !strings.Contains(err.Error(), "stuck") || time.Since(began) > 5*time.Second {
		t.Errorf("err = %v after %v", err, time.Since(began))
	}
}
