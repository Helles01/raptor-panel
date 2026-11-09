package app

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api/remoteapi"
	"github.com/Helles01/raptor-panel/internal/s3"
)

const (
	s3Server = "11111111-1111-4111-8111-111111111111"
	s3Backup = "22222222-2222-4222-8222-222222222222"
	s3Key    = "/bkt/" + s3Server + "/" + s3Backup + ".tar.gz"
)

// bucket is a server that answers the few S3 requests the adapter makes. It does not check signatures: that is the package s3's own test.
type bucket struct {
	mu       sync.Mutex
	requests []string
	// objectSize is what HEAD says; -1 for an object that is not there.
	objectSize int64
	// completeStatus is 0 for success, or the status with the code NoSuchUpload.
	completeNoSuchUpload bool
	parts                []s3.Part
	completed            string
	denyDelete           bool
}

func newBucket(t *testing.T) (*bucket, s3Backups) {
	t.Helper()
	b := &bucket{objectSize: -1}
	srv := httptest.NewServer(b)
	t.Cleanup(srv.Close)
	c, err := s3.New(s3.Config{Endpoint: srv.URL, Region: "us-east-1", Bucket: "bkt", PathStyle: true, AccessKey: "AKIAEXAMPLE", SecretKey: "secret-key-value"}, s3.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return b, s3Backups{client: c, partSize: 100, validity: time.Hour}
}

func (b *bucket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.requests = append(b.requests, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
	q := r.URL.Query()
	fail := func(status int, code string) {
		w.WriteHeader(status)
		fmt.Fprintf(w, `<Error><Code>%s</Code><Message>m</Message></Error>`, code)
	}
	switch {
	case r.Method == "POST" && q.Has("uploads"):
		fmt.Fprint(w, `<InitiateMultipartUploadResult><UploadId>UP-1</UploadId></InitiateMultipartUploadResult>`)
	case r.Method == "POST" && q.Has("uploadId"):
		if b.completeNoSuchUpload {
			fail(404, "NoSuchUpload")
			return
		}
		b.completed = string(body)
		fmt.Fprint(w, `<CompleteMultipartUploadResult/>`)
	case r.Method == "GET" && q.Has("uploadId"):
		fmt.Fprint(w, `<ListPartsResult><IsTruncated>false</IsTruncated>`)
		for _, p := range b.parts {
			fmt.Fprintf(w, `<Part><PartNumber>%d</PartNumber><ETag>%s</ETag></Part>`, p.Number, p.ETag)
		}
		fmt.Fprint(w, `</ListPartsResult>`)
	case r.Method == "DELETE" && q.Has("uploadId"):
		if q.Get("uploadId") == "gone" {
			fail(404, "NoSuchUpload")
			return
		}
		w.WriteHeader(204)
	case r.Method == "DELETE":
		if b.denyDelete {
			fail(403, "AccessDenied")
			return
		}
		w.WriteHeader(204)
	case r.Method == "HEAD":
		if b.objectSize < 0 {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(b.objectSize))
	default:
		fail(501, "NotImplemented")
	}
}

func (b *bucket) calls() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.requests...)
}

func TestS3BackupsBeginSignsOneAddressForEachPart(t *testing.T) {
	b, a := newBucket(t)
	for size, want := range map[int64]int{1: 1, 99: 1, 100: 1, 101: 2, 250: 3, 300: 3, 301: 4} {
		up, err := a.Begin(context.Background(), s3Server, s3Backup, size)
		if err != nil || len(up.PartURLs) != want || up.ID != "UP-1" || up.PartSize != 100 {
			t.Fatalf("size %d: %+v, %v (want %d parts)", size, up, err, want)
		}
	}
	up, _ := a.Begin(context.Background(), s3Server, s3Backup, 250)
	for i, raw := range up.PartURLs {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		// The address is of the bucket's server, for the key of the archive, with the part and the upload, signed for the time that is set.
		if u.Path != s3Key || q.Get("partNumber") != fmt.Sprint(i+1) || q.Get("uploadId") != "UP-1" || q.Get("X-Amz-Expires") != "3600" || q.Get("X-Amz-Signature") == "" {
			t.Errorf("address %d = %s", i+1, raw)
		}
		if strings.Contains(raw, "secret-key-value") {
			t.Errorf("the secret is in the address: %s", raw)
		}
	}
	// Starting the upload is the only request made: the addresses are signed here.
	for _, c := range b.calls() {
		if !strings.HasPrefix(c, "POST "+s3Key+"?uploads=") {
			t.Errorf("an unexpected request: %s", c)
		}
	}
}

