package store

import (
	"context"
	"fmt"
	"time"

	"github.com/Helles01/raptor-panel/internal/db/dbq"
	"github.com/Helles01/raptor-panel/internal/maintenance"
)

// activityChunk is how many rows of the activity log one statement deletes: a log that has grown for years is not cleared in one lock.
const activityChunk = 5000

// MaintenanceActivity deletes old activity.
func (d *DB) MaintenanceActivity() maintenance.ActivityStore { return housekeepingActivity{d} }

// MaintenanceResetTokens deletes password reset tokens that have run out.
func (d *DB) MaintenanceResetTokens() maintenance.ResetTokenStore { return housekeepingResets{d} }

// MaintenanceSessions deletes sessions that have run out.
func (d *DB) MaintenanceSessions() maintenance.SessionStore { return housekeepingSessions{d} }

type (
	housekeepingActivity struct{ *DB }
	housekeepingResets   struct{ *DB }
	housekeepingSessions struct{ *DB }
)

// DeleteActivityBefore deletes the entries older than the time, a chunk at a time; the subjects of an entry go with it through the foreign key.
func (s housekeepingActivity) DeleteActivityBefore(ctx context.Context, before time.Time) (int64, error) {
	var total int64
	for {
		n, err := s.q.DeleteActivityChunk(ctx, before.UTC())
		if err != nil {
			return total, fmt.Errorf("delete old activity: %w", err)
		}
		total += n
		if n < activityChunk {
			return total, nil
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}

func (s housekeepingResets) DeleteResetTokensBefore(ctx context.Context, before time.Time) (int64, error) {
	n, err := s.q.DeleteResetTokensBefore(ctx, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("delete expired reset tokens: %w", err)
	}
	return n, nil
}

// DeleteExpiredSessions deletes the sessions last used before idleBefore, and those whose payload says they were created before createdBefore (a payload
// that is not Raptor's has no creation time and only the idle rule reaches it).
func (s housekeepingSessions) DeleteExpiredSessions(ctx context.Context, idleBefore, createdBefore time.Time) (int64, error) {
	n, err := s.q.DeleteSpentSessions(ctx, dbq.DeleteSpentSessionsParams{
		LastActivity: int32(idleBefore.Unix()), Payload: createdBefore.UTC().Format("2006-01-02T15:04:05"),
	})
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	return n, nil
}
