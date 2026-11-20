package mcpserver

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var guidePath = filepath.Join("..", "..", "docs", "operator-guide.md")

// guideBlock returns the text between <!-- name:begin --> and <!-- name:end -->
// in the operator guide.
func guideBlock(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(guidePath) //nolint:gosec // G304: a fixed repository path
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(b), "<!-- "+name+":begin -->")
	if !ok {
		t.Fatalf("no %s:begin marker in the operator guide", name)
	}
	body, _, ok := strings.Cut(rest, "<!-- "+name+":end -->")
	if !ok {
		t.Fatalf("no %s:end marker in the operator guide", name)
	}
	return body
}

// firstCells returns the first cells of a table's rows with backticks removed.
func firstCells(block string) []string {
	var out []string
	for _, l := range strings.Split(block, "\n") {
		if !strings.HasPrefix(l, "| `") {
			continue
		}
		cell, _, _ := strings.Cut(strings.TrimPrefix(l, "| "), " |")
		out = append(out, strings.Trim(strings.TrimSpace(cell), "`"))
	}
	sort.Strings(out)
	return out
}

func sameList(a, b []string) bool {
	a, b = append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	return strings.Join(a, ",") == strings.Join(b, ",")
}

// TestGuideToolCodes: the operator guide's troubleshooting table lists exactly
// the ten tool error codes of Codes.
func TestGuideToolCodes(t *testing.T) {
	got := firstCells(guideBlock(t, "tool-codes"))
	if len(Codes) != 10 || !sameList(got, Codes) {
		t.Errorf("the guide lists %v, the code has %v", got, Codes)
	}
}

// TestGuideTools: the guide's tool table lists exactly the eight tools.
func TestGuideTools(t *testing.T) {
	var want []string
	for _, d := range toolDefs() {
		want = append(want, d.name)
	}
	got := firstCells(guideBlock(t, "tools"))
	if len(want) != 8 || !sameList(got, want) {
		t.Errorf("the guide lists %v, the code has %v", got, want)
	}
}

// TestGuideHTTPBodies: every fixed JSON error body the gateway or the guard
// can send is in the guide's HTTP status table.
func TestGuideHTTPBodies(t *testing.T) {
	body := regexp.MustCompile("`(\\{\"error\":\"[^\"]+\"\\})`")
	block := guideBlock(t, "http-statuses")
	inGuide := map[string]bool{}
	for _, m := range body.FindAllStringSubmatch(block, -1) {
		inGuide[m[1]] = true
	}
	inCode := map[string]bool{}
	lit := regexp.MustCompile("[`\"](\\{\\\\?\"error\\\\?\":\\\\?\"[^\"\\\\]+\\\\?\"\\})[`\"]")
	for _, f := range []string{"gateway.go", filepath.Join("..", "auth", "middleware.go")} {
		b, err := os.ReadFile(f) //nolint:gosec // G304: fixed repository paths
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range lit.FindAllStringSubmatch(string(b), -1) {
			inCode[strings.ReplaceAll(m[1], `\"`, `"`)] = true
		}
	}
	if len(inCode) < 8 {
		t.Fatalf("read only %d error bodies from the code: %v", len(inCode), inCode)
	}
	for b := range inCode {
		if !inGuide[b] {
			t.Errorf("the body %s is sent by the gateway and is not in the guide", b)
		}
	}
	for b := range inGuide {
		if !inCode[b] {
			t.Errorf("the guide shows the body %s, which the code does not send", b)
		}
	}
}
