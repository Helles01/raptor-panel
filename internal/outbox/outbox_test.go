package outbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/mail"
	"github.com/Helles01/raptor-panel/internal/wings"
)

var t0 = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

var errBoom = errors.New("the server is down: password=hunter2")

type entry struct {
	job      Job
	notAfter time.Time // not before
	leased   time.Time // taken until
	done     bool
	failed   bool
	failure  string
}

type store struct {
	mu      sync.Mutex
	entries []*entry
	err     map[string]error // by method
	claims  int
}

func newStore(jobs ...Job) *store {
	s := &store{err: map[string]error{}}
	for _, j := range jobs {
		s.entries = append(s.entries, &entry{job: j})
	}
	return s
}

func (s *store) find(id uint64) *entry {
	for _, e := range s.entries {
		if e.job.ID == id {
			return e
		}
	}
	return nil
}

func (s *store) Claim(_ context.Context, now time.Time, limit int, lease time.Duration) ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims++
	if err := s.err["Claim"]; err != nil {
		return nil, err
	}
	var out []Job
	for _, e := range s.entries {
		if len(out) == limit {
			break
		}
		if e.done || e.failed || e.notAfter.After(now) || e.leased.After(now) {
			continue
		}
		e.leased = now.Add(lease)
		out = append(out, e.job)
	}
	return out, nil
}

func (s *store) Done(_ context.Context, id uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.err["Done"]; err != nil {
		return err
	}
	s.find(id).done = true
	return nil
}

func (s *store) Retry(_ context.Context, id uint64, attempts int, at time.Time, failure string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.err["Retry"]; err != nil {
		return err
	}
	e := s.find(id)
	e.job.Attempts, e.notAfter, e.leased, e.failure = attempts, at, time.Time{}, failure
	return nil
}

