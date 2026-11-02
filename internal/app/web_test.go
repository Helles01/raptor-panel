package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Skip("no local listener:", err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}

type running struct {
	url    string
	cancel context.CancelFunc
	done   chan error
}

func start(t *testing.T, w Web, ln net.Listener) *running {
	t.Helper()
	if w.Logger == nil {
		w.Logger = quiet()
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{url: "http://" + ln.Addr().String(), cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- w.Serve(ctx, ln) }()
	t.Cleanup(cancel)
	return r
}

func (r *running) stop(t *testing.T) error {
	t.Helper()
	r.cancel()
	select {
	case err := <-r.done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return")
		return nil
	}
}

func hello() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "hello ", r.URL.Path) })
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestServesAndStopsCleanly(t *testing.T) {
	ln := listen(t)
	r := start(t, Web{Handler: hello()}, ln)
	if code, body := get(t, r.url+"/x"); code != 200 || body != "hello /x" {
		t.Errorf("%d %q", code, body)
	}
	if err := r.stop(t); err != nil {
		t.Errorf("err = %v", err)
	}
	if conn, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second); err == nil {
		conn.Close()
		t.Error("the port is still open")
	}
}

func TestRequestsInProgressFinishBeforeTheStop(t *testing.T) {
	ln := listen(t)
	entered, release := make(chan struct{}), make(chan struct{})
	slow := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		fmt.Fprint(w, "done")
	})
	r := start(t, Web{Handler: slow}, ln)

	answer := make(chan string, 1)
	go func() { _, body := get(t, r.url); answer <- body }()
	<-entered
	r.cancel()
	select {
	case err := <-r.done:
		t.Fatalf("Serve returned with a request in progress: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	// No new request is taken while it is stopping.
	if conn, err := net.DialTimeout("tcp", ln.Addr().String(), 200*time.Millisecond); err == nil {
		conn.SetDeadline(time.Now().Add(200 * time.Millisecond))
		if _, err := conn.Write([]byte("GET / HTTP/1.0\r\n\r\n")); err == nil {
			if n, _ := bufio.NewReader(conn).Read(make([]byte, 1)); n > 0 {
				t.Error("a new request was answered during the stop")
			}
		}
		conn.Close()
	}
	close(release)
	if body := <-answer; body != "done" {
		t.Errorf("body = %q", body)
	}
	if err := <-r.done; err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestRequestsThatWillNotFinishAreCutOff(t *testing.T) {
	ln := listen(t)
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	stuck := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-release
	})
	r := start(t, Web{Handler: stuck, ShutdownTimeout: 50 * time.Millisecond}, ln)
	failed := make(chan error, 1)
	go func() {
		_, err := (&http.Client{Timeout: 5 * time.Second}).Get(r.url)
		failed <- err
	}()
	<-entered
	began := time.Now()
	err := r.stop(t)
	if err == nil || !strings.Contains(err.Error(), "cut off") || time.Since(began) > 5*time.Second {
		t.Errorf("err = %v after %v", err, time.Since(began))
	}
	select {
	case err := <-failed:
		if err == nil {
			t.Error("the client got an answer from a request that was cut off")
		}
	case <-time.After(2 * time.Second):
		t.Error("the connection of the request was not closed")
	}
}

func TestAListenerThatBreaksIsAFailure(t *testing.T) {
	ln := listen(t)
	ln.Close()
	err := (Web{Handler: hello(), Logger: quiet()}).Serve(context.Background(), ln)
	if err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Errorf("err = %v", err)
	}
}

