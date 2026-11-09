package backupapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apitest"
	"github.com/Helles01/raptor-panel/internal/api/controlapi"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/backups"
	"github.com/Helles01/raptor-panel/internal/servers"
	"github.com/Helles01/raptor-panel/internal/wings"
)

const (
	ada   = 1 // owner
	grace = 2 // administrator
	bob   = 3 // holds every backup right
	cleo  = 4 // may only look
	dave  = 5 // may only start backups
	eve   = 6 // a stranger
)

const serverUUID = "aaaa1111-2222-4333-8444-555555555555"

var node = wings.Node{Scheme: "https", FQDN: "node1.example.com", Port: 8080, Token: strings.Repeat("a", 64)}

var errBoom = errors.New("the database is down: password=hunter2")

var t0 = apitest.Now

func ptr[T any](v T) *T { return &v }

// The backups of the server. Their UUIDs end in the number.
func uuidN(n int) string { return fmt.Sprintf("0000000%d-2222-4333-8444-555555555555", n) }

type store struct {
	mu sync.Mutex

	records []Record
	ledger  []time.Time // when every backup the server ever had was started, deleted ones too
	pruned  []uint64    // the backups whose rows went to make room for another
	limit   *int64
	nextID  uint64
	fail    error
	busy    bool // SetServerStatus answers false

	starts   []NewBackup
	since    time.Time // the start of the throttle window of the last request
	locked   map[uint64]bool
	deleted  []uint64
	statuses [][2]string
}

func (s *store) Page(_ context.Context, _ uint64, offset, limit int) (Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return Page{}, s.fail
	}
	nonFailed := 0
	for _, r := range s.records {
		if r.CompletedAt == nil || r.Successful {
			nonFailed++
		}
	}
	from := min(offset, len(s.records))
	to := min(from+limit, len(s.records))
	return Page{Total: len(s.records), NonFailed: nonFailed, Backups: s.records[from:to]}, nil
}

func (s *store) Find(_ context.Context, _ uint64, uuid string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return Record{}, s.fail
	}
	for _, r := range s.records {
		if r.UUID == uuid {
			return r, nil
		}
	}
	return Record{}, ErrNotFound
}

func (s *store) Start(_ context.Context, r StartRequest) (Started, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return Started{}, s.fail
	}
	var existing []backups.Existing
	for _, x := range s.records {
		if x.CompletedAt == nil || x.Successful {
			existing = append(existing, backups.Existing{ID: x.ID, CreatedAt: x.CreatedAt, Locked: x.Locked})
		}
	}
	s.since = r.Since
	admission, err := r.Check(s.limit, existing, s.ledger)
	if err != nil {
		return Started{}, err
	}
	var pruned *Record
	if admission.Prune {
		for i, x := range s.records {
			if x.ID == admission.PruneID {
				x := x
				pruned = &x
				s.records = append(s.records[:i:i], s.records[i+1:]...)
				s.pruned = append(s.pruned, x.ID)
				break
			}
		}
	}
	s.starts = append(s.starts, r.Backup)
	s.nextID++
	rec := Record{ID: s.nextID, UUID: r.Backup.UUID, Name: r.Backup.Name, Disk: r.Backup.Disk, IgnoredFiles: r.Backup.Ignored, Locked: r.Backup.Locked, CreatedAt: t0}
	s.records = append(s.records, rec)
	s.ledger = append(s.ledger, t0)
	return Started{Record: rec, Pruned: pruned}, nil
}

func (s *store) SetLocked(_ context.Context, _, id uint64, locked bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locked == nil {
		s.locked = map[uint64]bool{}
	}
	s.locked[id] = locked
	return s.fail
}

func (s *store) Delete(_ context.Context, _, id uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	s.deleted = append(s.deleted, id)
	for i, r := range s.records {
		if r.ID == id {
			s.records = append(s.records[:i:i], s.records[i+1:]...)
			break
		}
	}
	return nil
}

func (s *store) SetServerStatus(_ context.Context, _ uint64, from, to string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses = append(s.statuses, [2]string{from, to})
	return !s.busy, s.fail
}

type daemonCall struct {
	Kind, ServerUUID, BackupUUID, Disk, Ignore, URL string
	Truncate                                        bool
}

type fakeDaemon struct {
	mu        sync.Mutex
	calls     []daemonCall
	createErr error
	restoreEr error
	deleteErr error
}

func (d *fakeDaemon) Create(_ context.Context, _ wings.Node, serverUUID, disk, backupUUID, ignore string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, daemonCall{Kind: "create", ServerUUID: serverUUID, BackupUUID: backupUUID, Disk: disk, Ignore: ignore})
	return d.createErr
}

