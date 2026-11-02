package appservers

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/wings"
)

type seen struct{ Method, Path, Auth, Body string }

func TestTheRealDaemonCarriesTheCalls(t *testing.T) {
	var mu sync.Mutex
	var got []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, seen{r.Method, r.URL.Path, r.Header.Get("Authorization"), string(raw)})
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
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
	for name, err := range map[string]error{
		"create": d.Create(ctx, n, uuid1, true), "sync": d.Sync(ctx, n, uuid1), "reinstall": d.Reinstall(ctx, n, uuid1), "delete": d.Delete(ctx, n, uuid1),
	} {
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	auth := "Bearer " + strings.Repeat("w", 64)
	want := map[seen]bool{
		{"POST", "/api/servers", auth, `{"start_on_completion":true,"uuid":"` + uuid1 + `"}`}: true,
		{"POST", "/api/servers/" + uuid1 + "/sync", auth, ""}:                                 true,
		{"POST", "/api/servers/" + uuid1 + "/reinstall", auth, ""}:                            true,
		{"DELETE", "/api/servers/" + uuid1, auth, ""}:                                         true,
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != len(want) {
		t.Fatalf("%d requests: %+v", len(got), got)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected request %+v", g)
		}
	}

	// A node that is set up wrongly is an error of the call.
	bad := n
	bad.Token = "short"
	for name, err := range map[string]error{
		"create": d.Create(ctx, bad, uuid1, false), "sync": d.Sync(ctx, bad, uuid1), "reinstall": d.Reinstall(ctx, bad, uuid1), "delete": d.Delete(ctx, bad, uuid1),
	} {
		if err == nil {
			t.Errorf("%s: no error for a node with a short token", name)
		}
	}
}
