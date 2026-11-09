package admincli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/Helles01/raptor-panel/internal/admin"
	"github.com/Helles01/raptor-panel/internal/auth"
)

// Services are the parts the commands are made of. A part that is nil leaves out its commands, so a program that has no way to reach
// the database hosts, for one, does not offer them.
type Services struct {
	Admin     *admin.Service
	Hosts     *admin.HostService
	Directory *admin.Directory
	Rotation  *admin.Rotation
	Databases *admin.DatabaseRotation
}

// All returns the commands of every part that is there, by group and name. A group that two parts share ("apikey") has the commands of
// both.
func All(s Services) map[string]map[string]Handler {
	out := map[string]map[string]Handler{}
	add := func(from map[string]map[string]Handler) {
		for group, verbs := range from {
			if out[group] == nil {
				out[group] = map[string]Handler{}
			}
			for verb, h := range verbs {
				out[group][verb] = h
			}
		}
	}
	if s.Admin != nil {
		add(Commands(s.Admin))
	}
	if s.Hosts != nil {
		add(hostCommands(s.Hosts))
	}
	if s.Directory != nil {
		add(directoryCommands(s.Directory))
	}
	if s.Rotation != nil {
		add(rotationCommands(s.Rotation))
	}
	if s.Databases != nil {
		add(databaseRotationCommands(s.Databases))
	}
	return out
}

// cell makes a value from the database fit in a table on a terminal: a control character, which could move the cursor or change the
// colours, is shown as "?", and a tab or a line end, which would break the table, as a space.
func cell(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			return ' '
		case unicode.IsControl(r):
			return '?'
		}
		return r
	}, s)
}

func table(out io.Writer, header string, rows [][]string) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, header)
	for _, row := range rows {
		cells := make([]string, len(row))
		for i, c := range row {
			cells[i] = cell(c)
		}
		fmt.Fprintln(w, strings.Join(cells, "\t"))
	}
	return w.Flush()
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// number reads a positive number from an argument.
func number(what, arg string) (uint64, error) {
	n, err := strconv.ParseUint(arg, 10, 64)
	if err != nil || n == 0 {
		return 0, usagef("%s must be a number above 0", what)
	}
	return n, nil
}

// --- the database hosts

func hostCommands(h *admin.HostService) map[string]map[string]Handler {
	c := &hostCmds{h: h}
	return map[string]map[string]Handler{
		"dbhost": {"create": c.create, "list": c.list, "test": c.test, "update": c.update, "rotate-password": c.rotate, "delete": c.delete},
	}
}

type hostCmds struct{ h *admin.HostService }

func (c *hostCmds) create(ctx context.Context, e Env, args []string) error {
	fs := flags("dbhost create")
	in := admin.HostInput{}
	var node uint64
	fs.StringVar(&in.Name, "name", "", "a name for the host")
	fs.StringVar(&in.Host, "host", "", "the host name or address of the database host")
	fs.IntVar(&in.Port, "port", 3306, "its port")
	fs.StringVar(&in.Username, "username", "", "the account the panel manages the host with")
	fs.Uint64Var(&node, "node-id", 0, "the node the host is for (none: any server may use it where the panel allows that)")
	fromStdin := fs.Bool("password-stdin", false, "read the password of the account from the first line of the standard input")
	noPassword := fs.Bool("no-password", false, "the account has no password")
	if _, err := parse(fs, args, 0); err != nil {
		return err
	}
	if err := require(fs, "name", "host", "username"); err != nil {
		return err
	}
	if *fromStdin == *noPassword {
		return usagef("dbhost create: give either --password-stdin or --no-password")
	}
	if *fromStdin {
		p, err := readLine(e.Stdin)
		if err != nil {
			return err
		}
		in.Password = p
	}
	if node != 0 {
		in.NodeID = &node
	}
	id, err := c.h.CreateHost(ctx, in)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.Stderr, "Created database host %s (id %d). The panel signed in to it.\n", strings.TrimSpace(in.Name), id)
	return nil
}

func (c *hostCmds) list(ctx context.Context, e Env, args []string) error {
	if _, err := parse(flags("dbhost list"), args, 0); err != nil {
		return err
	}
	hosts, err := c.h.ListHosts(ctx)
	if err != nil {
		return err
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].ID < hosts[j].ID })
	rows := make([][]string, 0, len(hosts))
	for _, h := range hosts {
		node := "-"
		if h.NodeID != nil {
			node = strconv.FormatUint(*h.NodeID, 10)
		}
		rows = append(rows, []string{strconv.FormatUint(h.ID, 10), h.Name, h.Host + ":" + strconv.Itoa(h.Port), h.Username, node, strconv.FormatInt(h.Databases, 10)})
	}
	return table(e.Stdout, "ID\tNAME\tADDRESS\tUSER\tNODE\tDATABASES", rows)
}

