package deprule

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const module = "github.com/InfiniteRoomLabs/yasshm"

// allowed lists, per package under internal/, the packages of this module
// its non-test files may import (spec section 4). A package missing from this
// map may import anything; add it here when its phase builds it.
var allowed = map[string][]string{
	"tenant": {},
}

// testAllowed is added to allowed for _test.go files: test support only.
var testAllowed = []string{"internal/tenant", "internal/testkit"}

// moduleImports returns the in-module imports of the .go files in dir,
// split into those of non-test files and those of test files.
func moduleImports(t *testing.T, dir string) (prod, test map[string][]string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	prod, test = map[string][]string{}, map[string][]string{}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if p != module && !strings.HasPrefix(p, module+"/") {
				continue
			}
			rel := strings.TrimPrefix(strings.TrimPrefix(p, module), "/")
			if strings.HasSuffix(e.Name(), "_test.go") {
				test[rel] = append(test[rel], e.Name())
			} else {
				prod[rel] = append(prod[rel], e.Name())
			}
		}
	}
	return prod, test
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s || strings.HasPrefix(s, x+"/") {
			return true
		}
	}
	return false
}

func TestDependencyRule(t *testing.T) {
	names := make([]string, 0, len(allowed))
	for n := range allowed {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run("happy/"+name, func(t *testing.T) {
			dir := filepath.Join("..", name)
			if _, err := os.Stat(dir); err != nil {
				t.Fatalf("package internal/%s is in the rule but not on disk: %v", name, err)
			}
			prod, test := moduleImports(t, dir)
			for imp, files := range prod {
				if !contains(allowed[name], imp) {
					t.Errorf("internal/%s imports %s (in %v); the dependency rule allows only %v", name, imp, files, allowed[name])
				}
			}
			for imp, files := range test {
				if !contains(allowed[name], imp) && !contains(testAllowed, imp) {
					t.Errorf("tests of internal/%s import %s (in %v); allowed: %v", name, imp, files, append(allowed[name], testAllowed...))
				}
			}
		})
	}
	t.Run("sad/the checker flags a forbidden import", func(t *testing.T) {
		dir := t.TempDir()
		src := "package x\n\nimport _ \"" + module + "/internal/config\"\n"
		if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		prod, _ := moduleImports(t, dir)
		if _, ok := prod["internal/config"]; !ok || contains(allowed["policy"], "internal/config") {
			t.Fatalf("policy -> config was not flagged: %v", prod)
		}
	})
	t.Run("edge/prefix match does not admit a sibling", func(t *testing.T) {
		if contains([]string{"internal/tenant"}, "internal/tenantx") {
			t.Fatal("internal/tenantx matched internal/tenant")
		}
		if !contains([]string{"internal/testkit"}, "internal/testkit/sshtest") {
			t.Fatal("a testkit subpackage did not match")
		}
	})
}
