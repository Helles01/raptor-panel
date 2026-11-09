// Package admincli is the command line of the administrator: `user`, `apikey`, `location`, `node`, `nest`, `egg` and `settings`. It reads
// the arguments, calls internal/admin and writes what came out. A secret that was made (a password, a key, the token of a node) goes to
// standard output, once, and nothing else does; what the command says about itself goes to standard error, so that the output can be
// put in a file or a pipe as it is. A password is never taken from an argument, which every process on the machine can read.
package admincli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Helles01/raptor-panel/internal/admin"
	"github.com/Helles01/raptor-panel/internal/nodes"
	"github.com/Helles01/raptor-panel/internal/settings"
)

// Env is what a command reads and writes.
type Env struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Open opens a file named on the command line; os.Open when nil.
	Open func(path string) (io.ReadCloser, error)
}

// Handler is a command.
type Handler func(ctx context.Context, e Env, args []string) error

// UsageError is a mistake in how a command was called. The program answers it with the usage and the exit code 2.
type UsageError struct{ Msg string }

func (u UsageError) Error() string { return u.Msg }

func usagef(format string, a ...any) error { return UsageError{fmt.Sprintf(format, a...)} }

// Commands returns the commands, by group and name: "user" → "create" and so on.
func Commands(s *admin.Service) map[string]map[string]Handler {
	c := &commands{s: s}
	return map[string]map[string]Handler{
		"user":     {"create": c.userCreate, "delete": c.userDelete, "disable-2fa": c.userDisable2FA, "reset-2fa": c.userReset2FA},
		"apikey":   {"create": c.apikeyCreate},
		"location": {"create": c.locationCreate},
		"node":     {"create": c.nodeCreate},
		"nest":     {"create": c.nestCreate},
		"egg":      {"import": c.eggImport},
		"settings": {"set": c.settingsSet, "list": c.settingsList, "set-mail-password": c.settingsMailPassword},
	}
}

type commands struct{ s *admin.Service }

// flags makes the flag set of a command. A mistake in the flags is a usage error, and the flag package writes nothing of its own.
func flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parse reads the flags and returns the arguments that are not flags. The flag package stops at the first argument that is not a flag,
// so `dbhost update 3 --name x` would lose its flags; parse goes on after each such argument, and "--" still ends the flags.
func parse(fs *flag.FlagSet, args []string, positional int) ([]string, error) {
	return parseRange(fs, args, positional, positional)
}

// parseRange is parse for a command that takes between lo and hi arguments that are not flags.
func parseRange(fs *flag.FlagSet, args []string, lo, hi int) ([]string, error) {
	var rest []string
	for {
		if len(args) > 0 && args[0] == "--" {
			rest = append(rest, args[1:]...)
			break
		}
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, usagef("%s", usageOf(fs))
			}
			return nil, usagef("%v", err)
		}
		if fs.NArg() == 0 {
			break
		}
		rest = append(rest, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if lo == hi && len(rest) != lo {
		return nil, usagef("%s takes %d argument(s), not %d", fs.Name(), lo, len(rest))
	}
	if len(rest) < lo || len(rest) > hi {
		return nil, usagef("%s takes %d to %d arguments, not %d", fs.Name(), lo, hi, len(rest))
	}
	return rest, nil
}

func usageOf(fs *flag.FlagSet) string {
	var b strings.Builder
	b.WriteString("usage: raptor " + fs.Name() + " [flags]\n")
	fs.SetOutput(&b)
	fs.PrintDefaults()
	fs.SetOutput(io.Discard)
	return strings.TrimRight(b.String(), "\n")
}

// list is a flag that may be given more than once.
type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

func require(fs *flag.FlagSet, names ...string) error {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	for _, n := range names {
		if !set[n] {
			return usagef("%s: --%s is required", fs.Name(), n)
		}
	}
	return nil
}

