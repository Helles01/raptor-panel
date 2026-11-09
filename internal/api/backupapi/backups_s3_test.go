package backupapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/backups"
)

// fakeArchives is the bucket: it makes up the addresses and remembers what it was asked.
type fakeArchives struct {
	mu      sync.Mutex
	asked   []archiveAsk
	deleted [][2]string
	urlErr  error
	delErr  error
}

type archiveAsk struct {
	Server, Backup string
	Valid          time.Duration
}

func (a *fakeArchives) DownloadURL(serverUUID, backupUUID string, valid time.Duration) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked = append(a.asked, archiveAsk{serverUUID, backupUUID, valid})
	if a.urlErr != nil {
		return "", a.urlErr
	}
	return fmt.Sprintf("https://bucket.example.com/%s/%s.tar.gz?X-Amz-Signature=sig", serverUUID, backupUUID), nil
}

func (a *fakeArchives) Delete(_ context.Context, serverUUID, backupUUID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.deleted = append(a.deleted, [2]string{serverUUID, backupUUID})
	return a.delErr
}

// withS3 gives the panel the bucket, and says which disk new backups go to.
func withS3(a *fakeArchives, disk string) func(*Deps) {
	return func(d *Deps) { d.Archives, d.Disk = a, disk }
}

func signedURL(serverUUID, backupUUID string) string {
	return fmt.Sprintf("https://bucket.example.com/%s/%s.tar.gz?X-Amz-Signature=sig", serverUUID, backupUUID)
}

func TestDownloadFromS3GivesASignedAddressOfTheBucket(t *testing.T) {
	a := &fakeArchives{}
	r := newRig(t, withS3(a, "wings"))
	rp := r.do(bob, "GET", "/"+uuidN(5)+"/download", "")
	if rp.Status != 200 || rp.At("object") != "signed_url" || rp.At("attributes", "url") != signedURL(serverUUID, uuidN(5)) {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if want := []archiveAsk{{serverUUID, uuidN(5), 5 * time.Minute}}; fmt.Sprint(a.asked) != fmt.Sprint(want) {
		t.Errorf("asked %+v, want %+v", a.asked, want)
	}
	if rp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rp.Header.Get("Cache-Control"))
	}
	// The node is not asked for anything: the archive is not there.
	if len(r.daemon.calls) != 0 {
		t.Errorf("node calls = %+v", r.daemon.calls)
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:backup.download" || r.activity.entries[0].Properties != `{"name":"On S3"}` {
		t.Errorf("entries = %+v", r.activity.entries)
	}
	// The address is not in the log.
	if strings.Contains(r.logs.String(), "X-Amz-Signature") {
		t.Errorf("a signed address is in the log: %s", r.logs.String())
	}
}

func TestDownloadFromS3Refusals(t *testing.T) {
	a := &fakeArchives{}
	r := newRig(t, withS3(a, "wings"))
	// A backup that is not finished is refused before any address is made.
	r.store.records[4].CompletedAt = nil
	if rp := r.do(bob, "GET", "/"+uuidN(5)+"/download", ""); rp.Status != 400 || len(a.asked) != 0 {
		t.Errorf("a running backup on S3: %d %s, asked %+v", rp.Status, rp.Raw, a.asked)
	}
	// Without the right to download nothing is signed either.
	if rp := r.do(cleo, "GET", "/"+uuidN(5)+"/download", ""); rp.Status != 403 || len(a.asked) != 0 {
		t.Errorf("a user who may not download: %d, asked %+v", rp.Status, a.asked)
	}

	// A bucket that cannot sign: a 500 that does not say why, and no entry.
	a = &fakeArchives{urlErr: errors.New("the clock is wrong: secret=hunter2")}
	r = newRig(t, withS3(a, "wings"))
	rp := r.do(bob, "GET", "/"+uuidN(5)+"/download", "")
	if rp.Status != 500 || strings.Contains(string(rp.Raw), "hunter2") || len(r.activity.entries) != 0 {
		t.Errorf("a bucket that fails: %d %s", rp.Status, rp.Raw)
	}

	// A backup on a disk nobody knows is refused as before, with or without a bucket.
	r = newRig(t, withS3(&fakeArchives{}, "wings"))
	r.store.records[4].Disk = "floppy"
	if rp := r.do(bob, "GET", "/"+uuidN(5)+"/download", ""); rp.Status != 400 || rp.Detail() != errUnsupportedDisk.Error() {
		t.Errorf("an unknown disk: %d %s", rp.Status, rp.Raw)
	}
}

