package remoteapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apitest"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/backups"
	"github.com/Helles01/raptor-panel/internal/mail"
	"github.com/Helles01/raptor-panel/internal/remote"
	"github.com/Helles01/raptor-panel/internal/servers"
	"github.com/Helles01/raptor-panel/internal/sshkeys"
)

// The nodes of the world.
const (
	node1 = 1
	node2 = 2
	node3 = 3
)

// The users of the world.
const (
	ada   = 1 // owns the servers
	root  = 2 // an administrator
	bob   = 3 // a subuser with file.sftp
	cleo  = 4 // a subuser without it
	eve   = 5 // a stranger
	adaPW = "a long enough password"
)

// The servers of the world, by the first part of their UUID.
const (
	normalUUID     = "aaaa1111-2222-4333-8444-555555555555" // node 1
	installingUUID = "bbbb2222-2222-4333-8444-555555555555" // node 1, installing for the first time
	reinstallUUID  = "cccc3333-2222-4333-8444-555555555555" // node 1, installing again
	restoringUUID  = "dddd4444-2222-4333-8444-555555555555" // node 1, restoring the backup "nightly"
	suspendedUUID  = "eeee5555-2222-4333-8444-555555555555" // node 1, suspended while installing
	otherNodeUUID  = "ffff6666-2222-4333-8444-555555555555" // node 2
	movingUUID     = "99997777-2222-4333-8444-555555555555" // moving from node 1 to node 2
)

var adaHash = func() string {
	h, err := auth.HashPassword(adaPW)
	if err != nil {
		panic(err)
	}
	return h
}()

// record is a server of the fake store, with everything the store gives out about it.
type record struct {
	id       uint64
	node     uint64
	owner    uint64
	status   string
	before   bool // installed before
	state    servers.State
	subusers map[uint64][]string
	transfer *remote.Transfer
	backup   *BackupRef
	config   servers.Server
}

type installCall struct {
	ServerID uint64
	From     string
	Outcome  servers.InstallOutcome
	At       time.Time
}

type completeCall struct {
	ServerID uint64
	Result   remote.TransferResult
}

// store is every store of the node API in memory, and a record of what was asked of it.
type store struct {
	mu sync.Mutex

	// leak makes the lookups find servers whatever the node, to show that the handlers hold the line themselves.
	leak bool

	users   map[string]SFTPUser // by lower case name
	prints  map[uint64][]string
	records []*record

	// What the handlers asked for or wrote.
	listCalls    [][3]int // node, offset, limit
	installs     []installCall
	resets       [][]ResetChange
	completes    []completeCall
	activity     []StoredEntry
	storeCalls   int
	events       []Event
	mails        []mail.Message
	oldNodeCalls []string

	// Answers to inject.
	installRace        bool // Installed answers false
	completeRace       bool
	failList           error
	failActivity       error
	failReset          error
	failComplete       error
	failMail           error
	failOldNode        error
	failEvents         error
	failFind           error
	backupList         []NodeBackup
	backupDone         []completedBackup
	restoring          map[uint64]bool
	restoreEnds        []uint64
	failBackups        error
	completeBackupRace bool
	// extraReset are candidates a careless store would add to what it found.
	extraReset []ResetCandidate
}

func (s *store) find(node uint64, uuid string) *record {
	for _, r := range s.records {
		if r.config.UUID == uuid && (s.leak || r.node == node) {
			return r
		}
	}
	return nil
}

func (s *store) ByUsername(_ context.Context, name string) (SFTPUser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u, ok := s.users[strings.ToLower(name)]; ok {
		return u, nil
	}
	return SFTPUser{}, ErrNotFound
}

func (s *store) Fingerprints(_ context.Context, id uint64) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prints[id], nil
}

func (s *store) SFTP(_ context.Context, node uint64, shortID string, user uint64) (SFTPServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failFind != nil {
		return SFTPServer{}, s.failFind
	}
	for _, r := range s.records {
		if strings.HasPrefix(r.config.UUID, shortID) && (s.leak || r.node == node) {
			return SFTPServer{ID: r.id, UUID: r.config.UUID, NodeID: r.node, OwnerID: r.owner, State: r.state, Subuser: r.subusers[user]}, nil
		}
	}
	return SFTPServer{}, ErrNotFound
}

func (s *store) List(_ context.Context, node uint64, offset, limit int) (ServerPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listCalls = append(s.listCalls, [3]int{int(node), offset, limit})
	if s.failList != nil {
		return ServerPage{}, s.failList
	}
	var all []servers.Server
	for _, r := range s.records {
		if r.node == node {
			all = append(all, r.config)
		}
	}
	from := min(offset, len(all))
	to := min(from+limit, len(all))
	return ServerPage{Total: len(all), Servers: all[from:to]}, nil
}

func (s *store) Reachable(_ context.Context, node uint64, uuid string) (Detail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failFind != nil {
		return Detail{}, s.failFind
	}
	for _, r := range s.records {
		if r.config.UUID != uuid {
			continue
		}
		moving := r.transfer != nil && (r.transfer.OldNode == node || r.transfer.NewNode == node)
		if s.leak || r.node == node || moving {
			return Detail{NodeID: r.node, Transfer: r.transfer, Server: r.config}, nil
		}
	}
	return Detail{}, ErrNotFound
}

