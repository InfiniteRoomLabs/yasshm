package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/InfiniteRoomLabs/yasshm/internal/keyfile"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

func TestParseBase(t *testing.T) {
	k := genKeys(t)
	t.Run("happy/base config", func(t *testing.T) {
		cfg, err := Parse([]byte(k.base()))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Version != 1 || cfg.Gateway.Listen != "127.0.0.1:8822" || cfg.Gateway.DataDir != "/var/lib/yasshm" ||
			cfg.Gateway.AuditLog != "/var/log/yasshm/audit.jsonl" || cfg.Gateway.AllowNonLoopback {
			t.Fatalf("gateway settings: %+v", cfg.Gateway)
		}
		if cfg.Auth.Provider != ProviderCloudflareAccess || cfg.Auth.CloudflareAccess.TeamDomain != "example-team.cloudflareaccess.com" ||
			cfg.Auth.CloudflareAccess.Audience != "EXAMPLE-AUDIENCE-TAG" || cfg.Auth.CloudflareAccess.CertsURL != "" {
			t.Fatalf("auth: %+v", cfg.Auth)
		}
		if len(cfg.Tenants) != 1 {
			t.Fatalf("tenants: %d", len(cfg.Tenants))
		}
		tn := cfg.Tenants[0]
		if tn.ID != "default" || len(tn.Principals) != 1 || tn.Principals[0].Email != "operator@example.net" {
			t.Fatalf("tenant: %+v", tn)
		}
		id := tn.Identities["lab-look"]
		if id.ID != "lab-look" || id.User != "yasshm-look" || id.PrivateKeyFile != k.keyFile {
			t.Fatalf("identity: %+v", id)
		}
		s, ok := cfg.Server("default", "lab-1")
		if !ok {
			t.Fatal("server lab-1 not found")
		}
		if s.Tenant != "default" || s.Host != "lab-1.example.net" || s.Port != 22 || s.Description != "Example lab host" ||
			s.HostKey != k.hostKey || s.KnownHostsFile != "" || s.Access != tenant.AccessLook ||
			len(s.Tags) != 1 || s.Tags[0] != "lab" || s.IdentityFor(tenant.AccessLook) != id {
			t.Fatalf("server: %+v", s)
		}
		if tn.Limits != DefaultLimits() {
			t.Fatalf("limits %+v, want the defaults", tn.Limits)
		}
		if len(tn.Policy.DenyCommands) != 0 || len(tn.Policy.DenyPaths) != 0 {
			t.Fatalf("policy: %+v", tn.Policy)
		}
	})
	t.Run("sad/lookups are tenant scoped", func(t *testing.T) {
		cfg, err := Parse([]byte(k.base()))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := cfg.Server("other", "lab-1"); ok {
			t.Fatal("found lab-1 under another tenant")
		}
		if _, ok := cfg.Server("default", "nope"); ok {
			t.Fatal("found a server that does not exist")
		}
		if _, ok := cfg.Tenant("other"); ok {
			t.Fatal("found a tenant that does not exist")
		}
	})
	t.Run("happy/the default limits are the spec's", func(t *testing.T) {
		want := tenant.Limits{
			DefaultWaitSeconds: 30, MaxWaitSeconds: 60, DefaultTimeoutSeconds: 600, MaxTimeoutSeconds: 3600,
			MaxConcurrentCommands: 4, MaxConcurrentPerServer: 2, MaxCommandBytes: 8192, MaxOutputBytes: 16777216,
			MaxResultBytes: 100000, RetentionDays: 7, MaxDataBytes: 1073741824, MaxUploadBytes: 1048576,
			KillGraceSeconds: 5, ConnectTimeoutSeconds: 10, ShutdownGraceSeconds: 20,
		}
		if got := DefaultLimits(); got != want {
			t.Fatalf("DefaultLimits = %+v\nwant %+v", got, want)
		}
	})
}

// ruleCase edits the base config and expects one problem.
type ruleCase struct {
	name string
	old  string
	repl string
	want string // "" means the edited config must be valid
}

func runRuleCases(t *testing.T, k *keys, cases []ruleCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text := edit(t, k.base(), c.old, c.repl)
			_, err := Parse([]byte(text))
			if c.want == "" {
				if err != nil {
					t.Fatalf("want valid, got:\n%v", err)
				}
				return
			}
			wantProblem(t, err, c.want)
		})
	}
}

func TestParseTopLevelRules(t *testing.T) {
	k := genKeys(t)
	runRuleCases(t, k, []ruleCase{
		{"sad/version 2", "version: 1", "version: 2", "version: must be 1"},
		{"sad/version as a string", "version: 1", `version: "1"`, "version: expected an integer, got a string"},
		{"sad/version missing", "version: 1\n", "", "version: is required"},
		{"sad/version null", "version: 1", "version: ~", "version: is required"},
		{"sad/unknown top-level key", "version: 1", "version: 1\nlisten: 127.0.0.1:1", "listen: unknown key"},
		{"sad/duplicate key", "version: 1", "version: 1\nversion: 1", "version: duplicate key (first at line 1)"},
		{"sad/server missing", "server:\n  listen: 127.0.0.1:8822\n  data_dir: /var/lib/yasshm\n  audit_log: /var/log/yasshm/audit.jsonl\n", "", "server: is required"},
		{"sad/server is a list", "server:\n  listen: 127.0.0.1:8822\n  data_dir: /var/lib/yasshm\n  audit_log: /var/log/yasshm/audit.jsonl\n", "server: [1]\n", "server: expected a mapping, got a sequence"},
		{"happy/listen on ipv6 loopback", "127.0.0.1:8822", "'[::1]:8822'", ""},
		{"happy/listen on another loopback address", "127.0.0.1:8822", "127.0.0.2:8822", ""},
		{"edge/listen on port 0", "127.0.0.1:8822", "127.0.0.1:0", ""},
		{"edge/listen on port 65535", "127.0.0.1:8822", "127.0.0.1:65535", ""},
		{"sad/listen on a public address", "127.0.0.1:8822", "192.0.2.10:8822", "server.listen: must be a loopback IP address"},
		{"sad/listen on all interfaces", "127.0.0.1:8822", "':8822'", "server.listen: must be a loopback IP address"},
		{"sad/listen on localhost by name", "127.0.0.1:8822", "localhost:8822", "server.listen: must be a loopback IP address"},
		{"sad/listen without a port", "127.0.0.1:8822", "127.0.0.1", "server.listen: must be host:port"},
		{"sad/listen port out of range", "127.0.0.1:8822", "127.0.0.1:65536", "server.listen: port must be between 0 and 65535"},
		{"sad/listen port not a number", "127.0.0.1:8822", "127.0.0.1:http", "server.listen: port must be between 0 and 65535"},
		{"happy/non-loopback when allowed", "listen: 127.0.0.1:8822", "listen: 192.0.2.10:8822\n  allow_non_loopback: true", ""},
		{"happy/all interfaces when allowed", "listen: 127.0.0.1:8822", "listen: ':8822'\n  allow_non_loopback: true", ""},
		{"happy/host name when allowed", "listen: 127.0.0.1:8822", "listen: gw.example.net:8822\n  allow_non_loopback: true", ""},
		{"sad/bad host when allowed", "listen: 127.0.0.1:8822", "listen: 'bad host:8822'\n  allow_non_loopback: true", "server.listen: host must be an IP address or a DNS host name"},
		{"sad/non-loopback when explicitly false", "listen: 127.0.0.1:8822", "listen: 192.0.2.10:8822\n  allow_non_loopback: false", "server.listen: must be a loopback IP address"},
		{"sad/allow_non_loopback is not a boolean", "listen: 127.0.0.1:8822", "listen: 127.0.0.1:8822\n  allow_non_loopback: yes", "server.allow_non_loopback: expected a boolean, got a string"},
		{"sad/data_dir relative", "data_dir: /var/lib/yasshm", "data_dir: var/lib/yasshm", "server.data_dir: must be an absolute path"},
		{"sad/data_dir not clean", "data_dir: /var/lib/yasshm", "data_dir: /var/lib/yasshm/", "server.data_dir: must be a clean path"},
		{"sad/data_dir missing", "  data_dir: /var/lib/yasshm\n", "", "server.data_dir: is required"},
		{"sad/audit_log with dot-dot", "audit_log: /var/log/yasshm/audit.jsonl", "audit_log: /var/log/../yasshm/audit.jsonl", "server.audit_log: must be a clean path"},
		{"sad/audit_log is a number", "audit_log: /var/log/yasshm/audit.jsonl", "audit_log: 5", "server.audit_log: expected a string, got an integer"},
		{"sad/unknown key under server", "data_dir: /var/lib/yasshm", "data_dir: /var/lib/yasshm\n  datadir: /x", "server.datadir: unknown key"},
	})
}

