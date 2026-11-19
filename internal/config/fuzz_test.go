package config

import (
	"regexp"
	"strings"
	"testing"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// checkInvariants fails when an accepted config breaks a rule the loader is
// supposed to enforce.
func checkInvariants(t *testing.T, cfg *Config) {
	t.Helper()
	if cfg.Version != 1 || len(cfg.Tenants) != MaxTenants {
		t.Fatalf("accepted version %d with %d tenants", cfg.Version, len(cfg.Tenants))
	}
	if checkListen(cfg.Gateway.Listen, cfg.Gateway.AllowNonLoopback) != "" ||
		checkAbsPath(cfg.Gateway.DataDir) != "" || checkAbsPath(cfg.Gateway.AuditLog) != "" {
		t.Fatalf("accepted gateway settings %+v", cfg.Gateway)
	}
	a := cfg.Auth
	if a.Provider != ProviderCloudflareAccess || !isHostname(a.CloudflareAccess.TeamDomain) || a.CloudflareAccess.Audience == "" {
		t.Fatalf("accepted auth %+v", a)
	}
	if a.CloudflareAccess.CertsURL != "" && checkCertsURL(a.CloudflareAccess.CertsURL) != "" {
		t.Fatalf("accepted certs_url %q", a.CloudflareAccess.CertsURL)
	}
	for i := range cfg.Tenants {
		tn := &cfg.Tenants[i]
		if !idPattern.MatchString(string(tn.ID)) || len(tn.Principals) == 0 || len(tn.Identities) == 0 || len(tn.Servers) == 0 {
			t.Fatalf("accepted tenant %+v", tn)
		}
		seen := map[tenant.ServerID]bool{}
		for _, s := range tn.Servers {
			id, ok := tn.Identities[s.Identity.ID]
			if s.Tenant != tn.ID || !idPattern.MatchString(string(s.ID)) || seen[s.ID] || s.Access != tenant.AccessLook ||
				s.Port < 1 || s.Port > 65535 || !isHost(s.Host) || !ok || id != s.Identity ||
				(s.HostKey == "") == (s.KnownHostsFile == "") {
				t.Fatalf("accepted server %+v", s)
			}
			seen[s.ID] = true
		}
		for _, id := range tn.Identities {
			if checkAbsPath(id.PrivateKeyFile) != "" || id.User == "" {
				t.Fatalf("accepted identity %+v", id)
			}
		}
		for _, w := range wantLimits {
			if v := w.get(tn.Limits); v < w.min || v > w.max {
				t.Fatalf("accepted %s = %d", w.key, v)
			}
		}
		for _, p := range wantPairs {
			if wantLimitByKey(t, p[0]).get(tn.Limits) > wantLimitByKey(t, p[1]).get(tn.Limits) {
				t.Fatalf("accepted %s > %s", p[0], p[1])
			}
		}
		for _, c := range tn.Policy.DenyCommands {
			if re, err := regexp.Compile(c); err != nil || re.MatchString("") {
				t.Fatalf("accepted deny_commands entry %q", c)
			}
		}
		for _, g := range tn.Policy.DenyPaths {
			if checkPathGlob(g) != "" {
				t.Fatalf("accepted deny_paths entry %q", g)
			}
		}
	}
}

// FuzzParse feeds arbitrary bytes to the decoder: it must never panic, every
// rejection is an *Error with at least one problem, and an accepted config
// always satisfies the validation invariants.
func FuzzParse(f *testing.F) {
	k := genKeys(f)
	base := k.base()
	f.Add([]byte(base))
	f.Add([]byte(""))
	f.Add([]byte("version: 1\n"))
	f.Add([]byte(strings.Replace(base, "access: look", "access: full", 1)))
	f.Add([]byte(strings.Replace(base, "limits: {}", "limits: {default_wait_seconds: 61}", 1)))
	f.Add([]byte(strings.Replace(base, "listen: 127.0.0.1:8822", "listen: &a 127.0.0.1:8822", 1)))
	f.Add([]byte(strings.Replace(base, "deny_commands: []", `deny_commands: ['\bshred\b']`, 1)))
	f.Add([]byte(base + "---\n"))
	f.Add([]byte("a: [\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		cfg, err := Parse(data)
		if err != nil {
			if len(problems(t, err)) == 0 {
				t.Fatal("rejected with no problems")
			}
			return
		}
		checkInvariants(t, cfg)
	})
}
