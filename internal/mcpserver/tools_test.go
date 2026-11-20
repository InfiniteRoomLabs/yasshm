package mcpserver

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/leakcheck"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/loopback"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/sshtest"
)

// withServer adds a server of tenant A on an sshtest server with opts.
func withServer(t *testing.T, id tenant.ServerID, opts sshtest.Options) hopt {
	return func(_ *harness, ts []tenant.Tenant) {
		s := sshtest.Start(t, opts)
		ts[0].Servers = append(ts[0].Servers, serverFor(tA, id, s))
	}
}

// withRefusing adds a server of tenant A on a port that refuses every
// connection.
func withRefusing(t *testing.T, id tenant.ServerID) hopt {
	return func(h *harness, ts []tenant.Tenant) {
		s := serverFor(tA, id, h.sa)
		s.Port = loopback.RefusingPort(t)
		ts[0].Servers = append(ts[0].Servers, s)
	}
}

// withFull adds a server of tenant A that grants full (no config can).
func withFull(id tenant.ServerID) hopt {
	return func(h *harness, ts []tenant.Tenant) {
		s := serverFor(tA, id, h.sa)
		s.Access = tenant.AccessFull
		ts[0].Servers = append(ts[0].Servers, s)
	}
}

// waitDone polls read_output until the command has finished.
func (h *harness) waitDone(t *testing.T, who, id string) ReadResult {
	t.Helper()
	var r ReadResult
	ok := leakcheck.Poll(20*time.Second, func() bool {
		decode(t, h.call(t, who, ToolReadOutput, map[string]any{"command_id": id, "wait_seconds": 2}), &r)
		return r.Status != "running"
	})
	if !ok {
		t.Fatalf("command %s still running: %+v", id, r)
	}
	return r
}

