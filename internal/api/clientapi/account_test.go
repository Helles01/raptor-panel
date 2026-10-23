package clientapi

import (
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/api"
)

func TestAccount(t *testing.T) {
	r := newRig(t)

	rp := r.as(ada, "GET", "/api/client/account")
	if rp.Status != 200 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	want := map[string]any{"object": "user", "attributes": map[string]any{
		"id": 1.0, "admin": false, "username": "ada", "email": "Ada@Example.com", "first_name": "Ada", "last_name": "Lovelace", "language": "hu",
	}}
	if !reflect.DeepEqual(rp.Body, want) {
		t.Errorf("account =\n%v\nwant\n%v", rp.Body, want)
	}

	if rp := r.as(grace, "GET", "/api/client/account"); rp.at("attributes", "admin") != true || rp.at("attributes", "id") != 2.0 {
		t.Errorf("administrator = %s", rp.Raw)
	}

	// A user sees only their own details, whatever else is in the request.
	if rp := r.as(bob, "GET", "/api/client/account?id=1&user=1"); rp.at("attributes", "username") != "bob" {
		t.Errorf("bob sees %s", rp.Raw)
	}

	if rp := r.as(0, "GET", "/api/client/account"); rp.Status != 401 {
		t.Errorf("without a login: %d", rp.Status)
	}
}

func TestAccountStoreFailures(t *testing.T) {
	r := newRig(t)
	r.w.failAccount = fmt.Errorf("account: %w", ErrNotFound)
	if rp := r.as(ada, "GET", "/api/client/account"); rp.Status != 404 {
		t.Errorf("a user who is gone: %d", rp.Status)
	}

	r = newRig(t)
	r.w.failAccount = errBoom
	rp := r.as(ada, "GET", "/api/client/account")
	if rp.Status != 500 || strings.Contains(string(rp.Raw), "hunter2") {
		t.Errorf("status = %d: %s", rp.Status, rp.Raw)
	}
}

// A panel that requires two-factor authentication sends the user to set it up: the account page, where it is done, stays open.
func TestTwoFactorRequirement(t *testing.T) {
	routes := []string{"/api/client", "/api/client/permissions", "/api/client/account/activity", "/api/client/servers/aaaa1111", "/api/client/servers/aaaa1111/activity"}

	for _, level := range []int{1, 2} {
		r := newRig(t)
		r.w.level = level
		for _, user := range []uint64{ada, grace} {
			wantOpen := level == 1 && user == ada // level 1 is for administrators only
			for _, target := range routes {
				rp := r.as(user, "GET", target)
				if wantOpen && rp.Status != 200 {
					t.Errorf("level %d, user %d, %s: status = %d", level, user, target, rp.Status)
				}
				if !wantOpen && (rp.Status != 400 || rp.code() != "TwoFactorAuthRequiredException") {
					t.Errorf("level %d, user %d, %s: status = %d, code = %q", level, user, target, rp.Status, rp.code())
				}
			}
			if rp := r.as(user, "GET", "/api/client/account"); rp.Status != 200 {
				t.Errorf("level %d, user %d: the account page is closed: %d", level, user, rp.Status)
			}
		}

		// Whoever uses it is let in.
		r.w.totp[ada] = true
		if rp := r.as(ada, "GET", "/api/client"); rp.Status != 200 {
			t.Errorf("level %d: a user with two-factor authentication: %d", level, rp.Status)
		}
	}
}

func TestNobodyIsLetInWithoutALogin(t *testing.T) {
	r := newRig(t)
	for _, target := range []string{
		"/api/client", "/api/client/permissions", "/api/client/account", "/api/client/account/activity",
		"/api/client/servers/aaaa1111", "/api/client/servers/aaaa1111/activity", "/api/client/servers/nothing/activity",
	} {
		if rp := r.as(0, "GET", target); rp.Status != 401 {
			t.Errorf("%s: status = %d", target, rp.Status)
		}
	}
	if len(r.w.lists)+len(r.w.userReqs)+len(r.w.serverReqs) != 0 {
		t.Error("a store was asked for somebody who is not signed in")
	}
}

