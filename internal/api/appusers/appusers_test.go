package appusers

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apitest"
	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/mail"
	"github.com/Helles01/raptor-panel/internal/servers"
	"github.com/Helles01/raptor-panel/internal/users"
)

const (
	grace = 1 // an administrator whose application key may read and write users
	bob   = 2 // an administrator whose key may only read users
	cleo  = 3 // an administrator whose key has other resources only
	dave  = 4 // not an administrator, with a key that says users=rw
	eve   = 5 // an administrator without an application key
)

var errBoom = errors.New("the database is down: password=hunter2")

var t0 = time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

type update struct {
	ID uint64
	Up users.Update
	Fx users.Effects
}

type store struct {
	mu sync.Mutex

	users  []appkit.User
	nextID uint64
	fail   error
	owned  int64

	lastLoad    appkit.Includes
	listQuery   listquery.Query
	listPage    [2]int
	listTotal   int
	created     []CreateRequest
	updates     []update
	deleted     []uint64
	deleteCheck error
}

func (s *store) List(_ context.Context, q listquery.Query, page, perPage int, load appkit.Includes) ([]appkit.User, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listQuery, s.listPage, s.lastLoad = q, [2]int{page, perPage}, load
	total := s.listTotal
	if total == 0 {
		total = len(s.users)
	}
	out := make([]appkit.User, len(s.users))
	for i, u := range s.users {
		out[i] = fill(u, load)
	}
	return out, total, s.fail
}

func (s *store) find(id uint64) (int, bool) {
	for i, u := range s.users {
		if u.ID == id {
			return i, true
		}
	}
	return 0, false
}

func (s *store) Find(_ context.Context, id uint64, load appkit.Includes) (appkit.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastLoad = load
	if s.fail != nil {
		return appkit.User{}, s.fail
	}
	if i, ok := s.find(id); ok {
		return fill(s.users[i], load), nil
	}
	return appkit.User{}, appkit.ErrNotFound
}

func (s *store) FindExternal(_ context.Context, ext string, load appkit.Includes) (appkit.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastLoad = load
	if s.fail != nil {
		return appkit.User{}, s.fail
	}
	for _, u := range s.users {
		if u.ExternalID != nil && *u.ExternalID == ext {
			return fill(u, load), nil
		}
	}
	return appkit.User{}, appkit.ErrNotFound
}

// taken is the unique keys of the table, for every user but the one with the id given.
func (s *store) taken(except uint64, email, username string, ext *string) error {
	for _, u := range s.users {
		switch {
		case u.ID == except:
		case strings.EqualFold(u.Email, email):
			return &appkit.TakenError{Field: "email"}
		case u.Username == username:
			return &appkit.TakenError{Field: "username"}
		case ext != nil && u.ExternalID != nil && *u.ExternalID == *ext:
			return &appkit.TakenError{Field: "external_id"}
		}
	}
	return nil
}

func (s *store) Create(_ context.Context, r CreateRequest) (appkit.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return appkit.User{}, s.fail
	}
	if err := s.taken(0, r.User.Email, r.User.Username, r.User.ExternalID); err != nil {
		return appkit.User{}, err
	}
	s.created = append(s.created, r)
	s.nextID++
	u := appkit.User{
		ID: s.nextID, ExternalID: r.User.ExternalID, UUID: r.User.UUID, Username: r.User.Username, Email: r.User.Email, FirstName: r.User.FirstName,
		LastName: r.User.LastName, Language: r.User.Language, Admin: r.User.Admin, CreatedAt: t0, UpdatedAt: t0,
	}
	s.users = append(s.users, u)
	return u, nil
}

