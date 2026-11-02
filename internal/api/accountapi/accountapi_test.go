package accountapi

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apitest"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/auth/session"
	"github.com/Helles01/raptor-panel/internal/auth/totp"
)

func emailBodyOf(email, password string) string {
	return js(`{"email":%q,"password":%q}`, email, password)
}

func TestUpdateEmail(t *testing.T) {
	r := newRig(t)
	rp := r.do(ada, "PUT", "/email", emailBodyOf("New@Example.com", adaPW))
	if rp.Status != 204 || len(rp.Raw) != 0 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if !reflect.DeepEqual(r.store.emails, [][2]string{{adaEmail, "New@Example.com"}}) {
		t.Errorf("stored %v", r.store.emails)
	}
	keyID := uint64(ada)
	want := Entry{Event: "user:account.email-changed", UserID: ada, APIKeyID: &keyID, IP: "203.0.113.9", Properties: `{"new":"New@Example.com","old":"ada@example.com"}`}
	if len(r.activity.entries) != 1 || !reflect.DeepEqual(r.activity.entries[0], want) {
		t.Errorf("entries = %+v, want %+v", r.activity.entries, want)
	}
}

func TestTheSameAddressIsNoChange(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 6; i++ {
		if rp := r.do(ada, "PUT", "/email", emailBodyOf("ADA@example.com", adaPW)); rp.Status != 204 {
			t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
		}
	}
	if len(r.store.emails) != 0 || len(r.activity.entries) != 0 {
		t.Errorf("stored %v, logged %v: nothing changed", r.store.emails, r.activity.entries)
	}
	// And it did not count against the three changes a day.
	if rp := r.do(ada, "PUT", "/email", emailBodyOf("one@example.com", adaPW)); rp.Status != 204 {
		t.Errorf("status = %d", rp.Status)
	}
}

func TestAddressChangesAreLimited(t *testing.T) {
	r := newRig(t)
	for i, email := range []string{"one@example.com", "two@example.com", "three@example.com"} {
		if rp := r.do(ada, "PUT", "/email", emailBodyOf(email, adaPW)); rp.Status != 204 {
			t.Fatalf("change %d: status = %d: %s", i+1, rp.Status, rp.Raw)
		}
	}
	rp := r.do(ada, "PUT", "/email", emailBodyOf("four@example.com", adaPW))
	if rp.Status != 429 || rp.Header.Get("Retry-After") == "" {
		t.Errorf("the fourth: %d %s, Retry-After %q", rp.Status, rp.Raw, rp.Header.Get("Retry-After"))
	}
	// Also a request that would change nothing is refused: the limit is looked at first.
	if rp := r.do(ada, "PUT", "/email", emailBodyOf("THREE@example.com", adaPW)); rp.Status != 429 {
		t.Errorf("the same address: status = %d", rp.Status)
	}
	if len(r.store.emails) != 3 {
		t.Errorf("%d changes stored", len(r.store.emails))
	}
	// It is the user's own allowance.
	if rp := r.do(bob, "PUT", "/email", emailBodyOf("bobby@example.com", adaPW)); rp.Status != 204 {
		t.Errorf("another user: status = %d", rp.Status)
	}
}

func TestUpdateEmailRefusals(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		status int
		detail string
	}{
		{"a wrong password", emailBodyOf("x@example.com", "wrong"), 400, "The password provided was invalid for this account."},
		{"no password", `{"email":"x@example.com"}`, 400, "The password provided was invalid for this account."},
		{"no email", js(`{"password":%q}`, adaPW), 422, "The email field is required."},
		{"an email that is a number", js(`{"email":5,"password":%q}`, adaPW), 422, "The email must be a string."},
		{"an address that is not one", emailBodyOf("not an address", adaPW), 422, ""},
		{"an address another account has", emailBodyOf("Taken@example.com", adaPW), 422, "The email has already been taken."},
		{"not JSON", `email=x`, 400, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(ada, "PUT", "/email", tt.body)
			if rp.Status != tt.status || (tt.detail != "" && rp.Detail() != tt.detail) {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if rp.Status == 422 && rp.At("errors", 0, "meta", "source_field") != "email" {
				t.Errorf("field = %v", rp.At("errors", 0, "meta"))
			}
			if len(r.store.emails) != 0 || len(r.activity.entries) != 0 {
				t.Error("an address was stored or logged")
			}
		})
	}
}

