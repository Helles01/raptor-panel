package fileapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apitest"
	"github.com/Helles01/raptor-panel/internal/api/controlapi"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/wings"
)

const (
	ada   = 1 // owner
	grace = 2 // administrator
	bob   = 3 // holds every file right
	cleo  = 4 // may only look at the list of files
	dave  = 5 // may only create
	eve   = 6 // a stranger
)

const serverUUID = "aaaa1111-2222-4333-8444-555555555555"

var node = wings.Node{Scheme: "https", FQDN: "node1.example.com", Port: 8080, Token: strings.Repeat("a", 64)}

var errBoom = errors.New("the node is down: password=hunter2")

var t0 = apitest.Now

func yes() *bool { b := true; return &b }
func no() *bool  { b := false; return &b }

// call is one call to the node.
type call struct {
	Kind string
	Args []any
}

type fakeDaemon struct {
	mu      sync.Mutex
	calls   []call
	err     error // every call fails with it
	list    []wings.FileObject
	content []byte
	archive wings.FileObject
	// readErr, when set, is what Read answers.
	readErr error
}

func (d *fakeDaemon) record(kind string, args ...any) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, call{kind, args})
	return d.err
}

func (d *fakeDaemon) List(_ context.Context, _ wings.Node, uuid, dir string) ([]wings.FileObject, error) {
	return d.list, d.record("list", uuid, dir)
}

func (d *fakeDaemon) Read(_ context.Context, _ wings.Node, uuid, path string, max int64) ([]byte, error) {
	if err := d.record("read", uuid, path, max); err != nil {
		return nil, err
	}
	return d.content, d.readErr
}

func (d *fakeDaemon) Write(_ context.Context, _ wings.Node, uuid, path string, content []byte) error {
	return d.record("write", uuid, path, string(content))
}

func (d *fakeDaemon) CreateDirectory(_ context.Context, _ wings.Node, uuid, name, path string) error {
	return d.record("create-directory", uuid, name, path)
}

func (d *fakeDaemon) Rename(_ context.Context, _ wings.Node, uuid, root string, items []wings.RenameItem) error {
	return d.record("rename", uuid, root, items)
}

func (d *fakeDaemon) Copy(_ context.Context, _ wings.Node, uuid, location string) error {
	return d.record("copy", uuid, location)
}

func (d *fakeDaemon) Delete(_ context.Context, _ wings.Node, uuid, root string, names []string) error {
	return d.record("delete", uuid, root, names)
}

func (d *fakeDaemon) Compress(_ context.Context, _ wings.Node, uuid, root string, names []string) (wings.FileObject, error) {
	return d.archive, d.record("compress", uuid, root, names)
}

func (d *fakeDaemon) Decompress(_ context.Context, _ wings.Node, uuid, root, file string) error {
	return d.record("decompress", uuid, root, file)
}

func (d *fakeDaemon) Chmod(_ context.Context, _ wings.Node, uuid, root string, items []wings.ChmodItem) error {
	return d.record("chmod", uuid, root, items)
}

func (d *fakeDaemon) Pull(_ context.Context, _ wings.Node, uuid string, o wings.PullOptions) error {
	return d.record("pull", uuid, o)
}

func (d *fakeDaemon) last() call {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.calls) == 0 {
		return call{}
	}
	return d.calls[len(d.calls)-1]
}

func (d *fakeDaemon) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.calls)
}

type targets struct{ err error }

func (t targets) For(_ context.Context, _ uint64) (controlapi.Target, error) {
	if t.err != nil {
		return controlapi.Target{}, t.err
	}
	return controlapi.Target{UUID: serverUUID, Node: node}, nil
}

type activityLog struct {
	mu      sync.Mutex
	entries []Entry
	err     error
}

func (a *activityLog) Log(_ context.Context, e Entry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	a.entries = append(a.entries, e)
	return nil
}

type rig struct {
	t        *testing.T
	client   *apitest.Client
	daemon   *fakeDaemon
	targets  *targets
	activity *activityLog
	logs     *bytes.Buffer
	handlers *Handlers
}

type options struct {
	maxEdit  int64
	location *time.Location
	tokens   Tokens
}

// brokenTokens cannot make a token.
type brokenTokens struct{ err error }

func (b brokenTokens) FileDownload(wings.Node, wings.Subject, string, string) (string, error) {
	return "", b.err
}

func (b brokenTokens) FileUpload(wings.Node, wings.Subject, string) (string, error) { return "", b.err }

