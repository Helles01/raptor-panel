// Package audit checks stored data against what Raptor expects of it. The reference panel accepted some data that Raptor does not: a
// schedule that its cron library read leniently, an egg rule that Raptor's interpreter does not support, a stored list of addresses
// that nothing can read. The checks run on data that is already in the database before the cutover, and again whenever somebody
// asks, so that every such row is found while it is still easy to fix, not when its owner runs into it.
//
// The package reads no database: the command that calls it loads the rows into the types below. Every finding says which row, what
// is wrong and what follows from it.
package audit

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/eggrules"
	"github.com/Helles01/raptor-panel/internal/eggs"
	"github.com/Helles01/raptor-panel/internal/nodes"
	"github.com/Helles01/raptor-panel/internal/permissions"
	"github.com/Helles01/raptor-panel/internal/schedules"
	"github.com/Helles01/raptor-panel/internal/servers"
	"github.com/Helles01/raptor-panel/internal/settings"
	"github.com/Helles01/raptor-panel/internal/users"
)

// Severity is how much a finding matters. An Error is data that Raptor cannot use at all, so that something stops working for
// somebody (a user who cannot sign in, a schedule that never runs). A Warning is data that Raptor still reads but would refuse to
// write again: the row works until somebody changes it.
type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
)

// Finding is one problem in one row.
type Finding struct {
	Section  string   `json:"section"`
	Ref      string   `json:"ref"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
}

// Section is the result for one kind of row.
type Section struct {
	Name     string    `json:"name"`
	Checked  int       `json:"checked"`
	Findings []Finding `json:"findings"`
}

// Report is the result of a run, one section for each kind of row, always in the same order.
type Report struct {
	Sections []Section `json:"sections"`
}

// Count returns the number of findings of a severity.
func (r Report) Count(s Severity) int {
	n := 0
	for _, sec := range r.Sections {
		for _, f := range sec.Findings {
			if f.Severity == s {
				n++
			}
		}
	}
	return n
}

// HasErrors reports whether anything was found that stops something from working.
func (r Report) HasErrors() bool { return r.Count(Error) > 0 }

// Text writes the report for a person: a line for each section, and a line for each finding under it.
func (r Report) Text() string {
	var b strings.Builder
	for _, sec := range r.Sections {
		errs, warns := 0, 0
		for _, f := range sec.Findings {
			if f.Severity == Error {
				errs++
			} else {
				warns++
			}
		}
		fmt.Fprintf(&b, "%s: %d checked, %d errors, %d warnings\n", sec.Name, sec.Checked, errs, warns)
		for _, f := range sec.Findings {
			fmt.Fprintf(&b, "  %-7s %s: %s\n", f.Severity, f.Ref, f.Message)
		}
	}
	return b.String()
}

// JSON writes the report for a program.
func (r Report) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }

// The rows. They hold only what a check needs.

// EggVariable is a variable of an egg with its rules.
type EggVariable struct {
	ID      uint64
	Egg     string
	EnvName string
	Rules   string
}

// EggImages is the Docker images of an egg as stored: a JSON object from a name to an image.
type EggImages struct {
	ID   uint64
	Egg  string
	JSON string
}

// ScheduleTask is a task of a schedule.
type ScheduleTask struct {
	ID         uint64
	Action     string
	Payload    string
	TimeOffset int
	Sequence   int
}

// Schedule is a schedule with its tasks.
type Schedule struct {
	ID         uint64
	Name       string
	Minute     string
	Hour       string
	DayOfMonth string
	Month      string
	DayOfWeek  string
	Tasks      []ScheduleTask
}

// APIKey is an API key with the list of addresses it may be used from, as stored.
type APIKey struct {
	ID         uint64
	Identifier string
	AllowedIPs []string
}

// Subuser is a user's access to a server with the permissions stored for it.
type Subuser struct {
	ID          uint64
	ServerID    uint64
	Permissions []string
}

// Server is what a server's own settings are checked on.
type Server struct {
	ID          uint64
	Name        string
	Description string
	ExternalID  *string
	Image       string
	Startup     string
	Limits      servers.Limits
	Features    servers.FeatureLimits
}

// Node is a node's settings.
type Node struct {
	ID       uint64
	Settings nodes.Settings
}

// Location is a location's names.
type Location struct {
	ID    uint64
	Short string
	Long  string
}

// Allocation is an address and a port of a node.
type Allocation struct {
	ID     uint64
	NodeID uint64
	IP     string
	Port   int
	Alias  string
}

// User is a user as far as signing in and mail are concerned.
type User struct {
	ID            uint64
	Username      string
	Email         string
	PasswordHash  string
	UsesTOTP      bool
	HasTOTPSecret bool
}

// RecoveryToken is a stored two-factor recovery token.
type RecoveryToken struct {
	ID     uint64
	UserID uint64
	Token  string
}

// Setting is a stored panel setting with its name as Raptor knows it, without the storage prefix.
type Setting struct {
	Key   string
	Value string
}

// Input is everything a run looks at. A kind of row that is left empty is reported as nothing checked.
type Input struct {
	EggVariables   []EggVariable
	EggImages      []EggImages
	Schedules      []Schedule
	APIKeys        []APIKey
	Subusers       []Subuser
	Servers        []Server
	Nodes          []Node
	Locations      []Location
	Allocations    []Allocation
	Users          []User
	RecoveryTokens []RecoveryToken
	Settings       []Setting
}

// Run checks everything in in. now is the time a schedule is asked for its next run, to find the ones that have none.
func Run(in Input, now time.Time) Report {
	return Report{Sections: []Section{
		eggVariables(in.EggVariables),
		eggImages(in.EggImages),
		scheduleSection(in.Schedules, now),
		apiKeys(in.APIKeys),
		subusers(in.Subusers),
		serverSection(in.Servers),
		nodeSection(in.Nodes),
		locations(in.Locations),
		allocations(in.Allocations),
		userSection(in.Users),
		recoveryTokens(in.RecoveryTokens),
		settingSection(in.Settings),
	}}
}

// section collects the findings of one kind of row.
type section struct{ Section }

func newSection(name string, checked int) *section {
	return &section{Section{Name: name, Checked: checked, Findings: []Finding{}}}
}

func (s *section) add(sev Severity, ref, format string, args ...any) {
	s.Findings = append(s.Findings, Finding{Section: s.Name, Ref: ref, Severity: sev, Message: fmt.Sprintf(format, args...)})
}

func ref(kind string, id uint64, label string) string {
	if label == "" {
		return fmt.Sprintf("%s#%d", kind, id)
	}
	return fmt.Sprintf("%s#%d (%s)", kind, id, label)
}

func eggVariables(rows []EggVariable) Section {
	s := newSection("egg_variables", len(rows))
	for _, r := range rows {
		at := ref("egg_variables", r.ID, r.Egg+" "+r.EnvName)
		if _, err := eggrules.Parse(r.Rules); err != nil {
			s.add(Error, at, "the rules cannot be applied (%v): nobody can set this variable until they are changed", err)
		}
		if err := eggs.CheckEnvName(r.EnvName); err != nil {
			s.add(Warning, at, "%v: Raptor would refuse to import an egg with this variable", err)
		}
	}
	return s.Section
}

func eggImages(rows []EggImages) Section {
	s := newSection("egg_images", len(rows))
	for _, r := range rows {
		at := ref("eggs", r.ID, r.Egg)
		var images map[string]string
		if err := json.Unmarshal([]byte(r.JSON), &images); err != nil {
			s.add(Error, at, "the list of images is not a JSON object of names and images (%v): a user cannot choose an image for its servers", err)
			continue
		}
		if len(images) == 0 {
			s.add(Error, at, "the egg has no Docker image: its servers cannot change theirs")
			continue
		}
		names := make([]string, 0, len(images))
		for name := range images {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if err := servers.ValidateImage(images[name]); err != nil {
				s.add(Warning, at, "the image %q (%q): %v", name, images[name], err)
			}
		}
	}
	return s.Section
}

func scheduleSection(rows []Schedule, now time.Time) Section {
	s := newSection("schedules", len(rows))
	for _, r := range rows {
		at := ref("schedules", r.ID, r.Name)
		if err := schedules.ValidateName(r.Name); err != nil {
			s.add(Warning, at, "the name: %v", err)
		}
		c, err := schedules.Parse(r.Minute, r.Hour, r.DayOfMonth, r.Month, r.DayOfWeek)
		if err != nil {
			s.add(Error, at, "the cron fields %q %q %q %q %q are not an expression Raptor can run (%v): the schedule never runs",
				r.Minute, r.Hour, r.DayOfMonth, r.Month, r.DayOfWeek, err)
		} else if _, err := c.Next(now); err != nil {
			s.add(Error, at, "the cron fields %q %q %q %q %q never match a date (%v): the schedule never runs",
				r.Minute, r.Hour, r.DayOfMonth, r.Month, r.DayOfWeek, err)
		}
		seen := map[int]bool{}
		for _, t := range r.Tasks {
			tat := fmt.Sprintf("%s, task %d", at, t.ID)
			switch t.Action {
			case schedules.ActionCommand, schedules.ActionPower, schedules.ActionBackup:
				if err := (schedules.Task{Action: t.Action, Payload: t.Payload, TimeOffset: t.TimeOffset, Sequence: t.Sequence}).Validate(); err != nil {
					s.add(Warning, tat, "%v", err)
				}
			default:
				s.add(Error, tat, "the action %q is not known: a run stops at this task", t.Action)
			}
			if seen[t.Sequence] {
				s.add(Warning, tat, "the position %d is used by another task of the schedule", t.Sequence)
			}
			seen[t.Sequence] = true
		}
	}
	return s.Section
}

func apiKeys(rows []APIKey) Section {
	s := newSection("api_keys", len(rows))
	for _, r := range rows {
		_, bad := auth.LoadAllowedIPs(r.AllowedIPs)
		switch {
		case bad == 0:
		case bad == len(r.AllowedIPs):
			s.add(Error, ref("api_keys", r.ID, r.Identifier), "none of the %d allowed addresses can be read: the key works from nowhere (a list is a restriction, and Raptor does not read it as no list)", bad)
		default:
			s.add(Warning, ref("api_keys", r.ID, r.Identifier), "%d of the %d allowed addresses cannot be read and match nothing", bad, len(r.AllowedIPs))
		}
	}
	return s.Section
}

func subusers(rows []Subuser) Section {
	s := newSection("subusers", len(rows))
	for _, r := range rows {
		var unknown []string
		for _, p := range r.Permissions {
			if !permissions.Valid(p) {
				unknown = append(unknown, p)
			}
		}
		if len(unknown) > 0 {
			s.add(Warning, ref("subusers", r.ID, fmt.Sprintf("server %d", r.ServerID)),
				"permissions that do not exist: %s; only the owner and administrators can change this subuser", strings.Join(unknown, ", "))
		}
	}
	return s.Section
}

func serverSection(rows []Server) Section {
	s := newSection("servers", len(rows))
	for _, r := range rows {
		n := servers.NewServer{
			ExternalID: r.ExternalID, Name: r.Name, Description: r.Description, OwnerID: 1, EggID: 1, Image: r.Image, Startup: r.Startup,
			Limits: r.Limits, Features: r.Features, Allocation: 1,
		}
		if err := n.Validate(); err != nil {
			s.add(Warning, ref("servers", r.ID, r.Name), "%v: the server runs, but a change of its settings is refused until this is corrected", err)
		}
	}
	return s.Section
}

func nodeSection(rows []Node) Section {
	s := newSection("nodes", len(rows))
	for _, r := range rows {
		if err := r.Settings.Validate(); err != nil {
			s.add(Warning, ref("nodes", r.ID, r.Settings.Name), "%v: a change of the node is refused until this is corrected", err)
		}
	}
	return s.Section
}

func locations(rows []Location) Section {
	s := newSection("locations", len(rows))
	for _, r := range rows {
		if err := nodes.ValidateLocation(r.Short, r.Long); err != nil {
			s.add(Warning, ref("locations", r.ID, r.Short), "%v", err)
		}
	}
	return s.Section
}

func allocations(rows []Allocation) Section {
	s := newSection("allocations", len(rows))
	for _, r := range rows {
		at := ref("allocations", r.ID, fmt.Sprintf("node %d, %s:%d", r.NodeID, r.IP, r.Port))
		if _, err := netip.ParseAddr(r.IP); err != nil {
			s.add(Error, at, "the address cannot be read: the node's configuration of this server breaks")
		}
		if r.Port < 1 || r.Port > 65535 {
			s.add(Error, at, "the port is outside 1 to 65535")
		}
		if err := nodes.ValidateAlias(r.Alias); err != nil {
			s.add(Warning, at, "%v", err)
		}
	}
	return s.Section
}

func userSection(rows []User) Section {
	s := newSection("users", len(rows))
	for _, r := range rows {
		at := ref("users", r.ID, r.Username)
		if !auth.ValidHash(r.PasswordHash) {
			s.add(Error, at, "the stored password is not a bcrypt hash: the user cannot sign in until the password is reset")
		}
		if r.UsesTOTP && !r.HasTOTPSecret {
			s.add(Error, at, "two-factor authentication is on but there is no secret: the user can never pass the second step")
		}
		if err := users.ValidateUsername(r.Username); err != nil {
			s.add(Warning, at, "%v", err)
		} else if r.Username != strings.ToLower(r.Username) {
			s.add(Warning, at, "the name has capital letters; the reference stores user names in lower case")
		}
		if err := users.ValidateEmail(r.Email); err != nil {
			s.add(Warning, at, "%v: mail to this user may not be delivered, and the address cannot be changed to the same", err)
		}
	}
	return s.Section
}

func recoveryTokens(rows []RecoveryToken) Section {
	s := newSection("recovery_tokens", len(rows))
	for _, r := range rows {
		if !auth.ValidHash(r.Token) {
			s.add(Error, ref("recovery_tokens", r.ID, fmt.Sprintf("user %d", r.UserID)), "the stored token is not a bcrypt hash: it can never be used")
		}
	}
	return s.Section
}

func settingSection(rows []Setting) Section {
	s := newSection("settings", len(rows))
	for _, r := range rows {
		if !settings.Known(r.Key) {
			continue
		}
		if err := settings.Validate(r.Key, r.Value); err != nil {
			s.add(Warning, "settings."+r.Key, "%v: Raptor refuses to save this value again", err)
		}
	}
	return s.Section
}
