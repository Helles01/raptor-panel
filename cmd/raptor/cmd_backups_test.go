package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// simpleBucket answers what the check asks, without looking at signatures (the package s3 tests those).
type simpleBucket struct {
	mu      sync.Mutex
	parts   map[string][]byte
	objects map[string][]byte
	denyAll bool
}

func (b *simpleBucket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.denyAll {
		w.WriteHeader(403)
		fmt.Fprint(w, `<Error><Code>InvalidAccessKeyId</Code><Message>The key id does not exist</Message></Error>`)
		return
	}
	q := r.URL.Query()
	switch {
	case r.Method == "POST" && q.Has("uploads"):
		fmt.Fprint(w, `<InitiateMultipartUploadResult><UploadId>U1</UploadId></InitiateMultipartUploadResult>`)
	case r.Method == "PUT" && q.Has("partNumber"):
		b.parts[r.URL.Path] = body
		w.Header().Set("ETag", `"e1"`)
	case r.Method == "POST" && q.Has("uploadId"):
		b.objects[r.URL.Path] = b.parts[r.URL.Path]
		fmt.Fprint(w, `<CompleteMultipartUploadResult/>`)
	case r.Method == "GET":
		w.Write(b.objects[r.URL.Path])
	case r.Method == "DELETE":
		delete(b.objects, r.URL.Path)
		w.WriteHeader(204)
	case r.Method == "HEAD":
		if _, ok := b.objects[r.URL.Path]; !ok {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Length", "1")
	}
}

func s3Vars(endpoint string) map[string]string {
	return map[string]string{
		"RAPTOR_BACKUPS_S3_ENDPOINT": endpoint, "RAPTOR_BACKUPS_S3_REGION": "us-east-1", "RAPTOR_BACKUPS_S3_BUCKET": "bkt", "RAPTOR_BACKUPS_S3_PATH_STYLE": "true",
		"RAPTOR_BACKUPS_S3_ACCESS_KEY": "AKIACHECKEXAMPLE", "RAPTOR_BACKUPS_S3_SECRET_KEY": "check-secret-key-value",
	}
}

func TestBackupsCheckTriesTheBucket(t *testing.T) {
	b := &simpleBucket{parts: map[string][]byte{}, objects: map[string][]byte{}}
	srv := httptest.NewServer(b)
	defer srv.Close()

	code, stdout, stderr := runCmd(t, "", s3Vars(srv.URL), "backups", "check")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	for _, want := range []string{"ok    start an upload", "ok    send a part through a signed address", "ok    put the parts together", "ok    read the object through a signed address", "ok    delete the object", "ok    see that the object is gone"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stderr, "Everything worked") || !strings.Contains(stderr, "the node") {
		t.Errorf("stderr = %q", stderr)
	}
	// With the driver set to s3 it says that new backups go there.
	vars := s3Vars(srv.URL)
	vars["RAPTOR_BACKUPS_DRIVER"] = "s3"
	if code, _, stderr := runCmd(t, "", vars, "backups", "check"); code != 0 || !strings.Contains(stderr, "kept on S3") {
		t.Errorf("driver s3: %d %q", code, stderr)
	}
	if len(b.objects) != 0 {
		t.Errorf("%d objects are left", len(b.objects))
	}
	// The keys are in no line of what it says.
	for _, out := range []string{stdout, stderr} {
		if strings.Contains(out, "check-secret-key-value") || strings.Contains(out, "AKIACHECKEXAMPLE") {
			t.Errorf("a key is printed: %s", out)
		}
	}
}

func TestBackupsCheckSaysWhatIsWrong(t *testing.T) {
	b := &simpleBucket{denyAll: true, parts: map[string][]byte{}, objects: map[string][]byte{}}
	srv := httptest.NewServer(b)
	defer srv.Close()
	code, stdout, stderr := runCmd(t, "", s3Vars(srv.URL), "backups", "check")
	if code != 1 || !strings.Contains(stdout, "FAIL  start an upload: s3: InvalidAccessKeyId") || !strings.Contains(stderr, `the check stopped at "start an upload"`) {
		t.Errorf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	if strings.Contains(stdout+stderr, "check-secret-key-value") {
		t.Error("the secret is printed")
	}

	for name, tc := range map[string]struct {
		vars map[string]string
		want string
	}{
		"no S3 settings":         {nil, "nothing to check"},
		"a bucket with capitals": {map[string]string{"RAPTOR_BACKUPS_S3_REGION": "us-east-1", "RAPTOR_BACKUPS_S3_BUCKET": "Bkt", "RAPTOR_BACKUPS_S3_ACCESS_KEY": "AK", "RAPTOR_BACKUPS_S3_SECRET_KEY": "sk"}, "invalid configuration"},
		"driver s3 with nothing": {map[string]string{"RAPTOR_BACKUPS_DRIVER": "s3"}, "no backups.s3 settings"},
	} {
		code, _, stderr := runCmd(t, "", tc.vars, "backups", "check")
		if code != 1 || !strings.Contains(stderr, tc.want) {
			t.Errorf("%s: exit %d %q", name, code, stderr)
		}
	}
	if code, _, _ := runCmd(t, "", nil, "backups", "check", "extra"); code != 2 {
		t.Errorf("an argument that is not a flag: exit %d", code)
	}
	if code, _, _ := runCmd(t, "", nil, "backups"); code != 2 {
		t.Errorf("no verb: exit %d", code)
	}
}

func TestBackupsIsARootCommand(t *testing.T) {
	if rootCommands()["backups"] == nil {
		t.Error("raptor backups is not a command")
	}
}
