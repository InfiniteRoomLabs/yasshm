package jobs

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/audit"
	"github.com/InfiniteRoomLabs/yasshm/internal/sshclient"
	"github.com/InfiniteRoomLabs/yasshm/internal/store"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/audittest"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/leakcheck"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/sshtest"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/tenanttest"
)

func TestStatuses(t *testing.T) {
	t.Parallel()
	t.Run("happy/exit 0 is succeeded, with output, events and files", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		r := h.start("echo hello; echo oops >&2", StartOptions{})
		if !store.ValidCommandID(r.ID) || r.Tenant != tA || r.Principal != "user:a@example.net" || !r.MergeStderr || r.TimeoutSeconds != 30 {
			t.Fatalf("start record %+v", r)
		}
		r = h.wait(r.ID)
		if r.Status != StatusSucceeded || r.ExitCode == nil || *r.ExitCode != 0 || r.MayStillRun || r.StartedAt.IsZero() || r.FinishedAt.IsZero() ||
			r.StdoutBytes != 6 || r.StderrBytes != 5 || r.StoredBytes != 11 {
			t.Fatalf("record %+v", r)
		}
		out := h.read(r.ID, StreamStdout)
		if !strings.Contains(out, "hello\n") || !strings.Contains(out, "oops\n") || len(out) != 11 {
			t.Fatalf("stdout %q", out)
		}
		if got := h.names(t, r.ID); !slices.Equal(got, []string{audit.EventCommandRequested, audit.EventCommandStarted, audit.EventCommandFinished}) {
			t.Fatalf("events %v", got)
		}
		ev := h.events(t, r.ID)
		if ev[0].Command != "echo hello; echo oops >&2" || ev[2].Status != "succeeded" || ev[0].Tenant != string(tA) {
			t.Fatalf("events %+v", ev)
		}
		dir := filepath.Join(h.dir, "tenants", string(tA), "commands", string(r.ID))
		for p, want := range map[string]os.FileMode{dir: 0o700, filepath.Dir(dir): 0o700, filepath.Join(dir, "stdout"): 0o600} {
			fi, err := os.Stat(p)
			if err != nil || fi.Mode().Perm() != want {
				t.Fatalf("%s: %v %v", p, fi, err)
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "stderr")); !os.IsNotExist(err) {
			t.Fatalf("a merged command has a stderr file: %v", err)
		}
		h.noSlots(h.e)
	})
	t.Run("happy/non-zero exit is failed with the code", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		r := h.wait(h.start("exit 3", StartOptions{}).ID)
		if r.Status != StatusFailed || r.ExitCode == nil || *r.ExitCode != 3 || r.Signal != "" {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("sad/a signal nobody here sent is failed with the signal", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		r := h.wait(h.start("kill -USR1 $$", StartOptions{}).ID)
		if r.Status != StatusFailed || r.Signal != "USR1" || r.ExitCode != nil {
			t.Fatalf("%+v", r)
		}
		if ev := h.events(t, r.ID); ev[len(ev)-1].Signal != "USR1" {
			t.Fatalf("%+v", ev)
		}
	})
	t.Run("sad/the timeout ends the command as timed_out", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		r := h.wait(h.start("sleep 30", StartOptions{TimeoutSeconds: 1}).ID)
		if r.Status != StatusTimedOut || r.Signal != "TERM" || r.MayStillRun {
			t.Fatalf("%+v", r)
		}
		if leakcheck.Poll(5*time.Second, func() bool { return h.sa.Running() == 0 }) == false {
			t.Fatal("the remote command still runs")
		}
	})
	t.Run("corner/a background child holding the output: timed_out, exit code kept, may still run", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		r := h.wait(h.start("sleep 30 & echo started", StartOptions{TimeoutSeconds: 1}).ID)
		if r.Status != StatusTimedOut || r.ExitCode == nil || *r.ExitCode != 0 || !r.MayStillRun {
			t.Fatalf("%+v", r)
		}
		if out := h.read(r.ID, StreamStdout); out != "started\n" {
			t.Fatalf("%q", out)
		}
	})
	t.Run("happy/kill delivers TERM and the record is killed", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		r := h.start("echo ready; sleep 30", StartOptions{})
		if _, err := h.e.ReadOutput(t.Context(), tA, r.ID, ReadRequest{Stream: StreamStdout, Wait: 10 * time.Second}); err != nil {
			t.Fatal(err)
		}
		kr, delivered, err := h.e.Kill(t.Context(), tA, h.actorA, r.ID, "")
		if err != nil || !delivered || kr.KillRequestedAt.IsZero() {
			t.Fatalf("%v %v %+v", delivered, err, kr)
		}
		r = h.wait(r.ID)
		if r.Status != StatusKilled || r.Signal != "TERM" || r.MayStillRun || r.KillRequestedAt.IsZero() {
			t.Fatalf("%+v", r)
		}
		if got := h.names(t, r.ID); !slices.Equal(got, []string{audit.EventCommandRequested, audit.EventCommandStarted, audit.EventCommandKillRequested, audit.EventCommandFinished}) {
			t.Fatalf("events %v", got)
		}
		again, delivered, err := h.e.Kill(t.Context(), tA, h.actorA, r.ID, SignalKill)
		if err != nil || delivered || again.Status != StatusKilled {
			t.Fatalf("second kill: %v %v %+v", delivered, err, again)
		}
		if n := len(h.names(t, r.ID)); n != 4 {
			t.Fatalf("a kill of a finished command was audited: %d events", n)
		}
	})
	t.Run("sad/a refused signal: not delivered, killed, may still run", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withServer(sshtest.Options{RefuseSignal: true}))
		r := h.start("sleep 30", StartOptions{})
		_, delivered, err := h.e.Kill(t.Context(), tA, h.actorA, r.ID, SignalInt)
		if err != nil || delivered {
			t.Fatalf("%v %v", delivered, err)
		}
		r = h.wait(r.ID)
		if r.Status != StatusKilled || !r.MayStillRun {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("sad/no exit status is error with ssh_unreachable and may still run", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withServer(sshtest.Options{NoExitStatus: true}))
		r := h.wait(h.start("echo x", StartOptions{}).ID)
		if r.Status != StatusError || r.ErrorCode != string(sshclient.CodeUnreachable) || !r.MayStillRun {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("sad/a dropped connection is error with ssh_unreachable", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withServer(sshtest.Options{DropMidStream: true}))
		r := h.wait(h.start("echo x; sleep 30", StartOptions{}).ID)
		if r.Status != StatusError || r.ErrorCode != string(sshclient.CodeUnreachable) || !r.MayStillRun {
			t.Fatalf("%+v", r)
		}
		if ev := h.events(t, r.ID); ev[len(ev)-1].ErrorCode != "ssh_unreachable" {
			t.Fatalf("%+v", ev)
		}
	})
}

