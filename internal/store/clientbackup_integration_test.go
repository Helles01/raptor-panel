//go:build integration

package store_test

import (
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api/backupapi"
	"github.com/Helles01/raptor-panel/internal/backups"
	"github.com/Helles01/raptor-panel/internal/maintenance"
)

var errBackupLimit = errors.New("backup limit")

// startRequest is a request to start a backup with the rules of the service: the limit counts the backups that are not failed, and with replace the oldest one
// that is not locked makes room.
func startRequest(server uint64, uuid string, since time.Time, replace bool, seen *[]time.Time) backupapi.StartRequest {
	return backupapi.StartRequest{
		ServerID: server, Since: since,
		Backup: backupapi.NewBackup{UUID: uuid, Name: "backup " + uuid, Disk: "wings", Ignored: []string{"*.log", "cache/"}},
		Check: func(limit *int64, existing []backups.Existing, started []time.Time) (backups.Admission, error) {
			if seen != nil {
				*seen = started
			}
			a, err := backups.Admit(limit, existing, replace)
			if err != nil {
				return a, errBackupLimit
			}
			return a, nil
		},
	}
}

func buuid(n int) string { return fmt.Sprintf("bbbbbbbb-0000-4000-8000-%012d", n) }

func TestOwnerBackups(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.ClientBackups()
	r.exec("UPDATE servers SET backup_limit = 2 WHERE id IN (1, 2)")
	t0 := time.Now().UTC().Add(-time.Hour)

	// Start: the record as it is stored; the files to leave out come back as a list; the limit and the others are what the check sees.
	first, err := st.Start(r.ctx, startRequest(1, buuid(1), time.Time{}, false, nil))
	if err != nil || first.Pruned != nil {
		t.Fatalf("first = %+v, %v", first, err)
	}
	rec := first.Record
	if rec.ID == 0 || rec.UUID != buuid(1) || rec.Disk != "wings" || rec.Successful || rec.Locked || rec.Checksum != nil || rec.Bytes != 0 || rec.CompletedAt != nil || rec.CreatedAt.IsZero() ||
		!slices.Equal(rec.IgnoredFiles, []string{"*.log", "cache/"}) {
		t.Errorf("record = %+v", rec)
	}
	var stored string
	r.sql.QueryRow("SELECT ignored_files FROM backups WHERE id = ?", rec.ID).Scan(&stored)
	if stored != `["*.log","cache/"]` {
		t.Errorf("stored ignored files = %q", stored)
	}
	plain := startRequest(1, buuid(2), time.Time{}, false, nil)
	plain.Backup.Ignored, plain.Backup.Locked = nil, true
	second, err := st.Start(r.ctx, plain)
	if err != nil || !second.Record.Locked || second.Record.IgnoredFiles == nil || len(second.Record.IgnoredFiles) != 0 {
		t.Fatalf("second = %+v, %v", second, err)
	}
	if _, err := st.Start(r.ctx, startRequest(1, buuid(3), time.Time{}, false, nil)); !errors.Is(err, errBackupLimit) {
		t.Errorf("over the limit: %v", err)
	}
	if _, err := st.Start(r.ctx, startRequest(99, buuid(4), time.Time{}, false, nil)); !errors.Is(err, backupapi.ErrNotFound) {
		t.Errorf("server 99: %v", err)
	}
	if countWhere(r, "backups", "server_id = 1") != 2 {
		t.Error("a refused start left a backup")
	}

	// A start that may replace takes the place of the oldest one that is not locked, and the replaced one is deleted softly in the same transaction.
	third, err := st.Start(r.ctx, startRequest(1, buuid(5), time.Time{}, true, nil))
	if err != nil || third.Pruned == nil || third.Pruned.ID != rec.ID || third.Pruned.UUID != buuid(1) {
		t.Fatalf("third = %+v, %v", third, err)
	}
	if countWhere(r, "backups", "server_id = 1 AND deleted_at IS NOT NULL") != 1 || countWhere(r, "backups", "server_id = 1 AND deleted_at IS NULL") != 2 {
		t.Error("the replaced backup was not deleted softly")
	}
	// Both of those that are left are locked or the new one, so with the new one locked too nothing may be replaced.
	r.exec("UPDATE backups SET is_locked = 1 WHERE uuid = ?", buuid(5))
	if _, err := st.Start(r.ctx, startRequest(1, buuid(6), time.Time{}, true, nil)); !errors.Is(err, errBackupLimit) {
		t.Errorf("everything locked: %v", err)
	}
	// Failed backups do not count against the limit.
	r.exec("UPDATE backups SET completed_at = NOW(), is_successful = 0 WHERE uuid = ?", buuid(2))
	if _, err := st.Start(r.ctx, startRequest(1, buuid(7), time.Time{}, false, nil)); err != nil {
		t.Errorf("a failed backup counted: %v", err)
	}

	// The throttle gets the creation times from the start of its window on, those of deleted and failed backups included; none when it is off.
	var seen []time.Time
	if _, err := st.Start(r.ctx, startRequest(2, buuid(8), t0, false, &seen)); err != nil || len(seen) != 0 {
		t.Fatalf("start on server 2: %v, saw %v", err, seen)
	}
	r.exec("UPDATE servers SET backup_limit = 9 WHERE id = 1")
	if _, err := st.Start(r.ctx, startRequest(1, buuid(9), t0, false, &seen)); err != nil || len(seen) != 4 {
		t.Errorf("times in the window: %d (%v), %v", len(seen), seen, err)
	}
	r.exec("UPDATE backups SET created_at = ? WHERE uuid IN (?, ?)", t0.Add(-time.Hour), buuid(1), buuid(2))
	if _, err := st.Start(r.ctx, startRequest(1, buuid(10), t0, false, &seen)); err != nil || len(seen) != 3 {
		t.Errorf("times in the window after two went out of it: %d, %v", len(seen), err)
	}
	if _, err := st.Start(r.ctx, startRequest(1, buuid(11), time.Time{}, false, &seen)); err != nil || seen != nil {
		t.Errorf("times with the throttle off: %v, %v", seen, err)
	}
}

