// Package servers holds the logic around a server that needs no database: what the panel tells the node about a server
// (configuration, environment, egg process configuration) and how the startup command is shown to users. The database
// layer fills the types below; nothing here reads or writes storage.
package servers

// Server is what the builders need to know about a server.
type Server struct {
	UUID        string
	Name        string
	Description string
	// Status is empty for a server in its normal state, otherwise one of "installing", "install_failed",
	// "reinstall_failed", "suspended" or "restoring_backup".
	Status string

	// Startup is the startup command with its {{VARIABLE}} placeholders.
	Startup string
	Image   string

	Memory int64
	Swap   int64
	Disk   int64
	IO     int64
	CPU    int64
	// Threads is a CPU set such as "0-3,6", or nil for no limit.
	Threads     *string
	OOMDisabled bool
	SkipScripts bool

	// AllocationLimit is the number of extra allocations the owner may add, or nil for no limit.
	AllocationLimit *int64
	// LocationShort is the short code of the location of the node.
	LocationShort string

	// Allocation is the primary allocation. Allocations are all the allocations of the server on its node, the primary one
	// included, in the order they are shown.
	Allocation  Allocation
	Allocations []Allocation

	Variables []Variable
	Mounts    []Mount
	Egg       Egg
}

// Allocation is an address and port of a node assigned to a server.
type Allocation struct {
	IP   string
	Port int
}

// Variable is an egg variable together with the value of a server.
type Variable struct {
	// EnvName is the environment variable name, for example "SERVER_JARFILE".
	EnvName string
	Default string
	// Value is the server's own value, or nil when it uses the default.
	Value        *string
	UserViewable bool
}

// Current is the value in effect: the server's own, or the default.
func (v Variable) Current() string {
	if v.Value != nil {
		return *v.Value
	}
	return v.Default
}

// Mount is a host directory made available inside a server.
type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// Egg is the part of an egg that goes into the server configuration. Values inherited from a parent egg (config_from)
// must already be resolved.
type Egg struct {
	UUID            string
	ForceOutgoingIP bool
	FileDenylist    []string

	// ConfigFiles and ConfigStartup are JSON documents, ConfigStop a plain stop command or "^SIGNAL".
	ConfigFiles   string
	ConfigStartup string
	ConfigStop    string
}

// Suspended reports whether the server is suspended.
func (s Server) Suspended() bool { return s.Status == StatusSuspended }
