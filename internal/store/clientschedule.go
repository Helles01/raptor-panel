package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/scheduleapi"
	"github.com/Helles01/raptor-panel/internal/db/dbq"
	"github.com/Helles01/raptor-panel/internal/schedules"
)

// ClientSchedules is the schedule store of the client API.
func (d *DB) ClientSchedules() scheduleapi.Store { return clientSchedules{d} }

// SchedulesStore is what the scheduler reads and marks while it runs schedules.
func (d *DB) SchedulesStore() schedules.Store { return scheduleRunner{d} }

type (
	clientSchedules struct{ *DB }
	scheduleRunner  struct{ *DB }
)

const scheduleSelect = "SELECT `id`, `name`, `cron_minute`, `cron_hour`, `cron_day_of_month`, `cron_month`, `cron_day_of_week`, `is_active`, `is_processing`, `only_when_online`, " +
	"`last_run_at`, `next_run_at`, `created_at`, `updated_at` FROM `schedules`"

func scanSchedule(r scanner) (scheduleapi.ScheduleRecord, error) {
	var (
		s                         scheduleapi.ScheduleRecord
		only                      uint8
		lastRun, nextRun, cr, upd sql.NullTime
	)
	if err := r.Scan(&s.ID, &s.Name, &s.Minute, &s.Hour, &s.DayOfMonth, &s.Month, &s.DayOfWeek, &s.Active, &s.Processing, &only, &lastRun, &nextRun, &cr, &upd); err != nil {
		return s, err
	}
	s.OnlyWhenOnline, s.LastRunAt, s.NextRunAt, s.CreatedAt, s.UpdatedAt = only != 0, timePtr(lastRun), timePtr(nextRun), at(cr), at(upd)
	return s, nil
}

const taskSelect = "SELECT `id`, `sequence_id`, `action`, `payload`, `time_offset`, `is_queued`, `continue_on_failure`, `created_at`, `updated_at` FROM `tasks`"

func scanTask(r scanner) (scheduleapi.TaskRecord, error) {
	var (
		t       scheduleapi.TaskRecord
		cont    uint8
		cr, upd sql.NullTime
	)
	if err := r.Scan(&t.ID, &t.Sequence, &t.Action, &t.Payload, &t.TimeOffset, &t.Queued, &cont, &cr, &upd); err != nil {
		return t, err
	}
	t.ContinueOnFailure, t.CreatedAt, t.UpdatedAt = cont != 0, at(cr), at(upd)
	return t, nil
}

// withTasks fills the tasks of the schedules, in their order, with one query.
func (s clientSchedules) withTasks(ctx context.Context, list []scheduleapi.ScheduleRecord) error {
	if len(list) == 0 {
		return nil
	}
	marks := make([]string, len(list))
	args := make([]any, len(list))
	index := map[uint64]int{}
	for i, sc := range list {
		marks[i], args[i], index[sc.ID] = "?", sc.ID, i
	}
	rows, err := s.sql.QueryContext(ctx, "SELECT `schedule_id`, `id`, `sequence_id`, `action`, `payload`, `time_offset`, `is_queued`, `continue_on_failure`, `created_at`, `updated_at` FROM `tasks` "+
		"WHERE `schedule_id` IN ("+strings.Join(marks, ",")+") ORDER BY `schedule_id`, `sequence_id`, `id`", args...)
	if err != nil {
		return fmt.Errorf("read the tasks: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			schedule uint64
			t        scheduleapi.TaskRecord
			cont     uint8
			cr, upd  sql.NullTime
		)
		if err := rows.Scan(&schedule, &t.ID, &t.Sequence, &t.Action, &t.Payload, &t.TimeOffset, &t.Queued, &cont, &cr, &upd); err != nil {
			return fmt.Errorf("read a task: %w", err)
		}
		t.ContinueOnFailure, t.CreatedAt, t.UpdatedAt = cont != 0, timeOf(cr), timeOf(upd)
		i := index[schedule]
		list[i].Tasks = append(list[i].Tasks, t)
	}
	return rows.Err()
}

func (s clientSchedules) List(ctx context.Context, serverID uint64) ([]scheduleapi.ScheduleRecord, error) {
	list, err := all(ctx, s.DB, scanSchedule, scheduleSelect+" WHERE `server_id` = ? ORDER BY `id`", serverID)
	if err != nil {
		return nil, fmt.Errorf("read the schedules of server %d: %w", serverID, err)
	}
	return list, s.withTasks(ctx, list)
}