func (s *store) Fail(_ context.Context, id uint64, attempts int, failure string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.err["Fail"]; err != nil {
		return err
	}
	e := s.find(id)
	e.job.Attempts, e.failed, e.failure = attempts, true, failure
	return nil
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func runner(t *testing.T, s Store, h map[string]Handler, c *clock, o Options) (*Runner, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	o.Now = c.Now
	r, err := NewRunner(s, h, slog.New(slog.NewTextHandler(logs, nil)), o)
	if err != nil {
		t.Fatal(err)
	}
	return r, logs
}

func TestTickDoesDueJobsInOrder(t *testing.T) {
	s := newStore(Job{ID: 1, Kind: "a", Payload: []byte(`1`)}, Job{ID: 2, Kind: "b", Payload: []byte(`2`)}, Job{ID: 3, Kind: "a", Payload: []byte(`3`)})
	var seen []uint64
	h := func(_ context.Context, j Job) error { seen = append(seen, j.ID); return nil }
	r, _ := runner(t, s, map[string]Handler{"a": h, "b": h}, &clock{t0}, Options{})
	n, err := r.Tick(context.Background())
	if err != nil || n != 3 || !reflect.DeepEqual(seen, []uint64{1, 2, 3}) {
		t.Fatalf("took %d, err %v, saw %v", n, err, seen)
	}
	for _, e := range s.entries {
		if !e.done {
			t.Errorf("job %d is not done", e.job.ID)
		}
	}
	// A job that is done is not done twice.
	if n, _ := r.Tick(context.Background()); n != 0 || len(seen) != 3 {
		t.Errorf("second tick took %d, saw %v", n, seen)
	}
}

func TestABatchIsLimited(t *testing.T) {
	s := newStore(Job{ID: 1, Kind: "a"}, Job{ID: 2, Kind: "a"}, Job{ID: 3, Kind: "a"})
	r, _ := runner(t, s, map[string]Handler{"a": func(context.Context, Job) error { return nil }}, &clock{t0}, Options{Batch: 2})
	if n, _ := r.Tick(context.Background()); n != 2 || s.entries[2].done {
		t.Errorf("first tick took %d, third done %v", n, s.entries[2].done)
	}
	if n, _ := r.Tick(context.Background()); n != 1 || !s.entries[2].done {
		t.Errorf("second tick took %d", n)
	}
}

func TestAFailedJobIsTriedAgainLater(t *testing.T) {
	c := &clock{t0}
	s := newStore(Job{ID: 1, Kind: "a"})
	tries := 0
	r, logs := runner(t, s, map[string]Handler{"a": func(context.Context, Job) error {
		tries++
		if tries < 3 {
			return errBoom
		}
		return nil
	}}, c, Options{})

	r.Tick(context.Background())
	e := s.entries[0]
	if e.job.Attempts != 1 || !e.notAfter.Equal(t0.Add(30*time.Second)) || !strings.Contains(e.failure, "the server is down") {
		t.Fatalf("after the first failure: %+v", e)
	}
	// Not due yet: it is not taken.
	c.now = t0.Add(29 * time.Second)
	if n, _ := r.Tick(context.Background()); n != 0 || tries != 1 {
		t.Errorf("before it is due: took %d, tries %d", n, tries)
	}
	c.now = t0.Add(30 * time.Second)
	r.Tick(context.Background())
	if e.job.Attempts != 2 || !e.notAfter.Equal(c.now.Add(time.Minute)) {
		t.Errorf("after the second failure: %+v", e)
	}
	c.now = e.notAfter
	r.Tick(context.Background())
	if !e.done || tries != 3 {
		t.Errorf("after the third try: done %v, tries %d", e.done, tries)
	}
	if !strings.Contains(logs.String(), "tried again later") {
		t.Errorf("log = %s", logs.String())
	}
}

func TestAJobThatCannotSucceedIsSetAside(t *testing.T) {
	s := newStore(Job{ID: 1, Kind: "a"}, Job{ID: 2, Kind: "nobody"}, Job{ID: 3, Kind: "a", Attempts: 9})
	r, logs := runner(t, s, map[string]Handler{"a": func(_ context.Context, j Job) error {
		if j.ID == 1 {
			return Permanent(errBoom)
		}
		return errBoom
	}}, &clock{t0}, Options{})
	r.Tick(context.Background())

	one, two, three := s.entries[0], s.entries[1], s.entries[2]
	if !one.failed || one.job.Attempts != 1 || one.notAfter != (time.Time{}) {
		t.Errorf("permanent: %+v", one)
	}
	if !two.failed || two.failure != "no handler for the kind" {
		t.Errorf("no handler: %+v", two)
	}
	// The tenth failure is the last.
	if !three.failed || three.job.Attempts != 10 {
		t.Errorf("tenth try: %+v", three)
	}
	if !strings.Contains(logs.String(), "set aside") {
		t.Errorf("log = %s", logs.String())
	}
	// Nothing set aside comes back.
	if n, _ := r.Tick(context.Background()); n != 0 {
		t.Errorf("took %d", n)
	}
}

func TestAHandlerThatPanicsDoesNotStopTheOthers(t *testing.T) {
	s := newStore(Job{ID: 1, Kind: "a"}, Job{ID: 2, Kind: "b"})
	r, _ := runner(t, s, map[string]Handler{
		"a": func(context.Context, Job) error { panic("password=hunter2") },
		"b": func(context.Context, Job) error { return nil },
	}, &clock{t0}, Options{})
	if _, err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.entries[0].job.Attempts != 1 || s.entries[0].done || !s.entries[1].done || !strings.Contains(s.entries[0].failure, "panicked") {
		t.Errorf("entries = %+v %+v", s.entries[0], s.entries[1])
	}
}

func TestAJobHasTimeToDoItsWork(t *testing.T) {
	s := newStore(Job{ID: 1, Kind: "a"})
	var deadline time.Time
	var ok bool
	r, _ := runner(t, s, map[string]Handler{"a": func(ctx context.Context, _ Job) error { deadline, ok = ctx.Deadline(); return nil }}, &clock{t0}, Options{JobTimeout: 7 * time.Second})
	before := time.Now()
	r.Tick(context.Background())
	if !ok || deadline.Before(before.Add(6*time.Second)) || deadline.After(time.Now().Add(8*time.Second)) {
		t.Errorf("deadline %v (set %v)", deadline, ok)
	}
}

func TestFailuresOfTheStore(t *testing.T) {
	s := newStore(Job{ID: 1, Kind: "a"})
	s.err["Claim"] = errBoom
	r, _ := runner(t, s, map[string]Handler{"a": func(context.Context, Job) error { return nil }}, &clock{t0}, Options{})
	if _, err := r.Tick(context.Background()); err == nil || !strings.Contains(err.Error(), "claim jobs") {
		t.Errorf("claim: %v", err)
	}
	for _, method := range []string{"Done", "Retry", "Fail"} {
		s := newStore(Job{ID: 1, Kind: "a"})
		s.err[method] = errBoom
		h := func(context.Context, Job) error { return nil }
		switch method {
		case "Retry":
			h = func(context.Context, Job) error { return errBoom }
		case "Fail":
			h = func(context.Context, Job) error { return Permanent(errBoom) }
		}
		r, _ := runner(t, s, map[string]Handler{"a": h}, &clock{t0}, Options{})
		if _, err := r.Tick(context.Background()); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", method, err)
		}
	}
}