func TestListServers(t *testing.T) {
	h := newHarness(t)
	t.Run("happy/the caller's tenant only", func(t *testing.T) {
		var a, b ListServersResult
		decode(t, h.call(t, userA, ToolListServers, nil), &a)
		decode(t, h.call(t, svcB, ToolListServers, map[string]any{}), &b)
		if len(a.Servers) != 2 || a.Servers[0].ID != "srv-1" || a.Servers[1].ID != "srv-2" || a.Truncated {
			t.Fatalf("tenant A sees %+v", a)
		}
		if a.Servers[0].Access != "look" || a.Servers[0].Description != "test server srv-1" || len(a.Servers[0].Tags) != 1 {
			t.Fatalf("entry = %+v", a.Servers[0])
		}
		if len(b.Servers) != 2 || b.Servers[1].ID != "only-b" {
			t.Fatalf("tenant B sees %+v", b)
		}
	})
	t.Run("happy/text lists the servers", func(t *testing.T) {
		got := text(t, h.call(t, userA, ToolListServers, nil))
		if got != "srv-1 (look): test server srv-1 [test]\nsrv-2 (look): test server srv-2 [test]\n" {
			t.Fatalf("text = %q", got)
		}
	})
	for _, tc := range []struct{ name, args string }{
		{"sad/an argument it does not take", `{"server":"srv-1"}`},
		{"raging/arguments that are an array", `[1,2]`},
		{"raging/arguments that are a string", `"x"`},
		{"raging/trailing data", `{} {}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := h.callErr(t, userA, ToolListServers, tc.args)
			if err != nil {
				return // the SDK refused it as a protocol error, which is also a refusal
			}
			wantErr(t, res, CodeInvalidArgument, "")
		})
	}
}

func TestGetServerInfo(t *testing.T) {
	h := newHarness(t,
		withServer(t, "wrong-key", sshtest.Options{WrongHostKey: true}),
		withServer(t, "refuse-key", sshtest.Options{RefuseKey: true}),
		withRefusing(t, "down"),
	)
	t.Run("happy/probe", func(t *testing.T) {
		var r ServerInfoResult
		decode(t, h.call(t, userA, ToolGetServerInfo, map[string]any{"server": "srv-1"}), &r)
		if !r.Reachable || r.HostKeyOK == nil || !*r.HostKeyOK || r.Facts == nil || r.Facts.Kernel == "" || r.Facts.Arch == "" || r.ErrorCode != "" {
			t.Fatalf("info = %+v", r)
		}
		if r.ID != "srv-1" || r.Access != "look" {
			t.Fatalf("entry = %+v", r.ServerEntry)
		}
	})
	for _, tc := range []struct {
		name, server, code string
		reachable          bool
		keyOK              *bool
	}{
		{"sad/host key mismatch", "wrong-key", CodeHostKeyMismatch, true, boolp(false)},
		{"sad/key refused", "refuse-key", CodeSSHAuthFailed, true, boolp(true)},
		{"sad/unreachable", "down", CodeSSHUnreachable, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r ServerInfoResult
			decode(t, h.call(t, userA, ToolGetServerInfo, map[string]any{"server": tc.server}), &r)
			if r.Reachable != tc.reachable || r.ErrorCode != tc.code || r.Facts != nil {
				t.Fatalf("info = %+v", r)
			}
			if (r.HostKeyOK == nil) != (tc.keyOK == nil) || (r.HostKeyOK != nil && *r.HostKeyOK != *tc.keyOK) {
				t.Fatalf("host_key_ok = %v", r.HostKeyOK)
			}
		})
	}
	t.Run("sad/unknown server", func(t *testing.T) {
		wantErr(t, h.call(t, userA, ToolGetServerInfo, map[string]any{"server": "nope"}), CodeNotFound, "server-not-found")
	})
	t.Run("sad/server of another tenant", func(t *testing.T) {
		e := wantErr(t, h.call(t, userA, ToolGetServerInfo, map[string]any{"server": "only-b"}), CodeNotFound, "server-not-found")
		if e.Message != msgNoServer {
			t.Fatalf("message = %q", e.Message)
		}
	})
	t.Run("sad/no server", func(t *testing.T) {
		wantErr(t, h.call(t, userA, ToolGetServerInfo, nil), CodeInvalidArgument, "")
	})
	for _, bad := range []string{`{"server":"../srv-1"}`, `{"server":"SRV-1"}`, `{"server":1}`, `{"server":"` + strings.Repeat("a", 64) + `"}`, `{"Server":"srv-1"}`, `{"server":"srv-1","server":"srv-2"}`} {
		t.Run("raging/"+bad[:min(len(bad), 30)], func(t *testing.T) {
			wantErr(t, h.call(t, userA, ToolGetServerInfo, bad), CodeInvalidArgument, "")
		})
	}
}

func TestRunCommand(t *testing.T) {
	h := newHarness(t, withServer(t, "wrong-key", sshtest.Options{WrongHostKey: true}), withRefusing(t, "down"))
	t.Run("happy/finishes within the wait", func(t *testing.T) {
		r := h.run(t, userA, "srv-1", "echo hello; echo oops >&2", nil)
		if r.Status != "succeeded" || r.ExitCode == nil || *r.ExitCode != 0 || r.Stdout != "hello\noops\n" && r.Stdout != "oops\nhello\n" {
			t.Fatalf("run = %+v", r)
		}
		if !r.EOF || r.NextOffset != 11 || r.StdoutBytes != 6 || r.StderrBytes != 5 || !r.MergeStderr || r.Server != "srv-1" {
			t.Fatalf("run = %+v", r)
		}
	})
	t.Run("happy/separate streams", func(t *testing.T) {
		r := h.run(t, userA, "srv-1", "echo out; echo err >&2; exit 3", map[string]any{"merge_stderr": false})
		if r.Status != "failed" || *r.ExitCode != 3 || r.Stdout != "out\n" || r.Stderr != "err\n" || r.StderrNextOffset != 4 || r.MergeStderr {
			t.Fatalf("run = %+v", r)
		}
		txt := text(t, h.call(t, userA, ToolReadOutput, map[string]any{"command_id": r.CommandID, "stream": "stderr"}))
		if !strings.HasSuffix(txt, "\nerr\n") {
			t.Fatalf("stderr text = %q", txt)
		}
	})
	t.Run("happy/the text is a status line and the output", func(t *testing.T) {
		res := h.call(t, userA, ToolRunCommand, map[string]any{"server": "srv-1", "command": "printf 'a<b>&c'"})
		got := text(t, res)
		if !strings.Contains(got, ": succeeded, exit code 0; stdout bytes 0..6 of 6, eof\na<b>&c") {
			t.Fatalf("text = %q", got)
		}
	})
	t.Run("happy/equals read_output at offset 0", func(t *testing.T) {
		r := h.run(t, userA, "srv-1", "seq 1 20000", map[string]any{"max_bytes": 5000})
		var rr ReadResult
		decode(t, h.call(t, userA, ToolReadOutput, map[string]any{"command_id": r.CommandID, "max_bytes": 5000}), &rr)
		if r.Stdout != rr.Data || r.NextOffset != rr.NextOffset || len(r.Stdout) != 5000 || r.EOF {
			t.Fatalf("run %d bytes next %d, read %d bytes next %d", len(r.Stdout), r.NextOffset, len(rr.Data), rr.NextOffset)
		}
	})
	t.Run("edge/outlives the wait, is paged and killed", func(t *testing.T) {
		r := h.run(t, userA, "srv-1", "echo started; sleep 30", map[string]any{"wait_seconds": 1})
		if r.Status != "running" || r.EOF {
			t.Fatalf("run = %+v", r)
		}
		var page ReadResult
		ok := leakcheck.Poll(10*time.Second, func() bool {
			decode(t, h.call(t, userA, ToolReadOutput, map[string]any{"command_id": r.CommandID, "wait_seconds": 1}), &page)
			return page.Data == "started\n"
		})
		if !ok || page.EOF || page.NextOffset != 8 {
			t.Fatalf("page = %+v", page)
		}
		var k KillResult
		decode(t, h.call(t, userA, ToolKillCommand, map[string]any{"command_id": r.CommandID}), &k)
		if !k.SignalDelivered {
			t.Fatalf("kill = %+v", k)
		}
		if got := h.waitDone(t, userA, r.CommandID); got.Status != "killed" {
			t.Fatalf("status = %s", got.Status)
		}
	})
	t.Run("edge/wait 0 returns at once", func(t *testing.T) {
		r := h.run(t, userA, "srv-1", "sleep 2", map[string]any{"wait_seconds": 0})
		if r.Status != "running" {
			t.Fatalf("run = %+v", r)
		}
		h.waitDone(t, userA, r.CommandID)
	})
	t.Run("sad/denylisted", func(t *testing.T) {
		wantErr(t, h.call(t, userA, ToolRunCommand, map[string]any{"server": "srv-1", "command": "ls; sudo id"}), CodePolicyDenied, "sudo")
	})
	t.Run("sad/unknown and foreign servers read the same", func(t *testing.T) {
		a := wantErr(t, h.call(t, userA, ToolRunCommand, map[string]any{"server": "nope", "command": "id"}), CodeNotFound, "server-not-found")
		b := wantErr(t, h.call(t, userA, ToolRunCommand, map[string]any{"server": "only-b", "command": "id"}), CodeNotFound, "server-not-found")
		if a != b {
			t.Fatalf("%+v != %+v", a, b)
		}
	})
	t.Run("sad/timeout above the maximum", func(t *testing.T) {
		wantErr(t, h.call(t, userA, ToolRunCommand, map[string]any{"server": "srv-1", "command": "id", "timeout_seconds": 61}), CodeInvalidArgument, "")
	})
	t.Run("sad/host key mismatch", func(t *testing.T) {
		wantErr(t, h.call(t, userA, ToolRunCommand, map[string]any{"server": "wrong-key", "command": "id"}), CodeHostKeyMismatch, "")
	})
	t.Run("sad/unreachable", func(t *testing.T) {
		wantErr(t, h.call(t, userA, ToolRunCommand, map[string]any{"server": "down", "command": "id"}), CodeSSHUnreachable, "")
	})
	t.Run("sad/times out", func(t *testing.T) {
		r := h.run(t, userA, "srv-1", "sleep 30", map[string]any{"timeout_seconds": 1, "wait_seconds": 8})
		if r.Status != "timed_out" {
			t.Fatalf("run = %+v", r)
		}
	})
	for _, tc := range []struct {
		name string
		args any
		code string
		rule string
	}{
		{"raging/command too long for the tenant", map[string]any{"server": "srv-1", "command": strings.Repeat("x", 8193)}, CodePolicyDenied, "command-too-long"},
		{"raging/arguments too large", map[string]any{"server": "srv-1", "command": strings.Repeat("x", 200<<10)}, CodeInvalidArgument, ""},
		{"raging/NUL in the command", map[string]any{"server": "srv-1", "command": "id\x00"}, CodePolicyDenied, "command-nul"},
		{"raging/empty command", map[string]any{"server": "srv-1", "command": ""}, CodePolicyDenied, "command-empty"},
		{"raging/wait as a string", `{"server":"srv-1","command":"id","wait_seconds":"5"}`, CodeInvalidArgument, ""},
		{"raging/wait as a fraction", `{"server":"srv-1","command":"id","wait_seconds":1.5}`, CodeInvalidArgument, ""},
		{"raging/negative wait", map[string]any{"server": "srv-1", "command": "id", "wait_seconds": -1}, CodeInvalidArgument, ""},
		{"raging/negative timeout", map[string]any{"server": "srv-1", "command": "id", "timeout_seconds": -1}, CodeInvalidArgument, ""},
		{"raging/negative max_bytes", map[string]any{"server": "srv-1", "command": "id", "max_bytes": -1}, CodeInvalidArgument, ""},
		{"raging/unknown field", map[string]any{"server": "srv-1", "command": "id", "pty": true}, CodeInvalidArgument, ""},
		{"raging/command as an object", `{"server":"srv-1","command":{"x":1}}`, CodeInvalidArgument, ""},
		{"raging/huge wait", `{"server":"srv-1","command":"id","wait_seconds":99999999999999999999}`, CodeInvalidArgument, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantErr(t, h.call(t, userA, ToolRunCommand, tc.args), tc.code, tc.rule)
		})
	}
}

// TestRunCommandWaitClamped: a wait above max_wait_seconds is clamped, not
// refused.
func TestRunCommandWaitClamped(t *testing.T) {
	h := newHarness(t, withLimits(func(l *tenant.Limits) { l.DefaultWaitSeconds, l.MaxWaitSeconds = 1, 1 }))
	start := time.Now()
	r := h.run(t, userA, "srv-1", "sleep 30", map[string]any{"wait_seconds": 100000})
	if r.Status != "running" || time.Since(start) > 15*time.Second {
		t.Fatalf("run = %+v after %v", r, time.Since(start))
	}
	decode(t, h.call(t, userA, ToolKillCommand, map[string]any{"command_id": r.CommandID, "signal": "KILL"}), &KillResult{})
	h.waitDone(t, userA, r.CommandID)
}

// TestRunCommandBusy fills the per-server slots.
func TestRunCommandBusy(t *testing.T) {
	h := newHarness(t, withLimits(func(l *tenant.Limits) { l.MaxConcurrentPerServer = 1 }))
	r := h.run(t, userA, "srv-1", "sleep 30", map[string]any{"wait_seconds": 0})
	e := wantErr(t, h.call(t, userA, ToolRunCommand, map[string]any{"server": "srv-1", "command": "id"}), CodeBusy, "")
	if !strings.Contains(e.Message, "max_concurrent_per_server") {
		t.Fatalf("busy message = %q", e.Message)
	}
	decode(t, h.call(t, userA, ToolKillCommand, map[string]any{"command_id": r.CommandID, "signal": "KILL"}), &KillResult{})
	h.waitDone(t, userA, r.CommandID)
}

// TestRunCommandClientGone: a client that goes away while run_command waits
// does not stop the command (D10).
func TestRunCommandClientGone(t *testing.T) {
	h := newHarness(t)
	cs := h.session(userA)
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	_, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: ToolRunCommand, Arguments: map[string]any{
		"server": "srv-1", "command": "sleep 2; echo survived", "wait_seconds": 8,
	}})
	if err == nil {
		t.Fatal("the call returned before the client gave up")
	}
	var page ListCommandsResult
	ok := leakcheck.Poll(15*time.Second, func() bool {
		decode(t, h.call(t, userA, ToolListCommands, nil), &page)
		return len(page.Commands) == 1 && page.Commands[0].Status != "running"
	})
	if !ok || page.Commands[0].Status != "succeeded" {
		t.Fatalf("commands = %+v", page.Commands)
	}
	var rr ReadResult
	decode(t, h.call(t, userA, ToolReadOutput, map[string]any{"command_id": page.Commands[0].CommandID}), &rr)
	if rr.Data != "survived\n" {
		t.Fatalf("output = %q", rr.Data)
	}
}

func TestReadOutput(t *testing.T) {
	h := newHarness(t)
	abc := h.run(t, userA, "srv-1", "printf abcdef", nil)
	t.Run("happy/negative offset counts from the end", func(t *testing.T) {
		var r ReadResult
		decode(t, h.call(t, userA, ToolReadOutput, map[string]any{"command_id": abc.CommandID, "offset": -3}), &r)
		if r.Data != "def" || r.Offset != 3 || r.NextOffset != 6 || !r.EOF || r.Size != 6 {
			t.Fatalf("read = %+v", r)
		}
	})
	t.Run("happy/pages with max_bytes", func(t *testing.T) {
		var got strings.Builder
		off := int64(0)
		for range 10 {
			var r ReadResult
			decode(t, h.call(t, userA, ToolReadOutput, map[string]any{"command_id": abc.CommandID, "offset": off, "max_bytes": 4}), &r)
			got.WriteString(r.Data)
			off = r.NextOffset
			if r.EOF {
				break
			}
		}
		if got.String() != "abcdef" {
			t.Fatalf("pages = %q", got.String())
		}
	})
	t.Run("happy/invalid UTF-8 is replaced and marked lossy", func(t *testing.T) {
		r := h.run(t, userA, "srv-1", `printf 'a\377b'`, nil)
		var rr ReadResult
		decode(t, h.call(t, userA, ToolReadOutput, map[string]any{"command_id": r.CommandID}), &rr)
		if rr.Data != "a�b" || !rr.Lossy || !r.Lossy || rr.NextOffset != 3 {
			t.Fatalf("read = %+v", rr)
		}
	})
	t.Run("edge/a character split by the page boundary is kept whole", func(t *testing.T) {
		r := h.run(t, userA, "srv-1", `printf 'x\303\251y'`, nil) // x, e-acute, y
		var p1, p2 ReadResult
		decode(t, h.call(t, userA, ToolReadOutput, map[string]any{"command_id": r.CommandID, "max_bytes": 2}), &p1)
		decode(t, h.call(t, userA, ToolReadOutput, map[string]any{"command_id": r.CommandID, "offset": p1.NextOffset}), &p2)
		if p1.Data != "x" || p1.NextOffset != 1 || p1.Lossy || p2.Data != "éy" || p2.Lossy {
			t.Fatalf("pages %+v %+v", p1, p2)
		}
	})
	t.Run("edge/a page of only half a character still moves on", func(t *testing.T) {
		r := h.run(t, userA, "srv-1", `printf '\303\251'`, nil)
		var p ReadResult
		decode(t, h.call(t, userA, ToolReadOutput, map[string]any{"command_id": r.CommandID, "max_bytes": 1}), &p)
		if p.NextOffset != 1 || !p.Lossy {
			t.Fatalf("page = %+v", p)
		}
	})
	t.Run("happy/waits for output", func(t *testing.T) {
		r := h.run(t, userA, "srv-1", "sleep 1; echo late", map[string]any{"wait_seconds": 0})
		var rr ReadResult
		decode(t, h.call(t, userA, ToolReadOutput, map[string]any{"command_id": r.CommandID, "wait_seconds": 8}), &rr)
		if rr.Data != "late\n" {
			t.Fatalf("read = %+v", rr)
		}
	})
	t.Run("sad/unknown command", func(t *testing.T) {
		e := wantErr(t, h.call(t, userA, ToolReadOutput, map[string]any{"command_id": strings.Repeat("0", 32)}), CodeNotFound, "")
		if e.Message != msgNoCommand {
			t.Fatalf("message = %q", e.Message)
		}
	})
	t.Run("sad/a command of another tenant reads as unknown", func(t *testing.T) {
		b := h.run(t, svcB, "srv-1", "echo b-secret", nil)
		e := wantErr(t, h.call(t, userA, ToolReadOutput, map[string]any{"command_id": b.CommandID}), CodeNotFound, "")
		if e.Message != msgNoCommand {
			t.Fatalf("message = %q", e.Message)
		}
	})
	for _, tc := range []struct{ name, args string }{
		{"raging/malformed id", `{"command_id":"../../etc"}`},
		{"raging/upper-case id", `{"command_id":"` + strings.Repeat("A", 32) + `"}`},
		{"raging/no id", `{}`},
		{"raging/bad stream", `{"command_id":"` + strings.Repeat("0", 32) + `","stream":"stdin"}`},
		{"raging/negative wait", `{"command_id":"` + strings.Repeat("0", 32) + `","wait_seconds":-1}`},
		{"raging/negative max_bytes", `{"command_id":"` + strings.Repeat("0", 32) + `","max_bytes":-5}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantErr(t, h.call(t, userA, ToolReadOutput, tc.args), CodeInvalidArgument, "")
		})
	}
}

