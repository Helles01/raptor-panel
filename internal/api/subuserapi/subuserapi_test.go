package subuserapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apitest"
)

func strs(rp apitest.Reply, path ...any) []string {
	l, _ := rp.At(path...).([]any)
	out := make([]string, len(l))
	for i, v := range l {
		out[i], _ = v.(string)
	}
	return out
}

func TestListAndView(t *testing.T) {
	r := newRig(t)

	rp := r.do(ada, "GET", usersURL, "")
	if rp.Status != 200 || rp.At("object") != "list" {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	l, _ := rp.At("data").([]any)
	if len(l) != 5 {
		t.Fatalf("%d subusers: %s", len(l), rp.Raw)
	}
	if rp.At("data", 0, "object") != "server_subuser" || rp.At("data", 0, "attributes", "username") != "bob" ||
		rp.At("data", 0, "attributes", "email") != "bob@example.com" || !reflect.DeepEqual(strs(rp, "data", 0, "attributes", "permissions"), perms[bob]) {
		t.Errorf("first subuser = %v", rp.At("data", 0))
	}

	rp = r.do(ada, "GET", usersURL+"/"+uuidOf(cleo), "")
	if rp.Status != 200 || rp.At("object") != "server_subuser" || rp.At("attributes", "uuid") != uuidOf(cleo) {
		t.Errorf("view = %d %s", rp.Status, rp.Raw)
	}
	for _, target := range []string{usersURL + "/" + uuidOf(frank), usersURL + "/not-a-uuid", usersURL + "/" + uuidOf(ada)} {
		if rp := r.do(ada, "GET", target, ""); rp.Status != 404 {
			t.Errorf("%s: status = %d", target, rp.Status)
		}
	}
}

func TestReadingNeedsUserRead(t *testing.T) {
	tests := []struct {
		user   uint64
		status int
	}{{ada, 200}, {grace, 200}, {bob, 200}, {cleo, 200}, {dave, 403}, {eve, 404}, {0, 401}}
	for _, tt := range tests {
		r := newRig(t)
		if rp := r.do(tt.user, "GET", usersURL, ""); rp.Status != tt.status {
			t.Errorf("list, user %d: status = %d, want %d", tt.user, rp.Status, tt.status)
		}
		if rp := r.do(tt.user, "GET", usersURL+"/"+uuidOf(cleo), ""); rp.Status != tt.status {
			t.Errorf("view, user %d: status = %d, want %d", tt.user, rp.Status, tt.status)
		}
	}
}

func TestCreateForAnExistingUser(t *testing.T) {
	r := newRig(t)

	rp := r.do(ada, "POST", usersURL, request("Frank@Example.com", "control.start", "bogus.right", "file.read", "control.start"))
	if rp.Status != 200 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	// Only the permissions that exist, once each, and the right to open the console.
	want := []string{"control.start", "file.read", "websocket.connect"}
	if got := strs(rp, "attributes", "permissions"); !reflect.DeepEqual(got, want) {
		t.Errorf("permissions = %v, want %v", got, want)
	}
	if rp.At("attributes", "username") != "frank" {
		t.Errorf("answer = %s", rp.Raw)
	}
	if len(r.store.adds) != 1 || r.store.adds[0].NewUser != nil || r.store.adds[0].UserID != frank || r.store.adds[0].ServerID != 100 ||
		!reflect.DeepEqual(r.store.adds[0].Permissions, want) {
		t.Errorf("adds = %+v", r.store.adds)
	}

	// One mail, to the user who was added; the account was not new.
	if len(r.outbox.mails) != 1 || r.outbox.mails[0].To != "frank@example.com" || !strings.Contains(r.outbox.mails[0].Subject, "Added To Server") ||
		!strings.Contains(r.outbox.mails[0].Text, "Survival") || !strings.Contains(r.outbox.mails[0].Text, "/server/aaaa1111") {
		t.Errorf("mails = %+v", r.outbox.mails)
	}

	keyID := uint64(ada)
	wantEntry := Entry{
		Event: "server:subuser.create", ServerID: 100, SubjectUserID: frank, ActorID: ada, APIKeyID: &keyID, IP: "203.0.113.9",
		Properties: `{"email":"Frank@Example.com","permissions":["control.start","file.read","websocket.connect"]}`,
	}
	if len(r.activity.entries) != 1 || !reflect.DeepEqual(r.activity.entries[0], wantEntry) {
		t.Errorf("entries = %+v, want %+v", r.activity.entries, wantEntry)
	}
}

var resetLink = regexp.MustCompile(`/auth/password/reset/([0-9a-f]{64})\?email=`)

func TestCreateMakesAnAccountForANewAddress(t *testing.T) {
	r := newRig(t)

	rp := r.do(ada, "POST", usersURL, request("new.person+tag@example.com", "control.start"))
	if rp.Status != 200 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if len(r.store.adds) != 1 || r.store.adds[0].NewUser == nil {
		t.Fatalf("adds = %+v", r.store.adds)
	}
	rec := r.store.adds[0].NewUser
	if rec.Email != "new.person+tag@example.com" || !strings.HasPrefix(rec.Username, "new.persontag") || rec.FirstName != "Server" || rec.LastName != "Subuser" ||
		rec.Admin || !rec.NeedsSetup || rec.PasswordHash == "" || rec.UUID == "" {
		t.Errorf("account = %+v", rec)
	}

	// Two mails: the account, with the link to choose a password, and the access. What is stored is the hash of the token in the link.
	if len(r.outbox.mails) != 2 {
		t.Fatalf("%d mails", len(r.outbox.mails))
	}
	var account string
	for _, m := range r.outbox.mails {
		if strings.Contains(m.Subject, "Account Created") {
			account = m.Text
		}
	}
	m := resetLink.FindStringSubmatch(account)
	if m == nil {
		t.Fatalf("no link in the mail: %s", account)
	}
	sum := sha256.Sum256([]byte(m[1]))
	if hex.EncodeToString(sum[:]) != r.store.adds[0].SetupTokenHash {
		t.Error("the stored token is not the hash of the one in the mail")
	}
	if !strings.Contains(account, url.QueryEscape("new.person+tag@example.com")) || !strings.Contains(account, "https://panel.example.com/auth/password/reset/") {
		t.Errorf("link = %s", account)
	}
}

func TestCreateRetriesATakenUsername(t *testing.T) {
	r := newRig(t)
	r.store.taken = 2
	if rp := r.do(ada, "POST", usersURL, request("someone@example.com", "control.start")); rp.Status != 200 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if len(r.store.adds) != 3 {
		t.Fatalf("%d tries", len(r.store.adds))
	}
	names := map[string]bool{}
	for _, a := range r.store.adds {
		names[a.NewUser.Username] = true
	}
	if len(names) != 3 {
		t.Errorf("the same user name was tried again: %v", names)
	}

	r = newRig(t)
	r.store.taken = 100
	rp := r.do(ada, "POST", usersURL, request("someone@example.com", "control.start"))
	if rp.Status != 500 || len(r.store.adds) != maxAttempts || len(r.outbox.mails) != 0 || len(r.activity.entries) != 0 {
		t.Errorf("status = %d after %d tries", rp.Status, len(r.store.adds))
	}
}

func TestCreateWhenTheAddressGotAnAccountMeanwhile(t *testing.T) {
	r := newRig(t)
	r.store.emailTaken = true
	rp := r.do(ada, "POST", usersURL, request("late@example.com", "control.start"))
	if rp.Status != 200 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	// The second try found the account and used it: no setup mail.
	if len(r.store.adds) != 2 || r.store.adds[1].NewUser != nil {
		t.Errorf("adds = %+v", r.store.adds)
	}
	for _, m := range r.outbox.mails {
		if strings.Contains(m.Subject, "Account Created") {
			t.Error("a setup mail went to an account that already existed")
		}
	}
}

func TestCreateRefusals(t *testing.T) {
	tests := []struct {
		name   string
		user   uint64
		body   string
		status int
		detail string
	}{
		{"the owner", ada, request("ada@example.com", "control.start"), 400, "You cannot add the server owner as a subuser for this server."},
		{"a subuser already", ada, request("cleo@example.com", "control.start"), 400, "A user with that email address is already assigned as a subuser for this server."},
		{"the same address in other case", ada, request("CLEO@example.com", "control.start"), 400, "A user with that email address is already assigned as a subuser for this server."},
		{"no email", ada, `{"permissions":["control.start"]}`, 422, "The email field is required."},
		{"a blank email", ada, `{"email":"  ","permissions":["control.start"]}`, 422, "The email field is required."},
		{"an email that is not one", ada, request("not an address", "control.start"), 422, "The email must be a valid email address."},
		{"an email that is a number", ada, `{"email":5,"permissions":["control.start"]}`, 422, "The email must be a string."},
		{"no permissions", ada, `{"email":"x@example.com"}`, 422, "The permissions field is required."},
		{"null permissions", ada, `{"email":"x@example.com","permissions":null}`, 422, "The permissions field is required."},
		{"empty permissions", ada, `{"email":"x@example.com","permissions":[]}`, 422, "The permissions field is required."},
		{"permissions that are not a list", ada, `{"email":"x@example.com","permissions":"control.start"}`, 422, "The permissions must be an array."},
		{"a permission that is not a text", ada, `{"email":"x@example.com","permissions":["control.start",5]}`, 422, "The permissions.1 must be a string."},
		{"not JSON", ada, `email=x`, 400, ""},
		{"empty", ada, ``, 400, ""},
		{"a subuser without user.create", cleo, request("x@example.com", "file.read"), 403, "This action is unauthorized."},
		{"a stranger", eve, request("x@example.com", "file.read"), 404, ""},
		{"nobody", 0, request("x@example.com", "file.read"), 401, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(tt.user, "POST", usersURL, tt.body)
			if rp.Status != tt.status {
				t.Fatalf("status = %d, want %d: %s", rp.Status, tt.status, rp.Raw)
			}
			if tt.detail != "" && rp.Detail() != tt.detail {
				t.Errorf("detail = %q, want %q", rp.Detail(), tt.detail)
			}
			if len(r.store.adds) != 0 || len(r.outbox.mails) != 0 || len(r.activity.entries) != 0 {
				t.Error("something was stored, mailed or logged for a refused request")
			}
		})
	}

	t.Run("two requests added the same user", func(t *testing.T) {
		r := newRig(t)
		r.store.exists = true
		rp := r.do(ada, "POST", usersURL, request("frank@example.com", "control.start"))
		if rp.Status != 400 || rp.Detail() != "A user with that email address is already assigned as a subuser for this server." {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
}

func TestACreatorMayHandOnOnlyWhatTheyHold(t *testing.T) {
	tests := []struct {
		name   string
		user   uint64
		perms  []string
		status int
	}{
		{"a subuser hands on what they hold", bob, []string{"file.read", "control.start"}, 200},
		{"a subuser hands on more", bob, []string{"file.read", "backup.create"}, 403},
		{"the owner hands on anything", ada, []string{"backup.create", "user.delete", "settings.reinstall"}, 200},
		{"an administrator too", grace, []string{"backup.create"}, 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(tt.user, "POST", usersURL, request("frank@example.com", tt.perms...))
			if rp.Status != tt.status {
				t.Fatalf("status = %d, want %d: %s", rp.Status, tt.status, rp.Raw)
			}
			if tt.status == 403 && rp.Detail() != "Cannot assign permissions to a subuser that your account does not actively possess." {
				t.Errorf("detail = %q", rp.Detail())
			}
			if tt.status == 403 && len(r.store.adds) != 0 {
				t.Error("a subuser was added")
			}
		})
	}
}

func TestCreateIsThrottledForEachServer(t *testing.T) {
	r := newRig(t)
	for i := 0; i < CreateLimit; i++ {
		rp := r.do(ada, "POST", usersURL, request(fmt.Sprintf("person%d@example.com", i), "control.start"))
		if rp.Status != 200 {
			t.Fatalf("addition %d: status = %d: %s", i+1, rp.Status, rp.Raw)
		}
	}
	rp := r.do(ada, "POST", usersURL, request("one.more@example.com", "control.start"))
	if rp.Status != 429 || rp.Header.Get("Retry-After") == "" {
		t.Errorf("the eleventh: %d, Retry-After %q", rp.Status, rp.Header.Get("Retry-After"))
	}
	if len(r.store.adds) != CreateLimit {
		t.Errorf("%d additions", len(r.store.adds))
	}
}

func TestRefusedCallsDoNotUseUpTheAllowance(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 20; i++ {
		r.do(eve, "POST", usersURL, request("x@example.com", "file.read"))  // a stranger
		r.do(cleo, "POST", usersURL, request("x@example.com", "file.read")) // no user.create
		r.do(0, "POST", usersURL, request("x@example.com", "file.read"))
	}
	if rp := r.do(ada, "POST", usersURL, request("fine@example.com", "control.start")); rp.Status != 200 {
		t.Errorf("status = %d: the allowance was used up by refused calls", rp.Status)
	}
}

func TestUpdate(t *testing.T) {
	r := newRig(t)

	rp := r.do(ada, "POST", usersURL+"/"+uuidOf(cleo), body("file.read", "control.start", "nonsense"))
	if rp.Status != 200 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	want := []string{"file.read", "control.start", "websocket.connect"}
	if got := strs(rp, "attributes", "permissions"); !reflect.DeepEqual(got, want) {
		t.Errorf("permissions = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(r.store.setCalls, []setCall{{cleo, want, true}}) {
		t.Errorf("stored %+v: the tokens and SFTP access are to be revoked with it", r.store.setCalls)
	}
	wantProps := `{"email":"cleo@example.com","new":["control.start","file.read","websocket.connect"],"old":["file.read","user.read","websocket.connect"],"revoked":true}`
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:subuser.update" || r.activity.entries[0].Properties != wantProps ||
		r.activity.entries[0].SubjectUserID != cleo {
		t.Errorf("entries = %+v", r.activity.entries)
	}
}

func TestUpdateWithoutAChangeDoesNothing(t *testing.T) {
	r := newRig(t)
	// The same permissions in another order, with an unknown one that is dropped.
	rp := r.do(ada, "POST", usersURL+"/"+uuidOf(cleo), body("websocket.connect", "file.read", "user.read", "unknown.right"))
	if rp.Status != 200 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if len(r.store.setCalls) != 0 || len(r.activity.entries) != 0 {
		t.Errorf("stored %+v, logged %+v: nothing changed, so nothing is revoked or logged", r.store.setCalls, r.activity.entries)
	}
	if got := strs(rp, "attributes", "permissions"); !reflect.DeepEqual(sortedCopy(got), sortedCopy(perms[cleo])) {
		t.Errorf("permissions = %v", got)
	}
}

func sortedCopy(l []string) []string {
	c := slices.Clone(l)
	slices.Sort(c)
	return c
}

func TestWhoMayChangeWhom(t *testing.T) {
	tests := []struct {
		name   string
		user   uint64
		target uint64
		perms  []string
		status int
		detail string
	}{
		{"nobody changes themselves", bob, bob, []string{"file.read"}, 403, "This action is unauthorized."},
		{"the owner changes a subuser", ada, zed, []string{"file.read"}, 200, ""},
		{"a subuser changes one who holds no more than they do", bob, cleo, []string{"file.read"}, 200, ""},
		{"a subuser does not change one who holds more", bob, zed, []string{"file.read"}, 403, "Cannot change a subuser who has permissions that your account does not actively possess."},
		{"a subuser does not hand on more than they hold", bob, cleo, []string{"backup.create"}, 403, "Cannot assign permissions to a subuser that your account does not actively possess."},
		{"a subuser without user.update", cleo, gil, []string{"file.read"}, 403, "This action is unauthorized."},
		{"a user who is no subuser of the server", ada, frank, []string{"file.read"}, 404, ""},
		{"a stranger", eve, cleo, []string{"file.read"}, 404, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(tt.user, "POST", usersURL+"/"+uuidOf(tt.target), body(tt.perms...))
			if rp.Status != tt.status {
				t.Fatalf("status = %d, want %d: %s", rp.Status, tt.status, rp.Raw)
			}
			if tt.detail != "" && rp.Detail() != tt.detail {
				t.Errorf("detail = %q, want %q", rp.Detail(), tt.detail)
			}
			if tt.status != 200 && len(r.store.setCalls) != 0 {
				t.Error("permissions were stored")
			}
		})
	}
}

func TestUpdateValidation(t *testing.T) {
	for name, tt := range map[string]struct{ body, detail string }{
		"none":            {`{}`, "The permissions field is required."},
		"not a list":      {`{"permissions":"x"}`, "The permissions must be an array."},
		"a number inside": {`{"permissions":[1]}`, "The permissions.0 must be a string."},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(ada, "POST", usersURL+"/"+uuidOf(cleo), tt.body)
			if rp.Status != 422 || rp.Detail() != tt.detail {
				t.Errorf("answer = %d %s", rp.Status, rp.Raw)
			}
		})
	}
	r := newRig(t)
	if rp := r.do(ada, "POST", usersURL+"/not-a-uuid", body("file.read")); rp.Status != 404 {
		t.Errorf("status = %d", rp.Status)
	}
	if rp := r.do(ada, "POST", usersURL+"/"+uuidOf(cleo), `nonsense`); rp.Status != 400 {
		t.Errorf("status = %d", rp.Status)
	}
}

