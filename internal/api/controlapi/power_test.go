package controlapi

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/wings"
)

func TestPowerSendsTheSignalAndLogsIt(t *testing.T) {
	for _, signal := range []string{"start", "stop", "restart", "kill"} {
		t.Run(signal, func(t *testing.T) {
			r := newRig(t)

			rp := r.post(ada, "/api/client/servers/aaaa1111/power", fmt.Sprintf(`{"signal":%q}`, signal))
			if rp.Status != 204 || len(rp.Raw) != 0 {
				t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
			}
			want := []daemonCall{{Kind: "power", Node: node1, UUID: normalUUID, Payload: signal}}
			if got := r.daemon.all(); !reflect.DeepEqual(got, want) {
				t.Errorf("calls = %+v, want %+v", got, want)
			}

			entries := r.activity.all()
			if len(entries) != 1 {
				t.Fatalf("%d entries", len(entries))
			}
			keyID := uint64(ada)
			want2 := Entry{Event: "server:power." + signal, ServerID: 100, ActorID: ada, APIKeyID: &keyID, IP: "203.0.113.9", Properties: "{}"}
			if !reflect.DeepEqual(entries[0], want2) {
				t.Errorf("entry = %+v, want %+v", entries[0], want2)
			}
		})
	}
}

func TestPowerNeedsThePermissionOfTheSignal(t *testing.T) {
	tests := []struct {
		user   uint64
		signal string
		status int
	}{
		// Bob may start.
		{bob, "start", 204}, {bob, "stop", 403}, {bob, "restart", 403}, {bob, "kill", 403},
		// Cleo may stop and restart; killing is stopping.
		{cleo, "start", 403}, {cleo, "stop", 204}, {cleo, "restart", 204}, {cleo, "kill", 204},
		{ada, "kill", 204}, {grace, "kill", 204},
		{eve, "start", 404},
		{0, "start", 401},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("user %d %s", tt.user, tt.signal), func(t *testing.T) {
			r := newRig(t)
			rp := r.post(tt.user, "/api/client/servers/aaaa1111/power", fmt.Sprintf(`{"signal":%q}`, tt.signal))
			if rp.Status != tt.status {
				t.Fatalf("status = %d, want %d: %s", rp.Status, tt.status, rp.Raw)
			}
			sent := r.daemon.count("power") == 1
			if sent != (tt.status == 204) || len(r.activity.all()) != btoi(tt.status == 204) {
				t.Errorf("sent %v, %d entries, for status %d", sent, len(r.activity.all()), tt.status)
			}
			if tt.status == 403 && rp.Detail() != "This action is unauthorized." {
				t.Errorf("detail = %q", rp.Detail())
			}
		})
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestPowerValidation(t *testing.T) {
	tests := []struct {
		name   string
		user   uint64
		body   string
		status int
		detail string
	}{
		// Someone who holds everything is told what is wrong; someone who does not is told no.
		{"unknown signal, owner", ada, `{"signal":"reboot"}`, 422, "The selected signal is invalid."},
		{"unknown signal, administrator", grace, `{"signal":"reboot"}`, 422, "The selected signal is invalid."},
		{"unknown signal, subuser", bob, `{"signal":"reboot"}`, 403, "This action is unauthorized."},
		{"upper case", ada, `{"signal":"START"}`, 422, "The selected signal is invalid."},
		{"missing, owner", ada, `{}`, 422, "The signal field is required."},
		{"missing, subuser", bob, `{}`, 403, "This action is unauthorized."},
		{"null", ada, `{"signal":null}`, 422, "The signal field is required."},
		{"blank", ada, `{"signal":"   "}`, 422, "The signal field is required."},
		{"a number", ada, `{"signal":5}`, 422, "The signal must be a string."},
		{"a list", ada, `{"signal":["start"]}`, 422, "The signal must be a string."},
		{"padded", ada, `{"signal":" start "}`, 204, ""},
		{"extra fields are ignored", ada, `{"signal":"start","force":true}`, 204, ""},
		{"not JSON", ada, `signal=start`, 400, ""},
		{"empty body", ada, ``, 400, ""},
		{"a string, not an object", ada, `"start"`, 400, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.post(tt.user, "/api/client/servers/aaaa1111/power", tt.body)
			if rp.Status != tt.status {
				t.Fatalf("status = %d, want %d: %s", rp.Status, tt.status, rp.Raw)
			}
			if tt.detail != "" && rp.Detail() != tt.detail {
				t.Errorf("detail = %q, want %q", rp.Detail(), tt.detail)
			}
			if tt.status != 204 && (r.daemon.count("power") != 0 || len(r.activity.all()) != 0) {
				t.Error("the node was called or an entry written for a refused request")
			}
		})
	}
}

