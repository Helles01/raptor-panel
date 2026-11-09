package s3

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// The examples of the AWS documentation for Signature Version 4 with S3: the same key, bucket, date and request give the same signature.
const (
	docAccessKey = "AKIAIOSFODNN7EXAMPLE"
	docSecretKey = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	docDate      = "20130524T000000Z"
)

func docClient(t *testing.T) *Client {
	t.Helper()
	c, err := New(Config{Endpoint: "https://s3.amazonaws.com", Region: "us-east-1", Bucket: "examplebucket", AccessKey: docAccessKey, SecretKey: docSecretKey}, Options{
		Now: func() time.Time { return time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSignatureMatchesTheDocumentation(t *testing.T) {
	c := docClient(t)
	const host = "examplebucket.s3.amazonaws.com"

	t.Run("a signed address for GET", func(t *testing.T) {
		got, err := c.PresignGet("test.txt", 86400*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		want := "https://examplebucket.s3.amazonaws.com/test.txt?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request" +
			"&X-Amz-Date=20130524T000000Z&X-Amz-Expires=86400&X-Amz-SignedHeaders=host&X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"
		if got != want {
			t.Errorf("address = %s\nwant      %s", got, want)
		}
	})

	for name, tc := range map[string]struct {
		method, path, query string
		headers             map[string]string
		payload             string
		signed, signature   string
	}{
		"GET Object with a range": {
			"GET", "/test.txt", "", map[string]string{"host": host, "range": "bytes=0-9", "x-amz-content-sha256": emptyHash, "x-amz-date": docDate}, emptyHash,
			"host;range;x-amz-content-sha256;x-amz-date", "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41",
		},
		"PUT Object with a body and a dollar in the key": {
			"PUT", "/test%24file.text", "", map[string]string{
				"date": "Fri, 24 May 2013 00:00:00 GMT", "host": host, "x-amz-content-sha256": "44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072",
				"x-amz-date": docDate, "x-amz-storage-class": "REDUCED_REDUNDANCY",
			}, "44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072",
			"date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class", "98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd",
		},
		"GET Bucket with a query": {
			"GET", "/", "max-keys=2&prefix=J", map[string]string{"host": host, "x-amz-content-sha256": emptyHash, "x-amz-date": docDate}, emptyHash,
			"host;x-amz-content-sha256;x-amz-date", "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7",
		},
	} {
		t.Run(name, func(t *testing.T) {
			signed, sig := c.signature(tc.method, tc.path, tc.query, tc.headers, tc.payload, docDate)
			if signed != tc.signed || sig != tc.signature {
				t.Errorf("signed %q, signature %s\nwant   %q, %s", signed, sig, tc.signed, tc.signature)
			}
		})
	}
}

func TestEscape(t *testing.T) {
	for _, tc := range []struct {
		in        string
		keepSlash bool
		want      string
	}{
		{"a-b_c.d~e", false, "a-b_c.d~e"},
		{"a b/c", false, "a%20b%2Fc"},
		{"a b/c", true, "a%20b/c"},
		{"test$file.text", true, "test%24file.text"},
		{"é", true, "%C3%A9"},
		{"a+b=c&d", false, "a%2Bb%3Dc%26d"},
		{"", false, ""},
	} {
		if got := escape(tc.in, tc.keepSlash); got != tc.want {
			t.Errorf("escape(%q, %v) = %q, want %q", tc.in, tc.keepSlash, got, tc.want)
		}
	}
}

func TestConfigValidate(t *testing.T) {
	good := Config{Region: "eu-central-1", Bucket: "my-backups", AccessKey: "AKIA0123", SecretKey: "s3cr3t/key+value"}
	if err := good.Validate(); err != nil {
		t.Fatalf("a good configuration: %v", err)
	}
	withEndpoint := good
	withEndpoint.Endpoint = "http://127.0.0.1:9000"
	if err := withEndpoint.Validate(); err != nil {
		t.Errorf("an endpoint: %v", err)
	}

	for name, mutate := range map[string]func(*Config){
		"no bucket":                      func(c *Config) { c.Bucket = "" },
		"a bucket of capitals":           func(c *Config) { c.Bucket = "MyBackups" },
		"a bucket too short":             func(c *Config) { c.Bucket = "ab" },
		"a bucket with a slash":          func(c *Config) { c.Bucket = "a/b/c" },
		"no region":                      func(c *Config) { c.Region = "" },
		"a region with a space":          func(c *Config) { c.Region = "eu central" },
		"no access key":                  func(c *Config) { c.AccessKey = "" },
		"an access key with space":       func(c *Config) { c.AccessKey = "AK IA" },
		"no secret key":                  func(c *Config) { c.SecretKey = "" },
		"a secret with a space":          func(c *Config) { c.SecretKey = "has space" },
		"a secret with a newline":        func(c *Config) { c.SecretKey = "a\nb" },
		"a storage class in small":       func(c *Config) { c.StorageClass = "standard_ia" },
		"an endpoint that is no address": func(c *Config) { c.Endpoint = "minio" },
		"an endpoint with a path":        func(c *Config) { c.Endpoint = "https://h.example.com/s3" },
		"an endpoint with credentials":   func(c *Config) { c.Endpoint = "https://user:pw@h.example.com" },
		"an endpoint with a query":       func(c *Config) { c.Endpoint = "https://h.example.com?x=1" },
		"an endpoint with ftp":           func(c *Config) { c.Endpoint = "ftp://h.example.com" },
	} {
		c := good
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), "s3cr3t") {
			t.Errorf("%s: the error repeats the secret: %v", name, err)
		}
	}
}

func TestConfigNeverPrintsTheKeys(t *testing.T) {
	c := Config{Region: "eu-central-1", Bucket: "my-backups", AccessKey: "AKIA-visible-id-0", SecretKey: "very-secret-value"}
	for _, s := range []string{c.String(), c.GoString(), fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c), c.LogValue().String()} {
		if strings.Contains(s, "very-secret-value") || strings.Contains(s, "AKIA-visible-id-0") {
			t.Errorf("a key is printed: %s", s)
		}
	}
}

func TestAddresses(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg  Config
		want string
	}{
		"amazon, virtual-hosted": {Config{Region: "eu-west-1", Bucket: "bkt"}, "https://bkt.s3.eu-west-1.amazonaws.com/a/b.tar.gz?"},
		"amazon, path-style":     {Config{Region: "eu-west-1", Bucket: "bkt", PathStyle: true}, "https://s3.eu-west-1.amazonaws.com/bkt/a/b.tar.gz?"},
		"endpoint, path-style":   {Config{Endpoint: "http://127.0.0.1:9000", Region: "us-east-1", Bucket: "bkt", PathStyle: true}, "http://127.0.0.1:9000/bkt/a/b.tar.gz?"},
		"endpoint, virtual":      {Config{Endpoint: "https://store.example.com", Region: "us-east-1", Bucket: "bkt"}, "https://bkt.store.example.com/a/b.tar.gz?"},
	} {
		t.Run(name, func(t *testing.T) {
			tc.cfg.AccessKey, tc.cfg.SecretKey = docAccessKey, docSecretKey
			c, err := New(tc.cfg, Options{})
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.PresignGet("a/b.tar.gz", time.Minute)
			if err != nil || !strings.HasPrefix(got, tc.want) {
				t.Errorf("address = %s, %v\nwant prefix %s", got, err, tc.want)
			}
		})
	}
}

// verifyPresigned checks a signed address the way a server would: it takes the signature off, signs the rest again and compares.
func verifyPresigned(t *testing.T, c *Client, raw string) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	given := q.Get("X-Amz-Signature")
	q.Del("X-Amz-Signature")
	_, sig := c.signature(http.MethodPut, u.EscapedPath(), canonicalQuery(q), map[string]string{"host": u.Host}, unsignedPayload, q.Get("X-Amz-Date"))
	if given == "" || given != sig {
		// A GET was signed for the same path: try that before saying it is wrong.
		_, sigGet := c.signature(http.MethodGet, u.EscapedPath(), canonicalQuery(q), map[string]string{"host": u.Host}, unsignedPayload, q.Get("X-Amz-Date"))
		if given != sigGet {
			t.Fatalf("the signature of %s does not verify", raw)
		}
	}
	return q
}

