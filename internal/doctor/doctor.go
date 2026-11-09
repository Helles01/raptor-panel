// Package doctor looks at a panel that is about to be started, or has just been set up, and says what is wrong with it before a user finds out: the
// configuration, the key, the address, the frontend build, the database and its schema, the secrets that were not converted, the nodes that cannot be reached
// with the token they have, the mail, and the clock. Nothing here changes anything, and nothing here prints a secret.
//
// The checks are plain functions of what they look at, so each is tried with what is wrong; the command (cmd/raptor) gathers the facts and prints.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Helles01/raptor-panel/internal/config"
	"github.com/Helles01/raptor-panel/internal/mail"
	"github.com/Helles01/raptor-panel/internal/secretmigration"
)

// Level is how bad a finding is.
type Level int

const (
	// OK: nothing to do.
	OK Level = iota
	// Warn: the panel runs, but something is likely not what was meant.
	Warn
	// Fail: the panel cannot do what it is for, or will not start.
	Fail
)

func (l Level) String() string { return [...]string{"ok", "warn", "FAIL"}[l] }

// Result is one finding.
type Result struct {
	Name   string
	Level  Level
	Detail string
}

// Worst is the worst level among results; OK for none.
func Worst(rs []Result) Level {
	w := OK
	for _, r := range rs {
		if r.Level > w {
			w = r.Level
		}
	}
	return w
}

func res(name string, l Level, format string, a ...any) Result {
	return Result{Name: name, Level: l, Detail: fmt.Sprintf(format, a...)}
}

// Config checks the configuration, section by section, and what is likely to be a slip in it. cfg is as config.Load made it.
func Config(cfg config.Config) []Result {
	var out []Result
	sections := []struct {
		name string
		v    interface{ Validate() error }
	}{
		{"app", cfg.App}, {"db", cfg.DB}, {"http", cfg.HTTP}, {"wings", cfg.Wings}, {"log", cfg.Log}, {"jobs", cfg.Jobs}, {"web", cfg.Web},
		{"db_hosts", cfg.DBHosts}, {"backups", cfg.Backups}, {"features", cfg.Features},
	}
	var bad []string
	for _, s := range sections {
		if err := s.v.Validate(); err != nil {
			out = append(out, res("configuration: "+s.name, Fail, "%v", err))
			bad = append(bad, s.name)
		}
	}
	if len(bad) == 0 {
		out = append(out, res("configuration", OK, "every section is valid"))
	}

	if cfg.Secrets.Key == "" {
		out = append(out, res("secret key", Fail, "RAPTOR_SECRET_KEY is not set (make one with `raptor keygen`)"))
	} else if _, err := cfg.Secrets.KeyBytes(); err != nil {
		out = append(out, res("secret key", Fail, "RAPTOR_SECRET_KEY is not a key `raptor keygen` makes"))
	} else {
		out = append(out, res("secret key", OK, "set, and of the right form"))
	}

	if u, err := url.Parse(cfg.App.URL); err == nil && u.Scheme == "http" && !isLoopback(u.Hostname()) {
		out = append(out, res("address", Warn, "app.url is %s: the session cookie is then not marked Secure and travels in the clear (use https)", cfg.App.Origin()))
	} else if err == nil && u.Host != "" {
		out = append(out, res("address", OK, "%s", cfg.App.Origin()))
	}

	if prefixes, err := cfg.HTTP.TrustedPrefixes(); err == nil {
		switch u, _ := url.Parse(cfg.App.URL); {
		case len(prefixes) == 0 && u != nil && u.Scheme == "https":
			out = append(out, res("trusted proxies", Warn, "none is trusted, but the address is https, which usually means a proxy in front: every client then looks like the proxy, and the limits and the IP restrictions of keys apply to everybody at once (set RAPTOR_TRUSTED_PROXIES)"))
		case len(prefixes) == 0:
			out = append(out, res("trusted proxies", OK, "none (the address a client connects from is believed)"))
		default:
			out = append(out, res("trusted proxies", OK, "%d trusted", len(prefixes)))
		}
	}

	var dev []string
	if cfg.Web.Reload {
		dev = append(dev, "web.reload (a new build of the frontend is served at once)")
	}
	if cfg.Wings.InsecureTLS {
		dev = append(dev, "wings.insecure_tls (the certificates of the nodes are not checked)")
	}
	if len(dev) > 0 {
		out = append(out, res("development options", Warn, "on: %s; they are for a test machine", strings.Join(dev, ", ")))
	}

	if cfg.Backups.S3Configured() {
		if err := cfg.Backups.S3Config().Validate(); err == nil {
			out = append(out, res("backups on S3", OK, "the settings are valid; new backups go to %s (`raptor backups check` tries the bucket)", driverName(cfg.Backups.Driver)))
		}
	} else if cfg.Backups.Driver == "wings" {
		out = append(out, res("backups on S3", OK, "not set up: backups are kept on the nodes"))
	}
	return out
}

