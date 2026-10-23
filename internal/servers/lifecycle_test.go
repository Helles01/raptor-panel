package servers

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/fielderr"
)

func TestParseListScope(t *testing.T) {
	tests := []struct {
		typ   string
		admin bool
		want  ListScope
	}{
		{"", false, ScopeAccessible},
		{"", true, ScopeAccessible},
		{"all", true, ScopeAccessible},
		{"owner", false, ScopeOwner},
		{"owner", true, ScopeOwner},
		{"admin", true, ScopeAdminOthers},
		{"admin-all", true, ScopeAdminAll},
		{"admin", false, ScopeNone},
		{"admin-all", false, ScopeNone},
		{"Admin", true, ScopeAccessible},
		{"admin ", true, ScopeAccessible},
		{"OWNER", false, ScopeAccessible},
	}
	for _, tt := range tests {
		if got := ParseListScope(tt.typ, tt.admin); got != tt.want {
			t.Errorf("ParseListScope(%q, admin %v) = %d, want %d", tt.typ, tt.admin, got, tt.want)
		}
	}
}

func eggVars() []EggVariable {
	return []EggVariable{
		{EnvName: "SERVER_JARFILE", Name: "Server Jar File", Rules: "required|string|max:20", UserViewable: true, UserEditable: true},
		{EnvName: "MOTD", Name: "Message", Rules: "nullable|string|max:10", UserViewable: true, UserEditable: true},
		{EnvName: "BUILD", Name: "Build", Rules: "required|string", UserViewable: true, UserEditable: false},
		{EnvName: "RCON", Name: "Rcon Password", Rules: "required|string", UserViewable: false, UserEditable: true},
	}
}

