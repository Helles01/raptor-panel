package scheduleapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/activity"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/field"
	"github.com/Helles01/raptor-panel/internal/api/resp"
	"github.com/Helles01/raptor-panel/internal/api/validate"
	"github.com/Helles01/raptor-panel/internal/permissions"
	"github.com/Helles01/raptor-panel/internal/schedules"
)

// maxBody is the largest body these routes read: a task holds a command of 4096 bytes or a list of files.
const maxBody = 64 << 10

// atom is how the reference writes a time (Carbon's toAtomString): the offset as +00:00, never Z.
const atom = "2006-01-02T15:04:05-07:00"

type cronAttributes struct {
	DayOfWeek  string `json:"day_of_week"`
	DayOfMonth string `json:"day_of_month"`
	Month      string `json:"month"`
	Hour       string `json:"hour"`
	Minute     string `json:"minute"`
}

type scheduleAttributes struct {
	ID             uint64         `json:"id"`
	Name           string         `json:"name"`
	Cron           cronAttributes `json:"cron"`
	IsActive       bool           `json:"is_active"`
	IsProcessing   bool           `json:"is_processing"`
	OnlyWhenOnline bool           `json:"only_when_online"`
	LastRunAt      *string        `json:"last_run_at"`
	NextRunAt      *string        `json:"next_run_at"`
	CreatedAt      string         `json:"created_at"`
	UpdatedAt      string         `json:"updated_at"`
}

