// Package guidedoc reads the marked tables of docs/operator-guide.md for the
// tests that keep the guide equal to the code. A table sits between the lines
// <!-- name:begin --> and <!-- name:end -->.
package guidedoc

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Path is the operator guide, relative to a package directory two levels
// below the repository root (internal/<package>), where its tests run.
var Path = filepath.Join("..", "..", "docs", "operator-guide.md")

// Block returns the text between the markers <!-- name:begin --> and
// <!-- name:end --> in the operator guide; it fails the test when either is
// missing.
func Block(t testing.TB, name string) string {
	t.Helper()
	b, err := os.ReadFile(Path) //nolint:gosec // G304: a fixed repository path
	if err != nil {
		t.Fatal(err)
	}
	return Cut(t, string(b), name)
}

// Cut is Block over text already in memory.
func Cut(t testing.TB, text, name string) string {
	t.Helper()
	_, rest, ok := strings.Cut(text, "<!-- "+name+":begin -->")
	if !ok {
		t.Fatalf("the operator guide has no %s:begin marker", name)
	}
	body, _, ok := strings.Cut(rest, "<!-- "+name+":end -->")
	if !ok {
		t.Fatalf("the operator guide has no %s:end marker", name)
	}
	return body
}

// FirstCells returns the first cell of each table row of block that starts
// with a code span, with the backticks removed, in document order. The
// header and separator rows are skipped.
func FirstCells(block string) []string {
	var out []string
	for _, l := range strings.Split(block, "\n") {
		if !strings.HasPrefix(l, "| `") {
			continue
		}
		cell, _, _ := strings.Cut(strings.TrimPrefix(l, "| "), " |")
		out = append(out, strings.Trim(strings.TrimSpace(cell), "`"))
	}
	return out
}

// Diff describes how two lists differ as sets, or returns "".
func Diff(want, got []string) string {
	has := func(list []string, s string) bool {
		for _, x := range list {
			if x == s {
				return true
			}
		}
		return false
	}
	var missing, extra []string
	for _, k := range want {
		if !has(got, k) {
			missing = append(missing, k)
		}
	}
	for _, k := range got {
		if !has(want, k) {
			extra = append(extra, k)
		}
	}
	if len(missing) == 0 && len(extra) == 0 {
		return ""
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return "missing from the guide: " + strings.Join(missing, ", ") + "; not in the code: " + strings.Join(extra, ", ")
}
