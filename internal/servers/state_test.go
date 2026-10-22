package servers

import (
	"errors"
	"testing"
)

func TestStateValidate(t *testing.T) {
	tests := []struct {
		name string
		s    State
		want string // the message, empty for no error
	}{
		{"normal", State{}, ""},
		{"a failed reinstall is still usable", State{Status: "reinstall_failed"}, ""},
		{"suspended", State{Status: "suspended"}, "This server is currently suspended and the functionality requested is unavailable."},
		{"node under maintenance", State{NodeMaintenance: true}, "The node of this server is currently under maintenance and the functionality requested is unavailable."},
		{"installing", State{Status: "installing"}, "This server has not yet completed its installation process, please try again later."},
		{"install failed", State{Status: "install_failed"}, "This server has not yet completed its installation process, please try again later."},
		{"restoring a backup", State{Status: "restoring_backup"}, "This server is currently restoring from a backup, please try again later."},
		{"being transferred", State{Transferring: true}, "This server is currently being transferred to a new machine, please try again later."},

		{"suspended wins over maintenance", State{Status: "suspended", NodeMaintenance: true}, "This server is currently suspended and the functionality requested is unavailable."},
		{"maintenance wins over installing", State{Status: "installing", NodeMaintenance: true}, "The node of this server is currently under maintenance and the functionality requested is unavailable."},
		{"installing wins over a transfer", State{Status: "installing", Transferring: true}, "This server has not yet completed its installation process, please try again later."},
		{"restoring wins over a transfer", State{Status: "restoring_backup", Transferring: true}, "This server is currently restoring from a backup, please try again later."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.s.Validate()
			if tt.want == "" {
				if err != nil {
					t.Fatalf("error = %v", err)
				}
				return
			}
			var se *StateError
			if !errors.As(err, &se) || se.Message != tt.want {
				t.Errorf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestInstalled(t *testing.T) {
	for status, want := range map[string]bool{"": true, "suspended": true, "restoring_backup": true, "reinstall_failed": true, "installing": false, "install_failed": false} {
		if Installed(status) != want {
			t.Errorf("Installed(%q) = %v", status, !want)
		}
	}
}
