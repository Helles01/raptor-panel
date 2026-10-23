package servers

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/Helles01/raptor-panel/internal/eggrules"
	"github.com/Helles01/raptor-panel/internal/fielderr"
)

// ListScope says which servers a user's list holds.
type ListScope int

const (
	// ScopeAccessible: the servers the user owns or is a subuser of. It is what any other value of the type gives.
	ScopeAccessible ListScope = iota
	// ScopeOwner: only the servers the user owns.
	ScopeOwner
	// ScopeAdminOthers: for an administrator, the servers that they can see only because they are an administrator.
	ScopeAdminOthers
	// ScopeAdminAll: for an administrator, every server.
	ScopeAdminAll
	// ScopeNone: an administrator's list that somebody else asked for. It is empty, not an error, so that a client does not learn
	// who is an administrator.
	ScopeNone
)

// ParseListScope reads the "type" of the client's server list.
func ParseListScope(typ string, admin bool) ListScope {
	switch typ {
	case "admin", "admin-all":
		switch {
		case !admin:
			return ScopeNone
		case typ == "admin-all":
			return ScopeAdminAll
		}
		return ScopeAdminOthers
	case "owner":
		return ScopeOwner
	}
	return ScopeAccessible
}

// EggVariable is an egg variable as the validation of a server's environment sees it.
type EggVariable struct {
	EnvName string
	// Name is the variable's name for people, which the messages use ("The Server Jar File variable field is required.").
	Name         string
	Rules        string
	UserViewable bool
	UserEditable bool
}

// VariableValue is the value to store for a variable. Store is empty when the server has none.
type VariableValue struct {
	EnvName string
	Store   string
}

// VariableProblem is a variable whose value the rules refuse, with one message per rule. The API answers it with 422 entries on
// the field "environment.<NAME>".
type VariableProblem struct {
	Field    string
	Messages []string
}

// EnvironmentError holds the problems of all variables, so that a client sees every wrong value at once.
type EnvironmentError struct{ Problems []VariableProblem }

func (e *EnvironmentError) Error() string {
	return fmt.Sprintf("environment: %d variables have a value that is not allowed", len(e.Problems))
}

// ValidateEnvironment checks the values sent for the variables of an egg and returns what to store for each. An administrator's
// request covers every variable, and one that is not sent counts as having no value, so the rules of a required variable refuse
// it; a user's request covers only the variables the user may see and change, the others are left as they are. A name in values
// that is not a variable is ignored. A value is null, or text (numbers are sent as their text).
func ValidateEnvironment(vars []EggVariable, values map[string]*string, admin bool) ([]VariableValue, error) {
	var out []VariableValue
	var problems []VariableProblem
	for _, v := range vars {
		if !admin && !(v.UserEditable && v.UserViewable) {
			continue
		}
		rules, err := eggrules.Parse(v.Rules)
		if err != nil {
			return nil, fmt.Errorf("rules of variable %s cannot be applied: %w", v.EnvName, err)
		}

		value := eggrules.Normalize(values[v.EnvName])
		if msgs := rules.Validate(value, v.Name+" variable"); len(msgs) > 0 {
			problems = append(problems, VariableProblem{Field: "environment." + v.EnvName, Messages: msgs})
			continue
		}
		val := VariableValue{EnvName: v.EnvName}
		if value != nil {
			val.Store = *value
		}
		out = append(out, val)
	}
	if len(problems) > 0 {
		return nil, &EnvironmentError{Problems: problems}
	}
	return out, nil
}

// Details are the fields of a server that its details update sets.
type Details struct {
	ExternalID  *string
	OwnerID     uint64
	Name        string
	Description string
}

// DetailsChange is the body of PATCH /api/application/servers/{id}/details. The name and the owner are needed every time. The
// external id and the description keep their value when nil, and are cleared by a pointer to ""; the reference clears them when
// they are left out, so that a client which only renames a server loses its link to the billing system (C-049).
type DetailsChange struct {
	ExternalID  *string
	OwnerID     uint64
	Name        string
	Description *string
}

// DetailsPlan is a details update that is allowed.
type DetailsPlan struct {
	Next Details
	// OwnerChanged: have the node forget the previous owner's tokens and SFTP sessions (PreviousOwner), so that they lose their
	// access to the console and the files at once.
	OwnerChanged  bool
	PreviousOwner uint64
}

// PlanDetails checks a details update. That the new owner exists is for the store to check.
func PlanDetails(cur Details, ch DetailsChange) (DetailsPlan, error) {
	if err := validateName(ch.Name); err != nil {
		return DetailsPlan{}, err
	}
	if ch.OwnerID == 0 {
		return DetailsPlan{}, fieldErr("user", "is required")
	}

	next := Details{ExternalID: cur.ExternalID, OwnerID: ch.OwnerID, Name: ch.Name, Description: cur.Description}
	if ch.ExternalID != nil {
		next.ExternalID = nil
		if *ch.ExternalID != "" {
			if err := validateExternalID(*ch.ExternalID); err != nil {
				return DetailsPlan{}, err
			}
			next.ExternalID = ch.ExternalID
		}
	}
	if ch.Description != nil {
		if err := validateDescription(*ch.Description); err != nil {
			return DetailsPlan{}, err
		}
		next.Description = *ch.Description
	}
	return DetailsPlan{Next: next, OwnerChanged: cur.OwnerID != ch.OwnerID, PreviousOwner: cur.OwnerID}, nil
}

