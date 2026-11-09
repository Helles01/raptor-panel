// Package admin is what the administrator's command line does, apart from reading arguments and writing to a terminal: making a user, an
// API key, a location, a node, a nest or an egg, changing a setting. Each operation checks its input with the rules of the domain
// packages, makes whatever must be random, and hands the finished record to a store. A secret that is made here (a password, the
// secret of a key, the token of a node) is given back once, in the result, and nowhere else.
package admin

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/eggs"
	"github.com/Helles01/raptor-panel/internal/ids"
	"github.com/Helles01/raptor-panel/internal/nodes"
	"github.com/Helles01/raptor-panel/internal/settings"
	"github.com/Helles01/raptor-panel/internal/users"
)

// The store answers with these.
var (
	// ErrNotFound: the user, nest or location asked for is not there.
	ErrNotFound = errors.New("admin: not found")
	// ErrAmbiguous: the reference to a user names more than one.
	ErrAmbiguous = errors.New("admin: more than one match")
)

// TakenError is what a store answers when a unique key refuses a value; Field is its name in the request.
type TakenError struct{ Field string }

func (e *TakenError) Error() string {
	return "the " + strings.ReplaceAll(e.Field, "_", " ") + " is already in use"
}

// User is a user as the commands need to know them.
type User struct {
	ID       uint64
	UUID     string
	Username string
	Email    string
	Admin    bool
}

// NewKey is an API key to store. Only the hash of the secret is kept.
type NewKey struct {
	UserID      uint64
	Type        auth.KeyType
	Identifier  string
	SecretHash  string
	Description string
	AllowedIPs  []string
	// Access is the level per application resource; empty for a key of an account.
	Access map[string]auth.Access
}

// NewNode is a node to store; the store seals the token.
type NewNode struct {
	UUID        string
	Settings    nodes.Settings
	Credentials nodes.Credentials
}

// NewEgg is an egg to store, with the variables it came with.
type NewEgg struct {
	UUID   string
	NestID uint64
	Egg    eggs.Egg
}

// Store is the part of the storage the commands need.
type Store interface {
	CreateUser(ctx context.Context, u users.Record) (uint64, error)
	// FindUser finds a user by a number (the id), an address (it has an @) or a user name. Names are compared without regard to case.
	FindUser(ctx context.Context, ref string) (User, error)
	// DeleteUser removes a user. check is called with the number of servers they own, inside the transaction in which they are counted.
	DeleteUser(ctx context.Context, id uint64, check func(ownedServers int64) error) error
	// DisableTwoFactor removes the second factor of a user, with its recovery tokens.
	DisableTwoFactor(ctx context.Context, id uint64) error
	// TwoFactorUsers counts the users who have a second factor: switched on, or with a secret kept for them.
	TwoFactorUsers(ctx context.Context) (int64, error)
	// DisableAllTwoFactor removes the second factor of every user, with the secrets kept for them and every recovery token, in one transaction, and says how many
	// users had one.
	DisableAllTwoFactor(ctx context.Context) (int64, error)
	// CreateKey stores a key. check is called with the number of keys the user has, inside the transaction in which they are counted.
	CreateKey(ctx context.Context, k NewKey, check func(count int) error) error
	CreateLocation(ctx context.Context, short, long string) (uint64, error)
	// CreateNode stores a node, or answers ErrNotFound for a location that is not there.
	CreateNode(ctx context.Context, n NewNode) (uint64, error)
	CreateNest(ctx context.Context, uuid, name, description, author string) (uint64, error)
	// ImportEgg stores an egg and its variables, or answers ErrNotFound for a nest that is not there.
	ImportEgg(ctx context.Context, e NewEgg) (uint64, error)
	SetSetting(ctx context.Context, key, value string) error
	// SetMailPassword seals the password of the mail server. The row of the setting is made if there is none; a row that came from the old panel keeps its
	// old value, which is not touched.
	SetMailPassword(ctx context.Context, password string) error
	// ClearMailPassword removes the sealed password, so that mail is sent without one. It is not an error that there is none.
	ClearMailPassword(ctx context.Context) error
}

// Service does the operations.
type Service struct {
	store     Store
	random    io.Reader
	appURL    string
	languages []string
}

// New returns a Service. appURL is the address of the panel, which goes into the configuration of a node; languages are the ones an
// account may use. random defaults to crypto/rand.Reader.
func New(store Store, appURL string, languages []string, random io.Reader) (*Service, error) {
	if store == nil || appURL == "" {
		return nil, errors.New("admin: a store and the address of the panel are required")
	}
	if random == nil {
		random = rand.Reader
	}
	return &Service{store: store, random: random, appURL: appURL, languages: languages}, nil
}

// UserInput is what is asked for a new user. A password that is left out is made.
type UserInput struct {
	Email, Username, FirstName, LastName string
	Password                             string
	Language                             string
	Admin                                bool
}

