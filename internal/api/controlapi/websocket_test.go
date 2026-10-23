package controlapi

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/wings"
)

const socketURL = "/api/client/servers/aaaa1111/websocket"

func TestWebsocketGivesATokenForTheConsole(t *testing.T) {
	r := newRig(t)

	rp := r.get(bob, socketURL)
	if rp.Status != 200 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if got := rp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q: a token must not be cached", got)
	}
	if got := rp.At("data", "socket"); got != "wss://node1.example.com:8080/api/servers/"+normalUUID+"/ws" {
		t.Errorf("socket = %v", got)
	}

	token, _ := rp.At("data", "token").(string)
	tok := decodeJWT(t, token, node1.Token)
	if !tok.SigOK || tok.Header["alg"] != "HS256" {
		t.Errorf("signature ok %v, header %v: the node checks it with its own token", tok.SigOK, tok.Header)
	}
	c := tok.Claims
	if c["scope"] != "websocket" || c["server_uuid"] != normalUUID || c["user_uuid"] != "00000003-0000-4000-8000-000000000000" || c["iss"] != panelURL {
		t.Errorf("claims = %v", c)
	}
	if aud, _ := c["aud"].([]any); len(aud) != 1 || aud[0] != "https://node1.example.com:8080" {
		t.Errorf("aud = %v, want the address of the node", c["aud"])
	}
	// The token carries exactly what Bob holds, which the node enforces on every message.
	if perms, _ := c["permissions"].([]any); !reflect.DeepEqual(perms, []any{"control.start", "websocket.connect"}) {
		t.Errorf("permissions = %v", c["permissions"])
	}
}

func TestWebsocketTokenCarriesThePermissionsOfTheCaller(t *testing.T) {
	tests := []struct {
		user uint64
		want []any
	}{
		{ada, []any{"*"}},
		{grace, []any{"*", "admin.websocket.errors", "admin.websocket.install", "admin.websocket.transfer"}},
	}
	for _, tt := range tests {
		r := newRig(t)
		rp := r.get(tt.user, socketURL)
		if rp.Status != 200 {
			t.Fatalf("user %d: status = %d: %s", tt.user, rp.Status, rp.Raw)
		}
		tok := decodeJWT(t, rp.At("data", "token").(string), node1.Token)
		if perms, _ := tok.Claims["permissions"].([]any); !reflect.DeepEqual(perms, tt.want) {
			t.Errorf("user %d: permissions = %v, want %v", tt.user, perms, tt.want)
		}
	}
}

func TestWebsocketAccess(t *testing.T) {
	tests := []struct {
		name   string
		user   uint64
		target string
		status int
	}{
		{"a subuser without websocket.connect", cleo, "aaaa1111", 403},
		{"a stranger", eve, "aaaa1111", 404},
		{"nobody", 0, "aaaa1111", 401},
		// Only an administrator may open the console of a server that cannot be used yet.
		{"the owner of a server that is installing", ada, "bbbb2222", 409},
		{"an administrator of a server that is installing", grace, "bbbb2222", 200},
		{"the owner of a server that is being moved", ada, "cccc3333", 409},
		{"an administrator of a server that is being moved", grace, "cccc3333", 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.get(tt.user, "/api/client/servers/"+tt.target+"/websocket")
			if rp.Status != tt.status {
				t.Fatalf("status = %d, want %d: %s", rp.Status, tt.status, rp.Raw)
			}
			if tt.status != 200 && r.targets.calls != 0 {
				t.Error("the node was looked up for a refused request")
			}
		})
	}
}