func TestParseAuthRules(t *testing.T) {
	k := genKeys(t)
	runRuleCases(t, k, []ruleCase{
		{"sad/auth missing", "auth:\n  provider: cloudflare-access\n  cloudflare_access:\n    team_domain: example-team.cloudflareaccess.com\n    audience: \"EXAMPLE-AUDIENCE-TAG\"\n", "", "auth: is required"},
		{"sad/unknown provider", "provider: cloudflare-access", "provider: oidc", `auth.provider: must be "cloudflare-access"`},
		{"sad/provider missing", "  provider: cloudflare-access\n", "", "auth.provider: is required"},
		{"sad/cloudflare_access missing", "  cloudflare_access:\n    team_domain: example-team.cloudflareaccess.com\n    audience: \"EXAMPLE-AUDIENCE-TAG\"\n", "", "auth.cloudflare_access: is required"},
		{"sad/team_domain with a scheme", "team_domain: example-team.cloudflareaccess.com", "team_domain: https://example-team.cloudflareaccess.com", "auth.cloudflare_access.team_domain: must be a DNS host name"},
		{"sad/team_domain missing", "    team_domain: example-team.cloudflareaccess.com\n", "", "auth.cloudflare_access.team_domain: is required"},
		{"sad/audience with a space", `audience: "EXAMPLE-AUDIENCE-TAG"`, `audience: "EXAMPLE AUDIENCE"`, "auth.cloudflare_access.audience: must be 1 to 512 characters"},
		{"sad/audience empty", `audience: "EXAMPLE-AUDIENCE-TAG"`, `audience: ""`, "auth.cloudflare_access.audience: must be 1 to 512 characters"},
		{"raging/audience too long", `audience: "EXAMPLE-AUDIENCE-TAG"`, "audience: " + strings.Repeat("a", 513), "auth.cloudflare_access.audience: must be 1 to 512 characters"},
		{"happy/certs_url https", `audience: "EXAMPLE-AUDIENCE-TAG"`, `audience: "EXAMPLE-AUDIENCE-TAG"` + "\n    certs_url: https://issuer.example.net/certs", ""},
		{"happy/certs_url http on loopback", `audience: "EXAMPLE-AUDIENCE-TAG"`, `audience: "EXAMPLE-AUDIENCE-TAG"` + "\n    certs_url: http://127.0.0.1:9999/certs", ""},
		{"happy/certs_url http on ipv6 loopback", `audience: "EXAMPLE-AUDIENCE-TAG"`, `audience: "EXAMPLE-AUDIENCE-TAG"` + "\n    certs_url: http://[::1]:9999/certs", ""},
		{"sad/certs_url http elsewhere", `audience: "EXAMPLE-AUDIENCE-TAG"`, `audience: "EXAMPLE-AUDIENCE-TAG"` + "\n    certs_url: http://issuer.example.net/certs", "auth.cloudflare_access.certs_url: must be https"},
		{"sad/certs_url http on localhost by name", `audience: "EXAMPLE-AUDIENCE-TAG"`, `audience: "EXAMPLE-AUDIENCE-TAG"` + "\n    certs_url: http://localhost/certs", "auth.cloudflare_access.certs_url: must be https"},
		{"sad/certs_url relative", `audience: "EXAMPLE-AUDIENCE-TAG"`, `audience: "EXAMPLE-AUDIENCE-TAG"` + "\n    certs_url: /certs", "auth.cloudflare_access.certs_url: must be an absolute URL"},
		{"sad/certs_url unparsable", `audience: "EXAMPLE-AUDIENCE-TAG"`, `audience: "EXAMPLE-AUDIENCE-TAG"` + "\n    certs_url: \"http://[::1\"", "auth.cloudflare_access.certs_url: must be an absolute URL"},
		{"sad/certs_url with user info", `audience: "EXAMPLE-AUDIENCE-TAG"`, `audience: "EXAMPLE-AUDIENCE-TAG"` + "\n    certs_url: https://u:p@issuer.example.net/certs", "auth.cloudflare_access.certs_url: must not carry user information"},
		{"sad/certs_url ftp", `audience: "EXAMPLE-AUDIENCE-TAG"`, `audience: "EXAMPLE-AUDIENCE-TAG"` + "\n    certs_url: ftp://issuer.example.net/certs", "auth.cloudflare_access.certs_url: must be https"},
		{"sad/unknown key under cloudflare_access", `audience: "EXAMPLE-AUDIENCE-TAG"`, `audience: "EXAMPLE-AUDIENCE-TAG"` + "\n    jwks: x", "auth.cloudflare_access.jwks: unknown key"},
	})
}

