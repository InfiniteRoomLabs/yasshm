package e2e

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/sshtest"
)

// TestE2ELongCommand: a command that outlives wait_seconds comes back
// running with a command id, is paged with read_output (forward pages and a
// negative offset from the end) and is killed with kill_command (D10).
func TestE2ELongCommand(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	g := f.start(t)
	g.waitReady(t)
	c := connect(t, g.edge(t))

	script := `i=0; while [ $i -lt 300 ]; do echo "line-$i"; i=$((i+1)); done; echo ready; sleep 300`
	var want strings.Builder
	for i := range 300 {
		fmt.Fprintf(&want, "line-%d\n", i)
	}
	want.WriteString("ready\n")

	var id string
	t.Run("happy/outlives wait_seconds and returns running", func(t *testing.T) {
		begin := time.Now()
		r := c.runCommand(t, "lab-1", script, map[string]any{"wait_seconds": 1})
		took := time.Since(begin)
		if r.Status != "running" || r.CommandID == "" || r.ExitCode != nil || took < time.Second || took > 30*time.Second {
			t.Fatalf("run = %+v after %v", r, took)
		}
		id = r.CommandID
		c.waitOutput(t, id, "ready")
	})
	t.Run("happy/paged with read_output", func(t *testing.T) {
		var got strings.Builder
		var off int64
		pages := 0
		for {
			r := c.readOutput(t, id, map[string]any{"offset": off, "max_bytes": 256})
			if r.Offset != off || len(r.Data) > 256 || r.Status != "running" {
				t.Fatalf("page at %d = %+v", off, r)
			}
			got.WriteString(r.Data)
			pages++
			if r.NextOffset == r.Size {
				break
			}
			if r.NextOffset <= off {
				t.Fatalf("no progress at %d: %+v", off, r)
			}
			off = r.NextOffset
		}
		if got.String() != want.String() || pages < 10 {
			t.Fatalf("%d pages, output %d bytes, want %d", pages, got.Len(), want.Len())
		}
	})
	t.Run("edge/a negative offset reads from the end", func(t *testing.T) {
		r := c.readOutput(t, id, map[string]any{"offset": -15})
		if r.Data != "line-299\nready\n" || r.NextOffset != r.Size {
			t.Fatalf("tail = %+v", r)
		}
	})
	t.Run("happy/killed with kill_command", func(t *testing.T) {
		var k killResult
		c.ok(t, "kill_command", map[string]any{"command_id": id, "signal": "KILL"}, &k)
		if !k.SignalDelivered {
			t.Fatalf("kill = %+v", k)
		}
		r := c.waitStatus(t, id, callBound)
		if r.Status != "killed" {
			t.Fatalf("status = %+v", r)
		}
		var l listCommandsResult
		c.ok(t, "list_commands", map[string]any{"status": "killed"}, &l)
		if len(l.Commands) != 1 || l.Commands[0].CommandID != id {
			t.Fatalf("list = %+v", l)
		}
		got := f.events(t, func(l auditLine) bool { return l.CommandID == id && strings.HasPrefix(l.Event, "command.") })
		if !slices.Equal(got, []string{"command.requested", "command.started", "command.kill_requested", "command.finished"}) {
			t.Fatalf("events = %v", got)
		}
	})
}

// TestE2ETimeout: a command that passes its timeout_seconds is stopped and
// reports timed_out.
func TestE2ETimeout(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	g := f.start(t)
	g.waitReady(t)
	c := connect(t, g.edge(t))
	t.Run("sad/a command past its timeout is timed_out", func(t *testing.T) {
		r := c.runCommand(t, "lab-1", "echo begun; sleep 300", map[string]any{"timeout_seconds": 1, "wait_seconds": 15})
		if r.Status != "timed_out" || r.Stdout != "begun\n" {
			t.Fatalf("run = %+v", r)
		}
		lines, _ := f.auditLog(t)
		for _, l := range lines {
			if l.Event == "command.finished" && l.CommandID == r.CommandID && l.Status == "timed_out" {
				return
			}
		}
		t.Fatal("no command.finished with status timed_out")
	})
}

