package servers

// State is what decides whether a server can be used at the moment.
type State struct {
	// Status is empty in the normal state, otherwise installing, install_failed, reinstall_failed, suspended or
	// restoring_backup.
	Status          string
	NodeMaintenance bool
	Transferring    bool
}

// StateError is returned for a server that cannot be used now; the API answers it with 409.
type StateError struct{ Message string }

func (e *StateError) Error() string { return e.Message }

// Installed reports whether a server finished installing. A failed reinstall still counts as installed.
func Installed(status string) bool { return status != "installing" && status != "install_failed" }

// Validate refuses a server that is suspended, on a node under maintenance, not installed, restoring a backup or being
// transferred, with the reference's message for the first reason that applies.
func (s State) Validate() error {
	switch {
	case s.Status == statusSuspended:
		return &StateError{"This server is currently suspended and the functionality requested is unavailable."}
	case s.NodeMaintenance:
		return &StateError{"The node of this server is currently under maintenance and the functionality requested is unavailable."}
	case !Installed(s.Status):
		return &StateError{"This server has not yet completed its installation process, please try again later."}
	case s.Status == "restoring_backup":
		return &StateError{"This server is currently restoring from a backup, please try again later."}
	case s.Transferring:
		return &StateError{"This server is currently being transferred to a new machine, please try again later."}
	}
	return nil
}
