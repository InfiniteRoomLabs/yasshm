package e2e

import (
	"encoding/json"
	"os"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/sshtest"
)

// longID is a server id of the maximum length (63), so that the audit
// event of a call that names it is long.
const longID = "lab-with-a-long-server-id-so-that-its-audit-events-are-long-xyz"

// limitFileSize sets RLIMIT_FSIZE of process pid to n bytes with
// prlimit(2). Lowering a limit of a process of one's own uid needs no
// privilege, and the limit binds root as much as anyone. A write that would
// take a regular file past n then fails with EFBIG (the Go runtime ignores
// SIGXFSZ), the way a full disk fails a write: from the next write on, the
// gateway's audit log cannot be written. Pipes (the gateway's stderr) are
// not affected.
func limitFileSize(t *testing.T, pid int, n int64) {
	t.Helper()
	const rlimitFsize = 1
	var old syscall.Rlimit
	if err := syscall.Getrlimit(rlimitFsize, &old); err != nil {
		t.Fatal(err)
	}
	lim := syscall.Rlimit{Cur: uint64(n), Max: old.Max}                                                                                           //nolint:gosec // G115: n is a small positive file size
	if _, _, errno := syscall.RawSyscall6(syscall.SYS_PRLIMIT64, uintptr(pid), rlimitFsize, uintptr(unsafe.Pointer(&lim)), 0, 0, 0); errno != 0 { //nolint:gosec // G103: prlimit(2) takes a pointer to the new limit; no other use of unsafe
		t.Fatalf("prlimit(%d, RLIMIT_FSIZE, %d): %v", pid, n, errno)
	}
}

// maxTSLen is the longest RFC 3339 timestamp with nanoseconds the audit
// log writes (Go trims trailing zeros of the fraction, so it varies).
const maxTSLen = len("2006-01-02T15:04:05.999999999Z")

// TestE2EAuditUnwritable: an action whose audit event cannot be written
// does not happen (D12, spec 6). The audit log is made unwritable while
// the binary runs, without privileges and for root and a normal user
// alike: RLIMIT_FSIZE of the gateway is lowered so that the next request's
// auth.accepted still fits but its tool.called does not. Every acting tool
// then answers internal ("the audit log could not be written, so nothing
// was done") and nothing reaches the target; the logger refuses every later
// write, so the next request is refused by the guard (503) before any
// tool. One gateway per tool, since the first failure closes the log.
func TestE2EAuditUnwritable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		tool string
		args func(f *fixture, id string) map[string]any
	}{
		{"run_command", func(*fixture, string) map[string]any {
			return map[string]any{"server": longID, "command": "echo " + strings.Repeat("x", 2000)}
		}},
		{"read_output", func(_ *fixture, id string) map[string]any {
			return map[string]any{"command_id": id, "stream": "stderr", "offset": 12345, "max_bytes": 4096, "wait_seconds": 1}
		}},
		{"kill_command", func(_ *fixture, id string) map[string]any {
			return map[string]any{"command_id": id, "signal": "KILL"}
		}},
		{"list_commands", func(*fixture, string) map[string]any {
			return map[string]any{"server": longID, "status": "succeeded", "limit": 100}
		}},
		{"get_server_info", func(*fixture, string) map[string]any { return map[string]any{"server": longID} }},
		{"download_file", func(f *fixture, _ string) map[string]any {
			return map[string]any{"server": longID, "path": f.target(longID).Root + "/" + strings.Repeat("d", 200)}
		}},
		{"upload_file", func(f *fixture, _ string) map[string]any {
			return map[string]any{"server": longID, "path": f.target(longID).Root + "/" + strings.Repeat("u", 200), "content": "x"}
		}},
	} {
		t.Run("sad/"+tc.tool+" answers internal and does nothing", func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, withTarget(longID, sshtest.Options{}))
			g := f.start(t)
			g.waitReady(t)
			c := connect(t, g.edge(t))
			// A command for read_output and kill_command to name (a
			// command id is resolved before tool.called is written), and
			// a call that shows the length of this principal's
			// auth.accepted line.
			done := c.runCommand(t, longID, "echo done", map[string]any{"wait_seconds": 10})
			c.ok(t, "list_servers", map[string]any{}, &listServersResult{})
			authMax := lastAuthAcceptedMax(t, f)
			fi, err := os.Stat(f.audit)
			if err != nil {
				t.Fatal(err)
			}
			srv := f.target(longID)
			reqs, conns := sshRequests(srv)
			limitFileSize(t, g.pid, fi.Size()+int64(authMax))

			e := c.fail(t, tc.tool, tc.args(f, done.CommandID))
			if e.Code != "internal" || e.Message != "the audit log could not be written, so nothing was done" {
				t.Fatalf("error = %+v", e)
			}
			if r, cn := sshRequests(srv); r != reqs || cn != conns {
				t.Fatalf("the target saw %d requests and %d connections", r-reqs, cn-conns)
			}
			// The log is closed for good: the guard refuses the next
			// request before any tool (503).
			if _, err := c.callErr(t, "list_servers", map[string]any{}); err == nil {
				t.Fatal("a call after the failed write succeeded")
			}
			lines, rest := f.auditLog(t)
			// The last complete line is the request's auth.accepted; what
			// fitted of its tool.called (possibly nothing) is left
			// unterminated.
			if last := lines[len(lines)-1]; last.Event != "auth.accepted" || strings.Contains(rest, "\n") {
				t.Fatalf("last line %+v, unterminated rest %q", last, rest)
			}
			if code := g.stop(); code != 0 {
				t.Fatalf("exit %d", code)
			}
			if _, ok := g.waitLog(0, func(m map[string]any) bool { return m["msg"] == "gateway.stopped cannot be written to the audit log" }); !ok {
				t.Fatal("no log line saying gateway.stopped could not be written")
			}
		})
	}
}

