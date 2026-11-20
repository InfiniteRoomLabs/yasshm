package e2e

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestE2EEveryTool drives all eight tools through the fake edge against the
// real binary (spec 7: "every tool through the fake edge"), on one gateway
// the subtests share: each tool's structured answer comes from the binary,
// download_file reads a real file on the target, and upload_file is refused
// with access_denied (no server can grant full in v0.1, D5).
func TestE2EEveryTool(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	g := f.start(t)
	g.waitReady(t)
	c := connect(t, g.edge(t))
	srv := f.target("lab-1")

	t.Run("happy/list_servers", func(t *testing.T) {
		var r listServersResult
		c.ok(t, "list_servers", map[string]any{}, &r)
		if len(r.Servers) != 1 || r.Servers[0].ID != "lab-1" || r.Servers[0].Access != "look" ||
			r.Servers[0].Description != "e2e target lab-1" || !slices.Equal(r.Servers[0].Tags, []string{"e2e"}) {
			t.Fatalf("servers = %+v", r.Servers)
		}
		// D8: nothing from the config file but ids, descriptions, tags
		// and access levels.
		for _, s := range texts(t, c.call(t, "list_servers", map[string]any{})) {
			for _, secret := range []string{"127.0.0.1", srv.User, srv.ClientKeyFile, f.config, f.dataDir} {
				if strings.Contains(s, secret) {
					t.Fatalf("list_servers shows %q: %s", secret, s)
				}
			}
		}
	})
	t.Run("happy/get_server_info", func(t *testing.T) {
		var r serverInfoResult
		c.ok(t, "get_server_info", map[string]any{"server": "lab-1"}, &r)
		if r.ID != "lab-1" || !r.Reachable || r.HostKeyOK == nil || !*r.HostKeyOK || r.Facts == nil || r.Facts.Kernel == "" {
			t.Fatalf("info = %+v", r)
		}
	})
	var finished commandResult
	t.Run("happy/run_command", func(t *testing.T) {
		finished = c.runCommand(t, "lab-1", "echo hello-e2e; echo oops >&2; exit 3", map[string]any{"wait_seconds": 10, "merge_stderr": false})
		if finished.Status != "failed" || finished.ExitCode == nil || *finished.ExitCode != 3 ||
			finished.Stdout != "hello-e2e\n" || finished.Stderr != "oops\n" || !finished.EOF {
			t.Fatalf("run = %+v", finished)
		}
	})
	t.Run("happy/read_output", func(t *testing.T) {
		r := c.readOutput(t, finished.CommandID, map[string]any{"stream": "stderr"})
		if r.Data != "oops\n" || r.Status != "failed" || !r.EOF || r.Size != 5 {
			t.Fatalf("read = %+v", r)
		}
		r = c.readOutput(t, finished.CommandID, map[string]any{"offset": 6, "max_bytes": 3})
		if r.Data != "e2e" || r.NextOffset != 9 {
			t.Fatalf("paged read = %+v", r)
		}
	})
	t.Run("happy/list_commands", func(t *testing.T) {
		var r listCommandsResult
		c.ok(t, "list_commands", map[string]any{"server": "lab-1"}, &r)
		if len(r.Commands) == 0 || r.Commands[0].CommandID != finished.CommandID || r.Commands[0].Status != "failed" ||
			!strings.HasPrefix(r.Commands[0].Command, "echo hello-e2e") {
			t.Fatalf("list = %+v", r)
		}
	})
	t.Run("happy/kill_command", func(t *testing.T) {
		r := c.runCommand(t, "lab-1", "echo started; sleep 300", map[string]any{"wait_seconds": 0})
		c.waitOutput(t, r.CommandID, "started")
		var k killResult
		c.ok(t, "kill_command", map[string]any{"command_id": r.CommandID}, &k)
		if !k.SignalDelivered || k.CommandID != r.CommandID {
			t.Fatalf("kill = %+v", k)
		}
		if got := c.waitStatus(t, r.CommandID, callBound); got.Status != "killed" {
			t.Fatalf("status = %s", got.Status)
		}
	})
	t.Run("happy/download_file", func(t *testing.T) {
		content := []byte("line one\nline two\n")
		p := filepath.Join(srv.Root, "e2e-download.txt")
		if err := os.WriteFile(p, content, 0o600); err != nil {
			t.Fatal(err)
		}
		var r downloadResult
		c.ok(t, "download_file", map[string]any{"server": "lab-1", "path": p}, &r)
		if r.Size != int64(len(content)) || r.Encoding != "text" || r.Data != string(content) || !r.EOF {
			t.Fatalf("download = %+v", r)
		}
		bin := []byte{0, 1, 2, 0xff, 'x'}
		pb := filepath.Join(srv.Root, "e2e-download.bin")
		if err := os.WriteFile(pb, bin, 0o600); err != nil {
			t.Fatal(err)
		}
		c.ok(t, "download_file", map[string]any{"server": "lab-1", "path": pb, "offset": 1}, &r)
		if r.Encoding != "base64" || r.DataBase64 != base64.StdEncoding.EncodeToString(bin[1:]) || r.NextOffset != 5 {
			t.Fatalf("binary download = %+v", r)
		}
	})
	t.Run("sad/upload_file is access_denied", func(t *testing.T) {
		before, _ := sshRequests(srv)
		e := c.fail(t, "upload_file", map[string]any{"server": "lab-1", "path": filepath.Join(srv.Root, "up.txt"), "content": "x"})
		if e.Code != "access_denied" || e.Rule != "access-level" {
			t.Fatalf("upload = %+v", e)
		}
		if after, _ := sshRequests(srv); after != before {
			t.Fatalf("the target saw %d new requests", after-before)
		}
		if _, err := os.Stat(filepath.Join(srv.Root, "up.txt")); !os.IsNotExist(err) {
			t.Fatalf("the file was written: %v", err)
		}
	})
	t.Run("happy/every tool call is audited with the principal", func(t *testing.T) {
		called := map[string]bool{}
		lines, _ := f.auditLog(t)
		for _, l := range lines {
			if l.Event == "tool.called" {
				if l.Tenant != "default" || !strings.Contains(l.Principal, operator) {
					t.Fatalf("tool.called %+v", l)
				}
				called[l.Tool] = true
			}
		}
		for _, tool := range []string{"list_servers", "get_server_info", "run_command", "read_output", "list_commands", "kill_command", "download_file", "upload_file"} {
			if !called[tool] {
				t.Errorf("no tool.called for %s", tool)
			}
		}
	})
	t.Run("happy/the gateway stops with exit 0", func(t *testing.T) {
		if code := g.stop(); code != 0 {
			t.Fatalf("exit %d", code)
		}
	})
}