func TestWebsocketOfAServerBeingMoved(t *testing.T) {
	t.Run("before the files have left the old node", func(t *testing.T) {
		r := newRig(t)
		rp := r.get(grace, "/api/client/servers/cccc3333/websocket")
		if rp.At("data", "socket") != "wss://node1.example.com:8080/api/servers/"+movingUUID+"/ws" {
			t.Errorf("socket = %v", rp.At("data", "socket"))
		}
	})
	t.Run("after, the console is on the new node and signed for it", func(t *testing.T) {
		r := newRig(t)
		r.targets.targets[102] = Target{UUID: movingUUID, Node: node1, Transfer: &Transfer{Archived: true, NewNode: node2}}
		rp := r.get(grace, "/api/client/servers/cccc3333/websocket")
		if rp.At("data", "socket") != "wss://node2.example.com:8443/api/servers/"+movingUUID+"/ws" {
			t.Fatalf("socket = %v", rp.At("data", "socket"))
		}
		tok := decodeJWT(t, rp.At("data", "token").(string), node2.Token)
		if !tok.SigOK {
			t.Error("the token is not signed with the token of the new node")
		}
		if aud, _ := tok.Claims["aud"].([]any); len(aud) != 1 || aud[0] != "https://node2.example.com:8443" {
			t.Errorf("aud = %v", tok.Claims["aud"])
		}
	})
}

func TestConsoleNode(t *testing.T) {
	plain := Target{UUID: normalUUID, Node: node1}
	moving := Target{UUID: movingUUID, Node: node1, Transfer: &Transfer{NewNode: node2}}
	archived := Target{UUID: movingUUID, Node: node1, Transfer: &Transfer{Archived: true, NewNode: node2}}
	admin := []string{"*", "admin.websocket.transfer"}

	tests := []struct {
		name string
		t    Target
		held []string
		want wings.Node
		deny bool
	}{
		{"not moving", plain, []string{"websocket.connect"}, node1, false},
		{"moving, may read the log", moving, admin, node1, false},
		{"archived, may read the log", archived, admin, node2, false},
		// The owner holds "*", which does not cover the administrator's permissions.
		{"moving, an owner", moving, []string{"*"}, wings.Node{}, true},
		{"archived, a subuser", archived, []string{"websocket.connect"}, wings.Node{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := consoleNode(tt.t, tt.held)
			if (err != nil) != tt.deny || got != tt.want {
				t.Errorf("node %v, error %v; want %v, denied %v", got, err, tt.want, tt.deny)
			}
			if err != nil && !strings.Contains(err.Error(), "You do not have permission to view server transfer logs.") {
				t.Errorf("error = %v", err)
			}
		})
	}
}

func TestWebsocketIsThrottledForEachServer(t *testing.T) {
	r := newRig(t)

	// Five a minute for a server, whoever asks.
	for i := 0; i < WebsocketLimit; i++ {
		user := uint64(ada)
		if i%2 == 1 {
			user = bob
		}
		if rp := r.get(user, socketURL); rp.Status != 200 {
			t.Fatalf("request %d: status = %d", i+1, rp.Status)
		}
	}
	rp := r.get(ada, socketURL)
	if rp.Status != 429 || rp.Code() != "ThrottleRequestsException" || rp.Header.Get("Retry-After") == "" {
		t.Errorf("the sixth call: %d %s, Retry-After %q", rp.Status, rp.Raw, rp.Header.Get("Retry-After"))
	}

	// Another server has its own allowance.
	if rp := r.get(ada, "/api/client/servers/dddd4444/websocket"); rp.Status != 200 {
		t.Errorf("another server: status = %d", rp.Status)
	}
}

func TestRefusedCallsDoNotUseUpTheAllowance(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 10; i++ {
		r.get(eve, socketURL)  // a stranger: 404
		r.get(cleo, socketURL) // no websocket.connect: 403
		r.get(0, socketURL)    // nobody: 401
	}
	for i := 0; i < WebsocketLimit; i++ {
		if rp := r.get(ada, socketURL); rp.Status != 200 {
			t.Fatalf("request %d: status = %d: the allowance was used up by calls that were refused", i+1, rp.Status)
		}
	}
}