func (s *store) Update(_ context.Context, id uint64, up users.Update, fx users.Effects) (appkit.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return appkit.User{}, s.fail
	}
	i, ok := s.find(id)
	if !ok {
		return appkit.User{}, appkit.ErrNotFound
	}
	if err := s.taken(id, up.Email, up.Username, up.ExternalID); err != nil {
		return appkit.User{}, err
	}
	s.updates = append(s.updates, update{id, up, fx})
	u := s.users[i]
	u.ExternalID, u.Email, u.Username, u.FirstName, u.LastName, u.Language, u.Admin = up.ExternalID, up.Email, up.Username, up.FirstName, up.LastName, up.Language, up.Admin
	u.UpdatedAt = t0.Add(time.Hour)
	s.users[i] = u
	return u, nil
}

func (s *store) Delete(_ context.Context, id uint64, check func(int64) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	if _, ok := s.find(id); !ok {
		return appkit.ErrNotFound
	}
	if err := check(s.owned); err != nil {
		return err
	}
	s.deleted = append(s.deleted, id)
	return nil
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

type outbox struct {
	mu   sync.Mutex
	sent []mail.Message
	err  error
}

func (o *outbox) Send(_ context.Context, m mail.Message) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return o.err
	}
	o.sent = append(o.sent, m)
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
	r := &rig{t: t, store: &store{nextID: 100}, activity: &activityLog{}, outbox: &outbox{}, logs: &bytes.Buffer{}}
	r.store.users = []appkit.User{
		{ID: 10, ExternalID: ptr("crm-10"), UUID: "10000000-0000-4000-8000-000000000000", Username: "ann", Email: "ann@example.com", FirstName: "Ann", LastName: "Lee", Language: "en", TwoFactor: true, CreatedAt: t0, UpdatedAt: t0.Add(time.Minute)},
		{ID: 11, UUID: "11000000-0000-4000-8000-000000000000", Username: "ben", Email: "ben@example.com", FirstName: "Ben", LastName: "Ray", Language: "hu", Admin: true, CreatedAt: t0, UpdatedAt: t0},
	}

	w := apitest.NewWorld()
	for id, admin := range map[uint64]bool{grace: true, bob: true, cleo: true, dave: false, eve: true} {
		w.AddUser(id, admin)
	}
	w.GrantApp(grace, "users=rw", "servers=read")
	w.GrantApp(bob, "users=read")
	w.GrantApp(cleo, "nodes=rw")
	w.GrantApp(dave, "users=rw")

	site, err := mail.NewSite("Raptor", "https://panel.example.com")
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Deps{
		Store: r.store, Activity: r.activity, Outbox: r.outbox, Site: site, BaseURL: "https://panel.example.com/", Languages: []string{"en", "hu"},
		Logger: slog.New(slog.NewTextHandler(r.logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	r.handlers = h
	r.client = w.Serve(t, func(g api.Group) { h.Register(g) })
	return r
}

const base = "/api/application/users"

func (r *rig) do(user uint64, method, path, body string) apitest.Reply {
	r.t.Helper()
	return r.client.AsApp(user, method, base+path, body)
}

func TestEveryRouteNeedsItsLevel(t *testing.T) {
	routes := []struct {
		name, method, path, body string
		write                    bool
	}{
		{"list", "GET", "", "", false},
		{"view", "GET", "/10", "", false},
		{"external", "GET", "/external/crm-10", "", false},
		{"create", "POST", "", `{"email":"new@example.com","username":"newbie","first_name":"New","last_name":"Bie"}`, true},
		{"update", "PATCH", "/10", `{"email":"ann@example.com","username":"ann","first_name":"Ann","last_name":"Lee"}`, true},
		{"delete", "DELETE", "/10", "", true},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			for user, open := range map[uint64]bool{grace: true, bob: !rt.write, cleo: false, dave: false} {
				r := newRig(t)
				rp := r.do(user, rt.method, rt.path, rt.body)
				if open && (rp.Status < 200 || rp.Status > 299) || !open && rp.Status != 403 {
					t.Errorf("user %d: status = %d: %s", user, rp.Status, rp.Raw)
				}
				if !open && (len(r.store.created)+len(r.store.updates)+len(r.store.deleted) != 0 || len(r.outbox.sent) != 0) {
					t.Errorf("user %d: something was changed", user)
				}
			}
			r := newRig(t)
			if rp := r.do(0, rt.method, rt.path, rt.body); rp.Status != 401 {
				t.Errorf("nobody: status = %d", rp.Status)
			}
			if rp := r.do(eve, rt.method, rt.path, rt.body); rp.Status != 401 {
				t.Errorf("no application key: status = %d", rp.Status)
			}
			// The key of the client API does not open the Application API, whatever the user is (C-054).
			if rp := r.client.As(grace, rt.method, base+rt.path, rt.body); rp.Status != 403 {
				t.Errorf("a client key: status = %d", rp.Status)
			}
		})
	}
}

