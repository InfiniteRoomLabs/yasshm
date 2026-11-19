package policy

import (
	"path"
	"strings"
)

// pathRule is one entry of a path denylist: a stable name and one or more
// globs. A glob without a slash is matched (path.Match) against the base
// name, wherever the file is. A glob with a slash is matched against the
// whole cleaned path, element by element, and `*` does not cross a slash.
// Two subtree forms extend it: `**` as the whole last element matches the
// directory and everything under it (`/proc/*/root/**`), and `**` as the
// whole first element matches any depth before the rest
// (`**/.aws/credentials`). `**` anywhere else is not allowed (validGlob).
type pathRule struct {
	Name  string
	Globs []string
}

func (r pathRule) matches(clean string) bool {
	for _, g := range r.Globs {
		if globMatch(g, clean) {
			return true
		}
	}
	return false
}

// globMatch matches one glob against a cleaned absolute path.
func globMatch(g, clean string) bool {
	if !strings.Contains(g, "/") {
		ok, err := path.Match(g, path.Base(clean))
		return err == nil && ok
	}
	ge := strings.Split(g, "/")
	pe := strings.Split(clean, "/")
	anyDepth := ge[0] == "**"
	if anyDepth {
		ge = ge[1:]
	}
	subtree := len(ge) > 0 && ge[len(ge)-1] == "**"
	if subtree {
		ge = ge[:len(ge)-1]
	}
	if !anyDepth {
		return matchPrefix(ge, pe, subtree)
	}
	// Any depth: try the rest of the glob at every element of the path.
	for i := range pe {
		if matchPrefix(ge, pe[i:], subtree) {
			return true
		}
	}
	return false
}

// matchPrefix matches the glob elements ge against the path elements pe:
// exactly, or as a prefix when subtree is set.
func matchPrefix(ge, pe []string, subtree bool) bool {
	if len(pe) < len(ge) || (!subtree && len(pe) != len(ge)) {
		return false
	}
	for i, g := range ge {
		if ok, err := path.Match(g, pe[i]); err != nil || !ok {
			return false
		}
	}
	return true
}

// validGlob reports whether g is a glob the matcher accepts: not empty; with
// a slash, it starts with "/" or "**/"; "**" only as the whole first or last
// element of a glob with a slash; every other element valid for path.Match.
// The config loader applies the same rules (config.checkPathGlob).
func validGlob(g string) bool {
	if g == "" {
		return false
	}
	if strings.Contains(g, "/") && !strings.HasPrefix(g, "/") && !strings.HasPrefix(g, "**/") {
		return false
	}
	elems := strings.Split(g, "/")
	for i, e := range elems {
		if e == "**" && len(elems) > 1 && (i == 0 || i == len(elems)-1) {
			continue
		}
		if strings.Contains(e, "**") {
			return false
		}
		if _, err := path.Match(e, ""); err != nil {
			return false
		}
	}
	return true
}

// builtinPaths is the built-in path denylist for file transfer (spec 5.3):
// shadow files, private keys and common secret file names. Every entry has
// must-match and must-not-match cases in paths_test.go, and that test fails
// for an entry without both.
var builtinPaths = []pathRule{
	// Password hashes and their backups. /etc/passwd and /etc/group are
	// public and allowed.
	{Name: "shadow", Globs: []string{"/etc/shadow", "/etc/shadow-", "/etc/gshadow", "/etc/gshadow-", "/var/backups/*shadow*"}},
	// sudo policy: it maps out privilege paths. Reading it is denied even
	// though the look account usually cannot.
	{Name: "sudoers", Globs: []string{"/etc/sudoers", "/etc/sudoers.d/*"}},
	// SSH host private keys. The .pub files beside them are allowed.
	{Name: "ssh-host-key", Globs: []string{"/etc/ssh/ssh_host_*_key"}},
	// SSH user private keys by their default names, in any directory. The
	// .pub files, authorized_keys and known_hosts are allowed.
	{Name: "ssh-private-key", Globs: []string{"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519", "id_ecdsa_sk", "id_ed25519_sk"}},
	// Key and keystore files, in any directory. Most *.pem files are public
	// certificates, so only the private-key names (certbot's privkey*.pem)
	// are listed.
	{Name: "key-file", Globs: []string{"*.key", "*.p12", "*.pfx", "*.jks", "*.keystore", "*.keytab", "privkey*.pem"}},
	// dotenv files, in any directory. `.envrc` and `env.example` are allowed.
	{Name: "env-file", Globs: []string{".env", ".env.*"}},
	// Credential stores of common tools, in any directory.
	{Name: "credential-file", Globs: []string{".netrc", ".pgpass", ".git-credentials", ".my.cnf", "credentials.json", "**/.docker/config.json"}},
	// Cloud CLI credentials at any depth, so nested homes and homes outside
	// /home are covered.
	{Name: "cloud-credentials", Globs: []string{"**/.aws/credentials", "**/.config/gcloud/**", "**/.kube/config"}},
	// Process environments and memory, which hold other programs' secrets.
	{Name: "proc-secrets", Globs: []string{"/proc/*/environ", "/proc/*/mem", "/proc/*/task/*/environ", "/proc/kcore"}},
	// A process's root and working directory links reach the whole file
	// system and would get past every whole-path rule above.
	{Name: "proc-links", Globs: []string{"/proc/*/root/**", "/proc/*/cwd/**", "/proc/*/task/*/root/**", "/proc/*/task/*/cwd/**"}},
}
