package appkit

import (
	"bytes"
	"encoding/json"
	"net/url"

	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/api/resp"
)

// Includes are the related resources a request asked to attach with ?include=, limited to those this panel can show.
type Includes map[string]bool

// includeResources names, for each resource that has includes, the resource whose read right each include needs. config and script
// need none: they are the egg's own. The same name can need another right on another resource: the variables of an egg are read with
// the right to read eggs, those of a server with the right to read servers.
var includeResources = map[string]map[string]string{
	"users":       {"servers": "servers"},
	"locations":   {"nodes": "nodes", "servers": "servers"},
	"nodes":       {"allocations": "allocations", "location": "locations", "servers": "servers"},
	"allocations": {"node": "nodes", "server": "servers"},
	"nests":       {"eggs": "eggs", "servers": "servers"},
	"eggs":        {"nest": "nests", "servers": "servers", "config": "", "script": "", "variables": "eggs"},
	"servers": {
		"allocations": "allocations", "user": "users", "subusers": "users", "nest": "nests", "egg": "eggs", "variables": "servers",
		"location": "locations", "node": "nodes", "databases": "server_databases",
	},
	"databases": {"host": "database_hosts", "password": ""},
}

// Wanted reads the includes of a request against what the resource offers and what can be shown.
func Wanted(q url.Values, spec listquery.Spec) Includes {
	inc := Includes{}
	for _, i := range listquery.ParseIncludes(q, spec.Includes) {
		if _, ok := includeResources[spec.Table][i.Name]; ok {
			inc[i.Name] = true
		}
	}
	return inc
}

// Load is the part of the includes the store has to load: those the key may read. The others are shown as null resources without the
// store being asked.
func (inc Includes) Load(v View, spec listquery.Spec) Includes {
	out := Includes{}
	for name := range inc {
		if res := includeResources[spec.Table][name]; res == "" || v.CanRead(res) {
			out[name] = true
		}
	}
	return out
}

func null() resp.Item { return resp.NewItem("null_resource", nil) }

// attach adds the relationship of an include of the resource owner when it was asked for: a null resource if the key may not read it.
func (v View) attach(it resp.Item, inc Includes, owner, name string, render func() any) resp.Item {
	if !inc[name] {
		return it
	}
	if res := includeResources[owner][name]; res != "" && !v.CanRead(res) {
		return it.With(name, null())
	}
	return it.With(name, render())
}

