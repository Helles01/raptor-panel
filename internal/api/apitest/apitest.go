// Package apitest is the set-up the tests of the endpoint packages share: a world of users, servers and subusers behind the real
// authorizer, a real router, and a client that calls it with an API key of a user. Nothing here is for production code.
package apitest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/auth/session"
)

// Now is the clock of the authorizer in these tests.
var Now = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

// Secret is the secret of every key of the world: 32 characters.
const Secret = "abcdefghijklmnopqrstuvwxyzABCDEF"

// KeyIdentifier is the identifier of the client API key of a user, 16 characters as a key's must be.
func KeyIdentifier(user uint64) string { return fmt.Sprintf("ptlc_user%07d", user) }

// World holds the users, servers and subusers the authorizer reads. Fill the maps before the first call; the world is safe to
// read from the handlers of one test.
type World struct {
	mu sync.Mutex

	Users   map[uint64]authz.User
	Servers []authz.Server
	// Subusers holds the permissions of a subuser, by server and user.
	Subusers map[[2]uint64][]string
	// Nodes holds the nodes that may call the node API, by the id of their token (see AddNode).
	Nodes map[string]authz.NodeCredentials
	// TwoFactorLevel is the panel's requirement: 0 none, 1 administrators, 2 everybody.
	TwoFactorLevel int
}

// NewWorld returns an empty world.
func NewWorld() *World {
	return &World{Users: map[uint64]authz.User{}, Subusers: map[[2]uint64][]string{}, Nodes: map[string]authz.NodeCredentials{}}
}

// AddUser adds a user whose UUID is made of the number.
func (w *World) AddUser(id uint64, admin bool) authz.User {
	u := authz.User{ID: id, UUID: fmt.Sprintf("%08d-0000-4000-8000-000000000000", id), RootAdmin: admin}
	w.Users[id] = u
	return u
}

// AddServer adds a server on node 1. The short ID is the first eight characters of the UUID.
func (w *World) AddServer(id uint64, uuid string, owner uint64) *authz.Server {
	w.Servers = append(w.Servers, authz.Server{ID: id, UUID: uuid, ShortID: uuid[:8], OwnerID: owner, NodeID: 1, NodeName: "node-1"})
	return &w.Servers[len(w.Servers)-1]
}

// The stores the authorizer reads.

func (w *World) ByID(_ context.Context, id uint64) (authz.User, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if u, ok := w.Users[id]; ok {
		return u, nil
	}
	return authz.User{}, authz.ErrNoUser
}

func (w *World) ByIdentifier(_ context.Context, identifier string) (authz.Key, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for id := range w.Users {
		if KeyIdentifier(id) == identifier {
			return authz.Key{ID: id, UserID: id, Type: auth.KeyAccount, Identifier: identifier, SecretHash: auth.HashKeySecret(Secret)}, nil
		}
	}
	return authz.Key{}, authz.ErrNoKey
}

func (w *World) Touch(context.Context, uint64, time.Time) error { return nil }

func (w *World) Find(_ context.Context, ref string) (authz.Server, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, s := range w.Servers {
		if s.ShortID == ref || s.UUID == ref {
			return s, nil
		}
	}
	return authz.Server{}, authz.ErrNoServer
}

func (w *World) SubuserPermissions(_ context.Context, serverID, userID uint64) ([]string, bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p, ok := w.Subusers[[2]uint64{serverID, userID}]
	return p, ok, nil
}

func (w *World) ByTokenID(_ context.Context, id string) (authz.NodeCredentials, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if n, ok := w.Nodes[id]; ok {
		return n, nil
	}
	return authz.NodeCredentials{}, authz.ErrNoNode
}

func (w *World) TwoFactorLevelOf(context.Context) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.TwoFactorLevel, nil
}

func (w *World) IPBlocked(context.Context, uint64, string, string, string) error { return nil }

// settings adapts the world to authz.Settings, whose method has the name of the field.
type settings struct{ w *World }

func (s settings) TwoFactorLevel(ctx context.Context) (int, error) { return s.w.TwoFactorLevelOf(ctx) }