func (d *fakeDaemon) Restore(_ context.Context, _ wings.Node, serverUUID, backupUUID, disk string, truncate bool, url string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, daemonCall{Kind: "restore", ServerUUID: serverUUID, BackupUUID: backupUUID, Disk: disk, Truncate: truncate, URL: url})
	return d.restoreEr
}

func (d *fakeDaemon) Delete(_ context.Context, _ wings.Node, serverUUID, backupUUID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, daemonCall{Kind: "delete", ServerUUID: serverUUID, BackupUUID: backupUUID})
	return d.deleteErr
}

func (d *fakeDaemon) count(kind string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, c := range d.calls {
		if c.Kind == kind {
			n++
		}
	}
	return n
}

type targets struct {
	err error
}

func (t targets) For(_ context.Context, _ uint64) (controlapi.Target, error) {
	if t.err != nil {
		return controlapi.Target{}, t.err
	}
	return controlapi.Target{UUID: serverUUID, Node: node}, nil
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
	daemon   *fakeDaemon
	targets  *targets
	activity *activityLog
	logs     *bytes.Buffer
	handlers *Handlers
}

func newRig(t *testing.T, opts ...func(*Deps)) *rig {
	t.Helper()
	r := &rig{t: t, store: &store{nextID: 100}, daemon: &fakeDaemon{}, targets: &targets{}, activity: &activityLog{}, logs: &bytes.Buffer{}}
	r.store.limit = ptr(int64(5))
	done := t0.Add(-time.Hour)
	checksum := "sha256:abc123"
	r.store.records = []Record{
		{ID: 1, UUID: uuidN(1), Name: "Nightly", Disk: "wings", IgnoredFiles: []string{"*.log"}, Checksum: &checksum, Bytes: 1048576, Successful: true, CreatedAt: t0.Add(-2 * time.Hour), CompletedAt: &done},
		{ID: 2, UUID: uuidN(2), Name: "Failed one", Disk: "wings", Successful: false, CreatedAt: t0.Add(-3 * time.Hour), CompletedAt: &done},
		{ID: 3, UUID: uuidN(3), Name: "Running", Disk: "wings", CreatedAt: t0.Add(-time.Minute)},
		{ID: 4, UUID: uuidN(4), Name: "Locked", Disk: "wings", Locked: true, Successful: true, Bytes: 5, CreatedAt: t0.Add(-4 * time.Hour), CompletedAt: &done},
		{ID: 5, UUID: uuidN(5), Name: "On S3", Disk: "s3", Successful: true, CreatedAt: t0.Add(-5 * time.Hour), CompletedAt: &done},
	}

	w := apitest.NewWorld()
	for id, admin := range map[uint64]bool{ada: false, grace: true, bob: false, cleo: false, dave: false, eve: false} {
		w.AddUser(id, admin)
	}
	w.AddServer(100, serverUUID, ada)
	w.AddServer(101, "bbbb2222-2222-4333-8444-555555555555", ada)
	w.Subusers[[2]uint64{100, bob}] = []string{"backup.read", "backup.create", "backup.delete", "backup.download", "backup.restore", "websocket.connect"}
	w.Subusers[[2]uint64{100, cleo}] = []string{"backup.read", "websocket.connect"}
	w.Subusers[[2]uint64{100, dave}] = []string{"backup.create", "websocket.connect"}
	r.world = w

	issuer, err := wings.NewIssuer("https://panel.example.com")
	if err != nil {
		t.Fatal(err)
	}
	limiter, err := middleware.NewLimiter(RestoreLimit, RestoreWindow)
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{
		Store: r.store, Targets: r.targets, Daemon: r.daemon, Tokens: issuer, Activity: r.activity, RestoreLimiter: limiter,
		ThrottleLimit: 2, ThrottlePeriod: 600 * time.Second, Logger: slog.New(slog.NewTextHandler(r.logs, nil)),
		Now: func() time.Time { return t0 },
	}
	for _, f := range opts {
		f(&deps)
	}
	h, err := New(deps)
	if err != nil {
		t.Fatal(err)
	}
	r.handlers = h
	r.client = w.Serve(t, func(g api.Group) { h.Register(g) })
	return r
}

const base = "/api/client/servers/aaaa1111/backups"

func (r *rig) do(user uint64, method, path, body string) apitest.Reply {
	r.t.Helper()
	return r.client.As(user, method, base+path, body)
}

