package store

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestGuideStatuses: the operator guide's status table lists exactly the
// statuses the store defines.
func TestGuideStatuses(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "operator-guide.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, rest, _ := strings.Cut(string(b), "<!-- statuses:begin -->")
	body, _, ok := strings.Cut(rest, "<!-- statuses:end -->")
	if !ok {
		t.Fatal("the operator guide has no statuses markers")
	}
	var got []string
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, "| `") {
			cell, _, _ := strings.Cut(strings.TrimPrefix(l, "| "), " |")
			got = append(got, strings.Trim(cell, "`"))
		}
	}
	want := []string{string(StatusRunning), string(StatusSucceeded), string(StatusFailed), string(StatusKilled),
		string(StatusTimedOut), string(StatusLost), string(StatusError)}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the guide lists %v, the store defines %v", got, want)
	}
}
