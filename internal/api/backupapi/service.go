package backupapi

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/Helles01/raptor-panel/internal/api/controlapi"
	"github.com/Helles01/raptor-panel/internal/backups"
	"github.com/Helles01/raptor-panel/internal/ids"
)

// StartParams are what a backup is started with.
type StartParams struct {
	// Name is the finished name (backups.Name).
	Name    string
	Ignored []string
	Locked  bool
	// Replace lets a backup that would go over the limit of the server take the place of the oldest one that is not locked, which is what
	// a schedule does. A person who asks for a backup over the limit is told no.
	Replace bool
}

// ServiceDeps are what a Service needs.
type ServiceDeps struct {
	Store   Store
	Targets controlapi.Targets
	Daemon  Daemon
	// ThrottleLimit backups may be started in ThrottlePeriod for a server; a period of 0 turns it off.
	ThrottleLimit  int
	ThrottlePeriod time.Duration
	Logger         *slog.Logger
	// Now and Random default to the clock and crypto/rand.
	Now    func() time.Time
	Random io.Reader
}

// Service starts backups. The endpoint of a user and the scheduler of the panel both go through it, so that the limit, the throttle and
// the locking are the same for both.
type Service struct{ d ServiceDeps }

// NewService checks the dependencies and returns the Service.
func NewService(d ServiceDeps) (*Service, error) {
	if d.Store == nil || d.Targets == nil || d.Daemon == nil || d.Logger == nil {
		return nil, errors.New("backupapi: the store, the target store, the daemon and a logger are required")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Random == nil {
		d.Random = rand.Reader
	}
	return &Service{d: d}, nil
}

// Start stores a backup and has the server's node make it. Counting the backups, the throttle and the insert happen in one transaction
// with the server's backups locked, so that two requests at once cannot both see room (plan 10.1 d). The errors are the store's, a
// *backups.ThrottleError, a *backups.TooManyError, controlapi.ErrNotFound for a server whose node is not known, and what the node
// answered; when the node could not be told the row is taken back.
func (s *Service) Start(ctx context.Context, serverID uint64, p StartParams) (Record, error) {
	uuid, err := ids.UUIDv4(s.d.Random)
	if err != nil {
		return Record{}, err
	}
	started, err := s.d.Store.Start(ctx, StartRequest{
		ServerID: serverID,
		Since:    s.throttleSince(),
		Backup:   NewBackup{UUID: uuid, Name: p.Name, Disk: backups.DiskWings, Ignored: p.Ignored, Locked: p.Locked},
		Check: func(limit *int64, existing []backups.Existing, startedAt []time.Time) (backups.Admission, error) {
			if err := backups.Throttle(startedAt, s.d.Now(), s.d.ThrottleLimit, s.d.ThrottlePeriod); err != nil {
				return backups.Admission{}, err
			}
			return backups.Admit(limit, existing, p.Replace)
		},
	})
	if err != nil {
		return Record{}, err
	}
	rec := started.Record

	target, err := s.d.Targets.For(ctx, serverID)
	if err == nil {
		if started.Pruned != nil {
			// The row of the oldest backup went with the new one; its archive goes from the node too. A node that does not have it
			// is no hindrance.
			if perr := s.d.Daemon.Delete(ctx, target.Node, target.UUID, started.Pruned.UUID); perr != nil {
				s.d.Logger.Warn("the archive of a replaced backup was not deleted on the node", "server", serverID, "backup", started.Pruned.UUID, "error", perr)
			}
		}
		err = s.d.Daemon.Create(ctx, target.Node, target.UUID, rec.Disk, rec.UUID, strings.Join(p.Ignored, "\n"))
	}
	if err != nil {
		if delErr := s.d.Store.Delete(ctx, serverID, rec.ID); delErr != nil {
			s.d.Logger.Error("a backup that could not be started was not taken back", "server", serverID, "backup", rec.UUID, "error", delErr)
		}
		return Record{}, err
	}
	return rec, nil
}

// throttleSince is the start of the window the throttle counts in, or the zero time when it is off.
func (s *Service) throttleSince() time.Time {
	if s.d.ThrottlePeriod <= 0 {
		return time.Time{}
	}
	return s.d.Now().Add(-s.d.ThrottlePeriod)
}
