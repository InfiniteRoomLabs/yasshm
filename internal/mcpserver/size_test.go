package mcpserver

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// TestResultSize builds the largest result each tool can give and measures
// the HTTP response body: it never exceeds max_result_bytes, at the
// smallest and the largest value the config allows. The output is chosen to
// cost the most once encoded (characters JSON escapes to six bytes, bytes
// that are not UTF-8, NUL), and the lists are as long as they can be.
func TestResultSize(t *testing.T) {
	for _, limit := range []int64{4096, 140000} {
		t.Run(fmt.Sprintf("raging/max_result_bytes %d", limit), func(t *testing.T) {
			h := newHarness(t, withLimits(func(l *tenant.Limits) {
				l.MaxResultBytes, l.MaxOutputBytes = limit, 1<<20
			}), withTenants(func(ts []tenant.Tenant) {
				base := ts[0].Servers[0]
				for i := range 300 {
					s := base
					s.ID = tenant.ServerID(fmt.Sprintf("bulk-%03d", i))
					s.Description = strings.Repeat("<", 256)
					s.Tags = nil
					for j := range 16 {
						s.Tags = append(s.Tags, fmt.Sprintf("%02d", j)+strings.Repeat("t", 61))
					}
					ts[0].Servers = append(ts[0].Servers, s)
				}
			}))
			measure := func(t *testing.T, tool string, args any, wantErr bool) map[string]any {
				t.Helper()
				r := h.rawCall(t, userA, tool, args)
				if r.code != http.StatusOK {
					t.Fatalf("%s: status %d", tool, r.code)
				}
				if int64(len(r.body)) > limit {
					t.Fatalf("%s: %d bytes on the wire, max_result_bytes %d", tool, len(r.body), limit)
				}
				var msg struct {
					Result struct {
						IsError           bool           `json:"isError"`
						StructuredContent map[string]any `json:"structuredContent"`
					} `json:"result"`
				}
				if err := json.Unmarshal(r.body, &msg); err != nil {
					t.Fatalf("%s: %v", tool, err)
				}
				if msg.Result.IsError != wantErr {
					t.Fatalf("%s: isError %v: %s", tool, msg.Result.IsError, r.body)
				}
				t.Logf("%s: %d bytes", tool, len(r.body))
				return msg.Result.StructuredContent
			}
			outputs := map[string]string{
				"escapes": `head -c 300000 /dev/zero | tr '\0' '<'`,
				"invalid": `head -c 300000 /dev/zero | tr '\0' '\377'`,
				"nul":     `head -c 300000 /dev/zero`,
			}
			for name, cmd := range outputs {
				var run RunResult
				decode(t, h.call(t, userA, ToolRunCommand, map[string]any{"server": "srv-1", "command": cmd, "wait_seconds": 8}), &run)
				if run.Status != "succeeded" {
					t.Fatalf("%s: %+v", name, run.CommandStatus)
				}
				sc := measure(t, ToolRunCommand, map[string]any{"server": "srv-1", "command": cmd, "wait_seconds": 8, "max_bytes": 1 << 30}, false)
				if sc["stdout"] == "" {
					t.Fatalf("%s: empty page", name)
				}
				measure(t, ToolReadOutput, map[string]any{"command_id": run.CommandID, "max_bytes": 1 << 30}, false)
				measure(t, ToolReadOutput, map[string]any{"command_id": run.CommandID, "offset": -100000}, false)
			}
			split := `head -c 200000 /dev/zero | tr '\0' '<'; head -c 200000 /dev/zero >&2`
			measure(t, ToolRunCommand, map[string]any{"server": "srv-1", "command": split, "merge_stderr": false, "wait_seconds": 8}, false)

			big := filepath.Join(h.sa.Root, "big.txt")
			if err := os.WriteFile(big, []byte(strings.Repeat("<&>\"\\", 300000)), 0o600); err != nil {
				t.Fatal(err)
			}
			noise := make([]byte, 1<<20)
			if _, err := rand.Read(noise); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(h.sa.Root, "noise.bin")
			if err := os.WriteFile(bin, noise, 0o600); err != nil {
				t.Fatal(err)
			}
			if sc := measure(t, ToolDownloadFile, map[string]any{"server": "srv-1", "path": big, "max_bytes": 1 << 30}, false); sc["encoding"] != "text" {
				t.Fatalf("encoding = %v", sc["encoding"])
			}
			if sc := measure(t, ToolDownloadFile, map[string]any{"server": "srv-1", "path": bin}, false); sc["encoding"] != "base64" || sc["data_base64"] == "" {
				t.Fatalf("binary = %v", sc["encoding"])
			}

			if sc := measure(t, ToolListServers, nil, false); sc["truncated"] != true {
				t.Fatal("300 servers fit; the list test is not the largest")
			}
			measure(t, ToolGetServerInfo, map[string]any{"server": "bulk-000"}, false)

			long := "true #" + strings.Repeat("<", 300) // list_commands shows 200 characters
			for range 100 {
				h.run(t, userA, "srv-1", long, nil)
			}
			sc := measure(t, ToolListCommands, map[string]any{"limit": 100}, false)
			if sc["next_cursor"] == nil || sc["next_cursor"] == "" {
				t.Fatal("100 commands fit; the list test is not the largest")
			}
			measure(t, ToolKillCommand, map[string]any{"command_id": strings.Repeat("0", 32)}, true)
			measure(t, ToolUploadFile, map[string]any{"server": "srv-1", "path": "/tmp/x", "content": strings.Repeat("<", 1000)}, true)
			measure(t, ToolRunCommand, map[string]any{"server": "srv-1", "command": "sudo " + strings.Repeat("<", 8000)}, true)
		})
	}
}