// Serve builds the server with the real authorizer, lets register add the routes under a group that has the session loaded, and
// returns a client for it.
func (w *World) Serve(t *testing.T, register func(g api.Group)) *Client {
	t.Helper()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	az, err := authz.New(authz.Deps{Users: w, Keys: w, Servers: w, Nodes: w, Settings: settings{w}, Audit: w, Logger: quiet, Now: func() time.Time { return Now }})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(api.Options{
		Logger: quiet, Authorizer: az,
		Trust: middleware.NewProxyTrust([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, quiet),
	})
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := session.NewManager(session.NewMemoryStore(), session.Options{IdleTimeout: time.Hour, MaxLifetime: 8 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	register(srv.Router.Sub("", middleware.LoadSession(sessions, false)))
	h, err := srv.Handler()
	if err != nil {
		t.Fatal(err)
	}
	return &Client{t: t, h: h}
}

// Client calls the server.
type Client struct {
	t *testing.T
	h http.Handler
}

// As sends a request as a user, who signs in with a client API key; user 0 sends none. A body, if not empty, is sent as JSON.
func (c *Client) As(user uint64, method, target, body string) Reply {
	c.t.Helper()
	header := ""
	if user != 0 {
		header = "Bearer " + KeyIdentifier(user) + Secret
	}
	return c.send(header, method, target, body)
}

// send makes the request with the Authorization header, which may be empty.
func (c *Client) send(authorization, method, target, body string) Reply {
	c.t.Helper()
	var payload io.Reader
	if body != "" {
		payload = bytes.NewBufferString(body)
	}
	req := httptest.NewRequest(method, target, payload)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)

	out := Reply{Status: rec.Code, Header: rec.Header(), Raw: rec.Body.Bytes()}
	_ = json.Unmarshal(out.Raw, &out.Body)
	return out
}

// Reply is an answer, decoded.
type Reply struct {
	Status int
	Header http.Header
	Raw    []byte
	Body   map[string]any
}

// At walks the decoded body: strings are keys, integers are positions in a list. It returns nil when the path is not there.
func (r Reply) At(path ...any) any {
	var cur any = r.Body
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := cur.(map[string]any)
			if !ok {
				return nil
			}
			cur = m[k]
		case int:
			l, ok := cur.([]any)
			if !ok || k >= len(l) {
				return nil
			}
			cur = l[k]
		}
	}
	return cur
}

// Has reports whether a key is present, null or not.
func (r Reply) Has(path ...any) bool {
	if len(path) == 0 {
		return true
	}
	parent, ok := r.At(path[:len(path)-1]...).(map[string]any)
	if !ok {
		return false
	}
	_, found := parent[path[len(path)-1].(string)]
	return found
}

// Detail is the text of the first error.
func (r Reply) Detail() string {
	d, _ := r.At("errors", 0, "detail").(string)
	return d
}

// Code is the code of the first error.
func (r Reply) Code() string {
	d, _ := r.At("errors", 0, "code").(string)
	return d
}

// Contains reports whether the raw body has the text.
func (r Reply) Contains(s string) bool { return strings.Contains(string(r.Raw), s) }

// NodeTokenID is the id of the token of a node, 16 characters; NodeSecret is the secret of every node's token.
func NodeTokenID(node uint64) string { return fmt.Sprintf("node%012d", node) }

var NodeSecret = strings.Repeat("n", 64)

// AddNode lets a node call the node API with the token of NodeTokenID and NodeSecret.
func (w *World) AddNode(id uint64) {
	w.Nodes[NodeTokenID(id)] = authz.NodeCredentials{Node: authz.Node{ID: id, UUID: fmt.Sprintf("%08d-1111-4111-8111-111111111111", id)}, Token: NodeSecret}
}

// AsNode sends a request as a node, with its token; node 0 sends none.
func (c *Client) AsNode(node uint64, method, target, body string) Reply {
	c.t.Helper()
	header := ""
	if node != 0 {
		header = "Bearer " + NodeTokenID(node) + "." + NodeSecret
	}
	return c.send(header, method, target, body)
}