func TestList(t *testing.T) {
	r := newRig(t)
	rp := r.do(grace, "GET", "?filter[email]=example&sort=-id&per_page=2&page=1", "")
	if rp.Status != 200 || rp.At("object") != "list" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	want := map[string]any{
		"id": 10.0, "external_id": "crm-10", "uuid": "10000000-0000-4000-8000-000000000000", "username": "ann", "email": "ann@example.com",
		"first_name": "Ann", "last_name": "Lee", "language": "en", "root_admin": false, "2fa": true,
		"created_at": "2026-10-01T10:00:00+00:00", "updated_at": "2026-10-01T10:01:00+00:00",
	}
	if rp.At("data", 0, "object") != "user" || !reflect.DeepEqual(rp.At("data", 0, "attributes"), want) {
		t.Errorf("first = %v", rp.At("data", 0))
	}
	if rp.At("data", 1, "attributes", "root_admin") != true || rp.At("data", 1, "attributes", "external_id") != nil {
		t.Errorf("second = %v", rp.At("data", 1, "attributes"))
	}
	if rp.At("meta", "pagination", "total") != 2.0 || rp.At("meta", "pagination", "per_page") != 2.0 || rp.At("meta", "pagination", "count") != 2.0 {
		t.Errorf("meta = %v", rp.At("meta"))
	}
	if got := r.store.listPage; got != [2]int{1, 2} {
		t.Errorf("page = %v", got)
	}
	if len(r.store.listQuery.Conditions) != 1 || r.store.listQuery.Conditions[0].Column != "email" || !reflect.DeepEqual(r.store.listQuery.Conditions[0].Values, []string{"example"}) ||
		len(r.store.listQuery.Order) != 1 || r.store.listQuery.Order[0] != (listquery.Order{Column: "id", Desc: true}) {
		t.Errorf("query = %+v", r.store.listQuery)
	}

	// The defaults of a page.
	r.do(grace, "GET", "", "")
	if got := r.store.listPage; got != [2]int{1, 50} {
		t.Errorf("default page = %v", got)
	}
}

func TestListRefusals(t *testing.T) {
	for name, tt := range map[string]struct {
		query  string
		status int
		code   string
	}{
		"a filter that is not offered": {"?filter[password]=x", 400, "InvalidFilterQuery"},
		"a sort that is not offered":   {"?sort=password", 400, "InvalidSortQuery"},
		"a page that is not a number":  {"?page=x", 422, ""},
		"too many per page":            {"?per_page=101", 422, ""},
		"no page at all":               {"?page=0", 422, ""},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(grace, "GET", tt.query, "")
			if rp.Status != tt.status || (tt.code != "" && rp.Code() != tt.code) {
				t.Errorf("answer = %d %s", rp.Status, rp.Raw)
			}
			if r.store.listPage != [2]int{} {
				t.Error("the store was asked")
			}
		})
	}
}

