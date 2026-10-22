package schedules

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Helles01/raptor-panel/internal/permissions"
)

// The actions a task can have.
const (
	ActionCommand = "command"
	ActionPower   = "power"
	ActionBackup  = "backup"
)

const (
	// MaxTimeOffset is the longest a task may wait after the one before it, in seconds.
	MaxTimeOffset = 900
	// DefaultTaskLimit is how many tasks a schedule may have unless the panel is configured otherwise.
	DefaultTaskLimit = 10
	// MaxNameLength is the longest a schedule name may be.
	MaxNameLength = 191

	maxCommandLength = 4096 // what the node accepts for one console command
	maxBackupPayload = 8192
)

var powerActions = map[string]bool{"start": true, "stop": true, "restart": true, "kill": true}

// Task is one step of a schedule.
type Task struct {
	Action string
	// Payload is the console command, the power action, or for a backup the files to leave out, one per line.
	Payload string
	// TimeOffset is the wait in seconds before this task runs.
	TimeOffset int
	// Sequence is the position in the schedule, from 1.
	Sequence int
}

// Validate checks a task against allow-lists. It does not know the schedule, so the limit of tasks and the sequence are
// not checked here.
func (t Task) Validate() error {
	switch t.Action {
	case ActionCommand:
		if strings.TrimSpace(t.Payload) == "" {
			return errors.New("a command task needs a command")
		}
		if len(t.Payload) > maxCommandLength || strings.ContainsRune(t.Payload, 0) || !utf8.ValidString(t.Payload) {
			return fmt.Errorf("the command must be valid text of at most %d bytes without NUL characters", maxCommandLength)
		}
	case ActionPower:
		if !powerActions[t.Payload] {
			return errors.New("the power action must be start, stop, restart or kill")
		}
	case ActionBackup:
		if len(t.Payload) > maxBackupPayload || strings.ContainsRune(t.Payload, 0) || !utf8.ValidString(t.Payload) {
			return fmt.Errorf("the list of ignored files must be valid text of at most %d bytes without NUL characters", maxBackupPayload)
		}
	default:
		return errors.New("the action must be command, power or backup")
	}

	if t.TimeOffset < 0 || t.TimeOffset > MaxTimeOffset {
		return fmt.Errorf("the time offset must be between 0 and %d seconds", MaxTimeOffset)
	}
	if t.Sequence < 1 {
		return errors.New("the sequence must be 1 or more")
	}
	return nil
}

// Permission is the server permission a user needs to have this task run on their behalf: running a command needs the
// console, a backup needs backup creation, and a power action needs the permission of that signal.
//
// An action or power signal that is not known has no permission and gives an error, so nobody can run it. The reference
// treats such a task as needing no permission.
func (t Task) Permission() (string, error) {
	switch t.Action {
	case ActionCommand:
		return permissions.ControlConsole, nil
	case ActionBackup:
		return permissions.BackupCreate, nil
	case ActionPower:
		switch t.Payload {
		case "start":
			return permissions.ControlStart, nil
		case "stop", "kill":
			return permissions.ControlStop, nil
		case "restart":
			return permissions.ControlRestart, nil
		}
	}
	return "", fmt.Errorf("the task %q %q has no permission", t.Action, t.Payload)
}

// CanRun reports whether someone holding the given permissions may have all of these tasks run. Starting a schedule by
// hand runs its tasks as that person, so each task's permission must be held on top of the right to update schedules;
// otherwise a user who may only update schedules could run a power action or command they have no permission for.
func CanRun(held []string, tasks []Task) bool {
	if !permissions.Has(held, permissions.ScheduleUpdate) {
		return false
	}
	for _, t := range tasks {
		needed, err := t.Permission()
		if err != nil || !permissions.Has(held, needed) {
			return false
		}
	}
	return true
}

// ValidateName checks the name of a schedule.
func ValidateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("a schedule needs a name")
	}
	if utf8.RuneCountInString(name) > MaxNameLength {
		return fmt.Errorf("the name must be at most %d characters", MaxNameLength)
	}
	if !utf8.ValidString(name) || strings.ContainsFunc(name, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return errors.New("the name must not contain control characters")
	}
	return nil
}

// PlaceTask decides where a new task goes in a schedule whose tasks have the given sequence numbers. A request without a
// position (nil) or beyond the end goes last. A request for an earlier position takes it, and shift is true: the tasks
// from that position on must be moved one place later, in the same transaction that adds the task.
func PlaceTask(existing []int, requested *int) (sequence int, shift bool) {
	next := 1
	for _, s := range existing {
		if s >= next {
			next = s + 1
		}
	}

	want := next
	if requested != nil {
		want = *requested
	}
	if want < 1 {
		want = 1
	}
	if want < next {
		return want, true
	}
	return next, false
}