func TestWrongPasswordsAreCounted(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 5; i++ {
		if rp := r.do(ada, "PUT", "/email", emailBodyOf("x@example.com", "wrong")); rp.Status != 400 {
			t.Fatalf("attempt %d: status = %d", i+1, rp.Status)
		}
	}
	// The right password no longer helps, and the same count covers every request that asks for it.
	for _, rt := range []struct{ method, path, body string }{
		{"PUT", "/email", emailBodyOf("x@example.com", adaPW)},
		{"PUT", "/password", js(`{"current_password":%q,"password":%q,"password_confirmation":%q}`, adaPW, newPW, newPW)},
		{"POST", "/two-factor/disable", js(`{"password":%q}`, adaPW)},
	} {
		if rp := r.do(ada, rt.method, rt.path, rt.body); rp.Status != 429 || rp.Header.Get("Retry-After") == "" {
			t.Errorf("%s %s: status = %d, Retry-After %q", rt.method, rt.path, rp.Status, rp.Header.Get("Retry-After"))
		}
	}
	// Another user is not held up.
	if rp := r.do(bob, "PUT", "/email", emailBodyOf("bobby@example.com", adaPW)); rp.Status != 204 {
		t.Errorf("another user: status = %d", rp.Status)
	}
}

func passwordRequest(current, password, confirmation string) string {
	return js(`{"current_password":%q,"password":%q,"password_confirmation":%q}`, current, password, confirmation)
}

func TestUpdatePassword(t *testing.T) {
	r := newRig(t)
	rp := r.do(ada, "PUT", "/password", passwordRequest(adaPW, newPW, newPW))
	if rp.Status != 204 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if len(r.store.passwords) != 1 || !auth.VerifyPassword(r.store.passwords[0], newPW) || auth.VerifyPassword(r.store.passwords[0], adaPW) {
		t.Errorf("the hash stored is not the new password's")
	}
	if !reflect.DeepEqual(r.nodes.users, []uint64{ada}) {
		t.Errorf("the nodes were told about %v", r.nodes.users)
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "user:account.password-changed" || r.activity.entries[0].Properties != "{}" {
		t.Errorf("entries = %+v", r.activity.entries)
	}
}

func TestOtherSessionsEndButTheOneInUseStays(t *testing.T) {
	r := newRig(t)
	here, elsewhere, another, bobs := r.login(ada), r.login(ada), r.login(ada), r.login(bob)

	rp := r.world.Sessions // the manager of the server
	_ = rp
	reply := r.client.AsSession(r.sessions.Cookie(here), "PUT", base+"/password", passwordRequest(adaPW, newPW, newPW))
	if reply.Status != 204 {
		t.Fatalf("status = %d: %s", reply.Status, reply.Raw)
	}
	ctx := context.Background()
	if _, err := r.sessions.Load(ctx, here.ID); err != nil {
		t.Errorf("the session that made the request was ended: %v", err)
	}
	for name, s := range map[string]*session.Session{"elsewhere": elsewhere, "another": another} {
		if _, err := r.sessions.Load(ctx, s.ID); err == nil {
			t.Errorf("the %s session of the user still works", name)
		}
	}
	if _, err := r.sessions.Load(ctx, bobs.ID); err != nil {
		t.Errorf("another user's session was ended: %v", err)
	}
}

func TestAPasswordChangeByKeyEndsEverySession(t *testing.T) {
	r := newRig(t)
	s := r.login(ada)
	if rp := r.do(ada, "PUT", "/password", passwordRequest(adaPW, newPW, newPW)); rp.Status != 204 {
		t.Fatalf("status = %d", rp.Status)
	}
	if _, err := r.sessions.Load(context.Background(), s.ID); err == nil {
		t.Error("a session of the user still works although the change came with a key")
	}
}

func TestUpdatePasswordRefusals(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		status  int
		details []string
	}{
		{"a wrong current password", passwordRequest("wrong", newPW, newPW), 400, []string{"The password provided was invalid for this account."}},
		{"a wrong current password and no new one", js(`{"current_password":"wrong"}`), 400, []string{"The password provided was invalid for this account."}},
		{"no current password", js(`{"password":%q,"password_confirmation":%q}`, newPW, newPW), 400, []string{"The password provided was invalid for this account."}},
		{"no new password", js(`{"current_password":%q}`, adaPW), 422, []string{"The password field is required."}},
		{"a confirmation that differs", passwordRequest(adaPW, newPW, "something else entirely"), 422, []string{"The password confirmation does not match."}},
		{"too short", passwordRequest(adaPW, "short", "short"), 422, nil},
		{"too long for the hash", passwordRequest(adaPW, strings.Repeat("a", 73), strings.Repeat("a", 73)), 422, nil},
		{"not JSON", `password`, 400, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(ada, "PUT", "/password", tt.body)
			if rp.Status != tt.status {
				t.Fatalf("status = %d, want %d: %s", rp.Status, tt.status, rp.Raw)
			}
			for i, d := range tt.details {
				if rp.At("errors", i, "detail") != d {
					t.Errorf("error %d = %v, want %q", i, rp.At("errors", i, "detail"), d)
				}
			}
			if rp.Status == 422 && rp.At("errors", 0, "meta", "source_field") != "password" {
				t.Errorf("field = %v", rp.At("errors", 0, "meta"))
			}
			if len(r.store.passwords) != 0 || len(r.nodes.users) != 0 || len(r.activity.entries) != 0 {
				t.Error("the password was changed")
			}
		})
	}
}

