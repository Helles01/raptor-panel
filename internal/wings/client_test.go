package wings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type recorded struct {
	Method, Path, RawQuery, Auth, ContentType, Body string
}

// fakeWings is a node that records what it receives and answers with a canned reply.
type fakeWings struct {
	srv    *httptest.Server
	mu     sync.Mutex
	calls  []recorded
	status int
	body   string
	header http.Header
	delay  time.Duration
}

func newFakeWings(t *testing.T, tls bool) *fakeWings {
	t.Helper()
	f := &fakeWings{status: http.StatusNoContent, header: http.Header{}}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, recorded{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), string(b)})
		status, body, header, delay := f.status, f.body, f.header, f.delay
		f.mu.Unlock()

		time.Sleep(delay)
		for k, v := range header {
			w.Header()[k] = v
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
	if tls {
		f.srv = httptest.NewTLSServer(handler)
	} else {
		f.srv = httptest.NewServer(handler)
	}
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeWings) reply(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, body
}

func (f *fakeWings) last(t *testing.T) recorded {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("the node received no request")
	}
	return f.calls[len(f.calls)-1]
}

func (f *fakeWings) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeWings) node(t *testing.T) Node {
	t.Helper()
	u, err := url.Parse(f.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	return Node{Scheme: u.Scheme, FQDN: u.Hostname(), Port: port, Token: nodeToken}
}

func newClient(t *testing.T, f *fakeWings, o ClientOptions) *Client {
	t.Helper()
	if o.Timeout == 0 {
		o.Timeout = 5 * time.Second
	}
	if o.ConnectTimeout == 0 {
		o.ConnectTimeout = time.Second
	}
	factory, err := NewFactory(o)
	if err != nil {
		t.Fatal(err)
	}
	c, err := factory.Client(f.node(t))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPower(t *testing.T) {
	f := newFakeWings(t, false)
	c := newClient(t, f, ClientOptions{})

	for _, action := range []string{"start", "stop", "restart", "kill"} {
		if err := c.Power(context.Background(), serverUUID, action); err != nil {
			t.Fatalf("Power(%s): %v", action, err)
		}
		got := f.last(t)
		if got.Method != "POST" || got.Path != "/api/servers/"+serverUUID+"/power" || got.Body != fmt.Sprintf(`{"action":%q}`, action) {
			t.Errorf("request = %+v", got)
		}
		if got.Auth != "Bearer "+nodeToken || got.ContentType != "application/json" {
			t.Errorf("headers: auth %q content type %q", got.Auth, got.ContentType)
		}
		if got.RawQuery != "" {
			t.Errorf("the token must travel in a header, not the URL: %q", got.RawQuery)
		}
	}
}

func TestPowerAndCommandRejectBadInputBeforeAnyRequest(t *testing.T) {
	f := newFakeWings(t, false)
	c := newClient(t, f, ClientOptions{})
	ctx := context.Background()

	tests := map[string]error{
		"unknown action":      c.Power(ctx, serverUUID, "reboot"),
		"empty action":        c.Power(ctx, serverUUID, ""),
		"upper case action":   c.Power(ctx, serverUUID, "START"),
		"action with a space": c.Power(ctx, serverUUID, "start "),
		"server is a path":    c.Power(ctx, "../../etc", "start"),
		"empty server":        c.Power(ctx, "", "start"),
		"no commands":         c.Command(ctx, serverUUID),
		"empty command":       c.Command(ctx, serverUUID, ""),
		"NUL in a command":    c.Command(ctx, serverUUID, "say a\x00b"),
		"command too long":    c.Command(ctx, serverUUID, strings.Repeat("a", 4097)),
		"too many commands":   c.Command(ctx, serverUUID, make([]string, 101)...),
		"command, bad server": c.Command(ctx, "x", "say hi"),
	}
	for name, err := range tests {
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if f.count() != 0 {
		t.Errorf("%d requests reached the node for invalid input", f.count())
	}
}

func TestCommand(t *testing.T) {
	f := newFakeWings(t, false)
	c := newClient(t, f, ClientOptions{})

	if err := c.Command(context.Background(), serverUUID, "say hello", "list"); err != nil {
		t.Fatal(err)
	}

	got := f.last(t)
	if got.Path != "/api/servers/"+serverUUID+"/commands" || got.Body != `{"commands":["say hello","list"]}` {
		t.Errorf("request = %+v", got)
	}
}

func TestDetails(t *testing.T) {
	f := newFakeWings(t, false)
	f.reply(200, `{"state":"running","is_suspended":false,"utilization":{"memory_bytes":1048576,"memory_limit_bytes":2097152,
		"cpu_absolute":12.5,"disk_bytes":4096,"uptime":3600,"network":{"rx_bytes":10,"tx_bytes":20},"extra":"ignored"},"unknown":1}`)
	c := newClient(t, f, ClientOptions{})

	d, err := c.Details(context.Background(), serverUUID)
	if err != nil {
		t.Fatal(err)
	}

	if d.State != "running" || d.IsSuspended || d.Utilization.MemoryBytes != 1048576 || d.Utilization.MemoryLimitBytes != 2097152 ||
		d.Utilization.CPUAbsolute != 12.5 || d.Utilization.DiskBytes != 4096 || d.Utilization.Uptime != 3600 ||
		d.Utilization.Network.RxBytes != 10 || d.Utilization.Network.TxBytes != 20 {
		t.Errorf("details = %+v", d)
	}
	if got := f.last(t); got.Method != "GET" || got.Path != "/api/servers/"+serverUUID || got.Body != "" || got.ContentType != "" {
		t.Errorf("request = %+v", got)
	}

	f.reply(200, `not json`)
	if _, err := c.Details(context.Background(), serverUUID); err == nil {
		t.Error("an unusable reply was accepted")
	} else {
		var we *Error
		if !errors.As(err, &we) || we.Status != 502 {
			t.Errorf("error = %v, want a 502", err)
		}
	}
}

func TestDeauthorize(t *testing.T) {
	f := newFakeWings(t, false)
	c := newClient(t, f, ClientOptions{})

	if err := c.Deauthorize(context.Background(), userUUID, serverUUID); err != nil {
		t.Fatal(err)
	}
	if got := f.last(t); got.Path != "/api/deauthorize-user" || got.Body != `{"servers":["`+serverUUID+`"],"user":"`+userUUID+`"}` {
		t.Errorf("request = %+v", got)
	}

	if err := c.Deauthorize(context.Background(), userUUID); err != nil {
		t.Fatal(err)
	}
	if got := f.last(t); !strings.Contains(got.Body, `"servers":[]`) {
		t.Errorf("no servers must be an empty array: %s", got.Body)
	}

	if c.Deauthorize(context.Background(), "nope") == nil || c.Deauthorize(context.Background(), userUUID, "../x") == nil {
		t.Error("bad uuids were accepted")
	}
}

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		header      http.Header
		wantStatus  int
		wantContain string
		wantNot     string
	}{
		{"client error with a message", 404, `{"error":"The requested resource was not found on the system."}`, http.Header{"X-Request-Id": {"req-1"}},
			404, "An error occurred on the remote host: The requested resource was not found on the system. (request id: req-1)", ""},
		{"conflict", 409, `{"error":"server is already running"}`, nil, 409, "An error occurred on the remote host: server is already running. (request id: <nil>)", ""},
		{"client error without a message", 400, `{}`, nil, 400, "(code: 400) (request_id: <nil>)", ""},
		{"client error that is not json", 403, `<html>nope</html>`, nil, 403, "(code: 403)", "<html>"},
		{"server error hides the message", 500, `{"error":"panic: secret path /srv/x"}`, http.Header{"X-Request-Id": {"abc"}},
			500, "There was an error while communicating with the machine running this server. This error has been logged, please try again. (code: 500) (request_id: abc)", "secret path"},
		{"bad gateway", 502, ``, nil, 502, "(code: 502)", ""},
		{"a redirect is not followed and not trusted", 302, ``, http.Header{"Location": {"http://elsewhere.example.com/"}}, 502, "(code: 302)", ""},
		{"control characters in the message", 400, `{"error":"line1\nline2\u0000x"}`, nil, 400, "line1 line2 x", "\n"},
		// Go's HTTP stack already refuses control characters in header values; an over-long id is still possible.
		{"over-long request id", 400, `{"error":"x"}`, http.Header{"X-Request-Id": {strings.Repeat("a", 100)}}, 400, "(request id: <nil>)", "aaaa"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeWings(t, false)
			f.header = tt.header
			f.reply(tt.status, tt.body)
			c := newClient(t, f, ClientOptions{})

			err := c.Power(context.Background(), serverUUID, "start")

			var we *Error
			if !errors.As(err, &we) {
				t.Fatalf("error = %v, want *wings.Error", err)
			}
			if we.Status != tt.wantStatus || we.Unreachable {
				t.Errorf("status %d unreachable %v, want %d", we.Status, we.Unreachable, tt.wantStatus)
			}
			if !strings.Contains(we.Message, tt.wantContain) {
				t.Errorf("message = %q, want it to contain %q", we.Message, tt.wantContain)
			}
			if tt.wantNot != "" && strings.Contains(we.Message, tt.wantNot) {
				t.Errorf("message = %q must not contain %q", we.Message, tt.wantNot)
			}
		})
	}
}

