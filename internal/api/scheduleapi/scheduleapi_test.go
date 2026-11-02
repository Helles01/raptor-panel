package scheduleapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apitest"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/schedules"
)

const (
	ada   = 1 // owner
	grace = 2 // administrator
	bob   = 3 // holds every right of these routes and the ones the tasks need
	cleo  = 4 // may only look
	dave  = 5 // may update schedules and nothing else
	eve   = 6 // a stranger
)

const serverUUID = "aaaa1111-2222-4333-8444-555555555555"

var errBoom = errors.New("the database is down: password=hunter2")

type store struct {
	mu sync.Mutex

	schedules map[uint64]*ScheduleRecord
	owner     map[uint64]uint64 // schedule id to server id
	nextID    uint64
	nextTask  uint64
	backup    *int64
	hidden    []int // sequence numbers that exist only when the tasks are locked: a request that came in meanwhile
	fail      error

	created []schedules.Plan
	updated []schedules.Plan
	deleted []uint64
	added   []schedules.Task
	changed []TaskChange
	removed []uint64
}

func (s *store) sorted(r *ScheduleRecord) {
	slices.SortFunc(r.Tasks, func(a, b TaskRecord) int { return a.Sequence - b.Sequence })
}

func (s *store) copy(r *ScheduleRecord) ScheduleRecord {
	out := *r
	out.Tasks = append([]TaskRecord(nil), r.Tasks...)
	s.sorted(&out)
	return out
}

func (s *store) List(_ context.Context, serverID uint64) ([]ScheduleRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return nil, s.fail
	}
	var out []ScheduleRecord
	for id := uint64(1); id < s.nextID; id++ {
		if r, ok := s.schedules[id]; ok && s.owner[id] == serverID {
			out = append(out, s.copy(r))
		}
	}
	return out, nil
}

func (s *store) Find(_ context.Context, serverID, id uint64) (ScheduleRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return ScheduleRecord{}, s.fail
	}
	r, ok := s.schedules[id]
	if !ok || s.owner[id] != serverID {
		return ScheduleRecord{}, ErrNotFound
	}
	return s.copy(r), nil
}

func apply(r *ScheduleRecord, p schedules.Plan) {
	r.Name, r.Minute, r.Hour, r.DayOfMonth, r.Month, r.DayOfWeek = p.Name, p.Minute, p.Hour, p.DayOfMonth, p.Month, p.DayOfWeek
	r.Active, r.OnlyWhenOnline = p.Active, p.OnlyWhenOnline
	next := p.NextRunAt
	r.NextRunAt = &next
	if p.ResetProcessing {
		r.Processing = false
	}
}

func (s *store) Create(_ context.Context, serverID uint64, p schedules.Plan) (ScheduleRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.created = append(s.created, p)
	if s.fail != nil {
		return ScheduleRecord{}, s.fail
	}
	r := &ScheduleRecord{ID: s.nextID, CreatedAt: apitest.Now, UpdatedAt: apitest.Now}
	apply(r, p)
	s.schedules[r.ID] = r
	s.owner[r.ID] = serverID
	s.nextID++
	return s.copy(r), nil
}

func (s *store) Update(_ context.Context, _, id uint64, p schedules.Plan) (ScheduleRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updated = append(s.updated, p)
	if s.fail != nil {
		return ScheduleRecord{}, s.fail
	}
	r := s.schedules[id]
	apply(r, p)
	return s.copy(r), nil
}

func (s *store) Delete(_ context.Context, _, id uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, id)
	delete(s.schedules, id)
	return s.fail
}

func (s *store) BackupLimit(context.Context, uint64) (*int64, error) { return s.backup, s.fail }

func (s *store) AddTask(_ context.Context, id uint64, t schedules.Task, place Placement) (TaskRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.added = append(s.added, t)
	if s.fail != nil {
		return TaskRecord{}, s.fail
	}
	r := s.schedules[id]
	var existing []int
	for _, x := range r.Tasks {
		existing = append(existing, x.Sequence)
	}
	existing = append(existing, s.hidden...)
	seq, shift, err := place(existing)
	if err != nil {
		return TaskRecord{}, err
	}
	if shift {
		for i := range r.Tasks {
			if r.Tasks[i].Sequence >= seq {
				r.Tasks[i].Sequence++
			}
		}
	}
	rec := TaskRecord{ID: s.nextTask, Sequence: seq, Action: t.Action, Payload: t.Payload, TimeOffset: t.TimeOffset, ContinueOnFailure: t.ContinueOnFailure, CreatedAt: apitest.Now, UpdatedAt: apitest.Now}
	s.nextTask++
	r.Tasks = append(r.Tasks, rec)
	return rec, nil
}

