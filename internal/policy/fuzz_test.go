package policy

import (
	"path"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzCommandGuardRail: the command guard rail never panics, never allows a
// command with a NUL byte or invalid UTF-8, and a denied command stays
// denied when surrounded by white space.
func FuzzCommandGuardRail(f *testing.F) {
	for _, c := range commandCases {
		for _, s := range c.match {
			f.Add(s, uint8(1))
		}
		for _, s := range c.noMatch {
			f.Add(s, uint8(2))
		}
	}
	f.Add("ls\x00", uint8(0))
	f.Add("\xff", uint8(3))
	f.Add("", uint8(4))
	f.Fuzz(func(t *testing.T, cmd string, pad uint8) {
		r := &tenantRules{maxCommandBytes: 1 << 16}
		d := r.checkCommand(cmd)
		if d.Allow && (strings.IndexByte(cmd, 0) >= 0 || !utf8.ValidString(cmd)) {
			t.Fatalf("allowed %q", cmd)
		}
		if d.Allow != (d.Code == "") || d.Allow != (d.Rule == "") {
			t.Fatalf("inconsistent decision %+v", d)
		}
		if d.Allow {
			return
		}
		ws := []string{" ", "\t", "\n", " \t\r\n ", "\v\f"}[int(pad)%5]
		wrapped := ws + cmd + ws
		if w := r.checkCommand(wrapped); w.Allow {
			t.Fatalf("%q is denied by %s but %q is allowed", cmd, d.Rule, wrapped)
		}
	})
}

// FuzzPathGuardRail: the path guard rail never panics, never allows a path
// that is not absolute (or holds NUL or invalid UTF-8), and decides on the
// cleaned path, so a path and its cleaned form get the same decision.
func FuzzPathGuardRail(f *testing.F) {
	for _, c := range pathCases {
		for _, p := range c.match {
			f.Add(p)
		}
		for _, p := range c.noMatch {
			f.Add(p)
		}
	}
	for _, p := range []string{"/etc/./shadow", "/etc/x/../shadow", "//etc//shadow/", "etc/shadow", "", "/", "/\x00", "/\xff", "/../.."} {
		f.Add(p)
	}
	f.Fuzz(func(t *testing.T, p string) {
		r := &tenantRules{maxCommandBytes: 1, paths: []pathRule{{Name: "op", Globs: []string{"/srv/*/secret", "*.kdbx"}}}}
		d := r.checkPath(p)
		if d.Allow && (!strings.HasPrefix(p, "/") || strings.IndexByte(p, 0) >= 0 || !utf8.ValidString(p)) {
			t.Fatalf("allowed %q", p)
		}
		c := r.checkPath(path.Clean(p))
		// Cleaning never turns a denied path into an allowed one: a denied
		// clean form means the path is denied too. The reverse need not
		// hold: bytes that cleaning drops ("/\xd1/..") are refused first.
		if !c.Allow && d.Allow {
			t.Fatalf("%q allowed but its clean form %q is denied by %s", p, path.Clean(p), c.Rule)
		}
		if !strings.HasPrefix(p, "/") || len(p) > MaxPathBytes || strings.IndexByte(p, 0) >= 0 || !utf8.ValidString(p) {
			return
		}
		// A well-formed absolute path and its cleaned form get the same
		// decision.
		if c != d {
			t.Fatalf("%q -> %+v but its clean form %q -> %+v", p, d, path.Clean(p), c)
		}
	})
}

func TestCleanedPathDecisions(t *testing.T) {
	for _, p := range []string{"/etc/./shadow", "/etc/x/../shadow", "/etc//shadow", "/./etc/shadow/."} {
		t.Run("corner/"+p+" is decided like /etc/shadow", func(t *testing.T) {
			if d := checkPathBuiltin(p); d.Allow || d.Rule != "shadow" {
				t.Fatalf("got %+v", d)
			}
		})
	}
}
