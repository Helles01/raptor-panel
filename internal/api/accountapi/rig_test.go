package accountapi

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
	"github.com/Helles01/raptor-panel/internal/auth/session"
)

const (
	ada = 1 // the account the tests work on
	bob = 2 // another user, whose keys must stay out of reach
)

const (
	adaPW    = "a long enough password"
	newPW    = "another long password"
	adaEmail = "ada@example.com"
)

var adaHash = func() string {
	h, err := auth.HashPassword(adaPW)
	if err != nil {
		panic(err)
	}
	return h
}()

var errBoom = errors.New("the database is down: password=hunter2")

// store is the storage of the routes in memory, and a record of what was asked of it.
type store struct {
	mu sync.Mutex

	accounts map[uint64]*Account
	keys     map[uint64][]KeyRecord
	ssh      map[uint64][]SSHKeyRecord
	taken    map[string]bool // addresses other accounts have
	nextID   uint64
	fail     error

	emails        [][2]string
	passwords     []string
	secrets       []string
	enabled       [][]string
	enabledAt     []time.Time
	disabled      int
	keyChecks     []int // how many keys the user had when a key was created
	keyExistsRace bool
	hashes        []string
}

func (s *store) Account(_ context.Context, id uint64) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return Account{}, s.fail
	}
	a, ok := s.accounts[id]
	if !ok {
		return Account{}, ErrNotFound
	}
	return *a, nil
}

func (s *store) SetEmail(_ context.Context, id uint64, old, new string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	if s.taken[strings.ToLower(new)] {
		return ErrEmailTaken
	}
	s.emails = append(s.emails, [2]string{old, new})
	s.accounts[id].Email = new
	return nil
}

func (s *store) SetPassword(_ context.Context, id uint64, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	s.passwords = append(s.passwords, hash)
	s.accounts[id].PasswordHash = hash
	return nil
}

func (s *store) SetTOTPSecret(_ context.Context, id uint64, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	s.secrets = append(s.secrets, secret)
	s.accounts[id].TOTPSecret = secret
	return nil
}

func (s *store) EnableTOTP(_ context.Context, id uint64, hashes []string, usedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	s.enabled = append(s.enabled, hashes)
	s.enabledAt = append(s.enabledAt, usedAt)
	s.accounts[id].UsesTOTP = true
	return nil
}

func (s *store) DisableTOTP(_ context.Context, id uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	s.disabled++
	s.accounts[id].UsesTOTP = false
	s.accounts[id].TOTPSecret = ""
	return nil
}

func (s *store) APIKeys(_ context.Context, id uint64) ([]KeyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]KeyRecord(nil), s.keys[id]...), s.fail
}

func (s *store) CreateKey(_ context.Context, id uint64, k NewKey, check func(int) error) (KeyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return KeyRecord{}, s.fail
	}
	s.keyChecks = append(s.keyChecks, len(s.keys[id]))
	if err := check(len(s.keys[id])); err != nil {
		return KeyRecord{}, err
	}
	s.nextID++
	rec := KeyRecord{ID: s.nextID, Identifier: k.Identifier, Description: k.Description, AllowedIPs: k.AllowedIPs, CreatedAt: apitest.Now}
	s.keys[id] = append(s.keys[id], rec)
	// What a stolen database would show: the hash of the secret, not the secret.
	s.hashes = append(s.hashes, k.SecretHash)
	return rec, nil
}

func (s *store) DeleteKey(_ context.Context, id uint64, identifier string) (KeyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return KeyRecord{}, s.fail
	}
	for i, k := range s.keys[id] {
		if k.Identifier == identifier {
			s.keys[id] = append(s.keys[id][:i:i], s.keys[id][i+1:]...)
			return k, nil
		}
	}
	return KeyRecord{}, ErrNotFound
}

func (s *store) SSHKeys(_ context.Context, id uint64) ([]SSHKeyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SSHKeyRecord(nil), s.ssh[id]...), s.fail
}