func (s *store) ChangeTask(_ context.Context, id, taskID uint64, c TaskChange) (TaskRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.changed = append(s.changed, c)
	if s.fail != nil {
		return TaskRecord{}, s.fail
	}
	r := s.schedules[id]
	var cur *TaskRecord
	for i := range r.Tasks {
		if r.Tasks[i].ID == taskID {
			cur = &r.Tasks[i]
		}
	}
	if cur == nil {
		return TaskRecord{}, ErrNotFound
	}
	for i := range r.Tasks {
		x := &r.Tasks[i]
		switch {
		case c.Sequence < cur.Sequence && x.Sequence >= c.Sequence && x.Sequence < cur.Sequence:
			x.Sequence++
		case c.Sequence > cur.Sequence && x.Sequence > cur.Sequence && x.Sequence <= c.Sequence:
			x.Sequence--
		}
	}
	cur.Sequence, cur.Action, cur.Payload, cur.TimeOffset, cur.ContinueOnFailure = c.Sequence, c.Action, c.Payload, c.TimeOffset, c.ContinueOnFailure
	return *cur, nil
}

func (s *store) DeleteTask(_ context.Context, id, taskID uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removed = append(s.removed, taskID)
	if s.fail != nil {
		return s.fail
	}
	r := s.schedules[id]
	var seq int
	var kept []TaskRecord
	for _, x := range r.Tasks {
		if x.ID == taskID {
			seq = x.Sequence
			continue
		}
		kept = append(kept, x)
	}
	for i := range kept {
		if kept[i].Sequence > seq {
			kept[i].Sequence--
		}
	}
	r.Tasks = kept
	return nil
}

type starter struct {
	mu    sync.Mutex
	calls []uint64
	err   error
}

func (s *starter) Start(id uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, id)
	return s.err
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
	store    *store
	starter  *starter
	activity *activityLog
	logs     *bytes.Buffer
	handlers *Handlers
}

