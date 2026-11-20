package audit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

var guidePath = filepath.Join("..", "..", "docs", "operator-guide.md")

// guideBlock returns the text between the markers <!-- name:begin --> and
// <!-- name:end --> in the operator guide.
func guideBlock(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(guidePath) //nolint:gosec // G304: a fixed repository path
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(b), "<!-- "+name+":begin -->")
	if !ok {
		t.Fatalf("the operator guide has no %s:begin marker", name)
	}
	body, _, ok := strings.Cut(rest, "<!-- "+name+":end -->")
	if !ok {
		t.Fatalf("the operator guide has no %s:end marker", name)
	}
	return body
}

// guideSamples are the audit events the operator guide shows, one per event
// type (and one more where the shape differs), in the order of the guide.
// Each is written through a Logger with a fixed clock, so the line is exactly
// what a gateway writes.
func guideSamples() []struct {
	name string
	ev   Event
} {
	op := tenant.Principal{Tenant: "default", Kind: tenant.KindUser, Subject: "operator@example.net"}
	stranger := tenant.Principal{Kind: tenant.KindUser, Subject: "stranger@example.net"}
	actor := Actor{RequestID: "9f2c4e1ab07d4c35a1e86b3d5f70c2e4", Principal: op}
	exit0 := 0
	return []struct {
		name string
		ev   Event
	}{
		{"gateway.started", GatewayStarted("0.1.0", "127.0.0.1:8822")},
		{"auth.accepted", AuthAccepted(actor.RequestID, op)},
		{"auth.rejected", AuthRejected("1b7e0d52c9a34f6e8d21a0f4b6c83e97", RejectMissingToken, tenant.Principal{})},
		{"auth.rejected (unmapped principal)", AuthRejected("5d0a93c7e1b24a68b3f9c2e07d14a856", RejectUnmappedPrincipal, stranger)},
		{"auth.rejected (suppressed summary)", AuthRejectionsSuppressed(412, SuppressedUnverified)},
		{"tool.called", ToolCalled(actor, "run_command", "lab-1", ToolArgs{Command: "uptime", WaitSeconds: i64p(30), MergeStderr: boolp(true)})},
		{"policy.denied", PolicyDenied(actor, "run_command", "lab-1", "policy_denied", "sudo")},
		{"tool.failed", ToolFailed(actor, "run_command", "lab-1", "ssh_unreachable")},
		{"command.requested", CommandRequested(actor, "lab-1", "3a9f0c71d2e84b5690a1c7e8f4d25b36", "uptime", 600)},
		{"command.started", CommandStarted(actor, "lab-1", "3a9f0c71d2e84b5690a1c7e8f4d25b36")},
		{"command.finished", CommandFinished(actor, "lab-1", "3a9f0c71d2e84b5690a1c7e8f4d25b36", Finish{Status: "succeeded", ExitCode: &exit0, StdoutBytes: 62, Duration: 840 * time.Millisecond})},
		{"command.finished (timed out)", CommandFinished(actor, "lab-1", "c41e7b09a2d3458f9e60b1d7a38c5f24", Finish{Status: "timed_out", Signal: "TERM", StdoutBytes: 1048576, StderrBytes: 0, Truncated: true, Duration: 600 * time.Second})},
		{"command.kill_requested", CommandKillRequested(actor, "c41e7b09a2d3458f9e60b1d7a38c5f24", "TERM")},
		{"file.downloaded (requested)", FileDownloadRequested(actor, "lab-1", "/etc/hostname", 0, 65536)},
		{"file.downloaded (done)", FileDownloaded(actor, "lab-1", "/etc/hostname", 0, 6)},
		{"file.uploaded (requested)", FileUploadRequested(actor, "lab-1", "/tmp/note.txt", 12, "0644", false)},
		{"file.uploaded (done)", FileUploaded(actor, "lab-1", "/tmp/note.txt", 12)},
		{"gateway.stopped", GatewayStopped(StopSignal)},
	}
}

func i64p(v int64) *int64 { return &v }
func boolp(v bool) *bool  { return &v }