func TestPasswordEffectsThatFailAreOnlyLogged(t *testing.T) {
	r := newRig(t)
	r.nodes.err = errBoom
	if rp := r.do(ada, "PUT", "/password", passwordRequest(adaPW, newPW, newPW)); rp.Status != 204 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if !strings.Contains(r.logs.String(), "the nodes were not told to drop the tokens") || len(r.store.passwords) != 1 {
		t.Errorf("logs = %s", r.logs.String())
	}
}

func TestTwoFactorSetup(t *testing.T) {
	r := newRig(t)
	rp := r.do(ada, "GET", "/two-factor", "")
	if rp.Status != 200 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	secret, _ := rp.At("data", "secret").(string)
	uri, _ := rp.At("data", "image_url_data").(string)
	if len(secret) != 32 || !strings.HasPrefix(uri, "otpauth://totp/") || !strings.Contains(uri, "secret="+secret) || !strings.Contains(uri, "Raptor") {
		t.Errorf("secret %q, uri %q", secret, uri)
	}
	if !reflect.DeepEqual(r.store.secrets, []string{secret}) {
		t.Errorf("stored %v: the secret is kept as soon as it is made", r.store.secrets)
	}
	if rp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rp.Header.Get("Cache-Control"))
	}

	// Asking again replaces a secret nobody confirmed.
	r.do(ada, "GET", "/two-factor", "")
	if len(r.store.secrets) != 2 || r.store.secrets[0] == r.store.secrets[1] {
		t.Errorf("secrets = %v", r.store.secrets)
	}

	// Not while it is on.
	r.store.accounts[ada].UsesTOTP = true
	rp = r.do(ada, "GET", "/two-factor", "")
	if rp.Status != 400 || rp.Detail() != "Two-factor authentication is already enabled on this account." || len(r.store.secrets) != 2 {
		t.Errorf("answer = %d %s", rp.Status, rp.Raw)
	}
}

func enableBodyOf(code, password string) string {
	return js(`{"code":%q,"password":%q}`, code, password)
}