func newRig(t *testing.T, opts ...func(*options)) *rig {
	t.Helper()
	var o options
	for _, f := range opts {
		f(&o)
	}
	r := &rig{t: t, daemon: &fakeDaemon{}, targets: &targets{}, activity: &activityLog{}, logs: &bytes.Buffer{}}

	w := apitest.NewWorld()
	for id, admin := range map[uint64]bool{ada: false, grace: true, bob: false, cleo: false, dave: false, eve: false} {
		w.AddUser(id, admin)
	}
	w.AddServer(100, serverUUID, ada)
	w.AddServer(101, "bbbb2222-2222-4333-8444-555555555555", ada)
	w.Subusers[[2]uint64{100, bob}] = []string{"file.read", "file.read-content", "file.create", "file.update", "file.delete", "file.archive", "websocket.connect"}
	w.Subusers[[2]uint64{101, bob}] = []string{"file.create", "websocket.connect"}
	w.Subusers[[2]uint64{100, cleo}] = []string{"file.read", "websocket.connect"}
	w.Subusers[[2]uint64{100, dave}] = []string{"file.create", "websocket.connect"}

	issuer, err := wings.NewIssuer("https://panel.example.com")
	if err != nil {
		t.Fatal(err)
	}
	limiter, err := middleware.NewLimiter(PullLimit, PullWindow)
	if err != nil {
		t.Fatal(err)
	}
	var tokens Tokens = issuer
	if o.tokens != nil {
		tokens = o.tokens
	}
	h, err := New(Deps{
		Targets: r.targets, Daemon: r.daemon, Tokens: tokens, Activity: r.activity, PullLimiter: limiter,
		MaxEditSize: o.maxEdit, Location: o.location, Logger: slog.New(slog.NewTextHandler(r.logs, nil)),
		Now: func() time.Time { return t0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	r.handlers = h
	r.client = w.Serve(t, func(g api.Group) { h.Register(g) })
	return r
}

const base = "/api/client/servers/aaaa1111/files"

func (r *rig) do(user uint64, method, path, body string) apitest.Reply {
	r.t.Helper()
	return r.client.As(user, method, base+path, body)
}

func decodeToken(t *testing.T, token string) (map[string]any, bool) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(node.Token))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	return claims, base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) == parts[2]
}

// routes is every route with a request that is valid, and the users other than the owner and the administrator who may make it.
var routes = []struct {
	name, method, path, body string
	allowed                  []uint64
}{
	{"list", "GET", "/list", "", []uint64{bob, cleo}},
	{"contents", "GET", "/contents?file=%2Fa.txt", "", []uint64{bob}},
	{"download", "GET", "/download?file=%2Fa.txt", "", []uint64{bob}},
	{"rename", "PUT", "/rename", `{"root":"/","files":[{"from":"a","to":"b"}]}`, []uint64{bob}},
	{"copy", "POST", "/copy", `{"location":"/a.txt"}`, []uint64{bob, dave}},
	{"write", "POST", "/write?file=%2Fa.txt", "hello", []uint64{bob, dave}},
	{"compress", "POST", "/compress", `{"root":"/","files":["a"]}`, []uint64{bob}},
	{"decompress", "POST", "/decompress", `{"root":"/","file":"a.zip"}`, []uint64{bob, dave}},
	{"delete", "POST", "/delete", `{"root":"/","files":["a"]}`, []uint64{bob}},
	{"create-folder", "POST", "/create-folder", `{"root":"/","name":"x"}`, []uint64{bob, dave}},
	{"chmod", "POST", "/chmod", `{"root":"/","files":[{"file":"a","mode":"644"}]}`, []uint64{bob}},
	{"pull", "POST", "/pull", `{"url":"https://example.com/a.zip"}`, []uint64{bob, dave}},
	{"upload", "GET", "/upload", "", []uint64{bob, dave}},
}

func TestEveryRouteHasItsPermission(t *testing.T) {
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			for _, user := range []uint64{ada, grace, bob, cleo, dave} {
				r := newRig(t)
				rp := r.do(user, rt.method, rt.path, rt.body)
				open := user == ada || user == grace
				for _, a := range rt.allowed {
					open = open || a == user
				}
				if open && (rp.Status < 200 || rp.Status > 299) || !open && rp.Status != 403 {
					t.Errorf("user %d: status = %d: %s", user, rp.Status, rp.Raw)
				}
				if !open && r.daemon.count() != 0 {
					t.Errorf("user %d: the node was called", user)
				}
			}
			r := newRig(t)
			if rp := r.do(eve, rt.method, rt.path, rt.body); rp.Status != 404 {
				t.Errorf("a stranger: status = %d", rp.Status)
			}
			if rp := r.do(0, rt.method, rt.path, rt.body); rp.Status != 401 {
				t.Errorf("nobody: status = %d", rp.Status)
			}
		})
	}
}

