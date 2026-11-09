package remoteapi

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// --- backups kept on S3

func TestBackupUploadGivesTheAddressesOfAnS3Backup(t *testing.T) {
	up := &fakeUploads{partSize: 100}
	r := newRig(t, withUploads(up))
	rp := r.get(node1, reportURL(5)+"?size=250")
	if rp.Status != 200 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	// Three parts of 100 bytes for 250, and the id of the upload is kept on the backup for the end of it.
	urls, _ := rp.At("parts").([]any)
	if len(urls) != 3 || rp.At("part_size") != 100.0 {
		t.Fatalf("answer = %s", rp.Raw)
	}
	if !strings.Contains(urls[2].(string), "partNumber=3") || !strings.Contains(urls[0].(string), serverUUID100+"/"+backupUUIDN(5)+".tar.gz") {
		t.Errorf("addresses = %v", urls)
	}
	if want := []fakeBegin{{serverUUID100, backupUUIDN(5), 250}}; !reflect.DeepEqual(up.begins, want) {
		t.Errorf("begins = %+v", up.begins)
	}
	if r.store.uploadIDs[5] != "upload-"+backupUUIDN(5)[:8] {
		t.Errorf("kept upload ids = %v", r.store.uploadIDs)
	}

	// Asking again starts another upload and keeps the new id: the node that lost its addresses is not left without.
	if rp := r.get(node1, reportURL(6)+"?size=50"); rp.Status != 200 || r.store.uploadIDs[6] != "upload-"+backupUUIDN(6)[:8] {
		t.Errorf("a backup that had an upload: %d %s, kept %v", rp.Status, rp.Raw, r.store.uploadIDs)
	}
}

func TestBackupUploadRefusals(t *testing.T) {
	// A panel with S3 settings still answers a backup on the node's own disk as it did.
	up := &fakeUploads{}
	r := newRig(t, withUploads(up))
	if rp := r.get(node1, reportURL(1)+"?size=5"); rp.Status != 400 || rp.Detail() != "The configured backup adapter is not an S3 compatible adapter." {
		t.Errorf("a backup on the node: %d %s", rp.Status, rp.Raw)
	}
	// A backup on S3 on a panel with no S3 settings gets the same answer: there is no way to make addresses.
	r2 := newRig(t)
	if rp := r2.get(node1, reportURL(5)+"?size=5"); rp.Status != 400 || rp.Detail() != "The configured backup adapter is not an S3 compatible adapter." {
		t.Errorf("no S3 settings: %d %s", rp.Status, rp.Raw)
	}
	if rp := r.get(node2, reportURL(5)+"?size=5"); rp.Status != 403 {
		t.Errorf("another node: %d", rp.Status)
	}
	if rp := r.get(node1, reportURL(5)+"?size=0"); rp.Status != 400 {
		t.Errorf("a size of nothing: %d", rp.Status)
	}
	if len(up.begins) != 0 || len(r.store.uploadIDs) != 0 {
		t.Errorf("an upload was started: %+v, kept %v", up.begins, r.store.uploadIDs)
	}

	// A finished backup is not uploaded again, even on S3.
	r.store.backupList[4].Completed = true
	if rp := r.get(node1, reportURL(5)+"?size=5"); rp.Status != 409 || len(up.begins) != 0 {
		t.Errorf("a finished backup: %d %s", rp.Status, rp.Raw)
	}
}

func TestBackupUploadWhenTheStorageFails(t *testing.T) {
	up := &fakeUploads{beginErr: errBoom}
	r := newRig(t, withUploads(up))
	rp := r.get(node1, reportURL(5)+"?size=5")
	if rp.Status != 500 || strings.Contains(string(rp.Raw), "hunter2") || len(r.store.uploadIDs) != 0 {
		t.Errorf("a storage that fails: %d %s", rp.Status, rp.Raw)
	}

	// An archive that would need more parts than an upload has is a 400 that says so, and not a 500.
	up = &fakeUploads{beginErr: ErrTooLarge}
	r = newRig(t, withUploads(up))
	if rp := r.get(node1, reportURL(5)+"?size=5"); rp.Status != 400 || rp.Detail() != ErrTooLarge.Error() {
		t.Errorf("too large: %d %s", rp.Status, rp.Raw)
	}

	// A backup that ended while the upload was being started: the upload is thrown away and the node is told it ended.
	up = &fakeUploads{}
	r = newRig(t, withUploads(up))
	r.store.uploadRace = true
	if rp := r.get(node1, reportURL(5)+"?size=5"); rp.Status != 409 {
		t.Errorf("a race: %d %s", rp.Status, rp.Raw)
	}
	if want := []fakeAbort{{serverUUID100, backupUUIDN(5), "upload-" + backupUUIDN(5)[:8]}}; !reflect.DeepEqual(up.aborts, want) {
		t.Errorf("aborts = %+v", up.aborts)
	}

	// Not being able to keep the id is a failure too, and the upload is thrown away in the same way.
	up = &fakeUploads{}
	r = newRig(t, withUploads(up))
	r.store.failBackups = errBoom
	// OnNode fails first with a store that is down, so the upload is never started.
	if rp := r.get(node1, reportURL(5)+"?size=5"); rp.Status != 500 || len(up.begins) != 0 {
		t.Errorf("a store that fails: %d, begins %+v", rp.Status, up.begins)
	}
}

