package mail

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// session is what a fake mail server saw of one connection.
type session struct {
	Hello string
	TLS   bool
	Auth  string
	From  string
	To    string
	Data  string
	Quit  bool
}

type fakeServer struct {
	ln   net.Listener
	conf *tls.Config // for STARTTLS and for implicit TLS
	pool *x509.CertPool

	implicit bool
	starttls bool
	auth     bool
	// replies override the answer to a command: "MAIL", "RCPT", "DATA" (the one after the message), "AUTH".
	replies map[string]string
	// stall makes the server say nothing after the greeting.
	stall bool

	mu       sync.Mutex
	sessions []session
}

func selfSigned(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}, pool
}

func startServer(t *testing.T, configure func(*fakeServer)) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{ln: ln, replies: map[string]string{}}
	s.conf, s.pool = selfSigned(t)
	if configure != nil {
		configure(s)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return s
}

func (s *fakeServer) addr() string { return s.ln.Addr().String() }

func (s *fakeServer) last(t *testing.T) session {
	t.Helper()
	// A session is written down when the server is done with the connection, which can be a moment after the client is.
	for deadline := time.Now().Add(2 * time.Second); ; {
		s.mu.Lock()
		n := len(s.sessions)
		var got session
		if n > 0 {
			got = s.sessions[n-1]
		}
		s.mu.Unlock()
		if n > 0 {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatal("the server saw no connection")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (s *fakeServer) reply(key, def string) string {
	if r, ok := s.replies[key]; ok {
		return r
	}
	return def
}

func (s *fakeServer) serve(conn net.Conn) {
	defer conn.Close()
	var cur session
	record := func() {
		s.mu.Lock()
		s.sessions = append(s.sessions, cur)
		s.mu.Unlock()
	}
	defer record()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if s.implicit {
		tc := tls.Server(conn, s.conf)
		if tc.Handshake() != nil {
			return
		}
		conn, cur.TLS = tc, true
	}
	r := bufio.NewReader(conn)
	say := func(line string) { _, _ = conn.Write([]byte(line + "\r\n")) }
	say("220 localhost ready")
	if s.stall {
		time.Sleep(3 * time.Second)
		return
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(cmd) {
		case "EHLO", "HELO":
			cur.Hello = arg
			lines := []string{"250-localhost"}
			if s.starttls && !cur.TLS {
				lines = append(lines, "250-STARTTLS")
			}
			if s.auth {
				lines = append(lines, "250-AUTH PLAIN")
			}
			lines = append(lines, "250 8BITMIME")
			for _, l := range lines {
				say(l)
			}
		case "STARTTLS":
			say("220 go ahead")
			tc := tls.Server(conn, s.conf)
			if tc.Handshake() != nil {
				return
			}
			conn, r, cur.TLS = tc, bufio.NewReader(tc), true
		case "AUTH":
			cur.Auth = arg
			say(s.reply("AUTH", "235 signed in"))
		case "MAIL":
			cur.From = arg
			say(s.reply("MAIL", "250 ok"))
		case "RCPT":
			cur.To = arg
			say(s.reply("RCPT", "250 ok"))
		case "DATA":
			say("354 go on")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			cur.Data = b.String()
			say(s.reply("DATA", "250 queued"))
		case "QUIT":
			cur.Quit = true
			say("221 bye")
			return
		default:
			say("502 not here")
		}
	}
}

func sender(t *testing.T, s *fakeServer, o SenderOptions) *Sender {
	t.Helper()
	o.Addr = s.addr()
	if o.From == "" {
		o.From = "panel@example.com"
	}
	o.TLSConfig = &tls.Config{RootCAs: s.pool, MinVersion: tls.VersionTLS12}
	snd, err := NewSender(o)
	if err != nil {
		t.Fatal(err)
	}
	return snd
}

var hello = Message{To: "ann@example.com", Subject: "Account Created", Text: "Hello Ann!\r\nA line.\r\n", HTML: "<p>Hello Ann!</p>"}

func TestSendInTheClear(t *testing.T) {
	s := startServer(t, nil)
	snd := sender(t, s, SenderOptions{Security: NoTLS, Hello: "panel.example.com", Now: func() time.Time { return time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC) }})
	if err := snd.Send(context.Background(), hello); err != nil {
		t.Fatal(err)
	}
	got := s.last(t)
	if got.Hello != "panel.example.com" || !strings.HasPrefix(got.From, "FROM:<panel@example.com>") || got.To != "TO:<ann@example.com>" || got.TLS || !got.Quit {
		t.Errorf("session = %+v", got)
	}
	for _, want := range []string{"From: panel@example.com\r\n", "To: ann@example.com\r\n", "Subject: Account Created\r\n", "Date: Mon, 02 Nov 2026 12:00:00 +0000\r\n", "multipart/alternative", "Hello Ann!"} {
		if !strings.Contains(got.Data, want) {
			t.Errorf("the message lacks %q:\n%s", want, got.Data)
		}
	}
	if got.Auth != "" {
		t.Errorf("signed in without credentials: %q", got.Auth)
	}
}

func TestSendSignsIn(t *testing.T) {
	// A mail server on the same machine may be signed in to in the clear.
	s := startServer(t, func(s *fakeServer) { s.auth = true })
	snd := sender(t, s, SenderOptions{Security: NoTLS, Username: "panel", Password: "s3cret"})
	if err := snd.Send(context.Background(), hello); err != nil {
		t.Fatal(err)
	}
	creds := strings.TrimPrefix(s.last(t).Auth, "PLAIN ")
	raw, err := base64.StdEncoding.DecodeString(creds)
	if err != nil || string(raw) != "\x00panel\x00s3cret" {
		t.Errorf("auth = %q (%v)", raw, err)
	}

	// A server that does not offer to sign in is not sent a password.
	s = startServer(t, nil)
	snd = sender(t, s, SenderOptions{Security: NoTLS, Username: "panel", Password: "s3cret"})
	err = snd.Send(context.Background(), hello)
	var se *SendError
	if !errors.As(err, &se) || se.Stage != "sign-in" || se.Permanent() || s.last(t).Auth != "" {
		t.Errorf("no AUTH offered: %v, session %+v", err, s.last(t))
	}
	// A refusal to sign in is not a refusal of the message: the settings may be put right.
	s = startServer(t, func(s *fakeServer) { s.auth = true; s.replies["AUTH"] = "535 wrong password" })
	snd = sender(t, s, SenderOptions{Security: NoTLS, Username: "panel", Password: "wrong"})
	if err := snd.Send(context.Background(), hello); !errors.As(err, &se) || se.Stage != "sign-in" || se.Permanent() || strings.Contains(err.Error(), "wrong\"") {
		t.Errorf("wrong password: %v", err)
	}
}

func TestSendWithStartTLS(t *testing.T) {
	s := startServer(t, func(s *fakeServer) { s.starttls = true; s.auth = true })
	snd := sender(t, s, SenderOptions{Security: StartTLS, Username: "panel", Password: "s3cret"})
	if err := snd.Send(context.Background(), hello); err != nil {
		t.Fatal(err)
	}
	got := s.last(t)
	if !got.TLS || !got.Quit || got.Auth == "" || !strings.Contains(got.Data, "Hello Ann!") {
		t.Errorf("session = %+v", got)
	}

	// A server that does not offer it is not used: nothing is sent in the clear.
	s = startServer(t, nil)
	snd = sender(t, s, SenderOptions{Security: StartTLS})
	err := snd.Send(context.Background(), hello)
	var se *SendError
	if !errors.As(err, &se) || se.Stage != "tls" || se.Permanent() || s.last(t).From != "" {
		t.Errorf("no STARTTLS: %v, session %+v", err, s.last(t))
	}
	// A server whose certificate is not trusted is not used either.
	s = startServer(t, func(s *fakeServer) { s.starttls = true })
	untrusting, err := NewSender(SenderOptions{Addr: s.addr(), From: "panel@example.com", Security: StartTLS, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}})
	if err != nil {
		t.Fatal(err)
	}
	if err := untrusting.Send(context.Background(), hello); !errors.As(err, &se) || se.Stage != "tls" || s.last(t).From != "" {
		t.Errorf("untrusted: %v, session %+v", err, s.last(t))
	}
}

func TestSendWithImplicitTLS(t *testing.T) {
	s := startServer(t, func(s *fakeServer) { s.implicit = true; s.auth = true })
	snd := sender(t, s, SenderOptions{Security: ImplicitTLS, Username: "panel", Password: "s3cret"})
	if err := snd.Send(context.Background(), hello); err != nil {
		t.Fatal(err)
	}
	if got := s.last(t); !got.TLS || got.To != "TO:<ann@example.com>" || !got.Quit {
		t.Errorf("session = %+v", got)
	}
	// Plain text to a server that wants TLS fails at the start.
	plain := sender(t, s, SenderOptions{Security: NoTLS, Timeout: 300 * time.Millisecond})
	if err := plain.Send(context.Background(), hello); err == nil {
		t.Error("plain text to a TLS port")
	}
}

func TestWhichRefusalsAreForGood(t *testing.T) {
	tests := []struct {
		name      string
		replies   map[string]string
		stage     string
		permanent bool
	}{
		{"the recipient does not exist", map[string]string{"RCPT": "550 no such user"}, "recipient", true},
		{"the recipient is busy", map[string]string{"RCPT": "450 mailbox busy"}, "recipient", false},
		{"the message is rejected", map[string]string{"DATA": "554 looks like spam"}, "message", true},
		{"the message is deferred", map[string]string{"DATA": "451 try again later"}, "message", false},
		{"the sender is refused", map[string]string{"MAIL": "550 sender not allowed"}, "sender", false},
		{"the lowest code of a refusal", map[string]string{"RCPT": "500 syntax"}, "recipient", true},
		{"the highest code of a refusal", map[string]string{"RCPT": "599 odd"}, "recipient", true},
		{"the highest code of a deferral", map[string]string{"RCPT": "499 odd"}, "recipient", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := startServer(t, func(s *fakeServer) { s.replies = tt.replies })
			err := sender(t, s, SenderOptions{Security: NoTLS}).Send(context.Background(), hello)
			var se *SendError
			if !errors.As(err, &se) || se.Stage != tt.stage || se.Permanent() != tt.permanent {
				t.Errorf("error = %v", err)
			}
		})
	}
}

