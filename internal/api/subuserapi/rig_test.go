package subuserapi

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
	"github.com/Helles01/raptor-panel/internal/api/clientapi"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/mail"
)

// The users of the world. Ada owns the server; Bob, Cleo and Dave are subusers; Frank has an account and no part in the server.
const (
	ada   = 1
	grace = 2 // an administrator
	bob   = 3 // may manage subusers, holds a good deal
	cleo  = 4 // may only look at them, holds little
	dave  = 5 // holds only a power right
	eve   = 6 // a stranger
	frank = 7 // has an account, no access
	zed   = 8 // a subuser who holds more than Bob
	gil   = 9 // a subuser who holds a part of what Cleo does
)

const serverUUID = "aaaa1111-2222-4333-8444-555555555555"

func uuidOf(id uint64) string { return fmt.Sprintf("%08d-0000-4000-8000-000000000000", id) }

var perms = map[uint64][]string{
	bob:  {"user.read", "user.create", "user.update", "user.delete", "file.read", "control.start", "websocket.connect"},
	cleo: {"user.read", "file.read", "websocket.connect"},
	dave: {"control.start", "websocket.connect"},
	zed:  {"file.read", "backup.create", "websocket.connect"},
	gil:  {"file.read", "websocket.connect"},
}

type addCall struct{ Request AddRequest }

// store is the storage of the routes in memory, and a record of what was asked of it.
type store struct {
	mu sync.Mutex

	subs   map[uint64]Subuser
	people map[string]clientapi.Person // by lower case address
	ids    map[string]uint64
	nextID uint64

	adds       []AddRequest
	setCalls   []setCall
	removeIDs  []uint64
	taken      int // how many adds answer ErrUsernameTaken before one works
	emailTaken bool
	exists     bool
	fail       error
	failName   error
	lists      int
}

type setCall struct {
	UserID uint64
	Perms  []string
	Revoke bool
}

func (s *store) person(id uint64, name string) clientapi.Person {
	return clientapi.Person{UUID: uuidOf(id), Username: name, Email: name + "@example.com", CreatedAt: apitest.Now.Add(-48 * time.Hour)}
}

func newStore() *store {
	s := &store{subs: map[uint64]Subuser{}, people: map[string]clientapi.Person{}, ids: map[string]uint64{}, nextID: 100}
	for id, name := range map[uint64]string{ada: "ada", grace: "grace", bob: "bob", cleo: "cleo", dave: "dave", eve: "eve", frank: "frank", zed: "zed", gil: "gil"} {
		p := s.person(id, name)
		s.people[p.Email] = p
		s.ids[p.Email] = id
	}
	for id := range perms {
		p := s.person(id, map[uint64]string{bob: "bob", cleo: "cleo", dave: "dave", zed: "zed", gil: "gil"}[id])
		s.subs[id] = Subuser{UserID: id, User: p, Permissions: append([]string(nil), perms[id]...)}
	}
	return s
}

func (s *store) List(_ context.Context, serverID uint64) ([]Subuser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lists++
	if s.fail != nil {
		return nil, s.fail
	}
	var out []Subuser
	for _, id := range []uint64{bob, cleo, dave, zed, gil} {
		if sub, ok := s.subs[id]; ok {
			out = append(out, sub)
		}
	}
	return out, nil
}

func (s *store) Find(_ context.Context, _ uint64, uuid string) (Subuser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return Subuser{}, s.fail
	}
	for _, sub := range s.subs {
		if sub.User.UUID == uuid {
			return sub, nil
		}
	}
	return Subuser{}, ErrNotFound
}

func (s *store) UserByEmail(_ context.Context, email string) (UserRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return UserRef{}, s.fail
	}
	key := strings.ToLower(email)
	if p, ok := s.people[key]; ok {
		return UserRef{ID: s.ids[key], UUID: p.UUID, Username: p.Username}, nil
	}
	return UserRef{}, ErrNotFound
}

func (s *store) IsSubuser(_ context.Context, _, userID uint64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.subs[userID]
	return ok, nil
}