func (r *rig) setUp() (secret string) {
	r.t.Helper()
	rp := r.do(ada, "GET", "/two-factor", "")
	secret, _ = rp.At("data", "secret").(string)
	return secret
}

func codeFor(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	code, err := totp.Code(secret, totp.Step(at))
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func TestTwoFactorEnable(t *testing.T) {
	r := newRig(t)
	secret := r.setUp()

	rp := r.do(ada, "POST", "/two-factor", enableBodyOf(codeFor(t, secret, apitest.Now), adaPW))
	if rp.Status != 200 || rp.At("object") != "recovery_tokens" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	tokens, _ := rp.At("attributes", "tokens").([]any)
	if len(tokens) != 10 {
		t.Fatalf("%d tokens", len(tokens))
	}
	if len(r.store.enabled) != 1 || len(r.store.enabled[0]) != 10 {
		t.Fatalf("stored %v", r.store.enabled)
	}
	for i, tk := range tokens {
		plain, _ := tk.(string)
		if len(plain) != 10 || !auth.VerifyPassword(r.store.enabled[0][i], plain) || r.store.enabled[0][i] == plain {
			t.Errorf("token %d: %q is not stored as its hash", i, plain)
		}
	}
	// The code that did it is marked as used, so that it cannot sign anyone in afterwards.
	step := time.Unix(totp.Step(apitest.Now)*totp.Period, 0).UTC()
	if len(r.store.enabledAt) != 1 || !r.store.enabledAt[0].Equal(step) {
		t.Errorf("used at %v, want %v", r.store.enabledAt, step)
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "user:two-factor.create" {
		t.Errorf("entries = %+v", r.activity.entries)
	}
	if rp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rp.Header.Get("Cache-Control"))
	}
}

func TestTwoFactorEnableRefusals(t *testing.T) {
	good := func(r *rig, secret string) string { return codeFor(t, secret, apitest.Now) }
	tests := []struct {
		name    string
		body    func(r *rig, secret string) string
		status  int
		detail  string
		noSetup bool
	}{
		{"a wrong code", func(*rig, string) string { return enableBodyOf("000000", adaPW) }, 400, "The provided two-factor authentication token was not valid.", false},
		{"a wrong password", func(r *rig, s string) string { return enableBodyOf(good(r, s), "wrong") }, 400, "The password provided was not valid.", false},
		{"a code of the wrong size", func(*rig, string) string { return enableBodyOf("12345", adaPW) }, 422, "The code must be 6 characters.", false},
		{"no code", func(*rig, string) string { return js(`{"password":%q}`, adaPW) }, 422, "The code field is required.", false},
		{"no password", func(r *rig, s string) string { return js(`{"code":%q}`, good(r, s)) }, 422, "The password field is required.", false},
		{"a code that is a number", func(*rig, string) string { return js(`{"code":123456,"password":%q}`, adaPW) }, 422, "The code must be a string.", false},
		{"before a secret was asked for", func(*rig, string) string { return enableBodyOf("123456", adaPW) }, 400, "Two-factor authentication has not been set up: ask for a secret first.", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			secret := ""
			if !tt.noSetup {
				secret = r.setUp()
			}
			rp := r.do(ada, "POST", "/two-factor", tt.body(r, secret))
			if rp.Status != tt.status || rp.Detail() != tt.detail {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if len(r.store.enabled) != 0 || len(r.activity.entries) != 0 {
				t.Error("two-factor authentication was switched on")
			}
		})
	}
}

func TestWrongCodesAreCounted(t *testing.T) {
	r := newRig(t)
	secret := r.setUp()
	for i := 0; i < 5; i++ {
		if rp := r.do(ada, "POST", "/two-factor", enableBodyOf("000000", adaPW)); rp.Status != 400 {
			t.Fatalf("attempt %d: status = %d", i+1, rp.Status)
		}
	}
	if rp := r.do(ada, "POST", "/two-factor", enableBodyOf(codeFor(t, secret, apitest.Now), adaPW)); rp.Status != 429 {
		t.Errorf("the right code after five wrong ones: status = %d", rp.Status)
	}
}

