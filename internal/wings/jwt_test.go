package wings

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	serverUUID = "9f4d2c10-0000-4000-8000-000000000001"
	userUUID   = "11111111-2222-4333-8444-555555555555"
	backupUUID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	nodeToken  = "kRxj1xqCwMBmYYLU4n1sAUmuRVY9NCtwk7J3hFrRzAMLBlDvP5Jb4HEQPqsoQSEz"
)

type fakeUser struct {
	id    uint64
	uuid  string
	perms map[string]bool
	all   []string
}

func (u fakeUser) ID() uint64            { return u.id }
func (u fakeUser) UUID() string          { return u.uuid }
func (u fakeUser) Can(p string) bool     { return u.perms[p] }
func (u fakeUser) Permissions() []string { return u.all }

func userWith(perms ...string) fakeUser {
	m := map[string]bool{}
	for _, p := range perms {
		m[p] = true
	}
	return fakeUser{id: 42, uuid: userUUID, perms: m, all: perms}
}

func testNode() Node {
	return Node{Scheme: "https", FQDN: "node1.example.com", Port: 8080, Token: nodeToken}
}

func newTestIssuer(t *testing.T) (*Issuer, time.Time) {
	t.Helper()
	i, err := NewIssuer("https://panel.example.com")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	i.now = func() time.Time { return now }
	return i, now
}

// verify is what Wings does: check the signature with the node token, and read the claims.
func verify(t *testing.T, token, key string) (header map[string]any, claims map[string]any) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q does not have three parts", token)
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil)); want != parts[2] {
		t.Fatalf("the signature does not verify with the node token")
	}
	decode := func(s string, into *map[string]any) {
		raw, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatalf("part is not base64url: %v", err)
		}
		if err := json.Unmarshal(raw, into); err != nil {
			t.Fatalf("part is not JSON: %v", err)
		}
	}
	decode(parts[0], &header)
	decode(parts[1], &claims)
	return header, claims
}

