package servers

import (
	"errors"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/fielderr"
)

func str(s string) *string { return &s }

func startupVars() []StartupVariable {
	return []StartupVariable{
		{EnvName: "SERVER_JARFILE", UserViewable: true, UserEditable: true, Rules: "required|string|max:20", Value: str("server.jar")},
		{EnvName: "MOTD", UserViewable: true, UserEditable: true, Rules: "nullable|string|max:40"},
		{EnvName: "BUILD_NUMBER", UserViewable: true, UserEditable: false, Rules: "required|string"},
		{EnvName: "RCON_PASSWORD", UserViewable: false, UserEditable: true, Rules: "required|string"},
		{EnvName: "BROKEN", UserViewable: true, UserEditable: true, Rules: "required|no_such_rule"},
	}
}

func TestPlanStartupEdit(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   *string
		want    StartupEdit
		wantErr error
	}{
		{"new value", "SERVER_JARFILE", str("paper.jar"), StartupEdit{Store: "paper.jar", Old: str("server.jar"), New: "paper.jar", Changed: true}, nil},
		{"same value", "SERVER_JARFILE", str("server.jar"), StartupEdit{Store: "server.jar", Old: str("server.jar"), New: "server.jar"}, nil},
		{"value is trimmed", "SERVER_JARFILE", str("  paper.jar \n"), StartupEdit{Store: "paper.jar", Old: str("server.jar"), New: "paper.jar", Changed: true}, nil},
		{"first value of an unset variable", "MOTD", str("hello"), StartupEdit{Store: "hello", New: "hello", Changed: true}, nil},
		{"null for a nullable variable that was unset", "MOTD", nil, StartupEdit{}, nil},
		{"blank for a nullable variable that was unset", "MOTD", str("   "), StartupEdit{}, nil},
		{"unknown variable", "NOPE", str("x"), StartupEdit{}, ErrNoSuchVariable},
		{"names are case sensitive", "server_jarfile", str("x"), StartupEdit{}, ErrNoSuchVariable},
		{"empty key", "", str("x"), StartupEdit{}, ErrNoSuchVariable},
		{"hidden variable looks missing", "RCON_PASSWORD", str("x"), StartupEdit{}, ErrNoSuchVariable},
		{"read-only variable", "BUILD_NUMBER", str("x"), StartupEdit{}, ErrReadOnlyVariable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PlanStartupEdit(startupVars(), tt.key, tt.value)

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if got.Store != tt.want.Store || got.New != tt.want.New || got.Changed != tt.want.Changed {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
			if (got.Old == nil) != (tt.want.Old == nil) || (got.Old != nil && *got.Old != *tt.want.Old) {
				t.Errorf("Old = %v, want %v", got.Old, tt.want.Old)
			}
		})
	}
}

func TestPlanStartupEditClearingASetValue(t *testing.T) {
	vars := startupVars()
	vars[1].Value = str("welcome")

	got, err := PlanStartupEdit(vars, "MOTD", nil)

	if err != nil || got.Store != "" || !got.Changed || got.Old == nil || *got.Old != "welcome" {
		t.Errorf("got %+v, %v, want the value cleared and logged as a change", got, err)
	}
}

func TestPlanStartupEditRefusesWhatTheRulesRefuse(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value *string
		want  string
	}{
		{"missing for a required variable", "SERVER_JARFILE", nil, "The value field is required."},
		{"blank for a required variable", "SERVER_JARFILE", str("  "), "The value field is required."},
		{"too long", "SERVER_JARFILE", str(strings.Repeat("a", 21)), "greater than 20"},
		{"too long for the other variable", "MOTD", str(strings.Repeat("a", 41)), "greater than 40"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PlanStartupEdit(startupVars(), tt.key, tt.value)

			var ve *ValueError
			if !errors.As(err, &ve) {
				t.Fatalf("error = %v, want a *ValueError", err)
			}
			if len(ve.Messages) == 0 || !strings.Contains(strings.Join(ve.Messages, " "), tt.want) {
				t.Errorf("messages = %v, want one containing %q", ve.Messages, tt.want)
			}
			if got != (StartupEdit{}) {
				t.Errorf("a refused value must plan nothing: %+v", got)
			}
		})
	}
}

func TestPlanStartupEditWithRulesThatCannotBeApplied(t *testing.T) {
	_, err := PlanStartupEdit(startupVars(), "BROKEN", str("x"))

	var ve *ValueError
	if err == nil || errors.As(err, &ve) || errors.Is(err, ErrNoSuchVariable) || errors.Is(err, ErrReadOnlyVariable) {
		t.Errorf("error = %v, want a plain error that is neither the user's fault nor a 400", err)
	}
	if !strings.Contains(err.Error(), "BROKEN") {
		t.Errorf("error %q should name the variable", err)
	}
}