func TestStartFailures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opts sshtest.Options
		code sshclient.Code
	}{
		{"sad/a changed host key is host_key_mismatch", sshtest.Options{WrongHostKey: true}, sshclient.CodeHostKeyMismatch},
		{"sad/a refused key is ssh_auth_failed", sshtest.Options{RefuseKey: true}, sshclient.CodeAuthFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, withServer(c.opts))
			r, err := h.e.Start(t.Context(), tA, h.actorA, "srv-1", "echo x", StartOptions{})
			if CodeOf(err) != Code(c.code) || r.Status != StatusError || r.ErrorCode != string(c.code) {
				t.Fatalf("%v %+v", err, r)
			}
			if strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), h.sa.ClientKeyFile) {
				t.Fatalf("the error names the host or key: %v", err)
			}
			if got := h.names(t, r.ID); !slices.Equal(got, []string{audit.EventCommandRequested, audit.EventCommandFinished}) {
				t.Fatalf("events %v", got)
			}
			h.noSlots(h.e)
		})
	}
	t.Run("sad/an unreachable server is ssh_unreachable", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withRefusingPort())
		r, err := h.e.Start(t.Context(), tA, h.actorA, "srv-1", "echo x", StartOptions{})
		if CodeOf(err) != Code(sshclient.CodeUnreachable) || r.Status != StatusError {
			t.Fatalf("%v %+v", err, r)
		}
		if r.ErrorCode != string(sshclient.CodeUnreachable) {
			t.Fatalf("%+v", r)
		}
		h.noSlots(h.e)
	})
	t.Run("sad/a failed command.requested write: error record, nothing sent, slot freed", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.au.SetFail(audit.EventCommandRequested, true)
		r, err := h.e.Start(t.Context(), tA, h.actorA, "srv-1", "echo x", StartOptions{})
		if !errors.Is(err, ErrAuditFailed) || CodeOf(err) != CodeInternal || r.ID != "" {
			t.Fatalf("%v %+v", err, r)
		}
		if h.sa.Accepted() != 0 {
			t.Fatal("the target was contacted")
		}
		page, err := h.e.List(t.Context(), tA, ListFilter{})
		if err != nil || len(page.Records) != 1 || page.Records[0].Status != StatusError || page.Records[0].ErrorCode != "internal" {
			t.Fatalf("%v %+v", err, page)
		}
		if _, err := os.Stat(filepath.Join(h.dir, "tenants")); !os.IsNotExist(err) {
			t.Fatal("output files were created")
		}
		h.noSlots(h.e)
	})
	t.Run("sad/output files that cannot be created: error internal, nothing sent", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.e.openOutput = func(string) (io.WriteCloser, error) { return nil, errors.New("no space (test)") }
		r, err := h.e.Start(t.Context(), tA, h.actorA, "srv-1", "echo x", StartOptions{})
		if CodeOf(err) != CodeInternal || r.Status != StatusError || h.sa.Accepted() != 0 {
			t.Fatalf("%v %+v", err, r)
		}
		h.noSlots(h.e)
	})
	t.Run("sad/a separate stderr file that cannot be created closes stdout", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		var n int
		h.e.openOutput = func(p string) (io.WriteCloser, error) {
			n++
			if n == 2 {
				return nil, errors.New("no space (test)")
			}
			return defaultOpenOutput(p)
		}
		r, err := h.e.Start(t.Context(), tA, h.actorA, "srv-1", "echo x", StartOptions{MergeStderr: boolp(false)})
		if CodeOf(err) != CodeInternal || r.Status != StatusError {
			t.Fatalf("%v %+v", err, r)
		}
		if open := openFilesUnder(t, h.dir+"/tenants"); len(open) != 0 {
			t.Fatalf("open files: %v", open)
		}
	})
	t.Run("sad/a disk error while writing ends the command as error internal", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.e.openOutput = func(string) (io.WriteCloser, error) {
			return os.OpenFile("/dev/full", os.O_WRONLY, 0)
		}
		r := h.wait(h.start("echo x; sleep 30", StartOptions{}).ID)
		if r.Status != StatusError || r.ErrorCode != "internal" {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("sad/a failing id source is internal", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.e.cfg.Rand = strings.NewReader("short")
		if _, err := h.e.Start(t.Context(), tA, h.actorA, "srv-1", "echo x", StartOptions{}); CodeOf(err) != CodeInternal {
			t.Fatal(err)
		}
		h.noSlots(h.e)
	})
}