func TestEveryTokenHasExactlyOneScopeAndItsClaims(t *testing.T) {
	i, now := newTestIssuer(t)
	n := testNode()
	identity := sha256.Sum256([]byte("42" + serverUUID))

	tests := []struct {
		name      string
		issue     func() (string, error)
		wantScope string
		wantTTL   time.Duration
		wantExtra map[string]any
	}{
		{"websocket", func() (string, error) {
			return i.Websocket(n, userWith(PermissionWebsocket, "control.start"), serverUUID)
		},
			"websocket", 10 * time.Minute, map[string]any{"permissions": []any{PermissionWebsocket, "control.start"}}},
		{"upload", func() (string, error) { return i.FileUpload(n, userWith(PermissionFileUpload), serverUUID) },
			"file-upload", 15 * time.Minute, nil},
		{"file download", func() (string, error) {
			return i.FileDownload(n, userWith(PermissionFileDownload), serverUUID, "/logs/latest.log")
		},
			"file-download", 15 * time.Minute, map[string]any{"file_path": "/logs/latest.log"}},
		{"backup download", func() (string, error) {
			return i.BackupDownload(n, userWith(PermissionBackupDownload), serverUUID, backupUUID)
		},
			"backup-download", 15 * time.Minute, map[string]any{"backup_uuid": backupUUID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, err := tt.issue()
			if err != nil {
				t.Fatalf("issue: %v", err)
			}

			header, c := verify(t, token, nodeToken)

			if header["alg"] != "HS256" || header["typ"] != "JWT" {
				t.Errorf("header = %v", header)
			}
			if c["scope"] != tt.wantScope || strings.Contains(tt.wantScope, " ") {
				t.Errorf("scope = %v, want exactly %q", c["scope"], tt.wantScope)
			}
			want := map[string]any{
				"iss": "https://panel.example.com", "aud": []any{"https://node1.example.com:8080"},
				"jti": hex.EncodeToString(identity[:]),
				"iat": float64(now.Unix()), "nbf": float64(now.Add(-5 * time.Minute).Unix()), "exp": float64(now.Add(tt.wantTTL).Unix()),
				"server_uuid": serverUUID, "user_uuid": userUUID,
			}
			for k, v := range tt.wantExtra {
				want[k] = v
			}
			for k, v := range want {
				if got := c[k]; !jsonEqual(got, v) {
					t.Errorf("claim %s = %v, want %v", k, got, v)
				}
			}
			if id, _ := c["unique_id"].(string); len(id) != 16 {
				t.Errorf("unique_id = %v, want 16 characters", c["unique_id"])
			}
			// Nothing beyond what Wings reads.
			allowed := map[string]bool{"iss": true, "aud": true, "jti": true, "iat": true, "nbf": true, "exp": true, "scope": true,
				"user_uuid": true, "unique_id": true, "server_uuid": true, "permissions": true, "file_path": true, "backup_uuid": true}
			for k := range c {
				if !allowed[k] {
					t.Errorf("unexpected claim %q", k)
				}
			}
			if _, has := c["permissions"]; has != (tt.name == "websocket") {
				t.Errorf("permissions claim present = %v for %s", has, tt.name)
			}
		})
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// A token is made only when the user has the permission of its scope (GHSA-8r6w-3qq5-4p4r: a subuser with only
// websocket.connect must not get a token that uploads files).
func TestTokensNeedThePermissionOfTheirScope(t *testing.T) {
	i, _ := newTestIssuer(t)
	n := testNode()
	onlyWebsocket := userWith(PermissionWebsocket)
	nothing := userWith()

	tests := []struct {
		name  string
		issue func() (string, error)
	}{
		{"upload with only websocket.connect", func() (string, error) { return i.FileUpload(n, onlyWebsocket, serverUUID) }},
		{"file download with only websocket.connect", func() (string, error) { return i.FileDownload(n, onlyWebsocket, serverUUID, "/a") }},
		{"backup download with only websocket.connect", func() (string, error) { return i.BackupDownload(n, onlyWebsocket, serverUUID, backupUUID) }},
		{"websocket with nothing", func() (string, error) { return i.Websocket(n, nothing, serverUUID) }},
		{"websocket with file permissions", func() (string, error) {
			return i.Websocket(n, userWith(PermissionFileUpload, PermissionFileDownload), serverUUID)
		}},
		{"upload with the download permission", func() (string, error) { return i.FileUpload(n, userWith(PermissionFileDownload), serverUUID) }},
		{"download with the upload permission", func() (string, error) { return i.FileDownload(n, userWith(PermissionFileUpload), serverUUID, "/a") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, err := tt.issue()
			if !errors.Is(err, ErrMissingPermission) || token != "" {
				t.Errorf("token %q, error %v; want no token and ErrMissingPermission", token, err)
			}
		})
	}
}

func TestIssuingRejectsBadInput(t *testing.T) {
	i, _ := newTestIssuer(t)
	user := userWith(PermissionWebsocket, PermissionFileUpload, PermissionFileDownload, PermissionBackupDownload)

	tests := []struct {
		name  string
		issue func() (string, error)
	}{
		{"server uuid is a path", func() (string, error) { return i.Websocket(testNode(), user, "../../etc") }},
		{"empty server uuid", func() (string, error) { return i.FileUpload(testNode(), user, "") }},
		{"uuid with trailing newline", func() (string, error) { return i.FileUpload(testNode(), user, serverUUID+"\n") }},
		{"user without a valid uuid", func() (string, error) {
			u := user
			u.uuid = "not-a-uuid"
			return i.FileUpload(testNode(), u, serverUUID)
		}},
		{"empty file path", func() (string, error) { return i.FileDownload(testNode(), user, serverUUID, "") }},
		{"file path with a control character", func() (string, error) { return i.FileDownload(testNode(), user, serverUUID, "/a\x00b") }},
		{"file path too long", func() (string, error) {
			return i.FileDownload(testNode(), user, serverUUID, "/"+strings.Repeat("a", 4096))
		}},
		{"backup that is not a uuid", func() (string, error) { return i.BackupDownload(testNode(), user, serverUUID, "../x") }},
		{"node with a short token", func() (string, error) {
			n := testNode()
			n.Token = "short"
			return i.FileUpload(n, user, serverUUID)
		}},
		{"node with an empty token", func() (string, error) {
			n := testNode()
			n.Token = ""
			return i.FileUpload(n, user, serverUUID)
		}},
		{"node with a bad scheme", func() (string, error) {
			n := testNode()
			n.Scheme = "ftp"
			return i.FileUpload(n, user, serverUUID)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if token, err := tt.issue(); err == nil || token != "" {
				t.Errorf("token %q, error %v; want an error and no token", token, err)
			}
		})
	}
}

func TestEachTokenIsUnique(t *testing.T) {
	i, _ := newTestIssuer(t)
	user := userWith(PermissionFileUpload)

	a, _ := i.FileUpload(testNode(), user, serverUUID)
	b, _ := i.FileUpload(testNode(), user, serverUUID)
	_, ca := verify(t, a, nodeToken)
	_, cb := verify(t, b, nodeToken)

	if a == b || ca["unique_id"] == cb["unique_id"] {
		t.Error("two tokens share a unique_id; Wings accepts a unique token only once")
	}
	if ca["jti"] != cb["jti"] {
		t.Error("the jti must be the same for one user and server, it is what a revocation matches")
	}
}

func TestATokenOnlyVerifiesWithTheNodeToken(t *testing.T) {
	i, _ := newTestIssuer(t)
	token, _ := i.FileUpload(testNode(), userWith(PermissionFileUpload), serverUUID)

	parts := strings.Split(token, ".")
	mac := hmac.New(sha256.New, []byte(strings.Repeat("x", 64)))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) == parts[2] {
		t.Error("the token verifies with another key")
	}
	if strings.Contains(token, nodeToken) {
		t.Error("the token contains the node token")
	}
}

func TestWebsocketPermissionsAreAlwaysAnArray(t *testing.T) {
	i, _ := newTestIssuer(t)
	// A user who may connect but whose effective list is empty must still send "permissions": [], not omit it.
	u := userWith(PermissionWebsocket)
	u.all = nil

	token, err := i.Websocket(testNode(), u, serverUUID)
	if err != nil {
		t.Fatal(err)
	}
	_, c := verify(t, token, nodeToken)
	if perms, ok := c["permissions"].([]any); !ok || len(perms) != 0 {
		t.Errorf("permissions = %v (%T)", c["permissions"], c["permissions"])
	}

	// The claim is a copy: changing the subject's slice later must not change an issued token's source.
	u.all = []string{"*"}
	token2, _ := i.Websocket(testNode(), u, serverUUID)
	_, c2 := verify(t, token2, nodeToken)
	if !jsonEqual(c2["permissions"], []any{"*"}) {
		t.Errorf("permissions = %v", c2["permissions"])
	}
}

func TestNewIssuerValidatesTheURL(t *testing.T) {
	for _, bad := range []string{"", "panel.example.com", "ftp://x.example.com", "https://", "javascript:alert(1)"} {
		if _, err := NewIssuer(bad); err == nil {
			t.Errorf("NewIssuer accepted %q", bad)
		}
	}
}

func TestNodeURLs(t *testing.T) {
	https := testNode()
	plain := Node{Scheme: "http", FQDN: "10.0.0.5", Port: 8080, Token: nodeToken}
	v6 := Node{Scheme: "https", FQDN: "2001:db8::1", Port: 443, Token: nodeToken}

	tests := []struct{ got, want string }{
		{https.Address(), "https://node1.example.com:8080"},
		{plain.Address(), "http://10.0.0.5:8080"},
		{v6.Address(), "https://[2001:db8::1]:443"},
		{https.WebsocketURL(serverUUID), "wss://node1.example.com:8080/api/servers/" + serverUUID + "/ws"},
		{plain.WebsocketURL(serverUUID), "ws://10.0.0.5:8080/api/servers/" + serverUUID + "/ws"},
		{https.UploadURL("a.b.c"), "https://node1.example.com:8080/upload/file?token=a.b.c"},
		{https.FileDownloadURL("a b&c"), "https://node1.example.com:8080/download/file?token=a+b%26c"},
		{https.BackupDownloadURL("t"), "https://node1.example.com:8080/download/backup?token=t"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("got %q, want %q", tt.got, tt.want)
		}
	}
}

func TestNodeValidate(t *testing.T) {
	good := testNode()
	if err := good.Validate(); err != nil {
		t.Fatalf("a good node is refused: %v", err)
	}
	tests := map[string]func(*Node){
		"empty host":        func(n *Node) { n.FQDN = "" },
		"host with a port":  func(n *Node) { n.FQDN = "node1.example.com:8080" },
		"host with a path":  func(n *Node) { n.FQDN = "node1.example.com/x" },
		"host with a space": func(n *Node) { n.FQDN = "node 1.example.com" },
		"port zero":         func(n *Node) { n.Port = 0 },
		"port too large":    func(n *Node) { n.Port = 70000 },
		"empty scheme":      func(n *Node) { n.Scheme = "" },
		"short token":       func(n *Node) { n.Token = strings.Repeat("a", 31) },
	}
	for name, mutate := range tests {
		n := good
		mutate(&n)
		if err := n.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
