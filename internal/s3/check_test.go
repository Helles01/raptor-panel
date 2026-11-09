package s3

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// memBucket is an S3 server that keeps what it is given, and checks every signature, of a header and of a signed address alike.
type memBucket struct {
	t   *testing.T
	c   *Client
	srv *httptest.Server

	mu      sync.Mutex
	uploads map[string]map[int][]byte
	etags   map[string]map[int]string
	objects map[string][]byte
	log     []string
	next    int
	// deny makes a request fail with AccessDenied; corrupt changes what a read gives.
	deny    func(r *http.Request) bool
	corrupt bool
	// badSignatures is every request whose signature did not verify.
	badSignatures []string
}

func newMemBucket(t *testing.T) *memBucket {
	t.Helper()
	b := &memBucket{t: t, uploads: map[string]map[int][]byte{}, etags: map[string]map[int]string{}, objects: map[string][]byte{}}
	b.srv = httptest.NewServer(b)
	t.Cleanup(b.srv.Close)
	c, err := New(Config{Endpoint: b.srv.URL, Region: "us-east-1", Bucket: "bkt", PathStyle: true, AccessKey: docAccessKey, SecretKey: docSecretKey},
		Options{Now: func() time.Time { return time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	b.c = c
	return b
}

func (b *memBucket) verified(r *http.Request, body []byte) bool {
	q := r.URL.Query()
	if sig := q.Get("X-Amz-Signature"); sig != "" {
		q.Del("X-Amz-Signature")
		_, want := b.c.signature(r.Method, r.URL.EscapedPath(), canonicalQuery(q), map[string]string{"host": r.Host}, unsignedPayload, q.Get("X-Amz-Date"))
		return sig == want
	}
	m := authPattern.FindStringSubmatch(r.Header.Get("Authorization"))
	if m == nil || m[1] != docAccessKey {
		return false
	}
	headers := map[string]string{}
	for _, name := range strings.Split(m[4], ";") {
		if name == "host" {
			headers[name] = r.Host
		} else {
			headers[name] = r.Header.Get(name)
		}
	}
	_, want := b.c.signature(r.Method, r.URL.EscapedPath(), canonicalQuery(r.URL.Query()), headers, r.Header.Get("x-amz-content-sha256"), r.Header.Get("x-amz-date"))
	return want == m[5] && r.Header.Get("x-amz-content-sha256") == hexSHA256(body)
}

func (b *memBucket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.log = append(b.log, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
	if !b.verified(r, body) {
		b.badSignatures = append(b.badSignatures, r.Method+" "+r.URL.RequestURI())
		writeError(w, 403, "SignatureDoesNotMatch", "the signature does not match")
		return
	}
	if b.deny != nil && b.deny(r) {
		writeError(w, 403, "AccessDenied", "Access Denied")
		return
	}
	q := r.URL.Query()
	key := strings.TrimPrefix(r.URL.Path, "/bkt/")
	switch {
	case r.Method == "POST" && q.Has("uploads"):
		b.next++
		id := fmt.Sprintf("UPLOAD.%d", b.next)
		b.uploads[id], b.etags[id] = map[int][]byte{}, map[int]string{}
		fmt.Fprintf(w, `<InitiateMultipartUploadResult><UploadId>%s</UploadId></InitiateMultipartUploadResult>`, id)
	case r.Method == "PUT" && q.Has("uploadId") && q.Has("partNumber"):
		id := q.Get("uploadId")
		if _, ok := b.uploads[id]; !ok {
			writeError(w, 404, "NoSuchUpload", "no such upload")
			return
		}
		var n int
		fmt.Sscanf(q.Get("partNumber"), "%d", &n)
		b.uploads[id][n] = body
		b.etags[id][n] = fmt.Sprintf(`"etag-%d-%d"`, n, len(body))
		w.Header().Set("ETag", b.etags[id][n])
	case r.Method == "POST" && q.Has("uploadId"):
		id := q.Get("uploadId")
		parts, ok := b.uploads[id]
		if !ok {
			writeError(w, 404, "NoSuchUpload", "no such upload")
			return
		}
		var req completeXML
		if err := xml.Unmarshal(body, &req); err != nil || len(req.Parts) == 0 {
			writeError(w, 400, "MalformedXML", "bad")
			return
		}
		var object []byte
		for _, p := range req.Parts {
			if b.etags[id][p.PartNumber] != p.ETag {
				writeError(w, 400, "InvalidPart", "a part is not the one that was sent")
				return
			}
			object = append(object, parts[p.PartNumber]...)
		}
		b.objects[key] = object
		delete(b.uploads, id)
		fmt.Fprint(w, `<CompleteMultipartUploadResult/>`)
	case r.Method == "DELETE" && q.Has("uploadId"):
		delete(b.uploads, q.Get("uploadId"))
		w.WriteHeader(204)
	case r.Method == "GET":
		object, ok := b.objects[key]
		if !ok {
			writeError(w, 404, "NoSuchKey", "no such key")
			return
		}
		if b.corrupt {
			object = []byte("something else")
		}
		w.Write(object)
	case r.Method == "DELETE":
		delete(b.objects, key)
		w.WriteHeader(204)
	case r.Method == "HEAD":
		object, ok := b.objects[key]
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(object)))
	default:
		writeError(w, 501, "NotImplemented", r.Method)
	}
}

func names(steps []Step) []string {
	var out []string
	for _, s := range steps {
		out = append(out, s.Name)
	}
	return out
}

func TestCheckTriesEveryWayTheBucketIsUsedAndLeavesNothing(t *testing.T) {
	b := newMemBucket(t)
	steps := b.c.Check(context.Background())
	want := []string{
		"start an upload", "send a part through a signed address", "put the parts together", "read the object through a signed address",
		"delete the object", "see that the object is gone",
	}
	if fmt.Sprint(names(steps)) != fmt.Sprint(want) {
		t.Fatalf("steps = %v", names(steps))
	}
	for _, s := range steps {
		if s.Err != nil {
			t.Errorf("%s: %v", s.Name, s.Err)
		}
	}
	if len(b.objects) != 0 || len(b.uploads) != 0 {
		t.Errorf("left in the bucket: %d objects, %d uploads", len(b.objects), len(b.uploads))
	}
	if len(b.badSignatures) != 0 {
		t.Errorf("requests whose signature did not verify: %v", b.badSignatures)
	}
	// The object is under a prefix of its own, with a name that is different each time.
	again := newMemBucket(t)
	again.c.Check(context.Background())
	if !strings.Contains(b.log[0], "/bkt/raptor-check/") || b.log[0] == again.log[0] {
		t.Errorf("the keys: %q and %q", b.log[0], again.log[0])
	}
}

func TestCheckSaysWhereItStoppedAndCleansUp(t *testing.T) {
	for name, tc := range map[string]struct {
		deny    func(r *http.Request) bool
		corrupt bool
		stop    string
		code    string
	}{
		"no right to start an upload": {deny: func(r *http.Request) bool { return r.Method == "POST" && r.URL.Query().Has("uploads") }, stop: "start an upload", code: "AccessDenied"},
		"no right to send a part":     {deny: func(r *http.Request) bool { return r.Method == "PUT" }, stop: "send a part through a signed address", code: "AccessDenied"},
		"no right to put them together": {deny: func(r *http.Request) bool { return r.Method == "POST" && r.URL.Query().Has("uploadId") },
			stop: "put the parts together", code: "AccessDenied"},
		"no right to read":                 {deny: func(r *http.Request) bool { return r.Method == "GET" }, stop: "read the object through a signed address", code: "AccessDenied"},
		"a read that gives another object": {corrupt: true, stop: "read the object through a signed address"},
	} {
		t.Run(name, func(t *testing.T) {
			b := newMemBucket(t)
			b.deny, b.corrupt = tc.deny, tc.corrupt
			steps := b.c.Check(context.Background())
			last := steps[len(steps)-1]
			if last.Name != tc.stop || last.Err == nil {
				t.Fatalf("the last step is %q (%v), want a failure at %q", last.Name, last.Err, tc.stop)
			}
			if tc.code != "" && !IsCode(last.Err, tc.code) {
				t.Errorf("the failure is %v", last.Err)
			}
			for _, s := range steps[:len(steps)-1] {
				if s.Err != nil {
					t.Errorf("an earlier step failed: %s: %v", s.Name, s.Err)
				}
			}
			// What the check made is thrown away: no object and no upload is left, except where the bucket refused to let it be thrown away.
			if len(b.objects) != 0 || len(b.uploads) != 0 {
				t.Errorf("left in the bucket: %d objects, %d uploads", len(b.objects), len(b.uploads))
			}
		})
	}
}

// A bucket that will not delete what the check made says so, and what is left is named by the failure: the administrator has one object to remove.
func TestCheckWhenTheObjectCannotBeDeleted(t *testing.T) {
	b := newMemBucket(t)
	b.deny = func(r *http.Request) bool { return r.Method == "DELETE" }
	steps := b.c.Check(context.Background())
	last := steps[len(steps)-1]
	if last.Name != "delete the object" || !IsCode(last.Err, "AccessDenied") {
		t.Errorf("the last step: %+v", last)
	}
	if len(b.objects) != 1 {
		t.Errorf("%d objects are left", len(b.objects))
	}
}

func TestCheckWithWrongKeys(t *testing.T) {
	b := newMemBucket(t)
	wrong, err := New(Config{Endpoint: b.srv.URL, Region: "us-east-1", Bucket: "bkt", PathStyle: true, AccessKey: docAccessKey, SecretKey: "not-the-secret-key"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	steps := wrong.Check(context.Background())
	if len(steps) != 1 || steps[0].Name != "start an upload" || !IsCode(steps[0].Err, "SignatureDoesNotMatch") {
		t.Fatalf("steps = %+v", steps)
	}
	if strings.Contains(steps[0].Err.Error(), "not-the-secret-key") {
		t.Errorf("the secret is in the error")
	}
}

func TestCheckWhenTheServerIsNotThere(t *testing.T) {
	b := newMemBucket(t)
	b.srv.Close()
	steps := b.c.Check(context.Background())
	if len(steps) != 1 || steps[0].Err == nil || strings.Contains(steps[0].Err.Error(), "X-Amz") {
		t.Errorf("steps = %+v", steps)
	}
}

// The address of a part carries a signature: an error that comes from using it does not repeat it.
func TestCheckErrorsDoNotCarryTheSignedAddress(t *testing.T) {
	b := newMemBucket(t)
	b.deny = nil
	// The server goes away between the start of the upload and the sending of the part.
	var once sync.Once
	b.deny = func(r *http.Request) bool {
		if r.Method == "PUT" {
			once.Do(func() { go b.srv.CloseClientConnections() })
		}
		return false
	}
	steps := b.c.Check(context.Background())
	for _, s := range steps {
		if s.Err != nil && (strings.Contains(s.Err.Error(), "X-Amz-Signature") || strings.Contains(s.Err.Error(), "Credential")) {
			t.Errorf("%s: the signed address is in the error: %v", s.Name, s.Err)
		}
	}
}