func TestOwnerBackupsReadAndChange(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.ClientBackups()
	r.exec("UPDATE servers SET backup_limit = 10 WHERE id IN (1, 2)")
	var ids []uint64
	for i := 1; i <= 5; i++ {
		s, err := st.Start(r.ctx, startRequest(1, buuid(i), time.Time{}, false, nil))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, s.Record.ID)
	}
	other, _ := st.Start(r.ctx, startRequest(2, buuid(20), time.Time{}, false, nil))

	// Kinds: running (no end), good, failed; a deleted one is not shown.
	r.exec("UPDATE backups SET completed_at = NOW(), is_successful = 1, checksum = 'sha256:abc', bytes = 1234 WHERE uuid = ?", buuid(2))
	r.exec("UPDATE backups SET completed_at = NOW(), is_successful = 0 WHERE uuid = ?", buuid(3))
	if err := st.Delete(r.ctx, 1, ids[3]); err != nil {
		t.Fatal(err)
	}
	page, err := st.Page(r.ctx, 1, 0, 50)
	if err != nil || page.Total != 4 || page.NonFailed != 3 || len(page.Backups) != 4 || page.Backups[0].UUID != buuid(1) || page.Backups[1].UUID != buuid(2) || page.Backups[2].UUID != buuid(3) || page.Backups[3].UUID != buuid(5) {
		t.Fatalf("page = %+v, %v", page, err)
	}
	if b := page.Backups[1]; !b.Successful || b.Checksum == nil || *b.Checksum != "sha256:abc" || b.Bytes != 1234 || b.CompletedAt == nil {
		t.Errorf("the good backup = %+v", b)
	}
	if p, _ := st.Page(r.ctx, 1, 1, 1); len(p.Backups) != 1 || p.Backups[0].UUID != buuid(2) || p.Total != 4 {
		t.Errorf("second page of one: %+v", p)
	}
	if p, _ := st.Page(r.ctx, 1, 10, 5); len(p.Backups) != 0 || p.Total != 4 {
		t.Errorf("a page past the end: %+v", p)
	}

	if got, err := st.Find(r.ctx, 1, buuid(2)); err != nil || got.ID != ids[1] {
		t.Errorf("find = %+v, %v", got, err)
	}
	for _, c := range []struct {
		server uint64
		uuid   string
	}{{2, buuid(2)}, {1, buuid(4)}, {1, "nope"}} {
		if _, err := st.Find(r.ctx, c.server, c.uuid); !errors.Is(err, backupapi.ErrNotFound) {
			t.Errorf("find %s on %d: %v", c.uuid, c.server, err)
		}
	}

	// Lock and unlock: only the server's own live backups.
	if err := st.SetLocked(r.ctx, 1, ids[0], true); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Find(r.ctx, 1, buuid(1)); !got.Locked {
		t.Error("not locked")
	}
	if err := st.SetLocked(r.ctx, 1, ids[0], false); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ server, backup uint64 }{{2, ids[0]}, {1, ids[3]}, {1, 9999}, {99, ids[0]}} {
		if err := st.SetLocked(r.ctx, c.server, c.backup, true); !errors.Is(err, backupapi.ErrNotFound) {
			t.Errorf("lock of %d on %d: %v", c.backup, c.server, err)
		}
	}

	// Delete is soft, through the right server, and once.
	if err := st.Delete(r.ctx, 1, other.Record.ID); !errors.Is(err, backupapi.ErrNotFound) {
		t.Errorf("a backup of another server: %v", err)
	}
	if err := st.Delete(r.ctx, 1, ids[3]); !errors.Is(err, backupapi.ErrNotFound) {
		t.Errorf("twice: %v", err)
	}
	if countWhere(r, "backups", "id = "+fmt.Sprint(ids[3])) != 1 {
		t.Error("the row of a deleted backup is gone")
	}

	// The status of the server changes from the status it has, once: of 10 requests at once, one.
	if n := once(t, 10, func() (bool, error) { return st.SetServerStatus(r.ctx, 1, "", "restoring_backup") }); n != 1 {
		t.Errorf("%d requests marked the server as restoring", n)
	}
	if ok, err := st.SetServerStatus(r.ctx, 1, "restoring_backup", ""); !ok || err != nil {
		t.Errorf("back to normal: %v %v", ok, err)
	}
	if ok, err := st.SetServerStatus(r.ctx, 1<<40, "", "restoring_backup"); ok || err != nil {
		t.Errorf("a server out of range: %v %v", ok, err)
	}
}