func TestTwoFactorDisable(t *testing.T) {
	r := newRig(t)
	r.store.accounts[ada].UsesTOTP = true
	rp := r.do(ada, "POST", "/two-factor/disable", js(`{"password":%q}`, adaPW))
	if rp.Status != 204 || r.store.disabled != 1 {
		t.Fatalf("answer = %d %s, disabled %d", rp.Status, rp.Raw, r.store.disabled)
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "user:two-factor.delete" {
		t.Errorf("entries = %+v", r.activity.entries)
	}

	for _, body := range []string{js(`{"password":"wrong"}`), `{}`} {
		r = newRig(t)
		rp = r.do(ada, "POST", "/two-factor/disable", body)
		if rp.Status != 400 || rp.Detail() != "The password provided was not valid." || r.store.disabled != 0 {
			t.Errorf("%s: answer = %d %s", body, rp.Status, rp.Raw)
		}
	}
}

func TestTwoFactorRoutesAreOpenWhereThePanelRequiresIt(t *testing.T) {
	r := newRig(t)
	r.world.TwoFactorLevel = 2 // everybody must use it, and ada does not

	if rp := r.do(ada, "GET", "/two-factor", ""); rp.Status != 200 {
		t.Errorf("setup: status = %d: %s", rp.Status, rp.Raw)
	}
	secret := r.store.accounts[ada].TOTPSecret
	if rp := r.do(ada, "POST", "/two-factor", enableBodyOf(codeFor(t, secret, apitest.Now), adaPW)); rp.Status != 200 {
		t.Errorf("enable: status = %d: %s", rp.Status, rp.Raw)
	}
	// The rest of the account is closed until it is on.
	for _, rt := range []struct{ method, path, body string }{
		{"PUT", "/email", emailBodyOf("x@example.com", adaPW)},
		{"PUT", "/password", passwordRequest(adaPW, newPW, newPW)},
		{"GET", "/api-keys", ""}, {"GET", "/ssh-keys", ""},
	} {
		rp := r.do(ada, rt.method, rt.path, rt.body)
		if rp.Status != 400 || rp.Code() != "TwoFactorAuthRequiredException" {
			t.Errorf("%s %s: status = %d code %q", rt.method, rt.path, rp.Status, rp.Code())
		}
	}
}

func TestEveryRouteNeedsALogin(t *testing.T) {
	r := newRig(t)
	for _, rt := range []struct{ method, path string }{
		{"PUT", "/email"}, {"PUT", "/password"}, {"GET", "/two-factor"}, {"POST", "/two-factor"}, {"POST", "/two-factor/disable"},
		{"GET", "/api-keys"}, {"POST", "/api-keys"}, {"DELETE", "/api-keys/ptlc_abcdefghijk"}, {"GET", "/ssh-keys"}, {"POST", "/ssh-keys"}, {"POST", "/ssh-keys/remove"},
	} {
		if rp := r.do(0, rt.method, rt.path, `{}`); rp.Status != 401 {
			t.Errorf("%s %s: status = %d", rt.method, rt.path, rp.Status)
		}
	}
	if len(r.store.emails)+len(r.store.passwords)+len(r.store.secrets)+len(r.activity.entries) != 0 {
		t.Error("something was changed")
	}
}

func TestStoreFailuresDoNotLeak(t *testing.T) {
	for name, call := range map[string]func(*rig) apitest.Reply{
		"email":    func(r *rig) apitest.Reply { return r.do(ada, "PUT", "/email", emailBodyOf("x@example.com", adaPW)) },
		"password": func(r *rig) apitest.Reply { return r.do(ada, "PUT", "/password", passwordRequest(adaPW, newPW, newPW)) },
		"setup":    func(r *rig) apitest.Reply { return r.do(ada, "GET", "/two-factor", "") },
		"disable": func(r *rig) apitest.Reply {
			return r.do(ada, "POST", "/two-factor/disable", js(`{"password":%q}`, adaPW))
		},
		"list keys": func(r *rig) apitest.Reply { return r.do(ada, "GET", "/api-keys", "") },
		"new key":   func(r *rig) apitest.Reply { return r.do(ada, "POST", "/api-keys", `{"description":"x"}`) },
		"ssh keys":  func(r *rig) apitest.Reply { return r.do(ada, "GET", "/ssh-keys", "") },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.store.fail = errBoom
			rp := call(r)
			if rp.Status != 500 || rp.Contains("hunter2") {
				t.Errorf("answer = %d %s", rp.Status, rp.Raw)
			}
		})
	}
}