func TestList(t *testing.T) {
	r := newRig(t)
	rp := r.do(ada, "GET", "", "")
	if rp.Status != 200 || rp.At("object") != "list" {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if l, _ := rp.At("data").([]any); len(l) != 5 {
		t.Fatalf("%d backups", len(l))
	}
	want := map[string]any{
		"uuid": uuidN(1), "is_successful": true, "is_locked": false, "name": "Nightly", "ignored_files": []any{"*.log"}, "checksum": "sha256:abc123",
		"bytes": 1048576.0, "created_at": "2026-10-23T10:00:00+00:00", "completed_at": "2026-10-23T11:00:00+00:00",
	}
	if rp.At("data", 0, "object") != "backup" || !reflect.DeepEqual(rp.At("data", 0, "attributes"), want) {
		t.Errorf("first = %v", rp.At("data", 0))
	}
	// A running backup has no completion time or checksum, and an empty list of ignored files is a list.
	if rp.At("data", 2, "attributes", "completed_at") != nil || rp.At("data", 2, "attributes", "checksum") != nil || len(rp.At("data", 2, "attributes", "ignored_files").([]any)) != 0 {
		t.Errorf("running = %v", rp.At("data", 2, "attributes"))
	}
	// The failed one does not count against the limit.
	if rp.At("meta", "backup_count") != 4.0 || rp.At("meta", "pagination", "total") != 5.0 || rp.At("meta", "pagination", "per_page") != 20.0 {
		t.Errorf("meta = %v", rp.At("meta"))
	}
}

func TestListPages(t *testing.T) {
	r := newRig(t)
	rp := r.do(ada, "GET", "?per_page=2&page=3", "")
	if len(rp.At("data").([]any)) != 1 || rp.At("meta", "pagination", "current_page") != 3.0 || rp.At("meta", "pagination", "total_pages") != 3.0 {
		t.Errorf("answer = %s", rp.Raw)
	}
	if rp := r.do(ada, "GET", "?per_page=50", ""); rp.Status != 200 {
		t.Errorf("per_page 50: status = %d", rp.Status)
	}
	for _, q := range []string{"?per_page=51", "?per_page=0", "?page=0", "?page=x"} {
		if rp := r.do(ada, "GET", q, ""); rp.Status != 422 {
			t.Errorf("%s: status = %d", q, rp.Status)
		}
	}
}

func TestView(t *testing.T) {
	r := newRig(t)
	rp := r.do(ada, "GET", "/"+uuidN(1), "")
	if rp.Status != 200 || rp.At("object") != "backup" || rp.At("attributes", "name") != "Nightly" {
		t.Errorf("answer = %d %s", rp.Status, rp.Raw)
	}
	if rp := r.do(ada, "GET", "/"+strings.ToUpper(uuidN(1)), ""); rp.Status != 200 {
		t.Errorf("upper case: status = %d", rp.Status)
	}
	for _, id := range []string{uuidN(9), "not-a-uuid", "1"} {
		if rp := r.do(ada, "GET", "/"+id, ""); rp.Status != 404 {
			t.Errorf("%s: status = %d", id, rp.Status)
		}
	}
}

func TestEveryRouteHasItsPermission(t *testing.T) {
	routes := []struct {
		name, method, path, body string
		allowed                  []uint64
	}{
		{"list", "GET", "", "", []uint64{bob, cleo}},
		{"view", "GET", "/" + uuidN(1), "", []uint64{bob, cleo}},
		{"start", "POST", "", `{}`, []uint64{bob, dave}},
		{"download", "GET", "/" + uuidN(1) + "/download", "", []uint64{bob}},
		{"lock", "POST", "/" + uuidN(1) + "/lock", "", []uint64{bob}},
		{"restore", "POST", "/" + uuidN(1) + "/restore", `{"truncate":false}`, []uint64{bob}},
		{"delete", "DELETE", "/" + uuidN(2), "", []uint64{bob}},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			for _, user := range []uint64{ada, grace, bob, cleo, dave} {
				r := newRig(t)
				rp := r.do(user, rt.method, rt.path, rt.body)
				open := user == ada || user == grace
				for _, a := range rt.allowed {
					open = open || a == user
				}
				if open && (rp.Status < 200 || rp.Status > 299) || !open && rp.Status != 403 {
					t.Errorf("user %d: status = %d: %s", user, rp.Status, rp.Raw)
				}
			}
			r := newRig(t)
			if rp := r.do(eve, rt.method, rt.path, rt.body); rp.Status != 404 {
				t.Errorf("a stranger: status = %d", rp.Status)
			}
			if rp := r.do(0, rt.method, rt.path, rt.body); rp.Status != 401 {
				t.Errorf("nobody: status = %d", rp.Status)
			}
		})
	}
}

