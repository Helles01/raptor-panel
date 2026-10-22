// Package dbhosts holds the rules for the databases the panel creates for servers on external database hosts: names,
// generated credentials, which host to use, and the SQL that is run there. Nothing here touches a database.
package dbhosts

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Helles01/raptor-panel/internal/ids"
)

const (
	// MaxDatabaseNameLength includes the "s123_" prefix.
	MaxDatabaseNameLength = 48
	PasswordLength        = 24
	// MaxConnectionsLimit bounds the per-user connection limit of a database.
	MaxConnectionsLimit = 1 << 20

	specialCharacters = "!@=.+^"
	usernameSuffix    = 10
)

var (
	ErrFeatureDisabled = errors.New("Client database creation is not enabled in this Panel.")                                   //nolint:staticcheck // the reference's text
	ErrTooMany         = errors.New("Operation aborted: creating a new database would put this server over the defined limit.") //nolint:staticcheck // the reference's text
	ErrNoSuitableHost  = errors.New("No database host was found that meets the requirements for this server.")                  //nolint:staticcheck // the reference's text
)

var (
	namePartPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{3,48}$`)
	remotePattern   = regexp.MustCompile(`^[A-Za-z0-9_\-/.%:]{1,255}$`)
	hostPattern     = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,191}$`)
)

// UniqueName makes the name of a new database for a server, "s<server id>_<name>" cut to 48 characters. The name must be
// 3 to 48 ASCII letters, digits, dashes or underscores: the reference accepts any Unicode letter and then cuts the name by
// bytes, which can split a character (C-028).
func UniqueName(name string, serverID uint64) (string, error) {
	if !namePartPattern.MatchString(name) {
		return "", errors.New("the database name must be 3 to 48 letters, digits, dashes or underscores")
	}
	prefix := "s" + strconv.FormatUint(serverID, 10) + "_"
	// A uint64 id has at most 20 digits, so there is always room for at least 26 characters of the name.
	room := MaxDatabaseNameLength - len(prefix)
	return prefix + name[:min(len(name), room)], nil
}

// Username makes the database user of a server: "u<server id>_" and ten random letters and digits.
func Username(serverID uint64, r io.Reader) (string, error) {
	suffix, err := ids.RandomAlnum(r, usernameSuffix)
	if err != nil {
		return "", fmt.Errorf("generate username: %w", err)
	}
	return "u" + strconv.FormatUint(serverID, 10) + "_" + suffix, nil
}

// Password makes a database password: 24 letters and digits with between 2 and 6 of the characters ! @ = . + ^ in distinct
// places. None of the characters can end a quoted SQL string, which the SQL builders also insist on.
func Password(r io.Reader) (string, error) {
	base, err := ids.RandomAlnum(r, PasswordLength)
	if err != nil {
		return "", fmt.Errorf("generate password: %w", err)
	}
	b := []byte(base)

	extra, err := randomBelow(r, 5)
	if err != nil {
		return "", err
	}
	positions := make([]int, PasswordLength)
	for i := range positions {
		positions[i] = i
	}
	for i := 0; i < 2+extra; i++ {
		j, err := randomBelow(r, PasswordLength-i)
		if err != nil {
			return "", err
		}
		positions[i], positions[i+j] = positions[i+j], positions[i]

		c, err := randomBelow(r, len(specialCharacters))
		if err != nil {
			return "", err
		}
		b[positions[i]] = specialCharacters[c]
	}
	return string(b), nil
}

// randomBelow returns a uniform number from 0 to n-1 (n up to 256) without modulo bias.
func randomBelow(r io.Reader, n int) (int, error) {
	limit := 256 - 256%n
	var b [1]byte
	for {
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, fmt.Errorf("read random bytes: %w", err)
		}
		if int(b[0]) < limit {
			return int(b[0]) % n, nil
		}
	}
}

// ValidateRemote checks the address a database user may connect from: letters, digits and _ - / . % : (a host name, an
// address with % as a wildcard, an address with a netmask, or an IPv6 address).
func ValidateRemote(remote string) error {
	if !remotePattern.MatchString(remote) {
		return errors.New("remote must be 1 to 255 characters from letters, digits and _ - / . % :")
	}
	return nil
}

// ValidateMaxConnections checks the per-user connection limit; 0 means no limit.
func ValidateMaxConnections(n int) error {
	if n < 0 || n > MaxConnectionsLimit {
		return fmt.Errorf("max connections must be between 0 and %d", MaxConnectionsLimit)
	}
	return nil
}

// Host is a database host as an administrator enters it.
type Host struct {
	Name     string
	Host     string
	Port     int
	Username string
}

// Validate checks a database host. The panel connects with a configuration, not a connection string, so the user name may be
// any printable text.
func (h Host) Validate() error {
	switch {
	case strings.TrimSpace(h.Name) == "" || utf8.RuneCountInString(h.Name) > 191 || strings.ContainsFunc(h.Name, isControl):
		return errors.New("the name must be 1 to 191 characters without control characters")
	case !hostPattern.MatchString(h.Host):
		return errors.New("the host must be 1 to 191 letters, digits, dots, dashes or underscores")
	case h.Port < 1 || h.Port > 65535:
		return errors.New("the port must be between 1 and 65535")
	case h.Username == "" || utf8.RuneCountInString(h.Username) > 32 || !utf8.ValidString(h.Username) || strings.ContainsFunc(h.Username, isControl):
		return errors.New("the user name must be 1 to 32 characters without control characters")
	}
	return nil
}

func isControl(r rune) bool { return r < ' ' || r == 0x7f }

// CheckCanCreate applies the checks before a client creates a database. limit is the server's database limit, nil for none.
func CheckCanCreate(featureEnabled bool, limit *int64, current int64) error {
	if !featureEnabled {
		return ErrFeatureDisabled
	}
	if limit != nil && current >= *limit {
		return ErrTooMany
	}
	return nil
}

// HostChoice is a database host a new database may be created on.
type HostChoice struct {
	ID     uint64
	NodeID *uint64
}

// ChooseHost picks a host at random among those linked to the server's node. With none, any host is used when allowRandom
// is set (hosts linked to no node only ever qualify that way, as in the reference).
func ChooseHost(hosts []HostChoice, nodeID uint64, allowRandom bool, r io.Reader) (HostChoice, error) {
	var linked []HostChoice
	for _, h := range hosts {
		if h.NodeID != nil && *h.NodeID == nodeID {
			linked = append(linked, h)
		}
	}
	pool := linked
	if len(pool) == 0 && allowRandom {
		pool = hosts
	}
	if len(pool) == 0 {
		return HostChoice{}, ErrNoSuitableHost
	}

	i, err := randomBelow(r, len(pool))
	if err != nil {
		return HostChoice{}, err
	}
	return pool[i], nil
}