// TestListCommandsCursorAfterCut: when a page is cut to fit, next_cursor
// continues right after the last command shown, so paging sees every
// command exactly once.
func TestListCommandsCursorAfterCut(t *testing.T) {
	h := newHarness(t, withLimits(func(l *tenant.Limits) { l.MaxResultBytes = 8192 }))
	want := map[string]bool{}
	for i := range 12 {
		r := h.run(t, userA, "srv-1", fmt.Sprintf("true %d #%s", i, strings.Repeat("<", 900)), nil)
		want[r.CommandID] = true
	}
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 20 {
			t.Fatal("paging does not end")
		}
		args := map[string]any{"limit": 100}
		if cursor != "" {
			args["cursor"] = cursor
		}
		var p ListCommandsResult
		decode(t, h.call(t, userA, ToolListCommands, args), &p)
		for _, c := range p.Commands {
			if seen[c.CommandID] {
				t.Fatalf("%s listed twice", c.CommandID)
			}
			seen[c.CommandID] = true
		}
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
	if len(seen) != len(want) {
		t.Fatalf("saw %d of %d commands", len(seen), len(want))
	}
}

// TestShrinkInfo walks get_server_info's shrink ladder with facts a hostile
// target filled with characters JSON escapes.
func TestShrinkInfo(t *testing.T) {
	hostile := strings.Repeat("<", 256)
	res := ServerInfoResult{
		ServerEntry: ServerEntry{ID: "srv-1", Description: hostile, Tags: []string{strings.Repeat("t", 63)}, Access: "look"},
		Reachable:   true, HostKeyOK: boolp(true),
		Facts: &Facts{Kernel: hostile, Arch: hostile, OS: hostile, UptimeSeconds: 9},
	}
	for _, budget := range []int{100000 - envelopeReserve, 8192 - envelopeReserve, 4096 - envelopeReserve} {
		c := &call{budget: budget}
		r := c.shrinkInfo(res)
		if n := encodedLen(successResult(r)); n > budget {
			t.Fatalf("budget %d: %d bytes", budget, n)
		}
		got := r.structured.(ServerInfoResult)
		if budget > 50000 && got.Truncated {
			t.Fatalf("budget %d: truncated", budget)
		}
		if budget < 5000 && (!got.Truncated || got.Facts.Kernel == hostile) {
			t.Fatalf("budget %d: %+v", budget, got)
		}
	}
}
