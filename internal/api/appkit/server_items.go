package appkit

import (
	"github.com/Helles01/raptor-panel/internal/api/resp"
	"github.com/Helles01/raptor-panel/internal/servers"
)

type serverLimits struct {
	Memory      int64   `json:"memory"`
	Swap        int64   `json:"swap"`
	Disk        int64   `json:"disk"`
	IO          int64   `json:"io"`
	CPU         int64   `json:"cpu"`
	Threads     *string `json:"threads"`
	OOMDisabled bool    `json:"oom_disabled"`
}

type serverFeatureLimits struct {
	Databases   *int64 `json:"databases"`
	Allocations *int64 `json:"allocations"`
	Backups     *int64 `json:"backups"`
}

type serverContainer struct {
	StartupCommand string         `json:"startup_command"`
	Image          string         `json:"image"`
	Installed      int            `json:"installed"`
	Environment    map[string]any `json:"environment"`
	SkipScripts    bool           `json:"skip_scripts"`
}

type serverAttributes struct {
	ID            uint64              `json:"id"`
	ExternalID    *string             `json:"external_id"`
	UUID          string              `json:"uuid"`
	Identifier    string              `json:"identifier"`
	Name          string              `json:"name"`
	Description   string              `json:"description"`
	Status        *string             `json:"status"`
	Suspended     bool                `json:"suspended"`
	Limits        serverLimits        `json:"limits"`
	FeatureLimits serverFeatureLimits `json:"feature_limits"`
	User          uint64              `json:"user"`
	Node          uint64              `json:"node"`
	Allocation    uint64              `json:"allocation"`
	Nest          uint64              `json:"nest"`
	Egg           uint64              `json:"egg"`
	Container     serverContainer     `json:"container"`
	UpdatedAt     string              `json:"updated_at"`
	CreatedAt     string              `json:"created_at"`
}

// serverList shows servers, as an include of another resource does: without includes of their own.
func (v View) serverList(list []Server) resp.List {
	items := make([]resp.Item, len(list))
	for i, s := range list {
		items[i] = v.Server(s, nil)
	}
	return resp.NewList(items)
}

// Server shows a server. The environment is what the node is started with, so whoever may read servers may read the values of its
// variables, as in the reference. Includes: allocations, user, subusers, nest, egg, variables, location, node and databases.
func (v View) Server(s Server, inc Includes) resp.Item {
	c := s.Config
	var status *string
	if c.Status != "" {
		status = &c.Status
	}
	installed := 0
	if servers.Installed(c.Status) {
		installed = 1
	}
	it := resp.NewItem("server", serverAttributes{
		ID: s.ID, ExternalID: s.ExternalID, UUID: c.UUID, Identifier: s.ShortID, Name: c.Name, Description: c.Description, Status: status, Suspended: c.Suspended(),
		Limits:        serverLimits{Memory: c.Memory, Swap: c.Swap, Disk: c.Disk, IO: c.IO, CPU: c.CPU, Threads: c.Threads, OOMDisabled: c.OOMDisabled},
		FeatureLimits: serverFeatureLimits{Databases: s.DatabaseLimit, Allocations: c.AllocationLimit, Backups: s.BackupLimit},
		User:          s.OwnerID, Node: s.NodeID, Allocation: s.AllocationID, Nest: s.NestID, Egg: s.EggID,
		Container: serverContainer{StartupCommand: c.Startup, Image: c.Image, Installed: installed, Environment: servers.Environment(c), SkipScripts: c.SkipScripts},
		UpdatedAt: Time(s.UpdatedAt), CreatedAt: Time(s.CreatedAt),
	})

	it = v.attach(it, inc, "servers", "allocations", func() any {
		items := make([]resp.Item, len(s.Allocations))
		for i, a := range s.Allocations {
			items[i] = v.Allocation(a, nil)
		}
		return resp.NewList(items)
	})
	it = v.attach(it, inc, "servers", "subusers", func() any {
		items := make([]resp.Item, len(s.Subusers))
		for i, x := range s.Subusers {
			items[i] = v.Subuser(x)
		}
		return resp.NewList(items)
	})
	it = v.attach(it, inc, "servers", "user", func() any {
		if s.User == nil {
			return null()
		}
		return v.User(*s.User, nil)
	})
	it = v.attach(it, inc, "servers", "nest", func() any {
		if s.Nest == nil {
			return null()
		}
		return v.Nest(*s.Nest, nil)
	})
	it = v.attach(it, inc, "servers", "egg", func() any {
		if s.Egg == nil {
			return null()
		}
		return v.Egg(*s.Egg, nil)
	})
	it = v.attach(it, inc, "servers", "variables", func() any {
		items := make([]resp.Item, len(s.Variables))
		for i, x := range s.Variables {
			items[i] = ServerVariableItem(x)
		}
		return resp.NewList(items)
	})
	it = v.attach(it, inc, "servers", "location", func() any {
		if s.Location == nil {
			return null()
		}
		return v.Location(*s.Location, nil)
	})
	it = v.attach(it, inc, "servers", "node", func() any {
		if s.Node == nil {
			return null()
		}
		return v.Node(*s.Node, nil)
	})
	return v.attach(it, inc, "servers", "databases", func() any {
		items := make([]resp.Item, len(s.Databases))
		for i, d := range s.Databases {
			item := v.ServerDatabase(d, nil)
			// The reference names the objects of this collection after the include, not after the model.
			item.Object = "databases"
			items[i] = item
		}
		return resp.NewList(items)
	})
}

