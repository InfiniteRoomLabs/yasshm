package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// examplePath and unitPath are the shipped files the tests below keep true.
var (
	examplePath = filepath.Join("..", "..", "deploy", "config.example.yaml")
	unitPath    = filepath.Join("..", "..", "deploy", "yasshm.service")
)

const (
	placeholderHostKey = `"ssh-ed25519 REPLACE_WITH_THE_TARGETS_HOST_KEY_LINE_FROM_ssh-keyscan_OR_THE_HOST"`
	placeholderKeyFile = "/etc/yasshm/keys/lab-look_ed25519"
)

func readExample(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(examplePath) //nolint:gosec // G304: a fixed repository path
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestExampleLoads loads deploy/config.example.yaml through the real loader
// after the test puts a generated private key file and a real host key line in
// the two places an operator has to, so the example can never drift from the
// loader. The file as shipped is refused, on those two values and no others.
func TestExampleLoads(t *testing.T) {
	text := readExample(t)
	if !strings.Contains(text, placeholderHostKey) || !strings.Contains(text, placeholderKeyFile) {
		t.Fatal("the example no longer holds the placeholder host key or key file path this test replaces")
	}
	k := genKeys(t)
	t.Run("happy/the example loads with a generated key and host key", func(t *testing.T) {
		filled := strings.NewReplacer(placeholderHostKey, `"`+k.hostKey+`"`, placeholderKeyFile, k.keyFile).Replace(text)
		cfg, err := Load(writeConfig(t, k.dir, filled))
		if err != nil {
			t.Fatalf("the example does not load: %v", err)
		}
		if len(cfg.Tenants) != 1 || len(cfg.Tenants[0].Servers) != 1 {
			t.Fatalf("loaded %d tenants", len(cfg.Tenants))
		}
		if cfg.Tenants[0].Limits != DefaultLimits() {
			t.Errorf("the example's limits are not the defaults: %+v", cfg.Tenants[0].Limits)
		}
	})
	t.Run("sad/the example as shipped is refused on the host key and the key file only", func(t *testing.T) {
		_, err := Load(writeConfig(t, k.dir, text))
		if err == nil {
			t.Fatal("the unedited example loads: a placeholder would reach production")
		}
		got := problems(t, err)
		if len(got) != 2 || !strings.Contains(got[0]+got[1], "host_key") || !strings.Contains(got[0]+got[1], "private_key_file") {
			t.Fatalf("problems: %v", got)
		}
	})
}

// TestExampleLimitComments checks every limit in the example against the
// limits table: the key is there once, at its default, and the comment above
// it names that default and that range. It also checks the cross-field rules
// the example's comment states.
func TestExampleLimitComments(t *testing.T) {
	lines := strings.Split(readExample(t), "\n")
	stated := regexp.MustCompile(`Default (\d+), range (\d+) to (\d+)\.`)
	for _, s := range limitTable {
		key := regexp.MustCompile(`^\s+` + s.key + `: (\d+)$`)
		at := -1
		for i, l := range lines {
			if m := key.FindStringSubmatch(l); m != nil {
				if at >= 0 {
					t.Errorf("%s appears twice in the example", s.key)
				}
				at = i
				if v, _ := strconv.ParseInt(m[1], 10, 64); v != s.def {
					t.Errorf("%s: the example sets %d, the default is %d", s.key, v, s.def)
				}
			}
		}
		if at < 0 {
			t.Errorf("%s is missing from the example", s.key)
			continue
		}
		var block []string
		for j := at - 1; j >= 0 && strings.HasPrefix(strings.TrimSpace(lines[j]), "#"); j-- {
			block = append(block, lines[j])
		}
		m := stated.FindStringSubmatch(strings.Join(block, " "))
		if m == nil {
			t.Errorf("%s: no \"Default <n>, range <min> to <max>.\" in its comment", s.key)
			continue
		}
		def, _ := strconv.ParseInt(m[1], 10, 64)
		lo, _ := strconv.ParseInt(m[2], 10, 64)
		hi, _ := strconv.ParseInt(m[3], 10, 64)
		if def != s.def || lo != s.min || hi != s.max {
			t.Errorf("%s: the comment says default %d range %d to %d, the table says %d, %d to %d", s.key, def, lo, hi, s.def, s.min, s.max)
		}
	}
	// The pairs and the sum rule are stated once, in the comment above limits.
	text := strings.Join(lines, " ")
	text = regexp.MustCompile(`\s*#\s*`).ReplaceAllString(text, " ")
	for _, p := range limitPairs {
		if !strings.Contains(text, p.lo+" <= ") && !strings.Contains(text, p.lo+" <=") {
			t.Errorf("the pair %s <= %s is not stated in the example", p.lo, p.hi)
		}
	}
	if !strings.Contains(text, "must not exceed "+strconv.Itoa(MaxToolSeconds)) {
		t.Errorf("the sum rule (limit %d) is not stated in the example", MaxToolSeconds)
	}
}

// acceptedKeys returns every key the loader's decoder accepts, read from the
// calls in decode.go that name a mapping's keys, plus the limits table.
func acceptedKeys(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "decode.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	set := map[string]bool{}
	read := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := c.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "mapping", "item":
			for _, a := range c.Args[min(2, len(c.Args)):] {
				if lit, ok := a.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					set[unquote(t, lit.Value)] = true
				}
			}
		case "str", "nonEmpty", "integer", "boolean", "seq", "strings", "get", "absPath":
			for _, a := range c.Args {
				if lit, ok := a.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					read[unquote(t, lit.Value)] = true
				}
			}
		}
		return true
	})
	for _, s := range limitTable {
		set[s.key] = true
	}
	// Every key the decoder reads must be one it accepts: a blind spot in the
	// walk above would show here.
	for k := range read {
		if !set[k] {
			t.Errorf("decode.go reads the key %q but no mapping call lists it", k)
		}
	}
	var out []string
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func unquote(t *testing.T, lit string) string {
	t.Helper()
	s, err := strconv.Unquote(lit)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestExampleHasEveryKey: no key the loader accepts is missing from the
// example (written live or as a commented alternative).
func TestExampleHasEveryKey(t *testing.T) {
	keys := acceptedKeys(t)
	for _, want := range []string{"version", "server", "auth", "tenants", "listen", "allow_non_loopback", "data_dir", "audit_log",
		"provider", "cloudflare_access", "team_domain", "audience", "certs_url", "id", "principals", "identities", "servers",
		"policy", "limits", "email", "common_name", "user", "private_key_file", "description", "host", "port", "identity",
		"host_key", "known_hosts_file", "access", "tags", "deny_commands", "deny_paths"} {
		if !slices.Contains(keys, want) {
			t.Errorf("the key walk did not find %q: the test is blind", want)
		}
	}
	text := readExample(t)
	for _, k := range keys {
		if !regexp.MustCompile(`(?m)^\s*(#\s*)?(-\s*)?` + regexp.QuoteMeta(k) + `:`).MatchString(text) {
			t.Errorf("the key %q is accepted by the loader and missing from the example", k)
		}
	}
}

// TestUnitStopTimeout ties the unit's TimeoutStopSec to the limits table: it
// must exceed the largest shutdown_grace_seconds plus the 5 seconds serve
// waits for requests still answering, with margin.
func TestUnitStopTimeout(t *testing.T) {
	b, err := os.ReadFile(unitPath) //nolint:gosec // G304: a fixed repository path
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^TimeoutStopSec=(\d+)s$`).FindStringSubmatch(string(b))
	if m == nil {
		t.Fatal("the unit has no TimeoutStopSec in whole seconds")
	}
	secs, _ := strconv.Atoi(m[1])
	limit := time.Duration(limitByKey("shutdown_grace_seconds").max)*time.Second + 5*time.Second
	if got := time.Duration(secs) * time.Second; got < limit+10*time.Second {
		t.Errorf("TimeoutStopSec is %v; the longest graceful stop is %v, and it needs 10 seconds of margin", got, limit)
	}
}
