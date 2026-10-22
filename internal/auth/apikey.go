package auth

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/Helles01/raptor-panel/internal/ids"
)

// KeyType is the kind of an API key, stored in api_keys.key_type.
type KeyType int

const (
	// KeyAccount is a client API key (prefix ptlc_), used by a user for their own account.
	KeyAccount KeyType = 1
	// KeyApplication is an application API key (prefix ptla_), used for the admin API.
	KeyApplication KeyType = 2
)

const (
	// IdentifierLength is the length of the public part of a key, prefix included.
	IdentifierLength = 16
	// SecretLength is the length of the secret part of a key.
	SecretLength = 32
)

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_]{16}[A-Za-z0-9]{32}$`)

func (t KeyType) prefix() (string, error) {
	switch t {
	case KeyAccount:
		return "ptlc_", nil
	case KeyApplication:
		return "ptla_", nil
	}
	return "", fmt.Errorf("unsupported API key type %d", t)
}

// NewAPIKey generates the two parts of a key. The key given to its owner is identifier+secret; only the identifier
// may be shown again later.
func NewAPIKey(r io.Reader, t KeyType) (identifier, secret string, err error) {
	prefix, err := t.prefix()
	if err != nil {
		return "", "", err
	}
	rest, err := ids.RandomAlnum(r, IdentifierLength-len(prefix))
	if err != nil {
		return "", "", err
	}
	secret, err = ids.RandomAlnum(r, SecretLength)
	if err != nil {
		return "", "", err
	}
	return prefix + rest, secret, nil
}

// SplitKey splits a full key into identifier and secret. ok is false unless the key has exactly the expected shape.
func SplitKey(full string) (identifier, secret string, ok bool) {
	if !keyPattern.MatchString(full) {
		return "", "", false
	}
	return full[:IdentifierLength], full[IdentifierLength:], true
}

// Access is what an application key may do with one resource. The values are the bits stored in the r_* columns of
// api_keys: bit 1 is read, bit 2 is write.
type Access int

const (
	AccessNone      Access = 0
	AccessRead      Access = 1
	AccessReadWrite Access = 3
)

// Allows reports whether the access includes every bit of action (use AccessRead or the write bit, 2).
func (a Access) Allows(action Access) bool { return a&action != 0 }

// ParseAccess parses "none", "read" or "rw".
func ParseAccess(s string) (Access, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "none":
		return AccessNone, nil
	case "read":
		return AccessRead, nil
	case "rw", "readwrite":
		return AccessReadWrite, nil
	}
	return 0, fmt.Errorf("access %q must be none, read or rw", s)
}

// ApplicationResources lists the resources an application key has a level for, in the order of the r_* columns.
func ApplicationResources() []string {
	return []string{"servers", "nodes", "allocations", "users", "locations", "nests", "eggs", "database_hosts", "server_databases"}
}

// ParseResourceAccess turns "resource=level" arguments (for example "servers=rw") into a level per resource.
// Every resource is present in the result; those not mentioned get AccessNone.
func ParseResourceAccess(specs []string) (map[string]Access, error) {
	out := map[string]Access{}
	for _, r := range ApplicationResources() {
		out[r] = AccessNone
	}

	seen := map[string]bool{}
	for _, spec := range specs {
		name, level, found := strings.Cut(spec, "=")
		if !found {
			return nil, fmt.Errorf("%q must look like resource=level", spec)
		}
		name = strings.TrimSpace(name)
		if _, known := out[name]; !known {
			return nil, fmt.Errorf("unknown resource %q (one of %s)", name, strings.Join(ApplicationResources(), ", "))
		}
		if seen[name] {
			return nil, errors.New("resource " + name + " is given twice")
		}
		seen[name] = true

		access, err := ParseAccess(level)
		if err != nil {
			return nil, err
		}
		out[name] = access
	}
	return out, nil
}
