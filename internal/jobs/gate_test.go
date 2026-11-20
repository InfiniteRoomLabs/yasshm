package jobs

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/sshclient"
	"github.com/InfiniteRoomLabs/yasshm/internal/store"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/sshtest"
)

// The tests in this file came out of the Phase 3 review gate.

func withSSH(o sshclient.Options) opt {
	return func(_ *harness, cfg *Config) {
		cfg.SSHOptions = func(tenant.Limits) sshclient.Options { return o }
	}
}

// setHooks replaces the engine's store-write hooks and Kill hook.
func (e *Engine) setHooks(f func(h *hooks)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	f(&e.hk)
}

var errDiskFull = errors.New("database or disk is full (test)")

func TestUnansweredStart(t *testing.T) {
	t.Parallel()
	t.Run("sad/a start the server never answered: error, the SSH code, may still run", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withServer(sshtest.Options{SilentExec: true}),
			withSSH(sshclient.Options{ConnectTimeout: 300 * time.Millisecond, KillGrace: 300 * time.Millisecond, KillWait: time.Second}))
		r, err := h.e.Start(t.Context(), tA, h.actorA, "srv-1", "true", StartOptions{})
		if CodeOf(err) != Code(sshclient.CodeUnreachable) || r.Status != StatusError || r.ErrorCode != string(sshclient.CodeUnreachable) || !r.MayStillRun {
			t.Fatalf("%v %+v", err, r)
		}
		h.noSlots(h.e)
	})
}

func TestDataQuota(t *testing.T) {
	t.Parallel()
	t.Run("sad/Start is busy with max_data_bytes when the tenant's usage is at its quota", func(t *testing.T) {
		t.Parallel()
		const cmd = "printf 0123456789"
		per := int64(10 + len(cmd) + store.RowCost)
		h := newHarness(t, withLimits(func(l *tenant.Limits) { l.MaxDataBytes = per; l.MaxOutputBytes = 10 }))
		h.wait(h.start(cmd, StartOptions{}).ID)
		_, err := h.e.Start(t.Context(), tA, h.actorA, "srv-1", cmd, StartOptions{})
		var je *Error
		if !errors.As(err, &je) || je.Code() != CodeBusy || je.Limit() != LimitData {
			t.Fatalf("err = %v", err)
		}
		rb, err := h.e.Start(t.Context(), tB, h.actorB, "srv-1", cmd, StartOptions{})
		if err != nil {
			t.Fatalf("tenant b is refused for tenant a's quota: %v", err)
		}
		if _, err := h.e.Wait(t.Context(), tB, rb.ID, 30*time.Second); err != nil {
			t.Fatal(err)
		}
		h.noSlots(h.e)
	})
	t.Run("happy/records without output count against the quota", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withLimits(func(l *tenant.Limits) { l.MaxDataBytes = 3 * (4 + store.RowCost) }))
		for range 3 {
			h.wait(h.start("true", StartOptions{}).ID)
		}
		if _, err := h.e.Start(t.Context(), tA, h.actorA, "srv-1", "true", StartOptions{}); !errors.Is(err, CodeBusy) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("happy/the janitor counts what running commands have stored", func(t *testing.T) {
		t.Parallel()
		const done, running = "printf 0123456789", "printf 0123456789; sleep 30"
		quota := int64(10+len(done)+store.RowCost) + int64(len(running)+store.RowCost) + 5
		h := newHarness(t, withLimits(func(l *tenant.Limits) { l.MaxDataBytes = quota }))
		old := h.wait(h.start(done, StartOptions{}).ID)
		r := h.start(running, StartOptions{})
		if o, err := h.e.ReadOutput(t.Context(), tA, r.ID, ReadRequest{Stream: StreamStdout, Offset: 9, Wait: 10 * time.Second}); err != nil || len(o.Data) != 1 {
			t.Fatalf("%v %+v", err, o)
		}
		h.settle()
		if err := h.e.Sweep(t.Context()); err != nil {
			t.Fatal(err)
		}
		if _, err := h.e.Get(t.Context(), tA, old.ID); CodeOf(err) != CodeNotFound {
			t.Fatalf("the running command's 10 bytes were not counted: %v", err)
		}
		if g, err := h.e.Get(t.Context(), tA, r.ID); err != nil || g.Status != StatusRunning {
			t.Fatalf("%v %+v", err, g)
		}
	})
}