func TestStartValidation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	long := strings.Repeat("x", 8193)
	cases := []struct {
		name    string
		tid     tenant.TenantID
		by      audit.Actor
		sid     tenant.ServerID
		command string
		opts    StartOptions
		code    Code
	}{
		{"sad/empty command", tA, h.actorA, "srv-1", "", StartOptions{}, CodeInvalidArgument},
		{"edge/command one byte over max_command_bytes", tA, h.actorA, "srv-1", long, StartOptions{}, CodeInvalidArgument},
		{"raging/NUL in the command", tA, h.actorA, "srv-1", "echo\x00x", StartOptions{}, CodeInvalidArgument},
		{"raging/invalid UTF-8", tA, h.actorA, "srv-1", "echo \xff", StartOptions{}, CodeInvalidArgument},
		{"edge/timeout over the maximum", tA, h.actorA, "srv-1", "true", StartOptions{TimeoutSeconds: 61}, CodeInvalidArgument},
		{"sad/negative timeout", tA, h.actorA, "srv-1", "true", StartOptions{TimeoutSeconds: -1}, CodeInvalidArgument},
		{"sad/unknown server", tA, h.actorA, "srv-9", "true", StartOptions{}, CodeNotFound},
		{"raging/hostile server id", tA, h.actorA, "../srv-1", "true", StartOptions{}, CodeNotFound},
		{"sad/unknown tenant", "tenant-z", h.actorA, "srv-1", "true", StartOptions{}, CodeNotFound},
		{"raging/a principal of another tenant", tA, h.actorB, "srv-1", "true", StartOptions{}, CodeInvalidArgument},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := h.e.Start(t.Context(), c.tid, c.by, c.sid, c.command, c.opts)
			if CodeOf(err) != c.code {
				t.Fatalf("err = %v, want %s", err, c.code)
			}
		})
	}
	t.Run("edge/exactly max_command_bytes and the maximum timeout are accepted", func(t *testing.T) {
		cmd := "true #" + strings.Repeat("x", 8192-6)
		r, err := h.e.Start(t.Context(), tA, h.actorA, "srv-1", cmd, StartOptions{TimeoutSeconds: 60})
		if err != nil {
			t.Fatal(err)
		}
		if r = h.wait(r.ID); r.Status != StatusSucceeded || r.Command != cmd {
			t.Fatalf("%+v", r.Status)
		}
	})
	if h.sa.Accepted() != 1 {
		t.Fatalf("%d connections; only the valid command may reach the target", h.sa.Accepted())
	}
	h.noSlots(h.e)
}

