package remoteapi

import (
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/api/apitest"
)

func TestNewNeedsEverything(t *testing.T) {
	r := newRig(t)
	full := r.handlers.d

	if _, err := New(full); err != nil {
		t.Fatalf("a full set: %v", err)
	}
	for name, mutate := range map[string]func(*Deps){
		"no servers":  func(d *Deps) { d.Servers = nil },
		"no users":    func(d *Deps) { d.Users = nil },
		"no activity": func(d *Deps) { d.Activity = nil },
		"no events":   func(d *Deps) { d.Events = nil },
		"no outbox":   func(d *Deps) { d.Outbox = nil },
		"no old node": func(d *Deps) { d.OldNodes = nil },
		"no tracker":  func(d *Deps) { d.Failures = nil },
		"no logger":   func(d *Deps) { d.Logger = nil },
	} {
		d := full
		mutate(&d)
		if _, err := New(d); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestClientHost(t *testing.T) {
	const fallback = "203.0.113.9"
	tests := []struct{ in, want string }{
		{"198.51.100.7:51234", "198.51.100.7"},
		{"[2001:db8::1]:22", "2001:db8::1"},
		{"198.51.100.7", "198.51.100.7"},
		{"2001:db8::1", "2001:db8::1"},
		{"", fallback},
		{"garbage", fallback},
		{"999.1.1.1:22", fallback},
		{"host.example.com:22", fallback},
	}
	for _, tt := range tests {
		if got := clientHost(tt.in, fallback); got != tt.want {
			t.Errorf("clientHost(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestAnEventThatCannotBeStoredDoesNotChangeTheAnswer(t *testing.T) {
	r := newRig(t)
	r.store.failEvents = errBoom
	rp := r.post(node1, sftpURL, passwordBody("ada.aaaa1111", "wrong"))
	if rp.Status != 403 || rp.Detail() != "Authorization credentials were not correct, please try again." {
		t.Errorf("answer = %d %s", rp.Status, rp.Raw)
	}
	if !strings.Contains(r.logs.String(), "activity entry not stored") || strings.Contains(r.logs.String(), "wrong") {
		t.Errorf("logs = %s", r.logs.String())
	}
}

func TestStoreFailuresAreInternalErrors(t *testing.T) {
	for name, call := range map[string]func(*rig) apitest.Reply{
		"transfer": func(r *rig) apitest.Reply { return r.post(node2, successURL(movingUUID), "") },
		"keys": func(r *rig) apitest.Reply {
			r.store.prints = nil
			r.store.failFind = nil
			return r.post(node1, sftpURL, sftpBody("public_key", "ada.aaaa1111", ed25519Line, ""))
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			if name == "transfer" {
				r.store.failFind = errBoom
			}
			rp := call(r)
			if name == "transfer" && (rp.Status != 500 || rp.Contains("hunter2")) {
				t.Errorf("answer = %d %s", rp.Status, rp.Raw)
			}
			if name == "keys" && rp.Status != 403 {
				t.Errorf("no keys registered: status = %d", rp.Status)
			}
		})
	}
}
