// Package backupapi serves the routes with which a user manages the backups of their server: listing, starting, locking, downloading,
// restoring and deleting them. The rules are in internal/backups; this package puts them in order and talks to the storage, the node and
// the activity log through the interfaces below. Only backups kept on the node itself (the "wings" disk) are handled; the S3 disk is not
// supported yet (C-060).
package backupapi

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/controlapi"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/backups"
	"github.com/Helles01/raptor-panel/internal/permissions"
	"github.com/Helles01/raptor-panel/internal/wings"
)

// ErrNotFound is what the store answers when there is no such backup on the server.
var ErrNotFound = errors.New("backupapi: not found")

// Record is a backup as stored.
type Record struct {
	ID           uint64
	UUID         string
	Name         string
	Disk         string
	IgnoredFiles []string
	// Checksum is "type:value", nil for a backup that has none (running or failed).
	Checksum    *string
	Bytes       int64
	Successful  bool
	Locked      bool
	CreatedAt   time.Time
	CompletedAt *time.Time
}

// State is the state of the backup as the rules see it.
func (r Record) State() backups.State {
	return backups.State{Completed: r.CompletedAt != nil, Successful: r.Successful, Locked: r.Locked}
}

// Page is one page of the backups of a server.
type Page struct {
	Total int
	// NonFailed is how many backups are running or finished successfully, the ones that count against the limit.
	NonFailed int
	Backups   []Record
}

// NewBackup is a backup to store.
type NewBackup struct {
	UUID    string
	Name    string
	Disk    string
	Ignored []string
	Locked  bool
}

// StartRequest asks the store to add a backup in one transaction in which the backups of the server are locked.
type StartRequest struct {
	ServerID uint64
	Backup   NewBackup
	// Since is the start of the throttle window: the creation times from then on are what Check is given. A zero Since means the throttle is off and
	// Check gets none.
	Since time.Time
	// Check is called with the server's backup limit, the backups that count against it and the creation times of all the backups the
	// server ever had in the throttle window (deleted ones too, so that deleting and starting again does not get round it). An error
	// ends the request and is returned as it is; an admission that says Prune makes the store delete that backup first, in the same
	// transaction.
	Check func(limit *int64, existing []backups.Existing, started []time.Time) (backups.Admission, error)
}

// Started is the answer to a start: the new backup and, when the server was at its limit and the request was to replace the oldest, the
// backup whose row the store deleted in the same transaction to make room (its archive on the node is still to be deleted).
type Started struct {
	Record Record
	Pruned *Record
}

// Store is the part of the storage the routes need. Every method works on the backups of the one server.
type Store interface {
	Page(ctx context.Context, serverID uint64, offset, limit int) (Page, error)
	Find(ctx context.Context, serverID uint64, uuid string) (Record, error)
	Start(ctx context.Context, r StartRequest) (Started, error)
	SetLocked(ctx context.Context, serverID, backupID uint64, locked bool) error
	// Delete removes the backup. The throttle still counts it.
	Delete(ctx context.Context, serverID, backupID uint64) error
	// SetServerStatus changes the status of the server, only if it is still from, and says whether it did. Empty is the normal state.
	SetServerStatus(ctx context.Context, serverID uint64, from, to string) (bool, error)
}

// Daemon is the calls to a node that these routes make. NewDaemon is the one that talks to a real Wings.
type Daemon interface {
	Create(ctx context.Context, n wings.Node, serverUUID, disk, backupUUID, ignore string) error
	Restore(ctx context.Context, n wings.Node, serverUUID, backupUUID, disk string, truncate bool, downloadURL string) error
	Delete(ctx context.Context, n wings.Node, serverUUID, backupUUID string) error
}

// Tokens makes the token with which a browser downloads a backup from a node. *wings.Issuer is one.
type Tokens interface {
	BackupDownload(n wings.Node, who wings.Subject, serverUUID, backupUUID string) (string, error)
}

// Entry is an entry of the activity log about a server and a backup.
type Entry struct {
	Event string
	// ServerID is the server and BackupID the backup the entry is about; ActorID is the user who did it.
	ServerID   uint64
	BackupID   uint64
	ActorID    uint64
	APIKeyID   *uint64
	IP         string
	Properties string
}

// ActivityLog stores activity entries.
type ActivityLog interface {
	Log(ctx context.Context, e Entry) error
}

// The throttle of restoring backups: three in fifteen minutes for each server, whoever restores, as in the reference.
const (
	RestoreLimit  = 3
	RestoreWindow = 15 * time.Minute
)

// Deps are everything the endpoints need.
type Deps struct {
	Store    Store
	Targets  controlapi.Targets
	Daemon   Daemon
	Tokens   Tokens
	Activity ActivityLog
	// RestoreLimiter allows RestoreLimit restores in RestoreWindow for a server.
	RestoreLimiter *middleware.Limiter
	// ThrottleLimit backups may be started in ThrottlePeriod for a server (the reference: 2 in 600 seconds); a period of 0 turns it off.
	ThrottleLimit  int
	ThrottlePeriod time.Duration
	// Location is the time zone of the panel, UTC when nil.
	Location *time.Location
	Logger   *slog.Logger
	// Now and Random default to the clock and crypto/rand.
	Now    func() time.Time
	Random io.Reader
}

// Handlers are the endpoints.
type Handlers struct {
	d   Deps
	svc *Service
}

// New checks the dependencies and returns the endpoints.
func New(d Deps) (*Handlers, error) {
	if d.Store == nil || d.Targets == nil || d.Daemon == nil || d.Tokens == nil || d.Activity == nil || d.RestoreLimiter == nil || d.Logger == nil {
		return nil, errors.New("backupapi: the store, the target store, the daemon, the token maker, the activity log, the limiter and a logger are required")
	}
	if d.Location == nil {
		d.Location = time.UTC
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Random == nil {
		d.Random = rand.Reader
	}
	svc, err := NewService(ServiceDeps{
		Store: d.Store, Targets: d.Targets, Daemon: d.Daemon, ThrottleLimit: d.ThrottleLimit, ThrottlePeriod: d.ThrottlePeriod, Logger: d.Logger, Now: d.Now, Random: d.Random,
	})
	if err != nil {
		return nil, err
	}
	return &Handlers{d: d, svc: svc}, nil
}

// Register adds the routes below /api/client/servers/{server}/backups.
func (h *Handlers) Register(g api.Group) {
	b := g.Sub("/api/client/servers/:server/backups")
	b.GET("", h.List, api.ServerPermission(permissions.BackupRead))
	b.POST("", h.Start, api.ServerPermission(permissions.BackupCreate))
	b.GET("/:backup", h.View, api.ServerPermission(permissions.BackupRead))
	b.GET("/:backup/download", h.Download, api.ServerPermission(permissions.BackupDownload))
	b.POST("/:backup/lock", h.ToggleLock, api.ServerPermission(permissions.BackupDelete))
	b.POST("/:backup/restore", h.Restore, api.ServerPermission(permissions.BackupRestore))
	b.DELETE("/:backup", h.Delete, api.ServerPermission(permissions.BackupDelete))
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

// subject is the caller as the token maker sees them.
type subject struct {
	id   uint64
	uuid string
	held []string
}

func (s subject) ID() uint64                 { return s.id }
func (s subject) UUID() string               { return s.uuid }
func (s subject) Can(permission string) bool { return permissions.Has(s.held, permission) }
func (s subject) Permissions() []string      { return s.held }