func TestPowerOfAServerThatCannotBeUsed(t *testing.T) {
	r := newRig(t)
	for _, target := range []string{"bbbb2222", "cccc3333"} {
		rp := r.post(ada, "/api/client/servers/"+target+"/power", `{"signal":"start"}`)
		if rp.Status != 409 {
			t.Errorf("%s: status = %d, want 409: %s", target, rp.Status, rp.Raw)
		}
	}
	if r.targets.calls != 0 || r.daemon.count("power") != 0 {
		t.Error("the node was looked up or called")
	}
}

func TestPowerFailures(t *testing.T) {
	t.Run("the node refuses", func(t *testing.T) {
		r := newRig(t)
		r.daemon.err = &wings.Error{Status: 409, NodeStatus: 409, Message: "An error occurred on the remote host: server is already running. (request id: r1)"}
		rp := r.post(ada, "/api/client/servers/aaaa1111/power", `{"signal":"start"}`)
		if rp.Status != 409 || rp.Code() != "DaemonConnectionException" || rp.Detail() != "An error occurred on the remote host: server is already running. (request id: r1)" {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
		if len(r.activity.all()) != 0 {
			t.Error("an entry was written for a signal the node refused")
		}
	})
	t.Run("the node cannot be reached", func(t *testing.T) {
		r := newRig(t)
		r.daemon.err = fmt.Errorf("call: %w", &wings.Error{Status: 504, Unreachable: true, Message: "Could not establish a connection to the machine running this server. Please try again."})
		rp := r.post(ada, "/api/client/servers/aaaa1111/power", `{"signal":"start"}`)
		if rp.Status != 504 || rp.Code() != "DaemonConnectionException" {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
	t.Run("a failure that is not the node's does not leak", func(t *testing.T) {
		r := newRig(t)
		r.daemon.err = errBoom
		rp := r.post(ada, "/api/client/servers/aaaa1111/power", `{"signal":"start"}`)
		if rp.Status != 500 || rp.Contains("hunter2") {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
	t.Run("the node of the server is gone", func(t *testing.T) {
		r := newRig(t)
		delete(r.targets.targets, 100)
		if rp := r.post(ada, "/api/client/servers/aaaa1111/power", `{"signal":"start"}`); rp.Status != 404 {
			t.Errorf("status = %d", rp.Status)
		}
	})
	t.Run("the store is broken", func(t *testing.T) {
		r := newRig(t)
		r.targets.err = errBoom
		rp := r.post(ada, "/api/client/servers/aaaa1111/power", `{"signal":"start"}`)
		if rp.Status != 500 || rp.Contains("hunter2") {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
	t.Run("an entry that cannot be stored does not undo the signal", func(t *testing.T) {
		r := newRig(t)
		r.activity.err = errBoom
		rp := r.post(ada, "/api/client/servers/aaaa1111/power", `{"signal":"start"}`)
		if rp.Status != 204 || r.daemon.count("power") != 1 {
			t.Errorf("status = %d", rp.Status)
		}
		if !strings.Contains(r.logs.String(), "activity entry not stored") {
			t.Errorf("the failure was not logged: %s", r.logs.String())
		}
	})
}

func TestCommandSendsItAndLogsIt(t *testing.T) {
	r := newRig(t)

	rp := r.post(ada, "/api/client/servers/aaaa1111/command", `{"command":"  say hello  "}`)
	if rp.Status != 204 || len(rp.Raw) != 0 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	want := []daemonCall{{Kind: "command", Node: node1, UUID: normalUUID, Payload: "say hello"}}
	if got := r.daemon.all(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %+v, want %+v", got, want)
	}
	entries := r.activity.all()
	if len(entries) != 1 || entries[0].Event != "server:console.command" || entries[0].Properties != `{"command":"say hello"}` ||
		entries[0].ServerID != 100 || entries[0].ActorID != ada || entries[0].IP != "203.0.113.9" {
		t.Errorf("entries = %+v", entries)
	}
}

func TestCommandNeedsTheConsole(t *testing.T) {
	tests := []struct {
		user   uint64
		status int
	}{{ada, 204}, {grace, 204}, {cleo, 204}, {bob, 403}, {eve, 404}, {0, 401}}
	for _, tt := range tests {
		r := newRig(t)
		rp := r.post(tt.user, "/api/client/servers/aaaa1111/command", `{"command":"list"}`)
		if rp.Status != tt.status {
			t.Errorf("user %d: status = %d, want %d: %s", tt.user, rp.Status, tt.status, rp.Raw)
		}
		if tt.status != 204 && (r.daemon.count("command") != 0 || len(r.activity.all()) != 0) {
			t.Errorf("user %d: the node was called or an entry written", tt.user)
		}
	}
}

func TestCommandValidation(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		status int
		detail string
	}{
		{"missing", `{}`, 422, "The command field is required."},
		{"null", `{"command":null}`, 422, "The command field is required."},
		{"empty", `{"command":""}`, 422, "The command field is required."},
		{"blank", `{"command":" \t "}`, 422, "The command field is required."},
		{"a number", `{"command":5}`, 422, "The command must be a string."},
		{"an object", `{"command":{"a":1}}`, 422, "The command must be a string."},
		{"the longest", `{"command":"` + strings.Repeat("a", 4096) + `"}`, 204, ""},
		{"one byte too long", `{"command":"` + strings.Repeat("a", 4097) + `"}`, 422, "The command must be between 1 and 4096 bytes."},
		// 1366 euro signs are 4098 bytes: the limit is the size of what is sent to the node.
		{"too long in bytes", `{"command":"` + strings.Repeat("€", 1366) + `"}`, 422, "The command must be between 1 and 4096 bytes."},
		{"a NUL character", `{"command":"say a\u0000b"}`, 422, "The command format is invalid."},
		{"a new line is left to the console", `{"command":"say a\nsay b"}`, 204, ""},
		{"not JSON", `command=list`, 400, ""},
		{"empty body", ``, 400, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.post(ada, "/api/client/servers/aaaa1111/command", tt.body)
			if rp.Status != tt.status {
				t.Fatalf("status = %d, want %d: %s", rp.Status, tt.status, rp.Raw)
			}
			if tt.detail != "" && rp.Detail() != tt.detail {
				t.Errorf("detail = %q, want %q", rp.Detail(), tt.detail)
			}
			if tt.status != 204 && r.daemon.count("command") != 0 {
				t.Error("the node was called for a refused command")
			}
		})
	}

	t.Run("a body too large to read", func(t *testing.T) {
		r := newRig(t)
		rp := r.post(ada, "/api/client/servers/aaaa1111/command", `{"command":"`+strings.Repeat("a", 70<<10)+`"}`)
		if rp.Status != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d", rp.Status)
		}
	})
}

func TestCommandFailures(t *testing.T) {
	t.Run("a node that has no running server answers 502", func(t *testing.T) {
		r := newRig(t)
		r.daemon.err = &wings.Error{Status: 502, NodeStatus: 502, Message: "There was an error while communicating with the machine running this server. (code: 502)"}
		rp := r.post(ada, "/api/client/servers/aaaa1111/command", `{"command":"list"}`)
		if rp.Status != 502 || rp.Code() != "HttpException" || rp.Detail() != "Server must be online in order to send commands." {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
		if len(r.activity.all()) != 0 {
			t.Error("an entry was written for a command that was not sent")
		}
	})
	t.Run("a 502 the panel made up is not the same", func(t *testing.T) {
		r := newRig(t)
		r.daemon.err = &wings.Error{Status: 502, NodeStatus: 200, Message: "The machine running this server sent an unusable reply."}
		rp := r.post(ada, "/api/client/servers/aaaa1111/command", `{"command":"list"}`)
		if rp.Status != 502 || rp.Code() != "DaemonConnectionException" || rp.Detail() != "The machine running this server sent an unusable reply." {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
	t.Run("an unreachable node", func(t *testing.T) {
		r := newRig(t)
		r.daemon.err = &wings.Error{Status: 504, Unreachable: true, Message: "Could not establish a connection to the machine running this server. Please try again."}
		if rp := r.post(ada, "/api/client/servers/aaaa1111/command", `{"command":"list"}`); rp.Status != 504 || rp.Code() != "DaemonConnectionException" {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
	t.Run("another refusal of the node", func(t *testing.T) {
		r := newRig(t)
		r.daemon.err = &wings.Error{Status: 400, NodeStatus: 400, Message: "An error occurred on the remote host: bad. (request id: <nil>)"}
		if rp := r.post(ada, "/api/client/servers/aaaa1111/command", `{"command":"list"}`); rp.Status != 400 || rp.Code() != "DaemonConnectionException" {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
	t.Run("a failure that is not the node's does not leak", func(t *testing.T) {
		r := newRig(t)
		r.daemon.err = errBoom
		rp := r.post(ada, "/api/client/servers/aaaa1111/command", `{"command":"list"}`)
		if rp.Status != 500 || rp.Contains("hunter2") {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
	t.Run("a server that is not ready", func(t *testing.T) {
		r := newRig(t)
		if rp := r.post(ada, "/api/client/servers/bbbb2222/command", `{"command":"list"}`); rp.Status != 409 {
			t.Errorf("status = %d", rp.Status)
		}
	})
}

func TestTheRoutesTakeOnlyPost(t *testing.T) {
	r := newRig(t)
	for _, path := range []string{"power", "command"} {
		for _, method := range []string{"GET", "PUT", "DELETE"} {
			if rp := r.client.As(ada, method, "/api/client/servers/aaaa1111/"+path, ""); rp.Status != 404 && rp.Status != 405 {
				t.Errorf("%s %s: status = %d", method, path, rp.Status)
			}
		}
	}
	if len(r.daemon.all()) != 0 {
		t.Error("the node was called")
	}
}
