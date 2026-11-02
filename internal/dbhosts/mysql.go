package dbhosts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
)

// TLSModes are the values of the setting for how the panel talks to database hosts, the same words as for its own database.
var TLSModes = []string{"false", "true", "skip-verify", "preferred"}

// MySQLDialer connects to MariaDB and MySQL hosts.
type MySQLDialer struct {
	// TLS is one of TLSModes; "" is "false". The hosts are often on the same machine or the same private network, which is why the
	// default is no TLS, as in the reference; a host across the internet needs "true".
	TLS string
	// ConnectTimeout bounds opening the connection; 10 seconds when 0.
	ConnectTimeout time.Duration
}

// NewMySQLDialer checks the TLS mode.
func NewMySQLDialer(tls string, connect time.Duration) (MySQLDialer, error) {
	if tls != "" {
		known := false
		for _, m := range TLSModes {
			known = known || m == tls
		}
		if !known {
			return MySQLDialer{}, fmt.Errorf("dbhosts: %q is not a TLS mode for database hosts (use false, true, skip-verify or preferred)", tls)
		}
	}
	return MySQLDialer{TLS: tls, ConnectTimeout: connect}, nil
}

// config maps a connection to the driver. The values of a statement are put into it by the driver, escaped (InterpolateParams): the
// hosts differ in which statements they take as prepared statements, and the account statements are among those that some do not. A
// password that goes in is held to a short list of characters in any case (checkPassword). More than one statement per call, local
// files and cleartext passwords stay at the driver's safe defaults (off).
func (d MySQLDialer) config(c Conn) *mysql.Config {
	cfg := mysql.NewConfig()
	cfg.User = c.Username
	cfg.Passwd = c.Password
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	cfg.Collation = "utf8mb4_unicode_ci"
	cfg.InterpolateParams = true
	cfg.TLSConfig = d.TLS
	if cfg.TLSConfig == "" {
		cfg.TLSConfig = "false"
	}
	cfg.Timeout = d.ConnectTimeout
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	cfg.ReadTimeout = 30 * time.Second
	cfg.WriteTimeout = 30 * time.Second
	return cfg
}

// Dial opens one connection.
func (d MySQLDialer) Dial(ctx context.Context, c Conn) (Session, error) {
	connector, err := mysql.NewConnector(d.config(c))
	if err != nil {
		return nil, fmt.Errorf("configure the connection to %s: %w", c, err)
	}
	pool := sql.OpenDB(connector)
	pool.SetMaxOpenConns(1)
	conn, err := pool.Conn(ctx)
	if err != nil {
		pool.Close()
		return nil, translate(err)
	}
	return &mysqlSession{pool: pool, conn: conn}, nil
}

type mysqlSession struct {
	pool *sql.DB
	conn *sql.Conn
}

func (s *mysqlSession) Exec(ctx context.Context, query string, args ...any) error {
	_, err := s.conn.ExecContext(ctx, query, args...)
	return translate(err)
}

func (s *mysqlSession) Close() error {
	err := s.conn.Close()
	if perr := s.pool.Close(); err == nil {
		err = perr
	}
	return err
}

// translate turns an answer of the host into a *ServerError, which has no text, and leaves any other error alone.
func translate(err error) error {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return &ServerError{Number: me.Number}
	}
	return err
}