// CreatedUser is a user that was made. Password is the one the account has, given only here, and only when it was made.
type CreatedUser struct {
	ID       uint64
	UUID     string
	Username string
	Password string
}

// generatedPasswordLength is the length of the password made for a user who was not given one.
const generatedPasswordLength = 24

// CreateUser makes a user.
func (s *Service) CreateUser(ctx context.Context, in UserInput) (CreatedUser, error) {
	made := ""
	if in.Password == "" {
		p, err := ids.RandomAlnum(s.random, generatedPasswordLength)
		if err != nil {
			return CreatedUser{}, fmt.Errorf("make a password: %w", err)
		}
		in.Password, made = p, p
	}
	rec, err := users.PrepareAccount(users.Input{
		Email: in.Email, Username: in.Username, FirstName: in.FirstName, LastName: in.LastName, Password: in.Password, Language: in.Language, Admin: in.Admin,
	}, s.languages, s.random)
	if err != nil {
		return CreatedUser{}, err
	}
	id, err := s.store.CreateUser(ctx, rec)
	if err != nil {
		return CreatedUser{}, err
	}
	return CreatedUser{ID: id, UUID: rec.UUID, Username: rec.Username, Password: made}, nil
}

// DeleteUser removes a user who owns no servers.
func (s *Service) DeleteUser(ctx context.Context, ref string) (User, error) {
	u, err := s.store.FindUser(ctx, ref)
	if err != nil {
		return User{}, err
	}
	if err := s.store.DeleteUser(ctx, u.ID, users.CheckCanDelete); err != nil {
		return User{}, err
	}
	return u, nil
}

// DisableTwoFactor removes the second factor of a user who lost their device.
func (s *Service) DisableTwoFactor(ctx context.Context, ref string) (User, error) {
	u, err := s.store.FindUser(ctx, ref)
	if err != nil {
		return User{}, err
	}
	return u, s.store.DisableTwoFactor(ctx, u.ID)
}

// KeyInput is what is asked for a new API key. Access ("servers=rw", "nodes=read") is for a key of the Application API.
type KeyInput struct {
	User        string
	Application bool
	Description string
	Access      []string
	AllowedIPs  []string
}

// CreatedKey is a key that was made. Token is the whole key, which is shown here and cannot be found out again.
type CreatedKey struct {
	Identifier string
	Token      string
}

// CreateKey makes an API key for a user.
func (s *Service) CreateKey(ctx context.Context, in KeyInput) (CreatedKey, error) {
	if err := auth.ValidateKeyDescription(in.Description); err != nil {
		return CreatedKey{}, err
	}
	if _, err := auth.ParseAllowedIPs(in.AllowedIPs); err != nil {
		return CreatedKey{}, err
	}
	kind := auth.KeyAccount
	var access map[string]auth.Access
	var err error
	if in.Application {
		kind = auth.KeyApplication
		if access, err = auth.ParseResourceAccess(in.Access); err != nil {
			return CreatedKey{}, err
		}
	} else if len(in.Access) > 0 {
		return CreatedKey{}, errors.New("levels on resources belong to a key of the Application API")
	}
	u, err := s.store.FindUser(ctx, in.User)
	if err != nil {
		return CreatedKey{}, err
	}
	if in.Application && !u.Admin {
		return CreatedKey{}, errors.New("a key of the Application API is for an administrator")
	}

	identifier, secret, err := auth.NewAPIKey(s.random, kind)
	if err != nil {
		return CreatedKey{}, fmt.Errorf("make a key: %w", err)
	}
	k := NewKey{UserID: u.ID, Type: kind, Identifier: identifier, SecretHash: auth.HashKeySecret(secret), Description: strings.TrimSpace(in.Description), AllowedIPs: in.AllowedIPs, Access: access}
	if err := s.store.CreateKey(ctx, k, auth.CheckCanCreateKey); err != nil {
		return CreatedKey{}, err
	}
	return CreatedKey{Identifier: identifier, Token: identifier + secret}, nil
}

// CreateLocation makes a location.
func (s *Service) CreateLocation(ctx context.Context, short, long string) (uint64, error) {
	if err := nodes.ValidateLocation(short, long); err != nil {
		return 0, err
	}
	return s.store.CreateLocation(ctx, short, long)
}

// CreatedNode is a node that was made, with the configuration its Wings needs (config.yml). The file holds the token of the node, so it
// is shown here once and kept nowhere else.
type CreatedNode struct {
	ID     uint64
	UUID   string
	Config []byte
}