func TestBusy(t *testing.T) {
	t.Parallel()
	t.Run("sad/max_concurrent_per_server gives busy naming it, and frees on finish", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		a := h.start("sleep 30", StartOptions{})
		b := h.start("sleep 30", StartOptions{})
		_, err := h.e.Start(t.Context(), tA, h.actorA, "srv-1", "true", StartOptions{})
		var je *Error
		if !errors.As(err, &je) || je.Code() != CodeBusy || je.Limit() != LimitPerServer || !strings.Contains(err.Error(), LimitPerServer) {
			t.Fatalf("%v", err)
		}
		// Another server still has room.
		other, err := h.e.Start(t.Context(), tA, h.actorA, "srv-2", "true", StartOptions{})
		if err != nil {
			t.Fatal(err)
		}
		h.wait(other.ID)
		for _, r := range []Record{a, b} {
			if _, _, err := h.e.Kill(t.Context(), tA, h.actorA, r.ID, SignalKill); err != nil {
				t.Fatal(err)
			}
			h.wait(r.ID)
		}
		h.noSlots(h.e)
		h.wait(h.start("true", StartOptions{}).ID)
	})
	t.Run("sad/max_concurrent_commands gives busy naming it; another tenant is not affected", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withLimits(func(l *tenant.Limits) { l.MaxConcurrentCommands = 2 }))
		a := h.start("sleep 30", StartOptions{})
		b, err := h.e.Start(t.Context(), tA, h.actorA, "srv-2", "sleep 30", StartOptions{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = h.e.Start(t.Context(), tA, h.actorA, "srv-2", "true", StartOptions{})
		var je *Error
		if !errors.As(err, &je) || je.Limit() != LimitPerTenant || !errors.Is(err, CodeBusy) {
			t.Fatalf("%v", err)
		}
		rb, err := h.e.Start(t.Context(), tB, h.actorB, "srv-1", "true", StartOptions{})
		if err != nil {
			t.Fatalf("tenant b is blocked by tenant a: %v", err)
		}
		if _, err := h.e.Wait(t.Context(), tB, rb.ID, 30*time.Second); err != nil {
			t.Fatal(err)
		}
		for _, r := range []Record{a, b} {
			_, _, _ = h.e.Kill(t.Context(), tA, h.actorA, r.ID, SignalKill)
			h.wait(r.ID)
		}
		h.noSlots(h.e)
	})
}

