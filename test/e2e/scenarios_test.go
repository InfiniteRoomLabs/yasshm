package e2e

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// scenarios maps each end-to-end scenario (spec section 7 and the Phase 6
// additions) to its top-level test. TestScenarioTable holds the table and
// the package to each other, and the gate's e2e step requires every
// TestE2E* to pass, so renaming or deleting a scenario fails the gate.
var scenarios = []struct{ scenario, test string }{
	{"every tool through the fake edge", "TestE2EEveryTool"},
	{"no token, bad token and unmapped principal", "TestE2EAuth"},
	{"a request around the edge, a batch, an unknown path", "TestE2EHTTPSurface"},
	{"/healthz ok and degraded", "TestE2EHealthz"},
	{"a command that outlives wait_seconds, is paged and is killed", "TestE2ELongCommand"},
	{"a command that times out", "TestE2ETimeout"},
	{"output beyond max_output_bytes", "TestE2EOutputLimit"},
	{"a busy rejection", "TestE2EBusy"},
	{"a denylisted command", "TestE2EPolicyDenied"},
	{"a host key mismatch", "TestE2EHostKeyMismatch"},
	{"upload_file denied", "TestE2EUploadDenied"},
	{"an unwritable audit log blocks the action", "TestE2EAuditUnwritable"},
	{"an unwritable audit log at command.requested", "TestE2EAuditUnwritableAtRequested"},
	{"the gateway is killed mid-command and the restarted gateway reports lost", "TestE2ELostAfterKill"},
	{"graceful shutdown", "TestE2EGracefulShutdown"},
	{"config check on valid and invalid files", "TestE2EConfigCheck"},
	{"two tenants: refused by the binary (isolation in-process)", "TestE2ETwoTenantsRefused"},
	{"a second gateway on the same data directory", "TestE2ESecondGateway"},
	{"leak checks around the binary", "TestE2EResourceBounds"},
	{"full disk: an output file that cannot grow", "TestE2EFileSizeLimit"},
	{"SIGHUP is ignored", "TestE2ESIGHUPIgnored"},
}

// TestScenarioTable: every scenario's test exists as a top-level function
// of this package, every TestE2E* function is in the table, and the spec 7
// callout names every one of them.
func TestScenarioTable(t *testing.T) {
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var funcs []string
	fset := token.NewFileSet()
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, e.Name(), nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Fatal(perr)
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && strings.HasPrefix(fd.Name.Name, "TestE2E") {
				funcs = append(funcs, fd.Name.Name)
			}
		}
	}
	spec, err := os.ReadFile(filepath.Join("..", "..", "docs", "superpowers", "specs", "2026-11-18-yasshm-design.md"))
	if err != nil {
		t.Fatal(err)
	}
	var named []string
	for _, s := range scenarios {
		named = append(named, s.test)
		if !slices.Contains(funcs, s.test) {
			t.Errorf("scenario %q: no test %s in the package", s.scenario, s.test)
		}
		if !strings.Contains(string(spec), "`"+s.test+"`") {
			t.Errorf("scenario %q: the spec does not name %s", s.scenario, s.test)
		}
	}
	for _, f := range funcs {
		if !slices.Contains(named, f) {
			t.Errorf("%s is not in the scenario table", f)
		}
	}
	if len(funcs) != len(scenarios) {
		t.Errorf("%d TestE2E functions, %d scenarios", len(funcs), len(scenarios))
	}
}