func TestFullDisk(t *testing.T) {
	t.Parallel()
	t.Run("sad/an end that cannot be written is reported finished here and written by the janitor later", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		var full atomic.Bool
		full.Store(true)
		h.settle()
		h.e.setHooks(func(hk *hooks) {
			orig := hk.finishRow
			hk.finishRow = func(ctx context.Context, tid tenant.TenantID, id tenant.CommandID, f *store.Final) error {
				if full.Load() {
					return errDiskFull
				}
				return orig(ctx, tid, id, f)
			}
		})
		r := h.wait(h.start("echo x", StartOptions{}).ID)
		h.settle()
		if r.Status != StatusSucceeded || r.FinishedAt.IsZero() || r.StoredBytes != 2 {
			t.Fatalf("%+v", r)
		}
		o, err := h.e.ReadOutput(t.Context(), tA, r.ID, ReadRequest{Stream: StreamStdout, Wait: time.Minute})
		if err != nil || !o.EOF || string(o.Data) != "x\n" {
			t.Fatalf("%v %+v", err, o)
		}
		if _, delivered, err := h.e.Kill(t.Context(), tA, h.actorA, r.ID, ""); delivered || err != nil {
			t.Fatal(delivered, err)
		}
		if p, _ := h.e.List(t.Context(), tA, ListFilter{}); p.Records[0].Status != StatusSucceeded {
			t.Fatalf("%+v", p.Records[0])
		}
		if stored, _ := h.e.st.Get(t.Context(), tA, r.ID); stored.Status != StatusRunning {
			t.Fatalf("the store has %s", stored.Status)
		}
		h.noSlots(h.e)
		if err := h.e.Sweep(t.Context()); err == nil {
			t.Fatal("a sweep on a full disk reported no error")
		}
		full.Store(false)
		if err := h.e.Sweep(t.Context()); err != nil {
			t.Fatal(err)
		}
		if stored, _ := h.e.st.Get(t.Context(), tA, r.ID); stored.Status != StatusSucceeded {
			t.Fatalf("the janitor did not write the end: %s", stored.Status)
		}
		h.e.mu.Lock()
		n := len(h.e.pending)
		h.e.mu.Unlock()
		if n != 0 {
			t.Fatalf("%d ends still pending", n)
		}
	})
	t.Run("sad/a record that cannot be deleted for lack of space: the directory goes first, then the record", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
		h.setClock(t0)
		r := h.wait(h.start("echo old", StartOptions{}).ID)
		dir, _ := commandDir(h.dir, tA, r.ID)
		var calls atomic.Int32
		h.settle()
		h.e.setHooks(func(hk *hooks) {
			orig := hk.deleteRow
			hk.deleteRow = func(ctx context.Context, tid tenant.TenantID, id tenant.CommandID) error {
				switch calls.Add(1) {
				case 1:
					if _, err := os.Stat(dir); err != nil {
						t.Errorf("the directory went before the first try: %v", err)
					}
					return errDiskFull
				default:
					if _, err := os.Stat(dir); !os.IsNotExist(err) {
						t.Errorf("the record was retried before the directory was freed: %v", err)
					}
					return orig(ctx, tid, id)
				}
			}
		})
		h.setClock(t0.Add(8 * 24 * time.Hour))
		if err := h.e.Sweep(t.Context()); err != nil {
			t.Fatal(err)
		}
		if _, err := h.e.Get(t.Context(), tA, r.ID); CodeOf(err) != CodeNotFound || calls.Load() != 2 {
			t.Fatalf("%v, %d calls", err, calls.Load())
		}
	})
	t.Run("sad/a record that still cannot be deleted stops the pass and is retried", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
		h.setClock(t0)
		r := h.wait(h.start("echo old", StartOptions{}).ID)
		h.settle()
		h.e.setHooks(func(hk *hooks) {
			hk.deleteRow = func(context.Context, tenant.TenantID, tenant.CommandID) error { return errDiskFull }
		})
		h.setClock(t0.Add(8 * 24 * time.Hour))
		if err := h.e.Sweep(t.Context()); !errors.Is(err, errDiskFull) {
			t.Fatalf("err = %v", err)
		}
		if _, err := h.e.Get(t.Context(), tA, r.ID); err != nil {
			t.Fatal(err)
		}
	})
}

