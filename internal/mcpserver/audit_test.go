package mcpserver

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/InfiniteRoomLabs/yasshm/internal/audit"
)

// TestAuditBeforeAct: for every tool, a tool.called event that cannot be
// written means nothing happens and the answer is internal; for each acting
// tool the same holds for its own "requested" event (spec D12).
func TestAuditBeforeAct(t *testing.T) {
	h := newHarness(t, withFull("full-1"))
	file := filepath.Join(h.sa.Root, "f.txt")
	if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	running := h.run(t, userA, "srv-1", "sleep 30", map[string]any{"wait_seconds": 0})
	t.Cleanup(func() {
		h.au.setFail(audit.EventToolCalled, false)
		decode(t, h.call(t, userA, ToolKillCommand, map[string]any{"command_id": running.CommandID, "signal": "KILL"}), &KillResult{})
	})
	sftpOpens := func() int {
		n := 0
		for _, r := range h.sa.Requests() {
			if strings.Contains(r, "sftp") {
				n++
			}
		}
		return n
	}
	countEvents := func(name string) int {
		return len(h.events(t, func(l auditLine) bool { return l.Event == name }))
	}

	calls := map[string]map[string]any{
		ToolListServers:   nil,
		ToolGetServerInfo: {"server": "srv-1"},
		ToolRunCommand:    {"server": "srv-1", "command": "touch " + filepath.Join(h.sa.Root, "ran")},
		ToolReadOutput:    {"command_id": running.CommandID},
		ToolListCommands:  {},
		ToolKillCommand:   {"command_id": running.CommandID},
		ToolDownloadFile:  {"server": "srv-1", "path": file},
		ToolUploadFile:    {"server": "full-1", "path": filepath.Join(h.sa.Root, "up"), "content": "x"},
	}
	t.Run("sad/tool.called fails: no tool acts", func(t *testing.T) {
		h.au.setFail(audit.EventToolCalled, true)
		defer h.au.setFail(audit.EventToolCalled, false)
		before := h.sa.Accepted()
		for name, args := range calls {
			e := wantErr(t, h.call(t, userA, name, args), CodeInternal, "")
			if e.Message != msgAudit {
				t.Fatalf("%s: message %q", name, e.Message)
			}
		}
		if n := h.sa.Accepted(); n != before {
			t.Fatalf("%d connections to the target while tool.called failed", n-before)
		}
		if _, err := os.Stat(filepath.Join(h.sa.Root, "ran")); err == nil {
			t.Fatal("run_command ran")
		}
		if _, err := os.Stat(filepath.Join(h.sa.Root, "up")); err == nil {
			t.Fatal("upload_file wrote")
		}
	})
	t.Run("sad/command.requested fails: run_command starts nothing", func(t *testing.T) {
		h.au.setFail(audit.EventCommandRequested, true)
		defer h.au.setFail(audit.EventCommandRequested, false)
		started := countEvents(audit.EventCommandStarted)
		e := wantErr(t, h.call(t, userA, ToolRunCommand, calls[ToolRunCommand]), CodeInternal, "")
		if e.Message != msgAudit || countEvents(audit.EventCommandStarted) != started {
			t.Fatalf("error %+v; command.started %d -> %d", e, started, countEvents(audit.EventCommandStarted))
		}
		if _, err := os.Stat(filepath.Join(h.sa.Root, "ran")); err == nil {
			t.Fatal("run_command ran")
		}
	})
	t.Run("sad/command.kill_requested fails: nothing is signalled", func(t *testing.T) {
		h.au.setFail(audit.EventCommandKillRequested, true)
		defer h.au.setFail(audit.EventCommandKillRequested, false)
		wantErr(t, h.call(t, userA, ToolKillCommand, calls[ToolKillCommand]), CodeInternal, "")
		var r ReadResult
		decode(t, h.call(t, userA, ToolReadOutput, map[string]any{"command_id": running.CommandID}), &r)
		if r.Status != "running" {
			t.Fatalf("status = %s", r.Status)
		}
	})
	t.Run("sad/file.downloaded fails: nothing is read", func(t *testing.T) {
		h.au.setFail(audit.EventFileDownloaded, true)
		defer h.au.setFail(audit.EventFileDownloaded, false)
		before := sftpOpens()
		wantErr(t, h.call(t, userA, ToolDownloadFile, calls[ToolDownloadFile]), CodeInternal, "")
		if sftpOpens() != before {
			t.Fatal("an SFTP session was opened")
		}
	})
	t.Run("sad/file.uploaded fails: nothing is written", func(t *testing.T) {
		h.au.setFail(audit.EventFileUploaded, true)
		defer h.au.setFail(audit.EventFileUploaded, false)
		before := sftpOpens()
		wantErr(t, h.call(t, userA, ToolUploadFile, calls[ToolUploadFile]), CodeInternal, "")
		if _, err := os.Stat(filepath.Join(h.sa.Root, "up")); err == nil || sftpOpens() != before {
			t.Fatal("upload_file wrote")
		}
	})
	t.Run("sad/policy.denied fails: still refused, as internal", func(t *testing.T) {
		h.au.setFail(audit.EventPolicyDenied, true)
		defer h.au.setFail(audit.EventPolicyDenied, false)
		e := wantErr(t, h.call(t, userA, ToolRunCommand, map[string]any{"server": "srv-1", "command": "sudo id"}), CodeInternal, "")
		if e.Message != msgAudit {
			t.Fatalf("message = %q", e.Message)
		}
	})
	t.Run("happy/every call is audited with its request id", func(t *testing.T) {
		h.call(t, userA, ToolListServers, nil)
		h.call(t, userA, ToolListCommands, nil)
		h.call(t, userA, ToolReadOutput, calls[ToolReadOutput])
		h.call(t, userA, ToolGetServerInfo, calls[ToolGetServerInfo])
		lines := h.au.lines(t)
		accepted := map[string]bool{}
		for _, l := range lines {
			if l.Event == audit.EventAuthAccepted {
				accepted[l.RequestID] = true
			}
		}
		tools := map[string]bool{}
		for _, l := range lines {
			if l.Event == audit.EventToolCalled {
				tools[l.Tool] = true
				if !accepted[l.RequestID] || l.Tenant != string(tA) || l.Principal != "user:"+userA {
					t.Fatalf("tool.called %+v has no matching auth.accepted", l)
				}
			}
		}
		for name := range calls {
			if !tools[name] {
				t.Fatalf("no tool.called for %s", name)
			}
		}
	})
	t.Run("happy/arguments are audited, file content is not", func(t *testing.T) {
		for _, l := range h.au.lines(t) {
			if l.Event == audit.EventToolCalled && l.Tool == ToolUploadFile && !strings.Contains(string(l.Args), `"content_bytes":1`) {
				t.Fatalf("upload args = %s", l.Args)
			}
			if l.Event == audit.EventToolCalled && l.Tool == ToolUploadFile && strings.Contains(string(l.Args), `"x"`) {
				t.Fatalf("upload content audited: %s", l.Args)
			}
		}
	})
}