func TestAStoppedRunnerLeavesTheRestToTheLease(t *testing.T) {
	s := newStore(Job{ID: 1, Kind: "a"}, Job{ID: 2, Kind: "a"})
	ctx, cancel := context.WithCancel(context.Background())
	r, _ := runner(t, s, map[string]Handler{"a": func(context.Context, Job) error { cancel(); return nil }}, &clock{t0}, Options{})
	n, err := r.Tick(ctx)
	if !errors.Is(err, context.Canceled) || n != 2 || !s.entries[0].done || s.entries[1].done || s.entries[1].failed {
		t.Errorf("took %d, err %v, entries %+v %+v", n, err, s.entries[0], s.entries[1])
	}
	// The second job was taken: another process gets it only when the lease has passed.
	c := &clock{t0.Add(time.Minute)}
	r2, _ := runner(t, s, map[string]Handler{"a": func(context.Context, Job) error { return nil }}, c, Options{})
	if n, _ := r2.Tick(context.Background()); n != 0 {
		t.Errorf("during the lease: took %d", n)
	}
	c.now = t0.Add(6 * time.Minute)
	if n, _ := r2.Tick(context.Background()); n != 1 || !s.entries[1].done {
		t.Errorf("after the lease: took %d", n)
	}
}

func TestNewRunnerChecksItsInput(t *testing.T) {
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	if _, err := NewRunner(nil, nil, log, Options{}); err == nil {
		t.Error("no store")
	}
	if _, err := NewRunner(newStore(), nil, nil, Options{}); err == nil {
		t.Error("no logger")
	}
	if _, err := NewRunner(newStore(), nil, log, Options{JobTimeout: time.Minute, Lease: time.Minute}); err == nil {
		t.Error("a lease that is not longer than a job")
	}
	r, err := NewRunner(newStore(), map[string]Handler{"a": nil}, log, Options{})
	if err != nil || r.o.Batch != 20 || r.o.MaxAttempts != 10 || r.o.Lease != 5*time.Minute || r.o.JobTimeout != 30*time.Second || r.o.Now == nil {
		t.Errorf("defaults = %+v, %v", r, err)
	}
}

func TestDefaultBackoff(t *testing.T) {
	for attempts, want := range map[int]time.Duration{0: 30 * time.Second, 1: 30 * time.Second, 2: time.Minute, 3: 2 * time.Minute, 4: 4 * time.Minute, 7: 32 * time.Minute, 8: time.Hour, 9: time.Hour, 50: time.Hour} {
		if got := defaultBackoff(attempts); got != want {
			t.Errorf("after %d tries: %v, want %v", attempts, got, want)
		}
	}
}

func TestRunWorksUntilStopped(t *testing.T) {
	s := newStore(Job{ID: 1, Kind: "a"})
	done := make(chan struct{})
	r, _ := runner(t, s, map[string]Handler{"a": func(context.Context, Job) error { close(done); return nil }}, &clock{t0}, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- r.Run(ctx, 5*time.Millisecond) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the job was not done")
	}
	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Errorf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
	if err := r.Run(context.Background(), 0); err == nil {
		t.Error("an interval of nothing")
	}
}