func TestSendWhenTheServerIsNotThere(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	snd, err := NewSender(SenderOptions{Addr: addr, From: "panel@example.com", Security: NoTLS})
	if err != nil {
		t.Fatal(err)
	}
	var se *SendError
	if err := snd.Send(context.Background(), hello); !errors.As(err, &se) || se.Stage != "connect" || se.Permanent() {
		t.Errorf("error = %v", err)
	}
}

func TestSendGivesUpWhenTheServerSaysNothing(t *testing.T) {
	s := startServer(t, func(s *fakeServer) { s.stall = true })
	snd := sender(t, s, SenderOptions{Security: NoTLS, Timeout: 200 * time.Millisecond})
	start := time.Now()
	err := snd.Send(context.Background(), hello)
	var se *SendError
	if !errors.As(err, &se) || se.Permanent() || time.Since(start) > 2*time.Second {
		t.Errorf("error = %v after %v", err, time.Since(start))
	}
	// A context that ends ends the delivery.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	slow := sender(t, s, SenderOptions{Security: NoTLS, Timeout: time.Minute})
	start = time.Now()
	if err := slow.Send(ctx, hello); err == nil || time.Since(start) > 2*time.Second {
		t.Errorf("error = %v after %v", err, time.Since(start))
	}
}

func TestAMessageThatCannotBeWrittenIsNotSent(t *testing.T) {
	s := startServer(t, nil)
	snd := sender(t, s, SenderOptions{Security: NoTLS})
	for _, m := range []Message{
		{To: "not an address", Subject: "x", Text: "t", HTML: "h"},
		{To: "ann@example.com", Subject: "a\r\nBcc: eve@example.com", Text: "t", HTML: "h"},
		{To: "ann@example.com", Subject: "", Text: "t", HTML: "h"},
	} {
		err := snd.Send(context.Background(), m)
		var se *SendError
		if !errors.As(err, &se) || se.Stage != "encode" || !se.Permanent() {
			t.Errorf("%+v: %v", m, err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessions) != 0 {
		t.Errorf("the server was connected to %d times", len(s.sessions))
	}
}

func TestNewSenderChecksTheSettings(t *testing.T) {
	ok := SenderOptions{Addr: "mail.example.com:587", From: "panel@example.com"}
	if s, err := NewSender(ok); err != nil || s.o.Hello != "localhost" || s.o.Timeout != 30*time.Second || s.o.Random == nil || s.o.Now == nil || s.o.Security != StartTLS {
		t.Fatalf("defaults: %+v %v", s, err)
	}
	for name, mutate := range map[string]func(*SenderOptions){
		"no address":                  func(o *SenderOptions) { o.Addr = "" },
		"no port":                     func(o *SenderOptions) { o.Addr = "mail.example.com" },
		"a port that is no port":      func(o *SenderOptions) { o.Addr = "mail.example.com:smtp" },
		"a port of zero":              func(o *SenderOptions) { o.Addr = "mail.example.com:0" },
		"no host":                     func(o *SenderOptions) { o.Addr = ":587" },
		"no sender":                   func(o *SenderOptions) { o.From = "" },
		"a sender that is no address": func(o *SenderOptions) { o.From = "panel" },
		"a way that is not known":     func(o *SenderOptions) { o.Security = 7 },
		"a user without a password":   func(o *SenderOptions) { o.Username = "panel" },
		"a password without a user":   func(o *SenderOptions) { o.Password = "x" },
		"a password in the clear to another machine": func(o *SenderOptions) {
			o.Security, o.Username, o.Password = NoTLS, "panel", "s3cret"
		},
	} {
		o := ok
		mutate(&o)
		if _, err := NewSender(o); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	for _, addr := range []string{"localhost:25", "127.0.0.1:25", "[::1]:25"} {
		o := ok
		o.Addr, o.Security, o.Username, o.Password = addr, NoTLS, "panel", "s3cret"
		if _, err := NewSender(o); err != nil {
			t.Errorf("%s: %v", addr, err)
		}
	}
}
