package schedules

import (
	"errors"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func everyMinute(t *testing.T) Cron {
	t.Helper()
	c, err := Parse("*", "*", "*", "*", "*")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return c
}

func ago(d time.Duration) *time.Time { t := t0.Add(-d); return &t }

func TestDue(t *testing.T) {
	base := Schedule{Active: true, NextRunAt: ago(time.Minute)}
	tests := []struct {
		name   string
		mutate func(*Schedule)
		tasks  int
		want   bool
	}{
		{"ready", func(*Schedule) {}, 1, true},
		{"due this very moment", func(s *Schedule) { s.NextRunAt = ago(0) }, 1, true},
		{"one second early", func(s *Schedule) { x := t0.Add(time.Second); s.NextRunAt = &x }, 1, false},
		{"switched off", func(s *Schedule) { s.Active = false }, 1, false},
		{"already running", func(s *Schedule) { s.Processing = true }, 1, false},
		{"server suspended", func(s *Schedule) { s.ServerStatus = "suspended" }, 1, false},
		{"server installing", func(s *Schedule) { s.ServerStatus = "installing" }, 1, false},
		{"no next time", func(s *Schedule) { s.NextRunAt = nil }, 1, false},
		{"no tasks", func(*Schedule) {}, 0, false},
		{"more tasks", func(*Schedule) {}, 5, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := base
			tt.mutate(&s)
			if got := Due(s, tt.tasks, t0); got != tt.want {
				t.Errorf("Due = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFirstAndNextTask(t *testing.T) {
	tasks := []Task{{ID: 3, Sequence: 3}, {ID: 1, Sequence: 1}, {ID: 7, Sequence: 7}, {ID: 2, Sequence: 2}}

	first, ok := First(tasks)
	if !ok || first.ID != 1 {
		t.Errorf("First = %+v, %v, want the task with the lowest sequence", first, ok)
	}
	if _, ok := First(nil); ok {
		t.Error("First of nothing must not find one")
	}

	tests := []struct {
		after  int
		wantID uint64
		wantOK bool
	}{
		{0, 1, true},
		{1, 2, true},
		{2, 3, true},
		{3, 7, true},
		{6, 7, true},
		{7, 0, false},
		{100, 0, false},
	}
	for _, tt := range tests {
		got, ok := NextTask(tasks, tt.after)
		if ok != tt.wantOK || got.ID != tt.wantID {
			t.Errorf("NextTask(after %d) = %+v, %v; want id %d, %v", tt.after, got, ok, tt.wantID, tt.wantOK)
		}
	}
	if _, ok := NextTask(nil, 0); ok {
		t.Error("NextTask of nothing must not find one")
	}
}

func TestPlanStart(t *testing.T) {
	s := Schedule{Cron: everyMinute(t)}

	got, err := PlanStart(s, []Task{{ID: 2, Sequence: 2}, {ID: 1, Sequence: 1}}, t0)
	if err != nil {
		t.Fatalf("PlanStart: %v", err)
	}

	if got.First.ID != 1 || !got.NextRunAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("got %+v, want task 1 first and the next run a minute from now", got)
	}
}

func TestPlanStartWorksTheNextTimeFromNow(t *testing.T) {
	c, err := Parse("30", "3", "*", "*", "*")
	if err != nil {
		t.Fatal(err)
	}

	got, err := PlanStart(Schedule{Cron: c}, []Task{{ID: 1, Sequence: 1}}, t0)

	want := time.Date(2026, 10, 24, 3, 30, 0, 0, time.UTC)
	if err != nil || !got.NextRunAt.Equal(want) {
		t.Errorf("NextRunAt = %v, %v; want %v", got.NextRunAt, err, want)
	}
}

func TestPlanStartWithoutTasks(t *testing.T) {
	_, err := PlanStart(Schedule{Cron: everyMinute(t)}, nil, t0)

	if !errors.Is(err, ErrNoTasks) {
		t.Errorf("error = %v, want ErrNoTasks", err)
	}
}

func TestPlanStartWithACronThatNeverRuns(t *testing.T) {
	c, err := Parse("0", "0", "31", "2", "*")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := PlanStart(Schedule{Cron: c}, []Task{{ID: 1, Sequence: 1}}, t0); err == nil {
		t.Error("a schedule with no next time must not start")
	}
}

func TestOnlineGate(t *testing.T) {
	tests := []struct {
		name      string
		only      bool
		state     string
		reachable bool
		want      bool
	}{
		{"not needed, offline", false, "offline", true, true},
		{"not needed, unreachable", false, "", false, true},
		{"running", true, "running", true, true},
		{"starting", true, "starting", true, true},
		{"offline", true, "offline", true, false},
		{"stopping", true, "stopping", true, false},
		{"said nothing counts as offline", true, "", true, false},
		{"unreachable", true, "running", false, false},
		{"unreachable and silent", true, "", false, false},
		{"another state", true, "something-new", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OnlineGate(tt.only, tt.state, tt.reachable); got != tt.want {
				t.Errorf("OnlineGate = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBeforeTask(t *testing.T) {
	tests := []struct {
		name   string
		active bool
		manual bool
		status string
		want   Gate
	}{
		{"on", true, false, "", GoOn},
		{"switched off meanwhile", false, false, "", EndInactive},
		{"switched off but started by hand", false, true, "", GoOn},
		{"suspended", true, false, "suspended", EndBusy},
		{"suspended and started by hand", true, true, "suspended", EndBusy},
		{"installing", true, false, "installing", EndBusy},
		{"off and suspended is off first", false, false, "suspended", EndInactive},
		{"off, by hand and suspended", false, true, "suspended", EndBusy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BeforeTask(tt.active, tt.manual, tt.status); got != tt.want {
				t.Errorf("BeforeTask = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestClassifyAndAfterTask(t *testing.T) {
	nodeErr := errors.Join(errors.New("503"), ErrNode)
	wrapped := errors.Join(errors.New("context"), nodeErr)

	tests := []struct {
		name      string
		err       error
		continue_ bool
		want      bool
		wantKind  Failure
	}{
		{"worked", nil, false, true, NoFailure},
		{"worked, continue set", nil, true, true, NoFailure},
		{"node failed", nodeErr, false, false, NodeFailure},
		{"node failed, continue set", nodeErr, true, true, NodeFailure},
		{"node failed, wrapped further", wrapped, true, true, NodeFailure},
		{"panel failed", errors.New("backup limit"), false, false, PanelFailure},
		{"panel failed, continue set", errors.New("backup limit"), true, false, PanelFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind := Classify(tt.err)
			if kind != tt.wantKind {
				t.Errorf("Classify = %d, want %d", kind, tt.wantKind)
			}
			if got := AfterTask(Task{ContinueOnFailure: tt.continue_}, kind); got != tt.want {
				t.Errorf("AfterTask = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestErrNoTasksKeepsTheReferencesText(t *testing.T) {
	if got := ErrNoTasks.Error(); got != "Cannot process schedule for task execution: no tasks are registered." {
		t.Errorf("ErrNoTasks = %q", got)
	}
}