// TestE2EOutputLimit: output beyond max_output_bytes is cut at the limit
// and marked truncated, and the remote side is not blocked: the command
// writes far more than the limit and still finishes on its own.
func TestE2EOutputLimit(t *testing.T) {
	t.Parallel()
	f := newFixture(t, withLimits(map[string]int64{"max_output_bytes": 1024}))
	g := f.start(t)
	g.waitReady(t)
	c := connect(t, g.edge(t))
	t.Run("edge/output past max_output_bytes is truncated", func(t *testing.T) {
		r := c.runCommand(t, "lab-1", `head -c 500000 /dev/zero | tr '\0' a; echo; echo finished >&2`, map[string]any{"wait_seconds": 20, "merge_stderr": false})
		// stdout_bytes counts what the command wrote; the gateway keeps
		// the first max_output_bytes of it.
		if r.Status != "succeeded" || !r.Truncated || r.StdoutBytes != 500001 || r.Stdout != strings.Repeat("a", 1024) {
			t.Fatalf("run = status %s truncated %v stdout_bytes %d stdout %d bytes", r.Status, r.Truncated, r.StdoutBytes, len(r.Stdout))
		}
		o := c.readOutput(t, r.CommandID, map[string]any{"offset": -4})
		if !o.Truncated || o.Size != 1024 || o.Data != "aaaa" {
			t.Fatalf("read = %+v", o)
		}
	})
}

// TestE2EBusy: a command over max_concurrent_per_server is refused with
// busy, and the message names the limit.
func TestE2EBusy(t *testing.T) {
	t.Parallel()
	f := newFixture(t, withLimits(map[string]int64{"max_concurrent_per_server": 1, "max_concurrent_commands": 2}))
	g := f.start(t)
	g.waitReady(t)
	c := connect(t, g.edge(t))
	t.Run("sad/a second command on a busy server", func(t *testing.T) {
		first := c.runCommand(t, "lab-1", "sleep 300", map[string]any{"wait_seconds": 0})
		if first.Status != "running" {
			t.Fatalf("first = %+v", first)
		}
		reqs, _ := sshRequests(f.target("lab-1"))
		e := c.fail(t, "run_command", map[string]any{"server": "lab-1", "command": "id"})
		if e.Code != "busy" || e.Message != "a limit is reached: max_concurrent_per_server" {
			t.Fatalf("second = %+v", e)
		}
		if now, _ := sshRequests(f.target("lab-1")); now != reqs {
			t.Fatalf("the target saw %d new requests", now-reqs)
		}
		c.ok(t, "kill_command", map[string]any{"command_id": first.CommandID, "signal": "KILL"}, &killResult{})
		c.waitStatus(t, first.CommandID, callBound)
		// The slot is free again.
		if r := c.runCommand(t, "lab-1", "echo free", map[string]any{"wait_seconds": 10}); r.Status != "succeeded" {
			t.Fatalf("third = %+v", r)
		}
	})
}

// TestE2EPolicyDenied: a denylisted command (a built-in rule and an
// operator rule) and a denylisted path are refused with policy_denied and
// the rule, written as policy.denied, and nothing is sent to the target.
func TestE2EPolicyDenied(t *testing.T) {
	t.Parallel()
	f := newFixture(t, withDenyCommands(`\bforbidden-tool\b`))
	g := f.start(t)
	g.waitReady(t)
	c := connect(t, g.edge(t))
	srv := f.target("lab-1")
	reqs, conns := sshRequests(srv)
	for _, tc := range []struct {
		name, tool string
		args       map[string]any
		rule       string
	}{
		{"sad/a built-in command rule", "run_command", map[string]any{"server": "lab-1", "command": "ls; sudo id"}, "sudo"},
		{"sad/an operator command rule", "run_command", map[string]any{"server": "lab-1", "command": "forbidden-tool --now"}, ""},
		{"sad/a built-in path rule", "download_file", map[string]any{"server": "lab-1", "path": "/etc/shadow"}, "shadow"},
		{"raging/a quoted bypass attempt", "run_command", map[string]any{"server": "lab-1", "command": `echo x; "sudo" id`}, "sudo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := c.fail(t, tc.tool, tc.args)
			if e.Code != "policy_denied" || e.Rule == "" || (tc.rule != "" && e.Rule != tc.rule) {
				t.Fatalf("error = %+v", e)
			}
			found := false
			lines, _ := f.auditLog(t)
			for _, l := range lines {
				found = found || (l.Event == "policy.denied" && l.Rule == e.Rule && l.Tool == tc.tool && l.Code == "policy_denied")
			}
			if !found {
				t.Fatalf("no policy.denied event for %s", e.Rule)
			}
		})
	}
	t.Run("happy/nothing was sent to the target", func(t *testing.T) {
		if r, cn := sshRequests(srv); r != reqs || cn != conns {
			t.Fatalf("the target saw %d requests and %d connections: %v", r-reqs, cn-conns, srv.Requests())
		}
	})
}

