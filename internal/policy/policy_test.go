package policy

import (
	"strings"
	"testing"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

var (
	runCommand   = Tool{Name: "run_command", Requires: tenant.AccessLook, Input: InputCommand}
	downloadFile = Tool{Name: "download_file", Requires: tenant.AccessLook, Input: InputPath}
	uploadFile   = Tool{Name: "upload_file", Requires: tenant.AccessFull, Input: InputPath, WritesFiles: true}
	serverInfo   = Tool{Name: "get_server_info", Requires: tenant.AccessLook}
)

// twoTenants builds two tenants in code (the config loader accepts one).
// Both have a server called lab-1; beta's grants full, which only test code
// can build in v0.1.
func twoTenants() []tenant.Tenant {
	limits := tenant.Limits{MaxCommandBytes: 64}
	return []tenant.Tenant{
		{
			ID:      "alpha",
			Servers: []tenant.Server{{Tenant: "alpha", ID: "lab-1", Host: "lab-1.example.net", Access: tenant.AccessLook}},
			Policy:  tenant.Policy{DenyCommands: []string{`\bshred\b`}, DenyPaths: []string{"/srv/secret/*", "*.kdbx"}},
			Limits:  limits,
		},
		{
			ID: "beta",
			Servers: []tenant.Server{
				{Tenant: "beta", ID: "lab-1", Host: "other.example.net", Access: tenant.AccessFull},
				{Tenant: "beta", ID: "dark", Host: "dark.example.net", Access: tenant.AccessNone},
			},
			Limits: limits,
		},
	}
}

func newEngine(t *testing.T) (*Engine, []tenant.Tenant) {
	t.Helper()
	ts := twoTenants()
	e, err := NewEngine(ts)
	if err != nil {
		t.Fatal(err)
	}
	return e, ts
}

func TestDecide(t *testing.T) {
	e, ts := newEngine(t)
	alpha, beta := &ts[0], &ts[1]
	pa := tenant.Principal{Tenant: "alpha", Kind: tenant.KindUser, Subject: "a@example.net"}
	pb := tenant.Principal{Tenant: "beta", Kind: tenant.KindService, Subject: "svc"}
	aLab, _ := alpha.Server("lab-1")
	bLab, _ := beta.Server("lab-1")
	bDark, _ := beta.Server("dark")
	tests := []struct {
		name string
		p    tenant.Principal
		srv  *tenant.Server
		tool Tool
		args Args
		want Decision
	}{
		{"happy/look command on own server", pa, aLab, runCommand, Args{Command: "uptime"}, allow},
		{"happy/download on own server", pa, aLab, downloadFile, Args{Path: "/var/log/syslog"}, allow},
		{"happy/no input tool", pa, aLab, serverInfo, Args{}, allow},
		{"sad/no server found", pa, nil, runCommand, Args{Command: "uptime"}, deny(CodeNotFound, RuleServerNotFound)},
		{"sad/server of another tenant is not_found", pa, bLab, runCommand, Args{Command: "uptime"}, deny(CodeNotFound, RuleServerNotFound)},
		{"sad/another tenant's server with a denied command is still not_found", pa, bLab, runCommand, Args{Command: "sudo id"}, deny(CodeNotFound, RuleServerNotFound)},
		{"sad/another tenant's server that would be access_denied is not_found", pb, aLab, uploadFile, Args{Path: "/tmp/x"}, deny(CodeNotFound, RuleServerNotFound)},
		{"sad/unknown tenant", tenant.Principal{Tenant: "gamma"}, aLab, runCommand, Args{Command: "uptime"}, deny(CodeNotFound, RuleServerNotFound)},
		{"sad/empty principal", tenant.Principal{}, aLab, runCommand, Args{Command: "uptime"}, deny(CodeNotFound, RuleServerNotFound)},
		{"sad/upload at look is access_denied", pa, aLab, uploadFile, Args{Path: "/tmp/x"}, deny(CodeAccessDenied, RuleAccessLevel)},
		{"sad/upload at look with a bad path is still access_denied", pa, aLab, uploadFile, Args{Path: "relative"}, deny(CodeAccessDenied, RuleAccessLevel)},
		{"happy/a full grant built in code is accepted (D5)", pb, bLab, uploadFile, Args{Path: "/tmp/x"}, allow},
		{"sad/a full grant still runs the guard rails", pb, bLab, uploadFile, Args{Path: "/etc/shadow"}, deny(CodePolicyDenied, "shadow")},
		{"sad/a server granting none denies look", pb, bDark, runCommand, Args{Command: "uptime"}, deny(CodeAccessDenied, RuleAccessLevel)},
		{"sad/a writing tool that under-declares its level still needs full", pa, aLab, Tool{Name: "w", Requires: tenant.AccessLook, Input: InputPath, WritesFiles: true}, Args{Path: "/tmp/x"}, deny(CodeAccessDenied, RuleAccessLevel)},
		{"sad/built-in command rule", pa, aLab, runCommand, Args{Command: "sudo id"}, deny(CodePolicyDenied, "sudo")},
		{"sad/operator command rule", pa, aLab, runCommand, Args{Command: "shred -u x"}, deny(CodePolicyDenied, "config:deny_commands[0]")},
		{"happy/operator rule of alpha does not apply to beta", pb, bLab, runCommand, Args{Command: "shred -u x"}, allow},
		{"sad/built-in path rule", pa, aLab, downloadFile, Args{Path: "/etc/shadow"}, deny(CodePolicyDenied, "shadow")},
		{"sad/operator path rule with a slash", pa, aLab, downloadFile, Args{Path: "/srv/secret/db.txt"}, deny(CodePolicyDenied, "config:deny_paths[0]")},
		{"sad/operator path rule on a base name", pa, aLab, downloadFile, Args{Path: "/home/u/vault.kdbx"}, deny(CodePolicyDenied, "config:deny_paths[1]")},
		{"corner/operator path rule after cleaning", pa, aLab, downloadFile, Args{Path: "/srv/x/../secret/db.txt"}, deny(CodePolicyDenied, "config:deny_paths[0]")},
		{"edge/command at the tenant's max_command_bytes", pa, aLab, runCommand, Args{Command: strings.Repeat("a", 64)}, allow},
		{"edge/command one byte over the tenant's max_command_bytes", pa, aLab, runCommand, Args{Command: strings.Repeat("a", 65)}, deny(CodePolicyDenied, RuleCommandTooLong)},
		{"raging/unknown tool input", pa, aLab, Tool{Name: "x", Requires: tenant.AccessLook, Input: Input(9)}, Args{}, deny(CodePolicyDenied, RuleUnknownInput)},
		{"corner/a command tool ignores the path argument", pa, aLab, runCommand, Args{Command: "uptime", Path: "/etc/shadow"}, allow},
		{"corner/a path tool ignores the command argument", pa, aLab, downloadFile, Args{Command: "sudo id", Path: "/var/log/syslog"}, allow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := e.Decide(tt.p, tt.srv, tt.tool, tt.args); got != tt.want {
				t.Fatalf("Decide = %+v, want %+v", got, tt.want)
			}
		})
	}
	t.Run("corner/a server whose tenant field disagrees with the lookup is not_found", func(t *testing.T) {
		forged := *aLab
		forged.Tenant = "beta"
		if got := e.Decide(pa, &forged, runCommand, Args{Command: "uptime"}); got.Code != CodeNotFound {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestNewEngine(t *testing.T) {
	t.Run("happy/no tenants", func(t *testing.T) {
		e, err := NewEngine(nil)
		if err != nil {
			t.Fatal(err)
		}
		if d := e.Decide(tenant.Principal{Tenant: "x"}, &tenant.Server{Tenant: "x"}, runCommand, Args{Command: "ls"}); d.Code != CodeNotFound {
			t.Fatalf("got %+v", d)
		}
	})
	bad := []struct {
		name string
		edit func(ts []tenant.Tenant) []tenant.Tenant
		want string
	}{
		{"sad/duplicate tenant", func(ts []tenant.Tenant) []tenant.Tenant { return append(ts, ts[0]) }, `tenant "alpha" appears twice`},
		{"sad/zero max_command_bytes", func(ts []tenant.Tenant) []tenant.Tenant { ts[1].Limits.MaxCommandBytes = 0; return ts }, `tenant "beta": max_command_bytes must be positive`},
		{"sad/operator pattern does not compile", func(ts []tenant.Tenant) []tenant.Tenant {
			ts[0].Policy.DenyCommands = []string{"ok", "(x"}
			return ts
		}, `tenant "alpha": deny_commands[1]`},
		{"sad/operator glob is malformed", func(ts []tenant.Tenant) []tenant.Tenant {
			ts[0].Policy.DenyPaths = []string{"/srv/[x"}
			return ts
		}, `tenant "alpha": deny_paths[0]: not a valid glob`},
		{"edge/operator glob is empty", func(ts []tenant.Tenant) []tenant.Tenant {
			ts[0].Policy.DenyPaths = []string{""}
			return ts
		}, `tenant "alpha": deny_paths[0]: not a valid glob`},
		{"corner/every problem is reported", func(ts []tenant.Tenant) []tenant.Tenant {
			ts[0].Policy.DenyCommands = []string{"(x"}
			ts[1].Limits.MaxCommandBytes = -1
			return ts
		}, "max_command_bytes must be positive"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewEngine(tt.edit(twoTenants()))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}