type userAttributes struct {
	ID         uint64  `json:"id"`
	ExternalID *string `json:"external_id"`
	UUID       string  `json:"uuid"`
	Username   string  `json:"username"`
	Email      string  `json:"email"`
	FirstName  string  `json:"first_name"`
	LastName   string  `json:"last_name"`
	Language   string  `json:"language"`
	RootAdmin  bool    `json:"root_admin"`
	TwoFactor  bool    `json:"2fa"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
}

// User shows an account.
func (v View) User(u User, inc Includes) resp.Item {
	it := resp.NewItem("user", userAttributes{
		ID: u.ID, ExternalID: u.ExternalID, UUID: u.UUID, Username: u.Username, Email: u.Email, FirstName: u.FirstName, LastName: u.LastName,
		Language: u.Language, RootAdmin: u.Admin, TwoFactor: u.TwoFactor, CreatedAt: Time(u.CreatedAt), UpdatedAt: Time(u.UpdatedAt),
	})
	return v.attach(it, inc, "users", "servers", func() any { return v.serverList(u.Servers) })
}

type locationAttributes struct {
	ID        uint64  `json:"id"`
	Short     string  `json:"short"`
	Long      *string `json:"long"`
	UpdatedAt string  `json:"updated_at"`
	CreatedAt string  `json:"created_at"`
}

// Location shows a location; include nodes attaches its nodes.
func (v View) Location(l Location, inc Includes) resp.Item {
	it := resp.NewItem("location", locationAttributes{ID: l.ID, Short: l.Short, Long: l.Long, UpdatedAt: Time(l.UpdatedAt), CreatedAt: Time(l.CreatedAt)})
	it = v.attach(it, inc, "locations", "servers", func() any { return v.serverList(l.Servers) })
	return v.attach(it, inc, "locations", "nodes", func() any {
		items := make([]resp.Item, len(l.Nodes))
		for i, n := range l.Nodes {
			items[i] = v.Node(n, nil)
		}
		return resp.NewList(items)
	})
}

type allocated struct {
	Memory int64 `json:"memory"`
	Disk   int64 `json:"disk"`
}

type nodeAttributes struct {
	ID                 uint64    `json:"id"`
	UUID               string    `json:"uuid"`
	Public             bool      `json:"public"`
	Name               string    `json:"name"`
	Description        *string   `json:"description"`
	LocationID         uint64    `json:"location_id"`
	FQDN               string    `json:"fqdn"`
	Scheme             string    `json:"scheme"`
	BehindProxy        bool      `json:"behind_proxy"`
	MaintenanceMode    bool      `json:"maintenance_mode"`
	Memory             int64     `json:"memory"`
	MemoryOverallocate int64     `json:"memory_overallocate"`
	Disk               int64     `json:"disk"`
	DiskOverallocate   int64     `json:"disk_overallocate"`
	UploadSize         int64     `json:"upload_size"`
	DaemonListen       int       `json:"daemon_listen"`
	DaemonSFTP         int       `json:"daemon_sftp"`
	DaemonBase         string    `json:"daemon_base"`
	CreatedAt          string    `json:"created_at"`
	UpdatedAt          string    `json:"updated_at"`
	AllocatedResources allocated `json:"allocated_resources"`
}

// Node shows a node. The token is never part of it. Includes allocations and location attach.
func (v View) Node(n Node, inc Includes) resp.Item {
	s := n.Settings
	var description *string
	if s.Description != "" {
		description = &s.Description
	}
	it := resp.NewItem("node", nodeAttributes{
		ID: n.ID, UUID: n.UUID, Public: s.Public, Name: s.Name, Description: description, LocationID: s.LocationID, FQDN: s.FQDN, Scheme: s.Scheme,
		BehindProxy: s.BehindProxy, MaintenanceMode: s.MaintenanceMode, Memory: s.Memory, MemoryOverallocate: s.MemoryOverallocate, Disk: s.Disk,
		DiskOverallocate: s.DiskOverallocate, UploadSize: s.UploadSize, DaemonListen: s.DaemonListen, DaemonSFTP: s.DaemonSFTP, DaemonBase: s.DaemonBase,
		CreatedAt: Time(n.CreatedAt), UpdatedAt: Time(n.UpdatedAt), AllocatedResources: allocated{Memory: n.AllocatedMemory, Disk: n.AllocatedDisk},
	})
	it = v.attach(it, inc, "nodes", "allocations", func() any {
		items := make([]resp.Item, len(n.Allocations))
		for i, a := range n.Allocations {
			items[i] = v.Allocation(a, nil)
		}
		return resp.NewList(items)
	})
	it = v.attach(it, inc, "nodes", "location", func() any {
		if n.Location == nil {
			return null()
		}
		return v.Location(*n.Location, nil)
	})
	return v.attach(it, inc, "nodes", "servers", func() any { return v.serverList(n.Servers) })
}

type allocationAttributes struct {
	ID       uint64  `json:"id"`
	IP       string  `json:"ip"`
	Alias    *string `json:"alias"`
	Port     int     `json:"port"`
	Notes    *string `json:"notes"`
	Assigned bool    `json:"assigned"`
}

// Allocation shows an allocation; include node attaches its node.
func (v View) Allocation(a Allocation, inc Includes) resp.Item {
	it := resp.NewItem("allocation", allocationAttributes{ID: a.ID, IP: a.IP, Alias: a.Alias, Port: a.Port, Notes: a.Notes, Assigned: a.ServerID != nil})
	it = v.attach(it, inc, "allocations", "node", func() any {
		if a.Node == nil {
			return null()
		}
		return v.Node(*a.Node, nil)
	})
	return v.attach(it, inc, "allocations", "server", func() any {
		if a.Server == nil {
			return null()
		}
		return v.Server(*a.Server, nil)
	})
}

type nestAttributes struct {
	ID          uint64  `json:"id"`
	UUID        string  `json:"uuid"`
	Author      string  `json:"author"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// Nest shows a nest; include eggs attaches its eggs.
func (v View) Nest(n Nest, inc Includes) resp.Item {
	it := resp.NewItem("nest", nestAttributes{ID: n.ID, UUID: n.UUID, Author: n.Author, Name: n.Name, Description: n.Description, CreatedAt: Time(n.CreatedAt), UpdatedAt: Time(n.UpdatedAt)})
	it = v.attach(it, inc, "nests", "eggs", func() any {
		items := make([]resp.Item, len(n.Eggs))
		for i, e := range n.Eggs {
			items[i] = v.Egg(e, nil)
		}
		return resp.NewList(items)
	})
	return v.attach(it, inc, "nests", "servers", func() any { return v.serverList(n.Servers) })
}

// images writes the docker images as the object the egg has, in its order.
type images []DockerImage

func (m images) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, im := range m {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(im.Name)
		v, _ := json.Marshal(im.Image)
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

type eggConfig struct {
	Files        json.RawMessage `json:"files"`
	Startup      json.RawMessage `json:"startup"`
	Stop         *string         `json:"stop"`
	Logs         json.RawMessage `json:"logs"`
	FileDenylist []string        `json:"file_denylist"`
	Extends      *uint64         `json:"extends"`
}

type eggScript struct {
	Privileged bool    `json:"privileged"`
	Install    *string `json:"install"`
	Entry      string  `json:"entry"`
	Container  string  `json:"container"`
	Extends    *uint64 `json:"extends"`
}

type eggAttributes struct {
	ID           uint64    `json:"id"`
	UUID         string    `json:"uuid"`
	Name         string    `json:"name"`
	Nest         uint64    `json:"nest"`
	Author       string    `json:"author"`
	Description  *string   `json:"description"`
	DockerImage  string    `json:"docker_image"`
	DockerImages images    `json:"docker_images"`
	Config       eggConfig `json:"config"`
	Startup      string    `json:"startup"`
	Script       eggScript `json:"script"`
	CreatedAt    string    `json:"created_at"`
	UpdatedAt    string    `json:"updated_at"`
}

// jsonOr returns the JSON as stored, or the fallback when there is none.
func jsonOr(raw json.RawMessage, fallback string) json.RawMessage {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return json.RawMessage(fallback)
	}
	return t
}

// emptyFiles is the "files" of an egg that has none, which is an empty object whatever the column holds.
func emptyFiles(raw json.RawMessage) json.RawMessage {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) || bytes.Equal(t, []byte("[]")) || bytes.Equal(t, []byte("{}")) {
		return json.RawMessage("{}")
	}
	return t
}