// TestE2EHostKeyMismatch: a target whose host key is not the pinned one is
// never spoken to (D13): run_command is host_key_mismatch, the probe says
// host_key_ok false, and the target saw no request at all.
func TestE2EHostKeyMismatch(t *testing.T) {
	t.Parallel()
	f := newFixture(t, withTarget("impostor", sshtest.Options{WrongHostKey: true}))
	g := f.start(t)
	g.waitReady(t)
	c := connect(t, g.edge(t))
	srv := f.target("impostor")
	t.Run("sad/run_command is host_key_mismatch", func(t *testing.T) {
		e := c.fail(t, "run_command", map[string]any{"server": "impostor", "command": "id"})
		if e.Code != "host_key_mismatch" {
			t.Fatalf("error = %+v", e)
		}
	})
	t.Run("sad/the probe reports the mismatch", func(t *testing.T) {
		var r serverInfoResult
		c.ok(t, "get_server_info", map[string]any{"server": "impostor"}, &r)
		if r.ErrorCode != "host_key_mismatch" || r.HostKeyOK == nil || *r.HostKeyOK || r.Facts != nil {
			t.Fatalf("info = %+v", r)
		}
	})
	t.Run("sad/download_file is host_key_mismatch", func(t *testing.T) {
		e := c.fail(t, "download_file", map[string]any{"server": "impostor", "path": srv.Root + "/x"})
		if e.Code != "host_key_mismatch" {
			t.Fatalf("error = %+v", e)
		}
	})
	t.Run("happy/nothing was sent to the impostor", func(t *testing.T) {
		if got := srv.Requests(); len(got) != 0 {
			t.Fatalf("the impostor saw %v", got)
		}
	})
}

// TestE2EUploadDenied: upload_file needs full, which no server can grant
// in v0.1 (D5): access_denied with rule access-level, audited as
// policy.denied, and no SFTP session is opened on the target.
func TestE2EUploadDenied(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	g := f.start(t)
	g.waitReady(t)
	c := connect(t, g.edge(t))
	srv := f.target("lab-1")
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"sad/text content", map[string]any{"server": "lab-1", "path": srv.Root + "/a.txt", "content": "hello"}},
		{"sad/base64 content with overwrite", map[string]any{"server": "lab-1", "path": srv.Root + "/b.bin", "content_base64": "AAEC", "overwrite": true, "mode": "0600"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := c.fail(t, "upload_file", tc.args)
			if e.Code != "access_denied" || e.Rule != "access-level" {
				t.Fatalf("error = %+v", e)
			}
		})
	}
	t.Run("happy/audited and nothing sent", func(t *testing.T) {
		if got := f.events(t, func(l auditLine) bool { return l.Event == "policy.denied" && l.Tool == "upload_file" }); len(got) != 2 {
			t.Fatalf("policy.denied events = %v", got)
		}
		if got := f.events(t, func(l auditLine) bool { return l.Event == "file.uploaded" }); len(got) != 0 {
			t.Fatalf("file.uploaded events = %v", got)
		}
		if got := srv.Requests(); len(got) != 0 {
			t.Fatalf("the target saw %v", got)
		}
	})
}
