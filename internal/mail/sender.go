package mail

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"net/textproto"
	"strconv"
	"time"
)

// Security is how a connection to the mail server is protected.
type Security int

const (
	// StartTLS connects in the clear and switches to TLS before anything secret is sent. The server must offer it: a connection that
	// cannot be protected is not used. It is the default (port 587).
	StartTLS Security = iota
	// ImplicitTLS is TLS from the first byte (port 465).
	ImplicitTLS
	// NoTLS sends everything in the clear. It is for a mail server on the same machine, and a password is never sent that way to
	// another one.
	NoTLS
)

// SenderOptions are the settings of a Sender.
type SenderOptions struct {
	// Addr is host:port of the mail server.
	Addr string
	// Username and Password sign in to the server; both empty means no sign-in.
	Username, Password string
	Security           Security
	// From is the address mail is sent from.
	From string
	// Hello is the name the panel gives itself to the server, "localhost" by default.
	Hello string
	// Timeout bounds a whole delivery, 30 seconds by default (the context may be shorter).
	Timeout time.Duration
	// TLSConfig, if set, is used for the TLS connection (its ServerName defaults to the host of Addr). It is for tests.
	TLSConfig *tls.Config
	// Random and Now default to crypto/rand.Reader and time.Now.
	Random io.Reader
	Now    func() time.Time
}

// Sender delivers messages to a mail server over SMTP.
type Sender struct {
	o    SenderOptions
	host string
}

// NewSender checks the settings and returns a Sender.
func NewSender(o SenderOptions) (*Sender, error) {
	host, port, err := net.SplitHostPort(o.Addr)
	if err != nil || host == "" {
		return nil, errors.New("mail: the address of the mail server must look like host:port")
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return nil, errors.New("mail: the port of the mail server is not a port")
	}
	if err := ValidateAddress(o.From); err != nil {
		return nil, fmt.Errorf("mail: sender: %w", err)
	}
	if o.Security < StartTLS || o.Security > NoTLS {
		return nil, errors.New("mail: unknown way to protect the connection")
	}
	if (o.Username == "") != (o.Password == "") {
		return nil, errors.New("mail: the user name and the password of the mail server go together")
	}
	if o.Security == NoTLS && o.Username != "" && !isLoopback(host) {
		return nil, errors.New("mail: a password is not sent to another machine without TLS")
	}
	if o.Hello == "" {
		o.Hello = "localhost"
	}
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
	if o.Random == nil {
		o.Random = rand.Reader
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Sender{o: o, host: host}, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// SendError is a failed delivery. Permanent says whether the server refused the message or its recipient for good, so that trying again
// is no use; anything else (a server that is not there, that is busy, that wants another setting) may be cured by waiting.
type SendError struct {
	// Stage is the step that failed, such as "connect", "recipient" or "message".
	Stage string
	Err   error
	perm  bool
}

func (e *SendError) Error() string { return "mail: " + e.Stage + ": " + e.Err.Error() }
func (e *SendError) Unwrap() error { return e.Err }

// Permanent reports whether trying again is no use.
func (e *SendError) Permanent() bool { return e.perm }

// stage wraps a failure of a step. Only a refusal of the recipient or of the message itself, with a code from 500 to 599, is permanent.
func stage(name string, err error, canBePermanent bool) error {
	if err == nil {
		return nil
	}
	var te *textproto.Error
	return &SendError{Stage: name, Err: err, perm: canBePermanent && errors.As(err, &te) && te.Code >= 500 && te.Code <= 599}
}

// Send delivers one message.
func (s *Sender) Send(ctx context.Context, m Message) error {
	body, err := m.Encode(s.o.From, s.o.Now(), s.o.Random)
	if err != nil {
		// The message itself is wrong: no later try would make it right.
		return &SendError{Stage: "encode", Err: err, perm: true}
	}

	ctx, cancel := context.WithTimeout(ctx, s.o.Timeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", s.o.Addr)
	if err != nil {
		return stage("connect", err, false)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	// A context that ends closes the connection, which ends whatever step is waiting.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	tlsConf := s.tlsConfig()
	if s.o.Security == ImplicitTLS {
		tc := tls.Client(conn, tlsConf)
		if err := tc.HandshakeContext(ctx); err != nil {
			return stage("tls", err, false)
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, s.host)
	if err != nil {
		return stage("greeting", err, false)
	}
	defer c.Close()

	if err := c.Hello(s.o.Hello); err != nil {
		return stage("hello", err, false)
	}
	if s.o.Security == StartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return stage("tls", errors.New("the server does not offer STARTTLS"), false)
		}
		if err := c.StartTLS(tlsConf); err != nil {
			return stage("tls", err, false)
		}
	}
	if s.o.Username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return stage("sign-in", errors.New("the server does not offer to sign in"), false)
		}
		if err := c.Auth(smtp.PlainAuth("", s.o.Username, s.o.Password, s.host)); err != nil {
			return stage("sign-in", err, false)
		}
	}
	if err := c.Mail(s.o.From); err != nil {
		return stage("sender", err, false)
	}
	if err := c.Rcpt(m.To); err != nil {
		return stage("recipient", err, true)
	}
	w, err := c.Data()
	if err != nil {
		return stage("message", err, true)
	}
	if _, err := w.Write(body); err != nil {
		return stage("message", err, false)
	}
	if err := w.Close(); err != nil {
		return stage("message", err, true)
	}
	_ = c.Quit()
	return nil
}

func (s *Sender) tlsConfig() *tls.Config {
	if s.o.TLSConfig != nil {
		c := s.o.TLSConfig.Clone()
		if c.ServerName == "" {
			c.ServerName = s.host
		}
		return c
	}
	return &tls.Config{ServerName: s.host, MinVersion: tls.VersionTLS12}
}
