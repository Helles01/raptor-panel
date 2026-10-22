package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func testEnv(stdin string, vars map[string]string) (*env, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return &env{
		stdin:  strings.NewReader(stdin),
		stdout: &out,
		stderr: &errOut,
		getenv: func(k string) string { return vars[k] },
	}, &out, &errOut
}

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "version", args: []string{"version"}, wantCode: 0, wantStdout: "raptor "},
		{name: "no command", args: nil, wantCode: 2, wantStderr: "a command is required"},
		{name: "help", args: []string{"help"}, wantCode: 0, wantStderr: "version"},
		{name: "unknown command", args: []string{"nope"}, wantCode: 2, wantStderr: `unknown command "nope"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, out, errOut := testEnv("", nil)

			code := run(context.Background(), tt.args, e)

			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr: %s)", code, tt.wantCode, errOut)
			}
			if !strings.Contains(out.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", out, tt.wantStdout)
			}
			if !strings.Contains(errOut.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", errOut, tt.wantStderr)
			}
		})
	}
}