func TestStart(t *testing.T) {
	r := newRig(t)
	r.store.records = r.store.records[:1] // one of five used
	rp := r.do(ada, "POST", "", `{"name":"  Before the update  ","ignored":"*.log\r\ncache/\n\nplugins/*.jar"}`)
	if rp.Status != 200 || rp.At("object") != "backup" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	uuid, _ := rp.At("attributes", "uuid").(string)
	if len(uuid) != 36 || rp.At("attributes", "name") != "Before the update" || rp.At("attributes", "is_successful") != false || rp.At("attributes", "completed_at") != nil {
		t.Errorf("attributes = %v", rp.At("attributes"))
	}
	if got := rp.At("attributes", "ignored_files"); !reflect.DeepEqual(got, []any{"*.log", "cache/", "plugins/*.jar"}) {
		t.Errorf("ignored_files = %v", got)
	}
	if !reflect.DeepEqual(r.daemon.calls, []daemonCall{{Kind: "create", ServerUUID: serverUUID, BackupUUID: uuid, Disk: "wings", Ignore: "*.log\ncache/\nplugins/*.jar"}}) {
		t.Errorf("calls = %+v", r.daemon.calls)
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:backup.start" || r.activity.entries[0].Properties != `{"locked":false,"name":"Before the update"}` ||
		r.activity.entries[0].BackupID == 0 || r.activity.entries[0].ServerID != 100 {
		t.Errorf("entries = %+v", r.activity.entries)
	}
}

func TestStartDefaultsAndLock(t *testing.T) {
	r := newRig(t)
	r.store.records = nil
	rp := r.do(ada, "POST", "", `{}`)
	if rp.Status != 200 || rp.At("attributes", "name") != "Backup at 2026-10-23 12:00:00" || rp.At("attributes", "is_locked") != false {
		t.Errorf("default = %d %s", rp.Status, rp.Raw)
	}
	r = newRig(t)
	r.store.records = nil
	// Whoever may delete backups may lock the one they start.
	if rp := r.do(bob, "POST", "", `{"is_locked":true}`); rp.Status != 200 || rp.At("attributes", "is_locked") != true {
		t.Errorf("bob = %d %s", rp.Status, rp.Raw)
	}
	// Whoever may not has the request for a lock ignored, so that they cannot fill the server with backups nobody can remove.
	if rp := r.do(dave, "POST", "", `{"is_locked":true}`); rp.Status != 200 || rp.At("attributes", "is_locked") != false {
		t.Errorf("dave = %d %s", rp.Status, rp.Raw)
	}
	r.store.ledger = nil // two backups in ten minutes is the most
	if rp := r.do(ada, "POST", "", `{"is_locked":"1"}`); rp.At("attributes", "is_locked") != true {
		t.Errorf("a flag as text = %s", rp.Raw)
	}
}

func TestStartRefusals(t *testing.T) {
	tooMany := make([]string, 501)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("f%d", i)
	}
	tests := []struct {
		name   string
		body   string
		status int
		detail string
	}{
		{"a name too long", `{"name":"` + strings.Repeat("a", 192) + `"}`, 422, "The name must be at most 191 characters."},
		{"a name with a control character", `{"name":"a\u0000b"}`, 422, "The name must not contain control characters."},
		{"a name that is a number", `{"name":5}`, 422, "The name must be a string."},
		{"a lock that is not a flag", `{"is_locked":"maybe"}`, 422, "The is locked field must be true or false."},
		{"ignored files that are not text", `{"ignored":["a"]}`, 422, "The ignored must be a string."},
		{"too many ignored patterns", `{"ignored":"` + strings.Join(tooMany, `\n`) + `"}`, 422, "At most 500 ignored file patterns are allowed."},
		{"not JSON", `name=x`, 400, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			r.store.records = nil
			rp := r.do(ada, "POST", "", tt.body)
			if rp.Status != tt.status || (tt.detail != "" && rp.Detail() != tt.detail) {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if len(r.store.starts) != 0 || r.daemon.count("create") != 0 || len(r.activity.entries) != 0 {
				t.Error("a backup was started")
			}
		})
	}
}