func TestList(t *testing.T) {
	r := newRig(t)
	r.daemon.list = []wings.FileObject{
		{Name: "server.jar", Created: "2026-10-01T10:00:00Z", Modified: "2026-10-02T10:30:00Z", Mode: "-rw-r--r--", ModeBits: "644", Size: 1234, File: yes(), Symlink: no(), Mime: "application/java-archive"},
		{Name: "logs", Created: "2026-10-01T10:00:00Z", Modified: "2026-10-02T10:30:00Z", Mode: "drwxr-xr-x", ModeBits: "755", Size: 4096, File: no(), Symlink: yes(), Mime: "inode/directory"},
		// A node that says nothing about a file: it is a plain file, and a time it cannot read is the time of the request.
		{Name: "bare", Created: "not a time"},
	}
	rp := r.do(ada, "GET", "/list?directory=%2Fmy%20dir", "")
	if rp.Status != 200 || rp.At("object") != "list" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	want := map[string]any{
		"name": "server.jar", "mode": "-rw-r--r--", "mode_bits": "644", "size": 1234.0, "is_file": true, "is_symlink": false,
		"mimetype": "application/java-archive", "created_at": "2026-10-01T10:00:00+00:00", "modified_at": "2026-10-02T10:30:00+00:00",
	}
	if rp.At("data", 0, "object") != "file_object" || !reflect.DeepEqual(rp.At("data", 0, "attributes"), want) {
		t.Errorf("first = %v", rp.At("data", 0))
	}
	if rp.At("data", 1, "attributes", "is_file") != false || rp.At("data", 1, "attributes", "is_symlink") != true {
		t.Errorf("second = %v", rp.At("data", 1, "attributes"))
	}
	bare := rp.At("data", 2, "attributes").(map[string]any)
	if bare["is_file"] != true || bare["is_symlink"] != false || bare["mimetype"] != "application/octet-stream" ||
		bare["created_at"] != "2026-10-23T12:00:00+00:00" || bare["modified_at"] != "2026-10-23T12:00:00+00:00" {
		t.Errorf("bare = %v", bare)
	}
	if got := r.daemon.last(); got.Kind != "list" || !reflect.DeepEqual(got.Args, []any{serverUUID, "/my dir"}) {
		t.Errorf("call = %+v", got)
	}

	// No directory means the root of the server, and so does an empty one.
	for _, q := range []string{"/list", "/list?directory="} {
		r.do(ada, "GET", q, "")
		if got := r.daemon.last(); !reflect.DeepEqual(got.Args, []any{serverUUID, "/"}) {
			t.Errorf("%s: call = %+v", q, got)
		}
	}
}

func TestTimesFollowThePanelsTimeZone(t *testing.T) {
	r := newRig(t, func(o *options) { o.location = time.FixedZone("CEST", 2*3600) })
	r.daemon.list = []wings.FileObject{{Name: "a", Created: "2026-10-01T10:00:00Z", Modified: "2026-10-02T10:30:00.5Z"}}
	rp := r.do(ada, "GET", "/list", "")
	if rp.At("data", 0, "attributes", "created_at") != "2026-10-01T12:00:00+02:00" || rp.At("data", 0, "attributes", "modified_at") != "2026-10-02T12:30:00+02:00" {
		t.Errorf("attributes = %v", rp.At("data", 0, "attributes"))
	}
}

