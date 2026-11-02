package clientapi

import (
	"crypto/md5" //nolint:gosec // only the address of a picture at a public service, as in the reference
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/api/resp"
	"github.com/Helles01/raptor-panel/internal/ids"
	"github.com/Helles01/raptor-panel/internal/permissions"
	"github.com/Helles01/raptor-panel/internal/servers"
)

// atom is how the reference writes a time (Carbon's toAtomString): the offset as +00:00, never Z.
const atom = "2006-01-02T15:04:05-07:00"

// The page sizes of the server list: the reference shows 50 and allows 100.
const (
	serverPerPage    = 50
	maxServerPerPage = 100
)

// Servers is GET /api/client: the servers the user may reach, by default those they own or are a subuser of.
func (h *Handlers) Servers(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	q := c.QueryParams()
	query, err := listquery.Parse(q, listquery.ClientServers)
	if err != nil {
		return err
	}
	page, perPage, err := resp.PageParamsWith(q, serverPerPage, maxServerPerPage)
	if err != nil {
		return err
	}

	// A list of the administrator's servers that somebody else asked for is empty, not an error, so that nobody learns who is an
	// administrator. It is answered here and the store is not asked.
	scope := servers.ParseListScope(c.QueryParam("type"), p.User.RootAdmin)
	var result ServerPage
	if scope != servers.ScopeNone {
		result, err = h.d.Servers.List(c.Request().Context(), ServerList{Viewer: p.User.ID, Scope: scope, Query: query, Page: page, PerPage: perPage})
		if err != nil {
			return storeError(err)
		}
	}

	held := make([][]string, len(result.Servers))
	var readers []uint64
	for i, s := range result.Servers {
		held[i] = permissions.Effective(p.User.RootAdmin, s.OwnerID == p.User.ID, s.SubuserPermissions)
		if permissions.Has(held[i], permissions.UserRead) {
			readers = append(readers, s.ID)
		}
	}
	want := wanted(query.Includes)
	subusers, err := h.subusers(c, want, readers)
	if err != nil {
		return err
	}

	items := make([]resp.Item, len(result.Servers))
	for i, s := range result.Servers {
		if items[i], err = h.serverItem(s, p.User.ID, held[i], want, subusers[s.ID]); err != nil {
			return err
		}
	}
	list := resp.NewList(items).WithPagination(resp.NewPagination(c.Request().URL, result.Total, perPage, page, len(items)))
	return ok(c, list)
}

// Server is GET /api/client/servers/{server}: the page of one server, with what the caller may do with it.
func (h *Handlers) Server(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	if p.Server == nil {
		return apierr.Forbidden("This action is unauthorized.")
	}
	s, err := h.d.Servers.One(c.Request().Context(), p.Server.ID)
	if err != nil {
		return storeError(err)
	}

	want := wanted(listquery.ParseIncludes(c.QueryParams(), listquery.ClientServers.Includes))
	var readers []uint64
	if permissions.Has(p.Permissions, permissions.UserRead) {
		readers = append(readers, s.ID)
	}
	subusers, err := h.subusers(c, want, readers)
	if err != nil {
		return err
	}

	item, err := h.serverItem(s, p.User.ID, p.Permissions, want, subusers[s.ID])
	if err != nil {
		return err
	}
	perms := p.Permissions
	if perms == nil {
		perms = []string{}
	}
	return ok(c, item.WithMeta("is_server_owner", p.User.ID == s.OwnerID).WithMeta("user_permissions", perms))
}

// Permissions is GET /api/client/permissions: every permission a subuser can be given, in groups.
func (h *Handlers) Permissions(c echo.Context) error {
	if _, err := caller(c); err != nil {
		return err
	}
	return ok(c, map[string]any{
		"object":     "system_permissions",
		"attributes": map[string]any{"permissions": permissions.All()},
	})
}

// wanted turns the includes of a request into a set.
func wanted(includes []listquery.Include) map[string]bool {
	out := make(map[string]bool, len(includes))
	for _, in := range includes {
		out[in.Name] = true
	}
	return out
}

// subusers reads the subusers of the servers whose caller may see them, and only if the request asked for them.
func (h *Handlers) subusers(c echo.Context, want map[string]bool, serverIDs []uint64) (map[uint64][]Subuser, error) {
	if !want["subusers"] || len(serverIDs) == 0 {
		return nil, nil
	}
	out, err := h.d.Servers.Subusers(c.Request().Context(), serverIDs)
	if err != nil {
		return nil, storeError(err)
	}
	return out, nil
}