func TestDelete(t *testing.T) {
	r := newRig(t)

	rp := r.do(ada, "DELETE", usersURL+"/"+uuidOf(cleo), "")
	if rp.Status != 204 || len(rp.Raw) != 0 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if !reflect.DeepEqual(r.store.removeIDs, []uint64{cleo}) {
		t.Errorf("removed %v", r.store.removeIDs)
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "server:subuser.delete" || r.activity.entries[0].Properties != `{"email":"cleo@example.com","revoked":true}` ||
		r.activity.entries[0].SubjectUserID != cleo {
		t.Errorf("entries = %+v", r.activity.entries)
	}
	if len(r.outbox.mails) != 1 || r.outbox.mails[0].To != "cleo@example.com" || !strings.Contains(r.outbox.mails[0].Subject, "Removed From Server") {
		t.Errorf("mails = %+v", r.outbox.mails)
	}
	if rp := r.do(ada, "GET", usersURL+"/"+uuidOf(cleo), ""); rp.Status != 404 {
		t.Errorf("the subuser is still there: %d", rp.Status)
	}
}

func TestDeleteRules(t *testing.T) {
	tests := []struct {
		name   string
		user   uint64
		target uint64
		status int
	}{
		{"nobody removes themselves", bob, bob, 403},
		{"a subuser removes one who holds no more", bob, cleo, 204},
		{"a subuser does not remove one who holds more", bob, zed, 403},
		{"without user.delete", cleo, gil, 403},
		{"an administrator removes anyone", grace, zed, 204},
		{"a user who is no subuser", ada, frank, 404},
		{"a stranger", eve, cleo, 404},
		{"nobody", 0, cleo, 401},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			if rp := r.do(tt.user, "DELETE", usersURL+"/"+uuidOf(tt.target), ""); rp.Status != tt.status {
				t.Fatalf("status = %d, want %d: %s", rp.Status, tt.status, rp.Raw)
			}
			if tt.status != 204 && (len(r.store.removeIDs) != 0 || len(r.outbox.mails) != 0 || len(r.activity.entries) != 0) {
				t.Error("something was removed, mailed or logged for a refused request")
			}
		})
	}
}