func TestS3BackupsBeginRefusesAnArchiveOfTooManyParts(t *testing.T) {
	b, a := newBucket(t)
	a.partSize = s3.MinPartSize
	_, err := a.Begin(context.Background(), s3Server, s3Backup, int64(s3.MaxParts)*s3.MinPartSize+1)
	if !errors.Is(err, remoteapi.ErrTooLarge) {
		t.Errorf("err = %v", err)
	}
	if len(b.calls()) != 0 {
		t.Errorf("an upload was started for an archive that cannot be sent: %v", b.calls())
	}
	// Exactly the most parts is allowed.
	if up, err := a.Begin(context.Background(), s3Server, s3Backup, int64(s3.MaxParts)*s3.MinPartSize); err != nil || len(up.PartURLs) != s3.MaxParts {
		t.Errorf("the most parts: %d, %v", len(up.PartURLs), err)
	}
}

func TestS3BackupsBeginWhenTheBucketRefuses(t *testing.T) {
	_, a := newBucket(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
		fmt.Fprint(w, `<Error><Code>AccessDenied</Code><Message>no</Message></Error>`)
	}))
	defer srv.Close()
	c, err := s3.New(s3.Config{Endpoint: srv.URL, Region: "us-east-1", Bucket: "bkt", PathStyle: true, AccessKey: "AKIAEXAMPLE", SecretKey: "secret-key-value"}, s3.Options{})
	if err != nil {
		t.Fatal(err)
	}
	a.client = c
	if _, err := a.Begin(context.Background(), s3Server, s3Backup, 5); !s3.IsCode(err, "AccessDenied") {
		t.Errorf("err = %v", err)
	}
}

func TestS3BackupsFinish(t *testing.T) {
	b, a := newBucket(t)
	err := a.Finish(context.Background(), s3Server, s3Backup, "UP-1", []remoteapi.UploadedPart{{Number: 2, ETag: `"b"`}, {Number: 1, ETag: `"a"`}}, 10)
	if err != nil {
		t.Fatal(err)
	}
	// The parts are put together in order, with the tags the node gave.
	if got := completedParts(t, b.completed); fmt.Sprint(got) != fmt.Sprint([]s3.Part{{Number: 1, ETag: `"a"`}, {Number: 2, ETag: `"b"`}}) {
		t.Errorf("the parts = %v", got)
	}
	for _, c := range b.calls() {
		if strings.HasPrefix(c, "GET") {
			t.Errorf("the parts were listed although the node gave them: %v", b.calls())
		}
	}

	// No parts from the node: they are asked of the bucket.
	b, a = newBucket(t)
	b.parts = []s3.Part{{Number: 1, ETag: `"x"`}, {Number: 2, ETag: `"y"`}}
	if err := a.Finish(context.Background(), s3Server, s3Backup, "UP-1", nil, 10); err != nil || fmt.Sprint(completedParts(t, b.completed)) != fmt.Sprint(b.parts) {
		t.Errorf("listed parts: %v, body %s", err, b.completed)
	}
	// An upload with no parts at all cannot be finished.
	b, a = newBucket(t)
	if err := a.Finish(context.Background(), s3Server, s3Backup, "UP-1", nil, 10); err == nil || b.completed != "" {
		t.Errorf("no parts anywhere: %v", err)
	}
}