// readLine reads one line of the input, without its line end: for a password given on standard input.
func readLine(in io.Reader) (string, error) {
	line, err := bufio.NewReader(io.LimitReader(in, 4096)).ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", errors.New("no password was read from the standard input")
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		// An empty password is no password: the account would be given one that nobody asked for.
		return "", errors.New("the password read from the standard input is empty")
	}
	return line, nil
}

func (c *commands) userCreate(ctx context.Context, e Env, args []string) error {
	fs := flags("user create")
	var in admin.UserInput
	fs.StringVar(&in.Email, "email", "", "the e-mail address")
	fs.StringVar(&in.Username, "username", "", "the user name")
	fs.StringVar(&in.FirstName, "first-name", "", "the first name")
	fs.StringVar(&in.LastName, "last-name", "", "the last name")
	fs.StringVar(&in.Language, "language", "", "the language (en by default)")
	fs.BoolVar(&in.Admin, "admin", false, "make the user an administrator")
	fromStdin := fs.Bool("password-stdin", false, "read the password from the first line of the standard input (otherwise one is made)")
	if _, err := parse(fs, args, 0); err != nil {
		return err
	}
	if err := require(fs, "email", "username", "first-name", "last-name"); err != nil {
		return err
	}
	if *fromStdin {
		p, err := readLine(e.Stdin)
		if err != nil {
			return err
		}
		in.Password = p
	}
	u, err := c.s.CreateUser(ctx, in)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.Stderr, "Created user %s (id %d, uuid %s).\n", u.Username, u.ID, u.UUID)
	if u.Password != "" {
		fmt.Fprintln(e.Stderr, "The password is on the standard output. It is not kept anywhere else.")
		fmt.Fprintln(e.Stdout, u.Password)
	}
	return nil
}

func (c *commands) userDelete(ctx context.Context, e Env, args []string) error {
	rest, err := parse(flags("user delete"), args, 1)
	if err != nil {
		return err
	}
	u, err := c.s.DeleteUser(ctx, rest[0])
	if err != nil {
		return err
	}
	fmt.Fprintf(e.Stderr, "Deleted user %s (id %d).\n", u.Username, u.ID)
	return nil
}

func (c *commands) userDisable2FA(ctx context.Context, e Env, args []string) error {
	rest, err := parse(flags("user disable-2fa"), args, 1)
	if err != nil {
		return err
	}
	u, err := c.s.DisableTwoFactor(ctx, rest[0])
	if err != nil {
		return err
	}
	fmt.Fprintf(e.Stderr, "Two-factor authentication is off for %s (id %d).\n", u.Username, u.ID)
	return nil
}

func (c *commands) apikeyCreate(ctx context.Context, e Env, args []string) error {
	fs := flags("apikey create")
	var in admin.KeyInput
	var access, ips list
	fs.StringVar(&in.User, "user", "", "the user the key is for: id, e-mail address or user name")
	fs.BoolVar(&in.Application, "application", false, "a key of the Application API, for an administrator (otherwise a key of the account)")
	fs.StringVar(&in.Description, "description", "", "what the key is for")
	fs.Var(&access, "access", "a level on a resource, such as servers=rw or nodes=read (for an application key; give it more than once)")
	fs.Var(&ips, "allow-ip", "an address or network the key may be used from (give it more than once; none means from anywhere)")
	if _, err := parse(fs, args, 0); err != nil {
		return err
	}
	if err := require(fs, "user", "description"); err != nil {
		return err
	}
	in.Access, in.AllowedIPs = access, ips
	k, err := c.s.CreateKey(ctx, in)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.Stderr, "Created the key %s. The whole key is on the standard output; it cannot be found out again.\n", k.Identifier)
	fmt.Fprintln(e.Stdout, k.Token)
	return nil
}