func TestViewAndExternal(t *testing.T) {
	r := newRig(t)
	for _, path := range []string{"/10", "/external/crm-10", "/010"} {
		rp := r.do(bob, "GET", path, "")
		if rp.Status != 200 || rp.At("object") != "user" || rp.At("attributes", "username") != "ann" {
			t.Errorf("%s: %d %s", path, rp.Status, rp.Raw)
		}
	}
	for _, path := range []string{"/99", "/0", "/x", "/-1", "/external/nobody", "/10x"} {
		if rp := r.do(bob, "GET", path, ""); rp.Status != 404 {
			t.Errorf("%s: status = %d", path, rp.Status)
		}
	}
	// A user without an external identifier is not found by the empty one.
	if rp := r.do(bob, "GET", "/external/", ""); rp.Status != 404 {
		t.Errorf("empty external: %d", rp.Status)
	}
}

var setupLink = regexp.MustCompile(`/auth/password/reset/([0-9a-f]{64})`)

func TestCreateWithoutAPassword(t *testing.T) {
	r := newRig(t)
	rp := r.do(grace, "POST", "", `{"external_id":"crm-77","email":"New@Example.com","username":"NewUser","first_name":"Nina","last_name":"Fox","root_admin":true}`)
	if rp.Status != 201 || rp.At("object") != "user" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if rp.At("meta", "resource") != "https://panel.example.com/api/application/users/101" {
		t.Errorf("meta = %v", rp.At("meta"))
	}
	a := rp.At("attributes").(map[string]any)
	if a["id"] != 101.0 || a["username"] != "newuser" || a["email"] != "New@Example.com" || a["external_id"] != "crm-77" || a["language"] != "en" || a["root_admin"] != true || a["2fa"] != false {
		t.Errorf("attributes = %v", a)
	}

	if len(r.store.created) != 1 {
		t.Fatalf("%d accounts made", len(r.store.created))
	}
	req := r.store.created[0]
	if !req.User.NeedsSetup || !strings.HasPrefix(req.User.PasswordHash, "$2") || len(req.SetupTokenHash) != 64 || req.User.UUID == "" || req.User.Admin != true {
		t.Errorf("request = %+v", req)
	}
	// The mail carries the link to choose a password, whose token is the one that was stored in hashed form.
	if len(r.outbox.sent) != 1 || r.outbox.sent[0].To != "New@Example.com" {
		t.Fatalf("mail = %+v", r.outbox.sent)
	}
	m := setupLink.FindStringSubmatch(r.outbox.sent[0].Text)
	if m == nil || m[1] == req.SetupTokenHash {
		t.Errorf("the link in the mail = %v, the stored hash %q", m, req.SetupTokenHash)
	}
	if len(r.activity.entries) != 1 {
		t.Fatalf("entries = %+v", r.activity.entries)
	}
	e := r.activity.entries[0]
	if e.Event != "user:user.create" || e.Properties != `{"admin":true,"email":"New@Example.com","username":"newuser"}` || e.SubjectUserID != 101 || e.ActorID != grace || e.APIKeyID == nil || e.IP != "203.0.113.9" {
		t.Errorf("entry = %+v", e)
	}
}

