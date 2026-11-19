package cli

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/configtest"
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

func TestConfigCheck(t *testing.T) {
	files := configtest.Write(t)
	valid := files.Config
	invalid := files.Rewrite(t, "version: 1", "version: 2")
	invalid = configtest.Files{Config: invalid}.Rewrite(t, "access: look}\n", "access: full}\n")
	tests := []struct {
		name      string
		args      []string
		wantCode  int
		wantOut   []string
		wantErr   []string
		wantNoOut bool
		wantNoErr bool
	}{
		{name: "happy/valid file", args: []string{"config", "check", "--config", valid}, wantCode: 0, wantNoErr: true,
			wantOut: []string{"config ok: 1 tenant, 2 servers\n"}},
		{name: "happy/valid file with an equals sign", args: []string{"config", "check", "--config=" + valid}, wantCode: 0, wantNoErr: true,
			wantOut: []string{"config ok"}},
		{name: "happy/single dash flag", args: []string{"config", "check", "-config", valid}, wantCode: 0, wantNoErr: true,
			wantOut: []string{"config ok"}},
		{name: "happy/help", args: []string{"config", "check", "-h"}, wantCode: 0, wantNoErr: true,
			wantOut: []string{"usage: yasshm config check --config <file>"}},
		{name: "sad/invalid file lists every problem", args: []string{"config", "check", "--config", invalid}, wantCode: 1, wantNoOut: true,
			wantErr: []string{"version: must be 1 (line 1)\n", "tenants[0](default).servers[0](lab-1).access: access level full is not supported in this version"}},
		{name: "sad/missing file", args: []string{"config", "check", "--config", filepath.Join(t.TempDir(), "nope.yaml")}, wantCode: 1, wantNoOut: true,
			wantErr: []string{"config: cannot read the file"}},
		{name: "sad/missing --config", args: []string{"config", "check"}, wantCode: 2, wantNoOut: true,
			wantErr: []string{"config check needs --config <file>", "usage: yasshm config check"}},
		{name: "sad/empty --config", args: []string{"config", "check", "--config", ""}, wantCode: 2, wantNoOut: true,
			wantErr: []string{"config check needs --config <file>"}},
		{name: "sad/--config without a value", args: []string{"config", "check", "--config"}, wantCode: 2, wantNoOut: true,
			wantErr: []string{"flag needs an argument", "usage: yasshm config check"}},
		{name: "sad/unknown flag", args: []string{"config", "check", "--strict"}, wantCode: 2, wantNoOut: true,
			wantErr: []string{"flag provided but not defined: -strict"}},
		{name: "sad/extra argument", args: []string{"config", "check", "--config", valid, "extra"}, wantCode: 2, wantNoOut: true,
			wantErr: []string{`unexpected argument "extra"`}},
		{name: "sad/config without a subcommand", args: []string{"config"}, wantCode: 2, wantNoOut: true,
			wantErr: []string{"config needs a subcommand"}},
		{name: "sad/unknown config subcommand", args: []string{"config", "show"}, wantCode: 2, wantNoOut: true,
			wantErr: []string{`unknown config subcommand "show"`}},
		{name: "raging/hostile flag name is bounded", args: []string{"config", "check", "--" + strings.Repeat("x", 10000)}, wantCode: 2, wantNoOut: true,
			wantErr: []string{"flag provided but not defined", "..."}},
		{name: "edge/top-level usage mentions config check", args: []string{"help"}, wantCode: 0, wantNoErr: true,
			wantOut: []string{"config check --config <file>"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			code := Run(tt.args, &out, &errb)
			if code != tt.wantCode {
				t.Fatalf("exit code = %d, want %d (stderr %q)", code, tt.wantCode, errb.String())
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
			if tt.wantNoOut && out.Len() != 0 {
				t.Errorf("unexpected stdout %q", out.String())
			}
			if errb.Len() > 2000 {
				t.Errorf("stderr is %d bytes", errb.Len())
			}
		})
	}
	t.Run("corner/success names no host, user, key path, audience or team domain", func(t *testing.T) {
		var out, errb bytes.Buffer
		if code := Run([]string{"config", "check", "--config", valid}, &out, &errb); code != 0 {
			t.Fatalf("exit %d: %s", code, errb.String())
		}
		for _, secretish := range []string{"example.net", "yasshm-look", "id_test", configtest.Audience, "example-team", "ssh-ed25519"} {
			if strings.Contains(out.String(), secretish) {
				t.Fatalf("stdout %q names %q", out.String(), secretish)
			}
		}
	})
	t.Run("sad/stdout write fails", func(t *testing.T) {
		var errb bytes.Buffer
		if code := Run([]string{"config", "check", "--config", valid}, failWriter{}, &errb); code != 1 || !strings.Contains(errb.String(), "write failed") {
			t.Fatalf("code %d, stderr %q", code, errb.String())
		}
	})
	t.Run("sad/help write fails", func(t *testing.T) {
		var errb bytes.Buffer
		if code := Run([]string{"config", "check", "-h"}, failWriter{}, &errb); code != 1 {
			t.Fatalf("code %d", code)
		}
	})
	t.Run("edge/plural", func(t *testing.T) {
		if plural(0, "server") != "0 servers" || plural(1, "server") != "1 server" {
			t.Fatal(plural(0, "server"), plural(1, "server"))
		}
	})
}