const s3Report = `{"successful":true,"checksum":"abc123","checksum_type":"sha256","size":1048576,"parts":[{"etag":"\"bbb\"","part_number":2},{"etag":"\"aaa\"","part_number":1}]}`

func TestBackupReportFinishesTheUploadBeforeItStoresTheEnd(t *testing.T) {
	up := &fakeUploads{}
	r := newRig(t, withUploads(up))
	rp := r.post(node1, reportURL(6), s3Report)
	if rp.Status != 204 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	want := []fakeFinish{{serverUUID100, backupUUIDN(6), "up-6", []UploadedPart{{2, `"bbb"`}, {1, `"aaa"`}}, 1048576}}
	if !reflect.DeepEqual(up.finishes, want) {
		t.Errorf("finishes = %+v\nwant       %+v", up.finishes, want)
	}
	if len(r.store.backupDone) != 1 || !r.store.backupDone[0].Result.Successful || r.store.backupDone[0].ID != 6 {
		t.Errorf("stored %+v", r.store.backupDone)
	}

	// A node that does not say which parts it sent: the storage is asked of them (an empty list is what tells it to).
	up = &fakeUploads{}
	r = newRig(t, withUploads(up))
	if rp := r.post(node1, reportURL(6), goodReport); rp.Status != 204 || len(up.finishes) != 1 || len(up.finishes[0].Parts) != 0 {
		t.Errorf("no parts: %d %s, finishes %+v", rp.Status, rp.Raw, up.finishes)
	}
}

func TestBackupReportStoresNothingWhenTheStorageRefusesToFinish(t *testing.T) {
	up := &fakeUploads{finishErr: errBoom}
	r := newRig(t, withUploads(up))
	rp := r.post(node1, reportURL(6), s3Report)
	if rp.Status != 500 || strings.Contains(string(rp.Raw), "hunter2") {
		t.Errorf("answer = %d %s", rp.Status, rp.Raw)
	}
	// The backup stays unfinished, so the node can report again; no entry of the activity log is written either.
	if len(r.store.backupDone) != 0 || len(r.store.events) != 0 {
		t.Errorf("stored %+v, events %+v", r.store.backupDone, r.store.events)
	}
	// And it can: the second try goes through when the storage is well again.
	up.finishErr = nil
	if rp := r.post(node1, reportURL(6), s3Report); rp.Status != 204 || len(r.store.backupDone) != 1 {
		t.Errorf("the second try: %d %s", rp.Status, rp.Raw)
	}
}

func TestBackupReportOfAFailedUploadAbortsIt(t *testing.T) {
	up := &fakeUploads{}
	r := newRig(t, withUploads(up))
	if rp := r.post(node1, reportURL(6), `{"successful":false}`); rp.Status != 204 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if want := []fakeAbort{{serverUUID100, backupUUIDN(6), "up-6"}}; !reflect.DeepEqual(up.aborts, want) || len(up.finishes) != 0 {
		t.Errorf("aborts %+v, finishes %+v", up.aborts, up.finishes)
	}
	if len(r.store.backupDone) != 1 || r.store.backupDone[0].Result.Successful {
		t.Errorf("stored %+v", r.store.backupDone)
	}

	// A storage that cannot abort does not keep a failed backup from being recorded as failed, and the log says what happened.
	up = &fakeUploads{abortErr: errBoom}
	r = newRig(t, withUploads(up))
	if rp := r.post(node1, reportURL(6), `{"successful":false}`); rp.Status != 204 || len(r.store.backupDone) != 1 {
		t.Errorf("an abort that fails: %d %s", rp.Status, rp.Raw)
	}
	if !strings.Contains(r.logs.String(), "was not aborted") {
		t.Errorf("the failed abort was not logged: %s", r.logs.String())
	}

	// A node that failed before it was given addresses has nothing to abort.
	up = &fakeUploads{}
	r = newRig(t, withUploads(up))
	if rp := r.post(node1, reportURL(5), `{"successful":false}`); rp.Status != 204 || len(up.aborts) != 0 || len(r.store.backupDone) != 1 {
		t.Errorf("no upload: %d, aborts %+v", rp.Status, up.aborts)
	}
}