func (c *hostCmds) test(ctx context.Context, e Env, args []string) error {
	rest, err := parse(flags("dbhost test"), args, 1)
	if err != nil {
		return err
	}
	id, err := number("the id of the host", rest[0])
	if err != nil {
		return err
	}
	if err := c.h.TestHost(ctx, id); err != nil {
		return err
	}
	fmt.Fprintf(e.Stderr, "The panel signed in to database host %d.\n", id)
	return nil
}

func (c *hostCmds) update(ctx context.Context, e Env, args []string) error {
	fs := flags("dbhost update")
	name := fs.String("name", "", "a new name")
	host := fs.String("host", "", "a new host name or address")
	port := fs.Int("port", 0, "a new port")
	username := fs.String("username", "", "a new account")
	node := fs.Uint64("node-id", 0, "link the host to this node")
	noNode := fs.Bool("no-node", false, "link the host to no node")
	fromStdin := fs.Bool("password-stdin", false, "store the password the account has on the host, read from the first line of the standard input (it changes nothing on the host)")
	rest, err := parse(fs, args, 1)
	if err != nil {
		return err
	}
	id, err := number("the id of the host", rest[0])
	if err != nil {
		return err
	}
	if *node != 0 && *noNode {
		return usagef("dbhost update: --node-id and --no-node cannot be given together")
	}

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	var patch admin.HostPatch
	if set["name"] {
		patch.Name = name
	}
	if set["host"] {
		patch.Host = host
	}
	if set["port"] {
		patch.Port = port
	}
	if set["username"] {
		patch.Username = username
	}
	if *noNode || set["node-id"] {
		patch.NodeSet = true
		if !*noNode {
			if *node == 0 {
				return usagef("dbhost update: --node-id must be a number above 0")
			}
			patch.NodeID = node
		}
	}
	if *fromStdin {
		p, err := readLine(e.Stdin)
		if err != nil {
			return err
		}
		patch.Password = &p
	}
	if patch == (admin.HostPatch{}) {
		return usagef("dbhost update: nothing to change")
	}
	if err := c.h.UpdateHost(ctx, id, patch); err != nil {
		return err
	}
	fmt.Fprintf(e.Stderr, "Updated database host %d. The panel signed in to it with the new values.\n", id)
	return nil
}

func (c *hostCmds) rotate(ctx context.Context, e Env, args []string) error {
	rest, err := parse(flags("dbhost rotate-password"), args, 1)
	if err != nil {
		return err
	}
	id, err := number("the id of the host", rest[0])
	if err != nil {
		return err
	}
	password, err := c.h.RotatePassword(ctx, id)
	var mismatch *admin.HostMismatchError
	switch {
	case errors.As(err, &mismatch):
		// The host took the password and the panel did not store it: the person has to have it to put it right.
		fmt.Fprintf(e.Stderr, "The password was changed on database host %d but could not be stored in the panel (%v).\nThe new password is on the standard output. Store it with: raptor dbhost update %d --password-stdin\n", id, mismatch.Unwrap(), id)
		fmt.Fprintln(e.Stdout, mismatch.NewPassword())
		return errors.New("the panel and the database host now disagree about the password")
	case err != nil:
		return err
	}
	fmt.Fprintf(e.Stderr, "Changed the password of the panel's account on database host %d, there and in the panel. The new password is on the standard output; the panel keeps its own copy.\n", id)
	fmt.Fprintln(e.Stdout, password)
	return nil
}

func (c *hostCmds) delete(ctx context.Context, e Env, args []string) error {
	rest, err := parse(flags("dbhost delete"), args, 1)
	if err != nil {
		return err
	}
	id, err := number("the id of the host", rest[0])
	if err != nil {
		return err
	}
	if err := c.h.DeleteHost(ctx, id); err != nil {
		return err
	}
	fmt.Fprintf(e.Stderr, "Deleted database host %d.\n", id)
	return nil
}

// --- looking at the panel

func directoryCommands(d *admin.Directory) map[string]map[string]Handler {
	c := &dirCmds{d: d}
	return map[string]map[string]Handler{
		"user":     {"list": c.users},
		"apikey":   {"list": c.keys, "revoke": c.revoke},
		"location": {"list": c.locations},
		"node":     {"list": c.nodes},
		"settings": {"get": c.setting},
	}
}

type dirCmds struct{ d *admin.Directory }