// Egg shows an egg; includes nest, config, script and variables attach.
func (v View) Egg(e Egg, inc Includes) resp.Item {
	first := ""
	if len(e.DockerImages) > 0 {
		first = e.DockerImages[0].Image
	}
	deny := e.FileDenylist
	if deny == nil {
		deny = []string{}
	}
	it := resp.NewItem("egg", eggAttributes{
		ID: e.ID, UUID: e.UUID, Name: e.Name, Nest: e.NestID, Author: e.Author, Description: e.Description, DockerImage: first, DockerImages: images(e.DockerImages),
		Config: eggConfig{
			Files: emptyFiles(e.ConfigFiles), Startup: jsonOr(e.ConfigStartup, "null"), Stop: e.ConfigStop, Logs: jsonOr(e.ConfigLogs, "null"),
			FileDenylist: deny, Extends: e.ConfigFrom,
		},
		Startup:   e.Startup,
		Script:    eggScript{Privileged: e.ScriptPrivileged, Install: e.ScriptInstall, Entry: e.ScriptEntry, Container: e.ScriptContainer, Extends: e.CopyScriptFrom},
		CreatedAt: Time(e.CreatedAt), UpdatedAt: Time(e.UpdatedAt),
	})
	it = v.attach(it, inc, "eggs", "nest", func() any {
		if e.Nest == nil {
			return null()
		}
		return v.Nest(*e.Nest, nil)
	})
	// The two that are the egg's own are an item without an object name, as the reference's serializer writes one made from a closure.
	it = v.attach(it, inc, "eggs", "config", func() any {
		if e.ConfigFrom == nil || e.Inherited == nil {
			return null()
		}
		return unnamed{Attributes: map[string]any{
			"files": emptyFilesOrNull(e.Inherited.Files), "startup": jsonOr(e.Inherited.Startup, "null"), "stop": e.Inherited.Stop, "logs": jsonOr(e.Inherited.Logs, "null"),
		}}
	})
	it = v.attach(it, inc, "eggs", "script", func() any {
		if e.CopyScriptFrom == nil || e.InheritedScript == nil {
			return null()
		}
		return unnamed{Attributes: map[string]any{
			"privileged": e.ScriptPrivileged, "install": e.InheritedScript.Install, "entry": e.InheritedScript.Entry, "container": e.InheritedScript.Container,
		}}
	})
	it = v.attach(it, inc, "eggs", "variables", func() any {
		items := make([]resp.Item, len(e.Variables))
		for i, x := range e.Variables {
			items[i] = EggVariableItem(x)
		}
		return resp.NewList(items)
	})
	return v.attach(it, inc, "eggs", "servers", func() any { return v.serverList(e.Servers) })
}

// emptyFilesOrNull passes the inherited files on as stored: the reference decodes the column without turning an empty one into an object.
func emptyFilesOrNull(raw json.RawMessage) json.RawMessage { return jsonOr(raw, "null") }

// unnamed is an item whose object is null.
type unnamed struct {
	Attributes any `json:"attributes"`
}

func (u unnamed) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Object     *string `json:"object"`
		Attributes any     `json:"attributes"`
	}{nil, u.Attributes})
}

type eggVariableAttributes struct {
	ID           uint64 `json:"id"`
	EggID        uint64 `json:"egg_id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	EnvVariable  string `json:"env_variable"`
	DefaultValue string `json:"default_value"`
	UserViewable bool   `json:"user_viewable"`
	UserEditable bool   `json:"user_editable"`
	Rules        string `json:"rules"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// EggVariableItem shows a variable of an egg as the model itself is shown, with its times in the form of a model.
func EggVariableItem(x EggVariable) resp.Item {
	return resp.NewItem("egg_variable", eggVariableAttributes{
		ID: x.ID, EggID: x.EggID, Name: x.Name, Description: x.Description, EnvVariable: x.EnvVariable, DefaultValue: x.DefaultValue,
		UserViewable: x.UserViewable, UserEditable: x.UserEditable, Rules: x.Rules, CreatedAt: x.CreatedAt.UTC().Format(rawTime), UpdatedAt: x.UpdatedAt.UTC().Format(rawTime),
	})
}
