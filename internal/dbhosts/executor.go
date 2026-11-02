package dbhosts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"
)

// ErrNoHost is what a Lookup answers for a host that does not exist.
var ErrNoHost = errors.New("dbhosts: no such database host")

// DefaultTimeout is how long all the statements of one run may take together, connecting included.
const DefaultTimeout = time.Minute

// Conn is what it takes to sign in to a database host: the account the panel manages the host with. It prints without the password,
// so that no log line made with it can give it away.
type Conn struct {
	Host     string
	Port     int
	Username string
	Password string
}

// String is the host and the user, never the password.
func (c Conn) String() string {
	return c.Username + "@" + net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// GoString keeps %#v from printing the password.
func (c Conn) GoString() string { return "dbhosts.Conn{" + c.String() + "}" }

// LogValue keeps structured logs from printing the password.
func (c Conn) LogValue() slog.Value { return slog.StringValue(c.String()) }

func (c Conn) check() error {
	return Host{Name: "host", Host: c.Host, Port: c.Port, Username: c.Username}.Validate()
}

// Session is one open connection to a host.
type Session interface {
	Exec(ctx context.Context, query string, args ...any) error
	Close() error
}

// Dialer opens a connection to a host. An error it returns for a refusal by the host itself should be a *ServerError.
type Dialer interface {
	Dial(ctx context.Context, c Conn) (Session, error)
}

// Lookup finds how to reach a host by the number of its row, or answers ErrNoHost.
type Lookup interface {
	Conn(ctx context.Context, hostID uint64) (Conn, error)
}

// ServerError is a refusal by the host itself. It keeps the error number and drops the text, because the text of an error the host
// makes can repeat the part of the statement that it did not understand, and the statement holds a password.
type ServerError struct{ Number uint16 }

func (e *ServerError) Error() string { return fmt.Sprintf("the host answered with error %d", e.Number) }

// HostError says that a run on a database host failed, and where: Step 0 is connecting and a number from 1 is the statement that
// failed. Number is the host's error number if the host answered. The text of an error of the host is never kept (see ServerError); one
// from the network, which names no statement, is.
type HostError struct {
	Step   int
	Total  int
	Number uint16
	cause  error
}

func (e *HostError) Error() string {
	switch {
	case e.Step == 0 && e.Number != 0:
		return fmt.Sprintf("database host refused the connection (error %d)", e.Number)
	case e.Step == 0:
		return "database host could not be reached: " + e.cause.Error()
	case e.Number != 0:
		return fmt.Sprintf("database host failed statement %d of %d (error %d)", e.Step, e.Total, e.Number)
	}
	return fmt.Sprintf("database host failed statement %d of %d: %s", e.Step, e.Total, e.cause.Error())
}

// Unwrap gives the cause of an error from the network, so that a cancelled or expired context can be told.
func (e *HostError) Unwrap() error { return e.cause }

// Executor runs statements on database hosts: one connection for each run, closed when the run is over. Databases are made and
// dropped rarely, so a pool would only hold connections open to hosts that nobody is using.
type Executor struct {
	lookup  Lookup
	dialer  Dialer
	timeout time.Duration
}

// NewExecutor returns an Executor. lookup may be nil for a use that always names the connection (RunOn, Test). A timeout of 0 is
// DefaultTimeout.
func NewExecutor(lookup Lookup, dialer Dialer, timeout time.Duration) (*Executor, error) {
	if dialer == nil {
		return nil, errors.New("dbhosts: an executor needs a dialer")
	}
	if timeout < 0 {
		return nil, errors.New("dbhosts: the timeout cannot be negative")
	}
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	return &Executor{lookup: lookup, dialer: dialer, timeout: timeout}, nil
}

// Run executes the statements in order on a host and stops at the first that fails. It is the Hosts of the database endpoints.
func (e *Executor) Run(ctx context.Context, hostID uint64, statements []Statement) error {
	if e.lookup == nil {
		return errors.New("dbhosts: this executor cannot look hosts up")
	}
	c, err := e.lookup.Conn(ctx, hostID)
	if err != nil {
		return fmt.Errorf("find database host %d: %w", hostID, err)
	}
	return e.RunOn(ctx, c, statements)
}

// RunOn executes the statements on the host the connection describes.
func (e *Executor) RunOn(ctx context.Context, c Conn, statements []Statement) error {
	if err := c.check(); err != nil {
		return fmt.Errorf("database host: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	s, err := e.dialer.Dial(ctx, c)
	if err != nil {
		return failure(0, len(statements), err)
	}
	defer s.Close()
	for i, st := range statements {
		if err := s.Exec(ctx, st.SQL, st.Args...); err != nil {
			return failure(i+1, len(statements), err)
		}
	}
	return nil
}

// Test checks that the account can sign in to the host and run a statement, as the reference does when a host is saved.
func (e *Executor) Test(ctx context.Context, c Conn) error {
	return e.RunOn(ctx, c, []Statement{{SQL: "SELECT 1"}})
}

func failure(step, total int, err error) error {
	he := &HostError{Step: step, Total: total}
	var se *ServerError
	if errors.As(err, &se) {
		he.Number = se.Number
		return he
	}
	he.cause = err
	return he
}