func (s *store) Add(_ context.Context, r AddRequest) (Subuser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adds = append(s.adds, r)
	switch {
	case s.exists:
		return Subuser{}, ErrExists
	case r.NewUser != nil && s.taken > 0:
		s.taken--
		return Subuser{}, ErrUsernameTaken
	case r.NewUser != nil && s.emailTaken:
		// Somebody else made the account first: the address is looked up again.
		s.emailTaken = false
		p := s.person(s.nextID, strings.Split(r.NewUser.Email, "@")[0])
		p.Email = r.NewUser.Email
		s.people[strings.ToLower(p.Email)] = p
		s.ids[strings.ToLower(p.Email)] = s.nextID
		s.nextID++
		return Subuser{}, ErrEmailTaken
	}
	userID := r.UserID
	var person clientapi.Person
	if r.NewUser != nil {
		userID = s.nextID
		s.nextID++
		person = clientapi.Person{UUID: r.NewUser.UUID, Username: r.NewUser.Username, Email: r.NewUser.Email, CreatedAt: apitest.Now}
	} else {
		for email, id := range s.ids {
			if id == userID {
				person = s.people[email]
			}
		}
	}
	sub := Subuser{UserID: userID, User: person, Permissions: r.Permissions}
	s.subs[userID] = sub
	return sub, nil
}

func (s *store) SetPermissions(_ context.Context, _, userID uint64, p []string, revoke bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setCalls = append(s.setCalls, setCall{userID, p, revoke})
	if s.fail != nil {
		return s.fail
	}
	sub := s.subs[userID]
	sub.Permissions = p
	s.subs[userID] = sub
	return nil
}

func (s *store) Remove(_ context.Context, _, userID uint64, revoke bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !revoke {
		return errors.New("the revocation was not asked for")
	}
	s.removeIDs = append(s.removeIDs, userID)
	if s.fail != nil {
		return s.fail
	}
	delete(s.subs, userID)
	return nil
}

func (s *store) ServerName(context.Context, uint64) (string, error) { return "Survival", s.failName }

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

type outbox struct {
	mu    sync.Mutex
	mails []mail.Message
	err   error
}

func (o *outbox) Send(_ context.Context, m mail.Message) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return o.err
	}
	o.mails = append(o.mails, m)
	return nil
}

type rig struct {
	t        *testing.T
	client   *apitest.Client
	store    *store
	activity *activityLog
	outbox   *outbox
	logs     *bytes.Buffer
	handlers *Handlers
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, store: newStore(), activity: &activityLog{}, outbox: &outbox{}, logs: &bytes.Buffer{}}

	w := apitest.NewWorld()
	for id, admin := range map[uint64]bool{ada: false, grace: true, bob: false, cleo: false, dave: false, eve: false, frank: false, zed: false, gil: false} {
		w.AddUser(id, admin)
	}
	w.AddServer(100, serverUUID, ada)
	w.AddServer(101, "bbbb2222-2222-4333-8444-555555555555", ada)
	for id, p := range perms {
		w.Subusers[[2]uint64{100, id}] = p
	}

	limiter, err := middleware.NewLimiter(CreateLimit, CreateWindow)
	if err != nil {
		t.Fatal(err)
	}
	site, err := mail.NewSite("Raptor", "https://panel.example.com")
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Deps{Store: r.store, Activity: r.activity, Outbox: r.outbox, CreateLimiter: limiter, Site: site, Logger: slog.New(slog.NewTextHandler(r.logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	r.handlers = h
	r.client = w.Serve(t, func(g api.Group) { h.Register(g) })
	return r
}

const usersURL = "/api/client/servers/aaaa1111/users"

func (r *rig) do(user uint64, method, target, body string) apitest.Reply {
	r.t.Helper()
	return r.client.As(user, method, target, body)
}

func request(email string, permissions ...string) string {
	quoted := make([]string, len(permissions))
	for i, p := range permissions {
		quoted[i] = fmt.Sprintf("%q", p)
	}
	return fmt.Sprintf(`{"email":%q,"permissions":[%s]}`, email, strings.Join(quoted, ","))
}

func body(permissions ...string) string {
	quoted := make([]string, len(permissions))
	for i, p := range permissions {
		quoted[i] = fmt.Sprintf("%q", p)
	}
	return fmt.Sprintf(`{"permissions":[%s]}`, strings.Join(quoted, ","))
}

var errBoom = errors.New("the database is down: password=hunter2")