func TestValidateEnvironmentAsAdmin(t *testing.T) {
	values := map[string]*string{
		"SERVER_JARFILE": str("  paper.jar "), "MOTD": nil, "BUILD": str("123"), "RCON": str("s3cret"), "UNKNOWN": str("x"),
	}

	got, err := ValidateEnvironment(eggVars(), values, true)
	if err != nil {
		t.Fatalf("ValidateEnvironment: %v", err)
	}

	want := []VariableValue{{"SERVER_JARFILE", "paper.jar"}, {"MOTD", ""}, {"BUILD", "123"}, {"RCON", "s3cret"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
}

func TestValidateEnvironmentAsAdminNeedsEveryRequiredVariable(t *testing.T) {
	_, err := ValidateEnvironment(eggVars(), map[string]*string{"SERVER_JARFILE": str("a.jar")}, true)

	var ee *EnvironmentError
	if !errors.As(err, &ee) {
		t.Fatalf("error = %v, want an *EnvironmentError", err)
	}
	var fields []string
	for _, p := range ee.Problems {
		fields = append(fields, p.Field)
	}
	if !reflect.DeepEqual(fields, []string{"environment.BUILD", "environment.RCON"}) {
		t.Errorf("problems on %v, want BUILD and RCON, in the order of the variables", fields)
	}
}

func TestValidateEnvironmentMessagesNameTheVariable(t *testing.T) {
	_, err := ValidateEnvironment(eggVars(), map[string]*string{"SERVER_JARFILE": str(strings.Repeat("a", 21)), "BUILD": str("1"), "RCON": str("x")}, true)

	var ee *EnvironmentError
	if !errors.As(err, &ee) || len(ee.Problems) != 1 {
		t.Fatalf("error = %v, want one problem", err)
	}
	msg := strings.Join(ee.Problems[0].Messages, " ")
	if !strings.Contains(msg, "Server Jar File variable") || !strings.Contains(msg, "20 characters") {
		t.Errorf("messages = %q, want them to name the variable and its limit", msg)
	}
	if ee.Error() == "" {
		t.Error("the error has no text")
	}
}

func TestValidateEnvironmentCollectsEveryProblem(t *testing.T) {
	_, err := ValidateEnvironment(eggVars(), map[string]*string{"SERVER_JARFILE": str(strings.Repeat("a", 21)), "MOTD": str(strings.Repeat("b", 11))}, true)

	var ee *EnvironmentError
	if !errors.As(err, &ee) || len(ee.Problems) != 4 {
		t.Errorf("error = %v, want a problem for each of the four variables", err)
	}
}

func TestValidateEnvironmentAsUser(t *testing.T) {
	values := map[string]*string{"SERVER_JARFILE": str("a.jar"), "MOTD": str("hi"), "BUILD": str("hacked"), "RCON": str("hacked")}

	got, err := ValidateEnvironment(eggVars(), values, false)
	if err != nil {
		t.Fatalf("ValidateEnvironment: %v", err)
	}

	want := []VariableValue{{"SERVER_JARFILE", "a.jar"}, {"MOTD", "hi"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want only the variables the user may see and change: %v", got, want)
	}
}

func TestValidateEnvironmentAsUserDoesNotAskForWhatItMayNotSet(t *testing.T) {
	got, err := ValidateEnvironment(eggVars(), map[string]*string{"SERVER_JARFILE": str("a.jar")}, false)

	if err != nil || len(got) != 2 {
		t.Errorf("got %v, %v; BUILD and RCON are required but not the user's to give", got, err)
	}
}

func TestValidateEnvironmentWithRulesThatCannotBeApplied(t *testing.T) {
	vars := []EggVariable{{EnvName: "X", Name: "X", Rules: "required|no_such_rule", UserViewable: true, UserEditable: true}}

	_, err := ValidateEnvironment(vars, map[string]*string{"X": str("1")}, true)

	var ee *EnvironmentError
	if err == nil || errors.As(err, &ee) || !strings.Contains(err.Error(), "X") {
		t.Errorf("error = %v, want a plain error that names the variable", err)
	}
}

func TestValidateEnvironmentWithNothing(t *testing.T) {
	got, err := ValidateEnvironment(nil, nil, true)
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v", got, err)
	}
}

func current() Details {
	return Details{ExternalID: str("ext-1"), OwnerID: 5, Name: "Survival", Description: "old words"}
}

func TestPlanDetails(t *testing.T) {
	tests := []struct {
		name      string
		ch        DetailsChange
		want      Details
		wantOwner bool
	}{
		{"nothing but the required", DetailsChange{OwnerID: 5, Name: "Survival"}, current(), false},
		{"a new name", DetailsChange{OwnerID: 5, Name: "Creative"}, Details{str("ext-1"), 5, "Creative", "old words"}, false},
		{"a new owner", DetailsChange{OwnerID: 8, Name: "Survival"}, Details{str("ext-1"), 8, "Survival", "old words"}, true},
		{"external id replaced", DetailsChange{OwnerID: 5, Name: "Survival", ExternalID: str("ext-2")}, Details{str("ext-2"), 5, "Survival", "old words"}, false},
		{"external id cleared", DetailsChange{OwnerID: 5, Name: "Survival", ExternalID: str("")}, Details{nil, 5, "Survival", "old words"}, false},
		{"description replaced", DetailsChange{OwnerID: 5, Name: "Survival", Description: str("new")}, Details{str("ext-1"), 5, "Survival", "new"}, false},
		{"description cleared", DetailsChange{OwnerID: 5, Name: "Survival", Description: str("")}, Details{str("ext-1"), 5, "Survival", ""}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PlanDetails(current(), tt.ch)
			if err != nil {
				t.Fatalf("PlanDetails: %v", err)
			}

			if !reflect.DeepEqual(got.Next, tt.want) {
				t.Errorf("Next = %+v, want %+v", got.Next, tt.want)
			}
			if got.OwnerChanged != tt.wantOwner || got.PreviousOwner != 5 {
				t.Errorf("OwnerChanged = %v, PreviousOwner = %d; want %v and 5", got.OwnerChanged, got.PreviousOwner, tt.wantOwner)
			}
		})
	}
}

func TestPlanDetailsRefusals(t *testing.T) {
	tests := []struct {
		name  string
		ch    DetailsChange
		field string
	}{
		{"no name", DetailsChange{OwnerID: 5}, "name"},
		{"name too long", DetailsChange{OwnerID: 5, Name: strings.Repeat("a", 192)}, "name"},
		{"no owner", DetailsChange{Name: "Survival"}, "user"},
		{"external id with a control character", DetailsChange{OwnerID: 5, Name: "S", ExternalID: str("a\nb")}, "external_id"},
		{"external id too long", DetailsChange{OwnerID: 5, Name: "S", ExternalID: str(strings.Repeat("x", 192))}, "external_id"},
		{"description too long", DetailsChange{OwnerID: 5, Name: "S", Description: str(strings.Repeat("a", 2001))}, "description"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PlanDetails(current(), tt.ch)

			if fe, ok := fielderr.Of(err); !ok || fe.Field != tt.field {
				t.Fatalf("error = %v, want one about %q", err, tt.field)
			}
			if !reflect.DeepEqual(got, DetailsPlan{}) {
				t.Errorf("a refused change must plan nothing: %+v", got)
			}
		})
	}
}

