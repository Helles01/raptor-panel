package dbhosts

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

const goodPassword = "Abc!def@123456.78+90xyz^"

func sampleDB() Database {
	return Database{ServerID: 7, Name: "s7_world", Username: "u7_AbC123xyz0", Remote: "%", MaxConnections: 25}
}

func sqls(stmts []Statement) []string {
	out := make([]string, len(stmts))
	for i, s := range stmts {
		out[i] = s.SQL
	}
	return out
}

func TestCreate(t *testing.T) {
	stmts, err := Create(sampleDB(), goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"CREATE DATABASE IF NOT EXISTS `s7_world`",
		"CREATE USER `u7_AbC123xyz0`@`%` IDENTIFIED BY ? WITH MAX_USER_CONNECTIONS 25",
		"GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, ALTER, REFERENCES, INDEX, LOCK TABLES, CREATE ROUTINE, ALTER ROUTINE, EXECUTE, CREATE TEMPORARY TABLES, CREATE VIEW, SHOW VIEW, EVENT, TRIGGER ON `s7\\_world`.* TO `u7_AbC123xyz0`@`%`",
	}
	if !reflect.DeepEqual(sqls(stmts), want) {
		t.Errorf("statements:\n%s\nwant:\n%s", strings.Join(sqls(stmts), "\n"), strings.Join(want, "\n"))
	}
	if !reflect.DeepEqual(stmts[1].Args, []any{goodPassword}) || stmts[0].Args != nil || stmts[2].Args != nil {
		t.Errorf("the password must be the only argument: %v", stmts)
	}
	for _, s := range stmts {
		if strings.Contains(s.SQL, goodPassword) {
			t.Errorf("the password is part of the SQL text: %s", s.SQL)
		}
	}
}

