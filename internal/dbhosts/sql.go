package dbhosts

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Statement is one SQL statement for a database host. Identifiers cannot be bound, so they are checked and quoted here; the
// password is bound as an argument.
type Statement struct {
	SQL  string
	Args []any
}

// Database is what the SQL builders need to know about a server's database.
type Database struct {
	ServerID       uint64
	Name           string
	Username       string
	Remote         string
	MaxConnections int
}

const grantPrivileges = "SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, ALTER, REFERENCES, INDEX, LOCK TABLES, CREATE ROUTINE, ALTER ROUTINE, EXECUTE, CREATE TEMPORARY TABLES, CREATE VIEW, SHOW VIEW, EVENT, TRIGGER"

var (
	// Existing rows may hold any Unicode letter, which the reference allowed, so names are not restricted to ASCII here.
	nameChars     = regexp.MustCompile(`^[\p{L}\p{M}\p{N}_-]{1,64}$`)
	usernameChars = regexp.MustCompile(`^[\p{L}\p{M}\p{N}_-]{1,80}$`)
	passwordChars = regexp.MustCompile(`^[A-Za-z0-9!@=.+^]{8,64}$`)
)

// check makes sure that nothing in db can change the meaning of a statement, and that the database and user really belong to
// the server: a row that names someone else's database, or a system one, is refused rather than dropped.
func (d Database) check() error {
	id := strconv.FormatUint(d.ServerID, 10)
	switch {
	case !nameChars.MatchString(d.Name) || !strings.HasPrefix(d.Name, "s"+id+"_"):
		return errors.New("the database name is not one of this server's")
	case !usernameChars.MatchString(d.Username) || !strings.HasPrefix(d.Username, "u"+id+"_"):
		return errors.New("the database user is not one of this server's")
	case ValidateRemote(d.Remote) != nil:
		return errors.New("the remote address is not valid")
	case ValidateMaxConnections(d.MaxConnections) != nil:
		return errors.New("the connection limit is not valid")
	}
	return nil
}

func quote(identifier string) string { return "`" + identifier + "`" }

func (d Database) account() string { return quote(d.Username) + "@" + quote(d.Remote) }

// grantTarget is the database part of a GRANT. There "_" and "%" are wildcards, so without escaping the grant for a
// database named s1__x would also cover s11_x, a database of another server (C-028).
func (d Database) grantTarget() string {
	escaped := strings.NewReplacer(`_`, `\_`, `%`, `\%`).Replace(d.Name)
	return quote(escaped) + ".*"
}

func checkPassword(password string) error {
	if !passwordChars.MatchString(password) {
		return errors.New("the password does not come from the generator")
	}
	return nil
}

// Create returns the statements that create a server's database, its user, and the user's rights on that database only.
func Create(d Database, password string) ([]Statement, error) {
	if err := d.check(); err != nil {
		return nil, err
	}
	if err := checkPassword(password); err != nil {
		return nil, err
	}

	user := "CREATE USER " + d.account() + " IDENTIFIED BY ?"
	if d.MaxConnections > 0 {
		user += fmt.Sprintf(" WITH MAX_USER_CONNECTIONS %d", d.MaxConnections)
	}
	return []Statement{
		{SQL: "CREATE DATABASE IF NOT EXISTS " + quote(d.Name)},
		{SQL: user, Args: []any{password}},
		{SQL: "GRANT " + grantPrivileges + " ON " + d.grantTarget() + " TO " + d.account()},
	}, nil
}

// ChangePassword returns the statement that sets a new password. The user is not dropped and made again as the reference
// does, so a failure cannot leave the database without its user (C-029); it needs MariaDB 10.2 or MySQL 5.7.6 on the host.
func ChangePassword(d Database, password string) ([]Statement, error) {
	if err := d.check(); err != nil {
		return nil, err
	}
	if err := checkPassword(password); err != nil {
		return nil, err
	}
	return []Statement{{SQL: "ALTER USER " + d.account() + " IDENTIFIED BY ?", Args: []any{password}}}, nil
}

// Drop returns the statements that remove a server's database and its user. They are also what undoes a failed Create.
func Drop(d Database) ([]Statement, error) {
	if err := d.check(); err != nil {
		return nil, err
	}
	return []Statement{
		{SQL: "DROP DATABASE IF EXISTS " + quote(d.Name)},
		{SQL: "DROP USER IF EXISTS " + d.account()},
	}, nil
}
