package servers

// InstallReport is what a node tells the panel when an installation or reinstallation ends.
type InstallReport struct {
	Successful bool
	Reinstall  bool
}

// NotifyConfig says which finished installations the owner is told about by e-mail.
type NotifyConfig struct {
	Install   bool
	Reinstall bool
}

// Notification is the e-mail to send to the owner after an installation report.
type Notification int

const (
	NotifyNone Notification = iota
	NotifyInstalled
	NotifyReinstalled
)

// InstallOutcome is what to store after an installation report.
type InstallOutcome struct {
	// Status is the new status of the server, empty for the normal state.
	Status string
	// SetInstalledAt is always true: the time of the report is stored whether the installation worked or not (as in the
	// reference), and "first installation" means "no report yet".
	SetInstalledAt bool
	Notify         Notification
}

// ApplyInstallReport decides what an installation report from a node does to a server.
//
// Only a server that is waiting for the result takes one, that is one that is installing (initial or reinstall) or suspended
// meanwhile. The reference takes a report whatever the server is doing, so a node could set a server that is restoring a
// backup back to normal, or declare one installed that never was (C-033).
//
// A suspended server stays suspended. The e-mail goes out only when the installation worked; the reference sends it after a
// failure as well.
func ApplyInstallReport(status string, installedBefore bool, r InstallReport, notify NotifyConfig) (InstallOutcome, error) {
	if status != StatusInstalling && status != StatusSuspended {
		return InstallOutcome{}, &StateError{"This server is not waiting for the result of an installation."}
	}

	out := InstallOutcome{SetInstalledAt: true}
	switch {
	case status == StatusSuspended:
		out.Status = StatusSuspended
	case r.Successful:
		out.Status = ""
	case r.Reinstall:
		out.Status = StatusReinstallFailed
	default:
		out.Status = StatusInstallFailed
	}

	switch {
	case !r.Successful:
	case !installedBefore && notify.Install:
		out.Notify = NotifyInstalled
	case installedBefore && notify.Reinstall:
		out.Notify = NotifyReinstalled
	}
	return out, nil
}
