package mcpserver

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/guidedoc"
)

// TestGuideToolCodes: the operator guide's troubleshooting table lists exactly
// the ten tool error codes of Codes.
func TestGuideToolCodes(t *testing.T) {
	got := guidedoc.FirstCells(guidedoc.Block(t, "tool-codes"))
	if diff := guidedoc.Diff(Codes, got); len(Codes) != 10 || diff != "" {
		t.Errorf("the guide lists %v, the code has %v: %s", got, Codes, diff)
	}
}

// TestGuideTools: the guide's tool table lists exactly the eight tools.
func TestGuideTools(t *testing.T) {
	var want []string
	for _, d := range toolDefs() {
		want = append(want, d.name)
	}
	got := guidedoc.FirstCells(guidedoc.Block(t, "tools"))
	if diff := guidedoc.Diff(want, got); len(want) != 8 || diff != "" {
		t.Errorf("the guide lists %v, the code has %v: %s", got, want, diff)
	}
}

// TestGuideHTTPBodies: every fixed JSON error body the gateway or the guard
// can send is in the guide's HTTP status table.
func TestGuideHTTPBodies(t *testing.T) {
	body := regexp.MustCompile("`(\\{\"error\":\"[^\"]+\"\\})`")
	block := guidedoc.Block(t, "http-statuses")
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