func TestCreateWithAPassword(t *testing.T) {
	r := newRig(t)
	rp := r.do(grace, "POST", "", `{"email":"pw@example.com","username":"pwuser","first_name":"Pw","last_name":"User","password":"a long and secret password","language":"hu"}`)
	if rp.Status != 201 || rp.At("attributes", "language") != "hu" || rp.At("attributes", "root_admin") != false {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	req := r.store.created[0]
	if req.User.NeedsSetup || req.SetupTokenHash != "" {
		t.Errorf("request = %+v", req)
	}
	if rp.Contains("a long and secret password") || strings.Contains(r.logs.String(), "secret password") || strings.Contains(req.User.PasswordHash, "secret password") {
		t.Error("the password was kept in the clear")
	}
	// The mail tells the person that the account exists and carries no link.
	if len(r.outbox.sent) != 1 || setupLink.MatchString(r.outbox.sent[0].Text) {
		t.Errorf("mail = %+v", r.outbox.sent)
	}
}

func TestCreateRefusals(t *testing.T) {
	good := func(extra string) string {
		return `{"email":"x@example.com","username":"xuser","first_name":"X","last_name":"Y"` + extra + `}`
	}
	tests := []struct {
		name, body string
		status     int
		detail     string
		field      string
	}{
		{"no email", `{"username":"xuser","first_name":"X","last_name":"Y"}`, 422, "The email field is required.", "email"},
		{"no username", `{"email":"x@example.com","first_name":"X","last_name":"Y"}`, 422, "The username field is required.", "username"},
		{"no first name", `{"email":"x@example.com","username":"xuser","last_name":"Y"}`, 422, "The first name field is required.", "first_name"},
		{"no last name", `{"email":"x@example.com","username":"xuser","first_name":"X"}`, 422, "The last name field is required.", "last_name"},
		{"a name that is a number", `{"email":"x@example.com","username":"xuser","first_name":5,"last_name":"Y"}`, 422, "The first name must be a string.", "first_name"},
		{"an external id that is a list", good(`,"external_id":["a"]`), 422, "The external id must be a string.", "external_id"},
		{"a password that is a number", good(`,"password":12345678`), 422, "The password must be a string.", "password"},
		{"root_admin as text", good(`,"root_admin":"maybe"`), 422, "The root admin field must be true or false.", "root_admin"},
		{"root_admin as null", good(`,"root_admin":null`), 422, "The root admin field must be true or false.", "root_admin"},
		{"a bad address", `{"email":"nobody","username":"xuser","first_name":"X","last_name":"Y"}`, 422, "", "email"},
		{"a bad user name", `{"email":"x@example.com","username":"a b","first_name":"X","last_name":"Y"}`, 422, "", "username"},
		{"a language that is not offered", good(`,"language":"xx"`), 422, "", "language"},
		{"a password that is too short", good(`,"password":"short"`), 422, "", "password"},
		{"the address of another user", `{"email":"ANN@example.com","username":"xuser","first_name":"X","last_name":"Y"}`, 422, "The email has already been taken.", "email"},
		{"the user name of another", `{"email":"x@example.com","username":"ann","first_name":"X","last_name":"Y"}`, 422, "The username has already been taken.", "username"},
		{"the external id of another", good(`,"external_id":"crm-10"`), 422, "The external id has already been taken.", "external_id"},
		{"not JSON", `email=x`, 400, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(grace, "POST", "", tt.body)
			if rp.Status != tt.status || (tt.detail != "" && rp.Detail() != tt.detail) {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if tt.field != "" && rp.At("errors", 0, "meta", "source_field") != tt.field {
				t.Errorf("field = %v", rp.At("errors", 0, "meta"))
			}
			if len(r.store.users) != 2 || len(r.outbox.sent) != 0 || len(r.activity.entries) != 0 {
				t.Error("an account was made")
			}
		})
	}
}

func TestCreateSideEffectsThatFailDoNotUndoIt(t *testing.T) {
	r := newRig(t)
	r.outbox.err, r.activity.err = errBoom, errBoom
	rp := r.do(grace, "POST", "", `{"email":"x@example.com","username":"xuser","first_name":"X","last_name":"Y"}`)
	if rp.Status != 201 || rp.Contains("hunter2") || len(r.store.users) != 3 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	for _, want := range []string{"the mail about a new account was not queued", "activity entry not stored"} {
		if !strings.Contains(r.logs.String(), want) {
			t.Errorf("log lacks %q: %s", want, r.logs.String())
		}
	}
}

func TestUpdate(t *testing.T) {
	r := newRig(t)
	rp := r.do(grace, "PATCH", "/10", `{"email":"ann.lee@example.com","username":"Ann.Lee","first_name":"Anne","last_name":"Lee","language":"hu","root_admin":true,"password":"another long password"}`)
	if rp.Status != 200 || rp.At("object") != "user" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	a := rp.At("attributes").(map[string]any)
	if a["email"] != "ann.lee@example.com" || a["username"] != "ann.lee" || a["first_name"] != "Anne" || a["language"] != "hu" || a["root_admin"] != true || a["external_id"] != "crm-10" {
		t.Errorf("attributes = %v", a)
	}
	if len(r.store.updates) != 1 {
		t.Fatalf("updates = %+v", r.store.updates)
	}
	u := r.store.updates[0]
	if u.ID != 10 || !u.Fx.PasswordChanged || !u.Fx.EmailChanged || u.Fx.AdminRevoked || !strings.HasPrefix(u.Up.PasswordHash, "$2") {
		t.Errorf("update = %+v", u)
	}
	if rp.Contains("another long password") {
		t.Error("the password came back")
	}
	// Nothing is written to the log for a change, as in the reference.
	if len(r.activity.entries) != 0 || len(r.outbox.sent) != 0 {
		t.Errorf("entries = %+v, mail = %+v", r.activity.entries, r.outbox.sent)
	}
}

func TestUpdateKeepsWhatWasNotSent(t *testing.T) {
	r := newRig(t)
	rp := r.do(grace, "PATCH", "/11", `{"email":"ben@example.com","username":"ben","first_name":"Ben","last_name":"Ray"}`)
	if rp.Status != 200 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	u := r.store.updates[0]
	if u.Up.Language != "hu" || !u.Up.Admin || u.Up.PasswordHash != "" || u.Fx.PasswordChanged || u.Fx.EmailChanged || u.Fx.AdminRevoked {
		t.Errorf("update = %+v", u)
	}
	// Taking the admin right away is what ends the use of the application keys the user made.
	rp = r.do(grace, "PATCH", "/11", `{"email":"ben@example.com","username":"ben","first_name":"Ben","last_name":"Ray","root_admin":false}`)
	if rp.Status != 200 || !r.store.updates[1].Fx.AdminRevoked || r.store.updates[1].Up.Admin {
		t.Errorf("demotion: %d %+v", rp.Status, r.store.updates[1])
	}
}

func TestUpdateExternalID(t *testing.T) {
	r := newRig(t)
	body := func(ext string) string {
		return `{"email":"ann@example.com","username":"ann","first_name":"Ann","last_name":"Lee"` + ext + `}`
	}
	for ext, want := range map[string]*string{`,"external_id":null`: nil, `,"external_id":""`: nil, `,"external_id":"crm-99"`: ptr("crm-99"), ``: ptr("crm-10")} {
		if rp := r.do(grace, "PATCH", "/10", body(ext)); rp.Status != 200 {
			t.Fatalf("%s: %d %s", ext, rp.Status, rp.Raw)
		}
		got := r.store.updates[len(r.store.updates)-1].Up.ExternalID
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q: external id = %v, want %v", ext, got, want)
		}
		r.store.users[0].ExternalID = ptr("crm-10")
	}
}

