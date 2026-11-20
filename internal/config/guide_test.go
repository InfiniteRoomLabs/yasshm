package config

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var guidePath = filepath.Join("..", "..", "docs", "operator-guide.md")

// guideRow is one row of the operator guide's config reference table.
type guideRow struct {
	path, required, def, form, meaning string
}

// guideRows reads the rows between the config-reference markers.
func guideRows(t *testing.T) []guideRow {
	t.Helper()
	b, err := os.ReadFile(guidePath) //nolint:gosec // G304: a fixed repository path
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(b), "<!-- config-reference:begin -->")
	if !ok {
		t.Fatal("no config-reference:begin marker in the operator guide")
	}
	body, _, ok := strings.Cut(rest, "<!-- config-reference:end -->")
	if !ok {
		t.Fatal("no config-reference:end marker in the operator guide")
	}
	var rows []guideRow
	for _, l := range strings.Split(body, "\n") {
		if !strings.HasPrefix(l, "| `") {
			continue
		}
		cells := strings.Split(strings.Trim(l, "|"), " | ")
		if len(cells) != 5 {
			t.Fatalf("a row of the config reference does not have five cells: %q", l)
		}
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		rows = append(rows, guideRow{strings.Trim(cells[0], "`"), cells[1], cells[2], cells[3], cells[4]})
	}
	return rows
}

// lastKey is the final element of a dotted path with its list marker removed.
func lastKey(path string) string {
	last := path[strings.LastIndex(path, ".")+1:]
	return strings.TrimSuffix(last, "[]")
}

// TestGuideConfigReference checks the config reference table of the operator
// guide against the code: it holds exactly the keys the decoder accepts, each
// path once, and every limit's default and range are the limits table's.
func TestGuideConfigReference(t *testing.T) {
	rows := guideRows(t)
	t.Run("happy/the table lists exactly the keys the decoder accepts", func(t *testing.T) {
		have := map[string]bool{}
		seen := map[string]bool{}
		for _, r := range rows {
			if seen[r.path] {
				t.Errorf("the path %s is listed twice", r.path)
			}
			seen[r.path] = true
			// Every element of a path counts as covered: a mapping such as
			// `server` is described by the rows beneath it.
			for _, el := range strings.Split(r.path, ".") {
				have[strings.TrimSuffix(el, "[]")] = true
			}
			if !slices.Contains(acceptedKeys(t), lastKey(r.path)) {
				t.Errorf("the path %s ends in a key the decoder does not accept", r.path)
			}
		}
		delete(have, "NAME")
		var missing, extra []string
		for _, k := range acceptedKeys(t) {
			if !have[k] {
				missing = append(missing, k)
			}
			delete(have, k)
		}
		for k := range have {
			extra = append(extra, k)
		}
		sort.Strings(extra)
		if len(missing) > 0 || len(extra) > 0 {
			t.Errorf("keys the decoder accepts and the table lacks: %v; keys in the table the decoder does not accept: %v", missing, extra)
		}
	})
	t.Run("happy/every limit's default and range are the code's", func(t *testing.T) {
		byKey := map[string]guideRow{}
		for _, r := range rows {
			if strings.HasPrefix(r.path, "tenants[].limits.") {
				byKey[lastKey(r.path)] = r
			}
		}
		if len(byKey) != len(limitTable) {
			t.Errorf("the table has %d limits, the code %d", len(byKey), len(limitTable))
		}
		for _, s := range limitTable {
			r, ok := byKey[s.key]
			if !ok {
				t.Errorf("limit %s is not in the table", s.key)
				continue
			}
			if r.required != "no" || r.def != strconv.FormatInt(s.def, 10) || r.form != strconv.FormatInt(s.min, 10)+" to "+strconv.FormatInt(s.max, 10) {
				t.Errorf("%s: the table says required %q, default %q, range %q; the code says default %d, range %d to %d", s.key, r.required, r.def, r.form, s.def, s.min, s.max)
			}
		}
	})
	t.Run("happy/required keys are the keys the decoder requires", func(t *testing.T) {
		// A key marked required in the table is one the decoder reports as
		// "is required" when it is absent from the otherwise valid example.
		text := readExample(t)
		k := genKeys(t)
		filled := strings.NewReplacer(placeholderHostKey, `"`+k.hostKey+`"`, placeholderKeyFile, k.keyFile).Replace(text)
		for _, tc := range []struct{ path, line string }{
			{"server.listen", "  listen: 127.0.0.1:8822\n"},
			{"server.data_dir", "  data_dir: /var/lib/yasshm\n"},
			{"server.audit_log", "  audit_log: /var/log/yasshm/audit.jsonl\n"},
			{"auth.cloudflare_access.team_domain", "    team_domain: example-team.cloudflareaccess.com\n"},
			{"tenants[].servers[].access", "        access: look\n"},
			{"tenants[].servers[].host", "        host: lab-1.example.net\n"},
			{"tenants[].identities.NAME.user", "        user: look\n"},
		} {
			cfg := strings.Replace(filled, tc.line, "", 1)
			if cfg == filled {
				t.Fatalf("the example no longer holds the line %q", tc.line)
			}
			_, err := Parse([]byte(cfg))
			if err == nil || !strings.Contains(err.Error(), "is required") {
				t.Errorf("without %s the loader says %v, want \"is required\"", tc.path, err)
			}
			for _, r := range rows {
				if r.path == tc.path && r.required != "yes" {
					t.Errorf("%s is required by the decoder and the table says %q", tc.path, r.required)
				}
			}
		}
	})
}
