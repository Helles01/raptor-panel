//go:build integration

package store_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/api/subuserapi"
	"github.com/Helles01/raptor-panel/internal/mail"
	"github.com/Helles01/raptor-panel/internal/outbox"
	"github.com/Helles01/raptor-panel/internal/users"
)

func TestOwnerSubusers(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.ClientSubusers()
	var adaUUID, bobUUID string
	r.sql.QueryRow("SELECT uuid FROM users WHERE username = 'ada'").Scan(&adaUUID)
	r.sql.QueryRow("SELECT uuid FROM users WHERE username = 'bob'").Scan(&bobUUID)

	// Reading.
	list, err := st.List(r.ctx, 3)
	if err != nil || len(list) != 1 || list[0].UserID != 1 || list[0].User.UUID != adaUUID || list[0].User.Username != "ada" || list[0].User.Email != "ada@example.com" ||
		list[0].User.UsesTOTP || list[0].User.CreatedAt.IsZero() || len(list[0].Permissions) != 2 || list[0].Permissions[1] != "file.read" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if other, _ := st.List(r.ctx, 4); len(other) != 0 {
		t.Errorf("server 4: %+v", other)
	}
	if got, err := st.Find(r.ctx, 3, adaUUID); err != nil || got.UserID != 1 {
		t.Errorf("find = %+v, %v", got, err)
	}
	for _, c := range []struct {
		server uint64
		uuid   string
	}{{4, adaUUID}, {3, bobUUID}, {3, "nope"}} {
		if _, err := st.Find(r.ctx, c.server, c.uuid); !errors.Is(err, subuserapi.ErrNotFound) {
			t.Errorf("find %s on %d: %v", c.uuid, c.server, err)
		}
	}
	if ref, err := st.UserByEmail(r.ctx, "BOB@Example.com"); err != nil || ref.ID != 2 || ref.UUID != bobUUID || ref.Username != "bob" {
		t.Errorf("by email = %+v, %v", ref, err)
	}
	if _, err := st.UserByEmail(r.ctx, "nobody@example.com"); !errors.Is(err, subuserapi.ErrNotFound) {
		t.Errorf("unknown address: %v", err)
	}
	if is, _ := st.IsSubuser(r.ctx, 3, 1); !is {
		t.Error("ada is a subuser of server 3")
	}
	for _, c := range [][2]uint64{{3, 2}, {4, 1}, {1 << 40, 1}} {
		if is, err := st.IsSubuser(r.ctx, c[0], c[1]); is || err != nil {
			t.Errorf("subuser %v: %v %v", c, is, err)
		}
	}
	if name, err := st.ServerName(r.ctx, 3); err != nil || name != "server-3333" {
		t.Errorf("server name = %q, %v", name, err)
	}
	if _, err := st.ServerName(r.ctx, 99); !errors.Is(err, subuserapi.ErrNotFound) {
		t.Errorf("server 99: %v", err)
	}

	// Add: an account that exists, once; the permissions are stored as a list.
	added, err := st.Add(r.ctx, subuserapi.AddRequest{ServerID: 1, UserID: 2, Permissions: []string{"control.start", "control.stop"}})
	if err != nil || added.UserID != 2 || added.User.Username != "bob" || len(added.Permissions) != 2 || added.Permissions[0] != "control.start" {
		t.Fatalf("added = %+v, %v", added, err)
	}
	if _, err := st.Add(r.ctx, subuserapi.AddRequest{ServerID: 1, UserID: 2, Permissions: []string{"control.start"}}); !errors.Is(err, subuserapi.ErrExists) {
		t.Errorf("twice: %v", err)
	}
	if _, err := st.Add(r.ctx, subuserapi.AddRequest{ServerID: 99, UserID: 2}); !errors.Is(err, subuserapi.ErrNotFound) {
		t.Errorf("server 99: %v", err)
	}
	bare, err := st.Add(r.ctx, subuserapi.AddRequest{ServerID: 2, UserID: 2})
	if err != nil || bare.Permissions == nil || len(bare.Permissions) != 0 {
		t.Errorf("no permissions: %+v, %v", bare, err)
	}
	var stored string
	r.sql.QueryRow("SELECT permissions FROM subusers WHERE server_id = 2").Scan(&stored)
	if stored != "[]" {
		t.Errorf("stored permissions = %q", stored)
	}

	// Add with an account made for the address: the account, its setup link and the subuser are one change; a taken name or address changes nothing.
	invited := users.Record{UUID: "99999999-9999-4999-8999-999999999999", Email: "new@example.com", Username: "newbie", FirstName: "New", LastName: "Person", PasswordHash: "$2a$10$x", Language: "en"}
	made, err := st.Add(r.ctx, subuserapi.AddRequest{ServerID: 1, NewUser: &invited, SetupTokenHash: "setup-link", Permissions: []string{"file.read"}})
	if err != nil || made.User.Username != "newbie" || made.User.Email != "new@example.com" || made.User.UUID != invited.UUID {
		t.Fatalf("made = %+v, %v", made, err)
	}
	if link, found, _ := r.db.AuthapiResets().Latest(r.ctx, "new@example.com"); !found || link.Hash != "setup-link" {
		t.Errorf("the setup link = %+v %v", link, found)
	}
	for _, c := range []struct {
		name   string
		user   users.Record
		wanted error
	}{
		{"user name", users.Record{UUID: "88888888-8888-4888-8888-888888888881", Email: "other@example.com", Username: "newbie", PasswordHash: "x", Language: "en"}, subuserapi.ErrUsernameTaken},
		{"address", users.Record{UUID: "88888888-8888-4888-8888-888888888882", Email: "NEW@example.com", Username: "other", PasswordHash: "x", Language: "en"}, subuserapi.ErrEmailTaken},
	} {
		before := countRows(r, "users")
		if _, err := st.Add(r.ctx, subuserapi.AddRequest{ServerID: 2, NewUser: &c.user, SetupTokenHash: "link-2"}); !errors.Is(err, c.wanted) {
			t.Errorf("taken %s: %v", c.name, err)
		}
		if countRows(r, "users") != before {
			t.Errorf("an account was made although the %s is taken", c.name)
		}
	}
	if link, found, _ := r.db.AuthapiResets().Latest(r.ctx, "other@example.com"); found {
		t.Errorf("a setup link for an account that was not made: %+v", link)
	}

	// Of 12 requests at once to add one person, one is made: the server's row is locked while the entry is looked for (no unique key says so).
	wins := once(t, 12, func() (bool, error) {
		_, err := st.Add(r.ctx, subuserapi.AddRequest{ServerID: 4, UserID: 1, Permissions: []string{"file.read"}})
		if errors.Is(err, subuserapi.ErrExists) {
			return false, nil
		}
		return err == nil, err
	})
	if rows := countWhere(r, "subusers", "server_id = 4"); wins != 1 || rows != 1 {
		t.Errorf("%d requests made a subuser, %d rows", wins, rows)
	}
}