func TestUpdateRefusals(t *testing.T) {
	tests := []struct {
		name, path, body string
		status           int
		detail           string
	}{
		{"a user that is not there", "/99", `{"email":"a@example.com","username":"aa","first_name":"A","last_name":"B"}`, 404, ""},
		{"a user that is not there, and a bad body", "/99", `{}`, 404, ""},
		{"no email", "/10", `{"username":"ann","first_name":"Ann","last_name":"Lee"}`, 422, "The email field is required."},
		{"the address of another", "/10", `{"email":"ben@example.com","username":"ann","first_name":"Ann","last_name":"Lee"}`, 422, "The email has already been taken."},
		{"the user name of another", "/10", `{"email":"ann@example.com","username":"ben","first_name":"Ann","last_name":"Lee"}`, 422, "The username has already been taken."},
		{"the external id of another", "/11", `{"email":"ben@example.com","username":"ben","first_name":"Ben","last_name":"Ray","external_id":"crm-10"}`, 422, "The external id has already been taken."},
		{"a language that is not offered", "/10", `{"email":"ann@example.com","username":"ann","first_name":"Ann","last_name":"Lee","language":"xx"}`, 422, ""},
		{"a password that is too short", "/10", `{"email":"ann@example.com","username":"ann","first_name":"Ann","last_name":"Lee","password":"short"}`, 422, ""},
		{"root_admin as text", "/10", `{"email":"ann@example.com","username":"ann","first_name":"Ann","last_name":"Lee","root_admin":"x"}`, 422, "The root admin field must be true or false."},
		{"not JSON", "/10", `x`, 400, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(grace, "PATCH", tt.path, tt.body)
			if rp.Status != tt.status || (tt.detail != "" && rp.Detail() != tt.detail) {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if len(r.store.updates) != 0 {
				t.Error("a user was changed")
			}
		})
	}
}