func startup() StartupSettings {
	return StartupSettings{Startup: "java -jar {{SERVER_JARFILE}}", EggID: 3, Image: "ghcr.io/pterodactyl/yolks:java_17", SkipScripts: true}
}

func TestPlanStartup(t *testing.T) {
	ch := StartupChange{
		Startup: "java -Xmx{{SERVER_MEMORY}}M -jar {{SERVER_JARFILE}}", EggID: 4, Image: "registry.example.com/custom:1", SkipScripts: false,
		Environment: map[string]*string{"SERVER_JARFILE": str("paper.jar"), "BUILD": str("9"), "RCON": str("x")},
	}

	got, err := PlanStartup(startup(), ch, eggVars())
	if err != nil {
		t.Fatalf("PlanStartup: %v", err)
	}

	want := StartupSettings{Startup: ch.Startup, EggID: 4, Image: ch.Image, SkipScripts: false}
	if got.Next != want || !got.EggChanged {
		t.Errorf("got %+v, want %+v with the egg changed", got, want)
	}
	if len(got.Values) != 4 || got.Values[0] != (VariableValue{"SERVER_JARFILE", "paper.jar"}) {
		t.Errorf("Values = %v", got.Values)
	}
}

func TestPlanStartupSameEgg(t *testing.T) {
	cur := startup()
	got, err := PlanStartup(cur, StartupChange{Startup: cur.Startup, EggID: cur.EggID, Image: cur.Image, SkipScripts: true}, eggVars())

	if err != nil || got.EggChanged || got.Next != cur {
		t.Errorf("got %+v, %v, want no change", got, err)
	}
}

func TestPlanStartupSkipScriptsIsTakenAsSent(t *testing.T) {
	cur := startup()
	cur.SkipScripts = true

	got, _ := PlanStartup(cur, StartupChange{Startup: cur.Startup, EggID: cur.EggID, Image: cur.Image, SkipScripts: false}, eggVars())

	if got.Next.SkipScripts {
		t.Error("skip_scripts is part of the request and must be taken as sent")
	}
}

func TestPlanStartupChecksTheEnvironmentOnlyWhenOneWasSent(t *testing.T) {
	cur := startup()
	ch := StartupChange{Startup: cur.Startup, EggID: cur.EggID, Image: cur.Image}

	got, err := PlanStartup(cur, ch, eggVars())
	if err != nil || len(got.Values) != 0 {
		t.Errorf("an absent environment: got %v, %v", got.Values, err)
	}

	ch.Environment = map[string]*string{}
	got, err = PlanStartup(cur, ch, eggVars())
	if err != nil || len(got.Values) != 0 {
		t.Errorf("an empty environment: got %v, %v; the required variables must not be asked for", got.Values, err)
	}

	ch.Environment = map[string]*string{"MOTD": str("hi")}
	if _, err = PlanStartup(cur, ch, eggVars()); err == nil {
		t.Error("an environment with one variable must be checked as a whole, so the missing required ones fail")
	}
}

func TestPlanStartupRefusals(t *testing.T) {
	cur := startup()
	tests := []struct {
		name   string
		mutate func(*StartupChange)
		field  string
	}{
		{"no startup", func(ch *StartupChange) { ch.Startup = "  " }, "startup"},
		{"startup too long", func(ch *StartupChange) { ch.Startup = strings.Repeat("a", MaxStartupLength+1) }, "startup"},
		{"no egg", func(ch *StartupChange) { ch.EggID = 0 }, "egg"},
		{"no image", func(ch *StartupChange) { ch.Image = "" }, "docker_image"},
		{"image with a space", func(ch *StartupChange) { ch.Image = "a b" }, "docker_image"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := StartupChange{Startup: cur.Startup, EggID: cur.EggID, Image: cur.Image}
			tt.mutate(&ch)

			got, err := PlanStartup(cur, ch, eggVars())

			if fe, ok := fielderr.Of(err); !ok || fe.Field != tt.field {
				t.Fatalf("error = %v, want one about %q", err, tt.field)
			}
			if !reflect.DeepEqual(got, StartupPlan{}) {
				t.Errorf("a refused change must plan nothing: %+v", got)
			}
		})
	}
}

