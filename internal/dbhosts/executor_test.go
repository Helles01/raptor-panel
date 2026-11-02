package dbhosts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

const secret = "S3cretPassw0rd!"

var good = Conn{Host: "db.example.com", Port: 3306, Username: "panel", Password: secret}

type call struct {
	query string
	args  []any
}

type fakeSession struct {
	d *fakeDialer
}

func (s fakeSession) Exec(ctx context.Context, query string, args ...any) error {
	s.d.calls = append(s.d.calls, call{query, args})
	if err, ok := s.d.failAt[len(s.d.calls)]; ok {
		return err
	}
	return nil
}

func (s fakeSession) Close() error { s.d.closed++; return nil }

type fakeDialer struct {
	dialed  []Conn
	calls   []call
	closed  int
	dialErr error
	failAt  map[int]error // by the number of the call, from 1
	block   bool
}

func (d *fakeDialer) Dial(ctx context.Context, c Conn) (Session, error) {
	d.dialed = append(d.dialed, c)
	if d.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if d.dialErr != nil {
		return nil, d.dialErr
	}
	return fakeSession{d}, nil
}

type fakeLookup struct {
	conns map[uint64]Conn
	asked []uint64
}

func (l *fakeLookup) Conn(_ context.Context, id uint64) (Conn, error) {
	l.asked = append(l.asked, id)
	c, ok := l.conns[id]
	if !ok {
		return Conn{}, ErrNoHost
	}
	return c, nil
}

func statements() []Statement {
	return []Statement{
		{SQL: "CREATE DATABASE `s1_a`"},
		{SQL: "CREATE USER ?@?", Args: []any{"u1_a", "%"}},
		{SQL: "GRANT ALL ON `s1_a`.* TO ?"},
	}
}

func newExecutor(t *testing.T, d *fakeDialer, l Lookup, timeout time.Duration) *Executor {
	t.Helper()
	e, err := NewExecutor(l, d, timeout)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestRunOnExecutesInOrderAndCloses(t *testing.T) {
	d := &fakeDialer{}
	if err := newExecutor(t, d, nil, 0).RunOn(context.Background(), good, statements()); err != nil {
		t.Fatal(err)
	}
	if len(d.dialed) != 1 || d.dialed[0] != good || d.closed != 1 {
		t.Errorf("dialed %v, closed %d", d.dialed, d.closed)
	}
	want := []call{{"CREATE DATABASE `s1_a`", nil}, {"CREATE USER ?@?", []any{"u1_a", "%"}}, {"GRANT ALL ON `s1_a`.* TO ?", nil}}
	if len(d.calls) != len(want) {
		t.Fatalf("calls = %v", d.calls)
	}
	for i := range want {
		if d.calls[i].query != want[i].query || !reflect.DeepEqual(d.calls[i].args, want[i].args) {
			t.Errorf("call %d = %v, want %v", i+1, d.calls[i], want[i])
		}
	}
}

func TestRunOnStopsAtTheFirstFailure(t *testing.T) {
	d := &fakeDialer{failAt: map[int]error{2: &ServerError{Number: 1396}}}
	err := newExecutor(t, d, nil, 0).RunOn(context.Background(), good, statements())
	var he *HostError
	if !errors.As(err, &he) || he.Step != 2 || he.Total != 3 || he.Number != 1396 {
		t.Fatalf("err = %#v", err)
	}
	if len(d.calls) != 2 || d.closed != 1 {
		t.Errorf("%d statements were run and %d connections closed", len(d.calls), d.closed)
	}
	if !strings.Contains(err.Error(), "statement 2 of 3") || !strings.Contains(err.Error(), "1396") {
		t.Errorf("message = %q", err)
	}
}

func TestNetworkErrorsKeepTheirCauseAndNamesNoPassword(t *testing.T) {
	boom := &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset by peer")}
	d := &fakeDialer{failAt: map[int]error{1: boom}}
	err := newExecutor(t, d, nil, 0).RunOn(context.Background(), good, statements())
	var he *HostError
	if !errors.As(err, &he) || he.Step != 1 || he.Number != 0 || !errors.Is(err, boom) {
		t.Fatalf("err = %#v", err)
	}
	if !strings.Contains(err.Error(), "connection reset") || strings.Contains(err.Error(), secret) {
		t.Errorf("message = %q", err)
	}
}

func TestConnectingFails(t *testing.T) {
	refused := errors.New("dial tcp 10.0.0.5:3306: connect: connection refused")
	for name, tc := range map[string]struct {
		err    error
		number uint16
		text   string
	}{
		"unreachable":   {refused, 0, "could not be reached: dial tcp"},
		"access denied": {&ServerError{Number: 1045}, 1045, "refused the connection (error 1045)"},
	} {
		d := &fakeDialer{dialErr: tc.err}
		err := newExecutor(t, d, nil, 0).RunOn(context.Background(), good, statements())
		var he *HostError
		if !errors.As(err, &he) || he.Step != 0 || he.Number != tc.number || !strings.Contains(err.Error(), tc.text) {
			t.Errorf("%s: err = %v", name, err)
		}
		if len(d.calls) != 0 || d.closed != 0 {
			t.Errorf("%s: statements ran or a connection that was never open was closed", name)
		}
	}
}

func TestConnectionIsCheckedBeforeDialing(t *testing.T) {
	for name, c := range map[string]Conn{
		"no host":      {Port: 3306, Username: "panel"},
		"bad host":     {Host: "db; DROP", Port: 3306, Username: "panel"},
		"port zero":    {Host: "db", Port: 0, Username: "panel"},
		"port too big": {Host: "db", Port: 65536, Username: "panel"},
		"no user":      {Host: "db", Port: 3306},
	} {
		d := &fakeDialer{}
		if err := newExecutor(t, d, nil, 0).RunOn(context.Background(), c, statements()); err == nil || len(d.dialed) != 0 {
			t.Errorf("%s: err = %v, dialed %d", name, err, len(d.dialed))
		}
	}
}

func TestTheWholeRunHasATimeLimit(t *testing.T) {
	d := &fakeDialer{block: true}
	start := time.Now()
	err := newExecutor(t, d, nil, 30*time.Millisecond).RunOn(context.Background(), good, statements())
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Errorf("err = %v after %v", err, time.Since(start))
	}
	// A request that is given up on ends the run too.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := newExecutor(t, &fakeDialer{block: true}, nil, time.Minute).RunOn(ctx, good, statements()); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: err = %v", err)
	}
}