func (s clientSchedules) Find(ctx context.Context, serverID, scheduleID uint64) (scheduleapi.ScheduleRecord, error) {
	rec, err := one(ctx, s.DB, scanSchedule, scheduleSelect+" WHERE `id` = ? AND `server_id` = ?", scheduleID, serverID)
	if errors.Is(err, appkit.ErrNotFound) {
		return rec, scheduleapi.ErrNotFound
	}
	if err != nil {
		return rec, err
	}
	list := []scheduleapi.ScheduleRecord{rec}
	if err := s.withTasks(ctx, list); err != nil {
		return rec, err
	}
	return list[0], nil
}

func (s clientSchedules) Create(ctx context.Context, serverID uint64, p schedules.Plan) (scheduleapi.ScheduleRecord, error) {
	if !fits(serverID) {
		return scheduleapi.ScheduleRecord{}, scheduleapi.ErrNotFound
	}
	now := sql.NullTime{Time: s.now().UTC(), Valid: true}
	id, err := s.q.InsertSchedule(ctx, dbq.InsertScheduleParams{
		ServerID: uint32(serverID), Name: p.Name, CronDayOfWeek: p.DayOfWeek, CronMonth: p.Month, CronDayOfMonth: p.DayOfMonth, CronHour: p.Hour, CronMinute: p.Minute,
		IsActive: p.Active, OnlyWhenOnline: boolByte(p.OnlyWhenOnline), NextRunAt: sql.NullTime{Time: p.NextRunAt.UTC(), Valid: true}, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return scheduleapi.ScheduleRecord{}, fmt.Errorf("store the schedule: %w", err)
	}
	return s.Find(ctx, serverID, uint64(id))
}

// Update stores the plan with the schedule's row locked; with ResetProcessing the running mark goes too.
func (s clientSchedules) Update(ctx context.Context, serverID, scheduleID uint64, p schedules.Plan) (scheduleapi.ScheduleRecord, error) {
	if !fits(serverID, scheduleID) {
		return scheduleapi.ScheduleRecord{}, scheduleapi.ErrNotFound
	}
	err := s.inTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.LockScheduleOfServer(ctx, dbq.LockScheduleOfServerParams{ID: uint32(scheduleID), ServerID: uint32(serverID)}); errors.Is(err, sql.ErrNoRows) {
			return scheduleapi.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock schedule %d: %w", scheduleID, err)
		}
		err := q.UpdateSchedule(ctx, dbq.UpdateScheduleParams{
			Name: p.Name, CronDayOfWeek: p.DayOfWeek, CronMonth: p.Month, CronDayOfMonth: p.DayOfMonth, CronHour: p.Hour, CronMinute: p.Minute, IsActive: p.Active,
			OnlyWhenOnline: boolByte(p.OnlyWhenOnline), NextRunAt: sql.NullTime{Time: p.NextRunAt.UTC(), Valid: true}, UpdatedAt: sql.NullTime{Time: s.now().UTC(), Valid: true}, ID: uint32(scheduleID),
		})
		if err != nil {
			return fmt.Errorf("store the schedule: %w", err)
		}
		if p.ResetProcessing {
			return q.ClearScheduleProcessing(ctx, uint32(scheduleID))
		}
		return nil
	})
	if err != nil {
		return scheduleapi.ScheduleRecord{}, err
	}
	return s.Find(ctx, serverID, scheduleID)
}

func (s clientSchedules) Delete(ctx context.Context, serverID, scheduleID uint64) error {
	if !fits(serverID, scheduleID) {
		return scheduleapi.ErrNotFound
	}
	n, err := s.q.DeleteScheduleOfServer(ctx, dbq.DeleteScheduleOfServerParams{ID: uint32(scheduleID), ServerID: uint32(serverID)})
	if err != nil {
		return fmt.Errorf("delete schedule %d: %w", scheduleID, err)
	}
	if n == 0 {
		return scheduleapi.ErrNotFound
	}
	return nil
}

