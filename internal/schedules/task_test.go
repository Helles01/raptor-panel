package schedules

import (
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/permissions"
)

func TestTaskValidate(t *testing.T) {
	ok := func(action, payload string) Task { return Task{Action: action, Payload: payload, Sequence: 1} }
	tests := []struct {
		name string
		task Task
		ok   bool
	}{
		{"command", ok("command", "say hello"), true},
		{"power start", ok("power", "start"), true},
		{"power stop", ok("power", "stop"), true},
		{"power restart", ok("power", "restart"), true},
		{"power kill", ok("power", "kill"), true},
		{"backup without files", ok("backup", ""), true},
		{"backup with files", ok("backup", "*.log\ncache/"), true},
		{"offset at the limit", Task{Action: "power", Payload: "stop", TimeOffset: 900, Sequence: 1}, true},

		{"unknown action", ok("delete", "x"), false},
		{"empty action", ok("", "x"), false},
		{"upper-case action", ok("COMMAND", "x"), false},
		{"command needs a command", ok("command", ""), false},
		{"command of spaces", ok("command", "   "), false},
		{"command with NUL", ok("command", "say a\x00b"), false},
		{"command too long", ok("command", strings.Repeat("a", 4097)), false},
		{"command at the limit", ok("command", strings.Repeat("a", 4096)), true},
		{"command with invalid UTF-8", ok("command", "say \xff"), false},
		{"power needs an action", ok("power", ""), false},
		{"power with another action", ok("power", "reboot"), false},
		{"power with spaces", ok("power", "start "), false},
		{"power upper-case", ok("power", "START"), false},
		{"backup list with NUL", ok("backup", "a\x00"), false},
		{"backup list too long", ok("backup", strings.Repeat("a", 8193)), false},
		{"negative offset", Task{Action: "power", Payload: "stop", TimeOffset: -1, Sequence: 1}, false},
		{"offset over the limit", Task{Action: "power", Payload: "stop", TimeOffset: 901, Sequence: 1}, false},
		{"sequence zero", Task{Action: "power", Payload: "stop", Sequence: 0}, false},
		{"negative sequence", Task{Action: "power", Payload: "stop", Sequence: -3}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.task.Validate(); (err == nil) != tt.ok {
				t.Errorf("Validate() = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestTaskPermission(t *testing.T) {
	tests := []struct {
		task Task
		want string
		ok   bool
	}{
		{Task{Action: "command", Payload: "say hi"}, permissions.ControlConsole, true},
		{Task{Action: "backup"}, permissions.BackupCreate, true},
		{Task{Action: "power", Payload: "start"}, permissions.ControlStart, true},
		{Task{Action: "power", Payload: "stop"}, permissions.ControlStop, true},
		{Task{Action: "power", Payload: "kill"}, permissions.ControlStop, true},
		{Task{Action: "power", Payload: "restart"}, permissions.ControlRestart, true},

		// No permission means no run, where the reference lets such a task through.
		{Task{Action: "power", Payload: "reboot"}, "", false},
		{Task{Action: "power", Payload: " start"}, "", false},
		{Task{Action: "power"}, "", false},
		{Task{Action: "transfer"}, "", false},
		{Task{}, "", false},
	}
	for _, tt := range tests {
		got, err := tt.task.Permission()
		if got != tt.want || (err == nil) != tt.ok {
			t.Errorf("Permission(%+v) = %q, %v; want %q, ok=%v", tt.task, got, err, tt.want, tt.ok)
		}
	}
}

func TestCanRun(t *testing.T) {
	restart := Task{Action: "power", Payload: "restart"}
	command := Task{Action: "command", Payload: "save-all"}
	backup := Task{Action: "backup"}

	tests := []struct {
		name  string
		held  []string
		tasks []Task
		want  bool
	}{
		{"owner runs anything", []string{"*"}, []Task{restart, command, backup}, true},
		{"a schedule without tasks needs only the update permission", []string{"schedule.update"}, nil, true},
		{"every task's permission is held", []string{"schedule.update", "control.restart", "control.console"}, []Task{restart, command}, true},
		{"one task's permission is missing", []string{"schedule.update", "control.restart"}, []Task{restart, command}, false},
		{"the update permission alone is not enough", []string{"schedule.update"}, []Task{restart}, false},
		{"the task permissions alone are not enough", []string{"control.restart"}, []Task{restart}, false},
		{"stop does not cover restart", []string{"schedule.update", "control.stop"}, []Task{restart}, false},
		{"kill is covered by stop", []string{"schedule.update", "control.stop"}, []Task{{Action: "power", Payload: "kill"}}, true},
		{"a task nobody can run blocks the schedule", []string{"*"}, []Task{restart, {Action: "power", Payload: "reboot"}}, false},
		{"nothing held", nil, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanRun(tt.held, tt.tasks); got != tt.want {
				t.Errorf("CanRun = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{"plain", "Nightly restart", true},
		{"one character", "a", true},
		{"at the limit", strings.Repeat("a", 191), true},
		{"multi-byte characters count once", strings.Repeat("é", 191), true},
		{"empty", "", false},
		{"spaces only", "   ", false},
		{"too long", strings.Repeat("a", 192), false},
		{"control character", "a\x01b", false},
		{"newline", "a\nb", false},
		{"NUL", "a\x00", false},
		{"invalid UTF-8", "a\xff", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateName(tt.in); (err == nil) != tt.ok {
				t.Errorf("ValidateName(%q) = %v, want ok=%v", tt.in, err, tt.ok)
			}
		})
	}
}

func TestPlaceTask(t *testing.T) {
	ptr := func(n int) *int { return &n }
	tests := []struct {
		name      string
		existing  []int
		requested *int
		wantSeq   int
		wantShift bool
	}{
		{"the first task", nil, nil, 1, false},
		{"last by default", []int{1, 2, 3}, nil, 4, false},
		{"last when asked for the next place", []int{1, 2, 3}, ptr(4), 4, false},
		{"beyond the end goes last", []int{1, 2, 3}, ptr(99), 4, false},
		{"in the middle, shifting the rest", []int{1, 2, 3}, ptr(2), 2, true},
		{"at the front", []int{1, 2, 3}, ptr(1), 1, true},
		{"zero is the front", []int{1, 2, 3}, ptr(0), 1, true},
		{"negative is the front", []int{1, 2, 3}, ptr(-5), 1, true},
		{"zero with no tasks", nil, ptr(0), 1, false},
		{"gaps: the next one follows the highest", []int{1, 5}, nil, 6, false},
		{"gaps: a place in the gap shifts the later ones", []int{1, 5}, ptr(3), 3, true},
		{"unordered input", []int{3, 1, 2}, nil, 4, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seq, shift := PlaceTask(tt.existing, tt.requested)
			if seq != tt.wantSeq || shift != tt.wantShift {
				t.Errorf("PlaceTask = %d, %v; want %d, %v", seq, shift, tt.wantSeq, tt.wantShift)
			}
		})
	}
}