func TestTheLimitOfBackups(t *testing.T) {
	r := newRig(t)
	// Four count (the failed one does not): the limit is four.
	r.store.limit = ptr(int64(4))
	rp := r.do(ada, "POST", "", `{}`)
	if rp.Status != 400 || rp.Code() != "DisplayException" || rp.Detail() != "Cannot create a new backup, this server has reached its limit of 4 backups." {
		t.Errorf("at the limit: %d %s", rp.Status, rp.Raw)
	}
	r.store.limit = ptr(int64(5))
	if rp := r.do(ada, "POST", "", `{}`); rp.Status != 200 {
		t.Errorf("below the limit: %d %s", rp.Status, rp.Raw)
	}
	for _, limit := range []*int64{nil, ptr(int64(0))} {
		r := newRig(t)
		r.store.limit = limit
		rp := r.do(ada, "POST", "", `{}`)
		if rp.Status != 400 || rp.Detail() != "Cannot create a new backup, this server has reached its limit of 0 backups." {
			t.Errorf("no backups allowed: %d %s", rp.Status, rp.Raw)
		}
	}
}

func TestBackupsAreStartedAtMostTwiceIn600Seconds(t *testing.T) {
	r := newRig(t)
	r.store.records = nil
	r.store.ledger = []time.Time{t0.Add(-9 * time.Minute)} // one a moment ago, and one that is long past
	r.store.ledger = append(r.store.ledger, t0.Add(-3*time.Hour))

	if rp := r.do(ada, "POST", "", `{}`); rp.Status != 200 {
		t.Fatalf("the second: %d %s", rp.Status, rp.Raw)
	}
	rp := r.do(ada, "POST", "", `{}`)
	if rp.Status != 429 || rp.Code() != "ThrottleRequestsException" || rp.Detail() != "Only 2 backups may be generated within a 600 second span of time." || rp.Header.Get("Retry-After") != "60" {
		t.Errorf("the third: %d %s, Retry-After %q", rp.Status, rp.Raw, rp.Header.Get("Retry-After"))
	}
	// Deleting one does not get round it: the throttle still counts what was started.
	r.store.records = nil
	if rp := r.do(ada, "POST", "", `{}`); rp.Status != 429 {
		t.Errorf("after deleting: status = %d", rp.Status)
	}
}

func TestABackupTheNodeCannotStartIsTakenBack(t *testing.T) {
	r := newRig(t)
	r.store.records = nil
	r.daemon.createErr = &wings.Error{Status: 504, Unreachable: true, Message: "Could not establish a connection to the machine running this server. Please try again."}
	rp := r.do(ada, "POST", "", `{}`)
	if rp.Status != 504 || rp.Code() != "DaemonConnectionException" {
		t.Errorf("answer = %d %s", rp.Status, rp.Raw)
	}
	if len(r.store.records) != 0 || len(r.store.deleted) != 1 || len(r.activity.entries) != 0 {
		t.Errorf("records %d, deleted %v, entries %v", len(r.store.records), r.store.deleted, r.activity.entries)
	}

	r = newRig(t)
	r.store.records = nil
	r.targets.err = controlapi.ErrNotFound
	if rp := r.do(ada, "POST", "", `{}`); rp.Status != 404 || len(r.store.records) != 0 {
		t.Errorf("a server without a node: %d", rp.Status)
	}
}

func TestToggleLock(t *testing.T) {
	r := newRig(t)
	rp := r.do(bob, "POST", "/"+uuidN(1)+"/lock", "")
	if rp.Status != 200 || rp.At("attributes", "is_locked") != true || !r.store.locked[1] {
		t.Fatalf("lock: %d %s", rp.Status, rp.Raw)
	}
	rp = r.do(bob, "POST", "/"+uuidN(4)+"/lock", "")
	if rp.Status != 200 || rp.At("attributes", "is_locked") != false || r.store.locked[4] {
		t.Fatalf("unlock: %d %s", rp.Status, rp.Raw)
	}
	if len(r.activity.entries) != 2 || r.activity.entries[0].Event != "server:backup.lock" || r.activity.entries[1].Event != "server:backup.unlock" ||
		r.activity.entries[0].Properties != `{"name":"Nightly"}` {
		t.Errorf("entries = %+v", r.activity.entries)
	}
	if rp := r.do(bob, "POST", "/"+uuidN(9)+"/lock", ""); rp.Status != 404 {
		t.Errorf("unknown: %d", rp.Status)
	}
}

func decodeToken(t *testing.T, token string) (map[string]any, bool) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(node.Token))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	return claims, base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) == parts[2]
}