func TestRestoreFromS3GivesTheNodeAnAddress(t *testing.T) {
	a := &fakeArchives{}
	r := newRig(t, withS3(a, "wings"))
	rp := r.do(bob, "POST", "/"+uuidN(5)+"/restore", `{"truncate":true}`)
	if rp.Status != 204 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	want := daemonCall{Kind: "restore", ServerUUID: serverUUID, BackupUUID: uuidN(5), Disk: "s3", Truncate: true, URL: signedURL(serverUUID, uuidN(5))}
	if len(r.daemon.calls) != 1 || r.daemon.calls[0] != want {
		t.Errorf("calls = %+v\nwant   %+v", r.daemon.calls, want)
	}
	if len(a.asked) != 1 || a.asked[0].Valid != 5*time.Minute {
		t.Errorf("asked %+v", a.asked)
	}
	// A restore of a backup on the node is told no address, as before.
	r.daemon.calls = nil
	if rp := r.do(bob, "POST", "/"+uuidN(1)+"/restore", `{"truncate":false}`); rp.Status != 204 || r.daemon.calls[0].URL != "" || r.daemon.calls[0].Disk != "wings" {
		t.Errorf("a backup on the node: %d, calls %+v", rp.Status, r.daemon.calls)
	}
}

func TestRestoreFromS3WhenTheBucketCannotSign(t *testing.T) {
	a := &fakeArchives{urlErr: errors.New("no key")}
	r := newRig(t, withS3(a, "wings"))
	rp := r.do(bob, "POST", "/"+uuidN(5)+"/restore", `{"truncate":false}`)
	if rp.Status != 500 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	// Nothing was changed: the server was not marked as restoring and the node was not told.
	if len(r.store.statuses) != 0 || len(r.daemon.calls) != 0 || len(r.activity.entries) != 0 {
		t.Errorf("statuses %v, calls %+v, entries %+v", r.store.statuses, r.daemon.calls, r.activity.entries)
	}
}

func TestDeleteFromS3RemovesTheArchiveThenTheRow(t *testing.T) {
	a := &fakeArchives{}
	r := newRig(t, withS3(a, "wings"))
	rp := r.do(bob, "DELETE", "/"+uuidN(5), "")
	if rp.Status != 204 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if len(a.deleted) != 1 || a.deleted[0] != [2]string{serverUUID, uuidN(5)} {
		t.Errorf("deleted from the bucket: %v", a.deleted)
	}
	if len(r.store.deleted) != 1 || r.store.deleted[0] != 5 {
		t.Errorf("rows deleted: %v", r.store.deleted)
	}
	if len(r.daemon.calls) != 0 {
		t.Errorf("the node was asked: %+v", r.daemon.calls)
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:backup.delete" {
		t.Errorf("entries = %+v", r.activity.entries)
	}
}

// A bucket that refuses leaves the row: the backup is still listed and can be deleted again, rather than being an archive nobody can see.
func TestDeleteFromS3WhenTheBucketRefuses(t *testing.T) {
	a := &fakeArchives{delErr: errors.New("access denied")}
	r := newRig(t, withS3(a, "wings"))
	rp := r.do(bob, "DELETE", "/"+uuidN(5), "")
	if rp.Status != 500 || len(r.store.deleted) != 0 || len(r.activity.entries) != 0 {
		t.Errorf("answer = %d %s, rows deleted %v", rp.Status, rp.Raw, r.store.deleted)
	}
	a.delErr = nil
	if rp := r.do(bob, "DELETE", "/"+uuidN(5), ""); rp.Status != 204 || len(r.store.deleted) != 1 {
		t.Errorf("the second try: %d", rp.Status)
	}
}

func TestDeleteFromS3KeepsTheRulesOfLocks(t *testing.T) {
	a := &fakeArchives{}
	r := newRig(t, withS3(a, "wings"))
	r.store.records[4].Locked = true
	rp := r.do(bob, "DELETE", "/"+uuidN(5), "")
	if rp.Status == 204 || len(a.deleted) != 0 || len(r.store.deleted) != 0 {
		t.Errorf("a locked backup on S3 was deleted: %d %s", rp.Status, rp.Raw)
	}
}

func TestBackupsOnS3AreStillRefusedWithoutABucket(t *testing.T) {
	r := newRig(t)
	for name, c := range map[string]struct{ method, path, body string }{
		"download": {"GET", "/" + uuidN(5) + "/download", ""},
		"restore":  {"POST", "/" + uuidN(5) + "/restore", `{"truncate":false}`},
		"delete":   {"DELETE", "/" + uuidN(5), ""},
	} {
		if rp := r.do(bob, c.method, c.path, c.body); rp.Status != 400 || rp.Detail() != errUnsupportedDisk.Error() {
			t.Errorf("%s: %d %s", name, rp.Status, rp.Raw)
		}
	}
	if len(r.daemon.calls) != 0 || len(r.store.deleted) != 0 {
		t.Errorf("something was done: %+v %v", r.daemon.calls, r.store.deleted)
	}
}