func countRows(r *rig, table string) int { return countWhere(r, table, "1 = 1") }

func countWhere(r *rig, table, where string) int {
	var n int
	if err := r.sql.QueryRow("SELECT COUNT(*) FROM `" + table + "` WHERE " + where).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func TestOwnerSubuserChanges(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.ClientSubusers()

	// New permissions, with and without the order to the node.
	if err := st.SetPermissions(r.ctx, 3, 1, []string{"file.read", "file.update"}, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.List(r.ctx, 3); len(got) != 1 || len(got[0].Permissions) != 2 || got[0].Permissions[1] != "file.update" {
		t.Errorf("after the change: %+v", got)
	}
	if orders := countRows(r, "raptor_outbox"); orders != 0 {
		t.Errorf("%d orders for a change that took nothing away", orders)
	}
	if err := st.SetPermissions(r.ctx, 3, 1, []string{"file.read"}, true); err != nil {
		t.Fatal(err)
	}
	var kind, payload string
	if err := r.sql.QueryRow("SELECT kind, payload FROM raptor_outbox").Scan(&kind, &payload); err != nil || kind != outbox.KindDeauthorize ||
		!strings.Contains(payload, uuid3) || !strings.Contains(payload, `"node_id":`+strconv.FormatUint(f.node2, 10)) {
		t.Errorf("order = %s %s, %v", kind, payload, err)
	}
	var adaUUID string
	r.sql.QueryRow("SELECT uuid FROM users WHERE username = 'ada'").Scan(&adaUUID)
	if !strings.Contains(payload, adaUUID) {
		t.Errorf("the order does not name the user: %s", payload)
	}

	// Refusals change nothing and queue nothing.
	for _, c := range [][2]uint64{{3, 2}, {4, 1}, {99, 1}, {3, 1 << 40}} {
		if err := st.SetPermissions(r.ctx, c[0], c[1], []string{"x"}, true); !errors.Is(err, subuserapi.ErrNotFound) {
			t.Errorf("change of %v: %v", c, err)
		}
		if err := st.Remove(r.ctx, c[0], c[1], true); !errors.Is(err, subuserapi.ErrNotFound) {
			t.Errorf("removal of %v: %v", c, err)
		}
	}
	if orders := countRows(r, "raptor_outbox"); orders != 1 {
		t.Errorf("%d orders after refused changes", orders)
	}

	// Removal.
	if err := st.Remove(r.ctx, 3, 1, true); err != nil {
		t.Fatal(err)
	}
	if countWhere(r, "subusers", "server_id = 3") != 0 || countRows(r, "raptor_outbox") != 2 {
		t.Error("the entry was not removed with its order")
	}
	if err := st.Remove(r.ctx, 3, 1, true); !errors.Is(err, subuserapi.ErrNotFound) {
		t.Errorf("twice: %v", err)
	}
}

func TestSubuserOutboxQueuesTheMail(t *testing.T) {
	f := newFx(t)
	r := f.r
	if err := r.db.SubuserOutbox().Send(r.ctx, mail.Message{To: "x@example.com", Subject: "Invited", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	var kind, payload string
	if err := r.sql.QueryRow("SELECT kind, payload FROM raptor_outbox").Scan(&kind, &payload); err != nil || kind != outbox.KindMail || !strings.Contains(payload, "x@example.com") {
		t.Errorf("job = %s %s, %v", kind, payload, err)
	}
}
