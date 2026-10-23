package remoteapi

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/api/apitest"
)

const sftpURL = "/api/remote/sftp/auth"

func sftpBody(kind, username, password, ip string) string {
	b, _ := json.Marshal(map[string]string{"type": kind, "username": username, "password": password, "ip": ip})
	return string(b)
}

func passwordBody(username, password string) string {
	return sftpBody("password", username, password, "198.51.100.7:51234")
}

func perms(rp apitest.Reply) []string {
	l, _ := rp.At("permissions").([]any)
	out := make([]string, len(l))
	for i, v := range l {
		out[i], _ = v.(string)
	}
	return out
}

func TestSFTPSignIn(t *testing.T) {
	tests := []struct {
		name     string
		username string
		want     []string
	}{
		{"the owner", "ada.aaaa1111", []string{"*"}},
		{"an administrator", "root.aaaa1111", []string{"*", "admin.websocket.errors", "admin.websocket.install", "admin.websocket.transfer"}},
		{"a subuser with the right", "bob.aaaa1111", []string{"file.sftp", "file.read"}},
		{"a user name in other case", "ADA.aaaa1111", []string{"*"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.post(node1, sftpURL, passwordBody(tt.username, adaPW))
			if rp.Status != 200 {
				t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
			}
			if rp.At("server") != normalUUID || !reflect.DeepEqual(perms(rp), tt.want) {
				t.Errorf("answer = %s", rp.Raw)
			}
			if rp.Header.Get("Cache-Control") != "no-store" {
				t.Errorf("Cache-Control = %q", rp.Header.Get("Cache-Control"))
			}
			if len(r.store.events) != 0 {
				t.Errorf("events = %+v", r.store.events)
			}
		})
	}

	t.Run("the user in the answer", func(t *testing.T) {
		r := newRig(t)
		rp := r.post(node1, sftpURL, passwordBody("ada.aaaa1111", adaPW))
		if rp.At("user") != userUUID(ada) {
			t.Errorf("user = %v", rp.At("user"))
		}
	})
	t.Run("a user name with a dot in it", func(t *testing.T) {
		r := newRig(t)
		r.store.users["a.b"] = SFTPUser{ID: 9, UUID: userUUID(9), PasswordHash: adaHash}
		r.store.records[0].owner = 9
		if rp := r.post(node1, sftpURL, passwordBody("a.b.aaaa1111", adaPW)); rp.Status != 200 {
			t.Errorf("status = %d: %s", rp.Status, rp.Raw)
		}
	})
	t.Run("the type may be left out", func(t *testing.T) {
		r := newRig(t)
		b, _ := json.Marshal(map[string]string{"username": "ada.aaaa1111", "password": adaPW, "ip": "198.51.100.7:51234"})
		if rp := r.post(node1, sftpURL, string(b)); rp.Status != 200 {
			t.Errorf("status = %d: %s", rp.Status, rp.Raw)
		}
	})
}

func TestSFTPRefusals(t *testing.T) {
	const rejected = "Authorization credentials were not correct, please try again."
	tests := []struct {
		name     string
		node     uint64
		username string
		password string
	}{
		{"a wrong password", node1, "ada.aaaa1111", "wrong"},
		{"a user that does not exist", node1, "nobody.aaaa1111", adaPW},
		{"a server that does not exist", node1, "ada.12345678", adaPW},
		{"a server of another node", node1, "ada.ffff6666", adaPW},
		{"a node that does not have the server", node2, "ada.aaaa1111", adaPW},
		{"a name that cannot be one", node1, "a b.aaaa1111", adaPW},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.post(tt.node, sftpURL, passwordBody(tt.username, tt.password))
			// Whatever was wrong, the answer is the same.
			if rp.Status != 403 || rp.Detail() != rejected || rp.Code() != "AccessDeniedHttpException" {
				t.Errorf("answer = %d %s", rp.Status, rp.Raw)
			}
		})
	}

	t.Run("a server of another node is refused even if the store finds it", func(t *testing.T) {
		r := newRig(t)
		r.store.leak = true
		rp := r.post(node1, sftpURL, passwordBody("ada.ffff6666", adaPW))
		if rp.Status != 403 || rp.Detail() != rejected {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
}

func TestSFTPMalformedRequests(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		status int
	}{
		{"no server in the name", passwordBody("ada", adaPW), 400},
		{"a server id of the wrong length", passwordBody("ada.aaaa111", adaPW), 400},
		{"a server id with odd characters", passwordBody("ada.aaaa111!", adaPW), 400},
		{"an unknown type", sftpBody("keyboard", "ada.aaaa1111", adaPW, ""), 400},
		{"no password", passwordBody("ada.aaaa1111", ""), 400},
		{"no user name", passwordBody("", adaPW), 400},
		{"not JSON", `username=ada`, 400},
		{"empty", ``, 400},
		{"a password too long", passwordBody("ada.aaaa1111", strings.Repeat("a", 70<<10)), 413},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			if rp := r.post(node1, sftpURL, tt.body); rp.Status != tt.status {
				t.Errorf("status = %d, want %d: %s", rp.Status, tt.status, rp.Raw)
			}
			if len(r.store.events) != 0 {
				t.Errorf("events = %+v", r.store.events)
			}
		})
	}
}