func TestParseTenantRules(t *testing.T) {
	k := genKeys(t)
	secondTenant := "\n  - id: second\n    principals: [{email: b@example.net}]\n    identities: {x: {user: u, private_key_file: /k}}\n    servers: [{id: s, host: s.example.net, identity: x, host_key: \"" + k.hostKey + "\", access: look}]\n"
	runRuleCases(t, k, []ruleCase{
		{"sad/tenants empty", "tenants:\n", "tenants: []\nignored:\n", "tenants: must not be empty"},
		{"sad/two tenants are refused by MaxTenants", "    limits: {}\n", "    limits: {}" + secondTenant, "tenants: at most 1 tenant is supported in this version (MaxTenants), got 2"},
		{"sad/tenant id upper case", "  - id: default", "  - id: Default", "tenants[0].id: tenant id must match"},
		{"sad/tenant id missing", "  - id: default\n    principals:", "  - principals:", "tenants[0].id: is required"},
		{"raging/tenant id too long", "  - id: default", "  - id: " + strings.Repeat("a", 64), "tenants[0].id: tenant id must match"},
		{"edge/tenant id 63 characters", "  - id: default", "  - id: " + strings.Repeat("a", 63), ""},
		{"sad/tenant id starts with a hyphen", "  - id: default", "  - id: -x", "tenants[0].id: tenant id must match"},
		{"sad/unknown key in a tenant", "    limits: {}", "    limits: {}\n    name: x", "tenants[0](default).name: unknown key"},
		{"sad/tenant item is a string", "  - id: default\n", "  - just-a-string\n  - id: default\n", "tenants[0]: expected a mapping, got a string"},
		{"sad/tenant item is null", "  - id: default\n", "  - ~\n  - id: default\n", "tenants[0]: expected a mapping, got null"},
		{"sad/principals missing", "    principals:\n      - email: operator@example.net\n", "", "tenants[0](default).principals: is required"},
		{"sad/principals empty", "      - email: operator@example.net\n", "", "tenants[0](default).principals: is required"},
		{"sad/principals is a mapping", "    principals:\n      - email: operator@example.net\n", "    principals: {email: a@example.net}\n", "tenants[0](default).principals: expected a sequence, got a mapping"},
		{"happy/service token principal", "- email: operator@example.net", "- common_name: abc123.access", ""},
		{"sad/principal with both forms", "- email: operator@example.net", "- {email: operator@example.net, common_name: x}", "tenants[0](default).principals[0]: needs exactly one of email or common_name"},
		{"sad/principal with neither form", "- email: operator@example.net", "- {}", "tenants[0](default).principals[0]: needs exactly one of email or common_name"},
		{"sad/principal is null", "- email: operator@example.net", "- ~", "tenants[0](default).principals[0]: expected a mapping, got null"},
		{"sad/principal with an unknown key", "- email: operator@example.net", "- {email: operator@example.net, group: x}", "tenants[0](default).principals[0].group: unknown key"},
		{"sad/bad email", "- email: operator@example.net", "- email: operator", "tenants[0](default).principals[0].email: must be an ASCII email address"},
		{"sad/email with two at signs", "- email: operator@example.net", "- email: a@b@example.net", "tenants[0](default).principals[0].email: must be an ASCII email address"},
		{"sad/email with a space", "- email: operator@example.net", "- email: 'op erator@example.net'", "tenants[0](default).principals[0].email: must be an ASCII email address"},
		{"sad/common_name with a space", "- email: operator@example.net", "- common_name: 'a b'", "tenants[0](default).principals[0].common_name: must be 1 to 256 characters"},
		{"sad/duplicate email ignoring case", "- email: operator@example.net", "- email: operator@example.net\n      - email: Operator@Example.NET", "tenants[0](default).principals[1]: duplicate principal (also tenants[0](default).principals[0])"},
		{"sad/duplicate common_name", "- email: operator@example.net", "- common_name: x\n      - common_name: x", "tenants[0](default).principals[1]: duplicate principal"},
		{"sad/email is null", "- email: operator@example.net", "- email: ~", "tenants[0](default).principals[0].email: is required"},
		{"sad/email left blank", "- email: operator@example.net", "- email:", "tenants[0](default).principals[0].email: is required"},
		{"sad/email is empty", "- email: operator@example.net", "- email: ''", "tenants[0](default).principals[0].email: is required"},
		{"sad/common_name is null", "- email: operator@example.net", "- common_name: ~", "tenants[0](default).principals[0].common_name: is required"},
		{"corner/a null email beside a valid one is still reported", "- email: operator@example.net", "- email: ~\n      - email: operator@example.net", "tenants[0](default).principals[0].email: is required"},
		{"corner/a list of only bad principals leaves the tenant without one", "- email: operator@example.net", "- email: ~", "tenants[0](default).principals: must hold at least one valid principal"},
		{"raging/non-ASCII email", "- email: operator@example.net", "- email: \"op\u00e9rator@example.net\"", "tenants[0](default).principals[0].email: must be an ASCII email address"},
		{"raging/Kelvin sign in an email", "- email: operator@example.net", "- email: \"\u212aelvin@example.net\"", "tenants[0](default).principals[0].email: must be an ASCII email address"},
	})
}

func TestParseIdentityRules(t *testing.T) {
	k := genKeys(t)
	runRuleCases(t, k, []ruleCase{
		{"sad/identities missing", "    identities:\n      lab-look:\n        user: yasshm-look\n        private_key_file: " + k.keyFile + "\n", "", "tenants[0](default).identities: is required"},
		{"sad/identities empty", "    identities:\n      lab-look:\n        user: yasshm-look\n        private_key_file: " + k.keyFile + "\n", "    identities: {}\n", "tenants[0](default).identities: must not be empty"},
		{"sad/identities is a list", "    identities:\n      lab-look:\n        user: yasshm-look\n        private_key_file: " + k.keyFile + "\n", "    identities: [a]\n", "tenants[0](default).identities: expected a mapping, got a sequence"},
		{"sad/identity id invalid", "      lab-look:\n", "      Lab_Look:\n", "tenants[0](default).identities.Lab_Look: identity id must match"},
		{"sad/identity id not a string", "      lab-look:\n", "      7:\n", "tenants[0](default).identities: keys must be plain strings, got an integer"},
		{"sad/identity is null", "      lab-look:\n        user: yasshm-look\n        private_key_file: " + k.keyFile + "\n", "      lab-look: ~\n", "tenants[0](default).identities.lab-look: expected a mapping, got null"},
		{"sad/duplicate identity", "      lab-look:\n", "      lab-look: {user: a, private_key_file: /a}\n      lab-look:\n", "tenants[0](default).identities.lab-look: duplicate key"},
		{"sad/user missing", "        user: yasshm-look\n", "", "tenants[0](default).identities.lab-look.user: is required"},
		{"sad/user with a space", "user: yasshm-look", "user: 'yasshm look'", "tenants[0](default).identities.lab-look.user: must be a login name"},
		{"sad/user starts with a digit", "user: yasshm-look", "user: '1abc'", "tenants[0](default).identities.lab-look.user: must be a login name"},
		{"sad/private_key_file relative", "private_key_file: " + k.keyFile, "private_key_file: keys/id", "tenants[0](default).identities.lab-look.private_key_file: must be an absolute path"},
		{"sad/private_key_file missing", "        private_key_file: " + k.keyFile + "\n", "", "tenants[0](default).identities.lab-look.private_key_file: is required"},
		{"sad/unknown key in identity", "user: yasshm-look", "user: yasshm-look\n        passphrase: x", "tenants[0](default).identities.lab-look.passphrase: unknown key"},
	})
}