func (s *store) HasFingerprint(_ context.Context, id uint64, fp string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.ssh[id] {
		if k.Fingerprint == fp {
			return true, s.fail
		}
	}
	return false, s.fail
}

func (s *store) AddSSHKey(_ context.Context, id uint64, k SSHKeyRecord) (SSHKeyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return SSHKeyRecord{}, s.fail
	}
	if s.keyExistsRace {
		return SSHKeyRecord{}, ErrKeyExists
	}
	s.nextID++
	k.ID = s.nextID
	s.ssh[id] = append(s.ssh[id], k)
	return k, nil
}

func (s *store) RemoveSSHKey(_ context.Context, id uint64, fp string) (SSHKeyRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return SSHKeyRecord{}, false, s.fail
	}
	for i, k := range s.ssh[id] {
		if k.Fingerprint == fp {
			s.ssh[id] = append(s.ssh[id][:i:i], s.ssh[id][i+1:]...)
			return k, true, nil
		}
	}
	return SSHKeyRecord{}, false, nil
}

type nodes struct {
	mu    sync.Mutex
	users []uint64
	err   error
}

func (n *nodes) DeauthorizeUser(_ context.Context, id uint64) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.users = append(n.users, id)
	return n.err
}

type activityLog struct {
	mu      sync.Mutex
	entries []Entry
	err     error
}

func (a *activityLog) Log(_ context.Context, e Entry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	a.entries = append(a.entries, e)
	return nil
}

type rig struct {
	t        *testing.T
	client   *apitest.Client
	world    *apitest.World
	store    *store
	nodes    *nodes
	activity *activityLog
	sessions *session.Manager
	logs     *bytes.Buffer
	handlers *Handlers
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, nodes: &nodes{}, activity: &activityLog{}, logs: &bytes.Buffer{}}
	r.store = &store{
		accounts: map[uint64]*Account{
			ada: {ID: ada, UUID: "00000001-0000-4000-8000-000000000000", Email: adaEmail, PasswordHash: adaHash},
			bob: {ID: bob, UUID: "00000002-0000-4000-8000-000000000000", Email: "bob@example.com", PasswordHash: adaHash},
		},
		keys: map[uint64][]KeyRecord{}, ssh: map[uint64][]SSHKeyRecord{}, taken: map[string]bool{"taken@example.com": true}, nextID: 10,
	}

	w := apitest.NewWorld()
	w.AddUser(ada, false)
	w.AddUser(bob, false)
	sessions, err := session.NewManager(session.NewMemoryStore(), session.Options{IdleTimeout: time.Hour, MaxLifetime: 8 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	w.Sessions = sessions
	r.sessions = sessions
	r.world = w

	h, err := New(Deps{
		Store: r.store, Sessions: sessions, Nodes: r.nodes, Activity: r.activity, Failures: middleware.NewTracker(), Issuer: "Raptor",
		Logger: slog.New(slog.NewTextHandler(r.logs, nil)), Now: func() time.Time { return apitest.Now.Add(17 * time.Second) }, // inside a step of the code, not on its edge
	})
	if err != nil {
		t.Fatal(err)
	}
	r.handlers = h
	r.client = w.Serve(t, func(g api.Group) { h.Register(g) })
	return r
}

const base = "/api/client/account"

func (r *rig) do(user uint64, method, path, body string) apitest.Reply {
	r.t.Helper()
	return r.client.As(user, method, base+path, body)
}

func (r *rig) login(user uint64) *session.Session {
	r.t.Helper()
	g, err := r.sessions.Create(context.Background(), "203.0.113.9", "test")
	if err != nil {
		r.t.Fatal(err)
	}
	s, err := r.sessions.Login(context.Background(), g, user)
	if err != nil {
		r.t.Fatal(err)
	}
	return s
}

func js(format string, a ...any) string { return fmt.Sprintf(format, a...) }
