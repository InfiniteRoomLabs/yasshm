package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/configtest"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/leakcheck"
)

// TestE2ELostAfterKill: the gateway is killed with SIGKILL while a command
// runs; a gateway restarted on the same data directory reports the command
// lost (spec 5.6) and its captured output is still readable.
func TestE2ELostAfterKill(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	g, c := f.serve(t)
	r := c.runCommand(t, "lab-1", "echo before-the-kill; sleep 300", map[string]any{"wait_seconds": 0})
	c.waitOutput(t, r.CommandID, "before-the-kill")

	t.Run("raging/SIGKILL mid-command", func(t *testing.T) {
		g.signal(syscall.SIGKILL)
		code, ok := g.wait(stopBound)
		if !ok || code != -1 {
			t.Fatalf("exit %d (exited %v), want -1 (killed by a signal)", code, ok)
		}
		lines, _ := f.auditLog(t)
		if last := lines[len(lines)-1]; last.Event == "gateway.stopped" {
			t.Fatal("a killed gateway wrote gateway.stopped")
		}
	})
	g2, c2 := f.serve(t)
	t.Run("happy/the restarted gateway reports lost with the output", func(t *testing.T) {
		o := c2.readOutput(t, r.CommandID, nil)
		if o.Status != "lost" || o.Data != "before-the-kill\n" || !o.EOF {
			t.Fatalf("read = %+v", o)
		}
		var l listCommandsResult
		c2.ok(t, "list_commands", map[string]any{"status": "lost"}, &l)
		if len(l.Commands) != 1 || l.Commands[0].CommandID != r.CommandID {
			t.Fatalf("list = %+v", l)
		}
		if !f.finished(t, r.CommandID, "lost") {
			t.Fatal("no command.finished with status lost")
		}
	})
	t.Run("happy/the restarted gateway stops cleanly", func(t *testing.T) {
		g2.stop(t)
	})
	// A lost command may still run on the target (that is what lost
	// means: the gateway no longer holds its session). The target ends it
	// here so the scenario's leak check sees an empty target.
	f.target("lab-1").Close()
}

// TestE2EGracefulShutdown: on SIGTERM a running command ends killed, a
// request waiting on it answers at once, the exit code is 0 and
// gateway.stopped (reason signal) is the last audit event; a restarted
// gateway lists the command as killed.
func TestE2EGracefulShutdown(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	g, c := f.serve(t)
	r := c.runCommand(t, "lab-1", "echo up; sleep 300", map[string]any{"wait_seconds": 0})
	c.waitOutput(t, r.CommandID, "up")

	type answer struct {
		res *mcp.CallToolResult
		err error
		at  time.Time
	}
	readCalls := func() int {
		return len(f.events(t, func(l auditLine) bool { return l.Event == "tool.called" && l.Tool == "read_output" }))
	}
	before := readCalls()
	waiting := make(chan answer, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), callBound)
		defer cancel()
		res, err := c.cs.CallTool(ctx, &mcp.CallToolParams{Name: "read_output", Arguments: map[string]any{"command_id": r.CommandID, "offset": len("up\n"), "wait_seconds": 60}})
		waiting <- answer{res, err, time.Now()}
	}()
	// The read is in the gateway once its tool.called is in the audit log
	// (written before it starts to wait).
	if !leakcheck.Poll(callBound, func() bool { return readCalls() > before }) {
		t.Fatal("the waiting read never reached the gateway")
	}
	var sent time.Time
	t.Run("happy/SIGTERM exits 0", func(t *testing.T) {
		sent = time.Now()
		g.stop(t)
	})
	t.Run("happy/the waiting request answered at once", func(t *testing.T) {
		select {
		case a := <-waiting:
			if a.err != nil {
				t.Fatalf("the waiting read failed: %v", a.err)
			}
			if took := a.at.Sub(sent); took > 15*time.Second {
				t.Fatalf("the waiting read answered %v after SIGTERM", took)
			}
			// Either the wait saw the command end (killed), or the read
			// had not started waiting when the engine began to shut down
			// and was told to retry (spec 5.8 mapping of
			// jobs.ErrShuttingDown). Both answer at once.
			if a.res.IsError {
				if e := asError(t, "read_output", a.res); e.Code != "internal" || e.Message != "shutting down, retry later" {
					t.Fatalf("read = %+v", e)
				}
				return
			}
			var o readResult
			decodeResult(t, "read_output", a.res, &o)
			if o.Status != "killed" {
				t.Fatalf("read = %+v", o)
			}
		case <-time.After(callBound):
			t.Fatal("the waiting read never answered")
		}
	})
	t.Run("happy/the command ended killed and gateway.stopped is last", func(t *testing.T) {
		lines, rest := f.auditLog(t)
		if rest != "" {
			t.Fatalf("an unterminated audit line: %q", rest)
		}
		found := f.finished(t, r.CommandID, "killed")
		last := lines[len(lines)-1]
		if !found || last.Event != "gateway.stopped" || last.Reason != "signal" {
			t.Fatalf("command.finished killed: %v; last event %+v", found, last)
		}
		if lines[0].Event != "gateway.started" || !strings.HasPrefix(lines[0].Listen, "127.0.0.1:") {
			t.Fatalf("first event %+v", lines[0])
		}
	})
	t.Run("happy/a restarted gateway lists the command as killed", func(t *testing.T) {
		g2, c2 := f.serve(t)
		var l listCommandsResult
		c2.ok(t, "list_commands", map[string]any{"server": "lab-1"}, &l)
		if len(l.Commands) != 1 || l.Commands[0].Status != "killed" {
			t.Fatalf("list = %+v", l)
		}
		g2.stop(t)
	})
}