func TestLongNodeMessagesAreCut(t *testing.T) {
	f := newFakeWings(t, false)
	f.reply(400, `{"error":"`+strings.Repeat("x", 2000)+`"}`)
	err := newClient(t, f, ClientOptions{}).Power(context.Background(), serverUUID, "start")

	var we *Error
	if !errors.As(err, &we) || len(we.Message) > 700 {
		t.Errorf("message length = %d", len(we.Message))
	}
}

func TestUnreachableNode(t *testing.T) {
	f := newFakeWings(t, false)
	c := newClient(t, f, ClientOptions{ConnectTimeout: 200 * time.Millisecond})
	f.srv.Close() // nothing listens any more

	err := c.Power(context.Background(), serverUUID, "start")

	var we *Error
	if !errors.As(err, &we) || we.Status != 504 || !we.Unreachable ||
		we.Message != "Could not establish a connection to the machine running this server. Please try again." {
		t.Errorf("error = %#v", err)
	}
	if strings.Contains(we.Message, "127.0.0.1") {
		t.Error("the address of the node must not be in the user-facing message")
	}
	if we.Unwrap() == nil {
		t.Error("the cause must be kept for logs")
	}
}

func TestSlowNodeTimesOut(t *testing.T) {
	f := newFakeWings(t, false)
	f.delay = 500 * time.Millisecond
	c := newClient(t, f, ClientOptions{Timeout: 100 * time.Millisecond})

	start := time.Now()
	err := c.Power(context.Background(), serverUUID, "start")

	var we *Error
	if !errors.As(err, &we) || we.Status != 504 || time.Since(start) > 400*time.Millisecond {
		t.Errorf("error = %v after %v", err, time.Since(start))
	}
}

