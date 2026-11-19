package policy

import (
	"path"
	"strings"
)

// pathRule is one entry of a path denylist: a stable name and one or more
// globs (path.Match syntax). A glob that contains a slash is matched against
// the whole cleaned path, and `*` does not cross a slash. A glob without a
// slash is matched against the base name, wherever the file is.
type pathRule struct {
	Name  string
	Globs []string
}

func (r pathRule) matches(clean string) bool {
	base := path.Base(clean)
	for _, g := range r.Globs {
		subject := base
		if strings.Contains(g, "/") {
			subject = clean
		}
		if ok, err := path.Match(g, subject); err == nil && ok {
			return true
		}
	}
	return false
}

// builtinPaths is the built-in path denylist for file transfer (spec 5.3):
// shadow files, private keys and common secret file names. Every entry has
// must-match and must-not-match cases in paths_test.go, and that test fails
// for an entry without both.
var builtinPaths = []pathRule{
	// Password hashes. /etc/passwd and /etc/group are public and allowed.
	{Name: "shadow", Globs: []string{"/etc/shadow", "/etc/shadow-", "/etc/gshadow", "/etc/gshadow-"}},
	// sudo policy: it maps out privilege paths. Reading it is denied even
	// though the look account usually cannot.
	{Name: "sudoers", Globs: []string{"/etc/sudoers", "/etc/sudoers.d/*"}},
	// SSH host private keys. The .pub files beside them are allowed.
	{Name: "ssh-host-key", Globs: []string{"/etc/ssh/ssh_host_*_key"}},
	// SSH user private keys by their default names, in any directory. The
	// .pub files, authorized_keys and known_hosts are allowed.
	{Name: "ssh-private-key", Globs: []string{"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519", "id_ecdsa_sk", "id_ed25519_sk"}},
	// Key and keystore files by extension, in any directory. *.pem is not
	// on the list: most .pem files are public certificates.
	{Name: "key-file", Globs: []string{"*.key", "*.p12", "*.pfx", "*.jks", "*.keystore"}},
	// dotenv files, in any directory. `.envrc` and `env.example` are allowed.
	{Name: "env-file", Globs: []string{".env", ".env.*"}},
	// Credential stores of common tools, in any directory.
	{Name: "credential-file", Globs: []string{".netrc", ".pgpass", ".git-credentials", ".my.cnf", "credentials.json"}},
	// Cloud CLI credentials under a home directory.
	{Name: "cloud-credentials", Globs: []string{"/root/.aws/credentials", "/home/*/.aws/credentials", "/root/.config/gcloud/*", "/home/*/.config/gcloud/*", "/root/.kube/config", "/home/*/.kube/config"}},
	// Process environments and memory, which hold other programs' secrets.
	{Name: "proc-secrets", Globs: []string{"/proc/*/environ", "/proc/*/mem", "/proc/*/task/*/environ", "/proc/kcore"}},
}