// TestBrokenAuditLog: once the real log has failed it refuses every write
// until a restart, so the guard refuses every request before any tool.
func TestBrokenAuditLog(t *testing.T) {
	h := newHarness(t)
	h.session(userA) // connect while the log works
	if err := h.au.l.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{ToolRunCommand, ToolDownloadFile, ToolKillCommand, ToolGetServerInfo} {
		_, err := h.callErr(t, userA, name, map[string]any{"server": "srv-1", "command": "id", "path": "/tmp/x"})
		if err == nil || !strings.Contains(err.Error(), "Service Unavailable") {
			t.Fatalf("%s: err = %v, want a 503 from the guard", name, err)
		}
	}
	if n := h.sa.Accepted(); n != 0 {
		t.Fatalf("%d connections to the target", n)
	}
}

// TestToolFailedEvent: an error answer other than a denial is recorded with
// its code.
func TestToolFailedEvent(t *testing.T) {
	h := newHarness(t)
	wantErr(t, h.call(t, userA, ToolReadOutput, map[string]any{"command_id": "x"}), CodeInvalidArgument, "")
	got := h.events(t, func(l auditLine) bool {
		return l.Event == audit.EventToolFailed && l.Tool == ToolReadOutput && l.Code == CodeInvalidArgument
	})
	if len(got) != 1 {
		t.Fatalf("tool.failed events = %v", got)
	}
	denied := h.events(t, func(l auditLine) bool { return l.Event == audit.EventToolFailed && l.Code == CodePolicyDenied })
	wantErr(t, h.call(t, userA, ToolRunCommand, map[string]any{"server": "srv-1", "command": "sudo id"}), CodePolicyDenied, "sudo")
	if again := h.events(t, func(l auditLine) bool { return l.Event == audit.EventToolFailed && l.Code == CodePolicyDenied }); !slices.Equal(denied, again) {
		t.Fatal("a denial was also written as tool.failed")
	}
}
