package deprule

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// repoRoot is the repository root, relative to this package.
var repoRoot = filepath.Join("..", "..")

// trackedFiles returns the files git tracks or would track (not ignored), as
// paths relative to root, existing on disk. The docs checks read these, so a
// file that is new and not yet added is checked too.
func trackedFiles(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.Command("git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	var files []string
	for _, f := range strings.Split(out.String(), "\x00") {
		if f == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, f)); err == nil {
			files = append(files, f)
		}
	}
	sort.Strings(files)
	return files
}

func readRel(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel)) //nolint:gosec // G304: a tracked repository file
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var (
	// linkRef is an inline Markdown link or image: [text](target) or
	// [text](target "title").
	linkRef = regexp.MustCompile(`!?\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	// headingLine is an ATX heading.
	headingLine = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*#*\s*$`)
	// codeSpan is an inline code span (links inside it are text, not links).
	codeSpan = regexp.MustCompile("`[^`]*`")
)

// slug is GitHub's heading anchor: lower case, punctuation dropped except
// hyphens and underscores, spaces become hyphens.
func slug(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case r == ' ':
			b.WriteByte('-')
		case r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z'):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// anchors returns the anchors of a Markdown file's headings, with GitHub's
// -1, -2 suffix for a repeated heading.
func anchors(text string) map[string]bool {
	out := map[string]bool{}
	seen := map[string]int{}
	fenced := false
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		if m := headingLine.FindStringSubmatch(l); m != nil {
			plain := strings.NewReplacer("`", "", "*", "").Replace(m[1])
			s := slug(plain)
			if n := seen[s]; n > 0 {
				out[fmt.Sprintf("%s-%d", s, n)] = true
			} else {
				out[s] = true
			}
			seen[s]++
		}
	}
	return out
}

// linkProblems returns what is wrong with the relative links of the Markdown
// file rel (a path under root): a target that does not exist, or an anchor
// that no heading of a Markdown target produces.
func linkProblems(t *testing.T, root, rel string) []string {
	t.Helper()
	text := readRel(t, root, rel)
	var bad []string
	fenced := false
	for i, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		for _, m := range linkRef.FindAllStringSubmatch(codeSpan.ReplaceAllString(l, ""), -1) {
			target := m[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			pathPart, frag, hasFrag := strings.Cut(target, "#")
			pathPart, _, _ = strings.Cut(pathPart, "?")
			dest := rel
			if pathPart != "" {
				dest = filepath.Join(filepath.Dir(rel), pathPart)
				if _, err := os.Stat(filepath.Join(root, dest)); err != nil {
					bad = append(bad, fmt.Sprintf("%s:%d: the link %q points at %s, which does not exist", rel, i+1, target, dest))
					continue
				}
			}
			if hasFrag && frag != "" && strings.HasSuffix(dest, ".md") {
				if !anchors(readRel(t, root, dest))[frag] {
					bad = append(bad, fmt.Sprintf("%s:%d: the link %q names an anchor that %s has no heading for", rel, i+1, target, dest))
				}
			}
		}
	}
	return bad
}

// TestDocsLinks: every relative link in every tracked Markdown file resolves,
// and so does its anchor when the target is Markdown.
func TestDocsLinks(t *testing.T) {
	n := 0
	for _, f := range trackedFiles(t, repoRoot) {
		if !strings.HasSuffix(f, ".md") {
			continue
		}
		n++
		for _, msg := range linkProblems(t, repoRoot, f) {
			t.Error(msg)
		}
	}
	if n < 5 {
		t.Fatalf("checked only %d Markdown files", n)
	}
	t.Run("sad/a broken link and a broken anchor fail", func(t *testing.T) {
		dir := t.TempDir()
		write := func(name, text string) {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		write("a.md", "# A\n\n## Some Heading\n\nSee [b](b.md), [gone](missing.md), [ok](#some-heading), [bad](#nope), [x](b.md#there), [y](b.md#absent), `[code](nowhere.md)` and [web](https://example.net/x).\n\n```\n[fenced](nowhere.md)\n```\n")
		write("b.md", "# B\n\n## There\n")
		got := linkProblems(t, dir, "a.md")
		if len(got) != 3 {
			t.Fatalf("want 3 problems (missing.md, #nope, b.md#absent), got %d: %v", len(got), got)
		}
	})
}

// docExts are the tracked file kinds that must be ASCII.
var docExts = []string{".md", ".yaml", ".yml", ".service", ".sh", ".toml"}

func hasExt(name string, exts []string) bool {
	for _, e := range exts {
		if strings.HasSuffix(name, e) {
			return true
		}
	}
	return false
}

// nonASCII returns the 1-based line of the first byte outside printable ASCII
// (tab and newline allowed), or 0.
func nonASCII(text string) int {
	line := 1
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c == '\n':
			line++
		case c == '\t' || c == '\r' || (c >= 32 && c < 127):
		default:
			return line
		}
	}
	return 0
}

