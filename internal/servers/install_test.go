package servers

import (
	"errors"
	"testing"
)

func TestApplyInstallReport(t *testing.T) {
	both := NotifyConfig{Install: true, Reinstall: true}
	tests := []struct {
		name      string
		status    string
		installed bool
		report    InstallReport
		notify    NotifyConfig
		want      InstallOutcome
	}{
		{"first installation worked", StatusInstalling, false, InstallReport{Successful: true}, both, InstallOutcome{Status: "", SetInstalledAt: true, Notify: NotifyInstalled}},
		{"reinstallation worked", StatusInstalling, true, InstallReport{Successful: true, Reinstall: true}, both, InstallOutcome{Status: "", SetInstalledAt: true, Notify: NotifyReinstalled}},
		{"first installation failed", StatusInstalling, false, InstallReport{}, both, InstallOutcome{Status: StatusInstallFailed, SetInstalledAt: true}},
		{"reinstallation failed", StatusInstalling, true, InstallReport{Reinstall: true}, both, InstallOutcome{Status: StatusReinstallFailed, SetInstalledAt: true}},
		{"a failed reinstall flag on a first installation", StatusInstalling, false, InstallReport{Reinstall: true}, both, InstallOutcome{Status: StatusReinstallFailed, SetInstalledAt: true}},

		{"suspended meanwhile, worked", StatusSuspended, false, InstallReport{Successful: true}, both, InstallOutcome{Status: StatusSuspended, SetInstalledAt: true, Notify: NotifyInstalled}},
		{"suspended meanwhile, failed", StatusSuspended, false, InstallReport{}, both, InstallOutcome{Status: StatusSuspended, SetInstalledAt: true}},
		{"suspended, a reinstall that failed", StatusSuspended, true, InstallReport{Reinstall: true}, both, InstallOutcome{Status: StatusSuspended, SetInstalledAt: true}},

		{"first-installation e-mail switched off", StatusInstalling, false, InstallReport{Successful: true}, NotifyConfig{Reinstall: true}, InstallOutcome{SetInstalledAt: true}},
		{"reinstallation e-mail switched off", StatusInstalling, true, InstallReport{Successful: true}, NotifyConfig{Install: true}, InstallOutcome{SetInstalledAt: true}},
		{"both e-mails switched off", StatusInstalling, false, InstallReport{Successful: true}, NotifyConfig{}, InstallOutcome{SetInstalledAt: true}},
		{"the reinstall switch does not cover a first installation", StatusInstalling, false, InstallReport{Successful: true}, NotifyConfig{Reinstall: true}, InstallOutcome{SetInstalledAt: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ApplyInstallReport(tt.status, tt.installed, tt.report, tt.notify)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("outcome = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestApplyInstallReportRefusesAServerThatIsNotWaiting(t *testing.T) {
	for _, status := range []string{"", StatusInstallFailed, StatusReinstallFailed, StatusRestoringBackup, "something else"} {
		for _, report := range []InstallReport{{Successful: true}, {}, {Reinstall: true}} {
			_, err := ApplyInstallReport(status, true, report, NotifyConfig{Install: true, Reinstall: true})
			var se *StateError
			if !errors.As(err, &se) {
				t.Errorf("status %q, report %+v: error = %v, want a state error", status, report, err)
			}
		}
	}
}
