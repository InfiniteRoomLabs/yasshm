package deprule

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// specPath is the design spec, relative to this package.
var specPath = filepath.Join("..", "..", "docs", "superpowers", "specs", "2026-11-18-yasshm-design.md")

// The markers around the two tables of spec section 6, and the paragraphs
// they answer.
const (
	tableBegin       = "<!-- guarantees:begin -->"
	tableEnd         = "<!-- guarantees:end -->"
	guaranteesLead   = "**Guarantees v0.1 makes, each with a test:**"
	notGuaranteedLed = "**Not guaranteed, and documented as such:**"
)

var (
	// testRef is "`<package dir>` `<TestName>`".
	testRef = regexp.MustCompile("`((?:internal|test|cmd)/[a-z0-9/]+)` `(Test[A-Za-z0-9_]+)`")
	// phraseRef is "`<file>` says \"<phrase>\"".
	phraseRef = regexp.MustCompile("`([A-Za-z0-9_./-]+\\.(?:md|go))` says \"([^\"]+)\"")
)

// tableRow is one row of a section 6 table.
type tableRow struct {
	table  string // "Guarantee" or "Not guaranteed": the first header cell
	claim  string // the first cell
	proofs string // the second cell
}

// parseTables returns the rows of the tables between the markers in text.
func parseTables(text string) ([]tableRow, error) {
	_, rest, ok := strings.Cut(text, tableBegin)
	if !ok {
		return nil, fmt.Errorf("no %s marker", tableBegin)
	}
	body, _, ok := strings.Cut(rest, tableEnd)
	if !ok {
		return nil, fmt.Errorf("no %s marker", tableEnd)
	}
	var rows []tableRow
	header := ""
	for _, l := range strings.Split(body, "\n") {
		if !strings.HasPrefix(l, "| ") {
			if strings.TrimSpace(l) == "" {
				header = ""
			}
			continue
		}
		cells := strings.Split(strings.Trim(l, "|"), " | ")
		if len(cells) != 2 {
			return nil, fmt.Errorf("row %q does not have two cells", l)
		}
		claim, proofs := strings.TrimSpace(cells[0]), strings.TrimSpace(cells[1])
		if header == "" {
			header = claim
			continue
		}
		rows = append(rows, tableRow{table: header, claim: claim, proofs: proofs})
	}
	return rows, nil
}

// testFuncs returns the names of the top-level functions in the _test.go
// files of dir.
func testFuncs(dir string) (map[string]bool, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	fset := token.NewFileSet()
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
				out[fd.Name.Name] = true
			}
		}
	}
	return out, nil
}

// checkRow returns what is wrong with a row: a named test that does not
// exist, a quoted phrase missing from its file, or no evidence at all.
func checkRow(root string, r tableRow, funcs map[string]map[string]bool) []string {
	var bad []string
	tests := testRef.FindAllStringSubmatch(r.proofs, -1)
	phrases := phraseRef.FindAllStringSubmatch(r.proofs, -1)
	if r.table == "Guarantee" && len(tests) == 0 {
		bad = append(bad, fmt.Sprintf("guarantee %q names no test", r.claim))
	}
	if r.table != "Guarantee" && len(phrases) == 0 {
		bad = append(bad, fmt.Sprintf("%q names no file that says so", r.claim))
	}
	for _, m := range tests {
		pkg, name := m[1], m[2]
		fs, ok := funcs[pkg]
		if !ok {
			var err error
			if fs, err = testFuncs(filepath.Join(root, pkg)); err != nil {
				bad = append(bad, fmt.Sprintf("%q: package %s: %v", r.claim, pkg, err))
				continue
			}
			funcs[pkg] = fs
		}
		if !fs[name] {
			bad = append(bad, fmt.Sprintf("%q: %s has no test %s", r.claim, pkg, name))
		}
	}
	for _, m := range phrases {
		b, err := os.ReadFile(filepath.Join(root, m[1])) //nolint:gosec // G304: a repository file the spec names
		if err != nil || !strings.Contains(string(b), m[2]) {
			bad = append(bad, fmt.Sprintf("%q: %s does not say %q (%v)", r.claim, m[1], m[2], err))
		}
	}
	return bad
}

