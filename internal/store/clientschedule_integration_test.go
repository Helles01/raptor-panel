//go:build integration

package store_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api/scheduleapi"
	"github.com/Helles01/raptor-panel/internal/schedules"
)

var errTooMany = errors.New("too many tasks")

func plan(name string, next time.Time) schedules.Plan {
	return schedules.Plan{Name: name, Minute: "*/5", Hour: "*", DayOfMonth: "*", Month: "*", DayOfWeek: "*", Active: true, NextRunAt: next}
}

// last places a task after the others, as a request that names no position.
func last(existing []int) (int, bool, error) {
	seq, shift := schedules.PlaceTask(existing, nil)
	return seq, shift, nil
}

func first(existing []int) (int, bool, error) {
	one := 1
	seq, shift := schedules.PlaceTask(existing, &one)
	return seq, shift, nil
}

func task(action, payload string) schedules.Task {
	return schedules.Task{Action: action, Payload: payload, TimeOffset: 5, ContinueOnFailure: true}
}

// order is the actions of the schedule's tasks in the order of their sequence numbers, with the numbers.
func order(t *testing.T, st scheduleapi.Store, server, schedule uint64) (actions string, seqs []int) {
	t.Helper()
	rec, err := st.Find(context.Background(), server, schedule)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range rec.Tasks {
		actions += tk.Payload + " "
		seqs = append(seqs, tk.Sequence)
	}
	return actions, seqs
}

func TestOwnerSchedules(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.ClientSchedules()
	next := time.Date(2026, 11, 4, 10, 5, 0, 0, time.UTC)

	made, err := st.Create(r.ctx, 1, plan("Restart", next))
	if err != nil || made.ID == 0 || made.Name != "Restart" || made.Minute != "*/5" || made.Hour != "*" || !made.Active || made.Processing || made.OnlyWhenOnline ||
		made.LastRunAt != nil || made.NextRunAt == nil || !made.NextRunAt.Equal(next) || made.CreatedAt.IsZero() || len(made.Tasks) != 0 {
		t.Fatalf("created = %+v, %v", made, err)
	}
	if _, err := st.Create(r.ctx, 2, plan("Other", next)); err != nil {
		t.Fatal(err)
	}
	list, err := st.List(r.ctx, 1)
	if err != nil || len(list) != 1 || list[0].ID != made.ID {
		t.Errorf("list = %+v, %v", list, err)
	}
	if _, err := st.Find(r.ctx, 2, made.ID); !errors.Is(err, scheduleapi.ErrNotFound) {
		t.Errorf("a schedule of another server: %v", err)
	}
	if _, err := st.Create(r.ctx, 1<<40, plan("x", next)); !errors.Is(err, scheduleapi.ErrNotFound) {
		t.Errorf("server out of range: %v", err)
	}

	// Update: the fields; ResetProcessing clears the mark of a run that is stuck.
	r.exec("UPDATE schedules SET is_processing = 1 WHERE id = ?", made.ID)
	p := plan("Renamed", next.Add(time.Hour))
	p.Minute, p.Hour, p.Active, p.OnlyWhenOnline = "0", "4", false, true
	up, err := st.Update(r.ctx, 1, made.ID, p)
	if err != nil || up.Name != "Renamed" || up.Minute != "0" || up.Hour != "4" || up.Active || !up.OnlyWhenOnline || !up.Processing || !up.NextRunAt.Equal(next.Add(time.Hour)) {
		t.Fatalf("updated = %+v, %v", up, err)
	}
	p.ResetProcessing = true
	if up, _ := st.Update(r.ctx, 1, made.ID, p); up.Processing {
		t.Error("the running mark was not cleared")
	}
	if _, err := st.Update(r.ctx, 2, made.ID, p); !errors.Is(err, scheduleapi.ErrNotFound) {
		t.Errorf("update through another server: %v", err)
	}
	if _, err := st.Update(r.ctx, 1, 99, p); !errors.Is(err, scheduleapi.ErrNotFound) {
		t.Errorf("unknown schedule: %v", err)
	}
	if limit, err := st.BackupLimit(r.ctx, 1); err != nil || limit == nil || *limit != 0 {
		t.Errorf("backup limit = %v, %v", limit, err)
	}
	r.exec("UPDATE servers SET backup_limit = 3 WHERE id = 1")
	if limit, _ := st.BackupLimit(r.ctx, 1); limit == nil || *limit != 3 {
		t.Errorf("backup limit = %v", limit)
	}
	if _, err := st.BackupLimit(r.ctx, 99); !errors.Is(err, scheduleapi.ErrNotFound) {
		t.Errorf("server 99: %v", err)
	}

	// Delete: through the right server only; the tasks go with it.
	tk, err := st.AddTask(r.ctx, made.ID, task("command", "say hi"), last)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(r.ctx, 2, made.ID); !errors.Is(err, scheduleapi.ErrNotFound) {
		t.Errorf("delete through another server: %v", err)
	}
	if err := st.Delete(r.ctx, 1, made.ID); err != nil {
		t.Fatal(err)
	}
	if countWhere(r, "tasks", "id = "+strconv.FormatUint(tk.ID, 10)) != 0 {
		t.Error("the task of a deleted schedule is left")
	}
	if err := st.Delete(r.ctx, 1, made.ID); !errors.Is(err, scheduleapi.ErrNotFound) {
		t.Errorf("twice: %v", err)
	}
}

