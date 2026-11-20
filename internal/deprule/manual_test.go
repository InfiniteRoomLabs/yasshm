package deprule

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var manualDir = filepath.Join("..", "..", "test", "manual")

// manualHeaderBullets are the five required bullets of the agent-ops
// manual-test header, in order.
var manualHeaderBullets = []string{"**Runner:**", "**Placeholders:**", "**Live budget:**", "**Automate when:**", "**Preconditions:**"}

// runValues are angle-bracket words that name a value of the run or a scratch
// directory, not a placeholder resolved from the environment.
var runValues = map[string]bool{"scratch": true, "command-id": true, "next-offset": true}

var (
	mtName      = regexp.MustCompile(`^MT-(\d{3})-([a-z0-9-]+)\.md$`)
	angleWord   = regexp.MustCompile(`<([a-z][a-z0-9-]*)>`)
	stepHeading = regexp.MustCompile(`(?m)^### (\d+)\. (.+)$`)
	liveStep    = regexp.MustCompile(`\(live (\d+)`)
	testToken   = regexp.MustCompile(`\bTest[A-Za-z0-9_]+\b`)
	resultKeys  = "ts= test=MT-%s repo= result= version= build= runner= live= failed="
	forbidden   = []string{".skip(", ".only(", ".todo(", "eslint-disable", "@ts-ignore"}
)