func TestPlanImageChange(t *testing.T) {
	images := []string{"ghcr.io/pterodactyl/yolks:java_17", "ghcr.io/pterodactyl/yolks:java_21"}
	tests := []struct {
		name        string
		current     string
		requested   string
		wantChanged bool
		wantField   string
		wantErr     error
	}{
		{"another image of the egg", images[0], images[1], true, "", nil},
		{"the same image", images[0], images[0], false, "", nil},
		{"an image the egg does not list", images[0], "evil/miner:latest", false, "docker_image", nil},
		{"a lookalike", images[0], images[0] + " ", false, "docker_image", nil},
		{"a different case", images[0], strings.ToUpper(images[0]), false, "docker_image", nil},
		{"nothing requested", images[0], "", false, "docker_image", nil},
		{"image set by an administrator", "registry.example.com/custom:1", images[1], false, "", ErrImageLocked},
		{"the request is checked before the lock", "registry.example.com/custom:1", "evil/miner", false, "docker_image", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed, err := PlanImageChange(tt.current, images, tt.requested)

			if changed != tt.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tt.wantChanged)
			}
			if tt.wantField != "" {
				if fe, ok := fielderr.Of(err); !ok || fe.Field != tt.wantField {
					t.Errorf("error = %v, want one about %q", err, tt.wantField)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestPlanImageChangeWithNoImages(t *testing.T) {
	if _, err := PlanImageChange("a", nil, "a"); err == nil {
		t.Error("an egg without images offers nothing to choose")
	}
}

func TestPlanRename(t *testing.T) {
	cur := Naming{Name: "Survival", Description: "old words"}
	tests := []struct {
		name      string
		newName   string
		desc      *string
		want      Renamed
		wantField string
	}{
		{"name only keeps the description", "Creative", nil, Renamed{Naming{"Creative", "old words"}, true, false}, ""},
		{"both", "Creative", str("new words"), Renamed{Naming{"Creative", "new words"}, true, true}, ""},
		{"description cleared", "Survival", str(""), Renamed{Naming{"Survival", ""}, false, true}, ""},
		{"nothing changes", "Survival", str("old words"), Renamed{Naming{"Survival", "old words"}, false, false}, ""},
		{"empty name", "", nil, Renamed{}, "name"},
		{"name too long", strings.Repeat("a", 192), nil, Renamed{}, "name"},
		{"name at the limit", strings.Repeat("a", 191), nil, Renamed{Naming{strings.Repeat("a", 191), "old words"}, true, false}, ""},
		{"name with a control character", "bad\x07name", nil, Renamed{}, "name"},
		{"description too long", "Survival", str(strings.Repeat("a", 2001)), Renamed{}, "description"},
		{"description with NUL", "Survival", str("a\x00b"), Renamed{}, "description"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PlanRename(cur, tt.newName, tt.desc)

			if tt.wantField != "" {
				if fe, ok := fielderr.Of(err); !ok || fe.Field != tt.wantField {
					t.Fatalf("error = %v, want one about %q", err, tt.wantField)
				}
				if got != (Renamed{}) {
					t.Errorf("a refused rename must plan nothing: %+v", got)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("got %+v, %v\nwant %+v", got, err, tt.want)
			}
		})
	}
}

func TestCheckCanReinstall(t *testing.T) {
	tests := []struct {
		name        string
		status      string
		skipScripts bool
		wantErr     error
	}{
		{"normal server", "", false, nil},
		{"normal server that skips scripts", "", true, ErrReinstallSkipped},
		{"suspended server that skips scripts", StatusSuspended, true, ErrReinstallSkipped},
		{"installing server that skips scripts", StatusInstalling, true, nil},
		{"failed install that skips scripts", StatusInstallFailed, true, nil},
		{"failed reinstall that skips scripts", StatusReinstallFailed, true, nil},
		{"failed install", StatusInstallFailed, false, nil},
		{"restoring server that skips scripts", StatusRestoringBackup, true, ErrReinstallSkipped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := CheckCanReinstall(tt.status, tt.skipScripts); !errors.Is(err, tt.wantErr) {
				t.Errorf("error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateReinstall(t *testing.T) {
	tests := []struct {
		name    string
		state   State
		wantMsg string
	}{
		{"normal", State{}, ""},
		{"a failed first installation is let through", State{Status: StatusInstallFailed}, ""},
		{"a failed reinstall", State{Status: StatusReinstallFailed}, ""},
		{"still installing", State{Status: StatusInstalling}, "not yet completed its installation"},
		{"suspended", State{Status: StatusSuspended}, "suspended"},
		{"restoring", State{Status: StatusRestoringBackup}, "restoring"},
		{"failed install on a node under maintenance", State{Status: StatusInstallFailed, NodeMaintenance: true}, "maintenance"},
		{"failed install while transferring", State{Status: StatusInstallFailed, Transferring: true}, "transferred"},
		{"normal server on a node under maintenance", State{NodeMaintenance: true}, "maintenance"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.state.ValidateReinstall()

			switch {
			case tt.wantMsg == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tt.wantMsg != "" && (err == nil || !strings.Contains(err.Error(), tt.wantMsg)):
				t.Errorf("error = %v, want one containing %q", err, tt.wantMsg)
			}
		})
	}
}

func TestOnlyTheReinstallLetsAFailedInstallThrough(t *testing.T) {
	if err := (State{Status: StatusInstallFailed}).Validate(); err == nil {
		t.Error("every other request must still refuse a server whose installation failed")
	}
}

func TestSettingsErrorsKeepTheReferencesText(t *testing.T) {
	for err, part := range map[error]string{
		ErrNoSuchVariable:   "does not exist",
		ErrReadOnlyVariable: "read-only",
		ErrImageLocked:      "manually set by an administrator",
		ErrReinstallSkipped: "skip its egg's install script",
	} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("%q does not contain %q", err, part)
		}
	}
}

func TestPlanImageChangeSaysWhenNothingWasRequested(t *testing.T) {
	_, err := PlanImageChange("a", []string{"a"}, "")

	if fe, ok := fielderr.Of(err); !ok || fe.Message != "is required" {
		t.Errorf("error = %v, want docker_image to be required", err)
	}
}

func TestValueErrorText(t *testing.T) {
	err := &ValueError{Messages: []string{"The value field is required.", "Another."}}

	if got := err.Error(); got != "value: The value field is required. Another." {
		t.Errorf("Error() = %q", got)
	}
}