func TestFailuresOfTheStoreDoNotLeak(t *testing.T) {
	for name, call := range map[string]func(*rig) apitest.Reply{
		"list": func(r *rig) apitest.Reply { return r.do(ada, "GET", usersURL, "") },
		"view": func(r *rig) apitest.Reply { return r.do(ada, "GET", usersURL+"/"+uuidOf(cleo), "") },
		"create": func(r *rig) apitest.Reply {
			return r.do(ada, "POST", usersURL, request("frank@example.com", "file.read"))
		},
		"update": func(r *rig) apitest.Reply { return r.do(ada, "POST", usersURL+"/"+uuidOf(cleo), body("file.read")) },
		"delete": func(r *rig) apitest.Reply { return r.do(ada, "DELETE", usersURL+"/"+uuidOf(cleo), "") },
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

func TestTheChangeStandsWhenAMailOrAnEntryFails(t *testing.T) {
	r := newRig(t)
	r.outbox.err = errBoom
	r.activity.err = errBoom
	for _, call := range []func() apitest.Reply{
		func() apitest.Reply { return r.do(ada, "POST", usersURL, request("frank@example.com", "file.read")) },
		func() apitest.Reply { return r.do(ada, "POST", usersURL+"/"+uuidOf(cleo), body("control.start")) },
		func() apitest.Reply { return r.do(ada, "DELETE", usersURL+"/"+uuidOf(dave), "") },
	} {
		if rp := call(); rp.Status != 200 && rp.Status != 204 {
			t.Errorf("status = %d: %s", rp.Status, rp.Raw)
		}
	}
	if !strings.Contains(r.logs.String(), "activity entry not stored") || !strings.Contains(r.logs.String(), "a mail about a subuser was not queued") {
		t.Errorf("failures were not logged: %s", r.logs.String())
	}
}

func TestHandlersRefuseWithoutTheirPrincipal(t *testing.T) {
	r := newRig(t)
	client := apitest.NewWorld().Serve(t, func(g api.Group) {
		bare := g.Sub("/bare")
		bare.GET("/list", r.handlers.List, api.Public())
		bare.GET("/:user", r.handlers.View, api.Public())
		bare.POST("", r.handlers.Create, api.Public())
		bare.POST("/:user", r.handlers.Update, api.Public())
		bare.DELETE("/:user", r.handlers.Delete, api.Public())
	})
	u := "/bare/" + uuidOf(cleo)
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "/bare/list", ""}, {"GET", u, ""}, {"POST", "/bare", request("x@example.com", "file.read")}, {"POST", u, body("file.read")}, {"DELETE", u, ""},
	} {
		if rp := client.As(0, rt.method, rt.path, rt.body); rp.Status != 403 {
			t.Errorf("%s %s: status = %d, want 403", rt.method, rt.path, rp.Status)
		}
	}
	if r.store.lists+len(r.store.adds)+len(r.store.setCalls)+len(r.store.removeIDs) != 0 {
		t.Error("a store was asked")
	}
}

