// Package accountapi serves the routes with which a signed-in user manages their own account: the e-mail address, the password,
// two-factor authentication, API keys and SSH keys. The rules are in internal/account, internal/auth and internal/sshkeys; this package
// puts them in order and talks to the storage, the sessions, the nodes and the activity log through the interfaces below.
package accountapi

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/account"
	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/auth/lockout"
	"github.com/Helles01/raptor-panel/internal/auth/session"
)

// The store answers with these.
var (
	// ErrNotFound: there is no such key.
	ErrNotFound = errors.New("accountapi: not found")
	// ErrEmailTaken: another account has the address.
	ErrEmailTaken = errors.New("accountapi: e-mail address taken")
	// ErrKeyExists: the user has the SSH key already (another request was first).
	ErrKeyExists = errors.New("accountapi: SSH key exists")
)

// Account is the user as far as these routes go.
type Account struct {
	ID           uint64
	UUID         string
	Email        string
	PasswordHash string
	UsesTOTP     bool
	// TOTPSecret is the secret in the clear, opened by the store from its sealed form; empty when none was asked for.
	TOTPSecret string
}

// KeyRecord is an API key of the user, as shown: the secret is never stored in a form that can be shown again.
type KeyRecord struct {
	ID          uint64
	Identifier  string
	Description string
	AllowedIPs  []string
	LastUsedAt  *time.Time
	CreatedAt   time.Time
}

// NewKey is an API key to store.
type NewKey struct {
	Identifier string
	// SecretHash is auth.HashKeySecret of the secret.
	SecretHash  string
	Description string
	AllowedIPs  []string
}

// SSHKeyRecord is an SSH key of the user.
type SSHKeyRecord struct {
	ID          uint64
	Name        string
	Fingerprint string
	PublicKey   string
	CreatedAt   time.Time
}

// Store is the part of the storage the routes need. Every method works on the account of one user.
type Store interface {
	Account(ctx context.Context, userID uint64) (Account, error)
	// SetEmail stores the new address, or answers ErrEmailTaken. The password-reset tokens of the old address go in the same
	// transaction: one that was asked for before must not work for an address the user no longer has.
	SetEmail(ctx context.Context, userID uint64, oldEmail, newEmail string) error
	// SetPassword stores the hash, and deletes the password-reset tokens of the account, in one transaction.
	SetPassword(ctx context.Context, userID uint64, hash string) error
	// SetTOTPSecret stores the secret (sealed), replacing one that was never confirmed.
	SetTOTPSecret(ctx context.Context, userID uint64, secret string) error
	// EnableTOTP, in one transaction, deletes the old recovery tokens, stores the hashes of the new ones and switches two-factor
	// authentication on, with usedAt as the time of the code that was used.
	EnableTOTP(ctx context.Context, userID uint64, hashes []string, usedAt time.Time) error
	// DisableTOTP switches it off and deletes the secret and the recovery tokens.
	DisableTOTP(ctx context.Context, userID uint64) error

	APIKeys(ctx context.Context, userID uint64) ([]KeyRecord, error)
	// CreateKey stores a key in one transaction in which the user's keys are locked: check is called with how many they have, and an
	// error ends the request and is returned as it is.
	CreateKey(ctx context.Context, userID uint64, k NewKey, check func(count int) error) (KeyRecord, error)
	// DeleteKey removes an account key of the user by its identifier and returns it, or answers ErrNotFound.
	DeleteKey(ctx context.Context, userID uint64, identifier string) (KeyRecord, error)

	SSHKeys(ctx context.Context, userID uint64) ([]SSHKeyRecord, error)
	HasFingerprint(ctx context.Context, userID uint64, fingerprint string) (bool, error)
	// AddSSHKey stores a key, or answers ErrKeyExists when the user has the fingerprint already.
	AddSSHKey(ctx context.Context, userID uint64, k SSHKeyRecord) (SSHKeyRecord, error)
	// RemoveSSHKey removes the key with the fingerprint and returns it; the bool says whether there was one.
	RemoveSSHKey(ctx context.Context, userID uint64, fingerprint string) (SSHKeyRecord, bool, error)
}

// Nodes tells the nodes to forget what they hold for a user: tokens for the console and files, and SFTP sessions.
type Nodes interface {
	DeauthorizeUser(ctx context.Context, userID uint64) error
}

// Entry is an entry of the activity log about an account.
type Entry struct {
	Event string
	// UserID is the user the entry is about and who did it. APIKeyID is the key the call came with, if any; SubjectID the key or SSH
	// key the entry is about, 0 for none.
	UserID     uint64
	APIKeyID   *uint64
	SubjectID  uint64
	IP         string
	Properties string
}