func TestSFTPFailuresAreLoggedForTheUserAndTheirAddress(t *testing.T) {
	r := newRig(t)
	r.post(node1, sftpURL, passwordBody("ada.aaaa1111", "wrong"))

	if len(r.store.events) != 1 {
		t.Fatalf("events = %+v", r.store.events)
	}
	want := Event{Name: "auth:sftp.fail", UserSubject: ada, IP: "198.51.100.7", Properties: `{"method":"password"}`}
	if got := r.store.events[0]; got != want {
		t.Errorf("event = %+v, want %+v", got, want)
	}

	// A user that does not exist has no log to write to.
	r = newRig(t)
	r.post(node1, sftpURL, passwordBody("nobody.aaaa1111", "wrong"))
	if len(r.store.events) != 0 {
		t.Errorf("events = %+v", r.store.events)
	}

	// Without an address of the client, the one of the request is used.
	r = newRig(t)
	r.post(node1, sftpURL, sftpBody("password", "ada.aaaa1111", "wrong", ""))
	if len(r.store.events) != 1 || r.store.events[0].IP != "203.0.113.9" {
		t.Errorf("events = %+v", r.store.events)
	}
}

func TestSFTPGuessingIsLimited(t *testing.T) {
	t.Run("five wrong passwords in a minute for a user and a client", func(t *testing.T) {
		r := newRig(t)
		for i := 0; i < 5; i++ {
			if rp := r.post(node1, sftpURL, passwordBody("ada.aaaa1111", "wrong")); rp.Status != 403 {
				t.Fatalf("attempt %d: status = %d", i+1, rp.Status)
			}
		}
		// The right password no longer helps, and the check is not even made.
		rp := r.post(node1, sftpURL, passwordBody("ada.aaaa1111", adaPW))
		if rp.Status != 429 || rp.Header.Get("Retry-After") == "" {
			t.Errorf("sixth attempt: %d %s, Retry-After %q", rp.Status, rp.Raw, rp.Header.Get("Retry-After"))
		}
		// Another client of the same user is not held up by it.
		other := sftpBody("password", "ada.aaaa1111", adaPW, "198.51.100.8:51234")
		if rp := r.post(node1, sftpURL, other); rp.Status != 200 {
			t.Errorf("another client: status = %d: %s", rp.Status, rp.Raw)
		}
		// Nor is another user of the same client.
		if rp := r.post(node1, sftpURL, passwordBody("bob.aaaa1111", adaPW)); rp.Status != 200 {
			t.Errorf("another user: status = %d: %s", rp.Status, rp.Raw)
		}
	})
	t.Run("a user that does not exist is counted too", func(t *testing.T) {
		r := newRig(t)
		for i := 0; i < 5; i++ {
			r.post(node1, sftpURL, passwordBody("nobody.aaaa1111", "wrong"))
		}
		if rp := r.post(node1, sftpURL, passwordBody("nobody.aaaa1111", "wrong")); rp.Status != 429 {
			t.Errorf("status = %d", rp.Status)
		}
	})
	t.Run("names that cannot be one are counted for the client", func(t *testing.T) {
		r := newRig(t)
		for i := 0; i < 5; i++ {
			r.post(node1, sftpURL, passwordBody("a b.aaaa1111", "x"))
		}
		if rp := r.post(node1, sftpURL, passwordBody("a b.aaaa1111", "x")); rp.Status != 429 {
			t.Errorf("status = %d", rp.Status)
		}
	})
	t.Run("a good sign-in does not use up the allowance", func(t *testing.T) {
		r := newRig(t)
		for i := 0; i < 20; i++ {
			if rp := r.post(node1, sftpURL, passwordBody("ada.aaaa1111", adaPW)); rp.Status != 200 {
				t.Fatalf("sign-in %d: status = %d", i+1, rp.Status)
			}
		}
	})
}

