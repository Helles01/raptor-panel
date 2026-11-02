package backupapi

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
	if err := d.Create(ctx, n, serverUUID, "wings", uuidN(1), "*.log"); err != nil {
		t.Fatal(err)
	}
	if err := d.Restore(ctx, n, serverUUID, uuidN(1), "wings", true, ""); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(ctx, n, serverUUID, uuidN(1)); err != nil {
		t.Fatal(err)
	}

	auth := "Bearer " + strings.Repeat("w", 64)
	want := []seen{
		{"POST", "/api/servers/" + serverUUID + "/backup", auth, `{"adapter":"wings","ignore":"*.log","uuid":"` + uuidN(1) + `"}`},
		{"POST", "/api/servers/" + serverUUID + "/backup/" + uuidN(1) + "/restore", auth, `{"adapter":"wings","download_url":"","truncate_directory":true}`},
		{"DELETE", "/api/servers/" + serverUUID + "/backup/" + uuidN(1), auth, ""},
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
	for name, err := range map[string]error{
		"create": d.Create(ctx, bad, serverUUID, "wings", uuidN(1), ""), "restore": d.Restore(ctx, bad, serverUUID, uuidN(1), "wings", false, ""), "delete": d.Delete(ctx, bad, serverUUID, uuidN(1)),
	} {
		if err == nil {
			t.Errorf("%s: no error for a node with a short token", name)
		}
	}
}
