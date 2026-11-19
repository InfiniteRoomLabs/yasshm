package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantOut    []string
		wantErr    []string
		wantNoOut  bool
		failStdout bool
	}{
		{name: "happy/version", args: []string{"version"}, wantCode: 0,
			wantOut: []string{"yasshm " + Version, "commit " + Commit, "built " + Date}},
		{name: "sad/no command", args: nil, wantCode: 2, wantNoOut: true,
			wantErr: []string{"usage: yasshm"}},
		{name: "sad/unknown command", args: []string{"frobnicate"}, wantCode: 2, wantNoOut: true,
			wantErr: []string{`unknown command "frobnicate"`, "usage: yasshm"}},
		{name: "edge/version with extra argument", args: []string{"version", "x"}, wantCode: 2, wantNoOut: true,
			wantErr: []string{"takes no arguments"}},
		{name: "edge/empty string command", args: []string{""}, wantCode: 2, wantNoOut: true,
			wantErr: []string{"unknown command"}},
		{name: "sad/stdout write fails", args: []string{"version"}, wantCode: 1, failStdout: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			var code int
			if tt.failStdout {
				code = Run(tt.args, failWriter{}, &errb)
			} else {
				code = Run(tt.args, &out, &errb)
			}
			if code != tt.wantCode {
				t.Fatalf("exit code = %d, want %d", code, tt.wantCode)
			}
			for _, s := range tt.wantOut {
				if !strings.Contains(out.String(), s) {
					t.Errorf("stdout %q missing %q", out.String(), s)
				}
			}
			for _, s := range tt.wantErr {
				if !strings.Contains(errb.String(), s) {
					t.Errorf("stderr %q missing %q", errb.String(), s)
				}
			}
			if tt.wantNoOut && out.Len() != 0 {
				t.Errorf("unexpected stdout %q", out.String())
			}
		})
	}
}
