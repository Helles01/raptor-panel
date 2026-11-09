package admin

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Helles01/raptor-panel/internal/dbhosts"
)

// ServerDatabase is a database of a server as the rotation of passwords needs to know it.
type ServerDatabase struct {
	ID, ServerID, HostID uint64
	Name, Username       string
	Remote               string
	MaxConnections       int
}

// DatabaseRotationStore is the part of the storage the rotation of the passwords of server databases needs.
type DatabaseRotationStore interface {
	// Databases lists every database of a server, or the ones on one host when hostID is not nil.
	Databases(ctx context.Context, hostID *uint64) ([]ServerDatabase, error)
	// SetPassword stores a new password for a database, sealed. ErrNotFound for one that is not there.
	SetPassword(ctx context.Context, id uint64, password string) error
}

// DatabaseHosts runs statements on a database host. *dbhosts.Executor is one.
type DatabaseHosts interface {
	Run(ctx context.Context, hostID uint64, statements []dbhosts.Statement) error
}

// DatabaseRotation gives every database of every server a new password (plan 13.3: the passwords of the old panel were readable with its key).
type DatabaseRotation struct {
	store  DatabaseRotationStore
	hosts  DatabaseHosts
	random io.Reader
}

// NewDatabaseRotation returns a DatabaseRotation. random defaults to crypto/rand.Reader.
func NewDatabaseRotation(store DatabaseRotationStore, hosts DatabaseHosts, random io.Reader) (*DatabaseRotation, error) {
	if store == nil || hosts == nil {
		return nil, errors.New("admin: a rotation of database passwords needs a store and a way to reach the hosts")
	}
	if random == nil {
		random = rand.Reader
	}
	return &DatabaseRotation{store: store, hosts: hosts, random: random}, nil
}

// DatabasesNotConfirmedError is what RotateAll answers when it was not told to go ahead: nothing was changed, and Count is how many databases would get a new
// password.
type DatabasesNotConfirmedError struct{ Count int }

func (e *DatabasesNotConfirmedError) Error() string {
	return fmt.Sprintf("%d database password(s) would be changed and nothing was changed: passwords written into the configuration of game servers stop working, so tell the owners first; repeat the command with --yes to do it", e.Count)
}

// DatabaseFailure is a database whose password could not be changed. Stage says how far it got: "host" means nothing was changed; "store" means the host has the
// new password and the panel could not keep it, which RotateDatabase puts right (running RotateAll again would change every other database once more).
type DatabaseFailure struct {
	ID, HostID uint64
	Name       string
	Stage      string
	Cause      string
}

// DatabaseRotationReport is what a rotation did.
type DatabaseRotationReport struct {
	Total   int
	Changed int
	Failed  []DatabaseFailure
}

// RotateAll gives the databases a new password, on the host and in the panel, one at a time. A database that cannot be changed does not stop the others; it is in
// the report. The host is changed first and the panel after, because the old password may not be readable at all (it may still be in the old encrypted column), so
// there is nothing to put back; the panel never shows a password that was not set. Unless confirmed it changes nothing and answers a
// *DatabasesNotConfirmedError with the number of databases.
func (s *DatabaseRotation) RotateAll(ctx context.Context, hostID *uint64, confirmed bool) (DatabaseRotationReport, error) {
	dbs, err := s.store.Databases(ctx, hostID)
	if err != nil {
		return DatabaseRotationReport{}, err
	}
	return s.rotate(ctx, dbs, confirmed)
}

// RotateDatabase is RotateAll for one database. It is what puts right a database whose report said "store": the host has its new password and the panel does
// not, and running RotateAll again would give every other database yet another password, after their owners had put the first one into a configuration.
// ErrNotFound for a database that is not there.
func (s *DatabaseRotation) RotateDatabase(ctx context.Context, id uint64, confirmed bool) (DatabaseRotationReport, error) {
	dbs, err := s.store.Databases(ctx, nil)
	if err != nil {
		return DatabaseRotationReport{}, err
	}
	for _, d := range dbs {
		if d.ID == id {
			return s.rotate(ctx, []ServerDatabase{d}, confirmed)
		}
	}
	return DatabaseRotationReport{}, ErrNotFound
}

func (s *DatabaseRotation) rotate(ctx context.Context, dbs []ServerDatabase, confirmed bool) (DatabaseRotationReport, error) {
	if !confirmed {
		return DatabaseRotationReport{}, &DatabasesNotConfirmedError{Count: len(dbs)}
	}
	report := DatabaseRotationReport{Total: len(dbs)}
	for _, d := range dbs {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		fail := func(stage string, cause error) {
			report.Failed = append(report.Failed, DatabaseFailure{ID: d.ID, HostID: d.HostID, Name: oneLine(d.Name), Stage: stage, Cause: oneLine(cause.Error())})
		}
		password, err := dbhosts.Password(s.random)
		if err != nil {
			fail("host", err)
			continue
		}
		statements, err := dbhosts.ChangePassword(dbhosts.Database{ServerID: d.ServerID, Name: d.Name, Username: d.Username, Remote: d.Remote, MaxConnections: d.MaxConnections}, password)
		if err != nil {
			fail("host", err)
			continue
		}
		if err := s.hosts.Run(ctx, d.HostID, statements); err != nil {
			fail("host", err)
			continue
		}
		if err := s.store.SetPassword(ctx, d.ID, password); err != nil {
			fail("store", err)
			continue
		}
		report.Changed++
	}
	return report, nil
}

// oneLine makes text from a database or a host fit in a line of a terminal: control characters become spaces, and it is cut.
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) > 200 {
		s = s[:200]
	}
	return strings.TrimSpace(s)
}