func TestFinishOrdering(t *testing.T) {
	t.Parallel()
	t.Run("happy/a command seen finished by Wait has its event written and its slot free", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withLimits(func(l *tenant.Limits) { l.MaxConcurrentPerServer = 1; l.MaxConcurrentCommands = 1 }))
		for range 30 {
			r := h.wait(h.start("true", StartOptions{}).ID)
			if n := h.names(t, r.ID); n[len(n)-1] != "command.finished" {
				t.Fatalf("events %v", n)
			}
		}
	})
	t.Run("happy/a reader that sees eof can start the next command", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withLimits(func(l *tenant.Limits) { l.MaxConcurrentPerServer = 1 }))
		for range 20 {
			r := h.start("echo x", StartOptions{})
			var off int64
			for {
				o, err := h.e.ReadOutput(t.Context(), tA, r.ID, ReadRequest{Stream: StreamStdout, Offset: off, Wait: 10 * time.Second})
				if err != nil {
					t.Fatal(err)
				}
				off = o.Next
				if o.EOF {
					break
				}
			}
		}
	})
}

func TestKillRaces(t *testing.T) {
	t.Parallel()
	t.Run("corner/a kill that races a natural exit is not counted as a kill", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.settle()
		h.e.setHooks(func(hk *hooks) {
			hk.beforeSignal = func(j *job) {
				<-j.h.Done()
				// Give an engine without the kill lock time to read the
				// flag while it is still set.
				time.Sleep(200 * time.Millisecond)
			}
		})
		r := h.start("sleep 0.2", StartOptions{})
		_, delivered, err := h.e.Kill(t.Context(), tA, h.actorA, r.ID, "")
		if err != nil || delivered {
			t.Fatal(delivered, err)
		}
		if r = h.wait(r.ID); r.Status != StatusSucceeded || r.ExitCode == nil || *r.ExitCode != 0 {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("corner/a timeout wins over a kill request", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		r := h.start("trap '' TERM; echo ready; sleep 30", StartOptions{TimeoutSeconds: 2})
		if _, err := h.e.ReadOutput(t.Context(), tA, r.ID, ReadRequest{Stream: StreamStdout, Wait: 10 * time.Second}); err != nil {
			t.Fatal(err)
		}
		if _, delivered, err := h.e.Kill(t.Context(), tA, h.actorA, r.ID, SignalTerm); err != nil || !delivered {
			t.Fatal(delivered, err)
		}
		if r = h.wait(r.ID); r.Status != StatusTimedOut || r.KillRequestedAt.IsZero() {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("raging/a live command whose record was marked lost can still be killed", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		r := h.start("sleep 30", StartOptions{})
		if _, err := h.e.st.RecoverAllTenants(t.Context(), time.Now(), nil); err != nil {
			t.Fatal(err)
		}
		_, delivered, err := h.e.Kill(t.Context(), tA, h.actorA, r.ID, SignalKill)
		if err != nil || !delivered {
			t.Fatal(delivered, err)
		}
		if g, _ := h.e.Wait(t.Context(), tA, r.ID, 10*time.Second); g.Status != StatusLost {
			t.Fatalf("%+v", g)
		}
		h.noSlots(h.e)
	})
}

func TestRecoverRace(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	for round := range 10 {
		if err := h.e.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
		e := h.open()
		var wg sync.WaitGroup
		var r Record
		var serr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			r, serr = e.Start(context.Background(), tA, h.actorA, "srv-1", "sleep 30", StartOptions{})
		}()
		go func() { defer wg.Done(); _, _ = e.Recover(context.Background()) }()
		wg.Wait()
		if serr != nil {
			t.Fatal(serr)
		}
		if g, err := e.Get(t.Context(), tA, r.ID); err != nil || g.Status != StatusRunning {
			t.Fatalf("round %d: a new command was marked %s (%v)", round, g.Status, err)
		}
		h.e = e
	}
}

func TestLimitsGate(t *testing.T) {
	t.Parallel()
	t.Run("edge/waits are capped at max_wait_seconds", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withLimits(func(l *tenant.Limits) { l.MaxWaitSeconds = 1 }))
		r := h.start("sleep 30", StartOptions{})
		begin := time.Now()
		if g, err := h.e.Wait(t.Context(), tA, r.ID, time.Hour); err != nil || g.Status != StatusRunning {
			t.Fatal(g, err)
		}
		if o, err := h.e.ReadOutput(t.Context(), tA, r.ID, ReadRequest{Stream: StreamStdout, Wait: time.Hour}); err != nil || o.EOF {
			t.Fatal(o, err)
		}
		if d := time.Since(begin); d > 10*time.Second {
			t.Fatalf("waits took %v", d)
		}
	})
	t.Run("sad/a failed insert frees the slot", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.e.cfg.Rand = zeroReader{}
		first := h.start("true", StartOptions{})
		if _, err := h.e.Start(t.Context(), tA, h.actorA, "srv-1", "true", StartOptions{}); CodeOf(err) != CodeInternal {
			t.Fatalf("duplicate id: %v", err)
		}
		h.wait(first.ID)
		h.noSlots(h.e)
	})
	t.Run("edge/shutdown_grace_seconds bounds the wait for commands that are slow to stop", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withLimits(func(l *tenant.Limits) { l.ShutdownGraceSeconds = 1 }),
			withSSH(sshclient.Options{ConnectTimeout: 5 * time.Second, KillGrace: 30 * time.Second, KillWait: time.Second}))
		r := h.start("trap '' TERM; echo ready; sleep 60", StartOptions{})
		if _, err := h.e.ReadOutput(t.Context(), tA, r.ID, ReadRequest{Stream: StreamStdout, Wait: 10 * time.Second}); err != nil {
			t.Fatal(err)
		}
		done := make(chan time.Duration, 1)
		begin := time.Now()
		go func() {
			_ = h.e.Shutdown(context.Background())
			done <- time.Since(begin)
		}()
		select {
		case d := <-done:
			if d < 900*time.Millisecond {
				t.Fatalf("shutdown returned after %v, before the grace", d)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("shutdown did not return after its grace")
		}
		e2 := h.open()
		if g, err := e2.Get(t.Context(), tA, r.ID); err != nil || g.Status != StatusKilled || !g.MayStillRun {
			t.Fatalf("%v %+v", err, g)
		}
	})
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	copy(p, bytes.Repeat([]byte{0}, len(p)))
	return len(p), nil
}