func TestContextCancellation(t *testing.T) {
	f := newFakeWings(t, false)
	f.delay = 300 * time.Millisecond
	c := newClient(t, f, ClientOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if err := c.Power(ctx, serverUUID, "start"); err == nil {
		t.Error("a cancelled request succeeded")
	}
}

// A redirect must not be followed: the node token would be sent to whatever address it names.
func TestRedirectsAreNotFollowed(t *testing.T) {
	var mu sync.Mutex
	var leaked []string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		leaked = append(leaked, r.Header.Get("Authorization"))
		mu.Unlock()
	}))
	defer target.Close()

	f := newFakeWings(t, false)
	f.header = http.Header{"Location": {target.URL + "/steal"}}
	f.reply(307, ``)
	c := newClient(t, f, ClientOptions{})

	if err := c.Power(context.Background(), serverUUID, "start"); err == nil {
		t.Error("a redirect answer must be an error")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(leaked) != 0 {
		t.Errorf("the redirect target received %d requests, with authorization %v", len(leaked), leaked)
	}
}

// The node token travels in a header; a proxy named in the environment must never see it.
func TestEnvironmentProxiesAreIgnored(t *testing.T) {
	var mu sync.Mutex
	proxied := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		proxied++
		mu.Unlock()
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")

	f := newFakeWings(t, false)
	c := newClient(t, f, ClientOptions{})
	if err := c.Power(context.Background(), serverUUID, "start"); err != nil {
		t.Fatalf("Power: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if proxied != 0 || f.count() != 1 {
		t.Errorf("proxy saw %d requests, node saw %d", proxied, f.count())
	}
}

func TestTLSVerification(t *testing.T) {
	f := newFakeWings(t, true) // a self-signed certificate

	strict := newClient(t, f, ClientOptions{})
	err := strict.Power(context.Background(), serverUUID, "start")
	var we *Error
	if !errors.As(err, &we) || !we.Unreachable || f.count() != 0 {
		t.Errorf("a self-signed certificate must be refused by default: %v (node saw %d requests)", err, f.count())
	}

	lax := newClient(t, f, ClientOptions{InsecureTLS: true})
	if err := lax.Power(context.Background(), serverUUID, "start"); err != nil || f.count() != 1 {
		t.Errorf("with InsecureTLS: %v (node saw %d requests)", err, f.count())
	}
}

func TestOversizedRepliesAreRefused(t *testing.T) {
	f := newFakeWings(t, false)
	f.reply(200, `{"state":"`+strings.Repeat("a", 2<<20)+`"}`)

	_, err := newClient(t, f, ClientOptions{}).Details(context.Background(), serverUUID)

	var we *Error
	if !errors.As(err, &we) || we.Status != 502 {
		t.Errorf("error = %v", err)
	}
}

func TestTheNodeTokenNeverAppearsInErrorsOrLogs(t *testing.T) {
	n := testNode()

	var logs strings.Builder
	slog.New(slog.NewTextHandler(&logs, nil)).Info("node", "node", n)

	for name, text := range map[string]string{
		"String": n.String(), "%v": fmt.Sprintf("%v", n), "%+v": fmt.Sprintf("%+v", n), "%#v": fmt.Sprintf("%#v", n), "slog": logs.String(),
	} {
		if strings.Contains(text, nodeToken) {
			t.Errorf("%s leaks the node token: %s", name, text)
		}
	}

	// An unreachable node's error carries the address but not the header.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	factory, _ := NewFactory(ClientOptions{Timeout: time.Second, ConnectTimeout: 200 * time.Millisecond})
	c, _ := factory.Client(Node{Scheme: "http", FQDN: "127.0.0.1", Port: port, Token: nodeToken})
	err := c.Power(context.Background(), serverUUID, "start")
	if err == nil || strings.Contains(err.Error(), nodeToken) {
		t.Errorf("error = %v", err)
	}
}

func TestFactoryAndClientValidate(t *testing.T) {
	for _, o := range []ClientOptions{{}, {Timeout: time.Second}, {ConnectTimeout: time.Second}, {Timeout: -1, ConnectTimeout: time.Second}} {
		if _, err := NewFactory(o); err == nil {
			t.Errorf("NewFactory accepted %+v", o)
		}
	}
	factory, _ := NewFactory(ClientOptions{Timeout: time.Second, ConnectTimeout: time.Second})
	if _, err := factory.Client(Node{Scheme: "https", FQDN: "a.example.com", Port: 8080, Token: "short"}); err == nil {
		t.Error("a node with a short token was accepted")
	}
}

func TestBodiesAreWellFormedJSON(t *testing.T) {
	f := newFakeWings(t, false)
	c := newClient(t, f, ClientOptions{})
	_ = c.Command(context.Background(), serverUUID, `say "quoted" \ and <tags> é`)

	var body struct{ Commands []string }
	if err := json.Unmarshal([]byte(f.last(t).Body), &body); err != nil || len(body.Commands) != 1 || body.Commands[0] != `say "quoted" \ and <tags> é` {
		t.Errorf("body = %q, %v", f.last(t).Body, err)
	}
}
