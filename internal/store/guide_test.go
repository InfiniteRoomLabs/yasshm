package store

import (
	"testing"

	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/guidedoc"
)

// TestGuideStatuses: the operator guide's status table lists exactly the
// statuses the store's transition table knows (every key and every target), so
// a status added to the state machine fails here until the guide lists it.
func TestGuideStatuses(t *testing.T) {
	seen := map[string]bool{}
	var want []string
	add := func(s Status) {
		if !seen[string(s)] {
			seen[string(s)] = true
			want = append(want, string(s))
		}
	}
	for from, tos := range transitions {
		add(from)
		for _, to := range tos {
			add(to)
		}
	}
	if len(want) != 7 {
		t.Fatalf("the transition table names %d statuses: %v", len(want), want)
	}
	got := guidedoc.FirstCells(guidedoc.Block(t, "statuses"))
	if diff := guidedoc.Diff(want, got); diff != "" {
		t.Errorf("the guide's status table and the store differ: %s", diff)
	}
}