func TestNewNeedsEverything(t *testing.T) {
	r := newRig(t)
	full := r.handlers.d
	if _, err := New(full); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Deps){
		"no store": func(d *Deps) { d.Store = nil }, "no activity": func(d *Deps) { d.Activity = nil }, "no outbox": func(d *Deps) { d.Outbox = nil },
		"no limiter": func(d *Deps) { d.CreateLimiter = nil }, "no logger": func(d *Deps) { d.Logger = nil },
	} {
		d := full
		mutate(&d)
		if _, err := New(d); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestAServerNameThatCannotBeReadOnlyCostsTheMail(t *testing.T) {
	r := newRig(t)
	r.store.failName = errBoom
	if rp := r.do(ada, "POST", usersURL, request("frank@example.com", "file.read")); rp.Status != 200 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if len(r.outbox.mails) != 0 || !strings.Contains(r.logs.String(), "the name of a server was not read for a mail") {
		t.Errorf("mails %d, logs %s", len(r.outbox.mails), r.logs.String())
	}
}

func TestTheThrottleIsForTheServer(t *testing.T) {
	r := newRig(t)
	for i := 0; i < CreateLimit; i++ {
		r.do(ada, "POST", usersURL, request(fmt.Sprintf("person%d@example.com", i), "control.start"))
	}
	if rp := r.do(ada, "POST", usersURL, request("more@example.com", "control.start")); rp.Status != 429 {
		t.Fatalf("status = %d", rp.Status)
	}
	if rp := r.do(ada, "POST", "/api/client/servers/bbbb2222/users", request("more@example.com", "control.start")); rp.Status != 200 {
		t.Errorf("another server: status = %d: %s", rp.Status, rp.Raw)
	}
}
