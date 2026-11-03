package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Helles01/raptor-panel/internal/api/pageapi"
	"github.com/Helles01/raptor-panel/internal/api/web"
	"github.com/Helles01/raptor-panel/internal/settings"
)

// DefaultSiteName and DefaultLocale are what the pages show while the settings table says nothing.
const (
	DefaultSiteName = "Raptor"
	DefaultLocale   = "en"
)

// PageUsers is what the page tells the frontend about the signed-in person.
func (d *DB) PageUsers() pageapi.Users { return pageUsers{d} }

// PageSettings is the name and the language of the panel.
func (d *DB) PageSettings() pageapi.Settings { return pageSettings{d} }

// PageOrigins are the origins of the nodes, for the policy of the page.
func (d *DB) PageOrigins() pageapi.Origins { return pageOrigins{d} }

type (
	pageUsers    struct{ *DB }
	pageSettings struct{ *DB }
	pageOrigins  struct{ *DB }
)

func (s pageUsers) Find(ctx context.Context, id uint64) (web.User, error) {
	if id == 0 || id > 1<<32-1 {
		return web.User{}, pageapi.ErrNotFound
	}
	row, err := s.q.UserByID(ctx, uint32(id))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return web.User{}, pageapi.ErrNotFound
	case err != nil:
		return web.User{}, fmt.Errorf("find user %d: %w", id, err)
	}
	return web.User{
		UUID: row.Uuid, Username: row.Username, Email: row.Email, RootAdmin: row.RootAdmin != 0, UseTotp: row.UseTotp != 0,
		Language: row.Language, CreatedAt: timeOf(row.CreatedAt), UpdatedAt: timeOf(row.UpdatedAt),
	}, nil
}

func (s pageSettings) Site(ctx context.Context) (web.Site, error) {
	site := web.Site{Name: DefaultSiteName, Locale: DefaultLocale}
	for key, dst := range map[string]*string{"app:name": &site.Name, "app:locale": &site.Locale} {
		v, err := s.q.SettingValue(ctx, settings.StorageKey(key))
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return web.Site{}, fmt.Errorf("read setting %s: %w", key, err)
		case v != "":
			*dst = v
		}
	}
	return site, nil
}

// ConnectSources gives the two origins of every node (web.NodeSources); a node whose address is not of the plain form gives none.
func (s pageOrigins) ConnectSources(ctx context.Context) ([]string, error) {
	rows, err := s.q.NodeOrigins(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the nodes: %w", err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, web.NodeSources(r.Scheme, r.Fqdn, int(r.Daemonlisten))...)
	}
	return out, nil
}