func TestSFTPPublicKeys(t *testing.T) {
	key := func(line string) string { return sftpBody("public_key", "ada.aaaa1111", line, "198.51.100.7:51234") }

	t.Run("a registered key", func(t *testing.T) {
		r := newRig(t)
		rp := r.post(node1, sftpURL, key(ed25519Line))
		if rp.Status != 200 || rp.At("server") != normalUUID {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
	t.Run("with the new line the node adds", func(t *testing.T) {
		r := newRig(t)
		if rp := r.post(node1, sftpURL, key(ed25519Line+"\n")); rp.Status != 200 {
			t.Errorf("status = %d", rp.Status)
		}
	})
	t.Run("a valid key that is not registered is refused and not counted", func(t *testing.T) {
		r := newRig(t)
		for i := 0; i < 12; i++ {
			rp := r.post(node1, sftpURL, key(p256Line))
			if rp.Status != 403 || rp.Detail() != "Authorization credentials were not correct, please try again." {
				t.Fatalf("attempt %d: %d %s", i+1, rp.Status, rp.Raw)
			}
		}
		// A client that offers many keys is not locked out of the one that works.
		if rp := r.post(node1, sftpURL, key(ed25519Line)); rp.Status != 200 {
			t.Errorf("the registered key afterwards: status = %d", rp.Status)
		}
		if len(r.store.events) != 0 {
			t.Errorf("events = %+v", r.store.events)
		}
	})
	t.Run("text that is not a key is refused and counted", func(t *testing.T) {
		r := newRig(t)
		for i := 0; i < 5; i++ {
			if rp := r.post(node1, sftpURL, key("not a key")); rp.Status != 403 {
				t.Fatalf("attempt %d: status = %d", i+1, rp.Status)
			}
		}
		if rp := r.post(node1, sftpURL, key(ed25519Line)); rp.Status != 429 {
			t.Errorf("after five: status = %d", rp.Status)
		}
	})
	t.Run("a key of another user", func(t *testing.T) {
		r := newRig(t)
		b := sftpBody("public_key", "bob.aaaa1111", ed25519Line, "198.51.100.7:51234")
		if rp := r.post(node1, sftpURL, b); rp.Status != 403 {
			t.Errorf("status = %d", rp.Status)
		}
	})
}

func TestSFTPRights(t *testing.T) {
	t.Run("a subuser without file.sftp is turned away and it is logged", func(t *testing.T) {
		r := newRig(t)
		rp := r.post(node1, sftpURL, passwordBody("cleo.aaaa1111", adaPW))
		if rp.Status != 403 || rp.Detail() != "You do not have permission to access SFTP for this server." {
			t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
		}
		want := Event{Name: "server:sftp.denied", ServerSubject: 100, Actor: cleo, IP: "198.51.100.7", Properties: "{}"}
		if len(r.store.events) != 1 || r.store.events[0] != want {
			t.Errorf("events = %+v, want %+v", r.store.events, want)
		}
	})
	t.Run("a stranger is turned away", func(t *testing.T) {
		r := newRig(t)
		r.store.users["eve"] = SFTPUser{ID: eve, UUID: userUUID(eve), PasswordHash: adaHash}
		rp := r.post(node1, sftpURL, passwordBody("eve.aaaa1111", adaPW))
		if rp.Status != 403 || rp.Detail() != "You do not have permission to access SFTP for this server." {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
	t.Run("the right comes before the state", func(t *testing.T) {
		r := newRig(t)
		rp := r.post(node1, sftpURL, passwordBody("eve.bbbb2222", adaPW))
		if rp.Status != 403 {
			t.Errorf("status = %d", rp.Status)
		}
	})
}

func TestSFTPStates(t *testing.T) {
	tests := []struct {
		server  string
		message string
	}{
		{"bbbb2222", "This server has not yet completed its installation process, please try again later."},
		{"dddd4444", "This server is currently restoring from a backup, please try again later."},
		{"eeee5555", "This server is currently suspended and the functionality requested is unavailable."},
		{"99997777", "This server is currently being transferred to a new machine, please try again later."},
	}
	for _, tt := range tests {
		t.Run(tt.server, func(t *testing.T) {
			r := newRig(t)
			rp := r.post(node1, sftpURL, passwordBody("ada."+tt.server, adaPW))
			if rp.Status != 409 || rp.Detail() != tt.message || rp.Code() != "ConflictHttpException" {
				t.Errorf("answer = %d %s", rp.Status, rp.Raw)
			}
		})
	}
}

func TestSFTPStoreFailures(t *testing.T) {
	r := newRig(t)
	r.store.failFind = errBoom
	rp := r.post(node1, sftpURL, passwordBody("ada.aaaa1111", adaPW))
	if rp.Status != 500 || rp.Contains("hunter2") {
		t.Errorf("answer = %d %s", rp.Status, rp.Raw)
	}
}

func TestEveryRouteNeedsANode(t *testing.T) {
	r := newRig(t)
	routes := []struct{ method, path string }{
		{"POST", sftpURL}, {"GET", "/api/remote/servers"}, {"POST", "/api/remote/servers/reset"}, {"POST", "/api/remote/activity"},
		{"GET", "/api/remote/servers/" + normalUUID}, {"GET", "/api/remote/servers/" + normalUUID + "/install"},
		{"POST", "/api/remote/servers/" + normalUUID + "/install"},
		{"POST", "/api/remote/servers/" + movingUUID + "/transfer/failure"}, {"POST", "/api/remote/servers/" + movingUUID + "/transfer/success"},
	}
	for _, rt := range routes {
		if rp := r.client.AsNode(0, rt.method, rt.path, ""); rp.Status != 401 {
			t.Errorf("%s %s without a token: status = %d", rt.method, rt.path, rp.Status)
		}
		if rp := r.client.As(ada, rt.method, rt.path, ""); rp.Status != 400 && rp.Status != 403 {
			t.Errorf("%s %s with a user's key: status = %d", rt.method, rt.path, rp.Status)
		}
	}
	if len(r.store.events)+len(r.store.listCalls)+len(r.store.installs)+len(r.store.completes) != 0 {
		t.Error("a store was reached without a node")
	}
}