func driverName(d string) string {
	if d == "s3" {
		return "the bucket"
	}
	return "the node"
}

func isLoopback(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Listen checks that the panel can bind its address. A panel that is already running holds it, which is said as a warning: doctor is run on a panel that is
// stopped, or to look at one that runs, and either is a fine thing to do.
func Listen(addr string, listen func(network, address string) (net.Listener, error)) Result {
	l, err := listen("tcp", addr)
	if err != nil {
		return res("listen address", Warn, "%s cannot be bound (%s): something already holds it; is the panel running?", addr, simplify(err))
	}
	_ = l.Close()
	return res("listen address", OK, "%s is free", addr)
}

// simplify cuts an error of the network down to its cause.
func simplify(err error) string {
	var op *net.OpError
	if errors.As(err, &op) && op.Err != nil {
		return op.Err.Error()
	}
	return err.Error()
}

// Frontend says what loading the build of the frontend came to.
func Frontend(dir string, err error) Result {
	if err != nil {
		return res("frontend build", Fail, "%v", err)
	}
	return res("frontend build", OK, "%s is complete and every file is the one its manifest was made for", dir)
}

// Database is what the checks of the database ask. The command answers from a real database; the tests answer with what they want to see.
type Database interface {
	// Version is the version of the server.
	Version(ctx context.Context) (string, error)
	// Now is the time on the database server, in UTC.
	Now(ctx context.Context) (time.Time, error)
	// Schema says how far the schema is from the one the panel is written against.
	Schema(ctx context.Context) (Schema, error)
	// PendingSecrets lists, for every column of the old panel that was encrypted, how many values are not converted.
	PendingSecrets(ctx context.Context) ([]secretmigration.PendingColumn, error)
	// Nodes lists the nodes with whether each has a token the panel can use.
	Nodes(ctx context.Context) ([]Node, error)
	// Admins is the number of administrators.
	Admins(ctx context.Context) (int, error)
	// Mail returns the mail settings (the keys of mail.SettingKeys) and whether a password for the mail server is kept.
	Mail(ctx context.Context) (values map[string]string, hasPassword bool, err error)
}

// Schema is the state of the tables.
type Schema struct {
	// MissingReference and UnknownReference are the migrations of the reference that are missing, and the ones the database has that are not known.
	MissingReference, UnknownReference int
	// PendingOwn are the migrations of Raptor's own tables that have not been run.
	PendingOwn []string
}

// Node is a node as the check sees it.
type Node struct {
	ID       uint64
	Name     string
	HasToken bool
}

// Checks runs the checks of the database in order. A database that cannot be reached ends them with one failure.
func Checks(ctx context.Context, d Database, now time.Time) []Result {
	var out []Result
	version, err := d.Version(ctx)
	if err != nil {
		return []Result{res("database connection", Fail, "%s", cut(err))}
	}
	out = append(out, res("database connection", OK, "connected, server %s", cleanVersion(version)))

	if at, err := d.Now(ctx); err == nil {
		skew := at.Sub(now)
		if skew < 0 {
			skew = -skew
		}
		if skew > 5*time.Minute {
			out = append(out, res("clock", Warn, "the clock of this machine and the clock of the database differ by %s: the lifetimes of sessions, tokens and addresses are counted by both", skew.Round(time.Second)))
		} else {
			out = append(out, res("clock", OK, "this machine and the database agree to %s", skew.Round(time.Second)))
		}
	}

	schema, err := d.Schema(ctx)
	switch {
	case err != nil:
		out = append(out, res("schema", Fail, "%s", cut(err)))
		return out
	case schema.MissingReference > 0 || schema.UnknownReference > 0:
		out = append(out, res("schema", Fail, "the database is not the schema of the reference this panel is written against (%d migrations missing, %d unknown): `raptor db check` lists them", schema.MissingReference, schema.UnknownReference))
		return out
	case len(schema.PendingOwn) > 0:
		out = append(out, res("schema", Fail, "%d migrations of Raptor's own tables are pending: run `raptor db migrate`", len(schema.PendingOwn)))
		return out
	default:
		out = append(out, res("schema", OK, "the schema of the reference, and Raptor's own tables, are up to date"))
	}

	out = append(out, adminCheck(ctx, d))
	out = append(out, secretChecks(ctx, d)...)
	out = append(out, nodeChecks(ctx, d)...)
	out = append(out, mailCheck(ctx, d))
	return out
}

func cut(err error) string {
	s := strings.TrimSpace(err.Error())
	if len(s) > 300 {
		s = s[:300]
	}
	return strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

func cleanVersion(v string) string {
	if len(v) > 60 {
		v = v[:60]
	}
	return strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return ' '
		}
		return r
	}, v)
}