func TestListCommands(t *testing.T) {
	h := newHarness(t)
	long := "echo " + strings.Repeat("y", 300)
	first := h.run(t, userA, "srv-1", "echo one", nil)
	second := h.run(t, userA, "srv-2", long, nil)
	h.run(t, svcB, "srv-1", "echo tenant-b", nil)
	t.Run("happy/newest first, the caller's tenant only", func(t *testing.T) {
		var r ListCommandsResult
		decode(t, h.call(t, userA, ToolListCommands, nil), &r)
		if len(r.Commands) != 2 || r.Commands[0].CommandID != second.CommandID || r.Commands[1].CommandID != first.CommandID || r.NextCursor != "" {
			t.Fatalf("list = %+v", r)
		}
		c := r.Commands[0]
		if len([]rune(c.Command)) != 200 || !c.CommandTruncated || c.Status != "succeeded" || c.CreatedAt == "" || c.FinishedAt == "" || c.StdoutBytes != 301 {
			t.Fatalf("entry = %+v", c)
		}
	})
	t.Run("happy/cursor pages", func(t *testing.T) {
		var p1, p2 ListCommandsResult
		decode(t, h.call(t, userA, ToolListCommands, map[string]any{"limit": 1}), &p1)
		decode(t, h.call(t, userA, ToolListCommands, map[string]any{"limit": 1, "cursor": p1.NextCursor}), &p2)
		if len(p1.Commands) != 1 || p1.NextCursor == "" || len(p2.Commands) != 1 || p2.Commands[0].CommandID != first.CommandID {
			t.Fatalf("pages %+v %+v", p1, p2)
		}
	})
	t.Run("happy/filters", func(t *testing.T) {
		var r ListCommandsResult
		decode(t, h.call(t, userA, ToolListCommands, map[string]any{"server": "srv-1", "status": "succeeded"}), &r)
		if len(r.Commands) != 1 || r.Commands[0].CommandID != first.CommandID {
			t.Fatalf("list = %+v", r)
		}
	})
	t.Run("sad/a server of another tenant", func(t *testing.T) {
		wantErr(t, h.call(t, userA, ToolListCommands, map[string]any{"server": "only-b"}), CodeNotFound, "server-not-found")
	})
	for _, tc := range []struct{ name, args string }{
		{"raging/limit 0", `{"limit":0}`},
		{"raging/limit 101", `{"limit":101}`},
		{"raging/bad status", `{"status":"done"}`},
		{"raging/cursor with odd characters", `{"cursor":"c1.<script>"}`},
		{"raging/cursor that decodes to nothing", `{"cursor":"c1.zzzz"}`},
		{"raging/cursor too long", `{"cursor":"` + strings.Repeat("a", 300) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantErr(t, h.call(t, userA, ToolListCommands, tc.args), CodeInvalidArgument, "")
		})
	}
}