func TestNewBackupsGoToTheDiskThatIsSet(t *testing.T) {
	a := &fakeArchives{}
	r := newRig(t, withS3(a, "s3"))
	rp := r.do(bob, "POST", "", `{"name":"Cloud"}`)
	if rp.Status != 200 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	// The row says s3, and so does what the node is told: the node uploads to the bucket instead of keeping the file.
	last := r.store.records[len(r.store.records)-1]
	if last.Disk != "s3" || len(r.daemon.calls) != 1 || r.daemon.calls[0].Kind != "create" || r.daemon.calls[0].Disk != "s3" {
		t.Errorf("record %+v, calls %+v", last, r.daemon.calls)
	}

	// The default is the node's own disk.
	r = newRig(t)
	if rp := r.do(bob, "POST", "", `{"name":"Local"}`); rp.Status != 200 || r.daemon.calls[0].Disk != "wings" {
		t.Errorf("the default: %d, calls %+v", rp.Status, r.daemon.calls)
	}
}

func TestNewNeedsTheBucketForS3(t *testing.T) {
	r := newRig(t)
	for name, d := range map[string]Deps{
		"s3 without a bucket": {Disk: "s3"},
		"a disk nobody knows": {Disk: "floppy"},
	} {
		_, err := NewService(ServiceDeps{Store: r.store, Targets: r.targets, Daemon: r.daemon, Disk: d.Disk, Archives: d.Archives, Logger: slog.New(slog.NewTextHandler(r.logs, nil))})
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := NewService(ServiceDeps{Store: r.store, Targets: r.targets, Daemon: r.daemon, Disk: backups.DiskS3, Archives: &fakeArchives{}, Logger: slog.New(slog.NewTextHandler(r.logs, nil))}); err != nil {
		t.Errorf("s3 with a bucket: %v", err)
	}
}

// A schedule that replaces the oldest backup removes its archive from where it is: the bucket for one on S3, the node for one on the node.
func TestReplacingTheOldestBackupRemovesItsArchiveWhereItIs(t *testing.T) {
	// The oldest that is not locked is 5, on S3.
	a := &fakeArchives{}
	r := newRig(t, withS3(a, "wings"))
	r.store.limit = ptr(int64(4))
	s, err := NewService(ServiceDeps{
		Store: r.store, Targets: r.targets, Daemon: r.daemon, Archives: a, Disk: "wings", ThrottleLimit: 2, ThrottlePeriod: 600 * time.Second,
		Logger: slog.New(slog.NewTextHandler(r.logs, nil)), Now: func() time.Time { return t0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(context.Background(), 100, StartParams{Name: "Scheduled", Replace: true}); err != nil {
		t.Fatal(err)
	}
	if len(a.deleted) != 1 || a.deleted[0] != [2]string{serverUUID, uuidN(5)} {
		t.Errorf("deleted from the bucket: %v", a.deleted)
	}
	if r.daemon.count("delete") != 0 || r.daemon.count("create") != 1 {
		t.Errorf("node calls = %+v", r.daemon.calls)
	}

	// A bucket that cannot delete it is only logged, and the new backup is made all the same.
	a = &fakeArchives{delErr: errors.New("denied")}
	r = newRig(t, withS3(a, "wings"))
	r.store.limit = ptr(int64(4))
	s, _ = NewService(ServiceDeps{
		Store: r.store, Targets: r.targets, Daemon: r.daemon, Archives: a, Disk: "wings", ThrottleLimit: 2, ThrottlePeriod: 600 * time.Second,
		Logger: slog.New(slog.NewTextHandler(r.logs, nil)), Now: func() time.Time { return t0 },
	})
	if _, err := s.Start(context.Background(), 100, StartParams{Name: "Scheduled", Replace: true}); err != nil || !strings.Contains(r.logs.String(), "was not deleted from S3") {
		t.Errorf("err %v, log %s", err, r.logs.String())
	}

	// With the disk set to S3 a replaced backup that is on the node is still deleted on the node.
	a = &fakeArchives{}
	r = newRig(t, withS3(a, "s3"))
	r.store.limit = ptr(int64(4))
	r.store.records[4].Disk = "wings"
	s, _ = NewService(ServiceDeps{
		Store: r.store, Targets: r.targets, Daemon: r.daemon, Archives: a, Disk: "s3", ThrottleLimit: 2, ThrottlePeriod: 600 * time.Second,
		Logger: slog.New(slog.NewTextHandler(r.logs, nil)), Now: func() time.Time { return t0 },
	})
	if _, err := s.Start(context.Background(), 100, StartParams{Name: "Scheduled", Replace: true}); err != nil {
		t.Fatal(err)
	}
	if r.daemon.count("delete") != 1 || len(a.deleted) != 0 || r.daemon.calls[len(r.daemon.calls)-1].Disk != "s3" {
		t.Errorf("node calls = %+v, bucket %v", r.daemon.calls, a.deleted)
	}
}