func (c *commands) locationCreate(ctx context.Context, e Env, args []string) error {
	fs := flags("location create")
	short := fs.String("short", "", "the short name, such as eu.ams")
	long := fs.String("long", "", "a longer description")
	if _, err := parse(fs, args, 0); err != nil {
		return err
	}
	if err := require(fs, "short"); err != nil {
		return err
	}
	id, err := c.s.CreateLocation(ctx, *short, *long)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.Stderr, "Created location %s (id %d).\n", *short, id)
	return nil
}

func (c *commands) nodeCreate(ctx context.Context, e Env, args []string) error {
	fs := flags("node create")
	n := nodes.DefaultNewNode()
	var location uint64
	fs.StringVar(&n.Name, "name", "", "the name of the node")
	fs.StringVar(&n.Description, "description", "", "a description")
	fs.Uint64Var(&location, "location-id", 0, "the id of the location")
	fs.StringVar(&n.FQDN, "fqdn", "", "the host name or address the node is reached at")
	fs.StringVar(&n.Scheme, "scheme", n.Scheme, "http or https")
	fs.Int64Var(&n.Memory, "memory", 0, "the memory servers may use, in MB")
	fs.Int64Var(&n.Disk, "disk", 0, "the disk servers may use, in MB")
	fs.Int64Var(&n.MemoryOverallocate, "memory-overallocate", 0, "per cent over the memory, or -1 for no check")
	fs.Int64Var(&n.DiskOverallocate, "disk-overallocate", 0, "per cent over the disk, or -1 for no check")
	fs.StringVar(&n.DaemonBase, "daemon-base", n.DaemonBase, "where the node keeps the files of servers")
	fs.IntVar(&n.DaemonSFTP, "sftp-port", n.DaemonSFTP, "the SFTP port")
	fs.IntVar(&n.DaemonListen, "listen-port", n.DaemonListen, "the port of the node's API")
	fs.Int64Var(&n.UploadSize, "upload-size", n.UploadSize, "the largest upload through the browser, in MB")
	fs.BoolVar(&n.BehindProxy, "behind-proxy", false, "the node is behind a proxy that does the TLS")
	fs.BoolVar(&n.Public, "public", n.Public, "the node may be chosen for new servers")
	if _, err := parse(fs, args, 0); err != nil {
		return err
	}
	if err := require(fs, "name", "location-id", "fqdn", "memory", "disk"); err != nil {
		return err
	}
	n.LocationID = location
	made, err := c.s.CreateNode(ctx, n)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.Stderr, "Created node %s (id %d, uuid %s).\nIts configuration, token included, is on the standard output: save it as config.yml on the node. It cannot be found out again.\n", n.Name, made.ID, made.UUID)
	_, err = e.Stdout.Write(made.Config)
	return err
}

func (c *commands) nestCreate(ctx context.Context, e Env, args []string) error {
	fs := flags("nest create")
	name := fs.String("name", "", "the name of the nest")
	author := fs.String("author", "", "the e-mail address of its author")
	description := fs.String("description", "", "a description")
	if _, err := parse(fs, args, 0); err != nil {
		return err
	}
	if err := require(fs, "name", "author"); err != nil {
		return err
	}
	id, err := c.s.CreateNest(ctx, *name, *description, *author)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.Stderr, "Created nest %s (id %d).\n", strings.TrimSpace(*name), id)
	return nil
}

func (c *commands) eggImport(ctx context.Context, e Env, args []string) error {
	fs := flags("egg import")
	nest := fs.Uint64("nest-id", 0, "the id of the nest the egg goes in")
	rest, err := parse(fs, args, 1)
	if err != nil {
		return err
	}
	if err := require(fs, "nest-id"); err != nil {
		return err
	}
	var in io.Reader = e.Stdin
	if rest[0] != "-" {
		open := e.Open
		if open == nil {
			open = func(p string) (io.ReadCloser, error) { return os.Open(p) }
		}
		f, err := open(rest[0])
		if err != nil {
			return fmt.Errorf("open the egg file: %w", err)
		}
		defer f.Close()
		in = f
	}
	egg, err := c.s.ImportEgg(ctx, *nest, in)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.Stderr, "Imported the egg %s (id %d) with %d variable(s).\n", egg.Name, egg.ID, egg.Variables)
	return nil
}

