package healthapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apitest"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
)

var errBoom = errors.New("the database is down: password=hunter2 host=db.internal")

func serve(t *testing.T, d Deps) (*apitest.Client, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	d.Logger = slog.New(slog.NewTextHandler(logs, nil))
	h, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	return apitest.NewWorld().Serve(t, func(g api.Group) { h.Register(g) }), logs
}

func ok(context.Context) error      { return nil }
func failing(context.Context) error { return errBoom }

func TestLive(t *testing.T) {
	client, _ := serve(t, Deps{Checks: map[string]Check{"database": failing}})
	// The process is up whatever the database does, and nobody has to sign in to ask.
	rp := client.As(0, "GET", "/healthz", "")
	if rp.Status != 200 || rp.At("status") != "ok" || rp.Header.Get("Cache-Control") != "no-store" || rp.Has("checks") {
		t.Errorf("answer = %d %s", rp.Status, rp.Raw)
	}
}

func TestReady(t *testing.T) {
	client, _ := serve(t, Deps{Checks: map[string]Check{"database": ok, "outbox": ok}})
	rp := client.As(0, "GET", "/readyz", "")
	if rp.Status != 200 || rp.At("status") != "ok" || rp.At("checks", "database") != "ok" || rp.At("checks", "outbox") != "ok" || rp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("answer = %d %s", rp.Status, rp.Raw)
	}
	// With nothing to look at it is ready.
	client, _ = serve(t, Deps{})
	if rp := client.As(0, "GET", "/readyz", ""); rp.Status != 200 || rp.At("status") != "ok" {
		t.Errorf("no checks: %d %s", rp.Status, rp.Raw)
	}
}

func TestNotReadyAndWhatItGivesAway(t *testing.T) {
	client, logs := serve(t, Deps{Checks: map[string]Check{"database": failing, "outbox": ok}})
	rp := client.As(0, "GET", "/readyz", "")
	if rp.Status != 503 || rp.At("status") != "unavailable" || rp.At("checks", "database") != "failed" || rp.At("checks", "outbox") != "ok" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	// Whoever asks learns that something is wrong and not what: the reason is for the log.
	if rp.Contains("hunter2") || rp.Contains("db.internal") || !strings.Contains(logs.String(), "a readiness check failed") {
		t.Errorf("answer %s, log %s", rp.Raw, logs.String())
	}
}

func TestAChecksThatIsStuckOrPanicsHasFailed(t *testing.T) {
	stuck := make(chan struct{})
	defer close(stuck)
	client, logs := serve(t, Deps{
		Timeout: 100 * time.Millisecond,
		Checks: map[string]Check{
			"stuck":  func(context.Context) error { <-stuck; return nil },
			"panics": func(context.Context) error { panic("password=hunter2") },
			"fine":   ok,
		},
	})
	start := time.Now()
	rp := client.As(0, "GET", "/readyz", "")
	if rp.Status != 503 || rp.At("checks", "stuck") != "failed" || rp.At("checks", "panics") != "failed" || rp.At("checks", "fine") != "ok" || time.Since(start) > 3*time.Second {
		t.Errorf("answer = %d %s after %v", rp.Status, rp.Raw, time.Since(start))
	}
	if !strings.Contains(logs.String(), "panicked") {
		t.Errorf("log = %s", logs.String())
	}
}

func TestTheChecksRunAtTheSameTime(t *testing.T) {
	var running, peak atomic.Int32
	slow := func(context.Context) error {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
		running.Add(-1)
		return nil
	}
	client, _ := serve(t, Deps{Checks: map[string]Check{"a": slow, "b": slow, "c": slow}})
	start := time.Now()
	if rp := client.As(0, "GET", "/readyz", ""); rp.Status != 200 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if peak.Load() != 3 || time.Since(start) > 250*time.Millisecond {
		t.Errorf("peak %d, took %v", peak.Load(), time.Since(start))
	}
}

func TestTheLimitIsPerClientAddress(t *testing.T) {
	l, err := middleware.NewLimiter(2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	client, _ := serve(t, Deps{Limiter: l})
	for i := 0; i < 2; i++ {
		if rp := client.As(0, "GET", "/healthz", ""); rp.Status != 200 {
			t.Fatalf("request %d: %d", i+1, rp.Status)
		}
	}
	if rp := client.As(0, "GET", "/readyz", ""); rp.Status != 429 || rp.Header.Get("Retry-After") == "" {
		t.Errorf("over the limit: %d %v", rp.Status, rp.Header)
	}
}

func TestNewChecksItsInput(t *testing.T) {
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	if _, err := New(Deps{}); err == nil {
		t.Error("no logger")
	}
	if _, err := New(Deps{Logger: log, Checks: map[string]Check{"": ok}}); err == nil {
		t.Error("a check without a name")
	}
	if _, err := New(Deps{Logger: log, Checks: map[string]Check{"a": nil}}); err == nil {
		t.Error("a check that does nothing")
	}
	if h, err := New(Deps{Logger: log}); err != nil || h.d.Timeout != DefaultTimeout {
		t.Errorf("default timeout: %v", err)
	}
}
