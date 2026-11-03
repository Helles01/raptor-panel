package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/api/appnodes"
	"github.com/Helles01/raptor-panel/internal/api/controlapi"
	"github.com/Helles01/raptor-panel/internal/config"
	"github.com/Helles01/raptor-panel/internal/mail"
	"github.com/Helles01/raptor-panel/internal/outbox"
	"github.com/Helles01/raptor-panel/internal/wings"
)

type fakeTargets struct {
	target controlapi.Target
	err    error
	asked  uint64
}

func (f *fakeTargets) For(_ context.Context, id uint64) (controlapi.Target, error) {
	f.asked = id
	return f.target, f.err
}

// fakeDaemon records what a node was asked to do; it is the appservers.Daemon the adapters use.
type fakeDaemon struct {
	calls []string
	node  wings.Node
	err   error
}

func (f *fakeDaemon) Create(context.Context, wings.Node, string, bool) error { return nil }
func (f *fakeDaemon) Sync(context.Context, wings.Node, string) error         { return nil }
func (f *fakeDaemon) Reinstall(_ context.Context, n wings.Node, uuid string) error {
	f.calls, f.node = append(f.calls, "reinstall "+uuid), n
	return f.err
}
func (f *fakeDaemon) Delete(_ context.Context, n wings.Node, uuid string) error {
	f.calls, f.node = append(f.calls, "delete "+uuid), n
	return f.err
}

func TestReinstallerCallsTheNodeOfTheServer(t *testing.T) {
	node := wings.Node{Scheme: "https", FQDN: "node1.example.com", Port: 8080, Token: "secret"}
	targets, daemon := &fakeTargets{target: controlapi.Target{UUID: "u-1", Node: node}}, &fakeDaemon{}
	r := reinstaller{targets: targets, daemon: daemon}

	if err := r.Reinstall(context.Background(), 7, "u-1"); err != nil {
		t.Fatal(err)
	}
	if targets.asked != 7 || len(daemon.calls) != 1 || daemon.calls[0] != "reinstall u-1" || daemon.node != node {
		t.Errorf("asked %d, calls %v, node %+v", targets.asked, daemon.calls, daemon.node)
	}

	// A server whose node is not known is not asked about; what the node says is passed on.
	errGone := errors.New("no such server")
	targets.err = errGone
	if err := r.Reinstall(context.Background(), 8, "u-2"); !errors.Is(err, errGone) || len(daemon.calls) != 1 {
		t.Errorf("unknown target: %v, calls %v", err, daemon.calls)
	}
	targets.err, daemon.err = nil, errors.New("the node refused")
	if err := r.Reinstall(context.Background(), 9, "u-3"); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("the node's answer: %v", err)
	}
}

// connections is the part of appnodes.Store the adapter uses.
type connections struct {
	appnodes.Store
	node wings.Node
	err  error
	id   uint64
}

func (c *connections) Connection(_ context.Context, id uint64) (wings.Node, error) {
	c.id = id
	return c.node, c.err
}

func TestOldNodesDeletesOnTheNodeAsItIsStoredNow(t *testing.T) {
	node := wings.Node{Scheme: "https", FQDN: "old.example.com", Port: 8080, Token: "t"}
	nodes, daemon := &connections{node: node}, &fakeDaemon{}
	o := oldNodes{nodes: nodes, daemon: daemon}

	if err := o.DeleteServer(context.Background(), 3, "u-1"); err != nil {
		t.Fatal(err)
	}
	if nodes.id != 3 || len(daemon.calls) != 1 || daemon.calls[0] != "delete u-1" || daemon.node != node {
		t.Errorf("node %d, calls %v, %+v", nodes.id, daemon.calls, daemon.node)
	}
	errGone := errors.New("not found")
	nodes.err = errGone
	if err := o.DeleteServer(context.Background(), 4, "u-2"); !errors.Is(err, errGone) || len(daemon.calls) != 1 {
		t.Errorf("a node that is gone: %v, calls %v", err, daemon.calls)
	}
}

type fakeSettings struct {
	values   map[string]string
	password string
	err      error
}

func (f fakeSettings) Mail(context.Context) (map[string]string, string, error) {
	return f.values, f.password, f.err
}

func TestSettingsSender(t *testing.T) {
	msg := mail.Message{To: "a@example.com", Subject: "s", Text: "t"}

	// No mail server in the settings: nothing is sent and trying again is no use.
	err := settingsSender{settings: fakeSettings{values: map[string]string{}}}.Send(context.Background(), msg)
	if !errors.Is(err, mail.ErrNotConfigured) || !outbox.IsPermanent(err) {
		t.Errorf("no mail server: %v", err)
	}

	// The settings cannot be read: that is for another try.
	errDB := errors.New("the database is away")
	err = settingsSender{settings: fakeSettings{err: errDB}}.Send(context.Background(), msg)
	if !errors.Is(err, errDB) || outbox.IsPermanent(err) {
		t.Errorf("unreadable settings: %v", err)
	}

	// Settings that are wrong are for another try as well: somebody may put them right.
	err = settingsSender{settings: fakeSettings{values: map[string]string{"mail:mailers:smtp:host": "smtp.example.com", "mail:mailers:smtp:port": "not a port"}}}.Send(context.Background(), msg)
	if err == nil || errors.Is(err, mail.ErrNotConfigured) || outbox.IsPermanent(err) {
		t.Errorf("a port that is not one: %v", err)
	}
}

func TestMailSettingKeys(t *testing.T) {
	keys := mail.SettingKeys()
	if len(keys) != 5 {
		t.Fatalf("keys = %v", keys)
	}
	seen := map[string]bool{}
	for _, k := range keys {
		if !strings.HasPrefix(k, "mail:") || seen[k] {
			t.Errorf("key %q", k)
		}
		seen[k] = true
	}
	if seen["mail:mailers:smtp:password"] {
		t.Error("the password is a secret, not one of the settings that are read in the clear")
	}
}

func TestCSRFKeyIsMadeFromTheSecretAndIsNotIt(t *testing.T) {
	a, b := make([]byte, 32), make([]byte, 32)
	b[0] = 1
	ka, ka2, kb := csrfKey(a), csrfKey(a), csrfKey(b)
	if len(ka) != 32 || string(ka) != string(ka2) || string(ka) == string(kb) || string(ka) == string(a) {
		t.Errorf("keys: %x %x %x", ka, ka2, kb)
	}
}

func TestPanelMissingLeavesOutWhatTheCommandsAndTheWiringProvide(t *testing.T) {
	missing := Stores{}.PanelMissing()
	for _, name := range missing {
		switch name {
		case "AdminHostStore", "AdminStore", "SecretmigrationStore", "RemoteapiOldNodes":
			t.Errorf("%s is not needed from the SQL layer", name)
		}
	}
	// AdminDirectoryStore is used by the wiring for the settings it reads, so it is listed as needed.
	if len(missing) != len(Stores{}.Missing())-4 || !slices.Contains(missing, "AdminDirectoryStore") {
		t.Errorf("%d missing, %d in all", len(missing), len(Stores{}.Missing()))
	}
}

func TestBuildRefusesWhatCannotWork(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	if _, err := Build(ctx, Input{}); err == nil || !strings.Contains(err.Error(), "logger") {
		t.Errorf("no logger: %v", err)
	}
	_, err := Build(ctx, Input{Logger: logger, Config: config.Config{}})
	if err == nil || !strings.Contains(err.Error(), "missing stores") || !strings.Contains(err.Error(), "AuthzUsers") {
		t.Errorf("no stores: %v", err)
	}
}
