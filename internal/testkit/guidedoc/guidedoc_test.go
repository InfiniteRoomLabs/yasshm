package guidedoc

import "testing"

func TestHelpers(t *testing.T) {
	text := "x\n<!-- t:begin -->\n| Reason | Meaning |\n|---|---|\n| `a_b` | x |\n| `c` | y |\n<!-- t:end -->\n"
	got := FirstCells(Cut(t, text, "t"))
	if len(got) != 2 || got[0] != "a_b" || got[1] != "c" {
		t.Fatalf("FirstCells = %v", got)
	}
	if Diff([]string{"a"}, []string{"a"}) != "" || Diff([]string{"a"}, nil) == "" || Diff(nil, []string{"b"}) == "" {
		t.Fatal("Diff is wrong")
	}
}