// ActivityLog stores activity entries.
type ActivityLog interface {
	Log(ctx context.Context, e Entry) error
}

// Deps are everything the endpoints need.
type Deps struct {
	Store    Store
	Sessions *session.Manager
	Nodes    Nodes
	Activity ActivityLog
	// Failures counts the wrong passwords and codes, and the changes of the address.
	Failures *middleware.Tracker
	// Issuer is the name of the panel shown in authenticator apps.
	Issuer string
	// Location is the time zone of the panel, UTC when nil.
	Location *time.Location
	Logger   *slog.Logger
	// Now and Random default to the clock and crypto/rand.
	Now    func() time.Time
	Random io.Reader
}

// Handlers are the endpoints.
type Handlers struct {
	d Deps
}

// New checks the dependencies and returns the endpoints.
func New(d Deps) (*Handlers, error) {
	if d.Store == nil || d.Sessions == nil || d.Nodes == nil || d.Activity == nil || d.Failures == nil || d.Logger == nil {
		return nil, errors.New("accountapi: the store, the sessions, the nodes, the activity log, the failure tracker and a logger are required")
	}
	if d.Issuer == "" {
		return nil, errors.New("accountapi: the name of the panel is required")
	}
	if d.Location == nil {
		d.Location = time.UTC
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Random == nil {
		d.Random = rand.Reader
	}
	return &Handlers{d: d}, nil
}

// Register adds the routes below /api/client/account. The group must already have the session loaded.
func (h *Handlers) Register(g api.Group) {
	a := g.Sub("/api/client/account")
	a.PUT("/email", h.UpdateEmail, api.Authenticated())
	a.PUT("/password", h.UpdatePassword, api.Authenticated())

	// What a user needs to switch two-factor authentication on is open to one who has not, whatever the panel requires.
	tf := api.Authenticated().WithoutTwoFactor()
	a.GET("/two-factor", h.TwoFactorSetup, tf)
	a.POST("/two-factor", h.TwoFactorEnable, tf)
	a.POST("/two-factor/disable", h.TwoFactorDisable, tf)

	a.GET("/api-keys", h.ListKeys, api.Authenticated())
	a.POST("/api-keys", h.CreateKey, api.Authenticated())
	a.DELETE("/api-keys/:identifier", h.DeleteKey, api.Authenticated())

	a.GET("/ssh-keys", h.ListSSHKeys, api.Authenticated())
	a.POST("/ssh-keys", h.AddSSHKey, api.Authenticated())
	a.POST("/ssh-keys/remove", h.RemoveSSHKey, api.Authenticated())
}

// maxBody is the largest body these routes read: a public key is at most 16 KiB.
const maxBody = 64 << 10

// atom is how the reference writes a time (Carbon's toAtomString): the offset as +00:00, never Z.
const atom = "2006-01-02T15:04:05-07:00"

// caller returns who is calling and their account. A route wired without the authorizer is refused.
func (h *Handlers) caller(c echo.Context) (authz.Principal, Account, error) {
	p, ok := authz.From(c)
	if !ok {
		return authz.Principal{}, Account{}, apierr.Forbidden("This action is unauthorized.")
	}
	a, err := h.d.Store.Account(c.Request().Context(), p.User.ID)
	if err != nil {
		return authz.Principal{}, Account{}, storeError(err)
	}
	return p, a, nil
}

func storeError(err error) error {
	if errors.Is(err, ErrNotFound) {
		return apierr.NotFound()
	}
	return apierr.Internal(err)
}

// log writes an entry about the account. A failure is logged and never reaches the person, whose action has happened.
func (h *Handlers) log(c echo.Context, p authz.Principal, event string, subject uint64, props string) {
	e := Entry{Event: event, UserID: p.User.ID, SubjectID: subject, IP: c.RealIP(), Properties: props}
	if p.Key != nil {
		id := p.Key.ID
		e.APIKeyID = &id
	}
	if err := h.d.Activity.Log(c.Request().Context(), e); err != nil {
		h.d.Logger.Error("activity entry not stored", "event", event, "error", err)
	}
}

// confirmCounters are the counters of the requests that ask for the password again, and whether one of them is used up.
func (h *Handlers) confirmCounters(c echo.Context, p authz.Principal) ([]lockout.Counter, error) {
	counters, err := account.ConfirmCounters(p.User.ID, c.RealIP())
	if err != nil {
		return nil, apierr.BadRequest("The address of the client could not be read.")
	}
	if blocked, retry := h.d.Failures.Blocked(counters); blocked {
		return nil, apierr.TooManyRequests(retry)
	}
	return counters, nil
}