type taskAttributes struct {
	ID                uint64 `json:"id"`
	SequenceID        int    `json:"sequence_id"`
	Action            string `json:"action"`
	Payload           string `json:"payload"`
	TimeOffset        int    `json:"time_offset"`
	IsQueued          bool   `json:"is_queued"`
	ContinueOnFailure bool   `json:"continue_on_failure"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

func (h *Handlers) when(t time.Time) string { return t.In(h.d.Location).Format(atom) }

func (h *Handlers) whenPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := h.when(*t)
	return &s
}

func (h *Handlers) taskItem(t TaskRecord) resp.Item {
	return resp.NewItem("schedule_task", taskAttributes{
		ID: t.ID, SequenceID: t.Sequence, Action: t.Action, Payload: t.Payload, TimeOffset: t.TimeOffset, IsQueued: t.Queued,
		ContinueOnFailure: t.ContinueOnFailure, CreatedAt: h.when(t.CreatedAt), UpdatedAt: h.when(t.UpdatedAt),
	})
}

func (h *Handlers) scheduleItem(s ScheduleRecord) resp.Item {
	tasks := make([]resp.Item, len(s.Tasks))
	for i, t := range s.Tasks {
		tasks[i] = h.taskItem(t)
	}
	return resp.NewItem("server_schedule", scheduleAttributes{
		ID: s.ID, Name: s.Name,
		Cron:     cronAttributes{DayOfWeek: s.DayOfWeek, DayOfMonth: s.DayOfMonth, Month: s.Month, Hour: s.Hour, Minute: s.Minute},
		IsActive: s.Active, IsProcessing: s.Processing, OnlyWhenOnline: s.OnlyWhenOnline,
		LastRunAt: h.whenPtr(s.LastRunAt), NextRunAt: h.whenPtr(s.NextRunAt), CreatedAt: h.when(s.CreatedAt), UpdatedAt: h.when(s.UpdatedAt),
	}).With("tasks", resp.NewList(tasks))
}

// number reads a numeric {schedule} or {task} of the route. What cannot be one is not looked up.
func number(c echo.Context, name string) (uint64, error) {
	id, err := strconv.ParseUint(c.Param(name), 10, 64)
	if err != nil || id == 0 {
		return 0, apierr.NotFound()
	}
	return id, nil
}

// log writes an entry about a schedule, and a task if there is one. A failure is logged and never reaches the person.
func (h *Handlers) log(c echo.Context, p authz.Principal, event string, scheduleID, taskID uint64, values map[string]any) {
	props, err := activity.Properties(event, values)
	if err != nil {
		h.d.Logger.Error("activity entry refused", "event", event, "error", err)
		return
	}
	e := Entry{Event: event, ServerID: p.Server.ID, ScheduleID: scheduleID, TaskID: taskID, ActorID: p.User.ID, IP: c.RealIP(), Properties: props}
	if p.Key != nil {
		id := p.Key.ID
		e.APIKeyID = &id
	}
	if err := h.d.Activity.Log(c.Request().Context(), e); err != nil {
		h.d.Logger.Error("activity entry not stored", "event", event, "server", p.Server.ID, "error", err)
	}
}

// List is GET /schedules.
func (h *Handlers) List(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	all, err := h.d.Store.List(c.Request().Context(), p.Server.ID)
	if err != nil {
		return storeError(err)
	}
	items := make([]resp.Item, len(all))
	for i, s := range all {
		items[i] = h.scheduleItem(s)
	}
	return c.JSON(http.StatusOK, resp.NewList(items))
}

// View is GET /schedules/{schedule}.
func (h *Handlers) View(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	id, err := number(c, "schedule")
	if err != nil {
		return err
	}
	s, err := h.d.Store.Find(c.Request().Context(), p.Server.ID, id)
	if err != nil {
		return storeError(err)
	}
	return c.JSON(http.StatusOK, h.scheduleItem(s))
}

type scheduleBody struct {
	Name           field.String `json:"name"`
	Minute         field.String `json:"minute"`
	Hour           field.String `json:"hour"`
	DayOfMonth     field.String `json:"day_of_month"`
	Month          field.String `json:"month"`
	DayOfWeek      field.String `json:"day_of_week"`
	IsActive       field.Bool   `json:"is_active"`
	OnlyWhenOnline field.Bool   `json:"only_when_online"`
}

// input checks the body and turns it into what the rules take. Each text is needed and must be text.
func (b scheduleBody) input() (schedules.Input, error) {
	v := validate.New()
	for _, f := range []struct {
		name  string
		value field.String
	}{{"name", b.Name}, {"minute", b.Minute}, {"hour", b.Hour}, {"day_of_month", b.DayOfMonth}, {"month", b.Month}, {"day_of_week", b.DayOfWeek}} {
		v.Sent(f.name, f.value.State)
		v.Type(f.name, f.value.State, validate.KindString, false)
	}
	if b.IsActive.State == field.Null {
		v.Custom("is_active", "filled", "The is active field is required.")
	}
	v.Type("is_active", b.IsActive.State, validate.KindBoolean, true)
	v.Type("only_when_online", b.OnlyWhenOnline.State, validate.KindBoolean, true)
	if err := v.Err(); err != nil {
		return schedules.Input{}, err
	}

	in := schedules.Input{
		Name: b.Name.V, Minute: b.Minute.V, Hour: b.Hour.V, DayOfMonth: b.DayOfMonth.V, Month: b.Month.V, DayOfWeek: b.DayOfWeek.V,
	}
	if b.IsActive.State == field.Valid {
		in.Active = &b.IsActive.V
	}
	switch b.OnlyWhenOnline.State {
	case field.Valid:
		in.OnlyWhenOnline = &b.OnlyWhenOnline.V
	case field.Null:
		off := false
		in.OnlyWhenOnline = &off
	}
	return in, nil
}

// planError turns a refusal of the schedule rules into the API's answer.
func planError(err error) error {
	if errors.Is(err, schedules.ErrCron) {
		return apierr.Display(err.Error())
	}
	return validate.FromField(err)
}

// Create is POST /schedules. A flag that is not sent is off: a schedule made without is_active does not run until it is switched on.
func (h *Handlers) Create(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	// Two a minute for a server, whoever creates. Only calls that passed the authorizer count.
	if allowed, retry := h.d.CreateLimiter.Allow(p.Server.UUID); !allowed {
		return apierr.TooManyRequests(retry)
	}
	var body scheduleBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	in, err := body.input()
	if err != nil {
		return err
	}
	plan, err := schedules.PlanCreate(in, h.d.Now(), h.d.Location)
	if err != nil {
		return planError(err)
	}
	s, err := h.d.Store.Create(c.Request().Context(), p.Server.ID, plan)
	if err != nil {
		return storeError(err)
	}
	h.log(c, p, activity.ServerScheduleCreate, s.ID, 0, map[string]any{"name": s.Name})
	return c.JSON(http.StatusOK, h.scheduleItem(s))
}

// Update is POST /schedules/{schedule}. A flag that is not sent keeps its value (C-052); switching the schedule on or off clears a
// running mark that is stuck.
func (h *Handlers) Update(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	id, err := number(c, "schedule")
	if err != nil {
		return err
	}
	var body scheduleBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	in, err := body.input()
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	cur, err := h.d.Store.Find(ctx, p.Server.ID, id)
	if err != nil {
		return storeError(err)
	}
	plan, err := schedules.PlanUpdate(schedules.Current{Active: cur.Active, OnlyWhenOnline: cur.OnlyWhenOnline}, in, h.d.Now(), h.d.Location)
	if err != nil {
		return planError(err)
	}
	s, err := h.d.Store.Update(ctx, p.Server.ID, id, plan)
	if err != nil {
		return storeError(err)
	}
	h.log(c, p, activity.ServerScheduleUpdate, s.ID, 0, map[string]any{"name": s.Name, "active": s.Active})
	return c.JSON(http.StatusOK, h.scheduleItem(s))
}

// Execute is POST /schedules/{schedule}/execute: runs the schedule now, as the person who asked. They need the permission of every task
// in it, on top of the right to update schedules (C-001).
func (h *Handlers) Execute(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	id, err := number(c, "schedule")
	if err != nil {
		return err
	}
	s, err := h.d.Store.Find(c.Request().Context(), p.Server.ID, id)
	if err != nil {
		return storeError(err)
	}
	tasks := make([]schedules.Task, len(s.Tasks))
	for i, t := range s.Tasks {
		tasks[i] = schedules.Task{Action: t.Action, Payload: t.Payload, Sequence: t.Sequence, TimeOffset: t.TimeOffset}
	}
	if !schedules.CanRun(p.Permissions, tasks) {
		return apierr.Forbidden("This action is unauthorized.")
	}

	switch err := h.d.Starter.Start(id); {
	case errors.Is(err, schedules.ErrAlreadyRunning):
		return apierr.Conflict("This schedule is already running.")
	case errors.Is(err, schedules.ErrNoTasks):
		return apierr.Display(err.Error())
	case err != nil:
		return apierr.Internal(err)
	}
	h.log(c, p, activity.ServerScheduleExecute, id, 0, map[string]any{"name": s.Name})
	return c.JSON(http.StatusAccepted, []string{})
}

// Delete is DELETE /schedules/{schedule}.
func (h *Handlers) Delete(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	id, err := number(c, "schedule")
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	s, err := h.d.Store.Find(ctx, p.Server.ID, id)
	if err != nil {
		return storeError(err)
	}
	if err := h.d.Store.Delete(ctx, p.Server.ID, id); err != nil {
		return storeError(err)
	}
	h.log(c, p, activity.ServerScheduleDelete, id, 0, map[string]any{"name": s.Name})
	return c.NoContent(http.StatusNoContent)
}

type taskBody struct {
	Action            field.String `json:"action"`
	Payload           field.String `json:"payload"`
	TimeOffset        field.Int    `json:"time_offset"`
	Sequence          field.Int    `json:"sequence_id"`
	ContinueOnFailure field.Bool   `json:"continue_on_failure"`
}

// task is a checked task body.
type task struct {
	schedules.Task
	// Sequence is the position asked for, nil when none was.
	Requested *int
}

// mayHave checks the permission of the action in the body, which comes before its validation: a user may not put a task in a
// schedule that they could not run themselves. An action that is not one has no permission and is left to the validation.
func mayHave(p authz.Principal, b taskBody) error {
	needed, err := schedules.Task{Action: b.Action.V, Payload: b.Payload.V}.Permission()
	if err != nil || permissions.Has(p.Permissions, needed) {
		return nil
	}
	return apierr.Forbidden("This action is unauthorized.")
}

// check validates a task body.
func (b taskBody) check() (task, error) {
	v := validate.New()
	v.Sent("action", b.Action.State)
	v.Type("action", b.Action.State, validate.KindString, false)
	if b.Action.State == field.Valid {
		v.OneOf("action", b.Action.V, schedules.ActionCommand, schedules.ActionPower, schedules.ActionBackup)
	}
	v.Type("payload", b.Payload.State, validate.KindString, true)
	if b.Action.State == field.Valid && b.Action.V != schedules.ActionBackup && b.Payload.State != field.Valid && b.Payload.State != field.Invalid {
		v.Custom("payload", "required_unless", "The payload field is required unless action is in backup.")
	}
	if b.Action.V == schedules.ActionPower && b.Payload.State == field.Valid {
		v.OneOf("payload", b.Payload.V, "start", "stop", "restart", "kill")
	}
	v.Sent("time_offset", b.TimeOffset.State)
	v.Type("time_offset", b.TimeOffset.State, validate.KindNumber, false)
	if b.TimeOffset.State == field.Valid {
		switch {
		case b.TimeOffset.V < 0:
			v.Custom("time_offset", "min", "The time offset must be at least 0.")
		case b.TimeOffset.V > schedules.MaxTimeOffset:
			v.Custom("time_offset", "max", fmt.Sprintf("The time offset may not be greater than %d.", schedules.MaxTimeOffset))
		}
	}
	var requested *int
	if b.Sequence.State == field.Null {
		v.Custom("sequence_id", "required", "The sequence id field is required.")
	}
	v.Type("sequence_id", b.Sequence.State, validate.KindNumber, true)
	if b.Sequence.State == field.Valid {
		if b.Sequence.V < 1 {
			v.Custom("sequence_id", "min", "The sequence id must be at least 1.")
		}
		n := int(b.Sequence.V)
		requested = &n
	}
	if b.ContinueOnFailure.State == field.Null {
		v.Custom("continue_on_failure", "required", "The continue on failure field is required.")
	}
	v.Type("continue_on_failure", b.ContinueOnFailure.State, validate.KindBoolean, true)
	if err := v.Err(); err != nil {
		return task{}, err
	}

	t := schedules.Task{
		Action: b.Action.V, Payload: b.Payload.V, TimeOffset: int(b.TimeOffset.V), Sequence: 1, ContinueOnFailure: b.ContinueOnFailure.V,
	}
	// The rules of the task itself: a command that fits what the node takes, a list of files that makes sense.
	if err := t.Validate(); err != nil {
		w := validate.New()
		w.Custom("payload", "invalid", sentence(err))
		return task{}, w.Err()
	}
	return task{Task: t, Requested: requested}, nil
}

// sentence makes the text of a rule into a sentence for the person: capital letter, full stop.
func sentence(err error) string {
	s := strings.TrimSpace(err.Error())
	if s == "" {
		return "The payload is invalid."
	}
	s = strings.ToUpper(s[:1]) + s[1:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

// decodeTask reads and checks a task body for a route that has a schedule.
func (h *Handlers) decodeTask(c echo.Context, p authz.Principal) (task, error) {
	var body taskBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return task{}, err
	}
	if err := mayHave(p, body); err != nil {
		return task{}, err
	}
	t, err := body.check()
	if err != nil {
		return task{}, err
	}
	return t, nil
}

// backupAllowed refuses a backup task for a server that may have no backups.
func (h *Handlers) backupAllowed(c echo.Context, p authz.Principal, action string) error {
	if action != schedules.ActionBackup {
		return nil
	}
	limit, err := h.d.Store.BackupLimit(c.Request().Context(), p.Server.ID)
	if err != nil {
		return storeError(err)
	}
	if limit != nil && *limit == 0 {
		return apierr.Forbidden("A backup task cannot be created when the server's backup limit is set to 0.")
	}
	return nil
}

// AddTask is POST /schedules/{schedule}/tasks.
func (h *Handlers) AddTask(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	scheduleID, err := number(c, "schedule")
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	s, err := h.d.Store.Find(ctx, p.Server.ID, scheduleID)
	if err != nil {
		return storeError(err)
	}
	t, err := h.decodeTask(c, p)
	if err != nil {
		return err
	}
	if len(s.Tasks) >= h.d.TaskLimit {
		return h.overLimit()
	}
	if err := h.backupAllowed(c, p, t.Action); err != nil {
		return err
	}

	rec, err := h.d.Store.AddTask(ctx, scheduleID, t.Task, func(existing []int) (int, bool, error) {
		// Counted again under the lock: two requests at once must not both take the last place.
		if len(existing) >= h.d.TaskLimit {
			return 0, false, ErrTaskLimit
		}
		seq, shift := schedules.PlaceTask(existing, t.Requested)
		return seq, shift, nil
	})
	switch {
	case errors.Is(err, ErrTaskLimit):
		return h.overLimit()
	case err != nil:
		return storeError(err)
	}
	h.log(c, p, activity.ServerTaskCreate, scheduleID, rec.ID, map[string]any{"name": s.Name, "action": rec.Action, "payload": rec.Payload})
	return c.JSON(http.StatusOK, h.taskItem(rec))
}

func (h *Handlers) overLimit() error {
	return apierr.Display(fmt.Sprintf("Schedules may not have more than %d tasks associated with them. Creating this task would put this schedule over the limit.", h.d.TaskLimit))
}

// ChangeTask is POST /schedules/{schedule}/tasks/{task}. The task may be moved to another place in the schedule.
func (h *Handlers) ChangeTask(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	scheduleID, err := number(c, "schedule")
	if err != nil {
		return err
	}
	taskID, err := number(c, "task")
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	s, err := h.d.Store.Find(ctx, p.Server.ID, scheduleID)
	if err != nil {
		return storeError(err)
	}
	cur, ok := taskOf(s, taskID)
	if !ok {
		return apierr.NotFound()
	}
	t, err := h.decodeTask(c, p)
	if err != nil {
		return err
	}
	if err := h.backupAllowed(c, p, t.Action); err != nil {
		return err
	}

	sequence := cur.Sequence
	if t.Requested != nil {
		sequence = *t.Requested
	}
	rec, err := h.d.Store.ChangeTask(ctx, scheduleID, taskID, TaskChange{
		Action: t.Action, Payload: t.Payload, TimeOffset: t.TimeOffset, ContinueOnFailure: t.ContinueOnFailure, Sequence: sequence,
	})
	if err != nil {
		return storeError(err)
	}
	h.log(c, p, activity.ServerTaskUpdate, scheduleID, taskID, map[string]any{"name": s.Name, "action": rec.Action, "payload": rec.Payload})
	return c.JSON(http.StatusOK, h.taskItem(rec))
}

// DeleteTask is DELETE /schedules/{schedule}/tasks/{task}.
func (h *Handlers) DeleteTask(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	scheduleID, err := number(c, "schedule")
	if err != nil {
		return err
	}
	taskID, err := number(c, "task")
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	s, err := h.d.Store.Find(ctx, p.Server.ID, scheduleID)
	if err != nil {
		return storeError(err)
	}
	if _, ok := taskOf(s, taskID); !ok {
		return apierr.NotFound()
	}
	if err := h.d.Store.DeleteTask(ctx, scheduleID, taskID); err != nil {
		return storeError(err)
	}
	h.log(c, p, activity.ServerTaskDelete, scheduleID, taskID, map[string]any{"name": s.Name})
	return c.NoContent(http.StatusNoContent)
}

func taskOf(s ScheduleRecord, id uint64) (TaskRecord, bool) {
	for _, t := range s.Tasks {
		if t.ID == id {
			return t, true
		}
	}
	return TaskRecord{}, false
}
