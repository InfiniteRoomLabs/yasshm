package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/crypto/ssh"
)

var labelPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// isHostname reports whether s is a DNS host name (letters, digits and
// hyphens in dot-separated labels, 253 bytes at most).
func isHostname(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if !labelPattern.MatchString(label) {
			return false
		}
	}
	return true
}

// isHost reports whether s is a host name or an IP address literal (without
// brackets or a zone).
func isHost(s string) bool {
	return isHostname(s) || net.ParseIP(s) != nil
}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, unicode.IsControl)
}

// checkToken checks an opaque identifier: 1 to max bytes, no white space and
// no control characters.
func checkToken(s string, maxLen int) string {
	if s == "" || len(s) > maxLen || hasControl(s) || strings.ContainsFunc(s, unicode.IsSpace) {
		return fmt.Sprintf("must be 1 to %d characters without spaces or control characters", maxLen)
	}
	return ""
}

// isEmail is a deliberately plain check: ASCII only, one @, a local part
// without spaces or control characters, and a DNS host name after it. ASCII
// keeps matching to ASCII case folding (tenant.PrincipalMatch).
func isEmail(s string) bool {
	if len(s) > 254 || hasControl(s) || strings.ContainsFunc(s, unicode.IsSpace) ||
		strings.ContainsFunc(s, func(r rune) bool { return r > unicode.MaxASCII }) {
		return false
	}
	local, domain, ok := strings.Cut(s, "@")
	return ok && local != "" && !strings.Contains(domain, "@") && isHostname(domain)
}

// checkAbsPath requires an absolute, already clean path.
func checkAbsPath(p string) string {
	if !strings.HasPrefix(p, "/") {
		return "must be an absolute path"
	}
	if path.Clean(p) != p {
		return fmt.Sprintf("must be a clean path (did you mean %q?)", path.Clean(p))
	}
	return ""
}

// checkListen checks server.listen: host:port where the host is a loopback
// IP literal (127.0.0.0/8 or ::1) unless non-loopback addresses are allowed.
// A host name, even localhost, is not accepted as loopback: what it resolves
// to is not the config's to decide. Port 0 asks the kernel for a free port.
func checkListen(listen string, allowNonLoopback bool) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "must be host:port"
	}
	if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		return "port must be between 0 and 65535"
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return ""
	}
	if !allowNonLoopback {
		return "must be a loopback IP address (127.0.0.1 or ::1) unless server.allow_non_loopback is true"
	}
	if host == "" || ip != nil || isHostname(host) {
		return ""
	}
	return "host must be an IP address or a DNS host name"
}

// checkCertsURL checks the JWKS override: an absolute https URL, or http only
// when the host is a loopback IP literal (so tests can point at a local
// issuer).
func checkCertsURL(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || !u.IsAbs() {
		return "must be an absolute URL"
	}
	if u.User != nil || u.Fragment != "" {
		return "must not carry user information or a fragment"
	}
	if u.Scheme == "https" {
		return ""
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme == "http" && ip != nil && ip.IsLoopback() {
		return ""
	}
	return "must be https (http is allowed only for a loopback IP address)"
}

// parseHostKey parses a pinned host key in authorized_keys form and returns
// it normalised to "type base64" (the comment dropped).
func parseHostKey(k string) (norm, msg string) {
	if strings.ContainsAny(k, "\r\n") {
		return "", "must be one line"
	}
	pk, _, opts, rest, err := ssh.ParseAuthorizedKey([]byte(k))
	if err != nil {
		return "", "does not parse as an OpenSSH public key (\"type base64 [comment]\")"
	}
	if len(opts) > 0 {
		return "", "must not carry authorized_keys options"
	}
	if strings.TrimSpace(string(rest)) != "" {
		return "", "must hold exactly one key"
	}
	if _, isCert := pk.(*ssh.Certificate); isCert {
		return "", "SSH certificates are not supported in this version"
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk))), ""
}

// checkCommandPattern checks an operator deny_commands entry: it compiles as
// RE2 and does not match the empty string (such a pattern denies every
// command, which is never what was meant).
func checkCommandPattern(p string) string {
	if p == "" || len(p) > maxPattern {
		return fmt.Sprintf("must be 1 to %d bytes", maxPattern)
	}
	re, err := regexp.Compile(p)
	if err != nil {
		var se *syntax.Error
		if errors.As(err, &se) {
			return fmt.Sprintf("does not compile as an RE2 expression: %s in %q", se.Code, se.Expr)
		}
		return fmt.Sprintf("does not compile as an RE2 expression: %q", p)
	}
	if re.MatchString("") {
		return "matches the empty string, so it would deny every command"
	}
	return ""
}

// checkPathGlob checks an operator deny_paths entry. A glob without a slash
// is matched against the base name. A glob with a slash is matched against
// the whole cleaned path and must start with "/" or "**/". "**" is allowed
// only as the whole first element (any depth before the rest) or the whole
// last element (the directory and everything under it). The policy package
// does the matching; its NewEngine applies the same rules.
func checkPathGlob(g string) string {
	if g == "" || len(g) > maxPattern {
		return fmt.Sprintf("must be 1 to %d bytes", maxPattern)
	}
	if strings.Contains(g, "/") && !strings.HasPrefix(g, "/") && !strings.HasPrefix(g, "**/") {
		return "a glob with a slash must start with / or **/"
	}
	elems := strings.Split(g, "/")
	for i, e := range elems {
		if e == "**" && (i == 0 || i == len(elems)-1) && len(elems) > 1 {
			continue
		}
		if strings.Contains(e, "**") {
			return "** is allowed only as the whole first or last path element"
		}
		if _, err := path.Match(e, ""); err != nil {
			return "is not a valid glob"
		}
	}
	return ""
}