func (s *store) Own(_ context.Context, node uint64, uuid string) (Held, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failFind != nil {
		return Held{}, s.failFind
	}
	r := s.find(node, uuid)
	if r == nil {
		return Held{}, ErrNotFound
	}
	return Held{ID: r.id, UUID: uuid, NodeID: r.node, Name: r.config.Name, Status: r.status, InstalledBefore: r.before, OwnerEmail: "ada@example.com", OwnerUsername: "ada"}, nil
}

func (s *store) InstallScript(_ context.Context, id uint64) (InstallScript, error) {
	return InstallScript{ContainerImage: "ghcr.io/example/installer:latest", Entrypoint: "bash", Script: "#!/bin/bash\necho installing"}, nil
}

func (s *store) Installed(_ context.Context, id uint64, from string, o servers.InstallOutcome, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.installs = append(s.installs, installCall{id, from, o, at})
	return !s.installRace, nil
}

func (s *store) Resettable(_ context.Context, node uint64) ([]ResetCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ResetCandidate
	for _, r := range s.records {
		if r.node == node && (r.status == servers.StatusInstalling || r.status == servers.StatusRestoringBackup) {
			out = append(out, ResetCandidate{ID: r.id, Status: r.status, InstalledBefore: r.before, Restoring: r.backup})
		}
	}
	return append(out, s.extraReset...), nil
}

func (s *store) Reset(_ context.Context, node uint64, changes []ResetChange) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resets = append(s.resets, changes)
	return s.failReset
}

func (s *store) Transfer(_ context.Context, node uint64, uuid string) (TransferInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failFind != nil {
		return TransferInfo{}, s.failFind
	}
	for _, r := range s.records {
		if r.config.UUID != uuid {
			continue
		}
		moving := r.transfer != nil && (r.transfer.OldNode == node || r.transfer.NewNode == node)
		if s.leak || r.node == node || moving {
			return TransferInfo{ServerID: r.id, Transfer: r.transfer}, nil
		}
	}
	return TransferInfo{}, ErrNotFound
}

func (s *store) CompleteTransfer(_ context.Context, id uint64, r remote.TransferResult) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completes = append(s.completes, completeCall{id, r})
	return !s.completeRace, s.failComplete
}

func (s *store) Servers(_ context.Context, node uint64, uuids []string) (map[string]uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failActivity != nil {
		return nil, s.failActivity
	}
	out := map[string]uint64{}
	for _, u := range uuids {
		if r := s.find(node, u); r != nil {
			out[u] = r.id
		}
	}
	return out, nil
}

func (s *store) Users(_ context.Context, uuids []string) (map[string]uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]uint64{}
	for _, u := range s.users {
		for _, want := range uuids {
			if u.UUID == want {
				out[want] = u.ID
			}
		}
	}
	return out, nil
}

func (s *store) Store(_ context.Context, entries []StoredEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storeCalls++
	if s.failActivity != nil {
		return s.failActivity
	}
	s.activity = append(s.activity, entries...)
	return nil
}

func (s *store) Log(_ context.Context, e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failEvents != nil {
		return s.failEvents
	}
	s.events = append(s.events, e)
	return nil
}

func (s *store) Send(_ context.Context, m mail.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failMail != nil {
		return s.failMail
	}
	s.mails = append(s.mails, m)
	return nil
}

func (s *store) DeleteServer(_ context.Context, node uint64, uuid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.oldNodeCalls = append(s.oldNodeCalls, uuid)
	return s.failOldNode
}

type completedBackup struct {
	ID     uint64
	Result backups.Result
	At     time.Time
}

func userUUID(id uint64) string {
	return strings.Repeat(string(rune('0'+id)), 8) + "-0000-4000-8000-000000000000"
}