func TestDelete(t *testing.T) {
	r := newRig(t)
	if rp := r.do(grace, "DELETE", "/10", ""); rp.Status != 204 || len(rp.Raw) != 0 || !reflect.DeepEqual(r.store.deleted, []uint64{10}) {
		t.Errorf("answer = %d %s, deleted %v", rp.Status, rp.Raw, r.store.deleted)
	}

	r.store.owned = 2
	rp := r.do(grace, "DELETE", "/11", "")
	if rp.Status != 400 || rp.Code() != "DisplayException" || rp.Detail() != "Cannot delete a user with active servers attached to their account. Please delete their servers before continuing." || len(r.store.deleted) != 1 {
		t.Errorf("a user with servers: %d %s", rp.Status, rp.Raw)
	}
	for _, path := range []string{"/99", "/x"} {
		if rp := r.do(grace, "DELETE", path, ""); rp.Status != 404 {
			t.Errorf("%s: status = %d", path, rp.Status)
		}
	}
}

func TestStoreFailuresAreNotShown(t *testing.T) {
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "", ""}, {"GET", "/10", ""}, {"GET", "/external/crm-10", ""},
		{"POST", "", `{"email":"x@example.com","username":"xuser","first_name":"X","last_name":"Y"}`},
		{"PATCH", "/10", `{"email":"ann@example.com","username":"ann","first_name":"Ann","last_name":"Lee"}`},
		{"DELETE", "/10", ""},
	} {
		r := newRig(t)
		r.store.fail = errBoom
		rp := r.do(grace, rt.method, rt.path, rt.body)
		if rp.Status != 500 || rp.Contains("hunter2") {
			t.Errorf("%s %s: %d %s", rt.method, rt.path, rp.Status, rp.Raw)
		}
	}
}

// A route registered without the authorizer must refuse, not serve.
func TestHandlersRefuseWithoutTheAuthorizer(t *testing.T) {
	r := newRig(t)
	client := apitest.NewWorld().Serve(t, func(g api.Group) {
		b := g.Sub("/bare")
		b.GET("", r.handlers.List, api.Public())
		b.GET("/view", r.handlers.View, api.Public())
		b.GET("/external", r.handlers.External, api.Public())
		b.POST("", r.handlers.Create, api.Public())
		b.PATCH("/update", r.handlers.Update, api.Public())
		b.DELETE("/delete", r.handlers.Delete, api.Public())
	})
	for _, rt := range []struct{ method, path string }{
		{"GET", "/bare"}, {"GET", "/bare/view"}, {"GET", "/bare/external"}, {"POST", "/bare"}, {"PATCH", "/bare/update"}, {"DELETE", "/bare/delete"},
	} {
		if rp := client.As(0, rt.method, rt.path, `{}`); rp.Status != 403 {
			t.Errorf("%s %s: status = %d, want 403", rt.method, rt.path, rp.Status)
		}
	}
	if len(r.store.created)+len(r.store.updates)+len(r.store.deleted) != 0 {
		t.Error("something was changed")
	}
}

