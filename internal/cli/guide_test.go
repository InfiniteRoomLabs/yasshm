package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestGuideStartUpReasons: every fixed start-up failure reason in serve.go is
// in the operator guide's table of reasons, and every reason the guide lists
// is one serve.go can print.
func TestGuideStartUpReasons(t *testing.T) {
	src, err := os.ReadFile("serve.go")
	if err != nil {
		t.Fatal(err)
	}
	guide, err := os.ReadFile(filepath.Join("..", "..", "docs", "operator-guide.md"))
	if err != nil {
		t.Fatal(err)
	}
	var reasons []string
	for _, m := range regexp.MustCompile(`startError\(("(?:[^"\\]|\\.)*")\)`).FindAllStringSubmatch(string(src), -1) {
		s, err := strconv.Unquote(m[1])
		if err != nil {
			t.Fatal(err)
		}
		reasons = append(reasons, s)
	}
	if len(reasons) != 11 {
		t.Fatalf("read %d start-up reasons from serve.go, want 11", len(reasons))
	}
	for _, r := range reasons {
		if !strings.Contains(string(guide), "| `"+r+"` |") {
			t.Errorf("the start-up reason %q is not in the operator guide's table", r)
		}
	}
	for _, l := range strings.Split(string(guide), "\n") {
		if !strings.HasPrefix(l, "| `the ") && !strings.HasPrefix(l, "| `gateway.started cannot") {
			continue
		}
		cell, _, _ := strings.Cut(strings.TrimPrefix(l, "| `"), "` |")
		found := false
		for _, r := range reasons {
			found = found || r == cell
		}
		if !found {
			t.Errorf("the guide lists the start-up reason %q, which serve.go cannot print", cell)
		}
	}
}