func TestPlanStartupReturnsTheEnvironmentError(t *testing.T) {
	cur := startup()
	ch := StartupChange{Startup: cur.Startup, EggID: cur.EggID, Image: cur.Image, Environment: map[string]*string{"SERVER_JARFILE": nil}}

	_, err := PlanStartup(cur, ch, eggVars())

	var ee *EnvironmentError
	if !errors.As(err, &ee) {
		t.Errorf("error = %v, want an *EnvironmentError", err)
	}
}

func TestPlanSuspension(t *testing.T) {
	tests := []struct {
		name         string
		action       SuspendAction
		status       string
		transferring bool
		want         SuspensionPlan
		wantErr      error
	}{
		{"suspend a normal server", Suspend, "", false, SuspensionPlan{Status: StatusSuspended}, nil},
		{"release a suspended server", Unsuspend, StatusSuspended, false, SuspensionPlan{Status: ""}, nil},
		{"suspend a suspended server", Suspend, StatusSuspended, false, SuspensionPlan{Noop: true, Status: StatusSuspended}, nil},
		{"release a normal server", Unsuspend, "", false, SuspensionPlan{Noop: true, Status: ""}, nil},
		{"a suspended server in transfer is already as asked", Suspend, StatusSuspended, true, SuspensionPlan{Noop: true, Status: StatusSuspended}, nil},
		{"suspend while transferring", Suspend, "", true, SuspensionPlan{}, ErrSuspendTransfer},
		{"release while transferring", Unsuspend, StatusSuspended, true, SuspensionPlan{}, ErrSuspendTransfer},
		{"suspend while installing", Suspend, StatusInstalling, false, SuspensionPlan{}, ErrSuspendBusy},
		{"suspend after a failed installation", Suspend, StatusInstallFailed, false, SuspensionPlan{}, ErrSuspendBusy},
		{"suspend after a failed reinstall", Suspend, StatusReinstallFailed, false, SuspensionPlan{}, ErrSuspendBusy},
		{"suspend while restoring", Suspend, StatusRestoringBackup, false, SuspensionPlan{}, ErrSuspendBusy},
		{"release an installing server is nothing to release", Unsuspend, StatusInstalling, false, SuspensionPlan{Noop: true, Status: StatusInstalling}, nil},
		{"transfer is checked before the busy state", Suspend, StatusInstalling, true, SuspensionPlan{}, ErrSuspendTransfer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PlanSuspension(tt.action, tt.status, tt.transferring)

			if !errors.Is(err, tt.wantErr) || got != tt.want {
				t.Errorf("got %+v, %v; want %+v, %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestParseDeleteForce(t *testing.T) {
	tests := []struct {
		segment   string
		wantForce bool
		wantOK    bool
	}{
		{"", false, true},
		{"force", true, true},
		{"Force", false, false},
		{"forced", false, false},
		{"true", false, false},
		{"1", false, false},
		{"force ", false, false},
	}
	for _, tt := range tests {
		force, ok := ParseDeleteForce(tt.segment)
		if force != tt.wantForce || ok != tt.wantOK {
			t.Errorf("ParseDeleteForce(%q) = %v, %v; want %v, %v", tt.segment, force, ok, tt.wantForce, tt.wantOK)
		}
	}
}

func TestDeleteMayContinue(t *testing.T) {
	tests := []struct {
		name   string
		force  bool
		status int
		want   bool
	}{
		{"the node does not know the server", false, http.StatusNotFound, true},
		{"the node failed", false, http.StatusInternalServerError, false},
		{"the node refused", false, http.StatusForbidden, false},
		{"the node could not be reached", false, 0, false},
		{"forced after a failure", true, http.StatusInternalServerError, true},
		{"forced after no answer", true, 0, true},
		{"forced and not found", true, http.StatusNotFound, true},
	}
	for _, tt := range tests {
		if got := DeleteMayContinue(tt.force, tt.status); got != tt.want {
			t.Errorf("%s: DeleteMayContinue(%v, %d) = %v, want %v", tt.name, tt.force, tt.status, got, tt.want)
		}
	}
}

func TestLifecycleErrorsKeepTheReferencesText(t *testing.T) {
	if !strings.Contains(ErrSuspendTransfer.Error(), "currently being transferred") {
		t.Errorf("ErrSuspendTransfer = %q", ErrSuspendTransfer)
	}
}