func TestWebsocketFailures(t *testing.T) {
	t.Run("the node of the server is gone", func(t *testing.T) {
		r := newRig(t)
		delete(r.targets.targets, 100)
		if rp := r.get(ada, socketURL); rp.Status != 404 {
			t.Errorf("status = %d", rp.Status)
		}
	})
	t.Run("the store is broken", func(t *testing.T) {
		r := newRig(t)
		r.targets.err = errBoom
		rp := r.get(ada, socketURL)
		if rp.Status != 500 || rp.Contains("hunter2") {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
	t.Run("the token maker refuses for lack of the permission", func(t *testing.T) {
		r := newRig(t, func(o *options) { o.tokens = failingTokens{wings.ErrMissingPermission} })
		rp := r.get(ada, socketURL)
		if rp.Status != 403 || rp.Detail() != "You do not have permission to connect to this server's websocket." {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
	t.Run("the token maker fails otherwise", func(t *testing.T) {
		r := newRig(t, func(o *options) { o.tokens = failingTokens{errBoom} })
		rp := r.get(ada, socketURL)
		if rp.Status != 500 || rp.Contains("hunter2") {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
	t.Run("only GET", func(t *testing.T) {
		r := newRig(t)
		if rp := r.post(ada, socketURL, `{}`); rp.Status != 404 && rp.Status != 405 {
			t.Errorf("POST: status = %d", rp.Status)
		}
	})
}

func TestNewNeedsEverything(t *testing.T) {
	r := newRig(t)
	full := Deps{Targets: r.targets, Daemon: r.daemon, Tokens: failingTokens{}, Activity: r.activity, WebsocketLimiter: r.handlers.d.WebsocketLimiter, Logger: quiet}

	if _, err := New(full); err != nil {
		t.Fatalf("a full set: %v", err)
	}
	for name, mutate := range map[string]func(*Deps){
		"no targets":  func(d *Deps) { d.Targets = nil },
		"no daemon":   func(d *Deps) { d.Daemon = nil },
		"no tokens":   func(d *Deps) { d.Tokens = nil },
		"no activity": func(d *Deps) { d.Activity = nil },
		"no limiter":  func(d *Deps) { d.WebsocketLimiter = nil },
		"no logger":   func(d *Deps) { d.Logger = nil },
	} {
		d := full
		mutate(&d)
		if _, err := New(d); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// The authorizer puts the caller and the server in the request. A route wired without them must be refused, not served.
func TestHandlersRefuseWithoutTheirPrincipal(t *testing.T) {
	r := newRig(t)
	client := r.world.Serve(t, func(g api.Group) {
		bare := g.Sub("/bare")
		bare.GET("/websocket", r.handlers.Websocket, api.Public())
		bare.GET("/resources", r.handlers.Resources, api.Public())
		bare.POST("/power", r.handlers.Power, api.Public())
		bare.POST("/command", r.handlers.Command, api.Public())
		// A signed-in caller on a route that has no server.
		noServer := g.Sub("/noserver")
		noServer.GET("/websocket", r.handlers.Websocket, api.Authenticated())
		noServer.GET("/resources", r.handlers.Resources, api.Authenticated())
		noServer.POST("/power", r.handlers.Power, api.Authenticated())
		noServer.POST("/command", r.handlers.Command, api.Authenticated())
	})

	for _, prefix := range []string{"/bare", "/noserver"} {
		for _, route := range []struct{ method, path, body string }{
			{"GET", "/websocket", ""}, {"GET", "/resources", ""},
			{"POST", "/power", `{"signal":"start"}`}, {"POST", "/command", `{"command":"list"}`},
		} {
			if rp := client.As(ada, route.method, prefix+route.path, route.body); rp.Status != 403 {
				t.Errorf("%s%s: status = %d, want 403", prefix, route.path, rp.Status)
			}
		}
	}
	if r.targets.calls != 0 || len(r.daemon.all()) != 0 || len(r.activity.all()) != 0 {
		t.Error("something was asked, called or written")
	}
}