func TestServeNeedsItsParts(t *testing.T) {
	ln := listen(t)
	for name, w := range map[string]Web{"handler": {Logger: quiet()}, "logger": {Handler: hello()}} {
		if err := w.Serve(context.Background(), ln); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := (Web{Handler: hello(), Logger: quiet()}).Serve(context.Background(), nil); err == nil {
		t.Error("no listener accepted")
	}
}

func TestSlowClientsAreDropped(t *testing.T) {
	ln := listen(t)
	r := start(t, Web{Handler: hello(), ReadHeaderTimeout: 100 * time.Millisecond}, ln)
	defer r.stop(t)
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// The start of a request, and then nothing.
	fmt.Fprint(conn, "GET / HTTP/1.1\r\nHost: x\r\nX-Slow: ")
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	began := time.Now()
	_, err = io.ReadAll(conn)
	if time.Since(began) > 3*time.Second {
		t.Errorf("the connection was kept for %v (err %v)", time.Since(began), err)
	}
}

func TestHugeHeadersAreRefused(t *testing.T) {
	ln := listen(t)
	r := start(t, Web{Handler: hello()}, ln)
	defer r.stop(t)
	req, _ := http.NewRequest("GET", r.url, nil)
	// The standard library allows 4096 bytes more than the limit for the request line.
	req.Header.Set("Cookie", strings.Repeat("a", MaxHeaderBytes+8192))
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestTheServersOwnComplaintsGoToTheLog(t *testing.T) {
	ln := listen(t)
	var logs bytes.Buffer
	boom := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("handler went wrong") })
	r := start(t, Web{Handler: boom, Logger: slog.New(slog.NewTextHandler(&logs, nil))}, ln)
	// A panic that nothing caught is written by the server to its own error log, which is the log of the panel.
	if resp, err := http.Get(r.url); err == nil {
		resp.Body.Close()
	}
	r.stop(t)
	if !strings.Contains(logs.String(), "panic serving") || !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("logs = %s", logs.String())
	}
}

func TestDefaultsAndLimits(t *testing.T) {
	s := Web{Handler: hello(), Logger: quiet()}.server()
	if s.ReadHeaderTimeout != DefaultReadHeaderTimeout || s.ReadTimeout != DefaultReadTimeout || s.WriteTimeout != DefaultWriteTimeout ||
		s.IdleTimeout != DefaultIdleTimeout || s.MaxHeaderBytes != MaxHeaderBytes {
		t.Errorf("server = %+v", s)
	}
	s = Web{Handler: hello(), Logger: quiet(), ReadHeaderTimeout: time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 4 * time.Second}.server()
	if s.ReadHeaderTimeout != time.Second || s.ReadTimeout != 2*time.Second || s.WriteTimeout != 3*time.Second || s.IdleTimeout != 4*time.Second {
		t.Errorf("server = %+v", s)
	}
	if orDefault(-time.Second, time.Minute) != time.Minute || orDefault(0, time.Minute) != time.Minute || orDefault(time.Second, time.Minute) != time.Second {
		t.Error("orDefault")
	}
	if DefaultReadHeaderTimeout > 30*time.Second || DefaultReadTimeout > 5*time.Minute || DefaultWriteTimeout > 5*time.Minute {
		t.Error("the default limits are too loose for a panel")
	}
}

func TestListen(t *testing.T) {
	ln, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Skip("no local listener:", err)
	}
	defer ln.Close()
	if l, err := Listen(ln.Addr().String()); err == nil {
		l.Close()
		t.Error("a port that is taken was listened on twice")
	}
	for _, bad := range []string{"", "8080", "localhost", "127.0.0.1:99999", "127.0.0.1:abc"} {
		if l, err := Listen(bad); err == nil {
			l.Close()
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestLoop(t *testing.T) {
	ln := listen(t)
	l := Web{Handler: hello(), Logger: quiet()}.Loop(ln)
	if l.Name != "http" {
		t.Errorf("name = %q", l.Name)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch := finish(t, Supervisor{Logger: quiet()}, ctx, l)
	if code, _ := get(t, "http://"+ln.Addr().String()+"/"); code != 200 {
		t.Errorf("code = %d", code)
	}
	cancel()
	if err := answer(t, ch); err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}
