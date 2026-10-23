package listquery

// The specs of the Application API lists, as the reference's controllers and transformers define them. A filter name is
// also the column, so uuidShort keeps its spelling.
var (
	Users = Spec{
		Table:    "users",
		Filters:  []Filter{{"email", Partial}, {"uuid", Partial}, {"username", Partial}, {"external_id", Partial}},
		Sorts:    []string{"id", "uuid"},
		Includes: []string{"servers"},
	}

	// Nodes: daemon_token_id is matched exactly. The reference matches parts of it, which lets a key that may only
	// read nodes find out the token id piece by piece (C-044).
	Nodes = Spec{
		Table:    "nodes",
		Filters:  []Filter{{"uuid", Partial}, {"name", Partial}, {"fqdn", Partial}, {"daemon_token_id", Exact}},
		Sorts:    []string{"id", "uuid", "memory", "disk"},
		Includes: []string{"allocations", "location", "servers"},
	}

	Locations = Spec{
		Table:    "locations",
		Filters:  []Filter{{"short", Partial}, {"long", Partial}},
		Sorts:    []string{"id"},
		Includes: []string{"nodes", "servers"},
	}

	Servers = Spec{
		Table:    "servers",
		Filters:  []Filter{{"uuid", Partial}, {"uuidShort", Partial}, {"name", Partial}, {"description", Partial}, {"image", Partial}, {"external_id", Partial}},
		Sorts:    []string{"id", "uuid"},
		Includes: []string{"allocations", "user", "subusers", "nest", "egg", "variables", "location", "node", "databases", "transfer"},
	}

	// Allocations are listed per node, so the caller adds the node to the condition. No sort is offered.
	Allocations = Spec{
		Table:    "allocations",
		Filters:  []Filter{{"ip", Exact}, {"port", Exact}, {"ip_alias", Partial}, {"server_id", Owner}},
		Includes: []string{"node", "server"},
	}

	Nests = Spec{Table: "nests", Includes: []string{"eggs", "servers"}}

	Eggs = Spec{Table: "eggs", Includes: []string{"nest", "servers", "config", "script", "variables"}}

	ServerDatabases = Spec{Table: "databases", Includes: []string{"password", "host"}}
)

// The lists of the client API. The server list also takes filter[*], which looks for a text in several columns or for an
// address and port; the activity lists are for the entries of one user or one server, which the handler fixes in the query.
var (
	ClientServers = Spec{
		Table:    "servers",
		Filters:  []Filter{{"uuid", Partial}, {"name", Partial}, {"description", Partial}, {"external_id", Partial}, {"*", Search}},
		Includes: []string{"egg", "subusers"},
	}

	ClientActivity = Spec{
		Table:    "activity_logs",
		Filters:  []Filter{{"event", Partial}},
		Sorts:    []string{"timestamp"},
		Includes: []string{"actor"},
	}
)
