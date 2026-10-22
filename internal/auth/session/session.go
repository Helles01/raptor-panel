// Package session manages browser sessions: the secret cookie value, its expiry, and the login and logout rules.
//
// The cookie holds a random 256 bit identifier. The store never sees it: records are kept under the SHA-256 of the
// identifier, so a copy of the sessions table cannot be turned into working cookies. Logging in always issues a new
// identifier, which closes session fixation.
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/Helles01/raptor-panel/internal/ids"
)

var (
	// ErrNotFound is returned for a session that does not exist, has expired, or whose identifier is malformed.
	ErrNotFound = errors.New("session: not found")

	idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

const (
	idBytes = 32
	// ConfirmationLifetime is how long a login may wait for its second factor.
	ConfirmationLifetime = 5 * time.Minute
	confirmationLength   = 64
	// MaxConfirmationFailures is how many wrong second factors a pending login takes before it is cancelled and the password has to
	// be entered again, which the sign-in counters limit. The reference keeps the pending login for its five minutes however many
	// codes are tried (C-039).
	MaxConfirmationFailures = 5
)

// Session is a loaded session.
type Session struct {
	// ID is the secret cookie value. Never log it, never store it.
	ID string
	// UserID is the logged-in user, or 0 for a guest session.
	UserID uint64
	// Confirmation is set while a login waits for its second factor.
	Confirmation *Confirmation

	CreatedAt    time.Time
	LastActivity time.Time
	IP           string
	UserAgent    string
}

// Confirmation is a password-verified login that still needs a TOTP code or recovery token.
type Confirmation struct {
	UserID    uint64
	Token     string
	ExpiresAt time.Time
	// Failures counts the wrong second factors given for it.
	Failures int
}

// Record is what a Store keeps. Its columns match the reference panel's sessions table: id (Key), user_id, ip_address,
// user_agent, payload and last_activity.
type Record struct {
	Key          string
	UserID       uint64
	Payload      []byte
	IP           string
	UserAgent    string
	LastActivity time.Time
}

// Store persists session records.
type Store interface {
	// Get returns the record, or ErrNotFound.
	Get(ctx context.Context, key string) (Record, error)
	// Put creates or replaces a record.
	Put(ctx context.Context, r Record) error
	Delete(ctx context.Context, key string) error
	// DeleteForUser removes every session of a user, for example after a password change.
	DeleteForUser(ctx context.Context, userID uint64) error
}

// Options configure a Manager.
type Options struct {
	// IdleTimeout ends a session that has not been used for this long. The reference's default is 720 minutes.
	IdleTimeout time.Duration
	// MaxLifetime ends a session this long after it was created, however active it is.
	MaxLifetime time.Duration
	// Secure marks the cookie as HTTPS only and gives it the "__Host-" prefix. Turn it off only for local development.
	Secure bool
}

// Manager creates, loads and ends sessions.
type Manager struct {
	store Store
	opts  Options
	now   func() time.Time
}

// NewManager returns a Manager.
func NewManager(store Store, opts Options) (*Manager, error) {
	if store == nil {
		return nil, errors.New("session: a store is required")
	}
	if opts.IdleTimeout <= 0 || opts.MaxLifetime <= 0 || opts.IdleTimeout > opts.MaxLifetime {
		return nil, errors.New("session: the idle timeout must be positive and not longer than the maximum lifetime")
	}
	return &Manager{store: store, opts: opts, now: time.Now}, nil
}

func key(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

type payload struct {
	CreatedAt    time.Time     `json:"created_at"`
	Confirmation *Confirmation `json:"confirmation,omitempty"`
}

// Create starts a guest session.
func (m *Manager) Create(ctx context.Context, ip, userAgent string) (*Session, error) {
	return m.create(ctx, 0, ip, userAgent)
}

func (m *Manager) create(ctx context.Context, userID uint64, ip, userAgent string) (*Session, error) {
	raw := make([]byte, idBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generate session id: %w", err)
	}
	now := m.now()
	s := &Session{
		ID:        base64.RawURLEncoding.EncodeToString(raw),
		UserID:    userID,
		CreatedAt: now, LastActivity: now,
		IP: ip, UserAgent: truncate(userAgent, 255),
	}
	if err := m.write(ctx, s); err != nil {
		return nil, err
	}
	return s, nil
}

func (m *Manager) write(ctx context.Context, s *Session) error {
	p, err := json.Marshal(payload{CreatedAt: s.CreatedAt, Confirmation: s.Confirmation})
	if err != nil {
		return fmt.Errorf("encode session: %w", err)
	}
	return m.store.Put(ctx, Record{Key: key(s.ID), UserID: s.UserID, Payload: p, IP: s.IP, UserAgent: s.UserAgent, LastActivity: s.LastActivity})
}

// Load returns the session for a cookie value. Anything that is not a valid, live session is ErrNotFound; an expired
// session is also deleted.
func (m *Manager) Load(ctx context.Context, id string) (*Session, error) {
	if !idPattern.MatchString(id) {
		return nil, ErrNotFound
	}
	r, err := m.store.Get(ctx, key(id))
	if err != nil {
		return nil, err
	}

	var p payload
	if err := json.Unmarshal(r.Payload, &p); err != nil || p.CreatedAt.IsZero() {
		_ = m.store.Delete(ctx, r.Key)
		return nil, ErrNotFound
	}
	now := m.now()
	if now.Sub(r.LastActivity) > m.opts.IdleTimeout || now.Sub(p.CreatedAt) > m.opts.MaxLifetime {
		_ = m.store.Delete(ctx, r.Key)
		return nil, ErrNotFound
	}
	return &Session{
		ID: id, UserID: r.UserID, Confirmation: p.Confirmation,
		CreatedAt: p.CreatedAt, LastActivity: r.LastActivity, IP: r.IP, UserAgent: r.UserAgent,
	}, nil
}

// Save writes the session back and records that it was used now.
func (m *Manager) Save(ctx context.Context, s *Session) error {
	s.LastActivity = m.now()
	return m.write(ctx, s)
}

// Login turns a session into a session of the user. It issues a **new identifier** and deletes the old one, so an
// identifier an attacker planted before the login is worthless afterwards. Any pending confirmation is cleared.
func (m *Manager) Login(ctx context.Context, old *Session, userID uint64) (*Session, error) {
	if userID == 0 {
		return nil, errors.New("session: a login needs a user")
	}
	fresh, err := m.create(ctx, userID, old.IP, old.UserAgent)
	if err != nil {
		return nil, err
	}
	if err := m.store.Delete(ctx, key(old.ID)); err != nil {
		return nil, fmt.Errorf("delete the old session: %w", err)
	}
	return fresh, nil
}

// Destroy ends a session (logout).
func (m *Manager) Destroy(ctx context.Context, s *Session) error {
	return m.store.Delete(ctx, key(s.ID))
}

// RevokeUser ends every session of a user.
func (m *Manager) RevokeUser(ctx context.Context, userID uint64) error {
	return m.store.DeleteForUser(ctx, userID)
}

// CookieName is the name of the session cookie. With Secure it carries the "__Host-" prefix, which browsers only accept
// for Secure cookies on the root path without a Domain, so no sibling site can overwrite it.
func CookieName(secure bool) string {
	if secure {
		return "__Host-raptor_session"
	}
	return "raptor_session"
}

// Cookie returns the cookie to send for a session: HttpOnly, SameSite=Lax, Path=/, no Domain.
func (m *Manager) Cookie(s *Session) *http.Cookie {
	return &http.Cookie{
		Name: CookieName(m.opts.Secure), Value: s.ID, Path: "/",
		MaxAge: int(m.opts.MaxLifetime / time.Second), HttpOnly: true, Secure: m.opts.Secure, SameSite: http.SameSiteLaxMode,
	}
}

// ClearCookie returns a cookie that makes the browser drop the session cookie.
func (m *Manager) ClearCookie() *http.Cookie {
	return &http.Cookie{
		Name: CookieName(m.opts.Secure), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: m.opts.Secure, SameSite: http.SameSiteLaxMode,
	}
}

// StartConfirmation records that the password of userID was right and a second factor is needed. It returns the token
// the client must send back with the code, which expires after ConfirmationLifetime.
func (m *Manager) StartConfirmation(s *Session, userID uint64) (string, error) {
	token, err := ids.RandomAlnum(rand.Reader, confirmationLength)
	if err != nil {
		return "", err
	}
	s.Confirmation = &Confirmation{UserID: userID, Token: token, ExpiresAt: m.now().Add(ConfirmationLifetime)}
	return token, nil
}

// CheckConfirmation returns the user of a pending confirmation if the token matches (compared in constant time) and has
// not expired. It does not consume the confirmation: a wrong code does not force the user to log in again, but the
// token stops working after it expires, and a successful login replaces the session.
func (m *Manager) CheckConfirmation(s *Session, token string) (userID uint64, ok bool) {
	c := s.Confirmation
	if c == nil || c.UserID == 0 || c.Failures >= MaxConfirmationFailures || !m.now().Before(c.ExpiresAt) {
		return 0, false
	}
	if subtle.ConstantTimeCompare([]byte(c.Token), []byte(token)) != 1 {
		return 0, false
	}
	return c.UserID, true
}

// ConfirmationPending reports whether the session holds a login that waits for its second factor and has neither expired nor used
// up its failures. The caller uses it to tell an expired login from a wrong token.
func (m *Manager) ConfirmationPending(s *Session) bool {
	c := s.Confirmation
	return c != nil && c.UserID != 0 && c.Failures < MaxConfirmationFailures && m.now().Before(c.ExpiresAt)
}

// FailConfirmation counts a wrong second factor against the pending login, and cancels the login after MaxConfirmationFailures. It
// reports whether the login is still pending.
func (m *Manager) FailConfirmation(s *Session) bool {
	c := s.Confirmation
	if c == nil {
		return false
	}
	if c.Failures++; c.Failures >= MaxConfirmationFailures {
		s.Confirmation = nil
		return false
	}
	return true
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