// paragraph returns the rest of the line that starts with lead.
func paragraph(text, lead string) string {
	for _, l := range strings.Split(text, "\n") {
		if p, ok := strings.CutPrefix(l, lead); ok {
			return strings.TrimSpace(p)
		}
	}
	return ""
}

// TestSecurityModelTable keeps the section 6 tables true: every guarantee
// sentence has a row, every row names evidence, every named test function
// exists in its package and every quoted phrase is in its file.
func TestSecurityModelTable(t *testing.T) {
	b, err := os.ReadFile(specPath) //nolint:gosec // G304: the design spec at a fixed repository path
	if err != nil {
		t.Fatal(err)
	}
	spec := string(b)
	rows, err := parseTables(spec)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join("..", "..")
	funcs := map[string]map[string]bool{}
	t.Run("happy/every row names evidence that exists", func(t *testing.T) {
		for _, r := range rows {
			for _, msg := range checkRow(root, r, funcs) {
				t.Error(msg)
			}
		}
	})
	t.Run("happy/every guarantee sentence has a row", func(t *testing.T) {
		para := paragraph(spec, guaranteesLead)
		sentences := strings.Split(strings.TrimSuffix(para, "."), ". ")
		if len(sentences) < 7 {
			t.Fatalf("read %d guarantee sentences from %q", len(sentences), para)
		}
		for _, s := range sentences {
			found := false
			for _, r := range rows {
				found = found || (r.table == "Guarantee" && strings.EqualFold(r.claim, s))
			}
			if !found {
				t.Errorf("guarantee %q has no row", s)
			}
		}
	})
	t.Run("happy/every not-guaranteed row quotes section 6", func(t *testing.T) {
		para := paragraph(spec, notGuaranteedLed)
		n := 0
		for _, r := range rows {
			if r.table == "Not guaranteed" {
				n++
				if !strings.Contains(strings.ToLower(para), strings.ToLower(r.claim)) {
					t.Errorf("%q is not in the not-guaranteed paragraph", r.claim)
				}
			}
		}
		if n < 5 {
			t.Fatalf("%d not-guaranteed rows", n)
		}
	})
	t.Run("sad/a missing test and a missing phrase are flagged", func(t *testing.T) {
		bad := checkRow(root, tableRow{table: "Guarantee", claim: "x", proofs: "`internal/deprule` `TestNoSuchTest`"}, funcs)
		if len(bad) != 1 || !strings.Contains(bad[0], "TestNoSuchTest") {
			t.Fatalf("flagged %v", bad)
		}
		bad = checkRow(root, tableRow{table: "Not guaranteed", claim: "y", proofs: "`README.md` says \"no such phrase anywhere\""}, funcs)
		if len(bad) != 1 {
			t.Fatalf("flagged %v", bad)
		}
		if bad = checkRow(root, tableRow{table: "Guarantee", claim: "z", proofs: "prose only"}, funcs); len(bad) != 1 {
			t.Fatalf("flagged %v", bad)
		}
	})
	t.Run("raging/a table without markers is refused", func(t *testing.T) {
		if _, err := parseTables("| Guarantee | Proved by |\n"); err == nil {
			t.Fatal("parsed a table without markers")
		}
		if _, err := parseTables(tableBegin + "\n| a | b | c |\n" + tableEnd); err == nil {
			t.Fatal("parsed a row with three cells")
		}
	})
}

// TestSecurityMDMatchesSpec keeps SECURITY.md's two tables (between the same
// markers) equal to spec section 6's, row for row, so the threat model an
// outsider reads is the one the tests keep true. SECURITY.md also says the
// not-guaranteed sentence of the spec in its own paragraph.
func TestSecurityMDMatchesSpec(t *testing.T) {
	read := func(p string) string {
		b, err := os.ReadFile(p) //nolint:gosec // G304: fixed repository paths
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	want, err := parseTables(read(specPath))
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseTables(read(filepath.Join("..", "..", "SECURITY.md")))
	if err != nil {
		t.Fatal(err)
	}
	if len(want) < 13 {
		t.Fatalf("the spec has %d rows", len(want))
	}
	if len(got) != len(want) {
		t.Fatalf("SECURITY.md has %d rows, the spec %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d differs.\nSECURITY.md: %+v\nspec:        %+v", i+1, got[i], want[i])
		}
	}
}