func (c *commands) settingsSet(ctx context.Context, e Env, args []string) error {
	rest, err := parse(flags("settings set"), args, 2)
	if err != nil {
		return err
	}
	if err := c.s.SetSetting(ctx, rest[0], rest[1]); err != nil {
		return err
	}
	fmt.Fprintf(e.Stderr, "Set %s.\n", rest[0])
	return nil
}

// settingsList names the settings that can be changed, one a line.
func (c *commands) settingsList(_ context.Context, e Env, args []string) error {
	if _, err := parse(flags("settings list"), args, 0); err != nil {
		return err
	}
	for _, k := range settings.Keys() {
		fmt.Fprintln(e.Stdout, k)
	}
	return nil
}

// settingsMailPassword sets or removes the password of the mail server. It is read from the standard input, never from an argument, which every
// process on the machine can read, and it is not one of the settings that can be listed or shown.
func (c *commands) settingsMailPassword(ctx context.Context, e Env, args []string) error {
	fs := flags("settings set-mail-password")
	fromStdin := fs.Bool("password-stdin", false, "read the password from the first line of the standard input")
	clear := fs.Bool("clear", false, "remove the password, for a mail server that needs none")
	if _, err := parse(fs, args, 0); err != nil {
		return err
	}
	if *fromStdin == *clear {
		return usagef("settings set-mail-password: give --password-stdin or --clear, one of them")
	}
	if *clear {
		if err := c.s.ClearMailPassword(ctx); err != nil {
			return err
		}
		fmt.Fprintln(e.Stderr, "Removed the password of the mail server.")
		return nil
	}
	password, err := readLine(e.Stdin)
	if err != nil {
		return err
	}
	if err := c.s.SetMailPassword(ctx, password); err != nil {
		return err
	}
	fmt.Fprintln(e.Stderr, "Set the password of the mail server. Mail is sent with it from the next message on.")
	return nil
}

// userReset2FA removes the second factor of everybody. It is for the cutover, when the secrets of the old panel count as known to others, and it asks for --yes
// because it cannot be taken back: everybody who used a code has to set it up again.
func (c *commands) userReset2FA(ctx context.Context, e Env, args []string) error {
	fs := flags("user reset-2fa")
	all := fs.Bool("all", false, "every user (for one user there is disable-2fa)")
	yes := fs.Bool("yes", false, "really do it (without it nothing is changed, and the number of users who would be affected is shown)")
	require := fs.String("require", "", "afterwards, who must set it up again: none, admins or everyone (the setting pterodactyl:auth:2fa_required); left as it is when not given")
	if _, err := parse(fs, args, 0); err != nil {
		return err
	}
	if !*all {
		return usagef("user reset-2fa: --all is required (to take the second factor of one user, use user disable-2fa)")
	}
	level := ""
	switch *require {
	case "":
	case "none":
		level = "0"
	case "admins":
		level = "1"
	case "everyone":
		level = "2"
	default:
		return usagef("user reset-2fa: --require is none, admins or everyone")
	}
	if *require != "" && !*yes {
		return usagef("user reset-2fa: --require goes with --yes")
	}
	n, err := c.s.ResetAllTwoFactor(ctx, *yes)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.Stderr, "Removed the second factor of %d user(s). They sign in with their password and set it up again.\n", n)
	if level != "" {
		if err := c.s.SetSetting(ctx, "pterodactyl:auth:2fa_required", level); err != nil {
			return fmt.Errorf("the second factors are removed, but the requirement could not be set: %w", err)
		}
		fmt.Fprintf(e.Stderr, "Two-factor authentication is now required of: %s.\n", *require)
	} else {
		fmt.Fprintln(e.Stderr, "Whether they must is the setting pterodactyl:auth:2fa_required (raptor settings set, or run this again with --require).")
	}
	return nil
}
