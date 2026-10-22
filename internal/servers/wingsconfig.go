package servers

// Environment returns the environment variables a server starts with: the values of the egg variables, then the ones the
// panel adds. Later ones win, so an egg cannot override what the panel sets. Values are strings, except the allocation
// limit, which is a number or null.
func Environment(s Server) map[string]any {
	env := make(map[string]any, len(s.Variables)+4)
	for _, v := range s.Variables {
		env[v.EnvName] = v.Current()
	}
	env["STARTUP"] = s.Startup
	env["P_SERVER_LOCATION"] = s.LocationShort
	env["P_SERVER_UUID"] = s.UUID
	if s.AllocationLimit != nil {
		env["P_SERVER_ALLOCATION_LIMIT"] = *s.AllocationLimit
	} else {
		env["P_SERVER_ALLOCATION_LIMIT"] = nil
	}
	return env
}

// WingsConfig is the server configuration in the format the node reads (GET /api/remote/servers/{uuid}).
type WingsConfig struct {
	UUID           string         `json:"uuid"`
	Meta           WingsMeta      `json:"meta"`
	Suspended      bool           `json:"suspended"`
	Environment    map[string]any `json:"environment"`
	Invocation     string         `json:"invocation"`
	SkipEggScripts bool           `json:"skip_egg_scripts"`
	Build          WingsBuild     `json:"build"`
	Container      WingsContainer `json:"container"`
	Allocations    WingsAllocs    `json:"allocations"`
	Mounts         []WingsMount   `json:"mounts"`
	Egg            WingsEgg       `json:"egg"`
}

// WingsMeta is the name and description.
type WingsMeta struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// WingsBuild is the resource limits.
type WingsBuild struct {
	MemoryLimit int64   `json:"memory_limit"`
	Swap        int64   `json:"swap"`
	IOWeight    int64   `json:"io_weight"`
	CPULimit    int64   `json:"cpu_limit"`
	Threads     *string `json:"threads"`
	DiskSpace   int64   `json:"disk_space"`
	OOMDisabled bool    `json:"oom_disabled"`
}

// WingsContainer is the container settings. OOMDisabled is deprecated in favour of the build block but still read by
// older nodes.
type WingsContainer struct {
	Image           string `json:"image"`
	OOMDisabled     bool   `json:"oom_disabled"`
	RequiresRebuild bool   `json:"requires_rebuild"`
}

// WingsAllocs is the network configuration.
type WingsAllocs struct {
	ForceOutgoingIP bool             `json:"force_outgoing_ip"`
	Default         WingsAddress     `json:"default"`
	Mappings        map[string][]int `json:"mappings"`
}

// WingsAddress is an IP address and port.
type WingsAddress struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
}

// WingsMount is a mount of a server.
type WingsMount struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}

// WingsEgg identifies the egg and its file denylist.
type WingsEgg struct {
	ID           string   `json:"id"`
	FileDenylist []string `json:"file_denylist"`
}

// Config builds the configuration in the current format. Empty lists are empty JSON arrays and objects, never null: the
// node cannot read a null mount list or a mapping given as an array.
func Config(s Server) WingsConfig {
	mounts := make([]WingsMount, 0, len(s.Mounts))
	for _, m := range s.Mounts {
		mounts = append(mounts, WingsMount{Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly})
	}
	denylist := s.Egg.FileDenylist
	if denylist == nil {
		denylist = []string{}
	}

	return WingsConfig{
		UUID:           s.UUID,
		Meta:           WingsMeta{Name: s.Name, Description: s.Description},
		Suspended:      s.Suspended(),
		Environment:    Environment(s),
		Invocation:     s.Startup,
		SkipEggScripts: s.SkipScripts,
		Build: WingsBuild{
			MemoryLimit: s.Memory, Swap: s.Swap, IOWeight: s.IO, CPULimit: s.CPU,
			Threads: s.Threads, DiskSpace: s.Disk, OOMDisabled: s.OOMDisabled,
		},
		Container: WingsContainer{Image: s.Image, OOMDisabled: s.OOMDisabled},
		Allocations: WingsAllocs{
			ForceOutgoingIP: s.Egg.ForceOutgoingIP,
			Default:         WingsAddress{IP: s.Allocation.IP, Port: s.Allocation.Port},
			Mappings:        portsByIP(s.Allocations),
		},
		Mounts: mounts,
		Egg:    WingsEgg{ID: s.Egg.UUID, FileDenylist: denylist},
	}
}

// LegacyConfig is the older format, which egg process configurations refer to with {{server.…}} placeholders.
type LegacyConfig struct {
	UUID      string      `json:"uuid"`
	Build     LegacyBuild `json:"build"`
	Service   LegacyEgg   `json:"service"`
	Rebuild   bool        `json:"rebuild"`
	Suspended int         `json:"suspended"`
}

// LegacyBuild is the build block of the legacy format.
type LegacyBuild struct {
	Default     WingsAddress     `json:"default"`
	Ports       map[string][]int `json:"ports"`
	Env         map[string]any   `json:"env"`
	OOMDisabled bool             `json:"oom_disabled"`
	Memory      int64            `json:"memory"`
	Swap        int64            `json:"swap"`
	IO          int64            `json:"io"`
	CPU         int64            `json:"cpu"`
	Threads     *string          `json:"threads"`
	Disk        int64            `json:"disk"`
	Image       string           `json:"image"`
}

// LegacyEgg is the service block of the legacy format.
type LegacyEgg struct {
	Egg         string `json:"egg"`
	SkipScripts bool   `json:"skip_scripts"`
}

// Legacy builds the configuration in the legacy format.
func Legacy(s Server) LegacyConfig {
	suspended := 0
	if s.Suspended() {
		suspended = 1
	}
	return LegacyConfig{
		UUID: s.UUID,
		Build: LegacyBuild{
			Default:     WingsAddress{IP: s.Allocation.IP, Port: s.Allocation.Port},
			Ports:       portsByIP(s.Allocations),
			Env:         Environment(s),
			OOMDisabled: s.OOMDisabled,
			Memory:      s.Memory, Swap: s.Swap, IO: s.IO, CPU: s.CPU, Threads: s.Threads, Disk: s.Disk, Image: s.Image,
		},
		Service:   LegacyEgg{Egg: s.Egg.UUID, SkipScripts: s.SkipScripts},
		Suspended: suspended,
	}
}

// portsByIP groups the ports of the allocations by address, keeping their order.
func portsByIP(allocations []Allocation) map[string][]int {
	out := make(map[string][]int)
	for _, a := range allocations {
		out[a.IP] = append(out[a.IP], a.Port)
	}
	return out
}