func TestNewNeedsEverything(t *testing.T) {
	r := newRig(t)
	full := r.handlers.d
	if _, err := New(full); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Deps){
		"no store": func(d *Deps) { d.Store = nil }, "no activity": func(d *Deps) { d.Activity = nil }, "no outbox": func(d *Deps) { d.Outbox = nil },
		"no logger": func(d *Deps) { d.Logger = nil }, "no address": func(d *Deps) { d.BaseURL = "" }, "no languages": func(d *Deps) { d.Languages = nil },
	} {
		d := full
		mutate(&d)
		if _, err := New(d); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	d := full
	d.Random = nil
	if h, err := New(d); err != nil || h.d.Random == nil {
		t.Errorf("default random: %v", err)
	}
}

func TestTheMailGreetsTheNewUser(t *testing.T) {
	r := newRig(t)
	if rp := r.do(grace, "POST", "", `{"email":"nina@example.com","username":"nina","first_name":"Nina","last_name":"Fox"}`); rp.Status != 201 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if len(r.outbox.sent) != 1 || !strings.Contains(r.outbox.sent[0].Text, "Hello Nina!") || !strings.Contains(r.outbox.sent[0].Text, "Username: nina") {
		t.Errorf("mail = %+v", r.outbox.sent)
	}
}

func TestANullPasswordIsNoPassword(t *testing.T) {
	r := newRig(t)
	for _, body := range []string{
		`{"email":"n1@example.com","username":"nullpw","first_name":"N","last_name":"One","password":null}`,
		`{"email":"n2@example.com","username":"emptypw","first_name":"N","last_name":"Two","password":""}`,
	} {
		rp := r.do(grace, "POST", "", body)
		if rp.Status != 201 || !r.store.created[len(r.store.created)-1].User.NeedsSetup {
			t.Errorf("%s: %d %s", body, rp.Status, rp.Raw)
		}
	}
}

// fill is what a store does for the relations that were asked for.
func fill(u appkit.User, load appkit.Includes) appkit.User {
	if load["servers"] {
		u.Servers = []appkit.Server{{ID: 500, ShortID: "aaaa1111", OwnerID: u.ID, Config: servers.Server{UUID: "aaaa1111-2222-4333-8444-555555555555", Name: "Survival"}}}
	}
	return u
}

func TestTheServersOfAUser(t *testing.T) {
	r := newRig(t)
	rp := r.do(grace, "GET", "/10?include=servers", "")
	if rp.Status != 200 || rp.At("attributes", "relationships", "servers", "object") != "list" || rp.At("attributes", "relationships", "servers", "data", 0, "object") != "server" ||
		rp.At("attributes", "relationships", "servers", "data", 0, "attributes", "name") != "Survival" || rp.At("attributes", "relationships", "servers", "data", 0, "attributes", "user") != 10.0 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if !r.store.lastLoad["servers"] {
		t.Errorf("load = %v", r.store.lastLoad)
	}
	for _, path := range []string{"?include=servers", "/external/crm-10?include=servers"} {
		if rp := r.do(grace, "GET", path, ""); rp.Status != 200 {
			t.Errorf("%s: %d", path, rp.Status)
		}
	}
	// A key that may read users but not servers is given a null resource, and the store is not asked.
	r = newRig(t)
	rp = r.do(bob, "GET", "/10?include=servers", "")
	if rp.At("attributes", "relationships", "servers", "object") != "null_resource" || len(r.store.lastLoad) != 0 {
		t.Errorf("answer = %s, load = %v", rp.Raw, r.store.lastLoad)
	}
	// Without the include nothing is attached, and nothing is loaded.
	r = newRig(t)
	if rp := r.do(grace, "GET", "/10", ""); rp.Has("attributes", "relationships") || len(r.store.lastLoad) != 0 {
		t.Errorf("answer = %s, load = %v", rp.Raw, r.store.lastLoad)
	}
}