// CreateNode makes a node.
func (s *Service) CreateNode(ctx context.Context, node nodes.Settings) (CreatedNode, error) {
	if err := node.Validate(); err != nil {
		return CreatedNode{}, err
	}
	uuid, err := ids.UUIDv4(s.random)
	if err != nil {
		return CreatedNode{}, fmt.Errorf("make a UUID: %w", err)
	}
	creds, err := nodes.NewCredentials(s.random)
	if err != nil {
		return CreatedNode{}, err
	}
	config, err := nodes.WingsConfig(nodes.WingsNode{
		UUID: uuid, FQDN: node.FQDN, Scheme: node.Scheme, BehindProxy: node.BehindProxy, DaemonBase: node.DaemonBase,
		DaemonSFTP: node.DaemonSFTP, DaemonListen: node.DaemonListen, UploadSize: node.UploadSize,
	}, creds, s.appURL)
	if err != nil {
		return CreatedNode{}, err
	}
	id, err := s.store.CreateNode(ctx, NewNode{UUID: uuid, Settings: node, Credentials: creds})
	if err != nil {
		return CreatedNode{}, err
	}
	return CreatedNode{ID: id, UUID: uuid, Config: config}, nil
}

// CreateNest makes a nest. The author is an address, as in an egg file.
func (s *Service) CreateNest(ctx context.Context, name, description, author string) (uint64, error) {
	if n := len([]rune(strings.TrimSpace(name))); n < 1 || n > 191 {
		return 0, errors.New("the name of a nest must be 1 to 191 characters")
	}
	if err := users.ValidateEmail(author); err != nil {
		return 0, errors.New("the author of a nest must be an e-mail address")
	}
	if len([]rune(description)) > 10000 {
		return 0, errors.New("the description of a nest must be at most 10000 characters")
	}
	uuid, err := ids.UUIDv4(s.random)
	if err != nil {
		return 0, fmt.Errorf("make a UUID: %w", err)
	}
	return s.store.CreateNest(ctx, uuid, strings.TrimSpace(name), description, author)
}

// ImportedEgg is an egg that was made from a file.
type ImportedEgg struct {
	ID        uint64
	Name      string
	Variables int
}

// ImportEgg reads an egg file and stores the egg in a nest.
func (s *Service) ImportEgg(ctx context.Context, nestID uint64, file io.Reader) (ImportedEgg, error) {
	egg, err := eggs.Parse(file)
	if err != nil {
		return ImportedEgg{}, err
	}
	uuid, err := ids.UUIDv4(s.random)
	if err != nil {
		return ImportedEgg{}, fmt.Errorf("make a UUID: %w", err)
	}
	id, err := s.store.ImportEgg(ctx, NewEgg{UUID: uuid, NestID: nestID, Egg: egg})
	if err != nil {
		return ImportedEgg{}, err
	}
	return ImportedEgg{ID: id, Name: egg.Name, Variables: len(egg.Variables)}, nil
}

// SetSetting changes a setting of the panel. Only the settings in internal/settings can be changed.
func (s *Service) SetSetting(ctx context.Context, key, value string) error {
	if err := settings.Validate(key, value); err != nil {
		return err
	}
	return s.store.SetSetting(ctx, key, value)
}

// maxMailPasswordLength is the longest password of a mail server that is accepted.
const maxMailPasswordLength = 1000

// SetMailPassword sets the password the panel signs in to the mail server with. It is kept sealed, apart from the settings that can be read and
// listed. The password is not repeated in an error: it may be one that was typed wrongly into the wrong place.
func (s *Service) SetMailPassword(ctx context.Context, password string) error {
	if password == "" {
		return errors.New("the password of the mail server is empty (to remove it, clear it)")
	}
	if n := len([]rune(password)); n > maxMailPasswordLength {
		return fmt.Errorf("the password of the mail server is %d characters long, at most %d are accepted", n, maxMailPasswordLength)
	}
	// A line end or another control character would end the line of the SMTP command that carries the password.
	for _, r := range password {
		if unicode.IsControl(r) {
			return errors.New("the password of the mail server has a control character in it")
		}
	}
	return s.store.SetMailPassword(ctx, password)
}

// ClearMailPassword removes the password of the mail server, for a server that needs none.
func (s *Service) ClearMailPassword(ctx context.Context) error {
	return s.store.ClearMailPassword(ctx)
}

// TwoFactorNotConfirmedError is what ResetAllTwoFactor answers when it was not told to go ahead: nothing was changed, and Count is how many users would lose their
// second factor.
type TwoFactorNotConfirmedError struct{ Count int64 }

func (e *TwoFactorNotConfirmedError) Error() string {
	return fmt.Sprintf("%d user(s) would lose their second factor and nothing was changed: repeat the command with --yes to do it", e.Count)
}

// ResetAllTwoFactor removes the second factor of every user, because the secrets of the old panel were readable with its key and count as known to others
// (plan 13.3). They sign in with their password and set it up again; whether they must is the setting of the panel (pterodactyl:auth:2fa_required). Unless
// confirmed it changes nothing, and answers a *TwoFactorNotConfirmedError with the number of users who would be affected.
func (s *Service) ResetAllTwoFactor(ctx context.Context, confirmed bool) (int64, error) {
	if !confirmed {
		n, err := s.store.TwoFactorUsers(ctx)
		if err != nil {
			return 0, err
		}
		return 0, &TwoFactorNotConfirmedError{Count: n}
	}
	return s.store.DisableAllTwoFactor(ctx)
}