func TestPresignUploadPart(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	c, err := New(Config{Region: "eu-west-1", Bucket: "bkt", AccessKey: docAccessKey, SecretKey: docSecretKey}, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.PresignUploadPart("srv/backup.tar.gz", "up.load_ID-1", 3, 60*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	q := verifyPresigned(t, c, raw)
	if q.Get("partNumber") != "3" || q.Get("uploadId") != "up.load_ID-1" || q.Get("X-Amz-Expires") != "3600" || q.Get("X-Amz-SignedHeaders") != "host" ||
		q.Get("X-Amz-Date") != "20261108T120000Z" || q.Get("X-Amz-Credential") != docAccessKey+"/20261108/eu-west-1/s3/aws4_request" {
		t.Errorf("query = %v", q)
	}
	if strings.Contains(raw, docSecretKey) || strings.Contains(raw, "wJalrXUtnFEMI") {
		t.Errorf("the secret is in the address: %s", raw)
	}

	for name, f := range map[string]func() (string, error){
		"part 0":                  func() (string, error) { return c.PresignUploadPart("k", "id", 0, time.Minute) },
		"part 10001":              func() (string, error) { return c.PresignUploadPart("k", "id", MaxParts+1, time.Minute) },
		"an upload id with space": func() (string, error) { return c.PresignUploadPart("k", "a b", 1, time.Minute) },
		"an empty upload id":      func() (string, error) { return c.PresignUploadPart("k", "", 1, time.Minute) },
		"no key":                  func() (string, error) { return c.PresignGet("", time.Minute) },
		"a key with a newline":    func() (string, error) { return c.PresignGet("a\nb", time.Minute) },
		"a key from the root":     func() (string, error) { return c.PresignGet("/a", time.Minute) },
		"no time":                 func() (string, error) { return c.PresignGet("k", 0) },
		"eight days":              func() (string, error) { return c.PresignGet("k", 8*24*time.Hour) },
	} {
		if got, err := f(); err == nil || got != "" {
			t.Errorf("%s: got %q, %v", name, got, err)
		}
	}
}

// --- a server that speaks enough S3 to be asked

type fakeS3 struct {
	t   *testing.T
	c   *Client
	srv *httptest.Server

	mu       sync.Mutex
	requests []string
	// answers by "METHOD query-name" (for example "POST uploads"), "METHOD" alone for an object request without a query.
	answers map[string]func(w http.ResponseWriter, r *http.Request, body []byte)
	// signatureErrors collects requests whose signature the server could not verify.
	signatureErrors []string
}

var authPattern = regexp.MustCompile(`^AWS4-HMAC-SHA256 Credential=([^/]+)/(\d{8})/([^/]+)/s3/aws4_request, SignedHeaders=([a-z0-9;-]+), Signature=([0-9a-f]{64})$`)

func newFakeS3(t *testing.T, storageClass string) *fakeS3 {
	t.Helper()
	f := &fakeS3{t: t, answers: map[string]func(http.ResponseWriter, *http.Request, []byte){}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	c, err := New(Config{Endpoint: f.srv.URL, Region: "us-east-1", Bucket: "bkt", PathStyle: true, AccessKey: docAccessKey, SecretKey: docSecretKey, StorageClass: storageClass},
		Options{Now: func() time.Time { return time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	f.c = c
	return f
}

func (f *fakeS3) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	// Verify the signature as a server does, from what arrived.
	m := authPattern.FindStringSubmatch(r.Header.Get("Authorization"))
	ok := m != nil && m[1] == docAccessKey
	if ok {
		headers := map[string]string{}
		for _, name := range strings.Split(m[4], ";") {
			if name == "host" {
				headers[name] = r.Host
			} else {
				headers[name] = r.Header.Get(name)
			}
		}
		q := r.URL.Query()
		_, sig := f.c.signature(r.Method, r.URL.EscapedPath(), canonicalQuery(q), headers, r.Header.Get("x-amz-content-sha256"), r.Header.Get("x-amz-date"))
		ok = sig == m[5] && r.Header.Get("x-amz-content-sha256") == hexSHA256(body) && strings.Contains(m[4], "x-amz-date") && strings.Contains(m[4], "host")
	}
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
	if !ok {
		f.signatureErrors = append(f.signatureErrors, r.Method+" "+r.URL.RequestURI())
	}
	f.mu.Unlock()
	if !ok {
		writeError(w, 403, "SignatureDoesNotMatch", "The request signature we calculated does not match the signature you provided.")
		return
	}

	key := r.Method
	for _, name := range []string{"uploads", "uploadId"} {
		if _, has := r.URL.Query()[name]; has {
			key += " " + name
		}
	}
	if h, found := f.answers[key]; found {
		h(w, r, body)
		return
	}
	writeError(w, 501, "NotImplemented", "no answer for "+key)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message><RequestId>REQ1</RequestId></Error>`, code, msg)
}

func (f *fakeS3) assertSigned() {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.signatureErrors) > 0 {
		f.t.Errorf("requests the server could not verify: %v", f.signatureErrors)
	}
}

func TestCreateMultipartUpload(t *testing.T) {
	f := newFakeS3(t, "STANDARD_IA")
	var sawClass, sawType, sawPath string
	f.answers["POST uploads"] = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		sawClass, sawType, sawPath = r.Header.Get("x-amz-storage-class"), r.Header.Get("Content-Type"), r.URL.Path
		fmt.Fprint(w, `<?xml version="1.0"?><InitiateMultipartUploadResult><Bucket>bkt</Bucket><Key>a/b.tar.gz</Key><UploadId>VXBsb2FkIElE.1_2-3~</UploadId></InitiateMultipartUploadResult>`)
	}
	id, err := f.c.CreateMultipartUpload(context.Background(), "a/b.tar.gz")
	if err != nil || id != "VXBsb2FkIElE.1_2-3~" {
		t.Fatalf("id = %q, %v", id, err)
	}
	if sawClass != "STANDARD_IA" || sawType != ContentType || sawPath != "/bkt/a/b.tar.gz" {
		t.Errorf("class %q, type %q, path %q", sawClass, sawType, sawPath)
	}
	f.assertSigned()

	// An answer without a usable id is not taken for one.
	for name, body := range map[string]string{"none": `<InitiateMultipartUploadResult/>`, "garbage": `not xml`, "a space in it": `<InitiateMultipartUploadResult><UploadId>a b</UploadId></InitiateMultipartUploadResult>`} {
		f.answers["POST uploads"] = func(w http.ResponseWriter, _ *http.Request, _ []byte) { fmt.Fprint(w, body) }
		if id, err := f.c.CreateMultipartUpload(context.Background(), "k"); err == nil || id != "" {
			t.Errorf("%s: %q, %v", name, id, err)
		}
	}
}

func TestCompleteMultipartUpload(t *testing.T) {
	f := newFakeS3(t, "")
	var got completeXML
	f.answers["POST uploadId"] = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.URL.Query().Get("uploadId") != "up1" {
			writeError(w, 404, "NoSuchUpload", "no such upload")
			return
		}
		if err := xml.Unmarshal(body, &got); err != nil {
			writeError(w, 400, "MalformedXML", "bad")
			return
		}
		fmt.Fprint(w, `<CompleteMultipartUploadResult><Location>x</Location></CompleteMultipartUploadResult>`)
	}
	parts := []Part{{Number: 2, ETag: `"bbb"`}, {Number: 1, ETag: `"aaa"`}, {Number: 3, ETag: `"c&c"`}}
	if err := f.c.CompleteMultipartUpload(context.Background(), "k", "up1", parts); err != nil {
		t.Fatal(err)
	}
	// In ascending order, whatever order they came in, and the tags as they were.
	if len(got.Parts) != 3 || got.Parts[0] != (partXML{1, `"aaa"`}) || got.Parts[1] != (partXML{2, `"bbb"`}) || got.Parts[2] != (partXML{3, `"c&c"`}) {
		t.Errorf("parts sent = %+v", got.Parts)
	}
	if parts[0].Number != 2 {
		t.Error("the caller's list was reordered")
	}
	f.assertSigned()

	// The server may say 200 and put the refusal in the body.
	f.answers["POST uploadId"] = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		fmt.Fprint(w, `<Error><Code>InternalError</Code><Message>We encountered an internal error. Please try again.</Message></Error>`)
	}
	err := f.c.CompleteMultipartUpload(context.Background(), "k", "up1", parts)
	if !IsCode(err, "InternalError") {
		t.Errorf("a refusal in a 200: %v", err)
	}
	f.answers["POST uploadId"] = nil
	delete(f.answers, "POST uploadId")
	if err := f.c.CompleteMultipartUpload(context.Background(), "k", "up2", parts); err == nil {
		t.Error("an upload nobody has was completed")
	}
	f.answers["POST uploadId"] = func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeError(w, 404, "NoSuchUpload", "gone") }
	if err := f.c.CompleteMultipartUpload(context.Background(), "k", "up3", parts); !IsCode(err, "NoSuchUpload") {
		t.Errorf("no such upload: %v", err)
	}

	for name, ps := range map[string][]Part{
		"no parts": nil, "a part 0": {{0, `"a"`}}, "a part 10001": {{MaxParts + 1, `"a"`}}, "an empty tag": {{1, ""}}, "a tag with a newline": {{1, "a\nb"}},
		"a long tag": {{1, strings.Repeat("a", 257)}}, "two parts the same": {{1, `"a"`}, {1, `"b"`}},
	} {
		if err := f.c.CompleteMultipartUpload(context.Background(), "k", "up1", ps); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := f.c.CompleteMultipartUpload(context.Background(), "k", "bad id", parts); err == nil {
		t.Error("an upload id with a space")
	}
	many := make([]Part, MaxParts+1)
	for i := range many {
		many[i] = Part{Number: i + 1, ETag: `"a"`}
	}
	if err := f.c.CompleteMultipartUpload(context.Background(), "k", "up1", many); err == nil {
		t.Error("more parts than the protocol has")
	}
}

func TestListPartsFollowsThePages(t *testing.T) {
	f := newFakeS3(t, "")
	var markers []string
	f.answers["GET uploadId"] = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		markers = append(markers, r.URL.Query().Get("part-number-marker"))
		if r.URL.Query().Get("max-parts") != "1000" {
			t.Errorf("max-parts = %q", r.URL.Query().Get("max-parts"))
		}
		switch r.URL.Query().Get("part-number-marker") {
		case "":
			fmt.Fprint(w, `<ListPartsResult><IsTruncated>true</IsTruncated><NextPartNumberMarker>2</NextPartNumberMarker><Part><PartNumber>1</PartNumber><ETag>"a"</ETag></Part><Part><PartNumber>2</PartNumber><ETag>"b"</ETag></Part></ListPartsResult>`)
		case "2":
			fmt.Fprint(w, `<ListPartsResult><IsTruncated>false</IsTruncated><Part><PartNumber>3</PartNumber><ETag>"c"</ETag></Part></ListPartsResult>`)
		default:
			t.Errorf("an unexpected marker %q", r.URL.Query().Get("part-number-marker"))
		}
	}
	got, err := f.c.ListParts(context.Background(), "k", "up1")
	if err != nil || len(got) != 3 || got[2] != (Part{3, `"c"`}) || len(markers) != 2 {
		t.Fatalf("parts = %+v, markers %v, %v", got, markers, err)
	}
	f.assertSigned()

	// A server that keeps saying there is more without moving on does not make a loop.
	f.answers["GET uploadId"] = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		fmt.Fprint(w, `<ListPartsResult><IsTruncated>true</IsTruncated><NextPartNumberMarker>0</NextPartNumberMarker><Part><PartNumber>1</PartNumber><ETag>"a"</ETag></Part></ListPartsResult>`)
	}
	if got, err := f.c.ListParts(context.Background(), "k", "up1"); err != nil || len(got) != 1 {
		t.Errorf("a marker that does not move: %v, %v", got, err)
	}
	f.answers["GET uploadId"] = func(w http.ResponseWriter, _ *http.Request, _ []byte) { fmt.Fprint(w, `garbage`) }
	if _, err := f.c.ListParts(context.Background(), "k", "up1"); err == nil {
		t.Error("an answer that is not understood")
	}
}

func TestAbortDeleteAndHead(t *testing.T) {
	f := newFakeS3(t, "")
	f.answers["DELETE uploadId"] = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.URL.Query().Get("uploadId") == "gone" {
			writeError(w, 404, "NoSuchUpload", "gone")
			return
		}
		w.WriteHeader(204)
	}
	f.answers["DELETE"] = func(w http.ResponseWriter, _ *http.Request, _ []byte) { w.WriteHeader(204) }
	f.answers["HEAD"] = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		switch r.URL.Path {
		case "/bkt/there.tar.gz":
			w.Header().Set("Content-Length", "12345")
			w.WriteHeader(200)
		case "/bkt/nolength.tar.gz":
			w.WriteHeader(200)
		default:
			w.WriteHeader(404)
		}
	}
	ctx := context.Background()
	if err := f.c.AbortMultipartUpload(ctx, "k", "up1"); err != nil {
		t.Errorf("abort: %v", err)
	}
	if err := f.c.AbortMultipartUpload(ctx, "k", "gone"); !IsCode(err, "NoSuchUpload") {
		t.Errorf("abort of an upload that is gone: %v", err)
	}
	if err := f.c.DeleteObject(ctx, "a/b.tar.gz"); err != nil {
		t.Errorf("delete: %v", err)
	}
	if n, found, err := f.c.Head(ctx, "there.tar.gz"); err != nil || !found || n != 12345 {
		t.Errorf("head: %d %v %v", n, found, err)
	}
	if n, found, err := f.c.Head(ctx, "missing.tar.gz"); err != nil || found || n != 0 {
		t.Errorf("head of a missing object: %d %v %v", n, found, err)
	}
	if _, _, err := f.c.Head(ctx, "nolength.tar.gz"); err == nil {
		t.Error("head without a length")
	}
	f.assertSigned()
	if err := f.c.AbortMultipartUpload(ctx, "k", "a b"); err == nil {
		t.Error("abort with a bad id")
	}
	if err := f.c.DeleteObject(ctx, ""); err == nil {
		t.Error("delete with no key")
	}
}

func TestErrorsAreWhatTheServerSaid(t *testing.T) {
	f := newFakeS3(t, "")
	f.answers["DELETE"] = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeError(w, 403, "AccessDenied", "Access Denied\nsecond\tline")
	}
	err := f.c.DeleteObject(context.Background(), "k")
	var e *Error
	if !errors.As(err, &e) || e.Status != 403 || e.Code != "AccessDenied" || e.RequestID != "REQ1" {
		t.Fatalf("err = %v", err)
	}
	if strings.ContainsAny(e.Message, "\n\t") {
		t.Errorf("a control character is in the message: %q", e.Message)
	}
	if strings.Contains(err.Error(), docSecretKey) {
		t.Error("the secret is in the error")
	}

	// An answer that is no XML still gives the status, and no code.
	f.answers["DELETE"] = func(w http.ResponseWriter, _ *http.Request, _ []byte) { w.WriteHeader(503) }
	err = f.c.DeleteObject(context.Background(), "k")
	if !errors.As(err, &e) || e.Status != 503 || e.Code != "" || IsCode(err, "SlowDown") {
		t.Errorf("err = %v", err)
	}

	// A server that cannot be reached gives an error that does not carry the address with its signature.
	f.srv.Close()
	err = f.c.DeleteObject(context.Background(), "k")
	if err == nil || strings.Contains(err.Error(), "X-Amz") || strings.Contains(err.Error(), "Signature") {
		t.Errorf("err = %v", err)
	}
}

func TestARedirectIsNotFollowed(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the request was sent on to %s", r.URL)
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/elsewhere", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	c, err := New(Config{Endpoint: srv.URL, Region: "us-east-1", Bucket: "bkt", PathStyle: true, AccessKey: docAccessKey, SecretKey: docSecretKey}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteObject(context.Background(), "k"); err == nil {
		t.Error("a redirect was taken for success")
	}
}

func TestPresignedAddressesDifferByPartAndKey(t *testing.T) {
	c := docClient(t)
	seen := map[string]bool{}
	for part := 1; part <= 3; part++ {
		for _, key := range []string{"a/b", "a/c"} {
			u, err := c.PresignUploadPart(key, "up", part, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			sig := u[strings.LastIndex(u, "=")+1:]
			if seen[sig] {
				t.Errorf("two addresses have the signature %s", sig)
			}
			seen[sig] = true
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) != 6 {
		t.Errorf("%d signatures", len(keys))
	}
}