func TestRoutesAreOnlyForReading(t *testing.T) {
	r := newRig(t)
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		for _, target := range []string{"/api/client", "/api/client/account", "/api/client/servers/aaaa1111"} {
			if rp := r.as(ada, method, target); rp.Status != 404 && rp.Status != 405 {
				t.Errorf("%s %s: status = %d", method, target, rp.Status)
			}
		}
	}
}

func TestNewNeedsEverything(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := newWorld()
	full := Deps{Accounts: accountStore{w}, Servers: serverStore{w}, Activity: activityStore{w}, Logger: quiet}

	if _, err := New(full); err != nil {
		t.Fatalf("a full set: %v", err)
	}
	for name, mutate := range map[string]func(*Deps){
		"no accounts": func(d *Deps) { d.Accounts = nil },
		"no servers":  func(d *Deps) { d.Servers = nil },
		"no activity": func(d *Deps) { d.Activity = nil },
		"no logger":   func(d *Deps) { d.Logger = nil },
	} {
		d := full
		mutate(&d)
		if _, err := New(d); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// The authorizer puts the caller in the request. A route wired without it must be refused, not served with nobody.
func TestHandlersRefuseWithoutTheirPrincipal(t *testing.T) {
	r := newRig(t, func(o *options) {
		o.extra = func(g api.Group, h *Handlers) {
			bare := g.Sub("/bare")
			bare.GET("/list", h.Servers, api.Public())
			bare.GET("/permissions", h.Permissions, api.Public())
			bare.GET("/account", h.Account, api.Public())
			bare.GET("/account-activity", h.AccountActivity, api.Public())
			bare.GET("/server", h.Server, api.Public())
			bare.GET("/server-activity", h.ServerActivity, api.Public())
			// A signed-in caller on a route that has no server.
			noServer := g.Sub("/noserver")
			noServer.GET("/server", h.Server, api.Authenticated())
			noServer.GET("/server-activity", h.ServerActivity, api.Authenticated())
		}
	})

	for _, target := range []string{"list", "permissions", "account", "account-activity", "server", "server-activity"} {
		if rp := r.as(ada, "GET", "/bare/"+target); rp.Status != 403 {
			t.Errorf("/bare/%s: status = %d, want 403", target, rp.Status)
		}
	}
	for _, target := range []string{"server", "server-activity"} {
		if rp := r.as(ada, "GET", "/noserver/"+target); rp.Status != 403 {
			t.Errorf("/noserver/%s: status = %d, want 403", target, rp.Status)
		}
	}
	if len(r.w.lists)+len(r.w.userReqs)+len(r.w.serverReqs) != 0 {
		t.Error("a store was asked")
	}
}

// A user whose UUID is not one cannot be shown (the identifier is made of it): the answer is a server error, not a made-up value.
func TestPeopleWithBadUUIDsAreAnError(t *testing.T) {
	t.Run("an actor", func(t *testing.T) {
		r := newRig(t)
		r.w.seedActivity()
		r.w.userLog[ada][0].Actor.UUID = "not-a-uuid"
		if rp := r.as(ada, "GET", "/api/client/account/activity?include=actor"); rp.Status != 500 {
			t.Errorf("status = %d", rp.Status)
		}
		if rp := r.as(ada, "GET", "/api/client/account/activity"); rp.Status != 200 {
			t.Errorf("without the actors: status = %d", rp.Status)
		}
	})
	t.Run("a subuser", func(t *testing.T) {
		r := newRig(t)
		r.w.subusers[100][0].User.UUID = "not-a-uuid"
		if rp := r.as(ada, "GET", "/api/client/servers/aaaa1111?include=subusers"); rp.Status != 500 {
			t.Errorf("status = %d", rp.Status)
		}
	})
}
