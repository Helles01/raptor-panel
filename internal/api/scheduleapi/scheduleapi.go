// Package scheduleapi serves the routes with which a user manages the schedules of their server and the tasks in them. The rules are in
// internal/schedules (the cron expression, the tasks, who may run what); this package puts them in order and talks to the storage, the
// scheduler and the activity log through the interfaces below.
package scheduleapi

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/permissions"
	"github.com/Helles01/raptor-panel/internal/schedules"
)

// The store answers with these.
var (
	// ErrNotFound: there is no such schedule or task on the server.
	ErrNotFound = errors.New("scheduleapi: not found")
	// ErrTaskLimit: the schedule has as many tasks as it may.
	ErrTaskLimit = errors.New("scheduleapi: too many tasks")
)

// TaskRecord is a task as stored.
type TaskRecord struct {
	ID                uint64
	Sequence          int
	Action            string
	Payload           string
	TimeOffset        int
	Queued            bool
	ContinueOnFailure bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// ScheduleRecord is a schedule as stored, with its tasks in the order of their sequence.
type ScheduleRecord struct {
	ID             uint64
	Name           string
	Minute         string
	Hour           string
	DayOfMonth     string
	Month          string
	DayOfWeek      string
	Active         bool
	Processing     bool
	OnlyWhenOnline bool
	LastRunAt      *time.Time
	NextRunAt      *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Tasks          []TaskRecord
}

// Placement chooses the sequence number of a new task, given the sequence numbers the schedule has, and whether the later tasks are
// to be moved one place on. An error ends the request and is returned as it is.
type Placement func(existing []int) (sequence int, shift bool, err error)

// TaskChange is a change of a task. Sequence is the new position; the store moves the tasks between the old and the new one by one
// place to make room.
type TaskChange struct {
	Action            string
	Payload           string
	TimeOffset        int
	ContinueOnFailure bool
	Sequence          int
}

// Store is the part of the storage the routes need. Every method works on the schedules of the one server.
type Store interface {
	List(ctx context.Context, serverID uint64) ([]ScheduleRecord, error)
	// Find returns one schedule of the server with its tasks, or ErrNotFound.
	Find(ctx context.Context, serverID, scheduleID uint64) (ScheduleRecord, error)
	Create(ctx context.Context, serverID uint64, p schedules.Plan) (ScheduleRecord, error)
	// Update stores the plan; with p.ResetProcessing the running mark is cleared too.
	Update(ctx context.Context, serverID, scheduleID uint64, p schedules.Plan) (ScheduleRecord, error)
	Delete(ctx context.Context, serverID, scheduleID uint64) error
	// BackupLimit is the server's limit of backups, nil for none.
	BackupLimit(ctx context.Context, serverID uint64) (*int64, error)
	// AddTask stores a task in one transaction in which the tasks of the schedule are locked: the sequence numbers are read, place
	// decides, the later tasks are moved if it says so, and the task is inserted.
	AddTask(ctx context.Context, scheduleID uint64, t schedules.Task, place Placement) (TaskRecord, error)
	// ChangeTask stores a change of a task of the schedule, or answers ErrNotFound.
	ChangeTask(ctx context.Context, scheduleID, taskID uint64, c TaskChange) (TaskRecord, error)
	// DeleteTask removes a task of the schedule and moves the later ones one place back, or answers ErrNotFound.
	DeleteTask(ctx context.Context, scheduleID, taskID uint64) error
}

// Starter starts a schedule by hand. The run goes on after the request: the implementation keeps the context of the process, not
// that of the request.
type Starter interface {
	Start(scheduleID uint64) error
}

// Entry is an entry of the activity log about a schedule and perhaps one of its tasks.
type Entry struct {
	Event string
	// ServerID is the server, ScheduleID the schedule and TaskID the task (0 for none) the entry is about; ActorID did it.
	ServerID   uint64
	ScheduleID uint64
	TaskID     uint64
	ActorID    uint64
	APIKeyID   *uint64
	IP         string
	Properties string
}

// ActivityLog stores activity entries.
type ActivityLog interface {
	Log(ctx context.Context, e Entry) error
}

// The throttle of creating schedules: two a minute for each server, whoever creates, as in the reference.
const (
	CreateLimit  = 2
	CreateWindow = time.Minute
)

// Deps are everything the endpoints need.
type Deps struct {
	Store    Store
	Starter  Starter
	Activity ActivityLog
	// CreateLimiter allows CreateLimit schedules in CreateWindow for a server.
	CreateLimiter *middleware.Limiter
	// TaskLimit is how many tasks a schedule may have; schedules.DefaultTaskLimit when 0.
	TaskLimit int
	// Location is the time zone of the panel, in which the expressions are read; UTC when nil.
	Location *time.Location
	Logger   *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
}

// Handlers are the endpoints.
type Handlers struct {
	d Deps
}

// New checks the dependencies and returns the endpoints.
func New(d Deps) (*Handlers, error) {
	if d.Store == nil || d.Starter == nil || d.Activity == nil || d.CreateLimiter == nil || d.Logger == nil {
		return nil, errors.New("scheduleapi: the store, the starter, the activity log, the limiter and a logger are required")
	}
	if d.TaskLimit == 0 {
		d.TaskLimit = schedules.DefaultTaskLimit
	}
	if d.Location == nil {
		d.Location = time.UTC
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Handlers{d: d}, nil
}

// Register adds the routes below /api/client/servers/{server}/schedules.
func (h *Handlers) Register(g api.Group) {
	s := g.Sub("/api/client/servers/:server/schedules")
	s.GET("", h.List, api.ServerPermission(permissions.ScheduleRead))
	s.POST("", h.Create, api.ServerPermission(permissions.ScheduleCreate))
	s.GET("/:schedule", h.View, api.ServerPermission(permissions.ScheduleRead))
	s.POST("/:schedule", h.Update, api.ServerPermission(permissions.ScheduleUpdate))
	s.POST("/:schedule/execute", h.Execute, api.ServerPermission(permissions.ScheduleUpdate))
	s.DELETE("/:schedule", h.Delete, api.ServerPermission(permissions.ScheduleDelete))
	// A task also needs the permission of its action, which the handler asks for once it has read the body.
	s.POST("/:schedule/tasks", h.AddTask, api.ServerPermission(permissions.ScheduleUpdate))
	s.POST("/:schedule/tasks/:task", h.ChangeTask, api.ServerPermission(permissions.ScheduleUpdate))
	s.DELETE("/:schedule/tasks/:task", h.DeleteTask, api.ServerPermission(permissions.ScheduleUpdate))
}

func caller(c echo.Context) (authz.Principal, error) {
	p, ok := authz.From(c)
	if !ok || p.Server == nil {
		return authz.Principal{}, apierr.Forbidden("This action is unauthorized.")
	}
	return p, nil
}

func storeError(err error) error {
	if errors.Is(err, ErrNotFound) {
		return apierr.NotFound()
	}
	return apierr.Internal(err)
}