func TestRunFindsTheHost(t *testing.T) {
	d := &fakeDialer{}
	l := &fakeLookup{conns: map[uint64]Conn{4: good}}
	e := newExecutor(t, d, l, 0)
	if err := e.Run(context.Background(), 4, statements()); err != nil || len(d.dialed) != 1 || d.dialed[0] != good {
		t.Fatalf("err = %v, dialed %v", err, d.dialed)
	}
	err := e.Run(context.Background(), 5, statements())
	if !errors.Is(err, ErrNoHost) || len(d.dialed) != 1 {
		t.Errorf("unknown host: err = %v, dialed %d", err, len(d.dialed))
	}
	if err := newExecutor(t, d, nil, 0).Run(context.Background(), 4, statements()); err == nil {
		t.Error("an executor without a lookup ran a host by number")
	}
}

func TestTest(t *testing.T) {
	d := &fakeDialer{}
	if err := newExecutor(t, d, nil, 0).Test(context.Background(), good); err != nil || len(d.calls) != 1 || d.calls[0].query != "SELECT 1" {
		t.Fatalf("err = %v, calls %v", err, d.calls)
	}
	d = &fakeDialer{dialErr: &ServerError{Number: 1045}}
	if err := newExecutor(t, d, nil, 0).Test(context.Background(), good); err == nil {
		t.Error("a host that refuses the account passed")
	}
}

func TestNewExecutor(t *testing.T) {
	if _, err := NewExecutor(nil, nil, 0); err == nil {
		t.Error("no dialer accepted")
	}
	if _, err := NewExecutor(nil, &fakeDialer{}, -time.Nanosecond); err == nil {
		t.Error("negative timeout accepted")
	}
	if e := newExecutor(t, &fakeDialer{}, nil, 0); e.timeout != DefaultTimeout {
		t.Errorf("timeout = %v", e.timeout)
	}
}

func TestConnNeverPrintsItsPassword(t *testing.T) {
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("test", "host", good)
	slog.New(slog.NewTextHandler(&logs, nil)).Info("test", "host", good)
	for name, out := range map[string]string{
		"%v": fmt.Sprintf("%v", good), "%+v": fmt.Sprintf("%+v", good), "%#v": fmt.Sprintf("%#v", good), "%s": fmt.Sprintf("%s", good),
		"pointer": fmt.Sprintf("%v", &good), "log": logs.String(),
	} {
		if strings.Contains(out, secret) {
			t.Errorf("%s printed the password: %s", name, out)
		}
	}
	if got := good.String(); got != "panel@db.example.com:3306" {
		t.Errorf("String = %q", got)
	}
}

