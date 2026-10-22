// Package sftp holds the rules for the node's question "may this person connect by SFTP to this server": reading the request,
// which counters limit guessing, the decision, and the answer. It touches no database; the handler looks up the user, the
// server and the registered keys and passes them in.
package sftp

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Helles01/raptor-panel/internal/auth/lockout"
	"github.com/Helles01/raptor-panel/internal/permissions"
	"github.com/Helles01/raptor-panel/internal/servers"
	"github.com/Helles01/raptor-panel/internal/sshkeys"
)

// The two ways to sign in.
const (
	TypePassword  = "password"
	TypePublicKey = "public_key"
)

const maxCredentialLength = sshkeys.MaxLength

var (
	// ErrBadServerID is answered with 400; the node checks the form of the user name itself, so this means a bug or a stranger.
	ErrBadServerID = errors.New("No valid server identifier was included in the request.") //nolint:staticcheck // the reference's text
	// ErrRejected is answered with 403 for every kind of failed sign-in, so that nothing says which part was wrong.
	ErrRejected = errors.New("Authorization credentials were not correct, please try again.") //nolint:staticcheck // the reference's text
	// ErrDenied is answered with 403 for a user who has no SFTP right on the server.
	ErrDenied = errors.New("You do not have permission to access SFTP for this server.") //nolint:staticcheck // the reference's text
)

var (
	serverIDPattern = regexp.MustCompile(`^[A-Za-z0-9]{8}$`)
	usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,191}$`)
)

// Request is what the node posts to /api/remote/sftp/auth. The node also sends a session id and the client's version, which
// nothing here needs.
type Request struct {
	Type     string `json:"type"`
	Username string `json:"username"`
	Password string `json:"password"`
	// IP is the address of the SFTP client as the node saw it, with its port ("203.0.113.9:51234", "[2001:db8::1]:22").
	IP string `json:"ip"`
}

// Method checks the request and returns how the user signs in: with a password unless the type says a public key (in which
// case Password holds the key).
func (r Request) Method() (string, error) {
	switch r.Type {
	case "", TypePassword:
		if err := r.checkCredential(); err != nil {
			return "", err
		}
		return TypePassword, nil
	case TypePublicKey:
		if err := r.checkCredential(); err != nil {
			return "", err
		}
		return TypePublicKey, nil
	}
	return "", errors.New("the type must be password or public_key")
}

func (r Request) checkCredential() error {
	if r.Username == "" || len(r.Username) > 255 || r.Password == "" || len(r.Password) > maxCredentialLength {
		return errors.New("the username and password are required")
	}
	return nil
}

// Login is a user name and the server they asked for.
type Login struct {
	Username string
	// Server is the short identifier of the server: the first eight characters of its UUID.
	Server string
}

// ParseUsername splits "<user name>.<server id>" at its last dot, since a user name may contain dots itself.
func ParseUsername(value string) (Login, error) {
	i := strings.LastIndexByte(value, '.')
	if i < 0 || !serverIDPattern.MatchString(value[i+1:]) {
		return Login{}, ErrBadServerID
	}
	username := value[:i]
	if !usernamePattern.MatchString(username) {
		return Login{}, ErrRejected
	}
	return Login{Username: username, Server: value[i+1:]}, nil
}

// Throttle is one counter that limits failed sign-ins: at most Limit failures per Window for Key.
type Throttle = lockout.Counter

// Throttles are the counters a sign-in attempt is checked against, and counted in when it fails. The reference has one
// counter per server and per address of the request, but the request comes from the node, so that address is the node's
// and one person guessing blocks everybody who uses the server. Here the counters follow the user and the client's own
// address, and a user is also limited across all addresses, so that guessing from many addresses does not escape.
func Throttles(username, clientAddr string, nodeID uint64) []Throttle {
	user := strings.ToLower(username)
	client := lockout.Client(clientAddr, "node-"+strconv.FormatUint(nodeID, 10))
	return []Throttle{
		{Key: "sftp|user-client|" + user + "|" + client, Limit: 5, Window: time.Minute},
		{Key: "sftp|user|" + user, Limit: 30, Window: 10 * time.Minute},
		{Key: "sftp|client|" + client, Limit: 60, Window: 10 * time.Minute},
	}
}

// KeyOutcome is what became of a public key a client offered.
type KeyOutcome int

const (
	// KeyMatch: the key is one the user registered.
	KeyMatch KeyOutcome = iota
	// KeyUnknown: a valid key that is not registered. It is refused but not counted as a failure, since clients offer every
	// key they have and the proof of owning it comes afterwards, from the node's SSH library.
	KeyUnknown
	// KeyInvalid: text that is not a usable public key. Refused and counted.
	KeyInvalid
)

// CheckKey compares an offered key with the fingerprints a user registered.
func CheckKey(offered string, registered []string) KeyOutcome {
	key, err := sshkeys.Parse(offered)
	if err != nil {
		return KeyInvalid
	}
	for _, fp := range registered {
		if fp == key.Fingerprint {
			return KeyMatch
		}
	}
	return KeyUnknown
}

// User is the signed-in user, as the decision needs them.
type User struct {
	ID    uint64
	UUID  string
	Admin bool
}

// Access is the server, and what the user holds on it.
type Access struct {
	ServerUUID string
	OwnerID    uint64
	State      servers.State
	// Subuser holds the permissions of the user's subuser entry on the server, nil without one.
	Subuser []string
}

// Grant is the answer to the node when the sign-in is allowed.
type Grant struct {
	User        string   `json:"user"`
	Server      string   `json:"server"`
	Permissions []string `json:"permissions"`
}

// Authorize decides, after the credentials were accepted, whether the user may use SFTP on the server and in what state it is.
// Administrators and the owner always may; a subuser needs file.sftp. A denial is ErrDenied, which the caller logs as an
// activity; a server that cannot be used now is a *servers.StateError (409).
func Authorize(u User, a Access) (Grant, error) {
	// A user id of 0 is a bug in the caller, never an owner.
	owner := u.ID != 0 && a.OwnerID == u.ID
	if !u.Admin && !owner && !permissions.Has(a.Subuser, permissions.FileSFTP) {
		return Grant{}, ErrDenied
	}
	if err := a.State.Validate(); err != nil {
		return Grant{}, fmt.Errorf("sftp access: %w", err)
	}
	return Grant{User: u.UUID, Server: a.ServerUUID, Permissions: permissions.Effective(u.Admin, owner, a.Subuser)}, nil
}
