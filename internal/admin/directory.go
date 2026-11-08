package admin

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/settings"
)

// KeyInfo is an API key as a list shows it. The secret is not kept anywhere it could be shown from: only its hash is stored.
type KeyInfo struct {
	Identifier  string
	UserID      uint64
	Username    string
	Type        auth.KeyType
	Description string
	AllowedIPs  []string
	// Access is the level per application resource, for a key of the Application API.
	Access     map[string]auth.Access
	LastUsedAt *time.Time
	CreatedAt  time.Time
}

// LocationInfo is a location as a list shows it.
type LocationInfo struct {
	ID    uint64
	Short string
	Long  string
	Nodes int64
}

// NodeInfo is a node as a list shows it; the token of a node is not part of it.
type NodeInfo struct {
	ID         uint64
	UUID       string
	Name       string
	FQDN       string
	Scheme     string
	Port       int
	LocationID uint64
	Servers    int64
}

// DirectoryStore is the part of the storage the commands that look at the panel, or take a key away, need.
type DirectoryStore interface {
	ListUsers(ctx context.Context) ([]User, error)
	// FindUser finds a user by a number (the id), an address (it has an @) or a user name.
	FindUser(ctx context.Context, ref string) (User, error)
	// ListKeys lists the keys of all users, or of the user with the id given.
	ListKeys(ctx context.Context, userID *uint64) ([]KeyInfo, error)
	// DeleteKey removes the key with the identifier and says which it was, or answers ErrNotFound.
	DeleteKey(ctx context.Context, identifier string) (KeyInfo, error)
	// DeleteKeys removes every key of the kind, or every key when kind is nil, with the secrets sealed for them, in one transaction, and says
	// how many there were.
	DeleteKeys(ctx context.Context, kind *auth.KeyType) (int64, error)
	ListLocations(ctx context.Context) ([]LocationInfo, error)
	ListNodes(ctx context.Context) ([]NodeInfo, error)
	// Setting returns the value stored for a key of settings.Keys, and whether there is one.
	Setting(ctx context.Context, key string) (value string, ok bool, err error)
}

// Directory shows what the panel holds and takes keys away.
type Directory struct{ store DirectoryStore }

// NewDirectory returns a Directory.
func NewDirectory(store DirectoryStore) (*Directory, error) {
	if store == nil {
		return nil, errors.New("admin: a directory needs a store")
	}
	return &Directory{store: store}, nil
}

var identifierPattern = regexp.MustCompile(`^(ptlc|ptla)_[A-Za-z0-9]{11}$`)

// Users lists the users.
func (d *Directory) Users(ctx context.Context) ([]User, error) { return d.store.ListUsers(ctx) }

// Keys lists the keys of everybody, or of the user the reference names (an id, an address or a user name).
func (d *Directory) Keys(ctx context.Context, userRef string) ([]KeyInfo, error) {
	if userRef == "" {
		return d.store.ListKeys(ctx, nil)
	}
	u, err := d.store.FindUser(ctx, userRef)
	if err != nil {
		return nil, err
	}
	return d.store.ListKeys(ctx, &u.ID)
}

// RevokeKey removes a key by its identifier: the first 16 characters of the key, which is all that is needed and all that is stored in
// the clear. The secret part is never asked for, so it never has to be typed into a terminal.
func (d *Directory) RevokeKey(ctx context.Context, identifier string) (KeyInfo, error) {
	if !identifierPattern.MatchString(identifier) {
		// What was typed is not repeated: it may be a whole key.
		return KeyInfo{}, fmt.Errorf("that is not the identifier of a key: it is %d characters and begins with ptlc_ or ptla_ (give the first %d characters of the key, not the whole key)", auth.IdentifierLength, auth.IdentifierLength)
	}
	return d.store.DeleteKey(ctx, identifier)
}

// NotConfirmedError is what RevokeKeys answers when it was not told to go ahead: nothing was removed, and Count is how many keys it
// would have removed.
type NotConfirmedError struct{ Count int64 }

func (e *NotConfirmedError) Error() string {
	return fmt.Sprintf("%d key(s) would be revoked and nothing was removed: repeat the command with --yes to revoke them", e.Count)
}

// RevokeKeys removes every key, or every key of a kind ("account" or "application", empty for both), because the raw value of the keys was
// recoverable with the old application key and they count as compromised (plan 13.3). It says how many went. Unless confirmed it removes
// nothing, and answers a *NotConfirmedError with the number that would go.
func (d *Directory) RevokeKeys(ctx context.Context, kind string, confirmed bool) (int64, error) {
	var only *auth.KeyType
	switch kind {
	case "":
	case "account":
		only = ptrTo(auth.KeyAccount)
	case "application":
		only = ptrTo(auth.KeyApplication)
	default:
		return 0, errors.New("the kind of a key is account or application")
	}
	if !confirmed {
		keys, err := d.store.ListKeys(ctx, nil)
		if err != nil {
			return 0, err
		}
		var n int64
		for _, k := range keys {
			if only == nil || k.Type == *only {
				n++
			}
		}
		return 0, &NotConfirmedError{Count: n}
	}
	return d.store.DeleteKeys(ctx, only)
}

func ptrTo[T any](v T) *T { return &v }

// Locations lists the locations.
func (d *Directory) Locations(ctx context.Context) ([]LocationInfo, error) {
	return d.store.ListLocations(ctx)
}

// Nodes lists the nodes.
func (d *Directory) Nodes(ctx context.Context) ([]NodeInfo, error) { return d.store.ListNodes(ctx) }

// Setting shows a setting of the panel; ok is false for one that was never set.
func (d *Directory) Setting(ctx context.Context, key string) (value string, ok bool, err error) {
	if !settings.Known(key) {
		return "", false, fmt.Errorf("%q is not a setting that can be read here (see settings list)", key)
	}
	return d.store.Setting(ctx, key)
}
