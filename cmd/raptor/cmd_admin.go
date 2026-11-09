package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Helles01/raptor-panel/internal/admin"
	"github.com/Helles01/raptor-panel/internal/admincli"
	"github.com/Helles01/raptor-panel/internal/api/web"
	"github.com/Helles01/raptor-panel/internal/config"
	"github.com/Helles01/raptor-panel/internal/db"
	"github.com/Helles01/raptor-panel/internal/dbhosts"
	"github.com/Helles01/raptor-panel/internal/secrets"
	"github.com/Helles01/raptor-panel/internal/store"
)

// adminGroups are the commands of the administrator: `raptor user create …`, `raptor dbhost list`, and so on. The names are listed from a table
// made with services that are never called, so that help and a mistyped name need no database.
func adminGroups() map[string]map[string]admincli.Handler {
	return admincli.All(admincli.Services{Admin: &admin.Service{}, Hosts: &admin.HostService{}, Directory: &admin.Directory{}, Rotation: &admin.Rotation{}, Databases: &admin.DatabaseRotation{}})
}

func adminCommand(group string) handler {
	return func(ctx context.Context, e *env, args []string) error {
		verbs := adminGroups()[group]
		if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
			names := make([]string, 0, len(verbs))
			for v := range verbs {
				names = append(names, v)
			}
			sort.Strings(names)
			fmt.Fprintf(e.stderr, "usage: raptor %s <command> [flags]\n\ncommands:\n  %s\n", group, strings.Join(names, "\n  "))
			if len(args) == 0 {
				return usagef("a command is required")
			}
			return nil
		}
		if _, ok := verbs[args[0]]; !ok {
			return usagef("unknown command %q (try \"raptor %s help\")", args[0], group)
		}

		services, closeAll, err := openAdminServices(ctx, e)
		if err != nil {
			return err
		}
		defer closeAll()
		err = admincli.All(services)[group][args[0]](ctx, admincli.Env{Stdin: e.stdin, Stdout: e.stdout, Stderr: e.stderr}, args[1:])
		var ue admincli.UsageError
		if errors.As(err, &ue) {
			return usagef("%s", ue.Msg)
		}
		return err
	}
}

// openAdminServices makes the services of the commands over the database of the configuration. The configuration is the file named by
// RAPTOR_CONFIG and the RAPTOR_* variables, as for every command.
func openAdminServices(ctx context.Context, e *env) (admincli.Services, func(), error) {
	cfg, err := config.Load("", e.getenv)
	if err != nil {
		return admincli.Services{}, nil, err
	}
	if err := cfg.DB.Validate(); err != nil {
		return admincli.Services{}, nil, fmt.Errorf("invalid configuration: %w", err)
	}
	if err := cfg.App.Validate(); err != nil {
		return admincli.Services{}, nil, fmt.Errorf("invalid configuration: %w", err)
	}
	if err := cfg.DBHosts.Validate(); err != nil {
		return admincli.Services{}, nil, fmt.Errorf("invalid configuration: %w", err)
	}
	key, err := cfg.Secrets.KeyBytes()
	if err != nil {
		return admincli.Services{}, nil, err
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		return admincli.Services{}, nil, err
	}
	sqlDB, err := db.Open(ctx, cfg.DB)
	if err != nil {
		return admincli.Services{}, nil, err
	}
	closeAll := func() { _ = sqlDB.Close() }

	st, err := store.New(sqlDB, box)
	if err != nil {
		closeAll()
		return admincli.Services{}, nil, err
	}
	locales, err := web.EmbeddedLocales()
	if err != nil {
		closeAll()
		return admincli.Services{}, nil, err
	}
	svc, err := admin.New(st.AdminStore(), cfg.App.URL, languagesOf(locales), nil)
	if err != nil {
		closeAll()
		return admincli.Services{}, nil, err
	}
	dialer, err := dbhosts.NewMySQLDialer(cfg.DBHosts.TLS, 0)
	if err != nil {
		closeAll()
		return admincli.Services{}, nil, err
	}
	executor, err := dbhosts.NewExecutor(st.DBHostLookup(), dialer, 0)
	if err != nil {
		closeAll()
		return admincli.Services{}, nil, err
	}
	hosts, err := admin.NewHostService(st.AdminHosts(), executor, nil)
	if err != nil {
		closeAll()
		return admincli.Services{}, nil, err
	}
	dir, err := admin.NewDirectory(st.AdminDirectory())
	if err != nil {
		closeAll()
		return admincli.Services{}, nil, err
	}
	rotation, err := admin.NewRotation(st.AdminRotation(), cfg.App.URL, nil)
	if err != nil {
		closeAll()
		return admincli.Services{}, nil, err
	}
	databases, err := admin.NewDatabaseRotation(st.AdminDatabases(), executor, nil)
	if err != nil {
		closeAll()
		return admincli.Services{}, nil, err
	}
	return admincli.Services{Admin: svc, Hosts: hosts, Directory: dir, Rotation: rotation, Databases: databases}, closeAll, nil
}

// languagesOf lists the languages the panel has translations for ("en", "hu"), sorted.
func languagesOf(l *web.Locales) []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range l.Names() {
		lang, _, _ := strings.Cut(name, "/")
		if !seen[lang] {
			seen[lang] = true
			out = append(out, lang)
		}
	}
	sort.Strings(out)
	return out
}
