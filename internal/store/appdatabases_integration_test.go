//go:build integration

package store_test

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/appserverdbs"
	"github.com/Helles01/raptor-panel/internal/api/databaseapi"
)

var errLimit = errors.New("limit reached")

// atLimit is the check of the endpoints without the panel's switch.
func atLimit(limit *int64, count int64) error {
	if limit != nil && count >= *limit {
		return errLimit
	}
	return nil
}

func dbRequest(server, host uint64, name, user, password string) appserverdbs.CreateRequest {
	return appserverdbs.CreateRequest{
		Database: appserverdbs.NewDatabase{ServerID: server, HostID: host, Name: name, Username: user, Password: password, Remote: "%"},
		Check:    atLimit,
	}
}

func TestAppServerDatabases(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.AppServerDatabases()
	r.exec("INSERT INTO database_hosts (name, host, port, username, password) VALUES ('main', 'db.example.com', 3306, 'panel', '')")
	r.exec("UPDATE servers SET database_limit = 2 WHERE id = 1")

	if err := st.Exists(r.ctx, 1); err != nil {
		t.Errorf("server 1: %v", err)
	}
	for _, id := range []uint64{99, 1 << 40} {
		if err := st.Exists(r.ctx, id); !errors.Is(err, appkit.ErrNotFound) {
			t.Errorf("server %d: %v", id, err)
		}
	}

	// Create: the check sees the limit and how many there are; the password is sealed, the column holds nothing.
	var limit *int64
	var count int64 = -1
	req := dbRequest(1, 1, "s1_main", "u1_aaaa", "pw-one")
	req.Check = func(l *int64, c int64) error { limit, count = l, c; return nil }
	made, err := st.Create(r.ctx, req)
	if err != nil || made.ID == 0 || made.ServerID != 1 || made.HostID != 1 || made.Name != "s1_main" || made.Username != "u1_aaaa" || made.Remote != "%" || made.MaxConnections != 0 || made.CreatedAt.IsZero() || made.Password != nil || made.Host != nil {
		t.Fatalf("created = %+v, %v", made, err)
	}
	if limit == nil || *limit != 2 || count != 0 {
		t.Errorf("the check saw limit %v and count %d", limit, count)
	}
	var column string
	r.sql.QueryRow("SELECT password FROM `databases` WHERE id = ?", made.ID).Scan(&column)
	if column != "" {
		t.Errorf("the password column holds %q", column)
	}
	if _, err := st.Create(r.ctx, dbRequest(1, 1, "s1_second", "u1_bbbb", "pw-two")); err != nil {
		t.Fatal(err)
	}
	// Refusals: the limit (as the check says), the name of the same server on the same host, a user name taken, a host or server that is not there. Nothing is left.
	if _, err := st.Create(r.ctx, dbRequest(1, 1, "s1_third", "u1_cccc", "pw")); !errors.Is(err, errLimit) {
		t.Errorf("over the limit: %v", err)
	}
	r.exec("UPDATE servers SET database_limit = NULL WHERE id = 1")
	if _, err := st.Create(r.ctx, dbRequest(1, 1, "s1_main", "u1_dddd", "pw")); !errors.Is(err, appserverdbs.ErrNameTaken) {
		t.Errorf("name taken: %v", err)
	}
	if _, err := st.Create(r.ctx, dbRequest(1, 1, "s1_other", "u1_aaaa", "pw")); err == nil || errors.Is(err, appserverdbs.ErrNameTaken) {
		t.Errorf("user name taken: %v", err)
	}
	for _, host := range []uint64{0, 99, 1 << 40} {
		if _, err := st.Create(r.ctx, dbRequest(1, host, "s1_x", "u1_eeee", "pw")); !errors.Is(err, appserverdbs.ErrNoHost) {
			t.Errorf("host %d: %v", host, err)
		}
	}
	if _, err := st.Create(r.ctx, dbRequest(99, 1, "s99_x", "u99_ffff", "pw")); !errors.Is(err, appkit.ErrNotFound) {
		t.Errorf("server 99: %v", err)
	}
	var rows, sealed int
	r.sql.QueryRow("SELECT COUNT(*) FROM `databases`").Scan(&rows)
	r.sql.QueryRow("SELECT COUNT(*) FROM raptor_secrets WHERE table_name = 'databases'").Scan(&sealed)
	if rows != 2 || sealed != 2 {
		t.Errorf("%d databases and %d sealed passwords after the refusals", rows, sealed)
	}

	// Reading: the relations on request, the databases of the server only.
	list, err := st.List(r.ctx, 1, includes("host", "password"))
	if err != nil || len(list) != 2 || list[0].Name != "s1_main" || list[1].Name != "s1_second" ||
		list[0].Password == nil || *list[0].Password != "pw-one" || *list[1].Password != "pw-two" || list[0].Host == nil || list[0].Host.Host != "db.example.com" || list[0].Host.Port != 3306 || list[0].Host.Username != "panel" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if plain, _ := st.List(r.ctx, 1, nil); plain[0].Password != nil || plain[0].Host != nil {
		t.Error("relations that were not asked for were filled")
	}
	if other, err := st.List(r.ctx, 2, nil); err != nil || len(other) != 0 {
		t.Errorf("server 2: %+v, %v", other, err)
	}
	if got, err := st.Find(r.ctx, 1, list[1].ID, includes("password")); err != nil || got.Name != "s1_second" || *got.Password != "pw-two" {
		t.Errorf("find = %+v, %v", got, err)
	}
	if _, err := st.Find(r.ctx, 2, list[1].ID, nil); !errors.Is(err, appkit.ErrNotFound) {
		t.Errorf("a database of another server: %v", err)
	}
	r.exec("DELETE FROM raptor_secrets WHERE table_name = 'databases' AND row_id = ?", list[1].ID)
	if _, err := st.Find(r.ctx, 1, list[1].ID, includes("password")); err == nil || errors.Is(err, appkit.ErrNotFound) {
		t.Errorf("a password that is not sealed: %v", err)
	}

	// SetPassword replaces the sealed password (one row for the database, not two); a database that is not there is refused.
	if err := st.SetPassword(r.ctx, list[0].ID, "changed"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Find(r.ctx, 1, list[0].ID, includes("password")); got.Password == nil || *got.Password != "changed" {
		t.Errorf("after the change: %+v", got)
	}
	r.sql.QueryRow("SELECT COUNT(*) FROM raptor_secrets WHERE table_name = 'databases' AND row_id = ?", list[0].ID).Scan(&sealed)
	if sealed != 1 {
		t.Errorf("%d sealed passwords for one database", sealed)
	}
	for _, id := range []uint64{99, 1 << 40} {
		if err := st.SetPassword(r.ctx, id, "x"); !errors.Is(err, appkit.ErrNotFound) {
			t.Errorf("database %d: %v", id, err)
		}
	}

	// Delete removes the row of the server's own database with its password; another server's is refused and stays.
	if err := st.Delete(r.ctx, 2, list[0].ID); !errors.Is(err, appkit.ErrNotFound) {
		t.Errorf("a database of another server: %v", err)
	}
	if err := st.Delete(r.ctx, 1, list[0].ID); err != nil {
		t.Fatal(err)
	}
	r.sql.QueryRow("SELECT (SELECT COUNT(*) FROM `databases` WHERE id = ?) + (SELECT COUNT(*) FROM raptor_secrets WHERE table_name = 'databases' AND row_id = ?)", list[0].ID, list[0].ID).Scan(&rows)
	if rows != 0 {
		t.Errorf("%d rows of the deleted database are left", rows)
	}
	if err := st.Delete(r.ctx, 1, list[0].ID); !errors.Is(err, appkit.ErrNotFound) {
		t.Errorf("twice: %v", err)
	}
}

// Of many requests at once for a server that may have one more database, one is made: the limit is checked against what the server has when the database is
// stored.
func TestServerDatabaseLimitUnderLoad(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.AppServerDatabases()
	r.exec("INSERT INTO database_hosts (name, host, port, username, password) VALUES ('main', 'db.example.com', 3306, 'panel', '')")
	r.exec("UPDATE servers SET database_limit = 1 WHERE id = 2")
	var seq atomic.Int32
	wins := once(t, 12, func() (bool, error) {
		n := int(seq.Add(1))
		_, err := st.Create(r.ctx, dbRequest(2, 1, "s2_"+pad(n), "u2_"+pad(n), "pw"))
		if errors.Is(err, errLimit) {
			return false, nil
		}
		return err == nil, err
	})
	var rows int
	r.sql.QueryRow("SELECT COUNT(*) FROM `databases` WHERE server_id = 2").Scan(&rows)
	if wins != 1 || rows != 1 {
		t.Errorf("%d requests made a database, %d rows, for a limit of 1", wins, rows)
	}
}

func TestClientDatabases(t *testing.T) {
	f := clientFx(t)
	r := f.r
	st := r.db.ClientDatabases()
	r.exec("INSERT INTO database_hosts (name, host, port, username, password, node_id) VALUES ('a', 'db-a.example.com', 3306, 'panel', '', NULL), ('b', 'db-b.example.com', 3307, 'panel', '', ?)", f.node1)

	hosts, err := st.Hosts(r.ctx)
	if err != nil || len(hosts) != 2 || hosts[0].ID != 1 || hosts[0].NodeID != nil || hosts[1].ID != 2 || hosts[1].NodeID == nil || *hosts[1].NodeID != f.node1 {
		t.Errorf("hosts = %+v, %v", hosts, err)
	}

	r.exec("UPDATE servers SET database_limit = 5 WHERE id = 1")
	var limit *int64
	req := databaseapi.CreateRequest{
		Database: databaseapi.NewDatabase{ServerID: 1, HostID: 2, Name: "s1_mine", Username: "u1_mine", Password: "secret-1", Remote: "10.%"},
		Check:    func(l *int64, c int64) error { limit = l; return nil },
	}
	made, err := st.Create(r.ctx, req)
	if err != nil || made.ID == 0 || made.HostID != 2 || made.HostAddress != "db-b.example.com" || made.HostPort != 3307 || made.Name != "s1_mine" || made.Username != "u1_mine" || made.Remote != "10.%" ||
		made.MaxConnections == nil || *made.MaxConnections != 0 {
		t.Fatalf("created = %+v, %v", made, err)
	}
	if limit == nil || *limit != 5 {
		t.Errorf("the check saw the limit %v of a server that may have 5", limit)
	}
	if p, err := st.Password(r.ctx, made.ID); err != nil || p != "secret-1" {
		t.Errorf("password = %q, %v", p, err)
	}
	if _, err := st.Password(r.ctx, 99); err == nil {
		t.Error("the password of a database that is not there")
	}
	req.Database.Username = "u1_other"
	if _, err := st.Create(r.ctx, req); !errors.Is(err, databaseapi.ErrNameTaken) {
		t.Errorf("name taken: %v", err)
	}
	req.Database.Name, req.Database.Username, req.Database.HostID = "s1_x", "u1_x", 99
	if _, err := st.Create(r.ctx, req); err == nil {
		t.Error("a host that is not there")
	}

	list, err := st.List(r.ctx, 1)
	if err != nil || len(list) != 1 || list[0].ID != made.ID {
		t.Errorf("list = %+v, %v", list, err)
	}
	if other, _ := st.List(r.ctx, 2); len(other) != 0 {
		t.Errorf("server 2: %+v", other)
	}
	if _, err := st.Find(r.ctx, 2, made.ID); !errors.Is(err, databaseapi.ErrNotFound) {
		t.Errorf("a database of another server: %v", err)
	}
	if err := st.SetPassword(r.ctx, made.ID, "secret-2"); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.Password(r.ctx, made.ID); p != "secret-2" {
		t.Errorf("password after the change = %q", p)
	}
	if err := st.SetPassword(r.ctx, 99, "x"); !errors.Is(err, databaseapi.ErrNotFound) {
		t.Errorf("unknown database: %v", err)
	}
	if err := st.Delete(r.ctx, 2, made.ID); !errors.Is(err, databaseapi.ErrNotFound) {
		t.Errorf("a database of another server: %v", err)
	}
	if err := st.Delete(r.ctx, 1, made.ID); err != nil {
		t.Fatal(err)
	}
	var left int
	r.sql.QueryRow("SELECT (SELECT COUNT(*) FROM `databases`) + (SELECT COUNT(*) FROM raptor_secrets WHERE table_name = 'databases')").Scan(&left)
	if left != 0 {
		t.Errorf("%d rows are left", left)
	}
}
