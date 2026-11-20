// Package deploy holds the files an operator installs (the systemd unit, the
// example config, the install README) and the tests that keep them true. It has
// no code: the tests read the files beside them.
package deploy

import (
	"os"
	"strings"
	"testing"
)

// unit is the parsed unit file: section -> key -> every value in file order.
type unit map[string]map[string][]string

// parseUnit reads systemd's ini form: comment lines start with # or ;, a
// section is [Name], a setting is Key=Value. A line ending in a backslash
// would continue; the shipped unit has none, and the test fails on one.
func parseUnit(t *testing.T, text string) unit {
	t.Helper()
	u := unit{}
	section := ""
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";"):
			continue
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			section = line[1 : len(line)-1]
			if u[section] == nil {
				u[section] = map[string][]string{}
			}
		default:
			if strings.HasSuffix(line, "\\") {
				t.Fatalf("line %d: a continuation line; the test does not parse them", i+1)
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok || section == "" {
				t.Fatalf("line %d: %q is not a setting inside a section", i+1, line)
			}
			u[section][k] = append(u[section][k], v)
		}
	}
	return u
}

func readUnit(t *testing.T) (u unit, text string) {
	t.Helper()
	b, err := os.ReadFile("yasshm.service")
	if err != nil {
		t.Fatal(err)
	}
	return parseUnit(t, string(b)), string(b)
}

// TestUnitDirectives asserts every directive spec section 8 names, and the
// further hardening the unit carries, is present with the right value.
func TestUnitDirectives(t *testing.T) {
	u, _ := readUnit(t)
	want := []struct{ section, key, value string }{
		{"Service", "User", "yasshm"},
		{"Service", "Group", "yasshm"},
		{"Service", "ExecStartPre", "/usr/local/bin/yasshm config check --config /etc/yasshm/config.yaml"},
		{"Service", "ExecStart", "/usr/local/bin/yasshm serve --config /etc/yasshm/config.yaml"},
		{"Service", "Restart", "on-failure"},
		{"Service", "KillSignal", "SIGTERM"},
		{"Service", "NoNewPrivileges", "yes"},
		{"Service", "ProtectSystem", "strict"},
		{"Service", "ReadWritePaths", "/var/lib/yasshm /var/log/yasshm"},
		{"Service", "ProtectHome", "yes"},
		{"Service", "PrivateTmp", "yes"},
		{"Service", "PrivateDevices", "yes"},
		{"Service", "CapabilityBoundingSet", ""},
		{"Service", "AmbientCapabilities", ""},
		{"Service", "RestrictAddressFamilies", "AF_INET AF_INET6 AF_UNIX"},
		{"Service", "ProtectKernelTunables", "yes"},
		{"Service", "ProtectKernelModules", "yes"},
		{"Service", "ProtectKernelLogs", "yes"},
		{"Service", "ProtectControlGroups", "yes"},
		{"Service", "ProtectClock", "yes"},
		{"Service", "ProtectHostname", "yes"},
		{"Service", "RestrictNamespaces", "yes"},
		{"Service", "RestrictRealtime", "yes"},
		{"Service", "RestrictSUIDSGID", "yes"},
		{"Service", "LockPersonality", "yes"},
		{"Service", "MemoryDenyWriteExecute", "yes"},
		{"Service", "SystemCallArchitectures", "native"},
		{"Service", "UMask", "0077"},
		{"Install", "WantedBy", "multi-user.target"},
	}
	for _, w := range want {
		got := u[w.section][w.key]
		if len(got) != 1 || got[0] != w.value {
			t.Errorf("[%s] %s = %q, want exactly one setting with the value %q", w.section, w.key, got, w.value)
		}
	}
	// The system call filter is two lines: the allowed set, then the denied groups.
	filter := strings.Join(u["Service"]["SystemCallFilter"], " | ")
	if filter != "@system-service | ~@privileged @resources" {
		t.Errorf("SystemCallFilter = %q", filter)
	}
}

// TestUnitRestartAndStop checks the values with a range: a restart delay, a
// start limit and a stop timeout that leaves the gateway its whole graceful
// stop (internal/config's TestUnitStopTimeout ties the number to the limits
// table).
func TestUnitRestartAndStop(t *testing.T) {
	u, _ := readUnit(t)
	for _, k := range []string{"RestartSec", "TimeoutStopSec"} {
		if len(u["Service"][k]) != 1 {
			t.Errorf("[Service] %s is not set exactly once", k)
		}
	}
	for _, k := range []string{"StartLimitIntervalSec", "StartLimitBurst"} {
		if len(u["Unit"][k]) != 1 {
			t.Errorf("[Unit] %s is not set exactly once (a crash loop must stop)", k)
		}
	}
}

// TestUnitNoEnvironment keeps MCPGODEBUG (and any Environment= line) out of
// the service: the SDK reads MCPGODEBUG once and a malformed value stops the
// process. The unit's comment may name it.
func TestUnitNoEnvironment(t *testing.T) {
	u, text := readUnit(t)
	for _, k := range []string{"Environment", "EnvironmentFile", "PassEnvironment", "UnsetEnvironment"} {
		if len(u["Service"][k]) != 0 {
			t.Errorf("[Service] %s is set; the gateway needs no environment", k)
		}
	}
	for i, line := range strings.Split(text, "\n") {
		code := strings.TrimSpace(line)
		if !strings.HasPrefix(code, "#") && strings.Contains(code, "MCPGODEBUG") {
			t.Errorf("line %d sets or names MCPGODEBUG outside a comment", i+1)
		}
	}
	if !strings.Contains(text, "MCPGODEBUG") {
		t.Error("the unit does not say why MCPGODEBUG is absent")
	}
}

// TestUnitNoIPAddressDeny: IPAddressDeny=any without the operator's allow list
// would cut the gateway off from its targets, so the unit carries only a
// commented example.
func TestUnitNoIPAddressDeny(t *testing.T) {
	u, text := readUnit(t)
	for _, k := range []string{"IPAddressDeny", "IPAddressAllow"} {
		if len(u["Service"][k]) != 0 {
			t.Errorf("[Service] %s is active; leave it as a commented example", k)
		}
	}
	if !strings.Contains(text, "#   IPAddressDeny=any") {
		t.Error("the commented IPAddressDeny example is missing")
	}
}

// TestUnitIsASCII: the unit is a tracked text file like the docs.
func TestUnitIsASCII(t *testing.T) {
	_, text := readUnit(t)
	for i, line := range strings.Split(text, "\n") {
		for _, r := range line {
			if r > 126 || (r < 32 && r != '\t') {
				t.Fatalf("line %d holds a non-ASCII or control character %U", i+1, r)
			}
		}
	}
}

// TestParseUnit proves the parser rejects what it cannot read.
func TestParseUnit(t *testing.T) {
	u := parseUnit(t, "# c\n[A]\nk=v\nk=w\n; c\n[B]\nz=\n")
	if got := u["A"]["k"]; len(got) != 2 || got[1] != "w" {
		t.Fatalf("A.k = %q", got)
	}
	if got, ok := u["B"]["z"]; !ok || len(got) != 1 || got[0] != "" {
		t.Fatalf("B.z = %q, %v", got, ok)
	}
}