func (c *dirCmds) users(ctx context.Context, e Env, args []string) error {
	if _, err := parse(flags("user list"), args, 0); err != nil {
		return err
	}
	us, err := c.d.Users(ctx)
	if err != nil {
		return err
	}
	sort.Slice(us, func(i, j int) bool { return us[i].ID < us[j].ID })
	rows := make([][]string, 0, len(us))
	for _, u := range us {
		rows = append(rows, []string{strconv.FormatUint(u.ID, 10), u.Username, u.Email, yesNo(u.Admin), u.UUID})
	}
	return table(e.Stdout, "ID\tUSERNAME\tEMAIL\tADMIN\tUUID", rows)
}

func (c *dirCmds) keys(ctx context.Context, e Env, args []string) error {
	fs := flags("apikey list")
	user := fs.String("user", "", "only the keys of this user: id, e-mail address or user name")
	if _, err := parse(fs, args, 0); err != nil {
		return err
	}
	ks, err := c.d.Keys(ctx, *user)
	if err != nil {
		return err
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i].Identifier < ks[j].Identifier })
	rows := make([][]string, 0, len(ks))
	for _, k := range ks {
		kind, access := "account", "-"
		if k.Type == auth.KeyApplication {
			kind, access = "application", accessText(k.Access)
		}
		used := "never"
		if k.LastUsedAt != nil {
			used = k.LastUsedAt.UTC().Format(time.DateTime)
		}
		ips := "any"
		if len(k.AllowedIPs) > 0 {
			ips = strings.Join(k.AllowedIPs, ",")
		}
		rows = append(rows, []string{k.Identifier, k.Username, kind, k.Description, access, ips, used})
	}
	return table(e.Stdout, "IDENTIFIER\tUSER\tTYPE\tDESCRIPTION\tACCESS\tFROM\tLAST USED", rows)
}

// accessText writes the levels of an application key as "servers=rw users=read", leaving out the resources it has no access to.
func accessText(access map[string]auth.Access) string {
	var parts []string
	for _, r := range auth.ApplicationResources() {
		switch access[r] {
		case auth.AccessRead:
			parts = append(parts, r+"=read")
		case auth.AccessReadWrite:
			parts = append(parts, r+"=rw")
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, " ")
}

func (c *dirCmds) revoke(ctx context.Context, e Env, args []string) error {
	fs := flags("apikey revoke")
	all := fs.Bool("all", false, "revoke every key instead of one")
	kind := fs.String("kind", "", "with --all: only the keys of this kind, account or application")
	yes := fs.Bool("yes", false, "with --all: really revoke them (without it nothing is removed, and the number that would go is shown)")
	rest, err := parseRange(fs, args, 0, 1)
	if err != nil {
		return err
	}
	if *all {
		if len(rest) != 0 {
			return usagef("apikey revoke: --all takes no identifier")
		}
		n, err := c.d.RevokeKeys(ctx, *kind, *yes)
		if err != nil {
			return err
		}
		fmt.Fprintf(e.Stderr, "Revoked %d key(s). Their owners have to make new ones.\n", n)
		return nil
	}
	if len(rest) != 1 {
		return usagef("apikey revoke takes the identifier of a key, or --all")
	}
	if *kind != "" || *yes {
		return usagef("apikey revoke: --kind and --yes are for --all")
	}
	k, err := c.d.RevokeKey(ctx, rest[0])
	if err != nil {
		return err
	}
	fmt.Fprintf(e.Stderr, "Revoked the key %s of %s.\n", k.Identifier, cell(k.Username))
	return nil
}

func (c *dirCmds) locations(ctx context.Context, e Env, args []string) error {
	if _, err := parse(flags("location list"), args, 0); err != nil {
		return err
	}
	ls, err := c.d.Locations(ctx)
	if err != nil {
		return err
	}
	sort.Slice(ls, func(i, j int) bool { return ls[i].ID < ls[j].ID })
	rows := make([][]string, 0, len(ls))
	for _, l := range ls {
		rows = append(rows, []string{strconv.FormatUint(l.ID, 10), l.Short, l.Long, strconv.FormatInt(l.Nodes, 10)})
	}
	return table(e.Stdout, "ID\tSHORT\tDESCRIPTION\tNODES", rows)
}

func (c *dirCmds) nodes(ctx context.Context, e Env, args []string) error {
	if _, err := parse(flags("node list"), args, 0); err != nil {
		return err
	}
	ns, err := c.d.Nodes(ctx)
	if err != nil {
		return err
	}
	sort.Slice(ns, func(i, j int) bool { return ns[i].ID < ns[j].ID })
	rows := make([][]string, 0, len(ns))
	for _, n := range ns {
		rows = append(rows, []string{
			strconv.FormatUint(n.ID, 10), n.Name, n.Scheme + "://" + n.FQDN + ":" + strconv.Itoa(n.Port),
			strconv.FormatUint(n.LocationID, 10), strconv.FormatInt(n.Servers, 10), n.UUID,
		})
	}
	return table(e.Stdout, "ID\tNAME\tADDRESS\tLOCATION\tSERVERS\tUUID", rows)
}

func (c *dirCmds) setting(ctx context.Context, e Env, args []string) error {
	rest, err := parse(flags("settings get"), args, 1)
	if err != nil {
		return err
	}
	value, ok, err := c.d.Setting(ctx, rest[0])
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s has no value: the panel uses its default", rest[0])
	}
	fmt.Fprintln(e.Stdout, cell(value))
	return nil
}