// sampleLines writes the samples through a Logger with a clock that starts at
// 2026-11-20T12:00:00Z and moves one second a line, and returns the lines.
func sampleLines(t *testing.T) []string {
	t.Helper()
	ff := &fakeFile{}
	at := time.Date(2026, 11, 20, 12, 0, 0, 123_000_000, time.UTC)
	l := newLogger(ff, WithClock(func() time.Time { at = at.Add(time.Second); return at }))
	for _, s := range guideSamples() {
		if err := l.Write(s.ev); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
	}
	return strings.Split(strings.TrimRight(ff.buf.String(), "\n"), "\n")
}

// TestGuideAuditSamples keeps the operator guide's sample audit lines equal to
// what the audit package writes: the lines between the audit-samples markers
// must be exactly the lines the constructors above produce, so they cannot
// drift. It also checks that every event name the package defines has a sample.
func TestGuideAuditSamples(t *testing.T) {
	want := sampleLines(t)
	var got []string
	for _, l := range strings.Split(guideBlock(t, "audit-samples"), "\n") {
		if strings.HasPrefix(l, "{") {
			got = append(got, l)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("the guide shows %d sample lines, the audit package writes %d for them; the lines are:\n%s", len(got), len(want), strings.Join(want, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sample %d (%s) differs.\nguide: %s\nwrote: %s", i+1, guideSamples()[i].name, got[i], want[i])
		}
	}
	t.Run("happy/every event name has a sample", func(t *testing.T) {
		have := map[string]bool{}
		for _, l := range want {
			for name := range eventNames(t) {
				if strings.Contains(l, `"event":"`+name+`"`) {
					have[name] = true
				}
			}
		}
		for name := range eventNames(t) {
			if !have[name] {
				t.Errorf("no sample line for the event %s", name)
			}
		}
	})
}

// eventNames returns the values of the Event* constants in event.go.
func eventNames(t *testing.T) map[string]bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "event.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, id := range vs.Names {
			if strings.HasPrefix(id.Name, "Event") && i < len(vs.Values) {
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok {
					s, _ := strconv.Unquote(lit.Value)
					out[s] = true
				}
			}
		}
		return true
	})
	if len(out) != 13 {
		t.Fatalf("read %d event names from event.go, want 13", len(out))
	}
	return out
}

// TestGuideRejectReasons keeps the guide's auth.rejected reason table equal to
// the reasons in event.go (the RejectReason values) plus the summary reason.
func TestGuideRejectReasons(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "event.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{ReasonSuppressed: true}
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if id, ok := cl.Type.(*ast.Ident); ok && id.Name == "RejectReason" && len(cl.Elts) == 1 {
			if lit, ok := cl.Elts[0].(*ast.BasicLit); ok {
				s, _ := strconv.Unquote(lit.Value)
				want[s] = true
			}
		}
		return true
	})
	got := firstCells(guideBlock(t, "reject-reasons"))
	if len(want) < 15 {
		t.Fatalf("read only %d reasons from event.go", len(want))
	}
	if diff := setDiff(want, got); diff != "" {
		t.Errorf("the guide's reason table and event.go differ: %s", diff)
	}
}

// firstCells returns the set of first cells of the table rows in block, each
// with its backticks removed (the header and separator rows are skipped).
func firstCells(block string) map[string]bool {
	out := map[string]bool{}
	for _, l := range strings.Split(block, "\n") {
		if !strings.HasPrefix(l, "| `") {
			continue
		}
		cell, _, _ := strings.Cut(strings.TrimPrefix(l, "| "), " |")
		out[strings.Trim(strings.TrimSpace(cell), "`")] = true
	}
	return out
}

// setDiff describes how two sets differ, or returns "".
func setDiff(want, got map[string]bool) string {
	var missing, extra []string
	for k := range want {
		if !got[k] {
			missing = append(missing, k)
		}
	}
	for k := range got {
		if !want[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) == 0 && len(extra) == 0 {
		return ""
	}
	return "missing from the guide: " + strings.Join(missing, ", ") + "; not in the code: " + strings.Join(extra, ", ")
}

// TestGuideHelpers proves the table readers see what they should.
func TestGuideHelpers(t *testing.T) {
	got := firstCells("| Reason | Meaning |\n|---|---|\n| `a_b` | x |\n| `c` | y |\n")
	if len(got) != 2 || !got["a_b"] || !got["c"] {
		t.Fatalf("firstCells = %v", got)
	}
	if setDiff(map[string]bool{"a": true}, map[string]bool{"a": true}) != "" || setDiff(map[string]bool{"a": true}, nil) == "" {
		t.Fatal("setDiff is wrong")
	}
}