func TestMySQLConfig(t *testing.T) {
	cfg := MySQLDialer{}.config(good)
	if cfg.User != "panel" || cfg.Passwd != secret || cfg.Addr != "db.example.com:3306" || cfg.Net != "tcp" {
		t.Errorf("account: %+v", cfg)
	}
	if cfg.TLSConfig != "false" || !cfg.InterpolateParams || cfg.MultiStatements || cfg.AllowAllFiles || cfg.AllowCleartextPasswords || cfg.AllowOldPasswords {
		t.Errorf("safety: %+v", cfg)
	}
	if cfg.Timeout != 10*time.Second || cfg.ReadTimeout != 30*time.Second || cfg.WriteTimeout != 30*time.Second || cfg.DBName != "" {
		t.Errorf("limits: %+v", cfg)
	}
	d, err := NewMySQLDialer("true", 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if cfg := d.config(good); cfg.TLSConfig != "true" || cfg.Timeout != 3*time.Second {
		t.Errorf("configured: %+v", cfg)
	}
}

func TestNewMySQLDialer(t *testing.T) {
	for _, mode := range append([]string{""}, TLSModes...) {
		if _, err := NewMySQLDialer(mode, 0); err != nil {
			t.Errorf("%q: %v", mode, err)
		}
	}
	for _, mode := range []string{"yes", "TLS", "custom", "skip_verify", " true"} {
		if _, err := NewMySQLDialer(mode, 0); err == nil {
			t.Errorf("%q accepted", mode)
		}
	}
}

func TestTranslateDropsTheTextOfTheHost(t *testing.T) {
	// The text of the host can repeat the part of the statement it did not understand, which holds a password.
	err := translate(fmt.Errorf("exec: %w", &mysql.MySQLError{Number: 1064, Message: "syntax error near '" + secret + "'"}))
	var se *ServerError
	if !errors.As(err, &se) || se.Number != 1064 || strings.Contains(err.Error(), secret) {
		t.Errorf("err = %#v", err)
	}
	other := errors.New("broken pipe")
	if translate(other) != other || translate(nil) != nil {
		t.Error("an error that is not the host's was changed")
	}
}

func TestDialToNothingFailsAsAnUnreachableHost(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no local listener:", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	e, err2 := NewExecutor(nil, MySQLDialer{ConnectTimeout: 2 * time.Second}, 10*time.Second)
	if err2 != nil {
		t.Fatal(err2)
	}
	err = e.Test(context.Background(), Conn{Host: "127.0.0.1", Port: port, Username: "panel", Password: secret})
	var he *HostError
	if !errors.As(err, &he) || he.Step != 0 || he.Number != 0 || strings.Contains(err.Error(), secret) {
		t.Errorf("err = %v", err)
	}
}

func TestValidateHostPassword(t *testing.T) {
	for name, tc := range map[string]struct {
		password string
		ok       bool
	}{
		"empty":         {"", true},
		"plain":         {"hunter2", true},
		"unicode":       {"jelszó-ő-ű", true},
		"symbols":       {`p"a'ss\w;ord`, true},
		"128 letters":   {strings.Repeat("a", 128), true},
		"129 letters":   {strings.Repeat("a", 129), false},
		"newline":       {"a\nb", false},
		"nul":           {"a\x00b", false},
		"delete":        {"a\x7fb", false},
		"invalid utf-8": {"a\xffb", false},
	} {
		if err := ValidateHostPassword(tc.password); (err == nil) != tc.ok {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestChangeOwnPassword(t *testing.T) {
	st, err := ChangeOwnPassword("abcdEFGH1234")
	if err != nil || len(st) != 1 || st[0].SQL != "ALTER USER CURRENT_USER() IDENTIFIED BY ?" || !reflect.DeepEqual(st[0].Args, []any{"abcdEFGH1234"}) {
		t.Fatalf("st = %v, err = %v", st, err)
	}
	// The password is an argument; whatever it holds, the text of the statement is the same.
	for _, bad := range []string{"", "short", "has space in it", "quote'quote12", strings.Repeat("a", 65)} {
		if st, err := ChangeOwnPassword(bad); err == nil || st != nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