// allTestNames returns every top-level test function name in the repository.
func allTestNames(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	re := regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`)
	err := filepath.WalkDir(repoRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == "dist") {
			return filepath.SkipDir
		}
		if strings.HasSuffix(p, "_test.go") {
			b, rerr := os.ReadFile(p) //nolint:gosec // G304: repository files
			if rerr != nil {
				return rerr
			}
			for _, m := range re.FindAllStringSubmatch(string(b), -1) {
				out[m[1]] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// joinMarkers are phrases that, outside a code span, join two actions in one
// Do line. This is a heuristic: it catches the common forms ("A, then B",
// "A and then B", "A; B", "A. Then B") and cannot know in general that a
// sentence holds two actions, so the review of a new manual test still reads
// every Do line for "and".
var joinMarkers = []string{" and then ", ", then ", "; ", ". Then "}

var codeSpanText = regexp.MustCompile("`[^`]*`")

// joinedActions returns the first join marker found in the Do line of a step
// block (code spans removed), or "".
func joinedActions(block string) string {
	for _, l := range strings.Split(block, "\n") {
		if !strings.HasPrefix(l, "Do (") {
			continue
		}
		plain := codeSpanText.ReplaceAllString(l, "")
		for _, m := range joinMarkers {
			if strings.Contains(plain, m) {
				return strings.TrimSpace(m)
			}
		}
	}
	return ""
}

// manualProblems returns what is wrong with one manual-test script, in the
// agent-ops manual-test format.
func manualProblems(name, id, text string, tests map[string]bool) []string {
	var bad []string
	add := func(format string, args ...any) {
		bad = append(bad, name+": "+strings.TrimSpace(sprintf(format, args...)))
	}
	lines := strings.Split(text, "\n")
	if !strings.HasPrefix(lines[0], "# MT-"+id+" -- ") {
		add("line 1 is not \"# MT-%s -- <claim>\"", id)
	}
	if len(lines) < 3 || !strings.HasPrefix(lines[2], "Added in ") {
		add("line 3 is not \"Added in <version> for ...\"")
	}
	var bullets []string
	for _, l := range lines {
		if strings.HasPrefix(l, "- **") {
			bullets = append(bullets, l)
		}
	}
	if len(bullets) != len(manualHeaderBullets) {
		add("the header has %d bullets, want %d", len(bullets), len(manualHeaderBullets))
	} else {
		for i, want := range manualHeaderBullets {
			if !strings.HasPrefix(bullets[i], "- "+want) {
				add("header bullet %d is not %s", i+1, want)
			}
		}
	}
	for _, tok := range forbidden {
		if strings.Contains(text, tok) {
			add("holds the token %q", tok)
		}
	}
	if i := nonASCII(text); i > 0 {
		add("line %d is not ASCII", i)
	}
	// Steps: three labelled lines each, Do (Bash|tool|manual).
	_, body, ok := strings.Cut(text, "\n## Steps\n")
	if !ok {
		add("no ## Steps section")
		return bad
	}
	body, result, ok := strings.Cut(body, "\n## Result\n")
	if !ok {
		add("no ## Result section")
		return bad
	}
	heads := stepHeading.FindAllStringSubmatch(body, -1)
	if len(heads) == 0 {
		add("no steps")
	}
	live := 0
	for i, h := range heads {
		if n, _ := strconv.Atoi(h[1]); n != i+1 {
			add("step %s is out of order", h[1])
		}
		if m := liveStep.FindStringSubmatch(h[2]); m != nil {
			live++
			if n, _ := strconv.Atoi(m[1]); n != live {
				add("step %s is (live %d), want (live %d)", h[1], n, live)
			}
		}
	}
	for _, block := range strings.Split(body, "### ")[1:] {
		do := regexp.MustCompile(`(?m)^Do \((Bash|tool|manual)\): `).MatchString(block)
		if !do || !strings.Contains(block, "\nExpect: ") || !strings.Contains(block, "\nRecord: ") {
			add("a step lacks its Do (Bash|tool|manual), Expect or Record line: %q", strings.SplitN(block, "\n", 2)[0])
		}
		if strings.Count(block, "\nDo (") != 1 {
			add("a step has more than one action: %q", strings.SplitN(block, "\n", 2)[0])
		}
		if joined := joinedActions(block); joined != "" {
			add("a Do line joins actions (%s): %q", joined, strings.SplitN(block, "\n", 2)[0])
		}
	}
	// The live budget names the number of live steps.
	var budget string
	for _, b := range bullets {
		if strings.HasPrefix(b, "- **Live budget:**") {
			budget = b
		}
	}
	if m := regexp.MustCompile("so `live=(\\d+)`").FindStringSubmatch(budget); m == nil {
		add("the live budget does not say \"so `live=N`\"")
	} else if n, _ := strconv.Atoi(m[1]); n != live {
		add("the live budget says live=%d and %d steps are live", n, live)
	}
	// Every angle-bracket word is a run value or a placeholder with its variable.
	var placeholders string
	for _, b := range bullets {
		if strings.HasPrefix(b, "- **Placeholders:**") {
			placeholders = b
		}
	}
	for _, m := range angleWord.FindAllStringSubmatch(strings.Join(lines[2:], "\n"), -1) {
		w := m[1]
		if runValues[w] || w == "n" || w == "version" || w == "claim" {
			continue
		}
		want := "`<" + w + ">` -- `MT_" + strings.ToUpper(strings.ReplaceAll(w, "-", "_")) + "`"
		if !strings.Contains(placeholders, want) && !strings.Contains(placeholders, "<"+w+">` -- `MT_") {
			add("<%s> is not declared in the Placeholders bullet as %s", w, want)
		}
	}
	// The result section names the results file and the fixed keys.
	if !strings.Contains(result, "`test/manual/"+strings.TrimSuffix(name, ".md")+".results`") {
		add("the Result section does not name its results file")
	}
	if !strings.Contains(result, sprintf(resultKeys, id)) {
		add("the results line template lacks the fixed keys")
	}
	for _, tok := range testToken.FindAllString(text, -1) {
		if !tests[tok] {
			add("names the test %s, which does not exist", tok)
		}
	}
	return bad
}

func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// resultsLine is a logfmt results line with the fixed keys in order.
var resultsLine = regexp.MustCompile(`^ts=\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ test=MT-(\d{3}) repo=\S+ result=(PASS|FAIL|BLOCKED|SKIP|RETIRED) version=\S+ build=\S+ runner=\S+ live=\d+ failed=(\d+|-)( .*)?$`)

// TestManualTests checks the five manual tests of spec section 7: they exist
// as MT-001 to MT-005, each in the agent-ops manual-test format, each with a
// results file beside it. The build loop never runs them, so a results file is
// empty until the operator runs the test and then holds valid logfmt lines.
func TestManualTests(t *testing.T) {
	ents, err := os.ReadDir(manualDir)
	if err != nil {
		t.Fatal(err)
	}
	tests := allTestNames(t)
	found := map[string]string{}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		m := mtName.FindStringSubmatch(e.Name())
		if m == nil {
			t.Errorf("%s is not named MT-nnn-name.md", e.Name())
			continue
		}
		found[m[1]] = e.Name()
	}
	for _, id := range []string{"001", "002", "003", "004", "005"} {
		name, ok := found[id]
		if !ok {
			t.Errorf("MT-%s is missing", id)
			continue
		}
		b, err := os.ReadFile(filepath.Join(manualDir, name)) //nolint:gosec // G304: repository files
		if err != nil {
			t.Fatal(err)
		}
		for _, msg := range manualProblems(name, id, string(b), tests) {
			t.Error(msg)
		}
		res, err := os.ReadFile(filepath.Join(manualDir, strings.TrimSuffix(name, ".md")+".results")) //nolint:gosec // G304: repository files
		if err != nil {
			t.Errorf("%s has no results file beside it: %v", name, err)
			continue
		}
		for _, l := range strings.Split(strings.TrimRight(string(res), "\n"), "\n") {
			if l == "" {
				continue
			}
			if m := resultsLine.FindStringSubmatch(l); len(m) == 0 || m[1] != id {
				t.Errorf("%s: %q is not a results line of MT-%s", name, l, id)
			}
		}
	}
	if len(found) != 5 {
		t.Errorf("found %d manual tests, want exactly the five of spec section 7", len(found))
	}
	t.Run("happy/TESTING.md lists them as active", func(t *testing.T) {
		if !strings.Contains(readRel(t, repoRoot, "TESTING.md"), "Active: MT-001 to MT-005") {
			t.Error("TESTING.md does not say \"Active: MT-001 to MT-005\"")
		}
	})
	t.Run("sad/a script that breaks the format is flagged", func(t *testing.T) {
		bad := manualProblems("MT-009-x.md", "009", "# MT-009 -- x\n\nAdded in 0.1.\n\n- **Runner:** a\n\n## Steps\n\n### 1. one\nDo (Bash): `x`\nExpect: y\n\n## Result\n\nnothing\n", tests)
		if len(bad) < 4 {
			t.Fatalf("flagged only %v", bad)
		}
		if got := manualProblems("MT-009-x.md", "009", "# MT-009 -- x \u2014\n", tests); len(got) == 0 {
			t.Fatal("a non-ASCII script passed")
		}
		for _, joined := range []string{"Do (manual): Restart it and then check.", "Do (manual): Restart it, then check.", "Do (manual): Restart it; check.", "Do (manual): Restart it. Then check."} {
			if joinedActions("### 1. x\n"+joined+"\nExpect: y\n") == "" {
				t.Errorf("not flagged: %q", joined)
			}
		}
		if joinedActions("### 1. x\nDo (Bash): `a; b`\nExpect: y\n") != "" {
			t.Error("a semicolon inside a code span was flagged")
		}
	})
}