func TestOwnerScheduleTasks(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.ClientSchedules()
	sc, _ := st.Create(r.ctx, 1, plan("Tasks", time.Now().UTC()))
	other, _ := st.Create(r.ctx, 2, plan("Other", time.Now().UTC()))
	add := func(payload string, place scheduleapi.Placement) scheduleapi.TaskRecord {
		t.Helper()
		tk, err := st.AddTask(r.ctx, sc.ID, task("command", payload), place)
		if err != nil {
			t.Fatal(err)
		}
		return tk
	}

	a, b, c := add("a", last), add("b", last), add("c", last)
	if got, seqs := order(t, st, 1, sc.ID); got != "a b c " || !slices.Equal(seqs, []int{1, 2, 3}) {
		t.Fatalf("order = %q %v", got, seqs)
	}
	if a.Action != "command" || a.TimeOffset != 5 || !a.ContinueOnFailure || a.Queued || a.CreatedAt.IsZero() || a.Sequence != 1 {
		t.Errorf("task = %+v", a)
	}
	// A task at the first place moves the others on; the placement sees the numbers the schedule has, and its error ends the request.
	var seen []int
	d := add("d", func(existing []int) (int, bool, error) { seen = existing; return first(existing) })
	if got, seqs := order(t, st, 1, sc.ID); got != "d a b c " || !slices.Equal(seqs, []int{1, 2, 3, 4}) || !slices.Equal(seen, []int{1, 2, 3}) {
		t.Errorf("order after an insertion = %q %v (saw %v)", got, seqs, seen)
	}
	if _, err := st.AddTask(r.ctx, sc.ID, task("command", "no"), func([]int) (int, bool, error) { return 0, false, errTooMany }); !errors.Is(err, errTooMany) {
		t.Errorf("the placement's error: %v", err)
	}
	if _, err := st.AddTask(r.ctx, 99, task("command", "no"), last); !errors.Is(err, scheduleapi.ErrNotFound) {
		t.Errorf("unknown schedule: %v", err)
	}

	// Change: fields only; to the front; to the back; beyond the back (the last place); the places in between move one.
	ch := func(id uint64, payload string, seq int) scheduleapi.TaskRecord {
		t.Helper()
		got, err := st.ChangeTask(r.ctx, sc.ID, id, scheduleapi.TaskChange{Action: "power", Payload: payload, TimeOffset: 9, ContinueOnFailure: false, Sequence: seq})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := ch(c.ID, "C", 4); got.Action != "power" || got.Payload != "C" || got.TimeOffset != 9 || got.ContinueOnFailure || got.Sequence != 4 {
		t.Errorf("changed = %+v", got)
	}
	if got, seqs := order(t, st, 1, sc.ID); got != "d a b C " || !slices.Equal(seqs, []int{1, 2, 3, 4}) {
		t.Errorf("order after a change in place = %q %v", got, seqs)
	}
	ch(c.ID, "C", 1)
	if got, seqs := order(t, st, 1, sc.ID); got != "C d a b " || !slices.Equal(seqs, []int{1, 2, 3, 4}) {
		t.Errorf("order after a move to the front = %q %v", got, seqs)
	}
	ch(c.ID, "C", 99)
	if got, seqs := order(t, st, 1, sc.ID); got != "d a b C " || !slices.Equal(seqs, []int{1, 2, 3, 4}) {
		t.Errorf("order after a move beyond the end = %q %v", got, seqs)
	}
	ch(a.ID, "a", 3)
	if got, seqs := order(t, st, 1, sc.ID); got != "d b a C " || !slices.Equal(seqs, []int{1, 2, 3, 4}) {
		t.Errorf("order after a move in the middle = %q %v", got, seqs)
	}
	ch(d.ID, "d", 0)
	if got, _ := order(t, st, 1, sc.ID); got != "d b a C " {
		t.Errorf("a place below the first: %q", got)
	}
	if _, err := st.ChangeTask(r.ctx, sc.ID, 999, scheduleapi.TaskChange{Action: "power", Sequence: 1}); !errors.Is(err, scheduleapi.ErrNotFound) {
		t.Errorf("unknown task: %v", err)
	}
	if _, err := st.ChangeTask(r.ctx, other.ID, b.ID, scheduleapi.TaskChange{Action: "power", Sequence: 1}); !errors.Is(err, scheduleapi.ErrNotFound) {
		t.Errorf("a task of another schedule: %v", err)
	}

	// Delete: the later ones move one place back; a task of another schedule is not reached.
	if err := st.DeleteTask(r.ctx, other.ID, b.ID); !errors.Is(err, scheduleapi.ErrNotFound) {
		t.Errorf("a task of another schedule: %v", err)
	}
	if err := st.DeleteTask(r.ctx, sc.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if got, seqs := order(t, st, 1, sc.ID); got != "d a C " || !slices.Equal(seqs, []int{1, 2, 3}) {
		t.Errorf("order after a deletion = %q %v", got, seqs)
	}
	if err := st.DeleteTask(r.ctx, sc.ID, b.ID); !errors.Is(err, scheduleapi.ErrNotFound) {
		t.Errorf("twice: %v", err)
	}
}

// Of many requests at once to add a task to a schedule that may have five, five are made, with the places 1 to 5: the count is read under the lock.
func TestOwnerScheduleTasksUnderLoad(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.ClientSchedules()
	sc, _ := st.Create(r.ctx, 1, plan("Busy", time.Now().UTC()))
	wins := once(t, 14, func() (bool, error) {
		_, err := st.AddTask(r.ctx, sc.ID, task("command", "x"), func(existing []int) (int, bool, error) {
			if len(existing) >= 5 {
				return 0, false, errTooMany
			}
			return last(existing)
		})
		if errors.Is(err, errTooMany) {
			return false, nil
		}
		return err == nil, err
	})
	if _, seqs := order(t, st, 1, sc.ID); wins != 5 || !slices.Equal(seqs, []int{1, 2, 3, 4, 5}) {
		t.Errorf("%d tasks were made, with the places %v", wins, seqs)
	}
}

func TestScheduleRunnerDue(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.ClientSchedules()
	run := r.db.SchedulesStore()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	mk := func(server uint64, name string, next time.Time, tasks int) uint64 {
		t.Helper()
		sc, err := st.Create(r.ctx, server, plan(name, next))
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < tasks; i++ {
			if _, err := st.AddTask(r.ctx, sc.ID, task("command", name), last); err != nil {
				t.Fatal(err)
			}
		}
		return sc.ID
	}
	due := mk(1, "due", now.Add(-time.Minute), 2)
	mk(1, "exactly now", now, 1)
	mk(1, "future", now.Add(time.Minute), 1)
	mk(1, "no tasks", now.Add(-time.Minute), 0)
	off := mk(1, "off", now.Add(-time.Minute), 1)
	busy := mk(1, "running", now.Add(-time.Minute), 1)
	broken := mk(1, "broken", now.Add(-time.Minute), 1)
	mk(2, "suspended server", now.Add(-time.Minute), 1)
	r.exec("UPDATE schedules SET is_active = 0 WHERE id = ?", off)
	r.exec("UPDATE schedules SET is_processing = 1 WHERE id = ?", busy)
	r.exec("UPDATE schedules SET cron_minute = 'not a time' WHERE id = ?", broken)
	r.exec("UPDATE servers SET status = 'suspended' WHERE id = 2")
	r.exec("UPDATE schedules SET next_run_at = NULL WHERE name = 'future'")

	got, err := run.Due(r.ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range got {
		names = append(names, s.Name)
	}
	if !slices.Equal(names, []string{"due", "exactly now"}) {
		t.Fatalf("due = %v", names)
	}
	if s := got[0]; s.ID != due || s.ServerID != 1 || !s.Active || s.Processing || s.ServerStatus != "" || s.NextRunAt == nil || !s.NextRunAt.Equal(now.Add(-time.Minute)) {
		t.Errorf("schedule = %+v", s)
	}
	if n, err := got[0].Cron.Next(now); err != nil || !n.After(now) {
		t.Errorf("the expression of a schedule that is due: %v %v", n, err)
	}

	// One by its id, for a run by hand; a broken expression is an error and not a schedule; a missing one is an error.
	one, err := run.Schedule(r.ctx, due)
	if err != nil || one.Name != "due" || one.ServerID != 1 {
		t.Errorf("schedule = %+v, %v", one, err)
	}
	if _, err := run.Schedule(r.ctx, broken); err == nil {
		t.Error("a broken expression was read")
	}
	for _, id := range []uint64{999, 1 << 40} {
		if _, err := run.Schedule(r.ctx, id); err == nil {
			t.Errorf("schedule %d was found", id)
		}
	}
	tasks, err := run.Tasks(r.ctx, due)
	if err != nil || len(tasks) != 2 || tasks[0].Sequence != 1 || tasks[1].Sequence != 2 || tasks[0].Action != "command" || tasks[0].TimeOffset != 5 || !tasks[0].ContinueOnFailure || tasks[0].ID == 0 {
		t.Errorf("tasks = %+v, %v", tasks, err)
	}
}

func TestScheduleRunnerMarks(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.ClientSchedules()
	run := r.db.SchedulesStore()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	sc, _ := st.Create(r.ctx, 1, plan("Run", now.Add(-time.Minute)))
	st.AddTask(r.ctx, sc.ID, task("command", "one"), last)
	st.AddTask(r.ctx, sc.ID, task("command", "two"), last)
	s, err := run.Schedule(r.ctx, sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	tasks, _ := run.Tasks(r.ctx, sc.ID)
	next := now.Add(5 * time.Minute)

	// Of 12 starts at once, one takes the running mark and queues the first task.
	wins := once(t, 12, func() (bool, error) { return run.Begin(r.ctx, sc.ID, next, tasks[0]) })
	rec, _ := st.Find(r.ctx, 1, sc.ID)
	if wins != 1 || !rec.Processing || !rec.NextRunAt.Equal(next) || !rec.Tasks[0].Queued || rec.Tasks[1].Queued {
		t.Fatalf("%d starts, schedule %+v", wins, rec)
	}
	// A start of a schedule that runs is refused, also when it would change something (another next time), and changes nothing.
	if began, err := run.Begin(r.ctx, sc.ID, next.Add(time.Hour), tasks[0]); began || err != nil {
		t.Errorf("a start of a schedule that runs: %v %v", began, err)
	}
	if rec, _ := st.Find(r.ctx, 1, sc.ID); !rec.NextRunAt.Equal(next) {
		t.Errorf("a refused start changed the next time to %v", rec.NextRunAt)
	}

	// Moving on: the first task is dequeued and the second queued; progress reads the switch and the server's status; finishing clears the mark.
	if err := run.Dequeue(r.ctx, tasks[0]); err != nil {
		t.Fatal(err)
	}
	if err := run.Queue(r.ctx, tasks[1]); err != nil {
		t.Fatal(err)
	}
	if rec, _ := st.Find(r.ctx, 1, sc.ID); rec.Tasks[0].Queued || !rec.Tasks[1].Queued {
		t.Errorf("queued marks: %+v", rec.Tasks)
	}
	if active, status, err := run.Progress(r.ctx, s); err != nil || !active || status != "" {
		t.Errorf("progress = %v %q %v", active, status, err)
	}
	r.exec("UPDATE schedules SET is_active = 0 WHERE id = ?", sc.ID)
	r.exec("UPDATE servers SET status = 'suspended' WHERE id = 1")
	if active, status, err := run.Progress(r.ctx, s); err != nil || active || status != "suspended" {
		t.Errorf("progress = %v %q %v", active, status, err)
	}
	end := now.Add(time.Minute)
	if err := run.Finish(r.ctx, sc.ID, end); err != nil {
		t.Fatal(err)
	}
	if rec, _ := st.Find(r.ctx, 1, sc.ID); rec.Processing || rec.LastRunAt == nil || !rec.LastRunAt.Equal(end) {
		t.Errorf("after finishing: %+v", rec)
	}

	// Reset at start: every mark of every schedule and task goes.
	r.exec("UPDATE schedules SET is_processing = 1")
	r.exec("UPDATE tasks SET is_queued = 1")
	if err := run.ResetRunning(r.ctx); err != nil {
		t.Fatal(err)
	}
	if countWhere(r, "schedules", "is_processing = 1")+countWhere(r, "tasks", "is_queued = 1") != 0 {
		t.Error("a mark is left after the reset")
	}
}
