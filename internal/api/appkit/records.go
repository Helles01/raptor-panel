package appkit

import (
	"encoding/json"
	"time"

	"github.com/Helles01/raptor-panel/internal/nodes"
)

// The records are what the stores return. A relation (Nodes, Allocations, Location and so on) is filled only when the request asked
// for it and the key may read it; the store is told which in Includes.

// User is an account as the Application API shows it.
type User struct {
	ID         uint64
	ExternalID *string
	UUID       string
	Username   string
	Email      string
	FirstName  string
	LastName   string
	Language   string
	Admin      bool
	TwoFactor  bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Location is a place nodes are in. Long is null when it was left out.
type Location struct {
	ID        uint64
	Short     string
	Long      *string
	CreatedAt time.Time
	UpdatedAt time.Time

	Nodes []Node
}

// Node is a node with its settings and what its servers have been given.
type Node struct {
	ID       uint64
	UUID     string
	Settings nodes.Settings
	// TokenID is the public half of the node's credentials; it is not shown.
	TokenID string
	// AllocatedMemory and AllocatedDisk are the sums over the servers on the node, in MB.
	AllocatedMemory int64
	AllocatedDisk   int64
	CreatedAt       time.Time
	UpdatedAt       time.Time

	Allocations []Allocation
	Location    *Location
}

// Allocation is an address and port of a node, taken by a server or free.
type Allocation struct {
	ID       uint64
	NodeID   uint64
	IP       string
	Alias    *string
	Port     int
	Notes    *string
	ServerID *uint64

	Node *Node
}

// Nest is a group of eggs.
type Nest struct {
	ID          uint64
	UUID        string
	Author      string
	Name        string
	Description *string
	CreatedAt   time.Time
	UpdatedAt   time.Time

	Eggs []Egg
}

// DockerImage is one image of an egg under the name it is shown by. The order is the order of the egg: the first is the default.
type DockerImage struct{ Name, Image string }

// ConfigSet is the part of an egg's configuration that another egg may lend (config_from); the values are JSON as stored.
type ConfigSet struct {
	Files, Startup, Logs json.RawMessage
	Stop                 *string
}

// ScriptSet is the install script an egg may borrow (copy_script_from).
type ScriptSet struct {
	Install          *string
	Entry, Container string
}

// EggVariable is a setting of an egg that becomes an environment variable of the server.
type EggVariable struct {
	ID           uint64
	EggID        uint64
	Name         string
	Description  string
	EnvVariable  string
	DefaultValue string
	UserViewable bool
	UserEditable bool
	Rules        string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Egg is a kind of server. The JSON texts (ConfigFiles, ConfigStartup, ConfigLogs) are as stored.
type Egg struct {
	ID               uint64
	UUID             string
	NestID           uint64
	Name             string
	Author           string
	Description      *string
	DockerImages     []DockerImage
	ConfigFiles      json.RawMessage
	ConfigStartup    json.RawMessage
	ConfigStop       *string
	ConfigLogs       json.RawMessage
	FileDenylist     []string
	ConfigFrom       *uint64
	Startup          string
	ScriptPrivileged bool
	ScriptInstall    *string
	ScriptEntry      string
	ScriptContainer  string
	CopyScriptFrom   *uint64
	CreatedAt        time.Time
	UpdatedAt        time.Time

	Nest      *Nest
	Variables []EggVariable
	// Inherited is the configuration the egg gets from ConfigFrom, and InheritedScript the script it gets from CopyScriptFrom.
	Inherited       *ConfigSet
	InheritedScript *ScriptSet
}