func TestKillCommand(t *testing.T) {
	h := newHarness(t)
	t.Run("happy/kills with INT", func(t *testing.T) {
		r := h.run(t, userA, "srv-1", "sleep 30", map[string]any{"wait_seconds": 0})
		var k KillResult
		decode(t, h.call(t, userA, ToolKillCommand, map[string]any{"command_id": r.CommandID, "signal": "INT"}), &k)
		if !k.SignalDelivered {
			t.Fatalf("kill = %+v", k)
		}
		if got := h.waitDone(t, userA, r.CommandID); got.Status != "killed" {
			t.Fatalf("status = %s", got.Status)
		}
	})
	t.Run("edge/a finished command is left as it is", func(t *testing.T) {
		r := h.run(t, userA, "srv-1", "true", nil)
		for range 2 {
			var k KillResult
			decode(t, h.call(t, userA, ToolKillCommand, map[string]any{"command_id": r.CommandID}), &k)
			if k.SignalDelivered || k.Status != "succeeded" {
				t.Fatalf("kill = %+v", k)
			}
		}
	})
	t.Run("sad/another tenant's command", func(t *testing.T) {
		b := h.run(t, svcB, "srv-1", "sleep 30", map[string]any{"wait_seconds": 0})
		wantErr(t, h.call(t, userA, ToolKillCommand, map[string]any{"command_id": b.CommandID}), CodeNotFound, "")
		decode(t, h.call(t, svcB, ToolKillCommand, map[string]any{"command_id": b.CommandID, "signal": "KILL"}), &KillResult{})
		h.waitDone(t, svcB, b.CommandID)
	})
	for _, tc := range []struct{ name, args string }{
		{"raging/a signal it does not send", `{"command_id":"` + strings.Repeat("0", 32) + `","signal":"HUP"}`},
		{"raging/lower-case signal", `{"command_id":"` + strings.Repeat("0", 32) + `","signal":"term"}`},
		{"raging/no id", `{"signal":"TERM"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantErr(t, h.call(t, userA, ToolKillCommand, tc.args), CodeInvalidArgument, "")
		})
	}
}

func TestDownloadFile(t *testing.T) {
	h := newHarness(t, withRefusing(t, "down"))
	write := func(name string, data []byte) string {
		p := filepath.Join(h.sa.Root, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	txt := write("notes.txt", []byte("hello <world> & é\n"))
	bin := write("blob.bin", []byte{0, 1, 2, 0xff, 0xfe})
	t.Run("happy/text", func(t *testing.T) {
		var r DownloadResult
		decode(t, h.call(t, userA, ToolDownloadFile, map[string]any{"server": "srv-1", "path": txt}), &r)
		if r.Encoding != "text" || r.Data != "hello <world> & é\n" || !r.EOF || r.Size != 19 || r.NextOffset != 19 || r.DataBase64 != "" {
			t.Fatalf("download = %+v", r)
		}
	})
	t.Run("happy/binary as base64", func(t *testing.T) {
		var r DownloadResult
		decode(t, h.call(t, userA, ToolDownloadFile, map[string]any{"server": "srv-1", "path": bin}), &r)
		got, err := base64.StdEncoding.DecodeString(r.DataBase64)
		if err != nil || r.Encoding != "base64" || string(got) != "\x00\x01\x02\xff\xfe" || r.Data != "" || !r.EOF {
			t.Fatalf("download = %+v", r)
		}
	})
	t.Run("happy/offset and max_bytes page", func(t *testing.T) {
		var r DownloadResult
		decode(t, h.call(t, userA, ToolDownloadFile, map[string]any{"server": "srv-1", "path": txt, "offset": 6, "max_bytes": 7}), &r)
		if r.Data != "<world>" || r.NextOffset != 13 || r.EOF {
			t.Fatalf("download = %+v", r)
		}
	})
	t.Run("happy/audited before and after", func(t *testing.T) {
		var stages []string
		for _, l := range h.au.lines(t) {
			if l.Event == "file.downloaded" && l.Path == txt {
				stages = append(stages, l.Stage)
			}
		}
		if len(stages) < 2 || stages[0] != "requested" || stages[1] != "done" {
			t.Fatalf("stages = %v", stages)
		}
	})
	t.Run("sad/denied paths", func(t *testing.T) {
		wantErr(t, h.call(t, userA, ToolDownloadFile, map[string]any{"server": "srv-1", "path": "/etc/shadow"}), CodePolicyDenied, "shadow")
		wantErr(t, h.call(t, userA, ToolDownloadFile, map[string]any{"server": "srv-1", "path": "/tmp/../etc/./shadow"}), CodePolicyDenied, "shadow")
		wantErr(t, h.call(t, userA, ToolDownloadFile, map[string]any{"server": "srv-1", "path": "notes.txt"}), CodePolicyDenied, "path-not-absolute")
		wantErr(t, h.call(t, userA, ToolDownloadFile, map[string]any{"server": "srv-1", "path": "/root/.ssh/id_ed25519"}), CodePolicyDenied, "ssh-private-key")
	})
	t.Run("sad/missing file", func(t *testing.T) {
		wantErr(t, h.call(t, userA, ToolDownloadFile, map[string]any{"server": "srv-1", "path": filepath.Join(h.sa.Root, "nope")}), CodeNotFound, "")
	})
	t.Run("sad/a directory", func(t *testing.T) {
		wantErr(t, h.call(t, userA, ToolDownloadFile, map[string]any{"server": "srv-1", "path": h.sa.Root}), CodeInvalidArgument, "")
	})
	t.Run("sad/unreachable", func(t *testing.T) {
		wantErr(t, h.call(t, userA, ToolDownloadFile, map[string]any{"server": "down", "path": txt}), CodeSSHUnreachable, "")
	})
	t.Run("sad/a server of another tenant", func(t *testing.T) {
		wantErr(t, h.call(t, userA, ToolDownloadFile, map[string]any{"server": "only-b", "path": txt}), CodeNotFound, "server-not-found")
	})
	for _, tc := range []struct {
		name string
		args any
		code string
	}{
		{"raging/negative offset", map[string]any{"server": "srv-1", "path": txt, "offset": -1}, CodeInvalidArgument},
		{"raging/NUL in the path", map[string]any{"server": "srv-1", "path": "/tmp/a\x00b"}, CodePolicyDenied},
		{"raging/path too long", map[string]any{"server": "srv-1", "path": "/" + strings.Repeat("a", 5000)}, CodePolicyDenied},
		{"raging/path as a number", `{"server":"srv-1","path":7}`, CodeInvalidArgument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantErr(t, h.call(t, userA, ToolDownloadFile, tc.args), tc.code, "")
		})
	}
}

func TestUploadFile(t *testing.T) {
	h := newHarness(t, withFull("full-1"))
	t.Run("sad/every look server refuses", func(t *testing.T) {
		wantErr(t, h.call(t, userA, ToolUploadFile, map[string]any{"server": "srv-1", "path": "/tmp/x", "content": "x"}), CodeAccessDenied, "access-level")
	})
	t.Run("happy/a full server built in code writes the file", func(t *testing.T) {
		p := filepath.Join(h.sa.Root, "up.txt")
		var r UploadResult
		decode(t, h.call(t, userA, ToolUploadFile, map[string]any{"server": "full-1", "path": p, "content": "uploaded\n", "mode": "0600"}), &r)
		got, err := os.ReadFile(p) //nolint:gosec // G304: a path under the test server's temp root
		if err != nil || string(got) != "uploaded\n" || r.BytesWritten != 9 || r.Path != p {
			t.Fatalf("upload %+v, file %q %v", r, got, err)
		}
		var stages []string
		for _, l := range h.au.lines(t) {
			if l.Event == "file.uploaded" && l.Path == p {
				stages = append(stages, l.Stage)
			}
		}
		if len(stages) != 2 || stages[0] != "requested" || stages[1] != "done" {
			t.Fatalf("stages = %v", stages)
		}
	})
	t.Run("happy/base64 content and overwrite", func(t *testing.T) {
		p := filepath.Join(h.sa.Root, "up.bin")
		args := map[string]any{"server": "full-1", "path": p, "content_base64": base64.StdEncoding.EncodeToString([]byte{0, 1, 2})}
		decode(t, h.call(t, userA, ToolUploadFile, args), &UploadResult{})
		wantErr(t, h.call(t, userA, ToolUploadFile, args), CodeInvalidArgument, "")
		args["overwrite"] = true
		args["content_base64"] = base64.StdEncoding.EncodeToString([]byte{9})
		decode(t, h.call(t, userA, ToolUploadFile, args), &UploadResult{})
		if got, _ := os.ReadFile(p); string(got) != "\x09" { //nolint:gosec // G304: a path under the test server's temp root
			t.Fatalf("file = %q", got)
		}
	})
	t.Run("sad/denied path on a full server", func(t *testing.T) {
		wantErr(t, h.call(t, userA, ToolUploadFile, map[string]any{"server": "full-1", "path": "/etc/sudoers", "content": "x"}), CodePolicyDenied, "sudoers")
	})
	for _, tc := range []struct {
		name string
		args any
	}{
		{"raging/both contents", map[string]any{"server": "full-1", "path": "/tmp/x", "content": "x", "content_base64": "eA=="}},
		{"raging/no content", map[string]any{"server": "full-1", "path": "/tmp/x"}},
		{"raging/bad base64", map[string]any{"server": "full-1", "path": "/tmp/x", "content_base64": "!!!"}},
		{"raging/mode too wide", map[string]any{"server": "full-1", "path": "/tmp/x", "content": "x", "mode": "4755"}},
		{"raging/mode not octal", map[string]any{"server": "full-1", "path": "/tmp/x", "content": "x", "mode": "0999"}},
		{"raging/larger than max_upload_bytes", map[string]any{"server": "full-1", "path": "/tmp/x", "content": strings.Repeat("x", (1<<20)+1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantErr(t, h.call(t, userA, ToolUploadFile, tc.args), CodeInvalidArgument, "")
		})
	}
}
