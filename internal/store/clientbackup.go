package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/backupapi"
	"github.com/Helles01/raptor-panel/internal/backups"
	"github.com/Helles01/raptor-panel/internal/db/dbq"
	"github.com/Helles01/raptor-panel/internal/maintenance"
)

// ClientBackups is the backup store of the client API.
func (d *DB) ClientBackups() backupapi.Store { return clientBackups{d} }

// MaintenanceBackups marks the backups that never reported as failed.
func (d *DB) MaintenanceBackups() maintenance.BackupStore { return housekeepingBackups{d} }

type (
	clientBackups       struct{ *DB }
	housekeepingBackups struct{ *DB }
)

const backupSelect = "SELECT `id`, `uuid`, `name`, `disk`, `ignored_files`, `checksum`, `bytes`, `is_successful`, `is_locked`, `created_at`, `completed_at` FROM `backups`"

func scanBackup(r scanner) (backupapi.Record, error) {
	var (
		b              backupapi.Record
		ignored, sum   sql.NullString
		locked         uint8
		created, ended sql.NullTime
	)
	if err := r.Scan(&b.ID, &b.UUID, &b.Name, &b.Disk, &ignored, &sum, &b.Bytes, &b.Successful, &locked, &created, &ended); err != nil {
		return b, err
	}
	b.Locked, b.Checksum, b.CreatedAt, b.CompletedAt = locked != 0, nullStr(sum), at(created), timePtr(ended)
	b.IgnoredFiles = ignoredFiles(ignored.String)
	return b, nil
}

// ignoredFiles reads the patterns a backup leaves out: a JSON list as the reference stores it, or, in a row that was written some other way, one to a line.
func ignoredFiles(text string) []string {
	var out []string
	if json.Unmarshal([]byte(text), &out) == nil {
		return out
	}
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// Page lists the server's backups that were not deleted, in the order they were made, with how many there are and how many of them count against the limit.
func (s clientBackups) Page(ctx context.Context, serverID uint64, offset, limit int) (backupapi.Page, error) {
	var p backupapi.Page
	err := s.sql.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(`is_successful` = 1 OR `completed_at` IS NULL), 0) FROM `backups` WHERE `server_id` = ? AND `deleted_at` IS NULL", serverID).
		Scan(&p.Total, &p.NonFailed)
	if err != nil {
		return p, fmt.Errorf("count the backups of server %d: %w", serverID, err)
	}
	p.Backups, err = all(ctx, s.DB, scanBackup, backupSelect+" WHERE `server_id` = ? AND `deleted_at` IS NULL ORDER BY `id` LIMIT ? OFFSET ?", serverID, limit, offset)
	if err != nil {
		return p, fmt.Errorf("read the backups of server %d: %w", serverID, err)
	}
	return p, nil
}

func (s clientBackups) Find(ctx context.Context, serverID uint64, uuid string) (backupapi.Record, error) {
	b, err := one(ctx, s.DB, scanBackup, backupSelect+" WHERE `server_id` = ? AND `uuid` = ? AND `deleted_at` IS NULL", serverID, uuid)
	if errors.Is(err, appkit.ErrNotFound) {
		return b, backupapi.ErrNotFound
	}
	return b, err
}

// Start counts, checks the throttle and inserts the backup with the server's row locked, so that of requests at once only as many pass as the limit and the
// throttle allow. The backup the check says to replace is deleted in the same transaction.
func (s clientBackups) Start(ctx context.Context, r backupapi.StartRequest) (backupapi.Started, error) {
	if !fits(r.ServerID) {
		return backupapi.Started{}, backupapi.ErrNotFound
	}
	ignored, err := json.Marshal(append([]string{}, r.Backup.Ignored...))
	if err != nil {
		return backupapi.Started{}, fmt.Errorf("encode the ignored files: %w", err)
	}
	var id, pruned uint64
	err = appUsers{s.DB}.retryDeadlock(func() error {
		id, pruned = 0, 0
		return s.inTx(ctx, func(q *dbq.Queries) error {
			limit, err := q.LockServerBackupLimit(ctx, uint32(r.ServerID))
			if errors.Is(err, sql.ErrNoRows) {
				return backupapi.ErrNotFound
			} else if err != nil {
				return fmt.Errorf("lock server %d: %w", r.ServerID, err)
			}
			rows, err := q.NonFailedBackups(ctx, uint32(r.ServerID))
			if err != nil {
				return fmt.Errorf("read the backups of server %d: %w", r.ServerID, err)
			}
			existing := make([]backups.Existing, len(rows))
			for i, row := range rows {
				existing[i] = backups.Existing{ID: row.ID, CreatedAt: at(row.CreatedAt), Locked: row.IsLocked != 0}
			}
			var started []time.Time
			if !r.Since.IsZero() {
				times, err := q.BackupStartTimes(ctx, dbq.BackupStartTimesParams{ServerID: uint32(r.ServerID), CreatedAt: sql.NullTime{Time: r.Since.UTC(), Valid: true}})
				if err != nil {
					return fmt.Errorf("read the start times of the backups of server %d: %w", r.ServerID, err)
				}
				for _, t := range times {
					started = append(started, at(t))
				}
			}
			max := int64(limit)
			admission, err := r.Check(&max, existing, started)
			if err != nil {
				return err
			}
			now := sql.NullTime{Time: s.now().UTC(), Valid: true}
			if admission.Prune {
				n, err := q.SoftDeleteBackup(ctx, dbq.SoftDeleteBackupParams{DeletedAt: now, UpdatedAt: now, ID: admission.PruneID, ServerID: uint32(r.ServerID)})
				if err != nil {
					return fmt.Errorf("delete the backup that is replaced: %w", err)
				}
				if n != 1 {
					return fmt.Errorf("backup %d, which was to be replaced, is not there", admission.PruneID)
				}
				pruned = admission.PruneID
			}
			made, err := q.InsertBackup(ctx, dbq.InsertBackupParams{
				ServerID: uint32(r.ServerID), Uuid: r.Backup.UUID, IsLocked: boolByte(r.Backup.Locked), Name: r.Backup.Name, IgnoredFiles: string(ignored), Disk: r.Backup.Disk,
				CreatedAt: now, UpdatedAt: now,
			})
			if err != nil {
				return fmt.Errorf("store the backup: %w", err)
			}
			id = uint64(made)
			return nil
		})
	})
	if err != nil {
		return backupapi.Started{}, err
	}
	out := backupapi.Started{}
	if out.Record, err = one(ctx, s.DB, scanBackup, backupSelect+" WHERE `id` = ?", id); err != nil {
		return out, fmt.Errorf("read the backup that was made: %w", err)
	}
	if pruned != 0 {
		rec, err := one(ctx, s.DB, scanBackup, backupSelect+" WHERE `id` = ?", pruned)
		if err != nil {
			return out, fmt.Errorf("read the backup that was replaced: %w", err)
		}
		out.Pruned = &rec
	}
	return out, nil
}