func TestPathsAreChecked(t *testing.T) {
	tests := []struct {
		name, method, path, body, field, detail string
	}{
		{"list, control character", "GET", "/list?directory=a%00b", "", "directory", "A path must be valid text without control characters."},
		{"list, too long", "GET", "/list?directory=" + strings.Repeat("a", 4097), "", "directory", "A path must be at most 4096 bytes."},
		{"contents, no file", "GET", "/contents", "", "file", "The file field is required."},
		{"contents, control character", "GET", "/contents?file=a%0Ab", "", "file", "A path must be valid text without control characters."},
		{"download, no file", "GET", "/download", "", "file", "The file field is required."},
		{"write, no file", "POST", "/write", "x", "file", "The file field is required."},
		{"folder, no name", "POST", "/create-folder", `{"root":"/"}`, "name", "The name field is required."},
		{"folder, a name with a slash", "POST", "/create-folder", `{"name":"a/b"}`, "name", "A name must be valid text without slashes or control characters."},
		{"folder, the name ..", "POST", "/create-folder", `{"name":".."}`, "name", `A name must not be empty, "." or "..".`},
		{"folder, a name that is a number", "POST", "/create-folder", `{"name":5}`, "name", "The name must be a string."},
		{"folder, a root that is a number", "POST", "/create-folder", `{"name":"a","root":5}`, "root", "The root must be a string."},
		{"folder, a bad root", "POST", "/create-folder", `{"name":"a","root":"a\u0000"}`, "root", "A path must be valid text without control characters."},
		{"rename, no files", "PUT", "/rename", `{"root":"/"}`, "files", "The files field is required."},
		{"rename, an empty list", "PUT", "/rename", `{"files":[]}`, "files", "The files field is required."},
		{"rename, null", "PUT", "/rename", `{"files":null}`, "files", "The files field is required."},
		{"rename, not a list", "PUT", "/rename", `{"files":"a"}`, "files", "The files must be an array."},
		{"rename, an item that is no object", "PUT", "/rename", `{"files":["a"]}`, "files", "The files must be an array."},
		{"rename, no from", "PUT", "/rename", `{"files":[{"to":"b"}]}`, "files", "The files.0.from field is required."},
		{"rename, an empty to", "PUT", "/rename", `{"files":[{"from":"a","to":""}]}`, "files", "The files.0.to field is required."},
		{"rename, a bad path", "PUT", "/rename", `{"files":[{"from":"a","to":"b\u0001"}]}`, "files", "A path must be valid text without control characters."},
		{"rename, too many", "PUT", "/rename", `{"files":[` + strings.TrimSuffix(strings.Repeat(`{"from":"a","to":"b"},`, 1001), ",") + `]}`, "files", "Send between 1 and 1000 files."},
		{"copy, no location", "POST", "/copy", `{}`, "location", "The location field is required."},
		{"copy, a location that is a list", "POST", "/copy", `{"location":["a"]}`, "location", "The location must be a string."},
		{"copy, a bad location", "POST", "/copy", `{"location":"a\u0000"}`, "location", "A path must be valid text without control characters."},
		{"delete, files as text", "POST", "/delete", `{"files":"a"}`, "files", "The files must be an array."},
		{"delete, files as an object", "POST", "/delete", `{"files":{"a":1}}`, "files", "The files must be an array."},
		{"compress, no files", "POST", "/compress", `{"root":"/"}`, "files", "The files field is required."},
		{"compress, a file that is a number", "POST", "/compress", `{"files":[1]}`, "files", "The files.0 must be a string."},
		{"delete, an empty list", "POST", "/delete", `{"files":[]}`, "files", "The files field is required."},
		{"delete, a bad root", "POST", "/delete", `{"root":"\u0007","files":["a"]}`, "files", "A path must be valid text without control characters."},
		{"delete, a bad file", "POST", "/delete", `{"files":["a","\u007f"]}`, "files", "A path must be valid text without control characters."},
		{"delete, too many", "POST", "/delete", `{"files":[` + strings.TrimSuffix(strings.Repeat(`"a",`, 1001), ",") + `]}`, "files", "Send between 1 and 1000 files."},
		{"decompress, no file", "POST", "/decompress", `{"root":"/"}`, "file", "The file field is required."},
		{"decompress, a bad file", "POST", "/decompress", `{"file":"a\u0000"}`, "file", "A path must be valid text without control characters."},
		{"decompress, a bad root", "POST", "/decompress", `{"root":"\u0000","file":"a"}`, "root", "A path must be valid text without control characters."},
		{"chmod, no files", "POST", "/chmod", `{}`, "files", "The files field is required."},
		{"chmod, not a list", "POST", "/chmod", `{"files":3}`, "files", "The files must be an array."},
		{"chmod, no file", "POST", "/chmod", `{"files":[{"mode":"644"}]}`, "files", "The files.0.file field is required."},
		{"chmod, an empty mode", "POST", "/chmod", `{"files":[{"file":"a","mode":""}]}`, "files", "The files.0.mode must be a number."},
		{"chmod, no mode", "POST", "/chmod", `{"files":[{"file":"a"}]}`, "files", "The files.0.mode must be a number."},
		{"chmod, a mode that is text", "POST", "/chmod", `{"files":[{"file":"a","mode":"rwx"}]}`, "files", "A mode must be three or four octal digits."},
		{"chmod, a mode that is not octal", "POST", "/chmod", `{"files":[{"file":"a","mode":"999"}]}`, "files", "A mode must be three or four octal digits."},
		{"chmod, a mode too short", "POST", "/chmod", `{"files":[{"file":"a","mode":75}]}`, "files", "A mode must be three or four octal digits."},
		{"chmod, a mode that is a fraction", "POST", "/chmod", `{"files":[{"file":"a","mode":6.4}]}`, "files", "The files.0.mode must be a number."},
		{"chmod, a mode in exponent form", "POST", "/chmod", `{"files":[{"file":"a","mode":1e3}]}`, "files", "The files.0.mode must be a number."},
		{"chmod, a negative mode", "POST", "/chmod", `{"files":[{"file":"a","mode":-644}]}`, "files", "The files.0.mode must be a number."},
		{"pull, no url", "POST", "/pull", `{}`, "url", "The url field is required."},
		{"pull, a url that is a number", "POST", "/pull", `{"url":5}`, "url", "The url must be a string."},
		{"pull, a scheme that is not http", "POST", "/pull", `{"url":"ftp://example.com/a"}`, "url", "The URL must start with http:// or https://."},
		{"pull, a url with a password", "POST", "/pull", `{"url":"https://u:p@example.com/a"}`, "url", "The URL must not contain a user name or password."},
		{"pull, a url with a space", "POST", "/pull", `{"url":"https://example.com/a b"}`, "url", "The URL must not contain spaces, backslashes or control characters."},
		{"pull, a bad directory", "POST", "/pull", `{"url":"https://example.com/a","directory":"a\u0000"}`, "directory", "A path must be valid text without control characters."},
		{"pull, a name with a slash", "POST", "/pull", `{"url":"https://example.com/a","filename":"a/b"}`, "filename", "A name must be valid text without slashes or control characters."},
		{"pull, use_header as text", "POST", "/pull", `{"url":"https://example.com/a","use_header":"yes"}`, "use_header", "The use header field must be true or false."},
		{"pull, foreground as a number", "POST", "/pull", `{"url":"https://example.com/a","foreground":2}`, "foreground", "The foreground field must be true or false."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(bob, tt.method, tt.path, tt.body)
			if rp.Status != 422 || rp.Detail() != tt.detail || rp.At("errors", 0, "meta", "source_field") != tt.field {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if r.daemon.count() != 0 || len(r.activity.entries) != 0 {
				t.Error("the node was called or an entry written")
			}
		})
	}
}

func TestBodiesThatAreNotJSON(t *testing.T) {
	for _, path := range []string{"/rename", "/copy", "/compress", "/decompress", "/delete", "/create-folder", "/chmod", "/pull"} {
		r := newRig(t)
		method := "POST"
		if path == "/rename" {
			method = "PUT"
		}
		if rp := r.do(bob, method, path, "root=/"); rp.Status != 400 || r.daemon.count() != 0 {
			t.Errorf("%s: status = %d", path, rp.Status)
		}
	}
}

