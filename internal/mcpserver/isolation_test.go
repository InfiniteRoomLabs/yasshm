package mcpserver

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// TestTenantIsolation: two tenants built in code (the loader accepts one)
// cannot see each other's servers, commands or output through any tool
// (spec D7, section 6). Both have a server called srv-1, on different
// targets.
func TestTenantIsolation(t *testing.T) {
	h := newHarness(t, withTenants(func(ts []tenant.Tenant) {
		s := ts[1].Servers[0]
		s.ID, s.Access = "full-b", tenant.AccessFull
		ts[1].Servers = append(ts[1].Servers, s)
	}))
	if err := os.WriteFile(filepath.Join(h.sb.Root, "b-only.txt"), []byte("tenant b data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bFile := filepath.Join(h.sb.Root, "b-only.txt")
	b := h.run(t, svcB, "srv-1", "cat b-only.txt; sleep 30", map[string]any{"wait_seconds": 1})
	t.Cleanup(func() {
		decode(t, h.call(t, svcB, ToolKillCommand, map[string]any{"command_id": b.CommandID, "signal": "KILL"}), &KillResult{})
	})

	t.Run("happy/the same server id names the caller's own server", func(t *testing.T) {
		a := h.run(t, userA, "srv-1", "cat b-only.txt", nil)
		if a.Status != "failed" || a.Stdout == "tenant b data\n" {
			t.Fatalf("tenant A's srv-1 reached tenant B's target: %+v", a)
		}
	})
	t.Run("sad/servers", func(t *testing.T) {
		var ls ListServersResult
		decode(t, h.call(t, userA, ToolListServers, nil), &ls)
		for _, s := range ls.Servers {
			if s.ID == "only-b" || s.ID == "full-b" {
				t.Fatalf("tenant A sees %s", s.ID)
			}
		}
		for _, tool := range []struct {
			name string
			args map[string]any
		}{
			{ToolGetServerInfo, map[string]any{"server": "only-b"}},
			{ToolRunCommand, map[string]any{"server": "only-b", "command": "id"}},
			{ToolListCommands, map[string]any{"server": "only-b"}},
			{ToolDownloadFile, map[string]any{"server": "only-b", "path": bFile}},
			{ToolUploadFile, map[string]any{"server": "full-b", "path": filepath.Join(h.sb.Root, "x"), "content": "x"}},
		} {
			e := wantErr(t, h.call(t, userA, tool.name, tool.args), CodeNotFound, "server-not-found")
			if e.Message != msgNoServer {
				t.Fatalf("%s: message %q", tool.name, e.Message)
			}
		}
	})
	t.Run("sad/commands and output", func(t *testing.T) {
		var lc ListCommandsResult
		decode(t, h.call(t, userA, ToolListCommands, nil), &lc)
		for _, c := range lc.Commands {
			if c.CommandID == b.CommandID {
				t.Fatal("tenant A lists tenant B's command")
			}
		}
		for _, name := range []string{ToolReadOutput, ToolKillCommand} {
			e := wantErr(t, h.call(t, userA, name, map[string]any{"command_id": b.CommandID}), CodeNotFound, "")
			if e.Message != msgNoCommand {
				t.Fatalf("%s: message %q", name, e.Message)
			}
		}
		var r ReadResult
		decode(t, h.call(t, svcB, ToolReadOutput, map[string]any{"command_id": b.CommandID}), &r)
		if r.Status != "running" || r.Data != "tenant b data\n" {
			t.Fatalf("tenant B's own command = %+v", r)
		}
	})
	t.Run("sad/a cursor of another tenant shows only the caller's records", func(t *testing.T) {
		h.run(t, svcB, "srv-1", "true", nil)
		var pb ListCommandsResult
		decode(t, h.call(t, svcB, ToolListCommands, map[string]any{"limit": 1}), &pb)
		if pb.NextCursor == "" {
			t.Fatal("tenant B has no second page")
		}
		var pa ListCommandsResult
		decode(t, h.call(t, userA, ToolListCommands, map[string]any{"cursor": pb.NextCursor}), &pa)
		for _, c := range pa.Commands {
			if c.CommandID == b.CommandID || c.Server == "only-b" {
				t.Fatal("tenant A reached tenant B's record")
			}
		}
	})
}