type subuserAttributes struct {
	ID          uint64   `json:"id"`
	UserID      uint64   `json:"user_id"`
	ServerID    uint64   `json:"server_id"`
	Permissions []string `json:"permissions"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

// Subuser shows a user's access to a server, in the include of a server.
func (v View) Subuser(s Subuser) resp.Item {
	perms := s.Permissions
	if perms == nil {
		perms = []string{}
	}
	return resp.NewItem("subuser", subuserAttributes{ID: s.ID, UserID: s.UserID, ServerID: s.ServerID, Permissions: perms, CreatedAt: Time(s.CreatedAt), UpdatedAt: Time(s.UpdatedAt)})
}

type serverVariableAttributes struct {
	eggVariableAttributes
	ServerValue *string `json:"server_value"`
}

// ServerVariableItem shows a variable of the egg of a server with the value the server has for it, as the model is shown.
func ServerVariableItem(x ServerVariable) resp.Item {
	e := x.Variable
	return resp.NewItem("server_variable", serverVariableAttributes{
		eggVariableAttributes: eggVariableAttributes{
			ID: e.ID, EggID: e.EggID, Name: e.Name, Description: e.Description, EnvVariable: e.EnvVariable, DefaultValue: e.DefaultValue,
			UserViewable: e.UserViewable, UserEditable: e.UserEditable, Rules: e.Rules, CreatedAt: e.CreatedAt.UTC().Format(rawTime), UpdatedAt: e.UpdatedAt.UTC().Format(rawTime),
		},
		ServerValue: x.ServerValue,
	})
}

type databaseHostAttributes struct {
	ID        uint64  `json:"id"`
	Name      string  `json:"name"`
	Host      string  `json:"host"`
	Port      int     `json:"port"`
	Username  string  `json:"username"`
	Node      *uint64 `json:"node"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

// DatabaseHost shows a database host. The password of the panel's account on it is never part of it.
func (v View) DatabaseHost(h DatabaseHost) resp.Item {
	return resp.NewItem("database_host", databaseHostAttributes{
		ID: h.ID, Name: h.Name, Host: h.Host, Port: h.Port, Username: h.Username, Node: h.NodeID, CreatedAt: Time(h.CreatedAt), UpdatedAt: Time(h.UpdatedAt),
	})
}

type serverDatabaseAttributes struct {
	ID             uint64 `json:"id"`
	Server         uint64 `json:"server"`
	Host           uint64 `json:"host"`
	Database       string `json:"database"`
	Username       string `json:"username"`
	Remote         string `json:"remote"`
	MaxConnections int    `json:"max_connections"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

// ServerDatabase shows a database of a server. Includes: password (only when the store filled it) and host.
func (v View) ServerDatabase(d ServerDatabase, inc Includes) resp.Item {
	it := resp.NewItem("server_database", serverDatabaseAttributes{
		ID: d.ID, Server: d.ServerID, Host: d.HostID, Database: d.Name, Username: d.Username, Remote: d.Remote, MaxConnections: d.MaxConnections,
		CreatedAt: Time(d.CreatedAt), UpdatedAt: Time(d.UpdatedAt),
	})
	it = v.attach(it, inc, "databases", "password", func() any {
		if d.Password == nil {
			return null()
		}
		return resp.NewItem("database_password", map[string]string{"password": *d.Password})
	})
	return v.attach(it, inc, "databases", "host", func() any {
		if d.Host == nil {
			return null()
		}
		return v.DatabaseHost(*d.Host)
	})
}