func TestRunKeepsGoingWhenTheStoreFails(t *testing.T) {
	s := newStore()
	s.err["Claim"] = errBoom
	r, logs := runner(t, s, nil, &clock{t0}, Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	r.Run(ctx, 10*time.Millisecond)
	s.mu.Lock()
	claims := s.claims
	s.mu.Unlock()
	if claims < 2 || !strings.Contains(logs.String(), "could not be worked through") {
		t.Errorf("claims %d, log %s", claims, logs.String())
	}
}

// ---- the jobs ----

type queue struct{ jobs []New }

func (q *queue) Enqueue(_ context.Context, j New) error { q.jobs = append(q.jobs, j); return nil }

func TestMailIsQueuedAsJSON(t *testing.T) {
	q := &queue{}
	m := mail.Message{To: "ann@example.com", Subject: "Hello", Text: "text", HTML: "<p>html</p>"}
	if err := (Mailer{q}).Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if len(q.jobs) != 1 || q.jobs[0].Kind != KindMail {
		t.Fatalf("jobs = %+v", q.jobs)
	}
	var back mail.Message
	if err := json.Unmarshal(q.jobs[0].Payload, &back); err != nil || back != m {
		t.Errorf("payload = %s (%v)", q.jobs[0].Payload, err)
	}
}

type sender struct {
	sent []mail.Message
	err  error
}

func (s *sender) Send(_ context.Context, m mail.Message) error {
	s.sent = append(s.sent, m)
	return s.err
}

type refused struct{ permanent bool }

func (r refused) Error() string   { return "550 no such user" }
func (r refused) Permanent() bool { return r.permanent }

func TestMailHandler(t *testing.T) {
	m := mail.Message{To: "ann@example.com", Subject: "Hello", Text: "t", HTML: "h"}
	job, _ := MailJob(m)
	s := &sender{}
	h := MailHandler(s)
	if err := h(context.Background(), Job{Kind: job.Kind, Payload: job.Payload}); err != nil || len(s.sent) != 1 || s.sent[0] != m {
		t.Fatalf("sent %+v, err %v", s.sent, err)
	}

	for name, tc := range map[string]struct {
		err       error
		payload   []byte
		permanent bool
		failed    bool
	}{
		"a server that says no for good":    {refused{true}, job.Payload, true, true},
		"a server that says try later":      {refused{false}, job.Payload, false, true},
		"a server that cannot be reached":   {errBoom, job.Payload, false, true},
		"a payload that is not a message":   {nil, []byte(`[1,2]`), true, true},
		"a payload that is not JSON at all": {nil, []byte(`x`), true, true},
	} {
		s := &sender{err: tc.err}
		err := MailHandler(s)(context.Background(), Job{Payload: tc.payload})
		if (err != nil) != tc.failed || IsPermanent(err) != tc.permanent {
			t.Errorf("%s: err %v, permanent %v", name, err, IsPermanent(err))
		}
	}
}

type nodes struct {
	node wings.Node
	err  error
}

func (n nodes) Node(context.Context, uint64) (wings.Node, error) { return n.node, n.err }

type deauth struct {
	calls []Deauthorization
	err   error
}

func (d *deauth) Deauthorize(_ context.Context, n wings.Node, user string, servers ...string) error {
	d.calls = append(d.calls, Deauthorization{NodeID: uint64(n.Port), UserUUID: user, ServerUUIDs: servers})
	return d.err
}

func TestDeauthorizeHandler(t *testing.T) {
	job, err := DeauthorizeJob(Deauthorization{NodeID: 4, UserUUID: "u-1", ServerUUIDs: []string{"s-1", "s-2"}})
	if err != nil || job.Kind != KindDeauthorize {
		t.Fatal(job, err)
	}
	d := &deauth{}
	node := wings.Node{Port: 4}
	if err := DeauthorizeHandler(nodes{node: node}, d)(context.Background(), Job{Payload: job.Payload}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d.calls, []Deauthorization{{NodeID: 4, UserUUID: "u-1", ServerUUIDs: []string{"s-1", "s-2"}}}) {
		t.Errorf("calls = %+v", d.calls)
	}

	// Without servers the node is told for all of them.
	all, _ := DeauthorizeJob(Deauthorization{NodeID: 4, UserUUID: "u-1"})
	d = &deauth{}
	DeauthorizeHandler(nodes{node: node}, d)(context.Background(), Job{Payload: all.Payload})
	if len(d.calls) != 1 || len(d.calls[0].ServerUUIDs) != 0 || strings.Contains(string(all.Payload), "server_uuids") {
		t.Errorf("calls = %+v, payload %s", d.calls, all.Payload)
	}

	// A node that is gone needs no telling; a lookup that fails is tried again; a bad payload is set aside.
	d = &deauth{}
	if err := DeauthorizeHandler(nodes{err: ErrNoNode}, d)(context.Background(), Job{Payload: job.Payload}); err != nil || len(d.calls) != 0 {
		t.Errorf("gone: %v %+v", err, d.calls)
	}
	if err := DeauthorizeHandler(nodes{err: errBoom}, d)(context.Background(), Job{Payload: job.Payload}); err == nil || IsPermanent(err) {
		t.Errorf("lookup fails: %v", err)
	}
	if err := DeauthorizeHandler(nodes{}, d)(context.Background(), Job{Payload: []byte(`x`)}); !IsPermanent(err) {
		t.Errorf("bad payload: %v", err)
	}
}

func TestDeauthorizeHandlerSortsTheFailures(t *testing.T) {
	job, _ := DeauthorizeJob(Deauthorization{NodeID: 4, UserUUID: "u-1"})
	for name, tc := range map[string]struct {
		err       error
		permanent bool
	}{
		"the node says forbidden":       {&wings.Error{Status: 403, NodeStatus: 403, Message: "no"}, true},
		"the node says not found":       {&wings.Error{Status: 404, NodeStatus: 404, Message: "no"}, true},
		"the node says bad request":     {&wings.Error{Status: 400, NodeStatus: http.StatusBadRequest, Message: "no"}, true},
		"the node says try later":       {&wings.Error{Status: 429, NodeStatus: 429, Message: "slow down"}, false},
		"the node says request timeout": {&wings.Error{Status: 408, NodeStatus: 408, Message: "slow"}, false},
		"the node fails":                {&wings.Error{Status: 500, NodeStatus: 500, Message: "oops"}, false},
		"the node cannot be reached":    {&wings.Error{Status: 504, Message: "no reply", Unreachable: true}, false},
		"some other error":              {errBoom, false},
	} {
		err := DeauthorizeHandler(nodes{}, &deauth{err: tc.err})(context.Background(), Job{Payload: job.Payload})
		if err == nil || IsPermanent(err) != tc.permanent {
			t.Errorf("%s: err %v, permanent %v", name, err, IsPermanent(err))
		}
	}
}

func TestPermanent(t *testing.T) {
	if Permanent(nil) != nil {
		t.Error("Permanent(nil) is an error")
	}
	e := Permanent(errBoom)
	if !IsPermanent(e) || !errors.Is(e, errBoom) || e.Error() != errBoom.Error() || IsPermanent(errBoom) || IsPermanent(nil) {
		t.Errorf("Permanent = %v", e)
	}
	if !IsPermanent(errors.Join(errors.New("x"), e)) {
		t.Error("a wrapped permanent error is not seen")
	}
}

func TestTheRealDeauthorizerCallsTheNode(t *testing.T) {
	var method, path, auth, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		method, path, auth, body = r.Method, r.URL.Path, r.Header.Get("Authorization"), string(raw)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	p, _ := strconv.Atoi(port)
	n := wings.Node{Scheme: "http", FQDN: host, Port: p, Token: strings.Repeat("w", 64)}
	factory, err := wings.NewFactory(wings.ClientOptions{Timeout: 5 * time.Second, ConnectTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	d := NewDeauthorizer(factory)
	user, server := "aaaa1111-2222-4333-8444-555555555555", "bbbb2222-2222-4333-8444-555555555555"
	if err := d.Deauthorize(context.Background(), n, user, server); err != nil {
		t.Fatal(err)
	}
	if method != "POST" || path != "/api/deauthorize-user" || auth != "Bearer "+strings.Repeat("w", 64) || body != `{"servers":["`+server+`"],"user":"`+user+`"}` {
		t.Errorf("the node saw %s %s %q %q", method, path, auth, body)
	}
	bad := n
	bad.Token = "short"
	if err := d.Deauthorize(context.Background(), bad, user); err == nil {
		t.Error("no error for a node with a short token")
	}
}

// The sender of mail and the handler of mail agree on what a sender is and on what a permanent failure is.
var (
	_ Sender         = (*mail.Sender)(nil)
	_ PermanentError = (*mail.SendError)(nil)
)