// Of many requests at once to start a backup on a server that may have one, one is started: the count is read with the server's row locked.
func TestOwnerBackupStartUnderLoad(t *testing.T) {
	f := clientFx(t)
	st := f.r.db.ClientBackups()
	f.r.exec("UPDATE servers SET backup_limit = 1 WHERE id = 1")
	var seq atomic.Int32
	wins := once(t, 12, func() (bool, error) {
		_, err := st.Start(f.r.ctx, startRequest(1, buuid(int(seq.Add(1))), time.Time{}, false, nil))
		if errors.Is(err, errBackupLimit) {
			return false, nil
		}
		return err == nil, err
	})
	if rows := countWhere(f.r, "backups", "server_id = 1"); wins != 1 || rows != 1 {
		t.Errorf("%d backups were started, %d rows, for a limit of 1", wins, rows)
	}

	// With replacing, all of them are started and the server still has one that is not deleted.
	f.r.exec("UPDATE backups SET is_locked = 0")
	wins = once(t, 12, func() (bool, error) {
		_, err := st.Start(f.r.ctx, startRequest(1, buuid(int(seq.Add(1))), time.Time{}, true, nil))
		return err == nil, err
	})
	if live := countWhere(f.r, "backups", "server_id = 1 AND deleted_at IS NULL"); wins != 12 || live != 1 {
		t.Errorf("%d replacing starts won, %d live backups", wins, live)
	}
}