func TestContents(t *testing.T) {
	r := newRig(t)
	r.daemon.content = []byte("motd=<b>hello</b>\n")
	rp := r.do(bob, "GET", "/contents?file=%2Fserver.properties", "")
	if rp.Status != 200 || string(rp.Raw) != "motd=<b>hello</b>\n" {
		t.Fatalf("answer = %d %q", rp.Status, rp.Raw)
	}
	// A file is text and only text, whatever is in it.
	if ct := rp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") || rp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("Content-Type = %q, nosniff = %q", ct, rp.Header.Get("X-Content-Type-Options"))
	}
	if got := r.daemon.last(); got.Kind != "read" || !reflect.DeepEqual(got.Args, []any{serverUUID, "/server.properties", int64(DefaultMaxEditSize)}) {
		t.Errorf("call = %+v", got)
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:file.read" || r.activity.entries[0].Properties != `{"file":"/server.properties"}` ||
		r.activity.entries[0].ServerID != 100 || r.activity.entries[0].ActorID != bob || r.activity.entries[0].IP != "203.0.113.9" || r.activity.entries[0].APIKeyID == nil {
		t.Errorf("entries = %+v", r.activity.entries)
	}
}

func TestDownloadAndUpload(t *testing.T) {
	r := newRig(t)
	rp := r.do(bob, "GET", "/download?file=%2Fmods%2Fa%20b.jar", "")
	if rp.Status != 200 || rp.At("object") != "signed_url" || rp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	link, _ := rp.At("attributes", "url").(string)
	const prefix = "https://node1.example.com:8080/download/file?token="
	if !strings.HasPrefix(link, prefix) {
		t.Fatalf("url = %q", link)
	}
	token, _ := url.QueryUnescape(strings.TrimPrefix(link, prefix))
	claims, valid := decodeToken(t, token)
	if !valid || claims["file_path"] != "/mods/a b.jar" || claims["server_uuid"] != serverUUID {
		t.Errorf("claims = %v, valid = %v", claims, valid)
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:file.download" || r.activity.entries[0].Properties != `{"file":"/mods/a b.jar"}` {
		t.Errorf("entries = %+v", r.activity.entries)
	}
	if r.daemon.count() != 0 {
		t.Error("the node was called: it is the browser that talks to it")
	}

	rp = r.do(dave, "GET", "/upload", "")
	if rp.Status != 200 || rp.At("object") != "signed_url" || rp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("upload = %d %s", rp.Status, rp.Raw)
	}
	link, _ = rp.At("attributes", "url").(string)
	const uploadPrefix = "https://node1.example.com:8080/upload/file?token="
	if !strings.HasPrefix(link, uploadPrefix) {
		t.Fatalf("url = %q", link)
	}
	token, _ = url.QueryUnescape(strings.TrimPrefix(link, uploadPrefix))
	claims, valid = decodeToken(t, token)
	if !valid || claims["server_uuid"] != serverUUID || claims["file_path"] != nil {
		t.Errorf("claims = %v, valid = %v", claims, valid)
	}
	// Uploads are not written to the log by the panel: the node knows what arrived.
	if len(r.activity.entries) != 1 {
		t.Errorf("entries = %+v", r.activity.entries)
	}
}

// The authorizer has checked the permission the token needs before the handler runs, so a token maker that refuses anyway is a
// disagreement between the two: the person is told no, and nothing is written down.
func TestATokenThatCannotBeMade(t *testing.T) {
	for _, tt := range []struct {
		name   string
		err    error
		status int
	}{{"no permission", wings.ErrMissingPermission, 403}, {"broken", errBoom, 500}} {
		for _, rt := range []struct{ path string }{{"/download?file=%2Fa"}, {"/upload"}} {
			t.Run(tt.name+rt.path, func(t *testing.T) {
				r := newRig(t, func(o *options) { o.tokens = brokenTokens{tt.err} })
				rp := r.do(bob, "GET", rt.path, "")
				if rp.Status != tt.status || rp.Contains("hunter2") || rp.Contains("signed_url") || len(r.activity.entries) != 0 {
					t.Errorf("answer = %d %s, entries = %+v", rp.Status, rp.Raw, r.activity.entries)
				}
			})
		}
	}
}

func TestEveryOperationHasAPermission(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("an operation without a permission was registered")
		}
	}()
	permission("rm -rf")
}

func TestWrite(t *testing.T) {
	r := newRig(t)
	rp := r.do(dave, "POST", "/write?file=%2Fserver.properties", `{"motd":"hi"}`+"\n")
	if rp.Status != 204 || len(rp.Raw) != 0 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if got := r.daemon.last(); got.Kind != "write" || !reflect.DeepEqual(got.Args, []any{serverUUID, "/server.properties", `{"motd":"hi"}` + "\n"}) {
		t.Errorf("call = %+v", got)
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:file.write" || r.activity.entries[0].Properties != `{"file":"/server.properties"}` {
		t.Errorf("entries = %+v", r.activity.entries)
	}

	// An empty body makes an empty file.
	if rp := r.do(dave, "POST", "/write?file=%2Fempty", ""); rp.Status != 204 || !reflect.DeepEqual(r.daemon.last().Args, []any{serverUUID, "/empty", ""}) {
		t.Errorf("empty: %d %+v", rp.Status, r.daemon.last())
	}
}