func TestBackupReportOnS3WithoutAnUploadOrSettings(t *testing.T) {
	// Done, but never given addresses: the reference's refusal.
	up := &fakeUploads{}
	r := newRig(t, withUploads(up))
	rp := r.post(node1, reportURL(5), goodReport)
	if rp.Status != 400 || rp.Detail() != "Cannot complete backup request: no upload_id present on model." || len(r.store.backupDone) != 0 || len(up.finishes) != 0 {
		t.Errorf("no upload id: %d %s", rp.Status, rp.Raw)
	}

	// A panel with no S3 settings cannot finish an upload, so a success is not recorded; a failure can be.
	r = newRig(t)
	if rp := r.post(node1, reportURL(6), goodReport); rp.Status != 500 || len(r.store.backupDone) != 0 {
		t.Errorf("no settings, success: %d %s", rp.Status, rp.Raw)
	}
	if rp := r.post(node1, reportURL(6), `{"successful":false}`); rp.Status != 204 || len(r.store.backupDone) != 1 {
		t.Errorf("no settings, failure: %d %s", rp.Status, rp.Raw)
	}
}

func TestBackupReportOnTheNodesOwnDiskDoesNotTouchS3(t *testing.T) {
	up := &fakeUploads{}
	r := newRig(t, withUploads(up))
	if rp := r.post(node1, reportURL(1), goodReport); rp.Status != 204 || len(up.finishes)+len(up.aborts)+len(up.begins) != 0 {
		t.Errorf("a backup on the node: %d %s, S3 calls %+v", rp.Status, rp.Raw, up)
	}
}

func TestBackupReportRefusesPartsThatAreNotParts(t *testing.T) {
	const head = `{"successful":true,"checksum":"a","checksum_type":"sha256","size":5,"parts":`
	for name, body := range map[string]string{
		"a part without a tag":    head + `[{"part_number":1}]}`,
		"an empty tag":            head + `[{"etag":"","part_number":1}]}`,
		"a long tag":              head + `[{"etag":"` + strings.Repeat("a", 257) + `","part_number":1}]}`,
		"a part without a number": head + `[{"etag":"x"}]}`,
		"part 0":                  head + `[{"etag":"x","part_number":0}]}`,
		"part 10001":              head + `[{"etag":"x","part_number":10001}]}`,
		"a tag that is a number":  head + `[{"etag":5,"part_number":1}]}`,
	} {
		up := &fakeUploads{}
		r := newRig(t, withUploads(up))
		rp := r.post(node1, reportURL(6), body)
		if rp.Status != 422 {
			t.Errorf("%s: %d %s", name, rp.Status, rp.Raw)
		}
		if len(up.finishes) != 0 || len(r.store.backupDone) != 0 {
			t.Errorf("%s: something was done", name)
		}
	}

	// Parts that are no list at all are not a report either.
	up := &fakeUploads{}
	r := newRig(t, withUploads(up))
	if rp := r.post(node1, reportURL(6), head+`"all of them"}`); rp.Status == 204 || len(up.finishes) != 0 || len(r.store.backupDone) != 0 {
		t.Errorf("parts that are a string: %d %s", rp.Status, rp.Raw)
	}

	// More parts than an upload may have.
	var b strings.Builder
	b.WriteString(head + "[")
	for i := 1; i <= 10001; i++ {
		if i > 1 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"etag":"e","part_number":%d}`, 1+i%10000)
	}
	b.WriteString("]}")
	r = newRig(t, withUploads(&fakeUploads{}))
	if rp := r.post(node1, reportURL(6), b.String()); rp.Status != 422 {
		t.Errorf("10001 parts: %d", rp.Status)
	}

	// Null parts are no parts, like a report that has none.
	up = &fakeUploads{}
	r = newRig(t, withUploads(up))
	if rp := r.post(node1, reportURL(6), head+`null}`); rp.Status != 204 || len(up.finishes) != 1 {
		t.Errorf("null parts: %d %s", rp.Status, rp.Raw)
	}
}

// A node cannot end the upload of another node's backup, nor start one.
func TestS3RoutesAreForTheNodesOwnBackups(t *testing.T) {
	up := &fakeUploads{}
	r := newRig(t, withUploads(up))
	if rp := r.post(node1, reportURL(7), s3Report); rp.Status != 403 {
		t.Errorf("a report of another node's backup: %d", rp.Status)
	}
	if rp := r.get(node1, reportURL(7)+"?size=5"); rp.Status != 403 {
		t.Errorf("addresses for another node's backup: %d", rp.Status)
	}
	if len(up.begins)+len(up.finishes)+len(up.aborts) != 0 {
		t.Errorf("S3 was touched: %+v", up)
	}
}