// TestDocsASCII: every tracked .md, .yaml, .yml, .service, .sh and .toml file
// is ASCII (CLAUDE.md: no smart quotes, em dashes or arrows).
func TestDocsASCII(t *testing.T) {
	for _, f := range trackedFiles(t, repoRoot) {
		if !hasExt(f, docExts) {
			continue
		}
		if line := nonASCII(readRel(t, repoRoot, f)); line > 0 {
			t.Errorf("%s:%d is not ASCII", f, line)
		}
	}
	if nonASCII("a\nb — c") != 2 || nonASCII("plain\ttext\n") != 0 {
		t.Fatal("nonASCII is wrong")
	}
}

// placeholder is the template-placeholder pattern of CLAUDE.md's gotcha. It is
// spelled in two pieces so this file does not hold a match for itself.
var placeholder = regexp.MustCompile("<(" + "project|one-line-description|toolchain|gate-steps|repo|slug" + ")>")

// TestDocsPlaceholders: the template-placeholder grep prints nothing over the
// tracked files.
func TestDocsPlaceholders(t *testing.T) {
	for _, f := range trackedFiles(t, repoRoot) {
		b, err := os.ReadFile(filepath.Join(repoRoot, f)) //nolint:gosec // G304: a tracked repository file
		if err != nil || bytes.IndexByte(b, 0) >= 0 {
			continue
		}
		for i, l := range strings.Split(string(b), "\n") {
			if placeholder.MatchString(l) {
				t.Errorf("%s:%d holds a template placeholder", f, i+1)
			}
		}
	}
	if !placeholder.MatchString("<"+"repo"+">") || placeholder.MatchString("<hostname>") {
		t.Fatal("the placeholder pattern is wrong")
	}
}

// wrapLimit is the line length under which a line looks hand-wrapped.
const wrapLimit = 100

// wrapped returns the 1-based first lines of the paragraphs in text that look
// hard-wrapped: more than three consecutive plain-prose lines, each shorter
// than wrapLimit. Headings, tables, lists, block quotes, code and indented
// lines are not paragraphs.
func wrapped(text string) []int {
	var out []int
	fenced := false
	run, start := 0, 0
	flush := func() {
		if run > 3 {
			out = append(out, start)
		}
		run = 0
	}
	for i, l := range strings.Split(text, "\n") {
		trim := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(trim, "```"):
			flush()
			fenced = !fenced
		case fenced:
		case trim == "", strings.HasPrefix(l, " "), strings.HasPrefix(l, "\t"), strings.HasPrefix(trim, "#"),
			strings.HasPrefix(trim, "|"), strings.HasPrefix(trim, ">"), strings.HasPrefix(trim, "- "),
			strings.HasPrefix(trim, "* "), strings.HasPrefix(trim, "<!--"), regexp.MustCompile(`^\d+\. `).MatchString(trim):
			flush()
		case len(l) >= wrapLimit:
			flush()
		default:
			if run == 0 {
				start = i + 1
			}
			run++
		}
	}
	flush()
	return out
}

// TestDocsNotWrapped: no prose paragraph of a Markdown file is hard-wrapped
// (CLAUDE.md: docs are never hard-wrapped).
func TestDocsNotWrapped(t *testing.T) {
	for _, f := range trackedFiles(t, repoRoot) {
		if !strings.HasSuffix(f, ".md") || strings.HasPrefix(f, "docs/phases/") {
			continue
		}
		for _, line := range wrapped(readRel(t, repoRoot, f)) {
			t.Errorf("%s:%d starts a paragraph that looks hard-wrapped (more than three short lines in a row)", f, line)
		}
	}
	long := strings.Repeat("word ", 30)
	if got := wrapped("a b\nc d\ne f\ng h\n\n" + long + "\nx\ny\nz\n"); len(got) != 1 || got[0] != 1 {
		t.Fatalf("wrapped found %v", got)
	}
	if got := wrapped("- a\n- b\n- c\n- d\n- e\n\n```\na\nb\nc\nd\n```\n| a |\n| b |\n| c |\n| d |\n"); len(got) != 0 {
		t.Fatalf("wrapped flagged non-prose: %v", got)
	}
}