// BackupLimit is the limit of backups; the column holds 0 for none, which the rules of backups read as no backup allowed.
func (s clientSchedules) BackupLimit(ctx context.Context, serverID uint64) (*int64, error) {
	if !fits(serverID) {
		return nil, scheduleapi.ErrNotFound
	}
	n, err := s.q.ServerBackupLimitOf(ctx, uint32(serverID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, scheduleapi.ErrNotFound
	} else if err != nil {
		return nil, fmt.Errorf("read server %d: %w", serverID, err)
	}
	v := int64(n)
	return &v, nil
}

// lockTasks locks the schedule and then its tasks (in this order everywhere, so that two changes of one schedule take turns) and returns the tasks' places.
func (s clientSchedules) lockTasks(ctx context.Context, q *dbq.Queries, scheduleID uint64) ([]dbq.LockTasksOfScheduleRow, error) {
	if _, err := q.LockScheduleRow(ctx, uint32(scheduleID)); errors.Is(err, sql.ErrNoRows) {
		return nil, scheduleapi.ErrNotFound
	} else if err != nil {
		return nil, fmt.Errorf("lock schedule %d: %w", scheduleID, err)
	}
	rows, err := q.LockTasksOfSchedule(ctx, uint32(scheduleID))
	if err != nil {
		return nil, fmt.Errorf("lock the tasks of schedule %d: %w", scheduleID, err)
	}
	return rows, nil
}

func (s clientSchedules) task(ctx context.Context, id uint64) (scheduleapi.TaskRecord, error) {
	t, err := one(ctx, s.DB, scanTask, taskSelect+" WHERE `id` = ?", id)
	if errors.Is(err, appkit.ErrNotFound) {
		return t, scheduleapi.ErrNotFound
	}
	return t, err
}

// AddTask reads the places the tasks have with the schedule and its tasks locked, lets place decide, moves the later tasks if it says so, and inserts the task.
func (s clientSchedules) AddTask(ctx context.Context, scheduleID uint64, t schedules.Task, place scheduleapi.Placement) (scheduleapi.TaskRecord, error) {
	if !fits(scheduleID) {
		return scheduleapi.TaskRecord{}, scheduleapi.ErrNotFound
	}
	var id uint64
	err := appUsers{s.DB}.retryDeadlock(func() error {
		return s.inTx(ctx, func(q *dbq.Queries) error {
			rows, err := s.lockTasks(ctx, q, scheduleID)
			if err != nil {
				return err
			}
			existing := make([]int, len(rows))
			for i, r := range rows {
				existing[i] = int(r.SequenceID)
			}
			seq, shift, err := place(existing)
			if err != nil {
				return err
			}
			if shift {
				if err := q.MakeRoomForTask(ctx, dbq.MakeRoomForTaskParams{ScheduleID: uint32(scheduleID), SequenceID: uint32(seq)}); err != nil {
					return fmt.Errorf("make room for the task: %w", err)
				}
			}
			now := sql.NullTime{Time: s.now().UTC(), Valid: true}
			made, err := q.InsertTask(ctx, dbq.InsertTaskParams{
				ScheduleID: uint32(scheduleID), SequenceID: uint32(seq), Action: t.Action, Payload: t.Payload, TimeOffset: uint32(t.TimeOffset),
				ContinueOnFailure: boolByte(t.ContinueOnFailure), CreatedAt: now, UpdatedAt: now,
			})
			if err != nil {
				return fmt.Errorf("store the task: %w", err)
			}
			id = uint64(made)
			return nil
		})
	})
	if err != nil {
		return scheduleapi.TaskRecord{}, err
	}
	return s.task(ctx, id)
}

// ChangeTask stores the change and, if the task is to go to another place, moves the tasks in between one place to make room; a place beyond the last is
// the last.
func (s clientSchedules) ChangeTask(ctx context.Context, scheduleID, taskID uint64, c scheduleapi.TaskChange) (scheduleapi.TaskRecord, error) {
	if !fits(scheduleID, taskID) {
		return scheduleapi.TaskRecord{}, scheduleapi.ErrNotFound
	}
	err := appUsers{s.DB}.retryDeadlock(func() error {
		return s.inTx(ctx, func(q *dbq.Queries) error {
			rows, err := s.lockTasks(ctx, q, scheduleID)
			if err != nil {
				return err
			}
			old, last, found := 0, 0, false
			for _, r := range rows {
				if uint64(r.ID) == taskID {
					old, found = int(r.SequenceID), true
				}
				last = max(last, int(r.SequenceID))
			}
			if !found {
				return scheduleapi.ErrNotFound
			}
			seq := min(max(c.Sequence, 1), last)
			switch {
			case seq < old:
				err = q.MoveTasksLater(ctx, dbq.MoveTasksLaterParams{ScheduleID: uint32(scheduleID), SequenceID: uint32(seq), SequenceID_2: uint32(old)})
			case seq > old:
				err = q.MoveTasksEarlier(ctx, dbq.MoveTasksEarlierParams{ScheduleID: uint32(scheduleID), SequenceID: uint32(old), SequenceID_2: uint32(seq)})
			}
			if err != nil {
				return fmt.Errorf("move the tasks: %w", err)
			}
			err = q.UpdateTaskRow(ctx, dbq.UpdateTaskRowParams{
				Action: c.Action, Payload: c.Payload, TimeOffset: uint32(c.TimeOffset), ContinueOnFailure: boolByte(c.ContinueOnFailure), SequenceID: uint32(seq),
				UpdatedAt: sql.NullTime{Time: s.now().UTC(), Valid: true}, ID: uint32(taskID), ScheduleID: uint32(scheduleID),
			})
			if err != nil {
				return fmt.Errorf("store the task: %w", err)
			}
			return nil
		})
	})
	if err != nil {
		return scheduleapi.TaskRecord{}, err
	}
	return s.task(ctx, taskID)
}

// DeleteTask removes the task and moves the later ones one place back.
func (s clientSchedules) DeleteTask(ctx context.Context, scheduleID, taskID uint64) error {
	if !fits(scheduleID, taskID) {
		return scheduleapi.ErrNotFound
	}
	return appUsers{s.DB}.retryDeadlock(func() error {
		return s.inTx(ctx, func(q *dbq.Queries) error {
			rows, err := s.lockTasks(ctx, q, scheduleID)
			if err != nil {
				return err
			}
			seq, found := 0, false
			for _, r := range rows {
				if uint64(r.ID) == taskID {
					seq, found = int(r.SequenceID), true
				}
			}
			if !found {
				return scheduleapi.ErrNotFound
			}
			if err := q.DeleteTaskRow(ctx, dbq.DeleteTaskRowParams{ID: uint32(taskID), ScheduleID: uint32(scheduleID)}); err != nil {
				return fmt.Errorf("delete task %d: %w", taskID, err)
			}
			if err := q.CloseGapAfter(ctx, dbq.CloseGapAfterParams{ScheduleID: uint32(scheduleID), SequenceID: uint32(seq)}); err != nil {
				return fmt.Errorf("close the gap after task %d: %w", taskID, err)
			}
			return nil
		})
	})
}

// --- the scheduler

// runSchedule makes a schedule of what the scheduler needs; a stored expression that cannot be read is an error.
func runSchedule(id, server uint32, name, minute, hour, dom, month, dow string, active, processing bool, only uint8, next sql.NullTime, status sql.NullString) (schedules.Schedule, error) {
	cron, err := schedules.Parse(minute, hour, dom, month, dow)
	if err != nil {
		return schedules.Schedule{}, fmt.Errorf("schedule %d has an expression that cannot be read: %w", id, err)
	}
	return schedules.Schedule{
		ID: uint64(id), ServerID: uint64(server), Name: name, Cron: cron, Active: active, Processing: processing, OnlyWhenOnline: only != 0, NextRunAt: timePtr(next), ServerStatus: status.String,
	}, nil
}

// Due finds the schedules that may start with one query. One whose stored expression cannot be read is left out: it has no next time to work out, and
// raptor audit lists it.
func (s scheduleRunner) Due(ctx context.Context, now time.Time) ([]schedules.Schedule, error) {
	rows, err := s.q.DueSchedules(ctx, sql.NullTime{Time: now.UTC(), Valid: true})
	if err != nil {
		return nil, fmt.Errorf("find the schedules that are due: %w", err)
	}
	var out []schedules.Schedule
	for _, r := range rows {
		sc, err := runSchedule(r.ID, r.ServerID, r.Name, r.CronMinute, r.CronHour, r.CronDayOfMonth, r.CronMonth, r.CronDayOfWeek, r.IsActive, r.IsProcessing, r.OnlyWhenOnline, r.NextRunAt, r.Status)
		if err != nil {
			continue
		}
		out = append(out, sc)
	}
	return out, nil
}

func (s scheduleRunner) Schedule(ctx context.Context, id uint64) (schedules.Schedule, error) {
	if !fits(id) {
		return schedules.Schedule{}, fmt.Errorf("schedule %d: %w", id, sql.ErrNoRows)
	}
	r, err := s.q.ScheduleForRun(ctx, uint32(id))
	if err != nil {
		return schedules.Schedule{}, fmt.Errorf("read schedule %d: %w", id, err)
	}
	return runSchedule(r.ID, r.ServerID, r.Name, r.CronMinute, r.CronHour, r.CronDayOfMonth, r.CronMonth, r.CronDayOfWeek, r.IsActive, r.IsProcessing, r.OnlyWhenOnline, r.NextRunAt, r.Status)
}

func (s scheduleRunner) Tasks(ctx context.Context, scheduleID uint64) ([]schedules.Task, error) {
	if !fits(scheduleID) {
		return nil, nil
	}
	rows, err := s.q.TasksForRun(ctx, uint32(scheduleID))
	if err != nil {
		return nil, fmt.Errorf("read the tasks of schedule %d: %w", scheduleID, err)
	}
	out := make([]schedules.Task, len(rows))
	for i, r := range rows {
		out[i] = schedules.Task{ID: uint64(r.ID), Sequence: int(r.SequenceID), Action: r.Action, Payload: r.Payload, TimeOffset: int(r.TimeOffset), ContinueOnFailure: r.ContinueOnFailure != 0}
	}
	return out, nil
}

// Begin marks the schedule as running with one conditional statement, so that of two starts at once only one takes the mark, and queues the first task in
// the same transaction.
func (s scheduleRunner) Begin(ctx context.Context, scheduleID uint64, nextRun time.Time, first schedules.Task) (bool, error) {
	began := false
	err := s.inTx(ctx, func(q *dbq.Queries) error {
		now := sql.NullTime{Time: s.now().UTC(), Valid: true}
		n, err := q.BeginScheduleRun(ctx, dbq.BeginScheduleRunParams{NextRunAt: sql.NullTime{Time: nextRun.UTC(), Valid: true}, UpdatedAt: now, ID: uint32(scheduleID)})
		if err != nil {
			return fmt.Errorf("start schedule %d: %w", scheduleID, err)
		}
		if n == 0 {
			return nil
		}
		began = true
		return q.MarkTaskQueued(ctx, dbq.MarkTaskQueuedParams{IsQueued: true, UpdatedAt: now, ID: uint32(first.ID)})
	})
	return began, err
}

func (s scheduleRunner) mark(ctx context.Context, t schedules.Task, queued bool) error {
	err := s.q.MarkTaskQueued(ctx, dbq.MarkTaskQueuedParams{IsQueued: queued, UpdatedAt: sql.NullTime{Time: s.now().UTC(), Valid: true}, ID: uint32(t.ID)})
	if err != nil {
		return fmt.Errorf("mark task %d: %w", t.ID, err)
	}
	return nil
}

func (s scheduleRunner) Queue(ctx context.Context, t schedules.Task) error {
	return s.mark(ctx, t, true)
}
func (s scheduleRunner) Dequeue(ctx context.Context, t schedules.Task) error {
	return s.mark(ctx, t, false)
}

func (s scheduleRunner) Finish(ctx context.Context, scheduleID uint64, at time.Time) error {
	err := s.q.FinishScheduleRun(ctx, dbq.FinishScheduleRunParams{LastRunAt: sql.NullTime{Time: at.UTC(), Valid: true}, UpdatedAt: sql.NullTime{Time: s.now().UTC(), Valid: true}, ID: uint32(scheduleID)})
	if err != nil {
		return fmt.Errorf("finish schedule %d: %w", scheduleID, err)
	}
	return nil
}

func (s scheduleRunner) Progress(ctx context.Context, sc schedules.Schedule) (bool, string, error) {
	r, err := s.q.ScheduleProgress(ctx, uint32(sc.ID))
	if err != nil {
		return false, "", fmt.Errorf("read the progress of schedule %d: %w", sc.ID, err)
	}
	return r.IsActive, r.Status.String, nil
}

// ResetRunning clears the marks of every schedule and task in one transaction.
func (s scheduleRunner) ResetRunning(ctx context.Context) error {
	return s.inTx(ctx, func(q *dbq.Queries) error {
		if err := q.ClearScheduleMarks(ctx); err != nil {
			return fmt.Errorf("clear the running marks of the schedules: %w", err)
		}
		if err := q.ClearTaskMarks(ctx); err != nil {
			return fmt.Errorf("clear the queued marks of the tasks: %w", err)
		}
		return nil
	})
}