// --- replacing secrets

func rotationCommands(r *admin.Rotation) map[string]map[string]Handler {
	c := &rotationCmds{r: r}
	return map[string]map[string]Handler{
		"node": {"rotate-token": c.nodeToken},
	}
}

type rotationCmds struct{ r *admin.Rotation }

func (c *rotationCmds) nodeToken(ctx context.Context, e Env, args []string) error {
	rest, err := parse(flags("node rotate-token"), args, 1)
	if err != nil {
		return err
	}
	id, err := number("the id of the node", rest[0])
	if err != nil {
		return err
	}
	made, err := c.r.RotateNodeToken(ctx, id)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.Stderr, "Gave node %d (uuid %s) a new token. The old one no longer works.\nIts configuration, token included, is on the standard output: save it as config.yml on the node and restart Wings. Until then the panel cannot reach the node; its servers keep running. It cannot be found out again.\n", made.ID, cell(made.UUID))
	_, err = e.Stdout.Write(made.Config)
	return err
}

func databaseRotationCommands(r *admin.DatabaseRotation) map[string]map[string]Handler {
	c := &databaseRotationCmds{r: r}
	return map[string]map[string]Handler{
		"dbhost": {"rotate-all": c.rotateAll},
	}
}

type databaseRotationCmds struct{ r *admin.DatabaseRotation }

// rotateAll gives every database of every server a new password. It asks for --yes, because the passwords that game servers have in their configuration stop
// working, and it can be limited to the databases of one host with --host, so that it can be done host by host.
func (c *databaseRotationCmds) rotateAll(ctx context.Context, e Env, args []string) error {
	fs := flags("dbhost rotate-all")
	host := fs.Uint64("host", 0, "only the databases on this database host (an id; the default is every host)")
	database := fs.Uint64("database", 0, "only this database (an id): to put right one that a report said the panel could not keep")
	yes := fs.Bool("yes", false, "really do it (without it nothing is changed, and the number of databases is shown)")
	if _, err := parse(fs, args, 0); err != nil {
		return err
	}
	var onlyHost, onlyDatabase *uint64
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "host":
			onlyHost = host
		case "database":
			onlyDatabase = database
		}
	})
	if onlyHost != nil && *onlyHost == 0 {
		return usagef("dbhost rotate-all: --host is the id of a database host, a number above 0")
	}
	if onlyDatabase != nil && *onlyDatabase == 0 {
		return usagef("dbhost rotate-all: --database is the id of a database, a number above 0")
	}
	if onlyHost != nil && onlyDatabase != nil {
		return usagef("dbhost rotate-all: --host and --database do not go together")
	}
	var report admin.DatabaseRotationReport
	var err error
	if onlyDatabase != nil {
		report, err = c.r.RotateDatabase(ctx, *onlyDatabase, *yes)
	} else {
		report, err = c.r.RotateAll(ctx, onlyHost, *yes)
	}
	if err != nil {
		var nc *admin.DatabasesNotConfirmedError
		if errors.As(err, &nc) {
			return err
		}
		return fmt.Errorf("%w (%d of %d databases were changed before it stopped)", err, report.Changed, report.Total)
	}
	fmt.Fprintf(e.Stderr, "Gave %d of %d database(s) a new password.\n", report.Changed, report.Total)
	for _, f := range report.Failed {
		what := "nothing was changed"
		if f.Stage == "store" {
			what = fmt.Sprintf("the host has the new password and the panel does not: run `dbhost rotate-all --database %d --yes` (not the whole command again, which would change the others once more)", f.ID)
		}
		fmt.Fprintf(e.Stderr, "  database %d (%s, host %d): %s: %s\n", f.ID, cell(f.Name), f.HostID, what, cell(f.Cause))
	}
	if len(report.Failed) > 0 {
		return fmt.Errorf("%d database(s) were not changed", len(report.Failed))
	}
	return nil
}
