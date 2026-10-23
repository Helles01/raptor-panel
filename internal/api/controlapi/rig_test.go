package controlapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apitest"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/servers"
	"github.com/Helles01/raptor-panel/internal/wings"
)

// The users of the world. Ada owns the servers, Bob and Cleo are subusers of the first one, Eve is a stranger.
const (
	ada   = 1
	grace = 2 // an administrator
	bob   = 3
	cleo  = 4
	eve   = 5
)

const (
	normalUUID     = "aaaa1111-2222-4333-8444-555555555555"
	installingUUID = "bbbb2222-2222-4333-8444-555555555555"
	movingUUID     = "cccc3333-2222-4333-8444-555555555555"
	otherUUID      = "dddd4444-2222-4333-8444-555555555555"
	panelURL       = "https://panel.example.com"
)

var (
	node1 = wings.Node{Scheme: "https", FQDN: "node1.example.com", Port: 8080, Token: strings.Repeat("a", 64)}
	node2 = wings.Node{Scheme: "https", FQDN: "node2.example.com", Port: 8443, Token: strings.Repeat("b", 64)}
)

// fakeTargets knows the node of every server.
type fakeTargets struct {
	mu      sync.Mutex
	targets map[uint64]Target
	err     error
	calls   int
}

func (f *fakeTargets) For(_ context.Context, id uint64) (Target, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return Target{}, f.err
	}
	t, ok := f.targets[id]
	if !ok {
		return Target{}, ErrNotFound
	}
	return t, nil
}

type daemonCall struct {
	Kind    string // "power", "command" or "details"
	Node    wings.Node
	UUID    string
	Payload string
}

// fakeDaemon records what is sent to a node and answers as it is told.
type fakeDaemon struct {
	mu      sync.Mutex
	calls   []daemonCall
	err     error
	details wings.Details
}

func (f *fakeDaemon) record(kind string, n wings.Node, uuid, payload string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, daemonCall{kind, n, uuid, payload})
	return f.err
}

func (f *fakeDaemon) Power(_ context.Context, n wings.Node, uuid, action string) error {
	return f.record("power", n, uuid, action)
}

func (f *fakeDaemon) Command(_ context.Context, n wings.Node, uuid, command string) error {
	return f.record("command", n, uuid, command)
}

func (f *fakeDaemon) Details(_ context.Context, n wings.Node, uuid string) (wings.Details, error) {
	err := f.record("details", n, uuid, "")
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.details, err
}

func (f *fakeDaemon) count(kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Kind == kind {
			n++
		}
	}
	return n
}

func (f *fakeDaemon) all() []daemonCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]daemonCall(nil), f.calls...)
}

// fakeActivity records the entries.
type fakeActivity struct {
	mu      sync.Mutex
	entries []Entry
	err     error
}

func (f *fakeActivity) Log(_ context.Context, e Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.entries = append(f.entries, e)
	return nil
}

func (f *fakeActivity) all() []Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Entry(nil), f.entries...)
}

// failingTokens is a token maker that refuses.
type failingTokens struct{ err error }

func (f failingTokens) Websocket(wings.Node, wings.Subject, string) (string, error) { return "", f.err }

type options struct {
	daemon Daemon
	tokens Tokens
	// clock is moved by the tests to expire the cache.
	clock *time.Time
}

type rig struct {
	t        *testing.T
	client   *apitest.Client
	world    *apitest.World
	targets  *fakeTargets
	daemon   *fakeDaemon
	activity *fakeActivity
	logs     *bytes.Buffer
	clock    time.Time
	handlers *Handlers
}

func newRig(t *testing.T, opts ...func(*options)) *rig {
	t.Helper()
	r := &rig{t: t, clock: apitest.Now, logs: &bytes.Buffer{}}
	var o options
	for _, f := range opts {
		f(&o)
	}

	w := apitest.NewWorld()
	for id, admin := range map[uint64]bool{ada: false, grace: true, bob: false, cleo: false, eve: false} {
		w.AddUser(id, admin)
	}
	w.AddServer(100, normalUUID, ada)
	w.AddServer(101, installingUUID, ada).State = servers.State{Status: servers.StatusInstalling}
	w.AddServer(102, movingUUID, ada).State = servers.State{Transferring: true}
	w.AddServer(103, otherUUID, ada)
	w.Subusers[[2]uint64{100, bob}] = []string{"control.start", "websocket.connect"}
	w.Subusers[[2]uint64{100, cleo}] = []string{"control.console", "control.restart", "control.stop"}
	r.world = w

	r.targets = &fakeTargets{targets: map[uint64]Target{
		100: {UUID: normalUUID, Node: node1},
		101: {UUID: installingUUID, Node: node1},
		102: {UUID: movingUUID, Node: node1, Transfer: &Transfer{NewNode: node2}},
		103: {UUID: otherUUID, Node: node1},
	}}
	r.daemon = &fakeDaemon{details: wings.Details{State: "running"}}
	r.activity = &fakeActivity{}

	var daemon Daemon = r.daemon
	if o.daemon != nil {
		daemon = o.daemon
	}
	var tokens Tokens
	if o.tokens != nil {
		tokens = o.tokens
	} else {
		issuer, err := wings.NewIssuer(panelURL)
		if err != nil {
			t.Fatal(err)
		}
		tokens = issuer
	}
	limiter, err := middleware.NewLimiter(WebsocketLimit, WebsocketWindow)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Deps{
		Targets: r.targets, Daemon: daemon, Tokens: tokens, Activity: r.activity, WebsocketLimiter: limiter,
		Logger: slog.New(slog.NewTextHandler(r.logs, nil)), Now: func() time.Time { return r.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	r.handlers = h
	r.client = w.Serve(t, func(g api.Group) { h.Register(g) })
	return r
}

func (r *rig) post(user uint64, target, body string) apitest.Reply {
	r.t.Helper()
	return r.client.As(user, "POST", target, body)
}

func (r *rig) get(user uint64, target string) apitest.Reply {
	r.t.Helper()
	return r.client.As(user, "GET", target, "")
}

var errBoom = errors.New("the database is down: password=hunter2")

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// jwt is a decoded token and whether its signature is right for the key.
type jwt struct {
	Header map[string]any
	Claims map[string]any
	SigOK  bool
}

// decodeJWT reads a token without trusting it and checks the HS256 signature with the key.
func decodeJWT(t *testing.T, token, key string) jwt {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q has %d parts", token, len(parts))
	}
	var out jwt
	for i, dst := range []*map[string]any{&out.Header, &out.Claims} {
		raw, err := base64.RawURLEncoding.DecodeString(parts[i])
		if err != nil {
			t.Fatalf("part %d: %v", i, err)
		}
		if err := json.Unmarshal(raw, dst); err != nil {
			t.Fatalf("part %d: %v", i, err)
		}
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	out.SigOK = base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) == parts[2]
	return out
}