// StartupSettings are the fields of a server that its startup update sets.
type StartupSettings struct {
	Startup     string
	EggID       uint64
	Image       string
	SkipScripts bool
}

// StartupChange is the body of PATCH /api/application/servers/{id}/startup: all of it is needed, the environment as an
// object that may be empty.
type StartupChange struct {
	Startup     string
	EggID       uint64
	Image       string
	SkipScripts bool
	Environment map[string]*string
}

// StartupPlan is a startup update that is allowed.
type StartupPlan struct {
	Next       StartupSettings
	EggChanged bool
	// Values are the variables to store. They are empty when the environment was empty.
	Values []VariableValue
}

// PlanStartup checks a startup update against the variables of the egg it names (vars; that the egg exists is for the store to
// check). Like the reference it checks the values only when some were sent: an empty environment changes no variable and asks
// for none, not even the required ones. The image may be any image of the form that is allowed, so an administrator can set one
// that the egg does not list; a user cannot change it afterwards then (ErrImageLocked).
func PlanStartup(cur StartupSettings, ch StartupChange, vars []EggVariable) (StartupPlan, error) {
	if err := validateStartup(ch.Startup); err != nil {
		return StartupPlan{}, err
	}
	if ch.EggID == 0 {
		return StartupPlan{}, fielderr.New("egg", "is required")
	}
	if err := validateImage(ch.Image); err != nil {
		return StartupPlan{}, err
	}

	plan := StartupPlan{
		Next:       StartupSettings{Startup: ch.Startup, EggID: ch.EggID, Image: ch.Image, SkipScripts: ch.SkipScripts},
		EggChanged: cur.EggID != ch.EggID,
	}
	if len(ch.Environment) > 0 {
		values, err := ValidateEnvironment(vars, ch.Environment, true)
		if err != nil {
			return StartupPlan{}, err
		}
		plan.Values = values
	}
	return plan, nil
}

// SuspendAction is what an administrator asks of a server's suspension.
type SuspendAction int

const (
	Suspend SuspendAction = iota
	Unsuspend
)

// ErrSuspendTransfer and ErrSuspendBusy refuse to change the suspension of a server. The first has the reference's text.
var (
	ErrSuspendTransfer = errors.New("Cannot toggle suspension status on a server that is currently being transferred.") //nolint:staticcheck // the reference's text
	ErrSuspendBusy     = errors.New("This server is installing, restoring a backup or has a failed installation, so it cannot be suspended or released from suspension now.")
)

// SuspensionPlan is what a request to suspend or release a server does.
type SuspensionPlan struct {
	// Noop: the server is already as asked, so nothing is stored and the node is not told.
	Noop bool
	// Status is the status to store: "suspended", or the normal state (empty).
	Status string
}

// PlanSuspension decides a suspension. A server that is as asked is left alone. One that is being transferred cannot change.
// The suspension takes the place of the status, so a server that is installing, restoring a backup or has a failed
// installation would lose that state and come out of the suspension looking normal; it must be left to finish or be repaired
// first (C-049). If the node cannot be told, the caller sets the previous status back.
func PlanSuspension(action SuspendAction, status string, transferring bool) (SuspensionPlan, error) {
	suspending := action == Suspend
	if suspending == (status == StatusSuspended) {
		return SuspensionPlan{Noop: true, Status: status}, nil
	}
	if transferring {
		return SuspensionPlan{}, ErrSuspendTransfer
	}
	if status != "" && status != StatusSuspended {
		return SuspensionPlan{}, ErrSuspendBusy
	}
	if suspending {
		return SuspensionPlan{Status: StatusSuspended}, nil
	}
	return SuspensionPlan{}, nil
}

// ParseDeleteForce reads the last part of DELETE /api/application/servers/{id}/{force}: nothing, or "force". The reference
// treats any other word as no force; here it is not a route at all (ok is false: answer 404), so that a typo does not turn a
// forced deletion into a careful one, or the other way round.
func ParseDeleteForce(segment string) (force, ok bool) {
	switch segment {
	case "":
		return false, true
	case "force":
		return true, true
	}
	return false, false
}

// DeleteMayContinue says whether deleting a server goes on after the node refused or failed to delete it. It does when the
// deletion is forced, and when the node does not know the server (404), as in the reference: then there is nothing to delete
// there. A database that cannot be removed from its host stops the deletion unless it is forced.
func DeleteMayContinue(force bool, nodeStatus int) bool {
	return force || nodeStatus == http.StatusNotFound
}
