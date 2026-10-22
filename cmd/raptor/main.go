// Command raptor is the Raptor Panel executable: the web server and the admin CLI.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"sort"
)

// env carries the process streams and environment so that commands stay testable.
type env struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	getenv func(string) string
}

type handler func(ctx context.Context, e *env, args []string) error

// usageError marks a mistake in how the command was invoked (exit code 2).
type usageError struct{ msg string }

func (u usageError) Error() string { return u.msg }

func usagef(format string, a ...any) error { return usageError{msg: fmt.Sprintf(format, a...)} }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], &env{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv})
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, e *env) int {
	err := dispatch(ctx, e, "raptor", rootCommands(), args)

	var ue usageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ue):
		fmt.Fprintf(e.stderr, "raptor: %v\n", ue)
		return 2
	default:
		fmt.Fprintf(e.stderr, "raptor: %v\n", err)
		return 1
	}
}

func rootCommands() map[string]handler {
	return map[string]handler{
		"version":         cmdVersion,
		"keygen":          cmdKeygen,
		"db":              cmdDB,
		"migrate-secrets": cmdMigrateSecrets,
	}
}

// dispatch runs the handler named by args[0]. With no name, or an explicit help request, it lists the available names.
func dispatch(ctx context.Context, e *env, prefix string, table map[string]handler, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		names := make([]string, 0, len(table))
		for name := range table {
			names = append(names, name)
		}
		sort.Strings(names)

		fmt.Fprintf(e.stderr, "usage: %s <command> [arguments]\n\ncommands:\n", prefix)
		for _, name := range names {
			fmt.Fprintf(e.stderr, "  %s\n", name)
		}
		if len(args) == 0 {
			return usagef("a command is required")
		}
		return nil
	}

	h, ok := table[args[0]]
	if !ok {
		return usagef("unknown command %q (try \"%s help\")", args[0], prefix)
	}
	return h(ctx, e, args[1:])
}

func cmdVersion(_ context.Context, e *env, _ []string) error {
	version, revision, goVersion := "unknown", "", ""
	if info, ok := debug.ReadBuildInfo(); ok {
		version, goVersion = info.Main.Version, info.GoVersion
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 12 {
				revision = s.Value[:12]
			}
		}
	}

	fmt.Fprintf(e.stdout, "raptor %s", version)
	if revision != "" {
		fmt.Fprintf(e.stdout, " rev=%s", revision)
	}
	if goVersion != "" {
		fmt.Fprintf(e.stdout, " %s", goVersion)
	}
	fmt.Fprintln(e.stdout)
	return nil
}