func TestAnEntryThatCannotBeStoredDoesNotUndoTheChange(t *testing.T) {
	r := newRig(t)
	r.activity.err = errBoom
	if rp := r.do(ada, "PUT", "/email", emailBodyOf("x@example.com", adaPW)); rp.Status != 204 || len(r.store.emails) != 1 {
		t.Errorf("status = %d", rp.Status)
	}
	if !strings.Contains(r.logs.String(), "activity entry not stored") {
		t.Errorf("logs = %s", r.logs.String())
	}
}

func TestHandlersRefuseWithoutTheirPrincipal(t *testing.T) {
	r := newRig(t)
	client := apitest.NewWorld().Serve(t, func(g api.Group) {
		b := g.Sub("/bare")
		b.PUT("/email", r.handlers.UpdateEmail, api.Public())
		b.PUT("/password", r.handlers.UpdatePassword, api.Public())
		b.GET("/two-factor", r.handlers.TwoFactorSetup, api.Public())
		b.POST("/two-factor", r.handlers.TwoFactorEnable, api.Public())
		b.POST("/two-factor/disable", r.handlers.TwoFactorDisable, api.Public())
		b.GET("/api-keys", r.handlers.ListKeys, api.Public())
		b.POST("/api-keys", r.handlers.CreateKey, api.Public())
		b.DELETE("/api-keys/:identifier", r.handlers.DeleteKey, api.Public())
		b.GET("/ssh-keys", r.handlers.ListSSHKeys, api.Public())
		b.POST("/ssh-keys", r.handlers.AddSSHKey, api.Public())
		b.POST("/ssh-keys/remove", r.handlers.RemoveSSHKey, api.Public())
	})
	for _, rt := range []struct{ method, path string }{
		{"PUT", "/bare/email"}, {"PUT", "/bare/password"}, {"GET", "/bare/two-factor"}, {"POST", "/bare/two-factor"}, {"POST", "/bare/two-factor/disable"},
		{"GET", "/bare/api-keys"}, {"POST", "/bare/api-keys"}, {"DELETE", "/bare/api-keys/ptlc_abc"}, {"GET", "/bare/ssh-keys"}, {"POST", "/bare/ssh-keys"}, {"POST", "/bare/ssh-keys/remove"},
	} {
		if rp := client.As(0, rt.method, rt.path, `{}`); rp.Status != 403 {
			t.Errorf("%s %s: status = %d, want 403", rt.method, rt.path, rp.Status)
		}
	}
	if len(r.store.emails)+len(r.store.passwords)+len(r.store.secrets) != 0 {
		t.Error("something was changed")
	}
}

func TestNewNeedsEverything(t *testing.T) {
	r := newRig(t)
	full := r.handlers.d
	if _, err := New(full); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Deps){
		"no store": func(d *Deps) { d.Store = nil }, "no sessions": func(d *Deps) { d.Sessions = nil }, "no nodes": func(d *Deps) { d.Nodes = nil },
		"no activity": func(d *Deps) { d.Activity = nil }, "no tracker": func(d *Deps) { d.Failures = nil }, "no logger": func(d *Deps) { d.Logger = nil },
		"no issuer": func(d *Deps) { d.Issuer = "" },
	} {
		d := full
		mutate(&d)
		if _, err := New(d); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