func TestDownload(t *testing.T) {
	r := newRig(t)
	rp := r.do(bob, "GET", "/"+uuidN(1)+"/download", "")
	if rp.Status != 200 || rp.At("object") != "signed_url" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	url, _ := rp.At("attributes", "url").(string)
	const prefix = "https://node1.example.com:8080/download/backup?token="
	if !strings.HasPrefix(url, prefix) {
		t.Fatalf("url = %q", url)
	}
	claims, signed := decodeToken(t, strings.TrimPrefix(url, prefix))
	if !signed || claims["scope"] != "backup-download" || claims["backup_uuid"] != uuidN(1) || claims["server_uuid"] != serverUUID {
		t.Errorf("claims = %v, signed %v", claims, signed)
	}
	if rp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rp.Header.Get("Cache-Control"))
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:backup.download" || r.activity.entries[0].Properties != `{"name":"Nightly"}` {
		t.Errorf("entries = %+v", r.activity.entries)
	}
}

func TestDownloadRefusals(t *testing.T) {
	tests := []struct {
		name   string
		uuid   string
		status int
		detail string
	}{
		{"a backup that failed", uuidN(2), 400, "This backup cannot be downloaded at this time: not completed or failed."},
		{"a backup still running", uuidN(3), 400, "This backup cannot be downloaded at this time: not completed or failed."},
		{"a backup on S3", uuidN(5), 400, errUnsupportedDisk.Error()},
		{"a backup that is not there", uuidN(9), 404, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(bob, "GET", "/"+tt.uuid+"/download", "")
			if rp.Status != tt.status || (tt.detail != "" && rp.Detail() != tt.detail) {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if len(r.activity.entries) != 0 {
				t.Error("an entry was written")
			}
		})
	}
}

func TestRestore(t *testing.T) {
	r := newRig(t)
	rp := r.do(bob, "POST", "/"+uuidN(1)+"/restore", `{"truncate":true}`)
	if rp.Status != 204 || len(rp.Raw) != 0 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if !reflect.DeepEqual(r.store.statuses, [][2]string{{"", "restoring_backup"}}) {
		t.Errorf("statuses = %v", r.store.statuses)
	}
	if !reflect.DeepEqual(r.daemon.calls, []daemonCall{{Kind: "restore", ServerUUID: serverUUID, BackupUUID: uuidN(1), Disk: "wings", Truncate: true}}) {
		t.Errorf("calls = %+v", r.daemon.calls)
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:backup.restore" || r.activity.entries[0].Properties != `{"name":"Nightly","truncate":true}` {
		t.Errorf("entries = %+v", r.activity.entries)
	}
}

func TestRestoreRefusals(t *testing.T) {
	tests := []struct {
		name    string
		uuid    string
		body    string
		prepare func(*rig)
		status  int
		detail  string
	}{
		{"truncate not sent", uuidN(1), `{}`, nil, 422, "The truncate field is required."},
		{"truncate not a flag", uuidN(1), `{"truncate":"x"}`, nil, 422, "The truncate field must be true or false."},
		{"a backup that failed", uuidN(2), `{"truncate":false}`, nil, 400, "This backup cannot be restored at this time: not completed or failed."},
		{"a backup still running", uuidN(3), `{"truncate":false}`, nil, 400, "This backup cannot be restored at this time: not completed or failed."},
		{"a backup on S3", uuidN(5), `{"truncate":false}`, nil, 400, errUnsupportedDisk.Error()},
		{"a backup that is not there", uuidN(9), `{"truncate":false}`, nil, 404, ""},
		{"the server changed state meanwhile", uuidN(1), `{"truncate":false}`, func(r *rig) { r.store.busy = true }, 400, "This server is not currently in a state that allows for a backup to be restored."},
		{"a server that is installing", uuidN(1), `{"truncate":false}`, func(r *rig) {
			r.world.Servers[0].State = servers.State{Status: servers.StatusInstalling}
		}, 409, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			if tt.prepare != nil {
				tt.prepare(r)
			}
			rp := r.do(bob, "POST", "/"+tt.uuid+"/restore", tt.body)
			if rp.Status != tt.status || (tt.detail != "" && rp.Detail() != tt.detail) {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if r.daemon.count("restore") != 0 || len(r.activity.entries) != 0 {
				t.Error("the node was told or an entry written")
			}
		})
	}
}

