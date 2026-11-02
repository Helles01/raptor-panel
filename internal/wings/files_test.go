package wings

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFileCalls(t *testing.T) {
	f := newFakeWings(t, false)
	c := newClient(t, f, ClientOptions{})
	ctx := context.Background()
	base := "/api/servers/" + serverUUID + "/files/"
	yes := true

	tests := []struct {
		name   string
		call   func() error
		method string
		path   string
		query  string
		body   string
	}{
		{"create a directory", func() error { return c.CreateDirectory(ctx, serverUUID, "plugins", "/") }, "POST", base + "create-directory", "", `{"name":"plugins","path":"/"}`},
		{"rename", func() error {
			return c.RenameFiles(ctx, serverUUID, "/", []RenameItem{{From: "a.txt", To: "b.txt"}})
		}, "PUT", base + "rename", "", `{"files":[{"from":"a.txt","to":"b.txt"}],"root":"/"}`},
		{"copy", func() error { return c.CopyFile(ctx, serverUUID, "/a.txt") }, "POST", base + "copy", "", `{"location":"/a.txt"}`},
		{"delete", func() error { return c.DeleteFiles(ctx, serverUUID, "/logs", []string{"a.log", "b.log"}) }, "POST", base + "delete", "", `{"files":["a.log","b.log"],"root":"/logs"}`},
		{"chmod", func() error { return c.ChmodFiles(ctx, serverUUID, "/", []ChmodItem{{File: "run.sh", Mode: "755"}}) }, "POST", base + "chmod", "", `{"files":[{"file":"run.sh","mode":"755"}],"root":"/"}`},
		{"decompress", func() error { return c.DecompressFile(ctx, serverUUID, "/", "a.tar.gz") }, "POST", base + "decompress", "", `{"file":"a.tar.gz","root":"/"}`},
		{"write", func() error { return c.WriteFile(ctx, serverUUID, "/server.properties", []byte("motd=hi\n")) }, "POST", base + "write", "file=%2Fserver.properties", "motd=hi\n"},
		{"pull", func() error {
			return c.PullFile(ctx, serverUUID, PullOptions{URL: "https://example.com/a.zip", Root: "/", FileName: "a.zip", UseHeader: &yes})
		}, "POST", base + "pull", "", `{"file_name":"a.zip","root":"/","url":"https://example.com/a.zip","use_header":true}`},
		{"pull, only what was asked", func() error {
			return c.PullFile(ctx, serverUUID, PullOptions{URL: "https://example.com/a.zip", Root: "/x"})
		}, "POST", base + "pull", "", `{"root":"/x","url":"https://example.com/a.zip"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); err != nil {
				t.Fatal(err)
			}
			got := f.last(t)
			if got.Method != tt.method || got.Path != tt.path || got.RawQuery != tt.query || got.Body != tt.body || got.Auth != "Bearer "+nodeToken {
				t.Errorf("request = %+v", got)
			}
		})
	}
}

func TestListDirectory(t *testing.T) {
	f := newFakeWings(t, false)
	f.reply(200, `[{"name":"server.jar","created":"2026-10-01T10:00:00Z","modified":"2026-10-02T10:00:00Z","mode":"-rw-r--r--","mode_bits":"644","size":1234,"directory":false,"file":true,"symlink":false,"mime":"application/java-archive"},
		{"name":"logs","created":"2026-10-01T10:00:00Z","modified":"2026-10-02T10:00:00Z","mode":"drwxr-xr-x","mode_bits":"755","size":4096,"directory":true,"file":false,"symlink":false,"mime":"inode/directory"}]`)
	c := newClient(t, f, ClientOptions{})

	list, err := c.ListDirectory(context.Background(), serverUUID, "/some dir/&x")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "server.jar" || list[0].Size != 1234 || list[0].File == nil || !*list[0].File || list[1].Mime != "inode/directory" {
		t.Errorf("list = %+v", list)
	}
	got := f.last(t)
	if got.Method != "GET" || got.Path != "/api/servers/"+serverUUID+"/files/list-directory" || got.RawQuery != "directory=%2Fsome+dir%2F%26x" {
		t.Errorf("request = %+v: the directory must be escaped", got)
	}

	f.reply(404, `{"error":"The requested resource was not found on the system."}`)
	var we *Error
	if _, err := c.ListDirectory(context.Background(), serverUUID, "/x"); !errors.As(err, &we) || we.NodeStatus != 404 {
		t.Errorf("error = %v", err)
	}
}

func TestReadFile(t *testing.T) {
	f := newFakeWings(t, false)
	f.reply(200, "motd=hello\n")
	c := newClient(t, f, ClientOptions{})

	got, err := c.ReadFile(context.Background(), serverUUID, "/server.properties", 1024)
	if err != nil || string(got) != "motd=hello\n" {
		t.Fatalf("content %q, error %v", got, err)
	}
	if req := f.last(t); req.Method != "GET" || req.RawQuery != "file=%2Fserver.properties" {
		t.Errorf("request = %+v", req)
	}

	// A file longer than the editor takes is refused without being kept.
	if _, err := c.ReadFile(context.Background(), serverUUID, "/big", 5); !errors.Is(err, ErrTooLarge) {
		t.Errorf("error = %v, want ErrTooLarge", err)
	}
	// Exactly the limit is fine.
	if got, err := c.ReadFile(context.Background(), serverUUID, "/big", 11); err != nil || len(got) != 11 {
		t.Errorf("at the limit: %q %v", got, err)
	}
	// A refusal of the node is read whatever the limit.
	f.reply(404, `{"error":"The requested resource was not found on the system."}`)
	var we *Error
	if _, err := c.ReadFile(context.Background(), serverUUID, "/x", 3); !errors.As(err, &we) || we.NodeStatus != 404 {
		t.Errorf("error = %v", err)
	}
}

func TestReadFileWithoutALength(t *testing.T) {
	// A reply sent in pieces has no length up front; it is cut at the limit all the same.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 10; i++ {
			_, _ = w.Write([]byte("0123456789"))
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()
	f := &fakeWings{srv: srv}
	c := newClient(t, f, ClientOptions{})
	if _, err := c.ReadFile(context.Background(), serverUUID, "/x", 50); !errors.Is(err, ErrTooLarge) {
		t.Errorf("error = %v, want ErrTooLarge", err)
	}
}

func TestCompressFiles(t *testing.T) {
	f := newFakeWings(t, false)
	f.reply(200, `{"name":"archive-2026.tar.gz","created":"2026-10-02T10:00:00Z","modified":"2026-10-02T10:00:00Z","mode":"-rw-r--r--","mode_bits":"644","size":99,"file":true,"mime":"application/gzip"}`)
	c := newClient(t, f, ClientOptions{})

	got, err := c.CompressFiles(context.Background(), serverUUID, "/", []string{"world", "plugins"})
	if err != nil || got.Name != "archive-2026.tar.gz" || got.Size != 99 {
		t.Fatalf("archive %+v, error %v", got, err)
	}
	if req := f.last(t); req.Path != "/api/servers/"+serverUUID+"/files/compress" || req.Body != `{"files":["world","plugins"],"root":"/"}` {
		t.Errorf("request = %+v", req)
	}
}

// Packing takes longer than the usual timeout, so it has a timeout of its own, and the usual one does not apply.
func TestSlowCallsHaveALongerTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		_, _ = w.Write([]byte(`{"name":"a.tar.gz"}`))
	}))
	defer srv.Close()
	f := &fakeWings{srv: srv}

	short := newClient(t, f, ClientOptions{Timeout: 150 * time.Millisecond, LongTimeout: 5 * time.Second})
	var we *Error
	if _, err := short.ListDirectory(context.Background(), serverUUID, "/"); !errors.As(err, &we) || !we.Unreachable {
		t.Errorf("an ordinary call that is slow: %v", err)
	}
	if got, err := short.CompressFiles(context.Background(), serverUUID, "/", []string{"a"}); err != nil || got.Name != "a.tar.gz" {
		t.Errorf("a call that is slow by nature: %+v %v", got, err)
	}
	if err := short.DecompressFile(context.Background(), serverUUID, "/", "a.tar.gz"); err != nil {
		t.Errorf("decompress: %v", err)
	}

	tight := newClient(t, f, ClientOptions{Timeout: 100 * time.Millisecond, LongTimeout: 150 * time.Millisecond})
	if _, err := tight.CompressFiles(context.Background(), serverUUID, "/", []string{"a"}); !errors.As(err, &we) || !we.Unreachable {
		t.Errorf("a call slower than even the long timeout: %v", err)
	}
}

func TestFileCallsRejectABadServerBeforeAnyRequest(t *testing.T) {
	f := newFakeWings(t, false)
	c := newClient(t, f, ClientOptions{})
	ctx := context.Background()
	for name, err := range map[string]error{
		"list":       func() error { _, err := c.ListDirectory(ctx, "../x", "/"); return err }(),
		"read":       func() error { _, err := c.ReadFile(ctx, "x", "/a", 5); return err }(),
		"write":      c.WriteFile(ctx, "x", "/a", nil),
		"create":     c.CreateDirectory(ctx, "x", "a", "/"),
		"rename":     c.RenameFiles(ctx, "x", "/", nil),
		"copy":       c.CopyFile(ctx, "x", "/a"),
		"delete":     c.DeleteFiles(ctx, "x", "/", nil),
		"compress":   func() error { _, err := c.CompressFiles(ctx, "x", "/", nil); return err }(),
		"decompress": c.DecompressFile(ctx, "x", "/", "a"),
		"chmod":      c.ChmodFiles(ctx, "x", "/", nil),
		"pull":       c.PullFile(ctx, "x", PullOptions{}),
	} {
		if err == nil || strings.Contains(err.Error(), "wings 4") {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
	if f.count() != 0 {
		t.Errorf("%d requests reached the node for a server that is not a UUID", f.count())
	}
}