func newStore(t *testing.T) *store {
	t.Helper()
	s := &store{users: map[string]SFTPUser{}, prints: map[uint64][]string{}, restoring: map[uint64]bool{103: true}}
	s.backupList = []NodeBackup{
		{ID: 1, ServerID: 100, NodeID: node1, UUID: backupUUIDN(1), Name: "Running", Disk: "wings"},
		{ID: 2, ServerID: 100, NodeID: node1, UUID: backupUUIDN(2), Name: "Done", Disk: "wings", Completed: true, Successful: true},
		{ID: 3, ServerID: 105, NodeID: node2, UUID: backupUUIDN(3), Name: "Elsewhere", Disk: "wings"},
		{ID: 4, ServerID: 103, NodeID: node1, UUID: backupUUIDN(4), Name: "Nightly", Disk: "wings", Completed: true, Successful: true},
	}
	for id, name := range map[uint64]string{ada: "ada", root: "root", bob: "bob", cleo: "cleo", eve: "eve"} {
		s.users[name] = SFTPUser{ID: id, UUID: userUUID(id), Admin: id == root, PasswordHash: adaHash}
	}
	key, err := sshkeys.Parse(ed25519Line)
	if err != nil {
		t.Fatal(err)
	}
	s.prints[ada] = []string{key.Fingerprint}

	egg := servers.Egg{UUID: "egg-uuid", ConfigFiles: "{}", ConfigStartup: `{"done":"ready"}`, ConfigStop: "stop"}
	add := func(id, node uint64, uuid, name string, mutate func(*record)) {
		r := &record{id: id, node: node, owner: ada, config: servers.Server{
			UUID: uuid, Name: name, Startup: "java -jar server.jar", Image: "ghcr.io/example/java:21", Memory: 1024, Disk: 5000,
			Allocation: servers.Allocation{IP: "10.0.0.5", Port: 25565}, Allocations: []servers.Allocation{{IP: "10.0.0.5", Port: 25565}}, Egg: egg,
		}}
		if mutate != nil {
			mutate(r)
		}
		s.records = append(s.records, r)
	}
	add(100, node1, normalUUID, "Survival", func(r *record) {
		r.subusers = map[uint64][]string{bob: {"file.sftp", "file.read"}, cleo: {"file.read"}}
	})
	add(101, node1, installingUUID, "Creative", func(r *record) {
		r.status = servers.StatusInstalling
		r.config.Status = servers.StatusInstalling
		r.state = servers.State{Status: servers.StatusInstalling}
	})
	add(102, node1, reinstallUUID, "Again", func(r *record) {
		r.status, r.before = servers.StatusInstalling, true
		r.config.Status = servers.StatusInstalling
		r.state = servers.State{Status: servers.StatusInstalling}
	})
	add(103, node1, restoringUUID, "Restoring", func(r *record) {
		r.status, r.before = servers.StatusRestoringBackup, true
		r.config.Status = servers.StatusRestoringBackup
		r.state = servers.State{Status: servers.StatusRestoringBackup}
		r.backup = &BackupRef{ID: 7, Name: "nightly"}
	})
	add(104, node1, suspendedUUID, "Paused", func(r *record) {
		r.status = servers.StatusSuspended
		r.config.Status = servers.StatusSuspended
		r.state = servers.State{Status: servers.StatusSuspended}
	})
	add(105, node2, otherNodeUUID, "Elsewhere", nil)
	add(106, node1, movingUUID, "Moving", func(r *record) {
		r.transfer = &remote.Transfer{OldNode: node1, NewNode: node2, OldAllocation: 1, NewAllocation: 2, OldAdditional: []uint64{3}, NewAdditional: []uint64{4}}
		r.state = servers.State{Transferring: true}
	})
	return s
}

type options struct {
	notify servers.NotifyConfig
}

type rig struct {
	handlers *Handlers
	t        *testing.T
	client   *apitest.Client
	store    *store
	logs     *bytes.Buffer
}

func newRig(t *testing.T, opts ...func(*options)) *rig {
	t.Helper()
	o := options{notify: servers.NotifyConfig{Install: true, Reinstall: true}}
	for _, f := range opts {
		f(&o)
	}
	s := newStore(t)
	site, err := mail.NewSite("Raptor", "https://panel.example.com")
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	h, err := New(Deps{
		Servers: s, Users: s, Activity: s, Events: s, Outbox: s, OldNodes: s, Backups: s,
		Failures: middleware.NewTracker(), Site: site, Notify: o.notify,
		Logger: slog.New(slog.NewTextHandler(logs, nil)), Now: func() time.Time { return apitest.Now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, handlers: h, store: s, logs: logs, client: serve(t, h)}
}

// serve builds a world with the three nodes and the handlers on the real router.
func serve(t *testing.T, h *Handlers) *apitest.Client {
	t.Helper()
	w := apitest.NewWorld()
	for _, n := range []uint64{node1, node2, node3} {
		w.AddNode(n)
	}
	return w.Serve(t, func(g api.Group) { h.Register(g) })
}

func (r *rig) post(node uint64, target, body string) apitest.Reply {
	r.t.Helper()
	return r.client.AsNode(node, "POST", target, body)
}

func (r *rig) get(node uint64, target string) apitest.Reply {
	r.t.Helper()
	return r.client.AsNode(node, "GET", target, "")
}

var errBoom = errors.New("the database is down: password=hunter2")

// The backups of the world: the uuid ends in the number. 1 is running on node 1, 2 finished on node 1, 3 is on node 2.
func backupUUIDN(n int) string { return fmt.Sprintf("0000000%d-aaaa-4bbb-8ccc-dddddddddddd", n) }

func (s *store) OnNode(ctx context.Context, node uint64, uuid string) (NodeBackup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failBackups != nil {
		return NodeBackup{}, s.failBackups
	}
	for _, b := range s.backupList {
		if b.UUID == uuid && (s.leak || b.NodeID == node) {
			return b, nil
		}
	}
	return NodeBackup{}, ErrNotFound
}

func (s *store) Complete(_ context.Context, id uint64, r backups.Result, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backupDone = append(s.backupDone, completedBackup{id, r, at})
	return !s.completeBackupRace, s.failBackups
}

func (s *store) FinishRestore(_ context.Context, serverID uint64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.restoreEnds = append(s.restoreEnds, serverID)
	return s.restoring[serverID], s.failBackups
}
