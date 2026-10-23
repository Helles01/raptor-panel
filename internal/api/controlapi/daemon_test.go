package controlapi

import (
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

// fakeWings is a node that answers as it is told and keeps what it was sent.
type fakeWings struct {
	srv    *httptest.Server
	mu     sync.Mutex
	status int
	body   string
	got    []sent
}

type sent struct{ Method, Path, Auth, Body string }

func newFakeWings(t *testing.T) *fakeWings {
	t.Helper()
	f := &fakeWings{status: 204}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.got = append(f.got, sent{r.Method, r.URL.Path, r.Header.Get("Authorization"), string(raw)})
		status, body := f.status, f.body
		f.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeWings) node(t *testing.T) wings.Node {
	t.Helper()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(f.srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := strconv.Atoi(port)
	return wings.Node{Scheme: "http", FQDN: host, Port: p, Token: strings.Repeat("w", 64)}
}

func (f *fakeWings) reply(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, body
}

func (f *fakeWings) requests() []sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sent(nil), f.got...)
}

// realRig is a rig whose calls to the node go over HTTP to a fake Wings, through the real client.
func realRig(t *testing.T) (*rig, *fakeWings) {
	t.Helper()
	f := newFakeWings(t)
	factory, err := wings.NewFactory(wings.ClientOptions{Timeout: 5 * time.Second, ConnectTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t, func(o *options) { o.daemon = NewDaemon(factory) })
	r.targets.targets[100] = Target{UUID: normalUUID, Node: f.node(t)}
	return r, f
}

func TestTheRealClientCarriesTheCalls(t *testing.T) {
	r, f := realRig(t)

	if rp := r.post(ada, "/api/client/servers/aaaa1111/power", `{"signal":"restart"}`); rp.Status != 204 {
		t.Fatalf("power: status = %d: %s", rp.Status, rp.Raw)
	}
	if rp := r.post(ada, "/api/client/servers/aaaa1111/command", `{"command":"say hi"}`); rp.Status != 204 {
		t.Fatalf("command: status = %d: %s", rp.Status, rp.Raw)
	}
	f.reply(200, `{"state":"running","is_suspended":false,"utilization":{"memory_bytes":5,"cpu_absolute":1.5,"disk_bytes":6,"uptime":7,"network":{"rx_bytes":8,"tx_bytes":9}}}`)
	rp := r.get(ada, "/api/client/servers/aaaa1111/resources")
	if rp.Status != 200 || rp.At("attributes", "current_state") != "running" || rp.At("attributes", "resources", "network_tx_bytes") != 9.0 {
		t.Fatalf("resources: %d %s", rp.Status, rp.Raw)
	}

	token := "Bearer " + strings.Repeat("w", 64)
	want := []sent{
		{"POST", "/api/servers/" + normalUUID + "/power", token, `{"action":"restart"}`},
		{"POST", "/api/servers/" + normalUUID + "/commands", token, `{"commands":["say hi"]}`},
		{"GET", "/api/servers/" + normalUUID, token, ""},
	}
	got := f.requests()
	if len(got) != len(want) {
		t.Fatalf("%d requests: %+v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestTheRealClientsErrorsReachTheCaller(t *testing.T) {
	t.Run("a refusal of the node keeps its status and its text", func(t *testing.T) {
		r, f := realRig(t)
		f.reply(409, `{"error":"server is already running"}`)
		rp := r.post(ada, "/api/client/servers/aaaa1111/power", `{"signal":"start"}`)
		if rp.Status != 409 || rp.Code() != "DaemonConnectionException" ||
			rp.Detail() != "An error occurred on the remote host: server is already running. (request id: <nil>)" {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
		if len(r.activity.all()) != 0 {
			t.Error("an entry was written for a refused signal")
		}
	})
	t.Run("a 502 of the node means the server is offline", func(t *testing.T) {
		r, f := realRig(t)
		f.reply(502, ``)
		rp := r.post(ada, "/api/client/servers/aaaa1111/command", `{"command":"list"}`)
		if rp.Status != 502 || rp.Code() != "HttpException" || rp.Detail() != "Server must be online in order to send commands." {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
	t.Run("a reply the panel cannot use is a 502 of its own", func(t *testing.T) {
		r, f := realRig(t)
		f.reply(200, `not json`)
		rp := r.get(ada, "/api/client/servers/aaaa1111/resources")
		if rp.Status != 502 || rp.Code() != "DaemonConnectionException" {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
	t.Run("an error of the node does not show its internals", func(t *testing.T) {
		r, f := realRig(t)
		f.reply(500, `{"error":"panic at /srv/daemon/secret.go"}`)
		rp := r.post(ada, "/api/client/servers/aaaa1111/power", `{"signal":"start"}`)
		if rp.Status != 500 || rp.Contains("secret.go") || rp.Contains("127.0.0.1") {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
	t.Run("a node that is not there", func(t *testing.T) {
		r, f := realRig(t)
		f.srv.Close()
		rp := r.post(ada, "/api/client/servers/aaaa1111/power", `{"signal":"start"}`)
		if rp.Status != 504 || rp.Code() != "DaemonConnectionException" || rp.Contains("127.0.0.1") {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
	t.Run("a node that is set up wrongly", func(t *testing.T) {
		r, _ := realRig(t)
		bad := node1
		bad.Token = "short"
		r.targets.targets[100] = Target{UUID: normalUUID, Node: bad}
		rp := r.post(ada, "/api/client/servers/aaaa1111/power", `{"signal":"start"}`)
		if rp.Status != 500 || rp.Contains("short") {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
}

func TestANodeSetUpWronglyFailsEveryCall(t *testing.T) {
	r, _ := realRig(t)
	bad := node1
	bad.Token = "short"
	r.targets.targets[100] = Target{UUID: normalUUID, Node: bad}

	for _, call := range []func() int{
		func() int { return r.post(ada, "/api/client/servers/aaaa1111/command", `{"command":"list"}`).Status },
		func() int { return r.get(ada, "/api/client/servers/aaaa1111/resources").Status },
	} {
		if status := call(); status != 500 {
			t.Errorf("status = %d, want 500", status)
		}
	}
}