func TestParseServerRules(t *testing.T) {
	k := genKeys(t)
	certLine := "ssh-ed25519-cert-v01@openssh.com AAAA"
	runRuleCases(t, k, []ruleCase{
		{"sad/servers missing", "    servers:\n", "    servers: ~\n    ignored:\n", "tenants[0](default).servers: is required"},
		{"sad/servers empty", "    servers:\n", "    servers: []\n    ignored:\n", "tenants[0](default).servers: must not be empty"},
		{"sad/server id invalid", "      - id: lab-1", "      - id: lab_1", "tenants[0](default).servers[0].id: server id must match"},
		{"sad/server id missing", "      - id: lab-1\n        description", "      - description", "tenants[0](default).servers[0].id: is required"},
		{"sad/server is null", "    servers:\n", "    servers:\n      - ~\n", "tenants[0](default).servers[0]: expected a mapping, got null"},
		{"sad/duplicate server id", "    servers:\n", "    servers:\n      - {id: lab-1, host: a.example.net, identity: lab-look, host_key: \"" + k.hostKey + "\", access: look}\n", "tenants[0](default).servers[1](lab-1).id: duplicate server id (also tenants[0](default).servers[0])"},
		{"happy/server without description, port or tags", "        description: Example lab host\n        host: lab-1.example.net\n        port: 22\n", "        host: lab-1.example.net\n", ""},
		{"edge/description of 256 bytes", "description: Example lab host", "description: " + strings.Repeat("d", 256), ""},
		{"sad/description too long", "description: Example lab host", "description: " + strings.Repeat("d", 257), "tenants[0](default).servers[0](lab-1).description: must be at most 256 bytes"},
		{"raging/description with a control character", "description: Example lab host", `description: "a\x1bb"`, "tenants[0](default).servers[0](lab-1).description: must be at most 256 bytes without control characters"},
		{"happy/host is an IPv4 address", "host: lab-1.example.net", "host: 192.0.2.7", ""},
		{"happy/host is an IPv6 address", "host: lab-1.example.net", "host: '2001:db8::7'", ""},
		{"sad/host with a port", "host: lab-1.example.net", "host: lab-1.example.net:22", "tenants[0](default).servers[0](lab-1).host: must be a DNS host name or an IP address"},
		{"sad/host with an underscore", "host: lab-1.example.net", "host: lab_1.example.net", "tenants[0](default).servers[0](lab-1).host: must be a DNS host name or an IP address"},
		{"sad/host missing", "        host: lab-1.example.net\n", "", "tenants[0](default).servers[0](lab-1).host: is required"},
		{"edge/port 1", "port: 22", "port: 1", ""},
		{"edge/port 65535", "port: 22", "port: 65535", ""},
		{"sad/port 0", "port: 22", "port: 0", "tenants[0](default).servers[0](lab-1).port: must be between 1 and 65535"},
		{"sad/port 65536", "port: 22", "port: 65536", "tenants[0](default).servers[0](lab-1).port: must be between 1 and 65535"},
		{"sad/port as a string", "port: 22", `port: "22"`, "tenants[0](default).servers[0](lab-1).port: expected an integer, got a string"},
		{"raging/port beyond 64 bits", "port: 22", "port: 99999999999999999999999", "tenants[0](default).servers[0](lab-1).port: expected an integer, got a number"},
		{"sad/identity that does not exist", "identity: lab-look", "identity: nope", "tenants[0](default).servers[0](lab-1).identity: names an identity that does not exist in this tenant"},
		{"sad/identity missing", "        identity: lab-look\n", "", "tenants[0](default).servers[0](lab-1).identity: is required"},
		{"sad/neither host_key nor known_hosts_file", "        host_key: \"" + k.hostKey + "\"\n", "", "tenants[0](default).servers[0](lab-1): needs exactly one of host_key or known_hosts_file"},
		{"sad/both host_key and known_hosts_file", "        access: look", "        access: look\n        known_hosts_file: /etc/yasshm/known_hosts", "tenants[0](default).servers[0](lab-1): needs exactly one of host_key or known_hosts_file"},
		{"happy/known_hosts_file instead of host_key", "host_key: \"" + k.hostKey + "\"", "known_hosts_file: /etc/yasshm/known_hosts", ""},
		{"sad/known_hosts_file relative", "host_key: \"" + k.hostKey + "\"", "known_hosts_file: known_hosts", "tenants[0](default).servers[0](lab-1).known_hosts_file: must be an absolute path"},
		{"sad/host_key garbage", k.hostKey, "ssh-ed25519 AAAAnotbase64!", "tenants[0](default).servers[0](lab-1).host_key: does not parse as an OpenSSH public key"},
		{"sad/host_key the placeholder of the spec example", k.hostKey, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEXAMPLEONLYEXAMPLEONLYEXAMPLEONLYEXAMPLEONLY00", "tenants[0](default).servers[0](lab-1).host_key: does not parse"},
		{"happy/host_key with a comment", k.hostKey, k.hostKey + " root@lab-1", ""},
		{"sad/host_key with options", k.hostKey, "restrict " + k.hostKey, "tenants[0](default).servers[0](lab-1).host_key: must not carry authorized_keys options"},
		{"raging/host_key on two lines", k.hostKey, k.hostKey + `\n` + k.hostKey, "tenants[0](default).servers[0](lab-1).host_key: must be one line"},
		{"sad/host_key is a certificate", k.hostKey, certLine, "tenants[0](default).servers[0](lab-1).host_key: does not parse"},
		{"sad/access full is reserved", "access: look", "access: full", "tenants[0](default).servers[0](lab-1).access: access level full is not supported in this version"},
		{"sad/access none", "access: look", "access: none", `tenants[0](default).servers[0](lab-1).access: must be "look"`},
		{"sad/access Look", "access: look", "access: Look", `tenants[0](default).servers[0](lab-1).access: must be "look"`},
		{"sad/access missing", "        access: look\n", "", "tenants[0](default).servers[0](lab-1).access: is required"},
		{"sad/tag invalid", "tags: [lab]", "tags: [Lab]", "tenants[0](default).servers[0](lab-1).tags[0]: tag must match"},
		{"sad/tag not a string", "tags: [lab]", "tags: [lab, 5]", "tenants[0](default).servers[0](lab-1).tags[1]: expected a string, got an integer"},
		{"sad/duplicate tag", "tags: [lab]", "tags: [lab, lab]", "tenants[0](default).servers[0](lab-1).tags[1]: duplicate tag"},
		{"edge/sixteen tags", "tags: [lab]", "tags: [" + tagList(16) + "]", ""},
		{"sad/seventeen tags", "tags: [lab]", "tags: [" + tagList(17) + "]", "tenants[0](default).servers[0](lab-1).tags: at most 16 tags"},
		{"sad/tags is a string", "tags: [lab]", "tags: lab", "tenants[0](default).servers[0](lab-1).tags: expected a sequence, got a string"},
		{"sad/unknown key in a server", "access: look", "access: look\n        user: root", "tenants[0](default).servers[0](lab-1).user: unknown key"},
	})
}

func repeatList(item string, n int) string {
	items := make([]string, n)
	for i := range items {
		items[i] = item
	}
	return strings.Join(items, ", ")
}

func tagList(n int) string {
	tags := make([]string, n)
	for i := range tags {
		tags[i] = fmt.Sprintf("t%d", i)
	}
	return strings.Join(tags, ", ")
}

func TestParsePolicyRules(t *testing.T) {
	k := genKeys(t)
	runRuleCases(t, k, []ruleCase{
		{"happy/operator deny lists", "deny_commands: []", `deny_commands: ['\bshred\b', '^systemctl\s+stop']`, ""},
		{"happy/operator path globs", "deny_paths: []", `deny_paths: ['/srv/secrets/*', '*.kdbx']`, ""},
		{"happy/policy absent", "    policy:\n      deny_commands: []\n      deny_paths: []\n", "", ""},
		{"happy/policy null", "    policy:\n      deny_commands: []\n      deny_paths: []\n", "    policy: ~\n", ""},
		{"sad/deny_commands entry does not compile", "deny_commands: []", `deny_commands: ['(unclosed']`, "tenants[0](default).policy.deny_commands[0]: does not compile as an RE2 expression"},
		{"sad/deny_commands entry uses a backreference", "deny_commands: []", `deny_commands: ['(a)\1']`, "tenants[0](default).policy.deny_commands[0]: does not compile as an RE2 expression"},
		{"sad/deny_commands entry matches everything", "deny_commands: []", `deny_commands: ['ok', 'rm|']`, "tenants[0](default).policy.deny_commands[1]: matches the empty string"},
		{"sad/deny_commands entry empty", "deny_commands: []", `deny_commands: ['']`, "tenants[0](default).policy.deny_commands[0]: must be 1 to 1024 bytes"},
		{"raging/deny_commands entry too long", "deny_commands: []", "deny_commands: ['" + strings.Repeat("a", 1025) + "']", "tenants[0](default).policy.deny_commands[0]: must be 1 to 1024 bytes"},
		{"sad/deny_commands entry is a number", "deny_commands: []", "deny_commands: [5]", "tenants[0](default).policy.deny_commands[0]: expected a string, got an integer"},
		{"sad/deny_paths bad glob", "deny_paths: []", `deny_paths: ['/srv/[x']`, "tenants[0](default).policy.deny_paths[0]: is not a valid glob"},
		{"sad/deny_paths relative with a slash", "deny_paths: []", `deny_paths: ['srv/secrets']`, "tenants[0](default).policy.deny_paths[0]: a glob with a slash must start with / or **/"},
		{"sad/deny_paths entry empty", "deny_paths: []", `deny_paths: ['']`, "tenants[0](default).policy.deny_paths[0]: must be 1 to 1024 bytes"},
		{"happy/any-depth glob", "deny_paths: []", `deny_paths: ['**/.aws/credentials']`, ""},
		{"happy/subtree glob", "deny_paths: []", `deny_paths: ['/srv/secret/**']`, ""},
		{"happy/any-depth subtree glob", "deny_paths: []", `deny_paths: ['**/.config/gcloud/**']`, ""},
		{"sad/double star in the middle", "deny_paths: []", `deny_paths: ['/srv/**/x']`, "tenants[0](default).policy.deny_paths[0]: ** is allowed only as the whole first or last path element"},
		{"sad/double star inside an element", "deny_paths: []", `deny_paths: ['/srv/a**']`, "tenants[0](default).policy.deny_paths[0]: ** is allowed only as the whole first or last path element"},
		{"sad/double star alone", "deny_paths: []", `deny_paths: ['**']`, "tenants[0](default).policy.deny_paths[0]: ** is allowed only as the whole first or last path element"},
		{"sad/deny_commands error quotes the pattern", "deny_commands: []", "deny_commands: [\"(\\x1b\"]", `tenants[0](default).policy.deny_commands[0]: does not compile as an RE2 expression: missing closing ) in "(\x1b"`},
		{"edge/256 deny_commands entries", "deny_commands: []", "deny_commands: [" + repeatList("'x'", 256) + "]", ""},
		{"raging/257 deny_commands entries", "deny_commands: []", "deny_commands: [" + repeatList("'x'", 257) + "]", "tenants[0](default).policy.deny_commands: at most 256 entries, got 257"},
		{"edge/256 deny_paths entries", "deny_paths: []", "deny_paths: [" + repeatList("'x'", 256) + "]", ""},
		{"raging/257 deny_paths entries", "deny_paths: []", "deny_paths: [" + repeatList("'x'", 257) + "]", "tenants[0](default).policy.deny_paths: at most 256 entries, got 257"},
		{"sad/unknown key in policy", "deny_paths: []", "deny_paths: []\n      allow_commands: []", "tenants[0](default).policy.allow_commands: unknown key"},
	})
}

// wantLimit is the spec 5.1 table as this test states it, independently of
// limitTable: a changed default, bound or pair in limits.go must also be
// changed here (and in the spec callout), or this test fails.
type wantLimit struct {
	key           string
	def, min, max int64
	get           func(tenant.Limits) int64
}

var wantLimits = []wantLimit{
	{"default_wait_seconds", 30, 0, 290, func(l tenant.Limits) int64 { return l.DefaultWaitSeconds }},
	{"max_wait_seconds", 60, 1, 290, func(l tenant.Limits) int64 { return l.MaxWaitSeconds }},
	{"default_timeout_seconds", 600, 1, 86400, func(l tenant.Limits) int64 { return l.DefaultTimeoutSeconds }},
	{"max_timeout_seconds", 3600, 1, 86400, func(l tenant.Limits) int64 { return l.MaxTimeoutSeconds }},
	{"max_concurrent_commands", 4, 1, 64, func(l tenant.Limits) int64 { return l.MaxConcurrentCommands }},
	{"max_concurrent_per_server", 2, 1, 64, func(l tenant.Limits) int64 { return l.MaxConcurrentPerServer }},
	{"max_command_bytes", 8192, 1, 65536, func(l tenant.Limits) int64 { return l.MaxCommandBytes }},
	{"max_output_bytes", 16777216, 1024, 1073741824, func(l tenant.Limits) int64 { return l.MaxOutputBytes }},
	{"max_result_bytes", 100000, 1024, 140000, func(l tenant.Limits) int64 { return l.MaxResultBytes }},
	{"retention_days", 7, 1, 3650, func(l tenant.Limits) int64 { return l.RetentionDays }},
	{"max_data_bytes", 1073741824, 1048576, 1099511627776, func(l tenant.Limits) int64 { return l.MaxDataBytes }},
	{"max_upload_bytes", 1048576, 1, 1048576, func(l tenant.Limits) int64 { return l.MaxUploadBytes }},
	{"kill_grace_seconds", 5, 1, 60, func(l tenant.Limits) int64 { return l.KillGraceSeconds }},
	{"connect_timeout_seconds", 10, 1, 120, func(l tenant.Limits) int64 { return l.ConnectTimeoutSeconds }},
	{"shutdown_grace_seconds", 20, 1, 300, func(l tenant.Limits) int64 { return l.ShutdownGraceSeconds }},
}

// wantPairs are the cross-field rules: lo must not exceed hi.
var wantPairs = [][2]string{
	{"default_wait_seconds", "max_wait_seconds"},
	{"default_timeout_seconds", "max_timeout_seconds"},
	{"max_concurrent_per_server", "max_concurrent_commands"},
	{"max_output_bytes", "max_data_bytes"},
}

func wantLimitByKey(t testing.TB, key string) wantLimit {
	t.Helper()
	for _, w := range wantLimits {
		if w.key == key {
			return w
		}
	}
	t.Fatalf("no limit %s in wantLimits", key)
	return wantLimit{}
}

// partnerOf returns the other key of the pair key is in, or "".
func partnerOf(key string) string {
	for _, p := range wantPairs {
		if p[0] == key {
			return p[1]
		}
		if p[1] == key {
			return p[0]
		}
	}
	return ""
}

func TestParseLimits(t *testing.T) {
	k := genKeys(t)
	t.Run("happy/every limit key is in the test table", func(t *testing.T) {
		if len(wantLimits) != 15 {
			t.Fatalf("wantLimits has %d rows, want 15", len(wantLimits))
		}
		text := k.base()
		var b strings.Builder
		for _, w := range wantLimits {
			fmt.Fprintf(&b, "%s: %d, ", w.key, w.def)
		}
		cfg, err := Parse([]byte(edit(t, text, "limits: {}", "limits: {"+strings.TrimSuffix(b.String(), ", ")+"}")))
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range wantLimits {
			if got := w.get(cfg.Tenants[0].Limits); got != w.def {
				t.Errorf("%s = %d, want %d", w.key, got, w.def)
			}
			if got := w.get(DefaultLimits()); got != w.def {
				t.Errorf("default %s = %d, want %d", w.key, got, w.def)
			}
		}
	})
	for _, w := range wantLimits {
		// A bound that would break a cross-field rule on its own is paired
		// with an in-range value of the partner that keeps the rule.
		partner := func(v int64) string {
			other := partnerOf(w.key)
			if other == "" {
				return ""
			}
			o := wantLimitByKey(t, other)
			return fmt.Sprintf(", %s: %d", other, min(max(v, o.min), o.max))
		}
		for _, c := range []struct {
			class string
			v     int64
			ok    bool
		}{
			{"edge/minimum", w.min, true},
			{"edge/maximum", w.max, true},
			{"sad/below minimum", w.min - 1, false},
			{"sad/above maximum", w.max + 1, false},
		} {
			t.Run(c.class+" "+w.key, func(t *testing.T) {
				text := edit(t, k.base(), "limits: {}", fmt.Sprintf("limits: {%s: %d%s}", w.key, c.v, partner(c.v)))
				cfg, err := Parse([]byte(text))
				if c.ok {
					if err != nil {
						t.Fatalf("want valid, got:\n%v", err)
					}
					if got := w.get(cfg.Tenants[0].Limits); got != c.v {
						t.Fatalf("%s = %d, want %d", w.key, got, c.v)
					}
					return
				}
				wantProblem(t, err, fmt.Sprintf("tenants[0](default).limits.%s: must be between %d and %d", w.key, w.min, w.max))
			})
		}
	}
	for _, p := range wantPairs {
		lo, hi := wantLimitByKey(t, p[0]), wantLimitByKey(t, p[1])
		v := max(lo.min, hi.min) + 1
		t.Run("sad/"+p[0]+" above "+p[1], func(t *testing.T) {
			text := edit(t, k.base(), "limits: {}", fmt.Sprintf("limits: {%s: %d, %s: %d}", p[0], v, p[1], v-1))
			_, err := Parse([]byte(text))
			wantProblem(t, err, fmt.Sprintf("tenants[0](default).limits.%s: must not exceed %s (%d > %d)", p[0], p[1], v, v-1))
		})
		t.Run("edge/"+p[0]+" equal to "+p[1], func(t *testing.T) {
			text := edit(t, k.base(), "limits: {}", fmt.Sprintf("limits: {%s: %d, %s: %d}", p[0], v, p[1], v))
			if _, err := Parse([]byte(text)); err != nil {
				t.Fatalf("want valid, got:\n%v", err)
			}
		})
	}
	t.Run("corner/no pair rule beyond the four of spec 5.1", func(t *testing.T) {
		// Every in-range combination that breaks none of the four pairs is
		// accepted: lo at its maximum, hi at its minimum, for unpaired keys.
		for _, a := range wantLimits {
			for _, b := range wantLimits {
				if a.key == b.key || partnerOf(a.key) == b.key || partnerOf(a.key) != "" || partnerOf(b.key) != "" {
					continue
				}
				text := edit(t, k.base(), "limits: {}", fmt.Sprintf("limits: {%s: %d, %s: %d}", a.key, a.max, b.key, b.min))
				if _, err := Parse([]byte(text)); err != nil {
					t.Fatalf("%s=%d with %s=%d rejected:\n%v", a.key, a.max, b.key, b.min, err)
				}
			}
		}
	})
	runRuleCases(t, k, []ruleCase{
		{"happy/limits absent", "    limits: {}\n", "", ""},
		{"happy/limits null", "limits: {}", "limits: ~", ""},
		{"sad/unknown limit", "limits: {}", "limits: {max_jobs: 3}", "tenants[0](default).limits.max_jobs: unknown key"},
		{"sad/limit as a float", "limits: {}", "limits: {retention_days: 1.5}", "tenants[0](default).limits.retention_days: expected an integer, got a number"},
		{"sad/limit as a string", "limits: {}", `limits: {retention_days: "7"}`, "tenants[0](default).limits.retention_days: expected an integer, got a string"},
		{"sad/limits is a list", "limits: {}", "limits: [1]", "tenants[0](default).limits: expected a mapping, got a sequence"},
		{"corner/bad value does not also report the pair", "limits: {}", "limits: {default_wait_seconds: x, max_wait_seconds: 1}", "tenants[0](default).limits.default_wait_seconds: expected an integer"},
	})
	t.Run("corner/a bad partner suppresses the pair rule", func(t *testing.T) {
		text := edit(t, k.base(), "limits: {}", "limits: {default_wait_seconds: x, max_wait_seconds: 1}")
		_, err := Parse([]byte(text))
		for _, p := range problems(t, err) {
			if strings.Contains(p, "must not exceed") {
				t.Fatalf("pair rule reported on a bad value: %s", p)
			}
		}
	})
}

func TestParseDocumentRules(t *testing.T) {
	k := genKeys(t)
	tests := []struct {
		name string
		text string
		want string
	}{
		{"sad/empty file", "", "config: the file is empty"},
		{"sad/only a comment", "# nothing\n", "config: the file is empty"},
		{"sad/null document", "~\n", "config: the file is empty"},
		{"sad/root is a list", "- a\n", "config: expected a mapping, got a sequence"},
		{"sad/root is a string", "hello\n", "config: expected a mapping, got a string"},
		{"raging/anchored root", "&r {version: 1}\n", "config: anchors are not supported"},
		{"sad/syntax error", "version: 1\n  bad: [\n", "config: "},
		{"sad/two documents", k.base() + "---\nversion: 1\n", "config: the file must hold exactly one YAML document"},
		{"raging/anchor and alias", edit(t, k.base(), "  listen: 127.0.0.1:8822", "  listen: &l 127.0.0.1:8822") + "x: *l\n", "server.listen: anchors are not supported"},
		{"raging/alias as a value", edit(t, edit(t, k.base(), "      deny_commands: []", "      deny_commands: &d []"), "      deny_paths: []", "      deny_paths: *d"), "tenants[0](default).policy.deny_paths: aliases are not supported"},
		{"raging/merge key", edit(t, k.base(), "    limits: {}", "    limits: {<<: {retention_days: 3}}"), "tenants[0](default).limits: keys must be plain strings, got a value tagged !!merge"},
		{"raging/anchored key", edit(t, k.base(), "version: 1", "&v version: 1"), "version: anchors are not supported"},
		{"raging/mapping as a key", edit(t, k.base(), "version: 1", "version: 1\n? {a: 1}\n: x"), ": keys must be plain strings, got a mapping"},
		{"raging/NUL in a string", edit(t, k.base(), "description: Example lab host", `description: "a\0b"`), "tenants[0](default).servers[0](lab-1).description: must be valid UTF-8 without NUL"},
		{"raging/hostile key is quoted and bounded", edit(t, k.base(), "version: 1", "version: 1\n\"a\\tb"+strings.Repeat("z", 200)+"\": 1"), `"a\tb` + strings.Repeat("z", 61) + `": unknown key`},
		{"raging/explicit string tag on an integer field", edit(t, k.base(), "version: 1", "version: !!str 1"), "version: expected an integer, got a string"},
		{"raging/custom tag", edit(t, k.base(), "version: 1", "version: !custom 1"), "version: expected an integer, got a value tagged !custom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.text))
			wantProblem(t, err, tt.want)
		})
	}
	t.Run("raging/file larger than MaxFileBytes", func(t *testing.T) {
		_, err := Parse(make([]byte, MaxFileBytes+1))
		wantProblem(t, err, "config: the file is larger than")
	})
	t.Run("raging/billion laughs is refused, not expanded", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("a0: &a0 [x, x, x, x, x, x, x, x, x, x]\n")
		for i := 1; i < 9; i++ {
			fmt.Fprintf(&b, "a%d: &a%d [*a%d, *a%d, *a%d, *a%d, *a%d, *a%d, *a%d, *a%d, *a%d, *a%d]\n", i, i, i-1, i-1, i-1, i-1, i-1, i-1, i-1, i-1, i-1, i-1)
		}
		_, err := Parse([]byte(b.String()))
		wantProblem(t, err, "a0: unknown key")
	})
	t.Run("raging/deep nesting", func(t *testing.T) {
		text := "version: " + strings.Repeat("[", 20000) + strings.Repeat("]", 20000) + "\n"
		_, err := Parse([]byte(text))
		wantProblem(t, err, "config: ")
	})
}