func TestARestoreTheNodeRefusesIsTakenBack(t *testing.T) {
	r := newRig(t)
	r.daemon.restoreEr = &wings.Error{Status: 409, NodeStatus: 409, Message: "An error occurred on the remote host: busy. (request id: <nil>)"}
	rp := r.do(bob, "POST", "/"+uuidN(1)+"/restore", `{"truncate":false}`)
	if rp.Status != 409 || rp.Code() != "DaemonConnectionException" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if !reflect.DeepEqual(r.store.statuses, [][2]string{{"", "restoring_backup"}, {"restoring_backup", ""}}) {
		t.Errorf("statuses = %v: the mark must be taken back", r.store.statuses)
	}
	if len(r.activity.entries) != 0 {
		t.Error("a restore that did not start was logged")
	}
}

func TestRestoresAreThrottledForEachServer(t *testing.T) {
	r := newRig(t)
	for i := 0; i < RestoreLimit; i++ {
		if rp := r.do(bob, "POST", "/"+uuidN(1)+"/restore", `{"truncate":false}`); rp.Status != 204 {
			t.Fatalf("restore %d: %d", i+1, rp.Status)
		}
	}
	if rp := r.do(bob, "POST", "/"+uuidN(1)+"/restore", `{"truncate":false}`); rp.Status != 429 || rp.Header.Get("Retry-After") == "" {
		t.Errorf("the fourth: %d, Retry-After %q", rp.Status, rp.Header.Get("Retry-After"))
	}
	// Refused calls do not use the allowance up.
	r = newRig(t)
	for i := 0; i < 10; i++ {
		r.do(eve, "POST", "/"+uuidN(1)+"/restore", `{"truncate":false}`)
		r.do(cleo, "POST", "/"+uuidN(1)+"/restore", `{"truncate":false}`)
	}
	if rp := r.do(bob, "POST", "/"+uuidN(1)+"/restore", `{"truncate":false}`); rp.Status != 204 {
		t.Errorf("status = %d", rp.Status)
	}
}

func TestDelete(t *testing.T) {
	r := newRig(t)
	rp := r.do(bob, "DELETE", "/"+uuidN(1), "")
	if rp.Status != 204 || len(rp.Raw) != 0 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if !reflect.DeepEqual(r.daemon.calls, []daemonCall{{Kind: "delete", ServerUUID: serverUUID, BackupUUID: uuidN(1)}}) || !reflect.DeepEqual(r.store.deleted, []uint64{1}) {
		t.Errorf("calls %+v, deleted %v", r.daemon.calls, r.store.deleted)
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:backup.delete" || r.activity.entries[0].Properties != `{"failed":false,"name":"Nightly"}` {
		t.Errorf("entries = %+v", r.activity.entries)
	}
}

func TestDeleteRules(t *testing.T) {
	r := newRig(t)
	// A locked backup that finished stays.
	rp := r.do(bob, "DELETE", "/"+uuidN(4), "")
	if rp.Status != 400 || rp.Code() != "DisplayException" || rp.Detail() != "Cannot delete a backup that is marked as locked." || r.daemon.count("delete") != 0 || len(r.store.deleted) != 0 {
		t.Errorf("locked: %d %s", rp.Status, rp.Raw)
	}
	// A locked one that failed can go: the interface cannot unlock it.
	r.store.records[3].Successful = false
	done := t0
	r.store.records[3].CompletedAt = &done
	if rp := r.do(bob, "DELETE", "/"+uuidN(4), ""); rp.Status != 204 {
		t.Errorf("locked and failed: %d", rp.Status)
	}
	if rp := r.do(bob, "DELETE", "/"+uuidN(2), ""); rp.Status != 204 || r.activity.entries[1].Properties != `{"failed":true,"name":"Failed one"}` {
		t.Errorf("failed: %d, entries %+v", rp.Status, r.activity.entries)
	}
	if rp := r.do(bob, "DELETE", "/"+uuidN(5), ""); rp.Status != 400 || rp.Detail() != errUnsupportedDisk.Error() {
		t.Errorf("S3: %d %s", rp.Status, rp.Raw)
	}
	if rp := r.do(bob, "DELETE", "/"+uuidN(9), ""); rp.Status != 404 {
		t.Errorf("unknown: %d", rp.Status)
	}
}

func TestDeleteWhenTheNodeHasNoArchive(t *testing.T) {
	r := newRig(t)
	r.daemon.deleteErr = &wings.Error{Status: 404, NodeStatus: 404, Message: "An error occurred on the remote host: not found. (request id: <nil>)"}
	if rp := r.do(bob, "DELETE", "/"+uuidN(1), ""); rp.Status != 204 || len(r.store.deleted) != 1 {
		t.Errorf("a node that does not know it: %d", rp.Status)
	}

	r = newRig(t)
	r.daemon.deleteErr = &wings.Error{Status: 500, NodeStatus: 500, Message: "There was an error while communicating with the machine running this server."}
	rp := r.do(bob, "DELETE", "/"+uuidN(1), "")
	if rp.Status != 500 || rp.Code() != "DaemonConnectionException" || len(r.store.deleted) != 0 || len(r.activity.entries) != 0 {
		t.Errorf("a node that fails: %d %s", rp.Status, rp.Raw)
	}
}