func TestRecover(t *testing.T) {
	t.Parallel()
	t.Run("happy/an engine dropped mid-command: the next one marks it lost and keeps its output", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		r := h.start("echo partial; sleep 30", StartOptions{})
		if _, err := h.e.ReadOutput(t.Context(), tA, r.ID, ReadRequest{Stream: StreamStdout, Wait: 10 * time.Second}); err != nil {
			t.Fatal(err)
		}
		// The first engine dies without Shutdown: its store is closed (its
		// lock released, so nothing it does later reaches the database) and
		// its goroutines are left to the test's cleanup. A second one opens
		// the same data directory, as a restarted gateway would.
		_ = h.e.st.Close(t.Context())
		e2 := h.open()
		got, err := e2.Get(t.Context(), tA, r.ID)
		if err != nil || got.Status != StatusLost || got.FinishedAt.IsZero() || !got.MayStillRun || got.StoredBytes != 8 {
			t.Fatalf("%v %+v", err, got)
		}
		o, err := e2.ReadOutput(t.Context(), tA, r.ID, ReadRequest{Stream: StreamStdout})
		if err != nil || string(o.Data) != "partial\n" || !o.EOF {
			t.Fatalf("%v %+v", err, o)
		}
		ev := h.events(t, r.ID)
		if last := ev[len(ev)-1]; last.Event != audit.EventCommandFinished || last.Status != "lost" {
			t.Fatalf("%+v", ev)
		}
		h.noSlots(e2)
		r2, err := e2.Start(t.Context(), tA, h.actorA, "srv-1", "echo again", StartOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if w, _ := e2.Wait(t.Context(), tA, r2.ID, 30*time.Second); w.Status != StatusSucceeded {
			t.Fatalf("%+v", w)
		}
		if _, err := e2.Recover(t.Context()); CodeOf(err) != CodeInternal {
			t.Fatalf("Recover after a start: %v", err)
		}
		// The first engine's command ends at its shutdown; its record stays
		// lost.
		_ = h.e.Shutdown(t.Context())
		if got, _ := e2.Get(t.Context(), tA, r.ID); got.Status != StatusLost {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("sad/a database newer than the binary stops New", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		if err := h.e.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
		bump(t, h.dir)
		if _, err := New(t.Context(), h.cfg); !errors.Is(err, store.ErrTooNew) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestShutdown(t *testing.T) {
	t.Parallel()
	t.Run("happy/ends running commands as killed and leaves nothing behind", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		var ids []tenant.CommandID
		for range 2 {
			ids = append(ids, h.start("sleep 30", StartOptions{}).ID)
		}
		rb, err := h.e.Start(t.Context(), tB, h.actorB, "srv-1", "sleep 30", StartOptions{MergeStderr: boolp(false)})
		if err != nil {
			t.Fatal(err)
		}
		if err := h.e.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := h.e.Shutdown(t.Context()); err != nil {
			t.Fatal("second shutdown:", err)
		}
		if _, err := h.e.Start(t.Context(), tA, h.actorA, "srv-1", "true", StartOptions{}); !errors.Is(err, ErrShuttingDown) {
			t.Fatalf("start after shutdown: %v", err)
		}
		if _, err := h.e.Get(t.Context(), tA, ids[0]); !errors.Is(err, ErrShuttingDown) {
			t.Fatalf("get after shutdown: %v", err)
		}
		if err := h.e.Sweep(t.Context()); !errors.Is(err, ErrShuttingDown) {
			t.Fatal(err)
		}
		if open := openFilesUnder(t, h.dir); len(open) != 0 {
			t.Fatalf("open files: %v", open)
		}
		if h.sa.Conns() != 0 || h.sb.Conns() != 0 {
			t.Fatalf("connections left: %d %d", h.sa.Conns(), h.sb.Conns())
		}
		if !leakcheck.Poll(5*time.Second, func() bool { return h.sa.Running() == 0 && h.sb.Running() == 0 }) {
			t.Fatal("remote commands still run")
		}
		h.noSlots(h.e)
		e2 := h.open()
		for _, c := range []struct {
			tid tenant.TenantID
			id  tenant.CommandID
		}{{tA, ids[0]}, {tA, ids[1]}, {tB, rb.ID}} {
			r, err := e2.Get(t.Context(), c.tid, c.id)
			if err != nil || r.Status != StatusKilled {
				t.Fatalf("%v %+v", err, r)
			}
		}
	})
	t.Run("corner/past the grace the connections are closed and the record says may still run", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withServer(sshtest.Options{SilentSignal: true}), withSSH(tenanttest.ShortSSHOptions(tenant.Limits{})))
		r := h.start("sleep 30", StartOptions{})
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		if err := h.e.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
		e2 := h.open()
		got, err := e2.Get(t.Context(), tA, r.ID)
		if err != nil || got.Status != StatusKilled || !got.MayStillRun {
			t.Fatalf("%v %+v", err, got)
		}
		if h.sa.Conns() != 0 {
			t.Fatal("connection left")
		}
	})
	t.Run("edge/a second Shutdown waits for the first and honours its context", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withServer(sshtest.Options{SilentSignal: true}), withSSH(tenanttest.ShortSSHOptions(tenant.Limits{})))
		h.start("sleep 30", StartOptions{})
		first := make(chan error, 1)
		go func() { first <- h.e.Shutdown(context.Background()) }()
		if !leakcheck.Poll(5*time.Second, func() bool { h.e.mu.Lock(); defer h.e.mu.Unlock(); return h.e.closing }) {
			t.Fatal("shutdown did not start")
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := h.e.Shutdown(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		if err := <-first; err != nil {
			t.Fatal(err)
		}
	})
}

func TestNew(t *testing.T) {
	t.Parallel()
	base := func(t *testing.T) Config {
		return Config{DataDir: leakcheck.PrivDir(t), Audit: audittest.Detached()}
	}
	t.Run("sad/bad configurations are refused", func(t *testing.T) {
		t.Parallel()
		cases := map[string]func(c *Config){
			"no audit":      func(c *Config) { c.Audit = nil },
			"relative dir":  func(c *Config) { c.DataDir = "data" },
			"bad tenant id": func(c *Config) { c.Tenants = []tenant.Tenant{{ID: "../x"}} },
			"duplicate":     func(c *Config) { c.Tenants = []tenant.Tenant{{ID: tA}, {ID: tA}} },
			"unsafe dir": func(c *Config) {
				if err := os.Chmod(c.DataDir, 0o755); err != nil { //nolint:gosec // G302: the test needs the unsafe mode
					t.Fatal(err)
				}
			},
		}
		for name, mut := range cases {
			c := base(t)
			mut(&c)
			if e, err := New(t.Context(), c); err == nil {
				_ = e.Shutdown(t.Context())
				t.Errorf("%s: accepted", name)
			}
		}
	})
	t.Run("happy/defaults apply and an engine with no tenants shuts down", func(t *testing.T) {
		t.Parallel()
		e, err := New(t.Context(), base(t))
		if err != nil {
			t.Fatal(err)
		}
		if e.cfg.JanitorInterval != DefaultJanitorInterval || e.cfg.Rand == nil || e.cfg.Now == nil {
			t.Fatalf("%+v", e.cfg)
		}
		e.cfg.OnError(errors.New("discarded"))
		if err := e.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

// bump sets the database's schema version past the binary's.
func bump(t *testing.T, dir string) {
	t.Helper()
	s, err := store.Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := setUserVersion(t, filepath.Join(dir, store.FileName), 99); err != nil {
		t.Fatal(err)
	}
}

// setUserVersion writes PRAGMA user_version into the database at path.
func setUserVersion(t *testing.T, path string, v int) error {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	_, err = db.ExecContext(t.Context(), "PRAGMA user_version = "+strconv.Itoa(v))
	return err
}

// TestSignalReplyLost: the server ends the command and closes the channel
// before it answers the signal request. Kill reports the signal delivered
// (the command ended by it), the record is killed with that signal, and
// neither a kill nor a timeout leaves "may still run".
func TestSignalReplyLost(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withServer(sshtest.Options{ExitBeforeSignalReply: true}))
	t.Run("corner/a kill whose reply was lost is delivered", func(t *testing.T) {
		r := h.start("echo ready; sleep 30", StartOptions{})
		if _, err := h.e.ReadOutput(t.Context(), tA, r.ID, ReadRequest{Stream: StreamStdout, Wait: 10 * time.Second}); err != nil {
			t.Fatal(err)
		}
		_, delivered, err := h.e.Kill(t.Context(), tA, h.actorA, r.ID, "")
		if err != nil || !delivered {
			t.Fatalf("delivered %v err %v", delivered, err)
		}
		got := h.wait(r.ID)
		if got.Status != StatusKilled || got.Signal != "TERM" || got.MayStillRun {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("corner/a timeout whose reply was lost is timed_out and over", func(t *testing.T) {
		got := h.wait(h.start("sleep 30", StartOptions{TimeoutSeconds: 1}).ID)
		if got.Status != StatusTimedOut || got.Signal != "TERM" || got.MayStillRun {
			t.Fatalf("%+v", got)
		}
	})
}