// TestE2ESecondGateway: a second gateway on the same data directory
// refuses to start with exit 1 and a clear message, and the first keeps
// serving.
func TestE2ESecondGateway(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	g := f.start(t)
	g.waitReady(t)
	t.Run("sad/the second gateway exits 1", func(t *testing.T) {
		code, out, errs := run(t, "serve", "--config", f.config)
		if code != 1 || out != "" || !strings.Contains(errs, `"msg":"start-up failed"`) || !strings.Contains(errs, `"reason":"the data directory is in use by another gateway"`) {
			t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errs)
		}
	})
	t.Run("happy/the first gateway still serves", func(t *testing.T) {
		c := connect(t, g.edge(t))
		if r := c.runCommand(t, "lab-1", "echo still-here", map[string]any{"wait_seconds": 10}); r.Stdout != "still-here\n" {
			t.Fatalf("run = %+v", r)
		}
		g.stop(t)
	})
}

// TestE2EConfigCheck: `yasshm config check` on a valid file exits 0 with
// one line, and on invalid files exits 1 with the error lines (spec 5.9);
// a usage error exits 2.
func TestE2EConfigCheck(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	t.Run("happy/a valid file", func(t *testing.T) {
		code, out, errs := run(t, "config", "check", "--config", f.config)
		if code != 0 || out != "config ok: 1 tenant, 1 server\n" || errs != "" {
			t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errs)
		}
	})
	for _, tc := range []struct {
		name, old, repl string
		want            []string
	}{
		{"sad/a non-loopback listen address", "listen: 127.0.0.1:0", "listen: 192.0.2.1:8822",
			[]string{"server.listen: must be a loopback IP address"}},
		{"sad/an unknown key", "  audit_log:", "  colour: blue\n  audit_log:",
			[]string{"server.colour"}},
		{"sad/access full is not supported", "access: look}", "access: full}",
			[]string{"access level full is not supported in this version"}},
		{"sad/a limit out of range", "limits: {", "limits: {max_wait_seconds: 500, ",
			[]string{"limits.max_wait_seconds: must be between 1 and 110"}},
		{"sad/a malformed host key", `host_key: "ssh-ed25519 `, `host_key: "ssh-ed25519 !!!`,
			[]string{"(lab-1).host_key: does not parse as an OpenSSH public key"}},
		{"raging/a missing key file", "private_key_file: /", "private_key_file: /nonexistent/",
			[]string{"identities.id-0.private_key_file: file does not exist"}},
		{"raging/a YAML anchor", "version: 1", "version: &v 1",
			[]string{"version: anchors are not supported"}},
		{"raging/a YAML alias", "version: 1\nserver:\n  listen: 127.0.0.1:0", "version: &v 1\nserver:\n  listen: *v",
			[]string{"server.listen: aliases are not supported", "anchors are not supported"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := configtest.Files{Config: f.config}.Rewrite(t, tc.old, tc.repl)
			code, out, errs := run(t, "config", "check", "--config", p)
			if code != 1 || out != "" {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errs)
			}
			for _, w := range tc.want {
				if !strings.Contains(errs, w) {
					t.Fatalf("stderr %q does not hold %q", errs, w)
				}
			}
		})
	}
	t.Run("sad/a file that does not exist", func(t *testing.T) {
		code, out, errs := run(t, "config", "check", "--config", filepath.Join(t.TempDir(), "absent.yaml"))
		if code != 1 || out != "" || errs == "" {
			t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errs)
		}
	})
	t.Run("edge/no --config is a usage error", func(t *testing.T) {
		code, _, errs := run(t, "config", "check")
		if code != 2 || !strings.Contains(errs, "--config") {
			t.Fatalf("exit %d, stderr %q", code, errs)
		}
	})
	t.Run("happy/version prints the stamped metadata", func(t *testing.T) {
		code, out, _ := run(t, "version")
		if code != 0 || out != "yasshm "+buildVersion+"\ncommit "+buildCommit+"\nbuilt "+buildDate+"\n" {
			t.Fatalf("exit %d, stdout %q", code, out)
		}
	})
}

