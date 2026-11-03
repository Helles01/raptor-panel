//go:build integration

package store_test

import (
	"errors"
	"testing"

	"github.com/Helles01/raptor-panel/internal/api/settingsapi"
)

func TestSettingsStore(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.SettingsStore()

	// The server with the images of its egg, in the order of the egg.
	s, err := st.Server(r.ctx, 1)
	if err != nil || s.ID != 1 || s.Image != "ghcr.io/example/yolks:java_21" || len(s.Variables) != 2 {
		t.Fatalf("server = %+v, %v", s, err)
	}
	if len(s.EggImages) != 2 || s.EggImages[0].Label != "Java 21" || s.EggImages[0].Image != "ghcr.io/example/yolks:java_21" || s.EggImages[1].Label != "Java 17" {
		t.Errorf("images = %+v", s.EggImages)
	}
	if _, err := st.Server(r.ctx, 99); !errors.Is(err, settingsapi.ErrNotFound) {
		t.Errorf("unknown server: %v", err)
	}

	// A variable that has no value yet gets one; one that has, changes; the same value again is not a second row.
	count := func() int {
		var n int
		r.sql.QueryRow("SELECT COUNT(*) FROM server_variables WHERE server_id = 1").Scan(&n)
		return n
	}
	if err := st.SetVariable(r.ctx, 1, "SERVER_JARFILE", "paper.jar"); err != nil || count() != 1 {
		t.Fatalf("first value: %v, %d rows", err, count())
	}
	if err := st.SetVariable(r.ctx, 1, "SERVER_JARFILE", "paper.jar"); err != nil || count() != 1 {
		t.Fatalf("the same value: %v, %d rows", err, count())
	}
	if err := st.SetVariable(r.ctx, 1, "SERVER_JARFILE", "purpur.jar"); err != nil || count() != 1 {
		t.Fatalf("a new value: %v, %d rows", err, count())
	}
	if err := st.SetVariable(r.ctx, 1, "BUILD_NUMBER", ""); err != nil || count() != 2 {
		t.Fatalf("an empty value: %v, %d rows", err, count())
	}
	got, _ := st.Server(r.ctx, 1)
	if v := got.Variables[0]; v.ServerValue == nil || *v.ServerValue != "purpur.jar" {
		t.Errorf("variable = %+v", v)
	}
	if v := got.Variables[1]; v.ServerValue == nil || *v.ServerValue != "" {
		t.Errorf("the empty value = %+v", v)
	}
	// Another server's value is its own, a variable the egg does not have and a server that is not there are not found.
	if other, _ := st.Server(r.ctx, 2); other.Variables[0].ServerValue != nil {
		t.Errorf("server 2 got server 1's value: %+v", other.Variables[0])
	}
	if err := st.SetVariable(r.ctx, 1, "NOT_A_VARIABLE", "x"); !errors.Is(err, settingsapi.ErrNotFound) {
		t.Errorf("an unknown variable: %v", err)
	}
	if err := st.SetVariable(r.ctx, 99, "SERVER_JARFILE", "x"); !errors.Is(err, settingsapi.ErrNotFound) {
		t.Errorf("an unknown server: %v", err)
	}
	// Of 10 requests at once for a variable that has no value, one row.
	if n := once(t, 10, func() (bool, error) { return true, st.SetVariable(r.ctx, 2, "SERVER_JARFILE", "race.jar") }); n != 10 {
		t.Errorf("%d of 10 requests succeeded", n)
	}
	var rows int
	r.sql.QueryRow("SELECT COUNT(*) FROM server_variables WHERE server_id = 2").Scan(&rows)
	if rows != 1 {
		t.Errorf("%d rows for one variable after a race", rows)
	}

	if err := st.SetImage(r.ctx, 1, "ghcr.io/example/yolks:java_17"); err != nil {
		t.Fatal(err)
	}
	if err := st.Rename(r.ctx, 1, "New Name", "New description"); err != nil {
		t.Fatal(err)
	}
	if err := st.Rename(r.ctx, 1, "New Name", "New description"); err != nil {
		t.Errorf("the same name again: %v", err)
	}
	got, _ = st.Server(r.ctx, 1)
	if got.Image != "ghcr.io/example/yolks:java_17" || got.Name != "New Name" || got.Description != "New description" {
		t.Errorf("server = %+v", got)
	}
	if other, _ := st.Server(r.ctx, 2); other.Name == "New Name" {
		t.Error("another server was renamed")
	}
	for name, err := range map[string]error{"image": st.SetImage(r.ctx, 99, "x"), "rename": st.Rename(r.ctx, 99, "x", "y")} {
		if !errors.Is(err, settingsapi.ErrNotFound) {
			t.Errorf("%s of a server that is not there: %v", name, err)
		}
	}

	// The status changes only from the status the change was made against; of 10 requests at once, one.
	if n := once(t, 10, func() (bool, error) { return st.SetStatus(r.ctx, 1, "", "installing") }); n != 1 {
		t.Errorf("%d requests set the status", n)
	}
	if done, _ := st.SetStatus(r.ctx, 1, "", "installing"); done {
		t.Error("the status was set from a status the server did not have")
	}
	if done, err := st.SetStatus(r.ctx, 1, "installing", ""); err != nil || !done {
		t.Errorf("back to normal: %v %v", done, err)
	}
	if done, _ := st.SetStatus(r.ctx, 99, "", "installing"); done {
		t.Error("a server that is not there")
	}
}