// lastAuthAcceptedMax returns the length, newline included, that the last
// auth.accepted line of the audit log would have with the longest
// timestamp: no later auth.accepted line of the same principal is longer.
func lastAuthAcceptedMax(t *testing.T, f *fixture) int {
	t.Helper()
	b, err := os.ReadFile(f.audit)
	if err != nil {
		t.Fatal(err)
	}
	var last string
	for _, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if strings.Contains(l, `"event":"auth.accepted"`) {
			last = l
		}
	}
	var rec struct {
		TS string `json:"ts"`
	}
	if err := json.Unmarshal([]byte(last), &rec); err != nil || rec.TS == "" {
		t.Fatalf("auth.accepted line %q: %v", last, err)
	}
	return len(last) - len(rec.TS) + maxTSLen + 1
}

// TestE2EFileSizeLimit is the full-disk behaviour against the real binary
// (Phase 6 hardening): a normal user cannot mount a small file system, so
// the gateway's RLIMIT_FSIZE is lowered while a command writes, and the
// kernel refuses (EFBIG) every write that would take its output file past
// the limit, the way a full disk refuses it (ENOSPC). The command ends
// with status error, the output up to the limit stays readable to its end,
// its command.finished event is written, and the gateway keeps serving.
func TestE2EFileSizeLimit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	g := f.start(t)
	g.waitReady(t)
	c := connect(t, g.edge(t))
	srv := f.target("lab-1")
	const limit = 128 << 10
	// The command waits for a file the test creates once the limit is
	// set, then writes more than the limit.
	r := c.runCommand(t, "lab-1", "echo start; while [ ! -e go ]; do sleep 0.05; done; head -c 300000 /dev/zero | tr '\\0' b; echo end", map[string]any{"wait_seconds": 0})
	c.waitOutput(t, r.CommandID, "start")
	limitFileSize(t, g.pid, limit)
	if err := os.WriteFile(srv.Root+"/go", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Run("edge/the command ends error with its output kept up to the limit", func(t *testing.T) {
		c.waitStatus(t, r.CommandID, callBound)
		o := c.readOutput(t, r.CommandID, map[string]any{"offset": -4})
		if o.Status != "error" || o.ErrorCode != "internal" || o.Size != limit || o.Data != "bbbb" || !o.EOF {
			t.Fatalf("read = %+v", o)
		}
		found := false
		lines, _ := f.auditLog(t)
		for _, l := range lines {
			found = found || (l.Event == "command.finished" && l.CommandID == r.CommandID && l.Status == "error")
		}
		if !found {
			t.Fatal("no command.finished with status error")
		}
	})
	t.Run("happy/the gateway keeps serving", func(t *testing.T) {
		if r := c.runCommand(t, "lab-1", "echo after", map[string]any{"wait_seconds": 10}); r.Status != "succeeded" || r.Stdout != "after\n" {
			t.Fatalf("run = %+v", r)
		}
		if code := g.stop(); code != 0 {
			t.Fatalf("exit %d", code)
		}
	})
}