type serverAttributes struct {
	ServerOwner            bool          `json:"server_owner"`
	Identifier             string        `json:"identifier"`
	DeprecatedUUIDShort    string        `json:"__deprecated_uuid_short"`
	ServerIdentifier       string        `json:"server_identifier"`
	InternalID             uint64        `json:"internal_id"`
	UUID                   string        `json:"uuid"`
	Name                   string        `json:"name"`
	Node                   string        `json:"node"`
	IsNodeUnderMaintenance bool          `json:"is_node_under_maintenance"`
	SFTPDetails            sftpDetails   `json:"sftp_details"`
	Description            string        `json:"description"`
	Limits                 limits        `json:"limits"`
	Invocation             string        `json:"invocation"`
	DockerImage            string        `json:"docker_image"`
	EggFeatures            []string      `json:"egg_features"`
	FeatureLimits          featureLimits `json:"feature_limits"`
	Status                 *string       `json:"status"`
	IsSuspended            bool          `json:"is_suspended"`
	IsInstalling           bool          `json:"is_installing"`
	IsTransferring         bool          `json:"is_transferring"`
	SkipScripts            bool          `json:"skip_scripts"`
}

type sftpDetails struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
}

type limits struct {
	Memory      int64   `json:"memory"`
	Swap        int64   `json:"swap"`
	Disk        int64   `json:"disk"`
	IO          int64   `json:"io"`
	CPU         int64   `json:"cpu"`
	Threads     *string `json:"threads"`
	OOMDisabled bool    `json:"oom_disabled"`
}

type featureLimits struct {
	Databases   *int64 `json:"databases"`
	Allocations *int64 `json:"allocations"`
	Backups     *int64 `json:"backups"`
}

type allocationAttributes struct {
	ID        uint64  `json:"id"`
	IP        string  `json:"ip"`
	IPAlias   *string `json:"ip_alias"`
	Port      int     `json:"port"`
	Notes     *string `json:"notes"`
	IsDefault bool    `json:"is_default"`
}

type variableAttributes struct {
	Name         string  `json:"name"`
	Description  string  `json:"description"`
	EnvVariable  string  `json:"env_variable"`
	DefaultValue string  `json:"default_value"`
	ServerValue  *string `json:"server_value"`
	IsEditable   bool    `json:"is_editable"`
	Rules        string  `json:"rules"`
}

type eggAttributes struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

// userAttributes is a user as other users see one, with the e-mail address: the frontend shows it in a tip, as the reference does.
type userAttributes struct {
	UUID       string `json:"uuid"`
	Identifier string `json:"identifier"`
	Username   string `json:"username"`
	Email      string `json:"email"`
	Image      string `json:"image"`
	TwoFactor  bool   `json:"2fa_enabled"`
	CreatedAt  string `json:"created_at"`
}

type subuserAttributes struct {
	userAttributes
	Permissions []string `json:"permissions"`
}

// serverItem builds the object of a server for a caller who holds the permissions in held. The allocations and variables always
// come with it; the egg and subusers only when asked for.
func (h *Handlers) serverItem(s Server, viewer uint64, held []string, want map[string]bool, subusers []Subuser) (resp.Item, error) {
	can := func(permission string) bool { return permissions.Has(held, permission) }

	serverIdentifier, err := ids.Realtime("serv", s.UUID)
	if err != nil {
		return resp.Item{}, apierr.Internal(err)
	}
	var status *string
	if s.Status != "" {
		status = &s.Status
	}

	item := resp.NewItem("server", serverAttributes{
		ServerOwner: viewer == s.OwnerID,
		// The identifier of the routes is the short UUID. The reference can switch to the long form (a setting we do not offer),
		// so the long form is only given under its own name.
		Identifier:             s.ShortID,
		DeprecatedUUIDShort:    s.ShortID,
		ServerIdentifier:       serverIdentifier,
		InternalID:             s.ID,
		UUID:                   s.UUID,
		Name:                   s.Name,
		Node:                   s.Node.Name,
		IsNodeUnderMaintenance: s.Node.Maintenance,
		SFTPDetails:            sftpDetails{IP: s.Node.FQDN, Port: s.Node.SFTPPort},
		Description:            s.Description,
		Limits:                 limits{Memory: s.Memory, Swap: s.Swap, Disk: s.Disk, IO: s.IO, CPU: s.CPU, Threads: s.Threads, OOMDisabled: s.OOMDisabled},
		// The values of the variables show in the command only to someone who may read the startup settings.
		Invocation:     invocation(s, !can(permissions.StartupRead)),
		DockerImage:    s.Image,
		EggFeatures:    s.Egg.Features,
		FeatureLimits:  featureLimits{Databases: s.DatabaseLimit, Allocations: s.AllocationLimit, Backups: s.BackupLimit},
		Status:         status,
		IsSuspended:    s.Status == servers.StatusSuspended,
		IsInstalling:   !servers.Installed(s.Status),
		IsTransferring: s.Transferring,
		SkipScripts:    s.SkipScripts,
	})

	// Someone who may not read the allocations still gets the primary one, without its notes, because the pages need an address to
	// show.
	allocations := s.Allocations
	if !can(permissions.AllocationRead) {
		primary := s.Primary
		primary.Notes = nil
		allocations = []Allocation{primary}
	}
	list := make([]resp.Item, len(allocations))
	for i, a := range allocations {
		list[i] = resp.NewItem("allocation", allocationAttributes{
			ID: a.ID, IP: a.IP, IPAlias: a.IPAlias, Port: a.Port, Notes: a.Notes, IsDefault: a.ID == s.Primary.ID,
		})
	}
	item = item.With("allocations", resp.NewList(list))

	if can(permissions.StartupRead) {
		var vars []resp.Item
		for _, v := range s.Variables {
			if !v.UserViewable {
				continue
			}
			vars = append(vars, resp.NewItem("egg_variable", variableAttributes{
				Name: v.Name, Description: v.Description, EnvVariable: v.EnvName, DefaultValue: v.Default, ServerValue: v.ServerValue,
				IsEditable: v.UserEditable, Rules: v.Rules,
			}))
		}
		item = item.With("variables", resp.NewList(vars))
	} else {
		item = item.With("variables", nullResource())
	}

	if want["egg"] {
		item = item.With("egg", resp.NewItem("egg", eggAttributes{UUID: s.Egg.UUID, Name: s.Egg.Name}))
	}
	if want["subusers"] {
		if !can(permissions.UserRead) {
			item = item.With("subusers", nullResource())
		} else {
			list := make([]resp.Item, len(subusers))
			for i, u := range subusers {
				attrs, err := h.person(u.User)
				if err != nil {
					return resp.Item{}, err
				}
				perms := u.Permissions
				if perms == nil {
					perms = []string{}
				}
				list[i] = resp.NewItem("server_subuser", subuserAttributes{userAttributes: attrs, Permissions: perms})
			}
			item = item.With("subusers", resp.NewList(list))
		}
	}
	return item, nil
}