func TestProblemsAllAtOnce(t *testing.T) {
	k := genKeys(t)
	text := k.base()
	text = edit(t, text, "version: 1", "version: 3")
	text = edit(t, text, "127.0.0.1:8822", "192.0.2.1:8822")
	text = edit(t, text, "access: look", "access: full")
	text = edit(t, text, "limits: {}", "limits: {retention_days: 0}")
	_, err := Parse([]byte(text))
	got := problems(t, err)
	want := []string{
		"version: must be 1",
		"server.listen: must be a loopback IP address (127.0.0.1 or ::1) unless server.allow_non_loopback is true",
		"tenants[0](default).servers[0](lab-1).access: access level full is not supported in this version",
		"tenants[0](default).limits.retention_days: must be between 1 and 3650",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("corner/every problem in file order:\ngot  %q\nwant %q", got, want)
	}
	t.Run("happy/error text is one line per problem with the line number", func(t *testing.T) {
		lines := strings.Split(err.Error(), "\n")
		if len(lines) != 4 || lines[0] != "version: must be 1 (line 1)" {
			t.Fatalf("Error() = %q", err.Error())
		}
	})
}

func TestNewError(t *testing.T) {
	t.Run("edge/no problems is no error", func(t *testing.T) {
		if err := newError(nil); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("corner/sorted by line then path then message, duplicates dropped", func(t *testing.T) {
		err := newError([]Problem{
			{Path: "b", Line: 2, Message: "y"},
			{Path: "b", Line: 2, Message: "x"},
			{Path: "a", Line: 2, Message: "z"},
			{Path: "z", Line: 1, Message: "m"},
			{Path: "b", Line: 2, Message: "x"},
		})
		want := "z: m (line 1)\na: z (line 2)\nb: x (line 2)\nb: y (line 2)"
		if err.Error() != want {
			t.Fatalf("got %q, want %q", err.Error(), want)
		}
	})
	t.Run("happy/problem without a line", func(t *testing.T) {
		if s := (Problem{Path: "config", Message: "m"}).String(); s != "config: m" {
			t.Fatalf("got %q", s)
		}
	})
}

func TestLoad(t *testing.T) {
	t.Run("happy/the spec example loads with generated keys", func(t *testing.T) {
		k := genKeys(t)
		spec, err := os.ReadFile(filepath.Join("..", "..", "docs", "superpowers", "specs", "2026-11-18-yasshm-design.md"))
		if err != nil {
			t.Fatal(err)
		}
		_, after, ok := strings.Cut(string(spec), "```yaml\n")
		example, _, ok2 := strings.Cut(after, "```")
		if !ok || !ok2 {
			t.Fatal("no yaml example in spec 5.1")
		}
		example = edit(t, example, "/etc/yasshm/keys/lab-look_ed25519", k.keyFile)
		example = edit(t, example, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEXAMPLEONLYEXAMPLEONLYEXAMPLEONLYEXAMPLEONLY00", k.hostKey)
		cfg, err := Load(writeConfig(t, k.dir, example))
		if err != nil {
			t.Fatal(err)
		}
		if s, ok := cfg.Server("default", "lab-1"); !ok || s.Access != tenant.AccessLook {
			t.Fatalf("server: %+v %v", s, ok)
		}
	})
	t.Run("happy/known_hosts file that lists the host", func(t *testing.T) {
		k := genKeys(t)
		kh := filepath.Join(k.dir, "known_hosts")
		line := "lab-1.example.net " + k.hostKey + "\n[lab-1.example.net]:2222 " + strings.TrimSpace(string(sshAuthorized(k.otherPub))) + "\n"
		writeFile(t, kh, []byte(line), 0o644)
		text := edit(t, k.base(), "host_key: \""+k.hostKey+"\"", "known_hosts_file: "+kh)
		cfg, err := Load(writeConfig(t, k.dir, text))
		if err != nil {
			t.Fatal(err)
		}
		if s, _ := cfg.Server("default", "lab-1"); s.KnownHostsFile != kh || s.HostKey != "" {
			t.Fatalf("server: %+v", s)
		}
		t.Run("edge/non-default port uses the bracketed entry", func(t *testing.T) {
			moved := edit(t, text, "port: 22", "port: 2222")
			if _, err := Load(writeConfig(t, t.TempDir(), moved)); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("sad/known_hosts file without the host", func(t *testing.T) {
		k := genKeys(t)
		kh := filepath.Join(k.dir, "known_hosts")
		writeFile(t, kh, []byte("other.example.net "+k.hostKey+"\n"), 0o644)
		text := edit(t, k.base(), "host_key: \""+k.hostKey+"\"", "known_hosts_file: "+kh)
		_, err := Load(writeConfig(t, k.dir, text))
		wantProblem(t, err, "tenants[0](default).servers[0](lab-1).known_hosts_file: lists no key for lab-1.example.net:22")
	})
	t.Run("sad/known_hosts file that does not parse", func(t *testing.T) {
		k := genKeys(t)
		kh := filepath.Join(k.dir, "known_hosts")
		writeFile(t, kh, []byte("this is not a known hosts line\n"), 0o644)
		text := edit(t, k.base(), "host_key: \""+k.hostKey+"\"", "known_hosts_file: "+kh)
		_, err := Load(writeConfig(t, k.dir, text))
		wantProblem(t, err, "tenants[0](default).servers[0](lab-1).known_hosts_file: does not parse as a known_hosts file")
	})
	t.Run("sad/known_hosts file missing", func(t *testing.T) {
		k := genKeys(t)
		text := edit(t, k.base(), "host_key: \""+k.hostKey+"\"", "known_hosts_file: "+filepath.Join(k.dir, "nope"))
		_, err := Load(writeConfig(t, k.dir, text))
		wantProblem(t, err, "tenants[0](default).servers[0](lab-1).known_hosts_file: file does not exist")
	})
	t.Run("sad/known_hosts path is a directory", func(t *testing.T) {
		k := genKeys(t)
		text := edit(t, k.base(), "host_key: \""+k.hostKey+"\"", "known_hosts_file: "+k.dir)
		_, err := Load(writeConfig(t, k.dir, text))
		wantProblem(t, err, "tenants[0](default).servers[0](lab-1).known_hosts_file: is not a regular file")
	})
	t.Run("known_hosts markers and patterns", func(t *testing.T) {
		k := genKeys(t)
		key := k.hostKey
		other := strings.TrimSpace(string(sshAuthorized(k.otherPub)))
		hashed := knownhosts.HashHostname("lab-1.example.net")
		cases := []struct {
			name, content, want string // want "" = accepted
		}{
			{"happy/plain entry", "lab-1.example.net " + key + "\n", ""},
			{"happy/hashed entry that matches", hashed + " " + key + "\n", ""},
			{"sad/hashed entry for another host", knownhosts.HashHostname("other.example.net") + " " + key + "\n", "lists no key for lab-1.example.net:22"},
			{"happy/wildcard pattern pins the key for every match", "*.example.net " + key + "\n", ""},
			{"happy/a listed key beside a revoked other key", "lab-1.example.net " + key + "\n@revoked * " + other + "\n", ""},
			{"sad/cert-authority line only", "@cert-authority lab-1.example.net " + key + "\n", "holds a @cert-authority line; SSH certificates are not supported in this version"},
			{"sad/cert-authority line beside a plain key", "lab-1.example.net " + key + "\n@cert-authority *.example.net " + other + "\n", "holds a @cert-authority line"},
			{"sad/key listed and also revoked", "lab-1.example.net " + key + "\n@revoked * " + key + "\n", "lists only revoked keys for lab-1.example.net:22"},
			{"sad/revoked line only", "@revoked lab-1.example.net " + key + "\n", "lists no key for lab-1.example.net:22"},
			{"sad/entry for another port", "[lab-1.example.net]:2222 " + key + "\n", "lists no key for lab-1.example.net:22"},
			{"edge/empty file", "", "lists no key for lab-1.example.net:22"},
			{"edge/comments only", "# nothing here\n", "lists no key for lab-1.example.net:22"},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				kh := filepath.Join(t.TempDir(), "known_hosts")
				writeFile(t, kh, []byte(c.content), 0o644)
				text := edit(t, k.base(), "host_key: \""+k.hostKey+"\"", "known_hosts_file: "+kh)
				_, err := Load(writeConfig(t, t.TempDir(), text))
				if c.want == "" {
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				wantProblem(t, err, "tenants[0](default).servers[0](lab-1).known_hosts_file: "+c.want)
			})
		}
		t.Run("raging/known_hosts file larger than 1 MiB", func(t *testing.T) {
			kh := filepath.Join(t.TempDir(), "known_hosts")
			writeFile(t, kh, []byte(strings.Repeat("#", maxKnownHostsBytes+1)), 0o644)
			text := edit(t, k.base(), "host_key: \""+k.hostKey+"\"", "known_hosts_file: "+kh)
			_, err := Load(writeConfig(t, t.TempDir(), text))
			wantProblem(t, err, "known_hosts_file: is larger than 1 MiB")
		})
	})
	t.Run("sad/config file missing", func(t *testing.T) {
		_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
		wantProblem(t, err, "config: cannot read the file")
	})
	t.Run("sad/config path is a directory", func(t *testing.T) {
		_, err := Load(t.TempDir())
		wantProblem(t, err, "config: is not a regular file")
	})
	t.Run("raging/config file larger than MaxFileBytes", func(t *testing.T) {
		dir := t.TempDir()
		_, err := Load(writeConfig(t, dir, strings.Repeat("#", MaxFileBytes+1)))
		wantProblem(t, err, "config: the file is larger than")
	})
	t.Run("corner/decode and file problems are reported together", func(t *testing.T) {
		k := genKeys(t)
		text := edit(t, k.base(), "version: 1", "version: 2")
		text = edit(t, text, "private_key_file: "+k.keyFile, "private_key_file: "+filepath.Join(k.dir, "missing"))
		_, err := Load(writeConfig(t, k.dir, text))
		wantProblem(t, err, "version: must be 1")
		wantProblem(t, err, "tenants[0](default).identities.lab-look.private_key_file: file does not exist")
	})
}

func TestLoadPrivateKey(t *testing.T) {
	const marker = "SECRET-MARKER-NOT-A-KEY"
	tests := []struct {
		name  string
		setup func(t *testing.T, k *keys) string // returns the key path to use
		want  string
	}{
		{"happy/mode 0600", func(_ *testing.T, k *keys) string { return k.keyFile }, ""},
		{"happy/mode 0400", func(t *testing.T, k *keys) string {
			chmod(t, k.keyFile, 0o400)
			return k.keyFile
		}, ""},
		{"sad/readable by group", func(t *testing.T, k *keys) string {
			chmod(t, k.keyFile, 0o640)
			return k.keyFile
		}, "is accessible by group or other (mode 0640); it must be 0600 or stricter"},
		{"sad/readable by other", func(t *testing.T, k *keys) string {
			chmod(t, k.keyFile, 0o604)
			return k.keyFile
		}, "is accessible by group or other (mode 0604)"},
		{"sad/writable by group", func(t *testing.T, k *keys) string {
			chmod(t, k.keyFile, 0o620)
			return k.keyFile
		}, "is accessible by group or other (mode 0620)"},
		{"sad/missing", func(_ *testing.T, k *keys) string { return filepath.Join(k.dir, "missing") }, "file does not exist"},
		{"sad/a directory", func(_ *testing.T, k *keys) string { return k.dir }, "is not a regular file"},
		{"sad/a public key", func(t *testing.T, k *keys) string {
			p := filepath.Join(k.dir, "pub")
			writeFile(t, p, []byte(k.hostKey+"\n"), 0o600)
			return p
		}, "does not parse as a private key"},
		{"sad/protected by a passphrase", func(t *testing.T, k *keys) string {
			priv, _ := newEd25519(t)
			block, err := sshMarshalWithPassphrase(priv)
			if err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(k.dir, "enc")
			writeFile(t, p, block, 0o600)
			return p
		}, "is protected by a passphrase; yasshm needs a key without one"},
		{"raging/garbage content is not echoed", func(t *testing.T, k *keys) string {
			p := filepath.Join(k.dir, "garbage")
			writeFile(t, p, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\n"+marker+"\n-----END OPENSSH PRIVATE KEY-----\n"), 0o600)
			return p
		}, "does not parse as a private key"},
		{"raging/too large", func(t *testing.T, k *keys) string {
			p := filepath.Join(k.dir, "big")
			writeFile(t, p, make([]byte, keyfile.MaxBytes+1), 0o600)
			return p
		}, "is too large to be a private key"},
		{"corner/a key readable by others is refused without reading it", func(t *testing.T, k *keys) string {
			p := filepath.Join(k.dir, "loose")
			writeFile(t, p, []byte(marker), 0o644)
			return p
		}, "is accessible by group or other (mode 0644)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := genKeys(t)
			keyPath := tt.setup(t, k)
			text := edit(t, k.base(), "private_key_file: "+k.keyFile, "private_key_file: "+keyPath)
			cfg, err := Load(writeConfig(t, k.dir, text))
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if cfg.Tenants[0].Identities["lab-look"].PrivateKeyFile != keyPath {
					t.Fatal("key path not kept")
				}
				return
			}
			wantProblem(t, err, "tenants[0](default).identities.lab-look.private_key_file: "+tt.want)
			if strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), "BEGIN") {
				t.Fatalf("error carries key file content: %v", err)
			}
		})
	}
}
