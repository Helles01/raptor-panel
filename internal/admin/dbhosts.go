package admin

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"github.com/Helles01/raptor-panel/internal/dbhosts"
)

// ErrHostInUse: the host still has databases on it.
var ErrHostInUse = errors.New("admin: the database host still has databases")

// HostRecord is a database host as the store keeps it: the account the panel manages the host with, its password open, and the node
// the host is for (none: any server may have a database there when the panel allows it).
type HostRecord struct {
	ID       uint64
	Name     string
	Host     string
	Port     int
	Username string
	Password string
	NodeID   *uint64
}

// Conn is how to sign in to the host.
func (h HostRecord) Conn() dbhosts.Conn {
	return dbhosts.Conn{Host: h.Host, Port: h.Port, Username: h.Username, Password: h.Password}
}

// HostInfo is a host as a list shows it: no password.
type HostInfo struct {
	ID        uint64
	Name      string
	Host      string
	Port      int
	Username  string
	NodeID    *uint64
	Databases int64
}

// HostStore is the part of the storage the database host commands need. The store seals the password in what it writes and opens it in
// what it reads.
type HostStore interface {
	// CreateHost stores a host, or answers ErrNotFound for a node that is not there.
	CreateHost(ctx context.Context, h HostRecord) (uint64, error)
	ListHosts(ctx context.Context) ([]HostInfo, error)
	FindHost(ctx context.Context, id uint64) (HostRecord, error)
	// ChangeHost reads the host with its row locked, calls change, and stores what change returns; an error of change leaves the row as
	// it was. It answers ErrNotFound for a host or a node that is not there.
	ChangeHost(ctx context.Context, id uint64, change func(current HostRecord) (HostRecord, error)) error
	// DeleteHost removes a host. check is called with the number of databases on it, inside the transaction in which they are counted.
	DeleteHost(ctx context.Context, id uint64, check func(databases int64) error) error
}

// HostAccess reaches a host. *dbhosts.Executor is one.
type HostAccess interface {
	Test(ctx context.Context, c dbhosts.Conn) error
	RunOn(ctx context.Context, c dbhosts.Conn, statements []dbhosts.Statement) error
}

// HostService does the operations on database hosts.
type HostService struct {
	store  HostStore
	access HostAccess
	random io.Reader
}

// NewHostService returns a HostService. random defaults to crypto/rand.Reader.
func NewHostService(store HostStore, access HostAccess, random io.Reader) (*HostService, error) {
	if store == nil || access == nil {
		return nil, errors.New("admin: database hosts need a store and a way to reach them")
	}
	if random == nil {
		random = rand.Reader
	}
	return &HostService{store: store, access: access, random: random}, nil
}

// HostInput is a host to make.
type HostInput struct {
	Name     string
	Host     string
	Port     int
	Username string
	Password string
	NodeID   *uint64
}

func (in HostInput) record() HostRecord {
	return HostRecord{Name: in.Name, Host: in.Host, Port: in.Port, Username: in.Username, Password: in.Password, NodeID: in.NodeID}
}

func validateHost(h HostRecord) error {
	if err := (dbhosts.Host{Name: h.Name, Host: h.Host, Port: h.Port, Username: h.Username}).Validate(); err != nil {
		return err
	}
	return dbhosts.ValidateHostPassword(h.Password)
}

// CreateHost saves a host once the panel has signed in to it with the account given, as the reference does: a host the panel cannot
// reach is not saved.
func (s *HostService) CreateHost(ctx context.Context, in HostInput) (uint64, error) {
	h := in.record()
	if err := validateHost(h); err != nil {
		return 0, err
	}
	if err := s.access.Test(ctx, h.Conn()); err != nil {
		return 0, fmt.Errorf("sign in to the host: %w", err)
	}
	return s.store.CreateHost(ctx, h)
}