func TestSweepSerialized(t *testing.T) {
	t.Parallel()
	t.Run("corner/two concurrent sweeps never work on the same records at once", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
		h.setClock(t0)
		for range 3 {
			h.wait(h.start("echo old", StartOptions{}).ID)
		}
		h.settle()
		var active, overlap atomic.Int32
		h.e.setHooks(func(hk *hooks) {
			orig := hk.deleteRow
			hk.deleteRow = func(ctx context.Context, tid tenant.TenantID, id tenant.CommandID) error {
				if active.Add(1) > 1 {
					overlap.Store(1)
				}
				defer active.Add(-1)
				// Stay inside the call for a moment, so a second pass
				// running beside this one is caught here.
				time.Sleep(100 * time.Millisecond)
				return orig(ctx, tid, id)
			}
		})
		h.setClock(t0.Add(8 * 24 * time.Hour))
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := h.e.Sweep(context.Background()); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if overlap.Load() != 0 {
			t.Fatal("two passes deleted records at the same time")
		}
		if p, _ := h.e.List(t.Context(), tA, ListFilter{}); len(p.Records) != 0 {
			t.Fatalf("%d records left", len(p.Records))
		}
	})
}

// TestLeaks runs the engine's main paths with the goroutine leak check. It
// does not run in parallel, so no other test's goroutines are counted.
func TestLeaks(t *testing.T) {
	t.Run("happy/finished, killed, timed out, failed to start and shut down leave no goroutine", func(t *testing.T) {
		h := newCheckedHarness(t)
		h.wait(h.start("echo ok", StartOptions{}).ID)
		k := h.start("sleep 30", StartOptions{})
		if _, _, err := h.e.Kill(t.Context(), tA, h.actorA, k.ID, SignalKill); err != nil {
			t.Fatal(err)
		}
		h.wait(k.ID)
		h.wait(h.start("sleep 30 & echo bg", StartOptions{TimeoutSeconds: 1}).ID)
		if _, err := h.e.ReadOutput(t.Context(), tA, k.ID, ReadRequest{Stream: StreamStdout, Wait: 50 * time.Millisecond}); err != nil {
			t.Fatal(err)
		}
		h.start("sleep 30", StartOptions{})
		if err := h.e.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("sad/a start that fails leaves no goroutine", func(t *testing.T) {
		h := newCheckedHarness(t, withServer(sshtest.Options{WrongHostKey: true}))
		if _, err := h.e.Start(t.Context(), tA, h.actorA, "srv-1", "true", StartOptions{}); err == nil {
			t.Fatal("started")
		}
	})
}

func TestLiveHandleIsNotFinished(t *testing.T) {
	t.Parallel()
	for _, lost := range []bool{false, true} {
		name := "corner/a command whose end is recorded but whose slot is still held is running"
		if lost {
			name = "raging/a live command whose record another process marked lost is running until its slot is free"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, withLimits(func(l *tenant.Limits) { l.MaxConcurrentPerServer = 1 }))
			reached, proceed := make(chan struct{}), make(chan struct{})
			h.e.setHooks(func(hk *hooks) {
				hk.beforeRelease = func(*job) {
					close(reached)
					<-proceed
				}
			})
			r := h.start("echo x; sleep 30", StartOptions{})
			if _, err := h.e.ReadOutput(t.Context(), tA, r.ID, ReadRequest{Stream: StreamStdout, Wait: 10 * time.Second}); err != nil {
				t.Fatal(err)
			}
			if lost {
				if _, err := h.e.st.RecoverAllTenants(t.Context(), time.Now(), nil); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := h.e.Kill(t.Context(), tA, h.actorA, r.ID, SignalKill); err != nil {
				t.Fatal(err)
			}
			select {
			case <-reached:
			case <-time.After(30 * time.Second):
				t.Fatal("the command did not end")
			}
			if g, err := h.e.Get(t.Context(), tA, r.ID); err != nil || g.Status != StatusRunning {
				t.Errorf("get while the slot is held: %v %+v", err, g)
			}
			if g, err := h.e.Wait(t.Context(), tA, r.ID, 50*time.Millisecond); err != nil || g.Status != StatusRunning {
				t.Errorf("wait while the slot is held: %v %+v", err, g)
			}
			if o, err := h.e.ReadOutput(t.Context(), tA, r.ID, ReadRequest{Stream: StreamStdout}); err != nil || o.EOF {
				t.Errorf("read while the slot is held: %v %+v", err, o)
			}
			close(proceed)
			g, err := h.e.Wait(t.Context(), tA, r.ID, 10*time.Second)
			want := StatusKilled
			if lost {
				want = StatusLost
			}
			if err != nil || g.Status != want {
				t.Fatalf("%v %+v", err, g)
			}
			if o, err := h.e.ReadOutput(t.Context(), tA, r.ID, ReadRequest{Stream: StreamStdout}); err != nil || !o.EOF {
				t.Fatalf("%v %+v", err, o)
			}
			h.noSlots(h.e)
		})
	}
}

// TestUsageCountsAFinishedCommandOnce (Phase 6 review gate, seen as a
// TestDataQuota flake under load): between the end written to the store
// and the handle leaving the live map, the command's output is counted by
// the store; the live count must not add it again, or a janitor pass in
// that window sees the tenant over max_data_bytes and deletes the record.
func TestUsageCountsAFinishedCommandOnce(t *testing.T) {
	t.Parallel()
	const cmd = "printf 0123456789"
	h := newHarness(t)
	reached, proceed := make(chan struct{}), make(chan struct{})
	var inWindow int64
	h.e.setHooks(func(hk *hooks) {
		hk.beforeRelease = func(j *job) {
			n, err := h.e.usedBytes(context.Background(), j.tid)
			if err != nil {
				t.Error(err)
			}
			inWindow = n
			close(reached)
			<-proceed
		}
	})
	r := h.start(cmd, StartOptions{})
	select {
	case <-reached:
	case <-time.After(30 * time.Second):
		t.Fatal("the command never reached the release")
	}
	want := int64(10 + len(cmd) + store.RowCost)
	close(proceed)
	h.wait(r.ID)
	t.Run("corner/usage in the window between the record and the release", func(t *testing.T) {
		if inWindow != want {
			t.Fatalf("usage %d, want %d (the output counted once)", inWindow, want)
		}
	})
}
