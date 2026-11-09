package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/audit"
)

type fakeAuditSource struct {
	in  audit.Input
	err error
}

func (f fakeAuditSource) Load(context.Context) (audit.Input, error) { return f.in, f.err }

var auditNow = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

// Only warnings is a report with exit 0, an error is a report with a failure, so that a script can stop on it; both write the report to the standard output and the
// count to the standard error.
func TestRunAuditExitFollowsTheErrors(t *testing.T) {
	warnings := audit.Input{Subusers: []audit.Subuser{{ID: 1, ServerID: 2, Permissions: []string{"nope"}}}}
	errs := audit.Input{Users: []audit.User{{ID: 3, Username: "nohash", Email: "n@example.com", PasswordHash: "x"}}}

	var out, errOut bytes.Buffer
	if err := runAudit(context.Background(), &out, &errOut, fakeAuditSource{in: warnings}, auditNow, false); err != nil {
		t.Fatalf("warnings only: %v", err)
	}
	if !strings.Contains(out.String(), "subusers: 1 checked, 0 errors, 1 warnings") || !strings.Contains(errOut.String(), "0 error(s), 1 warning(s).") {
		t.Errorf("stdout %q, stderr %q", out.String(), errOut.String())
	}

	out.Reset()
	errOut.Reset()
	err := runAudit(context.Background(), &out, &errOut, fakeAuditSource{in: errs}, auditNow, false)
	if err == nil || !strings.Contains(err.Error(), "1 error(s) found") || !strings.Contains(out.String(), "error   users#3 (nohash)") || !strings.Contains(errOut.String(), "1 error(s), 0 warning(s).") {
		t.Errorf("an error: err = %v, stdout %q, stderr %q", err, out.String(), errOut.String())
	}

	// Nothing at all is a clean report.
	out.Reset()
	errOut.Reset()
	if err := runAudit(context.Background(), &out, &errOut, fakeAuditSource{}, auditNow, false); err != nil || !strings.Contains(errOut.String(), "0 error(s), 0 warning(s).") {
		t.Errorf("an empty input: %v, %q", err, errOut.String())
	}
}

func TestRunAuditJSON(t *testing.T) {
	var out, errOut bytes.Buffer
	in := audit.Input{Users: []audit.User{{ID: 3, Username: "nohash", Email: "n@example.com", PasswordHash: "x"}}}
	if err := runAudit(context.Background(), &out, &errOut, fakeAuditSource{in: in}, auditNow, true); err == nil {
		t.Fatal("an error in the data should fail the command")
	}
	var report audit.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || len(report.Sections) != 12 || report.Count(audit.Error) != 1 {
		t.Fatalf("not a report: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "checked,") {
		t.Errorf("the text form was written as well: %s", out.String())
	}
}

func TestRunAuditSaysWhenTheDataCannotBeRead(t *testing.T) {
	var out, errOut bytes.Buffer
	err := runAudit(context.Background(), &out, &errOut, fakeAuditSource{err: errors.New("the database is down")}, auditNow, false)
	if err == nil || !strings.Contains(err.Error(), "read the data to check: the database is down") || out.Len() != 0 {
		t.Errorf("err = %v, stdout %q", err, out.String())
	}
}

func TestAuditIsACommandAndTakesNoArguments(t *testing.T) {
	if rootCommands()["audit"] == nil {
		t.Fatal("raptor audit is not a command")
	}
	if code, _, _ := runCmd(t, "", nil, "audit", "everything"); code != 2 {
		t.Errorf("an argument: exit %d", code)
	}
	if code, _, _ := runCmd(t, "", nil, "audit", "--nope"); code != 2 {
		t.Errorf("a flag nobody knows: exit %d", code)
	}
	// Without a database to read it says why and changes nothing.
	if code, _, errOut := runCmd(t, "", map[string]string{"RAPTOR_DB_USER": "u"}, "audit"); code != 1 || !strings.Contains(errOut, "db.name") {
		t.Errorf("without a database name: exit %d, %q", code, errOut)
	}
}
