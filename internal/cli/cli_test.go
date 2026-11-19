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
		wantNoErr  bool
		maxErr     int
		failStdout bool
	}{
		{name: "happy/version", args: []string{"version"}, wantCode: 0, wantNoErr: true,
			wantOut: []string{"yasshm " + Version, "commit " + Commit, "built " + Date}},
		{name: "sad/no command", args: nil, wantCode: 2, wantNoOut: true,
			wantErr: []string{"usage: yasshm"}},
		{name: "sad/unknown command", args: []string{"frobnicate"}, wantCode: 2, wantNoOut: true,
			wantErr: []string{`unknown command "frobnicate"`, "usage: yasshm"}},
		{name: "edge/version with extra argument", args: []string{"version", "x"}, wantCode: 2, wantNoOut: true,
			wantErr: []string{"takes no arguments"}},
		{name: "edge/empty string command", args: []string{""}, wantCode: 2, wantNoOut: true,
			wantErr: []string{"unknown command"}},
		{name: "sad/stdout write fails", args: []string{"version"}, wantCode: 1, failStdout: true,
			wantErr: []string{"yasshm: write failed"}},
		{name: "sad/help write fails", args: []string{"help"}, wantCode: 1, failStdout: true,
			wantErr: []string{"yasshm: write failed"}},
		{name: "happy/help", args: []string{"help"}, wantCode: 0, wantNoErr: true,
			wantOut: []string{"usage: yasshm", "version"}},
		{name: "happy/-h", args: []string{"-h"}, wantCode: 0, wantNoErr: true,
			wantOut: []string{"usage: yasshm"}},
		{name: "happy/--help", args: []string{"--help"}, wantCode: 0, wantNoErr: true,
			wantOut: []string{"usage: yasshm"}},
		{name: "raging/hostile argument is bounded and quoted",
			args: []string{"x\x00\n" + strings.Repeat("A", 100000)}, wantCode: 2, wantNoOut: true,
			wantErr: []string{"unknown command", `\x00\n`, "usage: yasshm"}, maxErr: 1000},
		{name: "corner/many arguments after unknown command", args: append([]string{"nope"}, make([]string, 1000)...),
			wantCode: 2, wantNoOut: true, wantErr: []string{`unknown command "nope"`}, maxErr: 1000},
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
			if tt.wantNoErr && errb.Len() != 0 {
				t.Errorf("unexpected stderr %q", errb.String())
			}
			if tt.maxErr > 0 && errb.Len() > tt.maxErr {
				t.Errorf("stderr is %d bytes, want at most %d", errb.Len(), tt.maxErr)
			}
			if tt.wantNoOut && out.Len() != 0 {
				t.Errorf("unexpected stdout %q", out.String())
			}
		})
	}
}