// TestE2ETwoTenantsRefused: the two-tenant isolation scenario of spec 7
// cannot run against the binary, because the loader accepts exactly one
// tenant (D7, MaxTenants) and the shipped binary has no test-only way
// around it; it runs in-process (spec 7 callout). What the binary can
// prove: a config with two tenants is refused by config check and by
// serve, with the loader's message.
func TestE2ETwoTenantsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t, withTwoTenants())
	const msg = "tenants: at most 1 tenant is supported in this version (MaxTenants), got 2"
	t.Run("sad/config check refuses two tenants", func(t *testing.T) {
		code, out, errs := run(t, "config", "check", "--config", f.config)
		if code != 1 || out != "" || !strings.Contains(errs, msg) {
			t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errs)
		}
	})
	t.Run("sad/serve refuses two tenants", func(t *testing.T) {
		code, _, errs := run(t, "serve", "--config", f.config)
		if code != 1 || !strings.Contains(errs, `"msg":"config problem"`) || !strings.Contains(errs, msg) ||
			!strings.Contains(errs, `"reason":"the config is not valid"`) {
			t.Fatalf("exit %d, stderr %q", code, errs)
		}
		if _, err := os.Stat(f.audit); !os.IsNotExist(err) {
			t.Fatalf("serve opened the audit log of a refused config: %v", err)
		}
	})
}

// TestE2ESIGHUPIgnored: a SIGHUP (a log-rotation hook's habit) does not
// stop the gateway: it writes one log line, keeps serving, and still stops
// cleanly on SIGTERM. The gateway also warns at start that certs_url (set
// in every scenario's config) replaces the team domain's key set.
func TestE2ESIGHUPIgnored(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	g, c := f.serve(t)
	t.Run("happy/certs_url is announced at start", func(t *testing.T) {
		if _, ok := g.waitLog(0, func(m map[string]any) bool {
			return m["level"] == "WARN" && m["msg"] == "certs_url is set: signing keys are fetched from it instead of the team domain"
		}); !ok {
			t.Fatal("no certs_url warning")
		}
	})
	t.Run("happy/SIGHUP is logged and ignored", func(t *testing.T) {
		g.signal(syscall.SIGHUP)
		if _, ok := g.waitLog(startBound, func(m map[string]any) bool {
			return m["level"] == "WARN" && strings.HasPrefix(fmt.Sprint(m["msg"]), "SIGHUP ignored")
		}); !ok {
			t.Fatal("no SIGHUP log line")
		}
		if r := c.runCommand(t, "lab-1", "echo after-hup", map[string]any{"wait_seconds": 10}); r.Stdout != "after-hup\n" {
			t.Fatalf("run = %+v", r)
		}
		g.stop(t)
		if lines, _ := f.auditLog(t); lines[len(lines)-1].Event != "gateway.stopped" || lines[len(lines)-1].Reason != "signal" {
			t.Fatalf("last event %+v", lines[len(lines)-1])
		}
	})
}