func TestCreateWithoutAConnectionLimit(t *testing.T) {
	d := sampleDB()
	d.MaxConnections = 0
	stmts, err := Create(d, goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	if got := stmts[1].SQL; got != "CREATE USER `u7_AbC123xyz0`@`%` IDENTIFIED BY ?" {
		t.Errorf("CREATE USER = %q", got)
	}
}

func TestGrantEscapesTheWildcards(t *testing.T) {
	// In a GRANT, "_" stands for any character: unescaped, s1__secret would also reach s11_secret of another server.
	d := Database{ServerID: 1, Name: "s1__secret", Username: "u1_x", Remote: "%"}
	stmts, err := Create(d, goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	if got := stmts[0].SQL; got != "CREATE DATABASE IF NOT EXISTS `s1__secret`" {
		t.Errorf("the database itself keeps its name: %q", got)
	}
	if got := stmts[2].SQL; !strings.Contains(got, " ON `s1\\_\\_secret`.* TO ") {
		t.Errorf("GRANT = %q: every underscore must be escaped", got)
	}
}

func TestChangePasswordAndDrop(t *testing.T) {
	stmts, err := ChangePassword(sampleDB(), goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 1 || stmts[0].SQL != "ALTER USER `u7_AbC123xyz0`@`%` IDENTIFIED BY ?" || !reflect.DeepEqual(stmts[0].Args, []any{goodPassword}) {
		t.Errorf("ChangePassword = %+v", stmts)
	}

	stmts, err = Drop(sampleDB())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"DROP DATABASE IF EXISTS `s7_world`", "DROP USER IF EXISTS `u7_AbC123xyz0`@`%`"}
	if !reflect.DeepEqual(sqls(stmts), want) {
		t.Errorf("Drop = %v", sqls(stmts))
	}
}

func TestLegacyUnicodeNamesStillWork(t *testing.T) {
	// The reference allowed any Unicode letter, so an old row may hold one; it must still be possible to remove it.
	d := sampleDB()
	d.Name = "s7_café"
	if _, err := Drop(d); err != nil {
		t.Errorf("Drop: %v", err)
	}
}

func TestBuildersRefuseWhatIsNotTheServers(t *testing.T) {
	long := strings.Repeat("a", 65)
	tests := []struct {
		name string
		mut  func(*Database)
	}{
		{"a system database", func(d *Database) { d.Name = "mysql" }},
		{"information_schema", func(d *Database) { d.Name = "information_schema" }},
		{"another server's database", func(d *Database) { d.Name = "s8_world" }},
		{"a database of a server whose id starts like this one", func(d *Database) { d.Name = "s70_world" }},
		{"a database without the separator", func(d *Database) { d.Name = "s7world" }},
		{"no name", func(d *Database) { d.Name = "" }},
		{"a name that is too long", func(d *Database) { d.Name = "s7_" + long }},
		{"a backtick in the name", func(d *Database) { d.Name = "s7_a`b" }},
		{"a backtick that ends the identifier", func(d *Database) { d.Name = "s7_x`; DROP DATABASE mysql; --" }},
		{"a space in the name", func(d *Database) { d.Name = "s7_a b" }},
		{"a quote in the name", func(d *Database) { d.Name = "s7_a'b" }},
		{"a newline in the name", func(d *Database) { d.Name = "s7_a\nb" }},
		{"a NUL in the name", func(d *Database) { d.Name = "s7_a\x00b" }},
		{"a percent sign in the name", func(d *Database) { d.Name = "s7_a%" }},

		{"the root user", func(d *Database) { d.Username = "root" }},
		{"another server's user", func(d *Database) { d.Username = "u8_AbC123xyz0" }},
		{"a backtick in the user", func(d *Database) { d.Username = "u7_a`b" }},
		{"an at sign in the user", func(d *Database) { d.Username = "u7_a@b" }},
		{"no user", func(d *Database) { d.Username = "" }},

		{"a backtick in the remote", func(d *Database) { d.Remote = "%`" }},
		{"a quote in the remote", func(d *Database) { d.Remote = "%'" }},
		{"a space in the remote", func(d *Database) { d.Remote = "10.0.0.1 " }},
		{"no remote", func(d *Database) { d.Remote = "" }},
		{"a negative connection limit", func(d *Database) { d.MaxConnections = -1 }},
		{"a huge connection limit", func(d *Database) { d.MaxConnections = MaxConnectionsLimit + 1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := sampleDB()
			tt.mut(&d)
			if _, err := Create(d, goodPassword); err == nil {
				t.Error("Create accepted it")
			}
			if _, err := ChangePassword(d, goodPassword); err == nil {
				t.Error("ChangePassword accepted it")
			}
			if _, err := Drop(d); err == nil {
				t.Error("Drop accepted it")
			}
		})
	}
}

func TestBuildersRefusePasswordsThatAreNotGenerated(t *testing.T) {
	for _, pw := range []string{"", "short", "has a space!", "quote'quote12", `back\slash12345`, "semi;colon12345", "new\nline123456", strings.Repeat("a", 65), "unicodé123456", "`tick`1234567"} {
		if _, err := Create(sampleDB(), pw); err == nil {
			t.Errorf("Create accepted the password %q", pw)
		}
		if _, err := ChangePassword(sampleDB(), pw); err == nil {
			t.Errorf("ChangePassword accepted the password %q", pw)
		}
	}
	for i := 0; i < 200; i++ {
		pw, err := Password(randReader{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Create(sampleDB(), pw); err != nil {
			t.Fatalf("a generated password was refused: %q: %v", pw, err)
		}
	}
}

type randReader struct{}

func (randReader) Read(p []byte) (int, error) { return rand.Read(p) }

// However hostile the pieces are, a statement that is built has exactly the quoted identifiers it should have.
func TestStatementsKeepTheirShape(t *testing.T) {
	identifierChars := []rune("abcXYZ0129_-é")
	remoteChars := []rune("0123.%:/_-abc")
	hostile := []rune("`'\";\\ \n\x00@$(#=")
	rng := rand.New(rand.NewSource(1))

	// Mostly valid characters with a few hostile ones, so that a good share of the pieces is accepted.
	piece := func(valid []rune) string {
		out := make([]rune, 1+rng.Intn(9))
		for i := range out {
			if rng.Intn(12) == 0 {
				out[i] = hostile[rng.Intn(len(hostile))]
			} else {
				out[i] = valid[rng.Intn(len(valid))]
			}
		}
		return string(out)
	}

	built := 0
	for i := 0; i < 20000; i++ {
		d := Database{ServerID: 7, Name: "s7_" + piece(identifierChars), Username: "u7_" + piece(identifierChars), Remote: piece(remoteChars), MaxConnections: rng.Intn(3)}
		stmts, err := Create(d, goodPassword)
		if err != nil {
			continue
		}
		built++

		// Two backticks per quoted identifier: one more or fewer would mean something ended or began a quote.
		backticks := []int{2, 4, 6}
		for j, s := range stmts {
			if got := strings.Count(s.SQL, "`"); got != backticks[j] {
				t.Fatalf("%q has %d backticks, want %d (from %+v)", s.SQL, got, backticks[j], d)
			}
			if strings.ContainsAny(s.SQL, ";\n\x00'\"") {
				t.Fatalf("%q has something that can change its meaning (from %+v)", s.SQL, d)
			}
		}
	}
	if built < 500 {
		t.Errorf("only %d of 20000 random databases were accepted, so the check proves little", built)
	}
}
