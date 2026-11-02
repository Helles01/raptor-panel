package fileapi

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/wings"
)

type seen struct{ Method, Path, Query, Auth, Body string }

func TestTheRealDaemonCarriesTheCalls(t *testing.T) {
	var mu sync.Mutex
	var got []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, seen{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), string(raw)})
		mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/list-directory"):
			_, _ = w.Write([]byte(`[{"name":"a.txt","size":3,"file":true}]`))
		case strings.HasSuffix(r.URL.Path, "/contents"):
			_, _ = w.Write([]byte("hello"))
		case strings.HasSuffix(r.URL.Path, "/compress"):
			_, _ = w.Write([]byte(`{"name":"x.tar.gz","size":9}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	p, _ := strconv.Atoi(port)
	n := wings.Node{Scheme: "http", FQDN: host, Port: p, Token: strings.Repeat("w", 64)}

	factory, err := wings.NewFactory(wings.ClientOptions{Timeout: 5 * time.Second, ConnectTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	d := NewDaemon(factory)
	ctx := context.Background()

	list, err := d.List(ctx, n, serverUUID, "/")
	if err != nil || len(list) != 1 || list[0].Name != "a.txt" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	content, err := d.Read(ctx, n, serverUUID, "/a.txt", 100)
	if err != nil || string(content) != "hello" {
		t.Fatalf("read = %q, %v", content, err)
	}
	if _, err := d.Read(ctx, n, serverUUID, "/a.txt", 2); err == nil {
		t.Error("a file longer than the limit was read")
	}
	archive, err := d.Compress(ctx, n, serverUUID, "/", []string{"a"})
	if err != nil || archive.Name != "x.tar.gz" {
		t.Fatalf("compress = %+v, %v", archive, err)
	}
	yes := true
	for _, step := range []struct {
		name string
		err  error
	}{
		{"write", d.Write(ctx, n, serverUUID, "/a.txt", []byte("x"))},
		{"create", d.CreateDirectory(ctx, n, serverUUID, "d", "/")},
		{"rename", d.Rename(ctx, n, serverUUID, "/", []wings.RenameItem{{From: "a", To: "b"}})},
		{"copy", d.Copy(ctx, n, serverUUID, "/a")},
		{"delete", d.Delete(ctx, n, serverUUID, "/", []string{"a"})},
		{"decompress", d.Decompress(ctx, n, serverUUID, "/", "a.zip")},
		{"chmod", d.Chmod(ctx, n, serverUUID, "/", []wings.ChmodItem{{File: "a", Mode: "644"}})},
		{"pull", d.Pull(ctx, n, serverUUID, wings.PullOptions{URL: "https://example.com/a", Root: "/", UseHeader: &yes})},
	} {
		if step.err != nil {
			t.Errorf("%s: %v", step.name, step.err)
		}
	}

	auth := "Bearer " + strings.Repeat("w", 64)
	f := "/api/servers/" + serverUUID + "/files/"
	want := []seen{
		{"GET", f + "list-directory", "directory=%2F", auth, ""},
		{"GET", f + "contents", "file=%2Fa.txt", auth, ""},
		{"GET", f + "contents", "file=%2Fa.txt", auth, ""},
		{"POST", f + "compress", "", auth, `{"files":["a"],"root":"/"}`},
		{"POST", f + "write", "file=%2Fa.txt", auth, "x"},
		{"POST", f + "create-directory", "", auth, `{"name":"d","path":"/"}`},
		{"PUT", f + "rename", "", auth, `{"files":[{"from":"a","to":"b"}],"root":"/"}`},
		{"POST", f + "copy", "", auth, `{"location":"/a"}`},
		{"POST", f + "delete", "", auth, `{"files":["a"],"root":"/"}`},
		{"POST", f + "decompress", "", auth, `{"file":"a.zip","root":"/"}`},
		{"POST", f + "chmod", "", auth, `{"files":[{"file":"a","mode":"644"}],"root":"/"}`},
		{"POST", f + "pull", "", auth, `{"root":"/","url":"https://example.com/a","use_header":true}`},
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != len(want) {
		t.Fatalf("%d requests: %+v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	// A node that is set up wrongly is an error of the call.
	bad := n
	bad.Token = "short"
	errs := map[string]error{
		"list":       func() error { _, err := d.List(ctx, bad, serverUUID, "/"); return err }(),
		"read":       func() error { _, err := d.Read(ctx, bad, serverUUID, "/a", 5); return err }(),
		"write":      d.Write(ctx, bad, serverUUID, "/a", nil),
		"create":     d.CreateDirectory(ctx, bad, serverUUID, "d", "/"),
		"rename":     d.Rename(ctx, bad, serverUUID, "/", nil),
		"copy":       d.Copy(ctx, bad, serverUUID, "/a"),
		"delete":     d.Delete(ctx, bad, serverUUID, "/", nil),
		"compress":   func() error { _, err := d.Compress(ctx, bad, serverUUID, "/", nil); return err }(),
		"decompress": d.Decompress(ctx, bad, serverUUID, "/", "a"),
		"chmod":      d.Chmod(ctx, bad, serverUUID, "/", nil),
		"pull":       d.Pull(ctx, bad, serverUUID, wings.PullOptions{}),
	}
	for name, err := range errs {
		if err == nil {
			t.Errorf("%s: no error for a node with a short token", name)
		}
	}
}

func TestSubjectIsTheCaller(t *testing.T) {
	s := subject{id: 7, uuid: "u-7", held: []string{"file.read", "file.create"}}
	if s.ID() != 7 || s.UUID() != "u-7" || !s.Can("file.read") || s.Can("file.delete") || !reflect.DeepEqual(s.Permissions(), []string{"file.read", "file.create"}) {
		t.Errorf("subject = %+v", s)
	}
	if !(subject{held: []string{"*"}}).Can("file.delete") {
		t.Error("an owner holds every permission")
	}
}
