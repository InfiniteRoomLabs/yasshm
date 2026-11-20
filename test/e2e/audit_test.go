package e2e

import (
	"encoding/json"
	"net/http"
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
		args func(f *fixture, ids commandIDs) map[string]any
	}{
		{"run_command", func(*fixture, commandIDs) map[string]any {
			return map[string]any{"server": longID, "command": "echo " + strings.Repeat("x", 2000)}
		}},
		{"read_output", func(_ *fixture, ids commandIDs) map[string]any {
			return map[string]any{"command_id": ids.done, "stream": "stderr", "offset": 12345, "max_bytes": 4096, "wait_seconds": 1}
		}},
		{"kill_command", func(_ *fixture, ids commandIDs) map[string]any {
			return map[string]any{"command_id": ids.running, "signal": "KILL"}
		}},
		{"list_commands", func(*fixture, commandIDs) map[string]any {
			return map[string]any{"server": longID, "status": "succeeded", "limit": 100}
		}},
		{"get_server_info", func(*fixture, commandIDs) map[string]any { return map[string]any{"server": longID} }},
		{"download_file", func(f *fixture, _ commandIDs) map[string]any {
			return map[string]any{"server": longID, "path": f.target(longID).Root + "/" + strings.Repeat("d", 200)}
		}},
		{"upload_file", func(f *fixture, _ commandIDs) map[string]any {
			return map[string]any{"server": longID, "path": f.target(longID).Root + "/" + strings.Repeat("u", 200), "content": "x"}
		}},
	} {
		t.Run("sad/"+tc.tool+" answers internal and does nothing", func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, withTarget(longID, sshtest.Options{}))
			g, c := f.serve(t)
			// A command for read_output and kill_command to name, so a
			// call that went ahead would act on a real command, and
			// a call that shows the length of this principal's
			// auth.accepted line.
			ids := commandIDs{done: c.runCommand(t, longID, "echo done", map[string]any{"wait_seconds": 10}).CommandID}
			if tc.tool == "kill_command" {
				// A running command, so a kill that went ahead would
				// reach the target as a signal request.
				ids.running = c.runCommand(t, longID, "echo up; sleep 300", map[string]any{"wait_seconds": 0}).CommandID
				c.waitOutput(t, ids.running, "up")
			}
			c.ok(t, "list_servers", map[string]any{}, &listServersResult{})
			authMax := lastAuthAcceptedMax(t, f)
			fi, err := os.Stat(f.audit)
			if err != nil {
				t.Fatal(err)
			}
			srv := f.target(longID)
			reqs, conns := sshRequests(srv)
			limitFileSize(t, g.pid, fi.Size()+int64(authMax))

			e := c.fail(t, tc.tool, tc.args(f, ids))
			if e.Code != "internal" || e.Message != "the audit log could not be written, so nothing was done" {
				t.Fatalf("error = %+v", e)
			}
			if r, cn := sshRequests(srv); r != reqs || cn != conns {
				t.Fatalf("the target saw %d requests and %d connections: %v", r-reqs, cn-conns, srv.Requests()[reqs:])
			}
			if ids.running != "" && srv.Running() == 0 {
				t.Fatal("the command the refused kill named is no longer running")
			}
			// The log is closed for good: the guard refuses the next
			// request before any tool, with 503.
			if status, body := httpDo(t, http.MethodPost, g.edge(t).URL+"/mcp", mcpHeader(), []byte(initializeBody)); status != http.StatusServiceUnavailable || strings.TrimSpace(body) != `{"error":"unavailable"}` {
				t.Fatalf("a request after the failed write: %d %s", status, body)
			}
			lines, rest := f.auditLog(t)
			// The last complete line is the request's auth.accepted; what
			// fitted of its tool.called (possibly nothing) is left
			// unterminated.
			if last := lines[len(lines)-1]; last.Event != "auth.accepted" || strings.Contains(rest, "\n") {
				t.Fatalf("last line %+v, unterminated rest %q", last, rest)
			}
			g.stop(t)
			if _, ok := g.waitLog(0, func(m map[string]any) bool { return m["msg"] == "gateway.stopped cannot be written to the audit log" }); !ok {
				t.Fatal("no log line saying gateway.stopped could not be written")
			}
		})
	}
}

