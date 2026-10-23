package servers

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Helles01/raptor-panel/internal/eggrules"
	"github.com/Helles01/raptor-panel/internal/fielderr"
)

// What a user of a server may change about it, with the reference's texts. The four errors are answered with 400.
var (
	ErrNoSuchVariable   = errors.New("The environment variable you are trying to edit does not exist.")                                                           //nolint:staticcheck // the reference's text
	ErrReadOnlyVariable = errors.New("The environment variable you are trying to edit is read-only.")                                                             //nolint:staticcheck // the reference's text
	ErrImageLocked      = errors.New("This server's Docker image has been manually set by an administrator and cannot be updated.")                               //nolint:staticcheck // the reference's text
	ErrReinstallSkipped = errors.New("This server is configured to skip its egg's install script. Reinstalling is not available until that setting is disabled.") //nolint:staticcheck // the reference's text
)

// ValueError is a startup value that the rules of its variable refuse. The API answers it with a 422 entry on the
// field "value" for each message.
type ValueError struct{ Messages []string }

func (e *ValueError) Error() string { return "value: " + strings.Join(e.Messages, " ") }

// StartupVariable is an egg variable of a server as the startup endpoint sees it.
type StartupVariable struct {
	EnvName      string
	UserViewable bool
	UserEditable bool
	// Rules is the rule text of the egg variable, such as "required|string|max:20".
	Rules string
	// Value is the server's own value, nil while it has none.
	Value *string
}

// StartupEdit is what to do after a user has set a variable.
type StartupEdit struct {
	// Store is the value to save: an empty string when the user sent nothing.
	Store string
	// Old and New are for the activity entry. Old is nil for a variable the server had not set.
	Old *string
	New string
	// Changed is false when the value stays as it was, which is not logged.
	Changed bool
}

// PlanStartupEdit checks that the user may set the variable called key and that value passes the variable's rules.
// value is nil when the request carried null or a blank string. Like the reference, a variable the user cannot see is
// reported as missing, so that its name is not given away.
func PlanStartupEdit(vars []StartupVariable, key string, value *string) (StartupEdit, error) {
	i := slices.IndexFunc(vars, func(v StartupVariable) bool { return v.EnvName == key })
	if i < 0 || !vars[i].UserViewable {
		return StartupEdit{}, ErrNoSuchVariable
	}
	v := vars[i]
	if !v.UserEditable {
		return StartupEdit{}, ErrReadOnlyVariable
	}

	rules, err := eggrules.Parse(v.Rules)
	if err != nil {
		return StartupEdit{}, fmt.Errorf("rules of variable %s cannot be applied: %w", v.EnvName, err)
	}
	value = eggrules.Normalize(value)
	if msgs := rules.Validate(value, "value"); len(msgs) > 0 {
		return StartupEdit{}, &ValueError{Messages: msgs}
	}

	edit := StartupEdit{Old: v.Value}
	if value != nil {
		edit.Store = *value
	}
	edit.New = edit.Store
	old := ""
	if v.Value != nil {
		old = *v.Value
	}
	edit.Changed = old != edit.Store
	return edit, nil
}

// PlanImageChange checks a new Docker image for a server. It must be one of the images of the egg, and the server must
// still use one of them: an image an administrator set by hand is left alone. changed is false when nothing differs.
func PlanImageChange(current string, eggImages []string, requested string) (changed bool, err error) {
	if requested == "" {
		return false, fielderr.New("docker_image", "is required")
	}
	if !slices.Contains(eggImages, requested) {
		return false, fielderr.New("docker_image", "is not one of the images of this egg")
	}
	if !slices.Contains(eggImages, current) {
		return false, ErrImageLocked
	}
	return current != requested, nil
}

// Naming is the name and description of a server.
type Naming struct{ Name, Description string }

// Renamed is the result of a rename: the new names and which of them differ, for the two activity entries.
type Renamed struct {
	Naming
	NameChanged        bool
	DescriptionChanged bool
}

// PlanRename checks a rename. description is nil when it was not sent, which keeps the old one; a blank one clears it.
func PlanRename(cur Naming, name string, description *string) (Renamed, error) {
	if err := validateName(name); err != nil {
		return Renamed{}, err
	}
	next := Naming{Name: name, Description: cur.Description}
	if description != nil {
		if err := validateDescription(*description); err != nil {
			return Renamed{}, err
		}
		next.Description = *description
	}
	return Renamed{Naming: next, NameChanged: next.Name != cur.Name, DescriptionChanged: next.Description != cur.Description}, nil
}

// CheckCanReinstall refuses a reinstall that would run no install script. A server in a failed or interrupted state
// is the exception: the reinstall report is its way back to a normal state.
func CheckCanReinstall(status string, skipScripts bool) error {
	if skipScripts && status != StatusInstalling && status != StatusInstallFailed && status != StatusReinstallFailed {
		return ErrReinstallSkipped
	}
	return nil
}

// ValidateReinstall is Validate for the reinstall request. A server whose first installation failed is let through: the
// reinstall is what its owner has to do (C-036), where the reference lets only an administrator do it.
func (s State) ValidateReinstall() error {
	if s.Status == StatusInstallFailed {
		s.Status = ""
	}
	return s.Validate()
}