func (s clientBackups) SetLocked(ctx context.Context, serverID, backupID uint64, locked bool) error {
	if !fits(serverID, backupID) {
		return backupapi.ErrNotFound
	}
	return s.inTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.LockBackupOfServer(ctx, dbq.LockBackupOfServerParams{ID: backupID, ServerID: uint32(serverID)}); errors.Is(err, sql.ErrNoRows) {
			return backupapi.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock backup %d: %w", backupID, err)
		}
		return q.SetBackupLocked(ctx, dbq.SetBackupLockedParams{IsLocked: boolByte(locked), UpdatedAt: sql.NullTime{Time: s.now().UTC(), Valid: true}, ID: backupID})
	})
}

// Delete deletes the backup softly, as the reference does: the row stays, and the throttle goes on counting it.
func (s clientBackups) Delete(ctx context.Context, serverID, backupID uint64) error {
	if !fits(serverID) {
		return backupapi.ErrNotFound
	}
	now := sql.NullTime{Time: s.now().UTC(), Valid: true}
	n, err := s.q.SoftDeleteBackup(ctx, dbq.SoftDeleteBackupParams{DeletedAt: now, UpdatedAt: now, ID: backupID, ServerID: uint32(serverID)})
	if err != nil {
		return fmt.Errorf("delete backup %d: %w", backupID, err)
	}
	if n == 0 {
		return backupapi.ErrNotFound
	}
	return nil
}

func (s clientBackups) SetServerStatus(ctx context.Context, serverID uint64, from, to string) (bool, error) {
	if !fits(serverID) {
		return false, nil
	}
	return settingsStore{s.DB}.SetStatus(ctx, serverID, from, to)
}

// FailOrphans marks the backups that are unfinished and older than the time as failed and finished. A backup that is done, or deleted, is left as it is, and
// the lock of an unfinished one is not touched. The ones kept on S3 that were given an upload are listed with it, read in the same transaction with the rows
// locked, so that the list is the list of the ones that were marked.
func (s housekeepingBackups) FailOrphans(ctx context.Context, createdBefore, now time.Time) (int64, []maintenance.Upload, error) {
	var n int64
	var uploads []maintenance.Upload
	err := s.inTx(ctx, func(q *dbq.Queries) error {
		rows, err := q.OrphanBackupsToFail(ctx, sql.NullTime{Time: createdBefore.UTC(), Valid: true})
		if err != nil {
			return fmt.Errorf("list the orphaned backups: %w", err)
		}
		for _, r := range rows {
			if r.Disk == backups.DiskS3 && r.UploadID.Valid && r.UploadID.String != "" {
				uploads = append(uploads, maintenance.Upload{ServerUUID: r.ServerUuid, BackupUUID: r.Uuid, UploadID: r.UploadID.String})
			}
		}
		n, err = q.FailOrphanBackups(ctx, dbq.FailOrphanBackupsParams{
			CompletedAt: sql.NullTime{Time: now.UTC(), Valid: true}, UpdatedAt: sql.NullTime{Time: now.UTC(), Valid: true}, CreatedAt: sql.NullTime{Time: createdBefore.UTC(), Valid: true},
		})
		if err != nil {
			return fmt.Errorf("mark the orphaned backups as failed: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	return n, uploads, nil
}
