package dbhosts

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// maxHostPassword is longer than any password a host takes for an account, and short enough to keep a mistake out of the table.
const maxHostPassword = 128

// ValidateHostPassword checks the password of the account the panel manages a host with. It is only ever put in a connection
// setting, never in a statement, so any printable text will do; the reference takes anything, and an empty password is allowed for a
// host on the same machine that has none.
func ValidateHostPassword(password string) error {
	if utf8.RuneCountInString(password) > maxHostPassword || !utf8.ValidString(password) || strings.ContainsFunc(password, isControl) {
		return errors.New("the password must be at most 128 characters without control characters")
	}
	return nil
}

// ChangeOwnPassword is the statement with which the account the panel signed in with changes its own password. It names no account:
// the host decides which one it is, so it is right whatever the account's address part is ("panel"@"%" or "panel"@"10.0.0.7").
func ChangeOwnPassword(password string) ([]Statement, error) {
	if err := checkPassword(password); err != nil {
		return nil, err
	}
	return []Statement{{SQL: "ALTER USER CURRENT_USER() IDENTIFIED BY ?", Args: []any{password}}}, nil
}