// A report that is repeated because its answer was lost finds the upload gone: the object is looked at, and if it is there and the size is
// the one the node says, that is success.
func TestS3BackupsFinishRecognisesAnUploadThatWasAlreadyFinished(t *testing.T) {
	parts := []remoteapi.UploadedPart{{Number: 1, ETag: `"a"`}}
	for name, tc := range map[string]struct {
		object int64
		size   int64
		ok     bool
	}{
		"the object is there, the size agrees":       {object: 1234, size: 1234, ok: true},
		"the object is there, the node gave no size": {object: 1234, size: 0, ok: true},
		"the object is there, the size is another":   {object: 1234, size: 99, ok: false},
		"the object is not there":                    {object: -1, size: 1234, ok: false},
	} {
		b, a := newBucket(t)
		b.completeNoSuchUpload = true
		b.objectSize = tc.object
		err := a.Finish(context.Background(), s3Server, s3Backup, "UP-1", parts, tc.size)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v", name, err)
		}
		if err != nil && !s3.IsCode(err, "NoSuchUpload") {
			t.Errorf("%s: the refusal is not the bucket's own: %v", name, err)
		}
	}
}

func TestS3BackupsAbortDeleteAndDownload(t *testing.T) {
	b, a := newBucket(t)
	if err := a.Abort(context.Background(), s3Server, s3Backup, "UP-1"); err != nil {
		t.Errorf("abort: %v", err)
	}
	// An upload that is not there is already as aborted as it can be.
	if err := a.Abort(context.Background(), s3Server, s3Backup, "gone"); err != nil {
		t.Errorf("abort of an upload that is gone: %v", err)
	}
	if err := a.Delete(context.Background(), s3Server, s3Backup); err != nil {
		t.Errorf("delete: %v", err)
	}
	var sawDelete bool
	for _, c := range b.calls() {
		if strings.HasPrefix(c, "DELETE "+s3Key+"?") && !strings.Contains(c, "uploadId") {
			sawDelete = true
		}
	}
	if !sawDelete {
		t.Errorf("the archive was not deleted: %v", b.calls())
	}
	b.denyDelete = true
	if err := a.Delete(context.Background(), s3Server, s3Backup); !s3.IsCode(err, "AccessDenied") {
		t.Errorf("delete denied: %v", err)
	}

	raw, err := a.DownloadURL(s3Server, s3Backup, 5*time.Minute)
	u, perr := url.Parse(raw)
	if err != nil || perr != nil || u.Path != s3Key || u.Query().Get("X-Amz-Expires") != "300" {
		t.Errorf("download address = %s, %v", raw, err)
	}
	if _, err := a.DownloadURL(s3Server, s3Backup, 30*24*time.Hour); err == nil {
		t.Error("an address good for thirty days")
	}
}

// The key of an archive is the reference's, so that what an old panel put in the bucket is found.
func TestS3BackupsKeyIsTheReferences(t *testing.T) {
	var a s3Backups
	if got := a.key("srv-uuid", "bkp-uuid"); got != "srv-uuid/bkp-uuid.tar.gz" {
		t.Errorf("key = %q", got)
	}
}

// completedParts reads the parts out of the body that was sent to put an upload together: the quotes of a tag are escaped in the XML, which is the same
// text to whoever reads it.
func completedParts(t *testing.T, body string) []s3.Part {
	t.Helper()
	var x struct {
		Parts []struct {
			PartNumber int    `xml:"PartNumber"`
			ETag       string `xml:"ETag"`
		} `xml:"Part"`
	}
	if err := xml.Unmarshal([]byte(body), &x); err != nil {
		t.Fatalf("the body is not XML: %v\n%s", err, body)
	}
	var out []s3.Part
	for _, p := range x.Parts {
		out = append(out, s3.Part{Number: p.PartNumber, ETag: p.ETag})
	}
	return out
}
