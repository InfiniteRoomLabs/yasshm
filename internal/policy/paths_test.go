package policy

import (
	"path"
	"strings"
	"testing"
)

// pathCases holds, for every built-in path rule, paths it must match and
// paths it must not match. TestPathRuleCases fails for a rule that is missing
// here or has an empty list.
var pathCases = map[string]struct{ match, noMatch []string }{
	"shadow": {
		match:   []string{"/etc/shadow", "/etc/shadow-", "/etc/gshadow", "/etc/gshadow-"},
		noMatch: []string{"/etc/passwd", "/etc/group", "/etc/shadow.bak.d/x", "/home/u/etc/shadow"},
	},
	"sudoers": {
		match:   []string{"/etc/sudoers", "/etc/sudoers.d/90-cloud-init-users"},
		noMatch: []string{"/etc/sudo.conf", "/etc/sudoers.d", "/home/u/sudoers"},
	},
	"ssh-host-key": {
		match:   []string{"/etc/ssh/ssh_host_ed25519_key", "/etc/ssh/ssh_host_rsa_key"},
		noMatch: []string{"/etc/ssh/ssh_host_ed25519_key.pub", "/etc/ssh/sshd_config", "/etc/ssh/ssh_known_hosts"},
	},
	"ssh-private-key": {
		match:   []string{"/home/u/.ssh/id_ed25519", "/root/.ssh/id_rsa", "/tmp/id_ecdsa", "/srv/backup/id_ed25519_sk"},
		noMatch: []string{"/home/u/.ssh/id_ed25519.pub", "/home/u/.ssh/authorized_keys", "/home/u/.ssh/known_hosts", "/home/u/.ssh/config", "/srv/id_rsa_notes/readme"},
	},
	"key-file": {
		match:   []string{"/etc/ssl/private/server.key", "/home/u/cert.p12", "/opt/app/keystore.jks", "/x/y.pfx", "/opt/app/app.keystore"},
		noMatch: []string{"/etc/ssl/certs/ca.pem", "/etc/ssl/certs/server.crt", "/usr/share/keyrings/x.gpg", "/home/u/key.txt", "/home/u/monkey"},
	},
	"env-file": {
		match:   []string{"/srv/app/.env", "/srv/app/.env.production"},
		noMatch: []string{"/srv/app/.envrc", "/srv/app/env.example", "/etc/environment", "/srv/app/.environment"},
	},
	"credential-file": {
		match:   []string{"/home/u/.netrc", "/root/.pgpass", "/home/u/.git-credentials", "/root/.my.cnf", "/srv/app/credentials.json"},
		noMatch: []string{"/home/u/.gitconfig", "/etc/mysql/my.cnf", "/srv/app/credentials.md", "/srv/netrc"},
	},
	"cloud-credentials": {
		match:   []string{"/root/.aws/credentials", "/home/u/.aws/credentials", "/home/u/.config/gcloud/access_tokens.db", "/root/.kube/config", "/home/u/.kube/config"},
		noMatch: []string{"/home/u/.aws/config", "/home/u/.config/other/x", "/home/u/.kube/cache/x", "/home/u/sub/.aws/credentials"},
	},
	"proc-secrets": {
		match:   []string{"/proc/1/environ", "/proc/self/environ", "/proc/42/mem", "/proc/42/task/43/environ", "/proc/kcore"},
		noMatch: []string{"/proc/cpuinfo", "/proc/1/status", "/proc/self/cmdline", "/proc/meminfo"},
	},
}

func TestPathRuleCases(t *testing.T) {
	names := map[string]bool{}
	for _, r := range builtinPaths {
		if names[r.Name] {
			t.Fatalf("sad/duplicate rule name %q", r.Name)
		}
		names[r.Name] = true
		c, ok := pathCases[r.Name]
		if !ok || len(c.match) == 0 || len(c.noMatch) == 0 {
			t.Errorf("sad/rule %q needs at least one must-match and one must-not-match case in pathCases", r.Name)
			continue
		}
		for _, p := range c.match {
			t.Run("happy/"+r.Name+"/matches "+p, func(t *testing.T) {
				if !r.matches(p) {
					t.Fatalf("rule %s does not match %q", r.Name, p)
				}
				if d := checkPathBuiltin(p); d.Allow || d.Rule != r.Name {
					t.Fatalf("path %q: %+v, want denied by %s", p, d, r.Name)
				}
			})
		}
		for _, p := range c.noMatch {
			t.Run("sad/"+r.Name+"/does not match "+p, func(t *testing.T) {
				if r.matches(p) {
					t.Fatalf("rule %s matches %q", r.Name, p)
				}
			})
		}
	}
	for name := range pathCases {
		if !names[name] {
			t.Errorf("sad/pathCases has %q, which is not a built-in rule", name)
		}
	}
	for _, r := range builtinPaths {
		for _, g := range r.Globs {
			if _, err := path.Match(g, ""); err != nil {
				t.Errorf("sad/rule %s glob %q is malformed: %v", r.Name, g, err)
			}
		}
	}
}

func checkPathBuiltin(p string) Decision {
	return (&tenantRules{maxCommandBytes: 1}).checkPath(p)
}

func TestPathGuardRails(t *testing.T) {
	tests := []struct {
		name string
		path string
		rule string // "" = allowed
	}{
		{"happy/a log file", "/var/log/syslog", ""},
		{"happy/a public key", "/home/u/.ssh/id_ed25519.pub", ""},
		{"happy/the root directory", "/", ""},
		{"sad/empty", "", RulePathEmpty},
		{"sad/relative", "etc/shadow", RulePathNotAbsolute},
		{"sad/dot relative", "./etc/shadow", RulePathNotAbsolute},
		{"sad/home relative", "~/.ssh/id_rsa", RulePathNotAbsolute},
		{"raging/NUL byte", "/var/log/x\x00/etc/shadow", RulePathNUL},
		{"raging/invalid UTF-8", "/var/log/\xff", RulePathBadUTF8},
		{"edge/max length", "/" + strings.Repeat("a", MaxPathBytes-1), ""},
		{"raging/too long", "/" + strings.Repeat("a", MaxPathBytes), RulePathTooLong},
		{"corner/dot segment is cleaned first", "/etc/./shadow", "shadow"},
		{"corner/dot-dot segment is cleaned first", "/etc/x/../shadow", "shadow"},
		{"corner/doubled slashes are cleaned first", "//etc//shadow", "shadow"},
		{"corner/trailing slash is cleaned first", "/etc/shadow/", "shadow"},
		{"corner/dot-dot above the root", "/../../etc/shadow", "shadow"},
		{"corner/hiding a key behind dot-dot", "/home/u/.ssh/x/../id_rsa", "ssh-private-key"},
		{"edge/leading space is not absolute", " /etc/shadow", RulePathNotAbsolute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := checkPathBuiltin(tt.path)
			if tt.rule == "" {
				if !d.Allow {
					t.Fatalf("want allowed, got %+v", d)
				}
				return
			}
			if d.Allow || d.Code != CodePolicyDenied || d.Rule != tt.rule {
				t.Fatalf("got %+v, want policy_denied by %s", d, tt.rule)
			}
		})
	}
	t.Run("happy/CleanPath is path.Clean", func(t *testing.T) {
		if got := CleanPath("/etc/x/../shadow"); got != "/etc/shadow" {
			t.Fatalf("CleanPath = %q", got)
		}
	})
}