// nullResource stands for an include the caller may not have, as the reference's serializer writes it.
func nullResource() resp.Item { return resp.NewItem("null_resource", nil) }

// invocation is the startup command as it is shown: the built-in placeholders and the variables filled in, the values of those
// a user may not see (or of all, with hideAll) as "[hidden]".
func invocation(s Server, hideAll bool) string {
	vars := make([]servers.Variable, len(s.Variables))
	for i, v := range s.Variables {
		vars[i] = servers.Variable{EnvName: v.EnvName, Default: v.Default, Value: v.ServerValue, UserViewable: v.UserViewable}
	}
	return servers.StartupCommand(servers.Server{
		Startup:    s.Startup,
		Memory:     s.Memory,
		Allocation: servers.Allocation{IP: s.Primary.IP, Port: s.Primary.Port},
		Variables:  vars,
	}, hideAll)
}

// person turns a user into what the client API shows of one.
func (h *Handlers) person(u Person) (userAttributes, error) { return personAttributes(u, h.d.Location) }

func personAttributes(u Person, loc *time.Location) (userAttributes, error) {
	identifier, err := ids.Realtime("user", u.UUID)
	if err != nil {
		return userAttributes{}, apierr.Internal(err)
	}
	sum := md5.Sum([]byte(strings.ToLower(u.Email))) //nolint:gosec // see the import
	return userAttributes{
		UUID:       u.UUID,
		Identifier: identifier,
		Username:   u.Username,
		Email:      u.Email,
		Image:      "https://gravatar.com/avatar/" + hex.EncodeToString(sum[:]),
		TwoFactor:  u.UsesTOTP,
		CreatedAt:  u.CreatedAt.In(loc).Format(atom),
	}, nil
}

// SubuserItem is the object of a subuser as the client API shows it: the user, with the permissions they were given. The routes
// that change subusers answer with it too.
func SubuserItem(u Person, permissions []string, loc *time.Location) (resp.Item, error) {
	attrs, err := personAttributes(u, loc)
	if err != nil {
		return resp.Item{}, err
	}
	if permissions == nil {
		permissions = []string{}
	}
	return resp.NewItem("server_subuser", subuserAttributes{userAttributes: attrs, Permissions: permissions}), nil
}

// VariableItem is the object of an egg variable as the client API shows it to a user, who may only see the variables the egg
// marks as viewable: asking for another is a bug of the caller, and nothing is shown.
func VariableItem(v Variable) (resp.Item, error) {
	if !v.UserViewable {
		return resp.Item{}, errors.New("clientapi: a variable the user may not see was asked for")
	}
	return resp.NewItem("egg_variable", variableAttributes{
		Name: v.Name, Description: v.Description, EnvVariable: v.EnvName, DefaultValue: v.Default, ServerValue: v.ServerValue,
		IsEditable: v.UserEditable, Rules: v.Rules,
	}), nil
}

// StartupCommand is the startup command of a server as it is shown, with the values of the variables a user may not see (or all, with
// hideAll) as "[hidden]".
func StartupCommand(s Server, hideAll bool) string { return invocation(s, hideAll) }

// AllocationItem is the object of an allocation of a server as the client API shows it; isDefault says it is the primary one.
func AllocationItem(a Allocation, isDefault bool) resp.Item {
	return resp.NewItem("allocation", allocationAttributes{
		ID: a.ID, IP: a.IP, IPAlias: a.IPAlias, Port: a.Port, Notes: a.Notes, IsDefault: isDefault,
	})
}
