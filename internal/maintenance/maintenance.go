// Package maintenance does the housekeeping the panel owes its database, on a timer: marking backups that never reported as failed,
// deleting old activity, reset tokens and sessions. Each job is a function over a small store interface; the Scheduler runs them, each
// in a loop of its own so that a job never overlaps with itself and a slow one holds back no other.
package maintenance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Helles01/raptor-panel/internal/schedules"
)

// The defaults of the reference panel's settings.
const (
	// DefaultBackupPruneAge is how long a backup may be unfinished before it is taken for lost (backups.prune_age, 360 minutes).
	DefaultBackupPruneAge = 360 * time.Minute
	// DefaultActivityKeep is how long the activity log is kept (activity.prune_days, 90 days).
	DefaultActivityKeep = 90 * 24 * time.Hour
	// ResetTokenLifetime is how long a password reset token can be used; later ones are no use and are deleted.
	ResetTokenLifetime = time.Hour
)

// Job is a piece of housekeeping and how often it is done.
type Job struct {
	Name  string
	Every time.Duration
	// Run does the work. It returns a line about what it did when it did something, and nothing when there was nothing to do.
	Run func(ctx context.Context, now time.Time) (report string, err error)
}

// Upload is the multipart upload of a backup kept on S3 that was never finished.
type Upload struct {
	ServerUUID, BackupUUID, UploadID string
}

// BackupStore marks backups that never reported.
type BackupStore interface {
	// FailOrphans marks every backup that has no end time and was created before the given time as failed, finished now, and returns how
	// many it marked, and the open uploads of the ones kept on S3. A backup that is done is left alone, and so is one that was deleted; the lock of an
	// unfinished one is not touched.
	FailOrphans(ctx context.Context, createdBefore, now time.Time) (int64, []Upload, error)
}

// UploadAborter throws away an upload on S3. An upload that is not there is no error.
type UploadAborter interface {
	Abort(ctx context.Context, serverUUID, backupUUID, uploadID string) error
}

// PruneOrphanedBackups marks the backups that have been unfinished for longer than age as failed, so that they stop counting against the
// limit of their server and are shown as failed. The node reports the end of every backup, so one that has not by now never will. Every
// half hour, as in the reference. A backup on S3 that never finished has an upload open in the bucket, whose parts stay there and are paid for until it
// is aborted, which the reference leaves to the bucket's own rules; here it is thrown away when aborter is set, and what could not be is in the report.
func PruneOrphanedBackups(s BackupStore, age time.Duration, aborter UploadAborter) Job {
	return Job{Name: "orphaned backups", Every: 30 * time.Minute, Run: func(ctx context.Context, now time.Time) (string, error) {
		n, uploads, err := s.FailOrphans(ctx, now.Add(-age), now)
		report := counted(n, "backups that never finished were marked as failed")
		if err != nil || aborter == nil || len(uploads) == 0 {
			return report, err
		}
		failed := 0
		for _, u := range uploads {
			if aerr := aborter.Abort(ctx, u.ServerUUID, u.BackupUUID, u.UploadID); aerr != nil {
				failed++
			}
		}
		report += fmt.Sprintf("; %d open uploads on S3 were aborted", len(uploads)-failed)
		if failed > 0 {
			report += fmt.Sprintf(" and %d could not be", failed)
		}
		return report, nil
	}}
}

// ActivityStore deletes old activity.
type ActivityStore interface {
	DeleteActivityBefore(ctx context.Context, before time.Time) (int64, error)
}

// PruneActivity deletes the activity log entries older than keep. Daily.
func PruneActivity(s ActivityStore, keep time.Duration) Job {
	return Job{Name: "activity", Every: 24 * time.Hour, Run: func(ctx context.Context, now time.Time) (string, error) {
		n, err := s.DeleteActivityBefore(ctx, now.Add(-keep))
		return counted(n, "old activity log entries were deleted"), err
	}}
}

// ResetTokenStore deletes password reset tokens.
type ResetTokenStore interface {
	DeleteResetTokensBefore(ctx context.Context, before time.Time) (int64, error)
}

// PruneResetTokens deletes the password reset tokens that have run out. Hourly.
func PruneResetTokens(s ResetTokenStore) Job {
	return Job{Name: "reset tokens", Every: time.Hour, Run: func(ctx context.Context, now time.Time) (string, error) {
		n, err := s.DeleteResetTokensBefore(ctx, now.Add(-ResetTokenLifetime))
		return counted(n, "expired password reset tokens were deleted"), err
	}}
}

// SessionStore deletes sessions that have run out.
type SessionStore interface {
	// DeleteExpiredSessions deletes the sessions that were last used before idleBefore or created before createdBefore.
	DeleteExpiredSessions(ctx context.Context, idleBefore, createdBefore time.Time) (int64, error)
}

// PruneSessions deletes the sessions that have outlived their idle timeout or their lifetime. They would be refused anyway; this is for
// the size of the table. Hourly.
func PruneSessions(s SessionStore, idle, lifetime time.Duration) Job {
	return Job{Name: "sessions", Every: time.Hour, Run: func(ctx context.Context, now time.Time) (string, error) {
		n, err := s.DeleteExpiredSessions(ctx, now.Add(-idle), now.Add(-lifetime))
		return counted(n, "expired sessions were deleted"), err
	}}
}

func counted(n int64, what string) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("%d %s", n, what)
}

// Scheduler runs jobs.
type Scheduler struct {
	jobs  []Job
	clock schedules.Clock
	log   *slog.Logger
}

// NewScheduler checks the jobs and returns a Scheduler. clock is the time it goes by (taskexec.SystemClock in production).
func NewScheduler(jobs []Job, clock schedules.Clock, log *slog.Logger) (*Scheduler, error) {
	if clock == nil || log == nil {
		return nil, errors.New("maintenance: a clock and a logger are required")
	}
	seen := map[string]bool{}
	for _, j := range jobs {
		switch {
		case j.Name == "" || j.Run == nil:
			return nil, errors.New("maintenance: a job needs a name and something to run")
		case j.Every <= 0:
			return nil, fmt.Errorf("maintenance: the job %q needs an interval", j.Name)
		case seen[j.Name]:
			return nil, fmt.Errorf("maintenance: the job %q is there twice", j.Name)
		}
		seen[j.Name] = true
	}
	return &Scheduler{jobs: append([]Job(nil), jobs...), clock: clock, log: log}, nil
}

// Run does every job at once and then every Every, until the context ends, and returns when the jobs that were running have finished. A
// job that fails or panics is logged and tried again at its next time; it stops nothing.
func (s *Scheduler) Run(ctx context.Context) {
	done := make(chan struct{}, len(s.jobs))
	for _, j := range s.jobs {
		go func() {
			defer func() { done <- struct{}{} }()
			s.loop(ctx, j)
		}()
	}
	for range s.jobs {
		<-done
	}
}

func (s *Scheduler) loop(ctx context.Context, j Job) {
	for {
		s.once(ctx, j)
		if !s.clock.Wait(ctx, j.Every) {
			return
		}
	}
}

// once runs a job one time.
func (s *Scheduler) once(ctx context.Context, j Job) {
	defer func() {
		if p := recover(); p != nil {
			s.log.Error("a maintenance job panicked", "job", j.Name, "panic", fmt.Sprint(p))
		}
	}()
	report, err := j.Run(ctx, s.clock.Now())
	switch {
	case err != nil && ctx.Err() == nil:
		s.log.Error("a maintenance job failed", "job", j.Name, "error", err)
	case report != "":
		s.log.Info("maintenance", "job", j.Name, "did", report)
	}
}