func TestStoreFailuresDoNotLeak(t *testing.T) {
	for name, call := range map[string]func(*rig) apitest.Reply{
		"list":     func(r *rig) apitest.Reply { return r.do(ada, "GET", "", "") },
		"view":     func(r *rig) apitest.Reply { return r.do(ada, "GET", "/"+uuidN(1), "") },
		"start":    func(r *rig) apitest.Reply { return r.do(ada, "POST", "", `{}`) },
		"lock":     func(r *rig) apitest.Reply { return r.do(ada, "POST", "/"+uuidN(1)+"/lock", "") },
		"download": func(r *rig) apitest.Reply { return r.do(ada, "GET", "/"+uuidN(1)+"/download", "") },
		"restore":  func(r *rig) apitest.Reply { return r.do(ada, "POST", "/"+uuidN(1)+"/restore", `{"truncate":true}`) },
		"delete":   func(r *rig) apitest.Reply { return r.do(ada, "DELETE", "/"+uuidN(1), "") },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.store.fail = errBoom
			rp := call(r)
			if rp.Status != 500 || rp.Contains("hunter2") {
				t.Errorf("answer = %d %s", rp.Status, rp.Raw)
			}
		})
	}
	t.Run("a node store that fails", func(t *testing.T) {
		r := newRig(t)
		r.targets.err = errBoom
		for _, rp := range []apitest.Reply{r.do(ada, "GET", "/"+uuidN(1)+"/download", ""), r.do(ada, "POST", "/"+uuidN(1)+"/restore", `{"truncate":true}`), r.do(ada, "DELETE", "/"+uuidN(1), "")} {
			if rp.Status != 500 || rp.Contains("hunter2") {
				t.Errorf("answer = %d %s", rp.Status, rp.Raw)
			}
		}
	})
}

func TestAnEntryThatCannotBeStoredDoesNotUndoTheChange(t *testing.T) {
	r := newRig(t)
	r.activity.err = errBoom
	if rp := r.do(bob, "POST", "/"+uuidN(1)+"/lock", ""); rp.Status != 200 || !r.store.locked[1] {
		t.Errorf("status = %d", rp.Status)
	}
	if !strings.Contains(r.logs.String(), "activity entry not stored") {
		t.Errorf("logs = %s", r.logs.String())
	}
}

func TestHandlersRefuseWithoutTheirPrincipal(t *testing.T) {
	r := newRig(t)
	client := apitest.NewWorld().Serve(t, func(g api.Group) {
		b := g.Sub("/bare")
		b.GET("", r.handlers.List, api.Public())
		b.POST("", r.handlers.Start, api.Public())
		b.GET("/:backup", r.handlers.View, api.Public())
		b.GET("/:backup/download", r.handlers.Download, api.Public())
		b.POST("/:backup/lock", r.handlers.ToggleLock, api.Public())
		b.POST("/:backup/restore", r.handlers.Restore, api.Public())
		b.DELETE("/:backup", r.handlers.Delete, api.Public())
	})
	u := "/bare/" + uuidN(1)
	for _, rt := range []struct{ method, path string }{
		{"GET", "/bare"}, {"POST", "/bare"}, {"GET", u}, {"GET", u + "/download"}, {"POST", u + "/lock"}, {"POST", u + "/restore"}, {"DELETE", u},
	} {
		if rp := client.As(0, rt.method, rt.path, `{"truncate":true}`); rp.Status != 403 {
			t.Errorf("%s %s: status = %d, want 403", rt.method, rt.path, rp.Status)
		}
	}
	if len(r.store.starts)+len(r.store.deleted)+len(r.daemon.calls) != 0 {
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
		"no store": func(d *Deps) { d.Store = nil }, "no targets": func(d *Deps) { d.Targets = nil }, "no daemon": func(d *Deps) { d.Daemon = nil },
		"no tokens": func(d *Deps) { d.Tokens = nil }, "no activity": func(d *Deps) { d.Activity = nil }, "no limiter": func(d *Deps) { d.RestoreLimiter = nil },
		"no logger": func(d *Deps) { d.Logger = nil },
	} {
		d := full
		mutate(&d)
		if _, err := New(d); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