func newRig(t *testing.T, opts ...func(*Deps)) *rig {
	t.Helper()
	r := &rig{t: t, starter: &starter{}, activity: &activityLog{}, logs: &bytes.Buffer{}}
	r.store = &store{schedules: map[uint64]*ScheduleRecord{}, owner: map[uint64]uint64{}, nextID: 3, nextTask: 10}
	created := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	next := apitest.Now.Add(15 * time.Hour)
	last := apitest.Now.Add(-9 * time.Hour)
	r.store.schedules[1] = &ScheduleRecord{
		ID: 1, Name: "Nightly", Minute: "0", Hour: "3", DayOfMonth: "*", Month: "*", DayOfWeek: "*", Active: true, LastRunAt: &last, NextRunAt: &next,
		CreatedAt: created, UpdatedAt: created,
		Tasks: []TaskRecord{
			{ID: 1, Sequence: 1, Action: "command", Payload: "say backup soon", TimeOffset: 0, CreatedAt: created, UpdatedAt: created},
			{ID: 2, Sequence: 2, Action: "power", Payload: "restart", TimeOffset: 30, Queued: true, ContinueOnFailure: true, CreatedAt: created, UpdatedAt: created},
		},
	}
	r.store.owner[1] = 100
	// A schedule of another server.
	r.store.schedules[2] = &ScheduleRecord{ID: 2, Name: "Elsewhere", Minute: "*", Hour: "*", DayOfMonth: "*", Month: "*", DayOfWeek: "*", CreatedAt: created, UpdatedAt: created}
	r.store.owner[2] = 200

	w := apitest.NewWorld()
	for id, admin := range map[uint64]bool{ada: false, grace: true, bob: false, cleo: false, dave: false, eve: false} {
		w.AddUser(id, admin)
	}
	w.AddServer(100, serverUUID, ada)
	w.AddServer(101, "bbbb2222-2222-4333-8444-555555555555", ada)
	w.Subusers[[2]uint64{100, bob}] = []string{"schedule.read", "schedule.create", "schedule.update", "schedule.delete", "control.console", "control.start", "control.stop", "control.restart", "backup.create", "websocket.connect"}
	w.Subusers[[2]uint64{100, cleo}] = []string{"schedule.read", "websocket.connect"}
	w.Subusers[[2]uint64{100, dave}] = []string{"schedule.read", "schedule.update", "websocket.connect"}

	limiter, err := middleware.NewLimiter(CreateLimit, CreateWindow)
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{
		Store: r.store, Starter: r.starter, Activity: r.activity, CreateLimiter: limiter,
		Logger: slog.New(slog.NewTextHandler(r.logs, nil)), Now: func() time.Time { return apitest.Now },
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

const base = "/api/client/servers/aaaa1111/schedules"

func (r *rig) do(user uint64, method, path, body string) apitest.Reply {
	r.t.Helper()
	return r.client.As(user, method, base+path, body)
}

func scheduleJSON(name, minute, hour, dom, month, dow string, extra string) string {
	return fmt.Sprintf(`{"name":%q,"minute":%q,"hour":%q,"day_of_month":%q,"month":%q,"day_of_week":%q%s}`, name, minute, hour, dom, month, dow, extra)
}

func TestListAndView(t *testing.T) {
	r := newRig(t)

	rp := r.do(ada, "GET", "", "")
	if rp.Status != 200 || rp.At("object") != "list" {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if l, _ := rp.At("data").([]any); len(l) != 1 {
		t.Fatalf("%d schedules (another server's must not show): %s", len(l), rp.Raw)
	}
	want := map[string]any{
		"id": 1.0, "name": "Nightly",
		"cron":      map[string]any{"day_of_week": "*", "day_of_month": "*", "month": "*", "hour": "3", "minute": "0"},
		"is_active": true, "is_processing": false, "only_when_online": false,
		"last_run_at": "2026-10-23T03:00:00+00:00", "next_run_at": "2026-10-24T03:00:00+00:00",
		"created_at": "2026-10-01T08:00:00+00:00", "updated_at": "2026-10-01T08:00:00+00:00",
	}
	if rp.At("data", 0, "object") != "server_schedule" || !reflect.DeepEqual(rp.At("data", 0, "attributes"), want) {
		t.Errorf("schedule = %v", rp.At("data", 0))
	}
	wantTask := map[string]any{
		"id": 2.0, "sequence_id": 2.0, "action": "power", "payload": "restart", "time_offset": 30.0, "is_queued": true, "continue_on_failure": true,
		"created_at": "2026-10-01T08:00:00+00:00", "updated_at": "2026-10-01T08:00:00+00:00",
	}
	if rp.At("data", 0, "relationships", "tasks", "object") != "list" || rp.At("data", 0, "relationships", "tasks", "data", 1, "object") != "schedule_task" ||
		!reflect.DeepEqual(rp.At("data", 0, "relationships", "tasks", "data", 1, "attributes"), wantTask) {
		t.Errorf("tasks = %v", rp.At("data", 0, "relationships", "tasks"))
	}

	one := r.do(ada, "GET", "/1", "")
	if one.Status != 200 || one.At("object") != "server_schedule" || one.At("attributes", "name") != "Nightly" {
		t.Errorf("view = %d %s", one.Status, one.Raw)
	}
	for _, path := range []string{"/2", "/99", "/abc", "/0"} {
		if rp := r.do(ada, "GET", path, ""); rp.Status != 404 {
			t.Errorf("%s: status = %d", path, rp.Status)
		}
	}
}

func TestEveryRouteHasItsPermission(t *testing.T) {
	routes := []struct {
		name, method, path, body string
		allowed                  []uint64
	}{
		{"list", "GET", "", "", []uint64{bob, cleo, dave}},
		{"view", "GET", "/1", "", []uint64{bob, cleo, dave}},
		{"create", "POST", "", scheduleJSON("X", "0", "4", "*", "*", "*", ""), []uint64{bob}},
		{"update", "POST", "/1", scheduleJSON("X", "0", "4", "*", "*", "*", ""), []uint64{bob, dave}},
		{"execute", "POST", "/1/execute", "", []uint64{bob}},
		{"delete", "DELETE", "/1", "", []uint64{bob}},
		{"add task", "POST", "/1/tasks", `{"action":"backup","time_offset":0}`, []uint64{bob, dave}},
		{"change task", "POST", "/1/tasks/1", `{"action":"backup","time_offset":0}`, []uint64{bob, dave}},
		{"delete task", "DELETE", "/1/tasks/1", "", []uint64{bob, dave}},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			for _, user := range []uint64{ada, grace, bob, cleo, dave} {
				r := newRig(t)
				rp := r.do(user, rt.method, rt.path, rt.body)
				open := user == ada || user == grace || slices.Contains(rt.allowed, user)
				// Dave may update schedules but may not run or add what needs rights he does not hold.
				if rt.name == "add task" || rt.name == "change task" {
					open = open && user != dave
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

func TestCreate(t *testing.T) {
	r := newRig(t)
	rp := r.do(ada, "POST", "", scheduleJSON("Weekly", "30", "4", "*", "*", "1", ""))
	if rp.Status != 200 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	// Next Monday 04:30 after Friday 2026-10-23 12:00.
	if rp.At("attributes", "next_run_at") != "2026-10-26T04:30:00+00:00" || rp.At("attributes", "name") != "Weekly" || rp.At("attributes", "id") != 3.0 {
		t.Errorf("answer = %s", rp.Raw)
	}
	// Flags that are not sent are off: the schedule does not run until it is switched on.
	if rp.At("attributes", "is_active") != false || rp.At("attributes", "only_when_online") != false {
		t.Errorf("flags = %v %v", rp.At("attributes", "is_active"), rp.At("attributes", "only_when_online"))
	}
	if rp.At("relationships", "tasks", "object") != "list" || rp.At("relationships", "tasks", "data") == nil {
		t.Errorf("tasks = %v", rp.At("relationships"))
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:schedule.create" || r.activity.entries[0].Properties != `{"name":"Weekly"}` ||
		r.activity.entries[0].ScheduleID != 3 || r.activity.entries[0].ServerID != 100 || r.activity.entries[0].TaskID != 0 {
		t.Errorf("entries = %+v", r.activity.entries)
	}
}

func TestCreateFlags(t *testing.T) {
	tests := []struct {
		name         string
		extra        string
		status       int
		active, only bool
		detail       string
	}{
		{"both on", `,"is_active":true,"only_when_online":true`, 200, true, true, ""},
		{"as numbers", `,"is_active":1,"only_when_online":"0"`, 200, true, false, ""},
		{"online left null is off", `,"is_active":true,"only_when_online":null`, 200, true, false, ""},
		{"active null", `,"is_active":null`, 422, false, false, "The is active field is required."},
		{"active not a flag", `,"is_active":"yes"`, 422, false, false, "The is active field must be true or false."},
		{"online not a flag", `,"only_when_online":"maybe"`, 422, false, false, "The only when online field must be true or false."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(ada, "POST", "", scheduleJSON("S", "0", "4", "*", "*", "*", tt.extra))
			if rp.Status != tt.status {
				t.Fatalf("status = %d, want %d: %s", rp.Status, tt.status, rp.Raw)
			}
			if tt.status == 200 && (rp.At("attributes", "is_active") != tt.active || rp.At("attributes", "only_when_online") != tt.only) {
				t.Errorf("answer = %s", rp.Raw)
			}
			if tt.detail != "" && rp.Detail() != tt.detail {
				t.Errorf("detail = %q", rp.Detail())
			}
		})
	}
}

func TestCreateRefusals(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		status int
		detail string
	}{
		{"no name", `{"minute":"0","hour":"4","day_of_month":"*","month":"*","day_of_week":"*"}`, 422, "The name field is required."},
		{"a name too long", scheduleJSON(strings.Repeat("a", 192), "0", "4", "*", "*", "*", ""), 422, "The name must be at most 191 characters."},
		{"a name of control characters", `{"name":"a\u0000b","minute":"0","hour":"4","day_of_month":"*","month":"*","day_of_week":"*"}`, 422, "The name must not contain control characters."},
		{"no minute", `{"name":"S","hour":"4","day_of_month":"*","month":"*","day_of_week":"*"}`, 422, "The minute field is required."},
		{"no month", `{"name":"S","minute":"0","hour":"4","day_of_month":"*","day_of_week":"*"}`, 422, "The month field is required."},
		{"a minute that is a number", `{"name":"S","minute":0,"hour":"4","day_of_month":"*","month":"*","day_of_week":"*"}`, 422, "The minute must be a string."},
		{"an expression that never runs", scheduleJSON("S", "0", "4", "31", "2", "*", ""), 400, "The cron data provided does not evaluate to a valid expression."},
		{"an expression that is wrong", scheduleJSON("S", "61", "4", "*", "*", "*", ""), 400, "The cron data provided does not evaluate to a valid expression."},
		{"not JSON", `name=S`, 400, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(ada, "POST", "", tt.body)
			if rp.Status != tt.status || (tt.detail != "" && rp.Detail() != tt.detail) {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if len(r.store.created) != 0 || len(r.activity.entries) != 0 {
				t.Error("a schedule was stored or logged")
			}
		})
	}
}

func TestCreateIsThrottledForEachServer(t *testing.T) {
	r := newRig(t)
	body := scheduleJSON("S", "0", "4", "*", "*", "*", "")
	for i := 0; i < CreateLimit; i++ {
		if rp := r.do(ada, "POST", "", body); rp.Status != 200 {
			t.Fatalf("creation %d: status = %d", i+1, rp.Status)
		}
	}
	if rp := r.do(ada, "POST", "", body); rp.Status != 429 || rp.Header.Get("Retry-After") == "" {
		t.Errorf("the third: %d, Retry-After %q", rp.Status, rp.Header.Get("Retry-After"))
	}
	if rp := r.client.As(ada, "POST", "/api/client/servers/bbbb2222/schedules", body); rp.Status != 200 {
		t.Errorf("another server: status = %d: %s", rp.Status, rp.Raw)
	}
	// Refused calls do not use the allowance up.
	r = newRig(t)
	for i := 0; i < 10; i++ {
		r.do(eve, "POST", "", body)
		r.do(cleo, "POST", "", body)
	}
	if rp := r.do(ada, "POST", "", body); rp.Status != 200 {
		t.Errorf("status = %d", rp.Status)
	}
}

func TestUpdate(t *testing.T) {
	r := newRig(t)
	rp := r.do(ada, "POST", "/1", scheduleJSON("Renamed", "15", "5", "*", "*", "*", `,"is_active":false`))
	if rp.Status != 200 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if rp.At("attributes", "name") != "Renamed" || rp.At("attributes", "is_active") != false || rp.At("attributes", "next_run_at") != "2026-10-24T05:15:00+00:00" {
		t.Errorf("answer = %s", rp.Raw)
	}
	if len(r.store.updated) != 1 || !r.store.updated[0].ResetProcessing || r.store.updated[0].Active {
		t.Errorf("plan = %+v: switching the schedule off clears a stuck running mark", r.store.updated)
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:schedule.update" || r.activity.entries[0].Properties != `{"active":false,"name":"Renamed"}` {
		t.Errorf("entries = %+v", r.activity.entries)
	}
}

func TestUpdateKeepsTheFlagsThatAreNotSent(t *testing.T) {
	r := newRig(t)
	r.store.schedules[1].OnlyWhenOnline = true
	rp := r.do(ada, "POST", "/1", scheduleJSON("Nightly", "0", "3", "*", "*", "*", ""))
	if rp.Status != 200 || rp.At("attributes", "is_active") != true || rp.At("attributes", "only_when_online") != true {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if r.store.updated[0].ResetProcessing {
		t.Error("the running mark was cleared although the schedule stayed as it was")
	}
	if rp := r.do(ada, "POST", "/2", scheduleJSON("X", "0", "3", "*", "*", "*", "")); rp.Status != 404 {
		t.Errorf("another server's schedule: status = %d", rp.Status)
	}
}

func TestExecute(t *testing.T) {
	r := newRig(t)
	rp := r.do(ada, "POST", "/1/execute", "")
	if rp.Status != 202 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if !reflect.DeepEqual(r.starter.calls, []uint64{1}) {
		t.Errorf("started %v", r.starter.calls)
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:schedule.execute" || r.activity.entries[0].Properties != `{"name":"Nightly"}` {
		t.Errorf("entries = %+v", r.activity.entries)
	}
}

func TestExecuteNeedsTheRightsOfTheTasks(t *testing.T) {
	// Dave may update schedules, but the schedule restarts the server and runs a command: he holds neither right.
	r := newRig(t)
	rp := r.do(dave, "POST", "/1/execute", "")
	if rp.Status != 403 || len(r.starter.calls) != 0 {
		t.Errorf("answer = %d %s", rp.Status, rp.Raw)
	}
	// Bob holds them.
	if rp := r.do(bob, "POST", "/1/execute", ""); rp.Status != 202 {
		t.Errorf("bob: status = %d", rp.Status)
	}
}

func TestExecuteRefusals(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
	}{
		{"already running", schedules.ErrAlreadyRunning, 409},
		{"no tasks", schedules.ErrNoTasks, 400},
		{"a failure", errBoom, 500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			r.starter.err = tt.err
			rp := r.do(ada, "POST", "/1/execute", "")
			if rp.Status != tt.status || rp.Contains("hunter2") {
				t.Errorf("answer = %d %s", rp.Status, rp.Raw)
			}
			if len(r.activity.entries) != 0 {
				t.Error("an entry was written for a run that did not start")
			}
		})
	}
	r := newRig(t)
	if rp := r.do(ada, "POST", "/2/execute", ""); rp.Status != 404 || len(r.starter.calls) != 0 {
		t.Errorf("another server's schedule: status = %d", rp.Status)
	}
}

func TestDelete(t *testing.T) {
	r := newRig(t)
	if rp := r.do(ada, "DELETE", "/1", ""); rp.Status != 204 || len(rp.Raw) != 0 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if !reflect.DeepEqual(r.store.deleted, []uint64{1}) {
		t.Errorf("deleted %v", r.store.deleted)
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:schedule.delete" || r.activity.entries[0].Properties != `{"name":"Nightly"}` {
		t.Errorf("entries = %+v", r.activity.entries)
	}
	if rp := r.do(ada, "DELETE", "/2", ""); rp.Status != 404 || len(r.store.deleted) != 1 {
		t.Errorf("another server's schedule: status = %d", rp.Status)
	}
}

func taskBodyOf(action, payload string, offset int, extra string) string {
	return fmt.Sprintf(`{"action":%q,"payload":%q,"time_offset":%d%s}`, action, payload, offset, extra)
}

func sequences(r *rig) []int {
	var out []int
	rec := r.store.schedules[1]
	for _, t := range rec.Tasks {
		out = append(out, t.Sequence)
	}
	slices.Sort(out)
	return out
}

func TestAddTask(t *testing.T) {
	r := newRig(t)
	rp := r.do(ada, "POST", "/1/tasks", taskBodyOf("command", "say hi", 5, `,"continue_on_failure":true`))
	if rp.Status != 200 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	want := map[string]any{
		"id": 10.0, "sequence_id": 3.0, "action": "command", "payload": "say hi", "time_offset": 5.0, "is_queued": false, "continue_on_failure": true,
		"created_at": "2026-10-23T12:00:00+00:00", "updated_at": "2026-10-23T12:00:00+00:00",
	}
	if rp.At("object") != "schedule_task" || !reflect.DeepEqual(rp.At("attributes"), want) {
		t.Errorf("answer = %s", rp.Raw)
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:task.create" || r.activity.entries[0].TaskID != 10 || r.activity.entries[0].ScheduleID != 1 ||
		r.activity.entries[0].Properties != `{"action":"command","name":"Nightly","payload":"say hi"}` {
		t.Errorf("entries = %+v", r.activity.entries)
	}
}

func TestAddTaskPlacement(t *testing.T) {
	tests := []struct {
		name  string
		extra string
		seq   float64
		after []int
	}{
		{"last by default", ``, 3, []int{1, 2, 3}},
		{"in the middle", `,"sequence_id":1`, 1, []int{1, 2, 3}},
		{"past the end goes last", `,"sequence_id":9`, 3, []int{1, 2, 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(ada, "POST", "/1/tasks", taskBodyOf("command", "x", 0, tt.extra))
			if rp.Status != 200 || rp.At("attributes", "sequence_id") != tt.seq {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if got := sequences(r); !reflect.DeepEqual(got, tt.after) {
				t.Errorf("sequences = %v, want %v", got, tt.after)
			}
		})
	}
}

func TestAddTaskRefusals(t *testing.T) {
	tests := []struct {
		name   string
		user   uint64
		body   string
		status int
		detail string
	}{
		{"an action that is not one", ada, `{"action":"format","payload":"x","time_offset":0}`, 422, "The selected action is invalid."},
		{"an action that is not one, for a user without rights", dave, `{"action":"format","payload":"x","time_offset":0}`, 422, "The selected action is invalid."},
		{"no action", ada, `{"payload":"x","time_offset":0}`, 422, "The action field is required."},
		{"a command without a payload", ada, `{"action":"command","time_offset":0}`, 422, "The payload field is required unless action is in backup."},
		{"a blank payload", ada, `{"action":"command","payload":"  ","time_offset":0}`, 422, "The payload field is required unless action is in backup."},
		{"a power payload that is not a signal", ada, taskBodyOf("power", "reboot", 0, ""), 422, "The selected payload is invalid."},
		{"a payload that is a number", ada, `{"action":"command","payload":5,"time_offset":0}`, 422, "The payload must be a string."},
		{"a command too long", ada, taskBodyOf("command", strings.Repeat("a", 4097), 0, ""), 422, ""},
		{"no offset", ada, `{"action":"command","payload":"x"}`, 422, "The time offset field is required."},
		{"a negative offset", ada, taskBodyOf("command", "x", -1, ""), 422, "The time offset must be at least 0."},
		{"an offset too large", ada, taskBodyOf("command", "x", 901, ""), 422, "The time offset may not be greater than 900."},
		{"an offset that is not a number", ada, `{"action":"command","payload":"x","time_offset":"soon"}`, 422, "The time offset must be a number."},
		{"a position of zero", ada, taskBodyOf("command", "x", 0, `,"sequence_id":0`), 422, "The sequence id must be at least 1."},
		{"a position of null", ada, taskBodyOf("command", "x", 0, `,"sequence_id":null`), 422, "The sequence id field is required."},
		{"a flag of null", ada, taskBodyOf("command", "x", 0, `,"continue_on_failure":null`), 422, "The continue on failure field is required."},
		{"a flag that is not one", ada, taskBodyOf("command", "x", 0, `,"continue_on_failure":"yes"`), 422, "The continue on failure field must be true or false."},
		{"a power task without the right to the signal", dave, taskBodyOf("power", "start", 0, ""), 403, "This action is unauthorized."},
		{"a command without the console", dave, taskBodyOf("command", "x", 0, ""), 403, "This action is unauthorized."},
		{"not JSON", ada, `action=command`, 400, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(tt.user, "POST", "/1/tasks", tt.body)
			if rp.Status != tt.status || (tt.detail != "" && rp.Detail() != tt.detail) {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if len(r.store.added) != 0 || len(r.activity.entries) != 0 {
				t.Error("a task was stored or logged")
			}
		})
	}
	t.Run("a schedule of another server", func(t *testing.T) {
		r := newRig(t)
		if rp := r.do(ada, "POST", "/2/tasks", taskBodyOf("command", "x", 0, "")); rp.Status != 404 || len(r.store.added) != 0 {
			t.Errorf("status = %d", rp.Status)
		}
	})
}

func TestAddTaskActionsAndTheirRights(t *testing.T) {
	for _, body := range []string{
		taskBodyOf("command", "say hi", 0, ""), taskBodyOf("power", "kill", 0, ""), `{"action":"backup","time_offset":0}`,
		`{"action":"backup","payload":"*.log\ncache/","time_offset":0}`,
	} {
		r := newRig(t)
		if rp := r.do(bob, "POST", "/1/tasks", body); rp.Status != 200 {
			t.Errorf("%s: status = %d: %s", body, rp.Status, rp.Raw)
		}
	}
}

func TestTheLimitOfTasks(t *testing.T) {
	r := newRig(t, func(d *Deps) { d.TaskLimit = 2 })
	rp := r.do(ada, "POST", "/1/tasks", taskBodyOf("command", "x", 0, ""))
	if rp.Status != 400 || rp.Code() != "DisplayException" ||
		rp.Detail() != "Schedules may not have more than 2 tasks associated with them. Creating this task would put this schedule over the limit." {
		t.Errorf("answer = %d %s", rp.Status, rp.Raw)
	}
	if len(r.store.added) != 0 {
		t.Error("a task was added over the limit")
	}

	// The count is made again under the lock, where two requests at once meet.
	r = newRig(t, func(d *Deps) { d.TaskLimit = 3 })
	r.store.schedules[1].Tasks = r.store.schedules[1].Tasks[:1]
	r.store.fail = nil
	rp = r.do(ada, "POST", "/1/tasks", taskBodyOf("command", "x", 0, ""))
	if rp.Status != 200 {
		t.Fatalf("status = %d", rp.Status)
	}
}

func TestBackupTasksNeedABackupLimit(t *testing.T) {
	zero := int64(0)
	five := int64(5)
	tests := []struct {
		name   string
		limit  *int64
		action string
		status int
	}{
		{"a limit of nothing", &zero, "backup", 403},
		{"a limit of nothing, another action", &zero, "command", 200},
		{"a limit", &five, "backup", 200},
		{"no limit", nil, "backup", 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			r.store.backup = tt.limit
			body := taskBodyOf(tt.action, "say hi", 0, "")
			if tt.action == "backup" {
				body = `{"action":"backup","time_offset":0}`
			}
			rp := r.do(ada, "POST", "/1/tasks", body)
			if rp.Status != tt.status {
				t.Fatalf("add: status = %d, want %d: %s", rp.Status, tt.status, rp.Raw)
			}
			if tt.status == 403 && rp.Detail() != "A backup task cannot be created when the server's backup limit is set to 0." {
				t.Errorf("detail = %q", rp.Detail())
			}
			if rp := r.do(ada, "POST", "/1/tasks/1", body); rp.Status != tt.status {
				t.Errorf("change: status = %d, want %d", rp.Status, tt.status)
			}
		})
	}
}

func TestChangeTask(t *testing.T) {
	r := newRig(t)
	rp := r.do(ada, "POST", "/1/tasks/2", taskBodyOf("command", "say hello", 10, `,"sequence_id":1,"continue_on_failure":true`))
	if rp.Status != 200 || rp.At("attributes", "action") != "command" || rp.At("attributes", "payload") != "say hello" || rp.At("attributes", "sequence_id") != 1.0 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	// The task moved up and the one that was first moved down.
	got := map[uint64]int{}
	for _, tk := range r.store.schedules[1].Tasks {
		got[tk.ID] = tk.Sequence
	}
	if !reflect.DeepEqual(got, map[uint64]int{1: 2, 2: 1}) {
		t.Errorf("sequences = %v", got)
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:task.update" || r.activity.entries[0].TaskID != 2 ||
		r.activity.entries[0].Properties != `{"action":"command","name":"Nightly","payload":"say hello"}` {
		t.Errorf("entries = %+v", r.activity.entries)
	}
}

func TestChangeTaskPosition(t *testing.T) {
	tests := []struct {
		name  string
		extra string
		want  map[uint64]int
	}{
		{"kept when not sent", ``, map[uint64]int{1: 1, 2: 2}},
		{"moved down", `,"sequence_id":2`, map[uint64]int{1: 2, 2: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			target := "/1/tasks/1"
			if strings.HasPrefix(tt.name, "kept") {
				target = "/1/tasks/2" // not the first, so that "stays where it is" differs from "goes first"
			}
			if rp := r.do(ada, "POST", target, taskBodyOf("command", "x", 0, tt.extra)); rp.Status != 200 {
				t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
			}
			got := map[uint64]int{}
			for _, tk := range r.store.schedules[1].Tasks {
				got[tk.ID] = tk.Sequence
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("sequences = %v, want %v", got, tt.want)
			}
		})
	}
	r := newRig(t)
	for _, path := range []string{"/1/tasks/99", "/1/tasks/abc", "/2/tasks/1", "/1/tasks/0"} {
		if rp := r.do(ada, "POST", path, taskBodyOf("command", "x", 0, "")); rp.Status != 404 {
			t.Errorf("%s: status = %d", path, rp.Status)
		}
	}
	if len(r.store.changed) != 0 {
		t.Error("a task was changed")
	}
}

func TestDeleteTask(t *testing.T) {
	r := newRig(t)
	if rp := r.do(ada, "DELETE", "/1/tasks/1", ""); rp.Status != 204 || len(rp.Raw) != 0 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if !reflect.DeepEqual(r.store.removed, []uint64{1}) || !reflect.DeepEqual(sequences(r), []int{1}) {
		t.Errorf("removed %v, sequences %v: the later task moves back", r.store.removed, sequences(r))
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:task.delete" || r.activity.entries[0].TaskID != 1 || r.activity.entries[0].Properties != `{"name":"Nightly"}` {
		t.Errorf("entries = %+v", r.activity.entries)
	}
	for _, path := range []string{"/1/tasks/99", "/2/tasks/1", "/1/tasks/abc"} {
		if rp := r.do(ada, "DELETE", path, ""); rp.Status != 404 {
			t.Errorf("%s: status = %d", path, rp.Status)
		}
	}
}

func TestStoreFailuresDoNotLeak(t *testing.T) {
	for name, call := range map[string]func(*rig) apitest.Reply{
		"list": func(r *rig) apitest.Reply { return r.do(ada, "GET", "", "") },
		"view": func(r *rig) apitest.Reply { return r.do(ada, "GET", "/1", "") },
		"create": func(r *rig) apitest.Reply {
			return r.do(ada, "POST", "", scheduleJSON("S", "0", "4", "*", "*", "*", ""))
		},
		"update": func(r *rig) apitest.Reply {
			return r.do(ada, "POST", "/1", scheduleJSON("S", "0", "4", "*", "*", "*", ""))
		},
		"execute":     func(r *rig) apitest.Reply { return r.do(ada, "POST", "/1/execute", "") },
		"delete":      func(r *rig) apitest.Reply { return r.do(ada, "DELETE", "/1", "") },
		"add task":    func(r *rig) apitest.Reply { return r.do(ada, "POST", "/1/tasks", taskBodyOf("command", "x", 0, "")) },
		"change task": func(r *rig) apitest.Reply { return r.do(ada, "POST", "/1/tasks/1", taskBodyOf("command", "x", 0, "")) },
		"delete task": func(r *rig) apitest.Reply { return r.do(ada, "DELETE", "/1/tasks/1", "") },
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
}

func TestAnEntryThatCannotBeStoredDoesNotUndoTheChange(t *testing.T) {
	r := newRig(t)
	r.activity.err = errBoom
	if rp := r.do(ada, "DELETE", "/1", ""); rp.Status != 204 || len(r.store.deleted) != 1 {
		t.Errorf("status = %d", rp.Status)
	}
	if !strings.Contains(r.logs.String(), "activity entry not stored") {
		t.Errorf("logs = %s", r.logs.String())
	}
}

func TestTimesAreInThePanelsZone(t *testing.T) {
	zone := time.FixedZone("CEST", 2*3600)
	r := newRig(t, func(d *Deps) { d.Location = zone })
	rp := r.do(ada, "GET", "/1", "")
	if rp.At("attributes", "created_at") != "2026-10-01T10:00:00+02:00" || rp.At("attributes", "next_run_at") != "2026-10-24T05:00:00+02:00" {
		t.Errorf("answer = %s", rp.Raw)
	}
	// The expression is read in that zone too: 04:30 there is 02:30 UTC.
	rp = r.do(ada, "POST", "", scheduleJSON("S", "30", "4", "*", "*", "*", ""))
	if rp.At("attributes", "next_run_at") != "2026-10-24T04:30:00+02:00" {
		t.Errorf("next_run_at = %v", rp.At("attributes", "next_run_at"))
	}
}

func TestHandlersRefuseWithoutTheirPrincipal(t *testing.T) {
	r := newRig(t)
	client := apitest.NewWorld().Serve(t, func(g api.Group) {
		b := g.Sub("/bare")
		b.GET("", r.handlers.List, api.Public())
		b.POST("", r.handlers.Create, api.Public())
		b.GET("/:schedule", r.handlers.View, api.Public())
		b.POST("/:schedule", r.handlers.Update, api.Public())
		b.POST("/:schedule/execute", r.handlers.Execute, api.Public())
		b.DELETE("/:schedule", r.handlers.Delete, api.Public())
		b.POST("/:schedule/tasks", r.handlers.AddTask, api.Public())
		b.POST("/:schedule/tasks/:task", r.handlers.ChangeTask, api.Public())
		b.DELETE("/:schedule/tasks/:task", r.handlers.DeleteTask, api.Public())
	})
	for _, rt := range []struct{ method, path string }{
		{"GET", "/bare"}, {"POST", "/bare"}, {"GET", "/bare/1"}, {"POST", "/bare/1"}, {"POST", "/bare/1/execute"}, {"DELETE", "/bare/1"},
		{"POST", "/bare/1/tasks"}, {"POST", "/bare/1/tasks/1"}, {"DELETE", "/bare/1/tasks/1"},
	} {
		if rp := client.As(0, rt.method, rt.path, `{}`); rp.Status != 403 {
			t.Errorf("%s %s: status = %d, want 403", rt.method, rt.path, rp.Status)
		}
	}
	if len(r.store.created)+len(r.store.deleted)+len(r.store.added)+len(r.starter.calls) != 0 {
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
		"no store": func(d *Deps) { d.Store = nil }, "no starter": func(d *Deps) { d.Starter = nil }, "no activity": func(d *Deps) { d.Activity = nil },
		"no limiter": func(d *Deps) { d.CreateLimiter = nil }, "no logger": func(d *Deps) { d.Logger = nil },
	} {
		d := full
		mutate(&d)
		if _, err := New(d); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// Two requests at once both read two tasks and both want the third place: the second counts again under the lock and loses.
func TestTheLimitIsCheckedAgainWhereTheTasksAreLocked(t *testing.T) {
	r := newRig(t, func(d *Deps) { d.TaskLimit = 3 })
	r.store.hidden = []int{3}
	rp := r.do(ada, "POST", "/1/tasks", taskBodyOf("command", "x", 0, ""))
	if rp.Status != 400 || rp.Code() != "DisplayException" || !strings.Contains(rp.Detail(), "may not have more than 3 tasks") {
		t.Errorf("answer = %d %s", rp.Status, rp.Raw)
	}
	if len(r.activity.entries) != 0 || len(r.store.schedules[1].Tasks) != 2 {
		t.Error("a task was added or logged")
	}
}