func adminCheck(ctx context.Context, d Database) Result {
	n, err := d.Admins(ctx)
	switch {
	case err != nil:
		return res("administrators", Fail, "%s", cut(err))
	case n == 0:
		return res("administrators", Fail, "there is no administrator: make one with `raptor user create --admin`")
	default:
		return res("administrators", OK, "%d", n)
	}
}

// effects says what a value that is not converted costs, by column.
var effects = map[string]string{
	"nodes.daemon_token":      "the panel cannot reach those nodes, and they cannot call it",
	"api_keys.token":          "those keys of the old panel do not work (new keys can be made)",
	"users.totp_secret":       "those users cannot sign in with a code (`raptor user disable-2fa` lets them in to set it up again)",
	"database_hosts.password": "the panel cannot sign in to those database hosts",
	"databases.password":      "the passwords of those databases cannot be shown or changed",
}

func secretChecks(ctx context.Context, d Database) []Result {
	cols, err := d.PendingSecrets(ctx)
	if err != nil {
		return []Result{res("old secrets", Fail, "%s", cut(err))}
	}
	var out []Result
	pending := 0
	for _, c := range cols {
		if c.Pending == 0 {
			continue
		}
		pending += c.Pending
		effect := effects[c.Column.Table+"."+c.Column.Column]
		if c.Column.SettingsKey != "" {
			effect = "mail is sent without a password"
		}
		out = append(out, res("old secrets: "+c.Column.String(), Warn, "%d of %d are not converted: %s (run `raptor migrate-secrets`, or replace them)", c.Pending, c.Total, effect))
	}
	if pending == 0 {
		out = append(out, res("old secrets", OK, "no value of the old panel is waiting to be converted"))
	}
	return out
}

func nodeChecks(ctx context.Context, d Database) []Result {
	nodes, err := d.Nodes(ctx)
	if err != nil {
		return []Result{res("nodes", Fail, "%s", cut(err))}
	}
	if len(nodes) == 0 {
		return []Result{res("nodes", Warn, "there is no node: servers need one (`raptor node create`)")}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	var out []Result
	without := 0
	for _, n := range nodes {
		if !n.HasToken {
			without++
			out = append(out, res(fmt.Sprintf("node %d", n.ID), Warn, "%q has no token the panel can use: give it one with `raptor node rotate-token %d` and put the new config.yml on the node", cut(errors.New(n.Name)), n.ID))
		}
	}
	if without == 0 {
		out = append(out, res("nodes", OK, "%d, each with a token", len(nodes)))
	}
	return out
}

func mailCheck(ctx context.Context, d Database) Result {
	values, hasPassword, err := d.Mail(ctx)
	if err != nil {
		return res("mail", Fail, "%s", cut(err))
	}
	opts, err := mail.OptionsFromSettings(func(k string) string { return values[k] }, "", "localhost")
	switch {
	case errors.Is(err, mail.ErrNotConfigured):
		return res("mail", Warn, "no mail server is set up (`raptor settings set mail:mailers:smtp:host …`): the mails of the panel (password resets, invitations, finished installations) are not sent")
	case err != nil:
		return res("mail", Fail, "the settings are wrong: %s", cut(err))
	}
	switch {
	case opts.Username != "" && !hasPassword:
		return res("mail", Warn, "a user name is set for %s but no password: `raptor settings set-mail-password --password-stdin`", opts.Addr)
	case opts.Security == mail.NoTLS && opts.Username != "":
		return res("mail", Warn, "%s is used without encryption: a password would cross the network in the clear", opts.Addr)
	default:
		return res("mail", OK, "%s", opts.Addr)
	}
}

// Unreachable is the finding for a database that could not be opened at all.
func Unreachable(err error) Result {
	return res("database connection", Fail, "%s", cut(err))
}

// Skipped is the finding for the checks of the database that were not made because its settings are wrong: the configuration says why.
func Skipped() Result {
	return res("database", Warn, "not looked at: its settings are not valid (see the configuration above)")
}