func TestWriteSizeLimit(t *testing.T) {
	r := newRig(t, func(o *options) { o.maxEdit = 10 })
	if rp := r.do(bob, "POST", "/write?file=%2Fa", strings.Repeat("a", 10)); rp.Status != 204 {
		t.Errorf("exactly at the limit: %d", rp.Status)
	}
	before := r.daemon.count()
	rp := r.do(bob, "POST", "/write?file=%2Fa", strings.Repeat("a", 11))
	if rp.Status != 413 || rp.Detail() != "The request body is too large." || r.daemon.count() != before {
		t.Errorf("one over: %d %s", rp.Status, rp.Raw)
	}
	// The same limit applies to what the editor opens.
	r.do(bob, "GET", "/contents?file=%2Fa", "")
	if got := r.daemon.last(); got.Args[2] != int64(10) {
		t.Errorf("read limit = %v", got.Args[2])
	}
}

func TestSimpleActions(t *testing.T) {
	tests := []struct {
		name, method, path, body string
		want                     call
		event, properties        string
	}{
		{"create folder", "POST", "/create-folder", `{"root":"/plugins","name":"cfg"}`, call{"create-directory", []any{serverUUID, "cfg", "/plugins"}},
			"server:file.create-directory", `{"directory":"/plugins","name":"cfg"}`},
		{"create folder in the root", "POST", "/create-folder", `{"name":"cfg"}`, call{"create-directory", []any{serverUUID, "cfg", "/"}},
			"server:file.create-directory", `{"directory":"/","name":"cfg"}`},
		{"rename", "PUT", "/rename", `{"root":"/","files":[{"from":"a.txt","to":"b.txt"},{"from":"c","to":"../d"}]}`,
			call{"rename", []any{serverUUID, "/", []wings.RenameItem{{From: "a.txt", To: "b.txt"}, {From: "c", To: "../d"}}}},
			"server:file.rename", `{"directory":"/","files":[{"from":"a.txt","to":"b.txt"},{"from":"c","to":"../d"}]}`},
		{"copy", "POST", "/copy", `{"location":"/a.txt"}`, call{"copy", []any{serverUUID, "/a.txt"}}, "server:file.copy", `{"file":"/a.txt"}`},
		{"delete", "POST", "/delete", `{"root":"/logs","files":["a.log","b.log"]}`, call{"delete", []any{serverUUID, "/logs", []string{"a.log", "b.log"}}},
			"server:file.delete", `{"directory":"/logs","files":["a.log","b.log"]}`},
		{"decompress", "POST", "/decompress", `{"root":"/","file":"a.tar.gz"}`, call{"decompress", []any{serverUUID, "/", "a.tar.gz"}},
			"server:file.decompress", `{"directory":"/","files":"a.tar.gz"}`},
		{"rename in the root", "PUT", "/rename", `{"files":[{"from":"a","to":"b"}]}`, call{"rename", []any{serverUUID, "/", []wings.RenameItem{{From: "a", To: "b"}}}},
			"server:file.rename", `{"directory":"/","files":[{"from":"a","to":"b"}]}`},
		{"delete in the root", "POST", "/delete", `{"files":["a"]}`, call{"delete", []any{serverUUID, "/", []string{"a"}}}, "server:file.delete", `{"directory":"/","files":["a"]}`},
		{"decompress in the root", "POST", "/decompress", `{"file":"a.tar.gz"}`, call{"decompress", []any{serverUUID, "/", "a.tar.gz"}},
			"server:file.decompress", `{"directory":"/","files":"a.tar.gz"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(bob, tt.method, tt.path, tt.body)
			if rp.Status != 204 || len(rp.Raw) != 0 {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if got := r.daemon.last(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("call = %+v, want %+v", got, tt.want)
			}
			if len(r.activity.entries) != 1 || r.activity.entries[0].Event != tt.event || r.activity.entries[0].Properties != tt.properties {
				t.Errorf("entries = %+v", r.activity.entries)
			}
		})
	}
}

func TestCompress(t *testing.T) {
	r := newRig(t)
	r.daemon.archive = wings.FileObject{Name: "archive-2026.tar.gz", Created: "2026-10-23T11:00:00Z", Modified: "2026-10-23T11:00:00Z", Mode: "-rw-r--r--", ModeBits: "644", Size: 99, File: yes(), Mime: "application/gzip"}
	rp := r.do(bob, "POST", "/compress", `{"root":"/","files":["world","plugins"]}`)
	if rp.Status != 200 || rp.At("object") != "file_object" || rp.At("attributes", "name") != "archive-2026.tar.gz" || rp.At("attributes", "size") != 99.0 ||
		rp.At("attributes", "mimetype") != "application/gzip" || rp.At("attributes", "is_file") != true {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if got := r.daemon.last(); got.Kind != "compress" || !reflect.DeepEqual(got.Args, []any{serverUUID, "/", []string{"world", "plugins"}}) {
		t.Errorf("call = %+v", got)
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:file.compress" || r.activity.entries[0].Properties != `{"directory":"/","files":["world","plugins"]}` {
		t.Errorf("entries = %+v", r.activity.entries)
	}
}

func TestChmod(t *testing.T) {
	r := newRig(t)
	rp := r.do(bob, "POST", "/chmod", `{"root":"/bin","files":[{"file":"run.sh","mode":"755"},{"file":"x","mode":644},{"file":"y","mode":"0644"}]}`)
	if rp.Status != 204 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	want := []wings.ChmodItem{{File: "run.sh", Mode: "755"}, {File: "x", Mode: "644"}, {File: "y", Mode: "0644"}}
	if got := r.daemon.last(); got.Kind != "chmod" || !reflect.DeepEqual(got.Args, []any{serverUUID, "/bin", want}) {
		t.Errorf("call = %+v", got)
	}
	if rp := r.do(bob, "POST", "/chmod", `{"files":[{"file":"a","mode":"600"}]}`); rp.Status != 204 ||
		!reflect.DeepEqual(r.daemon.last().Args, []any{serverUUID, "/", []wings.ChmodItem{{File: "a", Mode: "600"}}}) {
		t.Errorf("in the root: %d %+v", rp.Status, r.daemon.last())
	}
	// The reference does not write a line for a change of modes, and neither does this panel.
	if len(r.activity.entries) != 0 {
		t.Errorf("entries = %+v", r.activity.entries)
	}
}

func TestPull(t *testing.T) {
	r := newRig(t)
	rp := r.do(dave, "POST", "/pull", `{"url":"https://example.com/a.zip?key=secret#frag","directory":"/mods","filename":"a.zip","use_header":true,"foreground":false}`)
	if rp.Status != 204 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	want := wings.PullOptions{URL: "https://example.com/a.zip?key=secret#frag", Root: "/mods", FileName: "a.zip", UseHeader: yes(), Foreground: no()}
	if got := r.daemon.last(); got.Kind != "pull" || !reflect.DeepEqual(got.Args, []any{serverUUID, want}) {
		t.Errorf("call = %+v", got)
	}
	// What the address carries after the path is a secret more often than not, and the log is read by others.
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:file.pull" || r.activity.entries[0].Properties != `{"directory":"/mods","url":"https://example.com/a.zip"}` {
		t.Errorf("entries = %+v", r.activity.entries)
	}

	// Only what was asked for is passed on, and the root is the default one.
	if rp := r.do(dave, "POST", "/pull", `{"url":"http://example.com:8080/b.zip"}`); rp.Status != 204 {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	want = wings.PullOptions{URL: "http://example.com:8080/b.zip", Root: "/"}
	if got := r.daemon.last(); !reflect.DeepEqual(got.Args, []any{serverUUID, want}) {
		t.Errorf("call = %+v", got)
	}
	// null is as good as left out.
	if rp := r.do(dave, "POST", "/pull", `{"url":"http://example.com/c.zip","use_header":null,"foreground":null,"filename":null,"directory":null}`); rp.Status != 204 {
		t.Fatalf("null: %d %s", rp.Status, rp.Raw)
	}
	if got := r.daemon.last(); !reflect.DeepEqual(got.Args, []any{serverUUID, wings.PullOptions{URL: "http://example.com/c.zip", Root: "/"}}) {
		t.Errorf("call = %+v", got)
	}
}

func TestPullIsThrottledPerServer(t *testing.T) {
	r := newRig(t)
	pull := func(user uint64, server string) apitest.Reply {
		return r.client.As(user, "POST", "/api/client/servers/"+server+"/files/pull", `{"url":"https://example.com/a.zip"}`)
	}
	// Calls the authorizer turns away do not use up the five.
	for i := 0; i < PullLimit+1; i++ {
		if rp := pull(cleo, "aaaa1111"); rp.Status != 403 {
			t.Fatalf("cleo: %d", rp.Status)
		}
		if rp := pull(eve, "aaaa1111"); rp.Status != 404 {
			t.Fatalf("eve: %d", rp.Status)
		}
	}
	// The five are the server's, whoever pulls.
	for i := 0; i < PullLimit; i++ {
		user := uint64(bob)
		if i%2 == 1 {
			user = ada
		}
		if rp := pull(user, "aaaa1111"); rp.Status != 204 {
			t.Fatalf("pull %d: %d %s", i+1, rp.Status, rp.Raw)
		}
	}
	rp := pull(bob, "aaaa1111")
	if rp.Status != 429 || rp.Header.Get("Retry-After") == "" {
		t.Errorf("the sixth: %d %v", rp.Status, rp.Header)
	}
	if n := r.daemon.count(); n != PullLimit {
		t.Errorf("%d calls reached the node", n)
	}
	// Another server has its own.
	if rp := pull(bob, "bbbb2222"); rp.Status != 204 {
		t.Errorf("other server: %d %s", rp.Status, rp.Raw)
	}
}

func TestNodeFailures(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		detail string
		code   string
	}{
		{"the node is unreachable", &wings.Error{Status: 504, Message: "Could not reach the node.", Unreachable: true}, 504, "Could not reach the node.", "DaemonConnectionException"},
		{"the node refuses", &wings.Error{Status: 404, NodeStatus: 404, Message: "The requested resource was not found on the system."}, 404, "The requested resource was not found on the system.", "DaemonConnectionException"},
		{"something else", errBoom, 500, "An unexpected error was encountered while processing this request, please try again.", "Exception"},
	}
	for _, rt := range routes {
		if rt.name == "download" || rt.name == "upload" {
			continue // the browser talks to the node; the panel only signs
		}
		for _, tc := range cases {
			t.Run(rt.name+"/"+tc.name, func(t *testing.T) {
				r := newRig(t)
				r.daemon.err = tc.err
				rp := r.do(ada, rt.method, rt.path, rt.body)
				if rp.Status != tc.status || rp.Detail() != tc.detail || rp.Code() != tc.code || rp.Contains("hunter2") {
					t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
				}
				// A call that failed is not written down as if it had happened.
				if len(r.activity.entries) != 0 {
					t.Errorf("entries = %+v", r.activity.entries)
				}
			})
		}
	}
}

func TestContentsTooLarge(t *testing.T) {
	r := newRig(t)
	r.daemon.readErr = fmt.Errorf("read: %w", wings.ErrTooLarge)
	rp := r.do(bob, "GET", "/contents?file=%2Fbig", "")
	if rp.Status != 400 || rp.Code() != "DisplayException" || rp.Detail() != "The file you are attempting to open is too large to view in the file editor." {
		t.Errorf("answer = %d %s", rp.Status, rp.Raw)
	}
	if len(r.activity.entries) != 0 {
		t.Errorf("entries = %+v", r.activity.entries)
	}
}

func TestWithoutAServerTarget(t *testing.T) {
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			r := newRig(t)
			r.targets.err = controlapi.ErrNotFound
			if rp := r.do(ada, rt.method, rt.path, rt.body); rp.Status != 404 {
				t.Errorf("gone: status = %d", rp.Status)
			}
			r.targets.err = errBoom
			rp := r.do(ada, rt.method, rt.path, rt.body)
			if rp.Status != 500 || rp.Contains("hunter2") {
				t.Errorf("broken: status = %d %s", rp.Status, rp.Raw)
			}
			if r.daemon.count() != 0 {
				t.Error("the node was called")
			}
		})
	}
}

func TestAnEntryThatFailsDoesNotUndoTheAction(t *testing.T) {
	r := newRig(t)
	r.activity.err = errBoom
	rp := r.do(ada, "POST", "/delete", `{"files":["a"]}`)
	if rp.Status != 204 || rp.Contains("hunter2") {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if !strings.Contains(r.logs.String(), "activity entry not stored") || r.daemon.count() != 1 {
		t.Errorf("log = %q, calls = %d", r.logs.String(), r.daemon.count())
	}
}

// A route registered without the authorizer must refuse, not serve: the permission is the authorizer's, and a handler cannot tell who
// is calling without it.
func TestHandlersRefuseWithoutTheAuthorizer(t *testing.T) {
	r := newRig(t)
	client := apitest.NewWorld().Serve(t, func(g api.Group) {
		b := g.Sub("/bare")
		b.GET("/list", r.handlers.List, api.Public())
		b.GET("/contents", r.handlers.Contents, api.Public())
		b.GET("/download", r.handlers.Download, api.Public())
		b.PUT("/rename", r.handlers.Rename, api.Public())
		b.POST("/copy", r.handlers.Copy, api.Public())
		b.POST("/write", r.handlers.Write, api.Public())
		b.POST("/compress", r.handlers.Compress, api.Public())
		b.POST("/decompress", r.handlers.Decompress, api.Public())
		b.POST("/delete", r.handlers.Delete, api.Public())
		b.POST("/create-folder", r.handlers.CreateFolder, api.Public())
		b.POST("/chmod", r.handlers.Chmod, api.Public())
		b.POST("/pull", r.handlers.Pull, api.Public())
		b.GET("/upload", r.handlers.Upload, api.Public())
	})
	for _, rt := range routes {
		if rp := client.As(0, rt.method, "/bare"+rt.path, rt.body); rp.Status != 403 {
			t.Errorf("%s: status = %d, want 403", rt.name, rp.Status)
		}
	}
	if r.daemon.count() != 0 {
		t.Error("the node was called")
	}
}

func TestNewNeedsEverything(t *testing.T) {
	r := newRig(t)
	full := r.handlers.d
	if _, err := New(full); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Deps){
		"no targets": func(d *Deps) { d.Targets = nil }, "no daemon": func(d *Deps) { d.Daemon = nil }, "no tokens": func(d *Deps) { d.Tokens = nil },
		"no activity": func(d *Deps) { d.Activity = nil }, "no limiter": func(d *Deps) { d.PullLimiter = nil }, "no logger": func(d *Deps) { d.Logger = nil },
	} {
		d := full
		mutate(&d)
		if _, err := New(d); err == nil {
			t.Errorf("%s: no error", name)
		}
	}

	d := full
	d.MaxEditSize, d.Location, d.Now = 0, nil, nil
	h, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	if h.d.MaxEditSize != DefaultMaxEditSize || h.d.Location != time.UTC || h.d.Now == nil {
		t.Errorf("defaults = %d %v %v", h.d.MaxEditSize, h.d.Location, h.d.Now == nil)
	}
	d.MaxEditSize = -5
	if h, _ := New(d); h.d.MaxEditSize != DefaultMaxEditSize {
		t.Errorf("a negative size = %d", h.d.MaxEditSize)
	}
}