func TestMaintenanceBackups(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.MaintenanceBackups()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	old := now.Add(-7 * time.Hour)
	ins := func(n int, created time.Time) {
		t.Helper()
		r.exec("INSERT INTO backups (server_id, uuid, name, ignored_files, disk, created_at) VALUES (1, ?, 'b', '[]', 'wings', ?)", buuid(n), created)
	}
	ins(1, old)                   // unfinished and old: failed
	ins(2, now.Add(-time.Minute)) // unfinished and new: left
	ins(3, old)                   // finished and good: left
	ins(4, old)                   // unfinished, old and deleted: left
	ins(5, old)                   // unfinished, old and locked: failed, still locked
	r.exec("UPDATE backups SET completed_at = ?, is_successful = 1 WHERE uuid = ?", old.Add(time.Minute), buuid(3))
	r.exec("UPDATE backups SET deleted_at = ? WHERE uuid = ?", old.Add(time.Minute), buuid(4))
	r.exec("UPDATE backups SET is_locked = 1 WHERE uuid = ?", buuid(5))

	n, uploads, err := st.FailOrphans(r.ctx, now.Add(-6*time.Hour), now)
	if err != nil || n != 2 || len(uploads) != 0 {
		t.Fatalf("marked %d, %v", n, err)
	}
	state := func(n int) (ok bool, done bool, locked bool) {
		var end *time.Time
		var good, lock uint8
		r.sql.QueryRow("SELECT is_successful, completed_at, is_locked FROM backups WHERE uuid = ?", buuid(n)).Scan(&good, &end, &lock)
		return good == 1, end != nil, lock == 1
	}
	for _, c := range []struct {
		n                  int
		good, done, locked bool
	}{{1, false, true, false}, {2, false, false, false}, {3, true, true, false}, {4, false, false, false}, {5, false, true, true}} {
		if good, done, locked := state(c.n); good != c.good || done != c.done || locked != c.locked {
			t.Errorf("backup %d: good %v, done %v, locked %v", c.n, good, done, locked)
		}
	}
	var end time.Time
	r.sql.QueryRow("SELECT completed_at FROM backups WHERE uuid = ?", buuid(1)).Scan(&end)
	if !end.Equal(now) {
		t.Errorf("finished at %v, not %v", end, now)
	}
	if n, _, _ := st.FailOrphans(r.ctx, now.Add(-6*time.Hour), now); n != 0 {
		t.Errorf("marked %d the second time", n)
	}
}

// A backup on S3 that never finished has an upload open in the bucket: the job is told which, and only for the ones it marked.
func TestMaintenanceBackupsListTheOpenUploadsOnS3(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.MaintenanceBackups()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	old := now.Add(-7 * time.Hour)
	ins := func(n int, disk string, created time.Time, upload any) {
		t.Helper()
		r.exec("INSERT INTO backups (server_id, uuid, name, ignored_files, disk, created_at, upload_id) VALUES (1, ?, 'b', '[]', ?, ?, ?)", buuid(n), disk, created, upload)
	}
	ins(1, "s3", old, "up-1")                   // on S3, old, with an upload: failed, listed
	ins(2, "s3", old, nil)                      // on S3, old, never given addresses: failed, nothing to abort
	ins(3, "s3", now.Add(-time.Minute), "up-3") // on S3, new: left alone, not listed
	ins(4, "wings", old, "left-over")           // on the node, whatever its upload id: failed, not listed
	ins(5, "s3", old, "up-5")                   // on S3, old, but done: left alone, not listed
	ins(6, "s3", old, "up-6")                   // on S3, old, deleted: left alone, not listed
	ins(7, "s3", old, "")                       // on S3, old, an empty id: failed, not listed
	r.exec("UPDATE backups SET completed_at = ?, is_successful = 1 WHERE uuid = ?", old.Add(time.Minute), buuid(5))
	r.exec("UPDATE backups SET deleted_at = ? WHERE uuid = ?", old.Add(time.Minute), buuid(6))

	n, uploads, err := st.FailOrphans(r.ctx, now.Add(-6*time.Hour), now)
	if err != nil || n != 4 {
		t.Fatalf("marked %d, %v", n, err)
	}
	if len(uploads) != 1 || uploads[0] != (maintenance.Upload{ServerUUID: uuid1, BackupUUID: buuid(1), UploadID: "up-1"}) {
		t.Errorf("uploads = %+v", uploads)
	}
	// The second time there is nothing to mark and nothing to abort.
	if n, uploads, _ := st.FailOrphans(r.ctx, now.Add(-6*time.Hour), now); n != 0 || len(uploads) != 0 {
		t.Errorf("the second time: %d, %+v", n, uploads)
	}
}