// commandIDs are the commands a refused call may name: one that finished
// and, for kill_command, one still running.
type commandIDs struct{ done, running string }

// TestE2EAuditUnwritableAtRequested: the tools layer wrote tool.called,
// then the job engine cannot write command.requested: run_command answers
// internal and no exec reaches the target. The limit is set so that the
// call's auth.accepted and tool.called fit and nothing more. The audit log
// is first padded with denied calls (they write the audit log only), so the
// limit is above the database's size and the record insert before
// command.requested still succeeds.
func TestE2EAuditUnwritableAtRequested(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	g, c := f.serve(t)
	command := "echo " + strings.Repeat("r", 3000)
	c.runCommand(t, "lab-1", command, map[string]any{"wait_seconds": 10})
	toolMax := lastLineMax(t, f, `"event":"tool.called"`)
	for range 40 {
		c.fail(t, "run_command", map[string]any{"server": "lab-1", "command": "sudo " + strings.Repeat("p", 6000)})
	}
	authMax := lastAuthAcceptedMax(t, f)
	fi, err := os.Stat(f.audit)
	if err != nil {
		t.Fatal(err)
	}
	srv := f.target("lab-1")
	reqs, conns := sshRequests(srv)
	limitFileSize(t, g.pid, fi.Size()+int64(authMax+toolMax))
	t.Run("sad/run_command stops at command.requested", func(t *testing.T) {
		e := c.fail(t, "run_command", map[string]any{"server": "lab-1", "command": command})
		if e.Code != "internal" || e.Message != "the audit log could not be written, so nothing was done" {
			t.Fatalf("error = %+v", e)
		}
		if r, cn := sshRequests(srv); r != reqs || cn != conns {
			t.Fatalf("the target saw %d requests and %d connections: %v", r-reqs, cn-conns, srv.Requests()[reqs:])
		}
		lines, _ := f.auditLog(t)
		if last := lines[len(lines)-1]; last.Event != "tool.called" || last.Tool != "run_command" {
			t.Fatalf("last complete line %+v, want the call's tool.called", last)
		}
	})
	g.stop(t)
}

// lastLineMax is lastAuthAcceptedMax for the last line holding marker.
func lastLineMax(t *testing.T, f *fixture, marker string) int {
	t.Helper()
	b, err := os.ReadFile(f.audit)
	if err != nil {
		t.Fatal(err)
	}
	var last string
	for _, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if strings.Contains(l, marker) {
			last = l
		}
	}
	var rec struct {
		TS string `json:"ts"`
	}
	if err := json.Unmarshal([]byte(last), &rec); err != nil || rec.TS == "" {
		t.Fatalf("line %q: %v", last, err)
	}
	return len(last) - len(rec.TS) + maxTSLen + 1
}

// lastAuthAcceptedMax returns the length, newline included, that the last
// auth.accepted line of the audit log would have with the longest
// timestamp: no later auth.accepted line of the same principal is longer.
func lastAuthAcceptedMax(t *testing.T, f *fixture) int {
	t.Helper()
	return lastLineMax(t, f, `"event":"auth.accepted"`)
}

// TestE2EFileSizeLimit is the full-disk behaviour against the real binary
// (Phase 6 hardening): a normal user cannot mount a small file system, so
// the gateway's RLIMIT_FSIZE is lowered while a command writes, and the
// kernel refuses (EFBIG) every write that would take its output file past
// the limit, the way a full disk refuses it (ENOSPC). The command ends
// with status error, the output up to the limit stays readable to its end,
// its command.finished event is written, and the gateway keeps serving.
// The limit binds the gateway's SQLite files too: the scenario holds only
// while the database and its WAL stay under 128 KiB (about 45 KiB here),
// since "keeps serving" inserts a record after the limit is set.
func TestE2EFileSizeLimit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	g, c := f.serve(t)
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
		if !f.finished(t, r.CommandID, "error") {
			t.Fatal("no command.finished with status error")
		}
	})
	t.Run("happy/the gateway keeps serving", func(t *testing.T) {
		if r := c.runCommand(t, "lab-1", "echo after", map[string]any{"wait_seconds": 10}); r.Status != "succeeded" || r.Stdout != "after\n" {
			t.Fatalf("run = %+v", r)
		}
		g.stop(t)
	})
}