// ListHosts lists the hosts.
func (s *HostService) ListHosts(ctx context.Context) ([]HostInfo, error) {
	return s.store.ListHosts(ctx)
}

// TestHost signs in to a stored host.
func (s *HostService) TestHost(ctx context.Context, id uint64) error {
	h, err := s.store.FindHost(ctx, id)
	if err != nil {
		return err
	}
	if err := s.access.Test(ctx, h.Conn()); err != nil {
		return fmt.Errorf("sign in to the host: %w", err)
	}
	return nil
}

// HostPatch is what to change about a host; what is not set stays. NodeSet says that NodeID is to be changed, and nil then unlinks the
// host from its node.
type HostPatch struct {
	Name     *string
	Host     *string
	Port     *int
	Username *string
	// Password is the password the account has on the host; it changes what the panel stores and nothing on the host. Use
	// RotatePassword to change the password on the host.
	Password *string
	NodeSet  bool
	NodeID   *uint64
}

// UpdateHost changes a host, after the panel has signed in to it with the result, as the reference does.
func (s *HostService) UpdateHost(ctx context.Context, id uint64, p HostPatch) error {
	return s.store.ChangeHost(ctx, id, func(h HostRecord) (HostRecord, error) {
		if p.Name != nil {
			h.Name = *p.Name
		}
		if p.Host != nil {
			h.Host = *p.Host
		}
		if p.Port != nil {
			h.Port = *p.Port
		}
		if p.Username != nil {
			h.Username = *p.Username
		}
		if p.Password != nil {
			h.Password = *p.Password
		}
		if p.NodeSet {
			h.NodeID = p.NodeID
		}
		if err := validateHost(h); err != nil {
			return HostRecord{}, err
		}
		if err := s.access.Test(ctx, h.Conn()); err != nil {
			return HostRecord{}, fmt.Errorf("sign in to the host: %w", err)
		}
		return h, nil
	})
}

// HostMismatchError says that the password of the account was changed on the host and the panel could not store it. The panel can no
// longer sign in to the host until the password is stored by hand; the new password is kept here for the one who runs the command, and
// is not in the text of the error.
type HostMismatchError struct {
	HostID   uint64
	password string
	cause    error
}

func (e *HostMismatchError) Error() string {
	return fmt.Sprintf("the password of the account on database host %d was changed, but the panel could not store it: %v", e.HostID, e.cause)
}

// Unwrap gives the failure of the store.
func (e *HostMismatchError) Unwrap() error { return e.cause }

// NewPassword is the password the account has on the host now.
func (e *HostMismatchError) NewPassword() string { return e.password }

// RotatePassword gives the account the panel manages a host with a new password, on the host and in the panel together. The old
// password is used once, to sign in. The new password is made here and returned once.
func (s *HostService) RotatePassword(ctx context.Context, id uint64) (string, error) {
	password, err := dbhosts.Password(s.random)
	if err != nil {
		return "", fmt.Errorf("make a password: %w", err)
	}
	statements, err := dbhosts.ChangeOwnPassword(password)
	if err != nil {
		return "", err
	}

	changedOnHost := false
	err = s.store.ChangeHost(ctx, id, func(h HostRecord) (HostRecord, error) {
		if err := s.access.RunOn(ctx, h.Conn(), statements); err != nil {
			return HostRecord{}, fmt.Errorf("change the password on the host: %w", err)
		}
		changedOnHost = true
		h.Password = password
		return h, nil
	})
	switch {
	case err == nil:
		return password, nil
	case changedOnHost:
		return "", &HostMismatchError{HostID: id, password: password, cause: err}
	}
	return "", err
}

// DeleteHost removes a host that has no databases on it.
func (s *HostService) DeleteHost(ctx context.Context, id uint64) error {
	return s.store.DeleteHost(ctx, id, func(databases int64) error {
		if databases > 0 {
			return fmt.Errorf("%w (%d)", ErrHostInUse, databases)
		}
		return nil
	})
}
