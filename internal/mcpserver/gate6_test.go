package mcpserver

import (
	"context"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/leakcheck"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/sshtest"
)

// The tests in this file came out of the Phase 6 review gate.

// TestShareBeforeBody (security 5): a principal's share of the request
// cap is taken before its body is read, so requests whose bodies never
// arrive hold the share and a further request of that principal is
// refused at once; another principal is served.
func TestShareBeforeBody(t *testing.T) {
	h := newHarness(t, withInFlight(4))
	key := tenant.Principal{Tenant: tA, Kind: tenant.KindUser, Subject: userA}.String()
	var wg sync.WaitGroup
	var pipes []*io.PipeWriter
	for range 3 {
		pr, pw := io.Pipe()
		pipes = append(pipes, pw)
		req, err := http.NewRequestWithContext(context.WithoutCancel(t.Context()), http.MethodPost, h.url+PathMCP, pr)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range h.mcpHeaders(userA) {
			req.Header.Set(k, v)
		}
		wg.Go(func() {
			if resp, err := http.DefaultClient.Do(req); err == nil {
				_ = resp.Body.Close()
			}
		})
	}
	defer func() {
		for _, pw := range pipes {
			_ = pw.CloseWithError(io.ErrUnexpectedEOF)
		}
		wg.Wait()
	}()
	if !leakcheck.Poll(10*time.Second, func() bool { return h.gw.holdingOf(key) == 3 }) {
		t.Fatalf("requests with unsent bodies hold %d of the share, want 3", h.gw.holdingOf(key))
	}
	if over := h.rawCall(t, userA, ToolListServers, nil); over.code != http.StatusServiceUnavailable || string(over.body) != `{"error":"busy"}` {
		t.Fatalf("a fourth request of the same principal = %d %s", over.code, over.body)
	}
	if other := h.rawCall(t, svcB, ToolListServers, nil); other.code != http.StatusOK {
		t.Fatalf("another principal = %d %s", other.code, other.body)
	}
}

// TestTransferSlots (security 4): probes and file transfers take a
// per-server slot of their own; a server already running
// max_concurrent_per_server of them answers busy naming the limit, other
// servers and commands are not affected, and the slots come back.
func TestTransferSlots(t *testing.T) {
	h := newHarness(t, withServer(t, "slow", sshtest.Options{SlowBanner: 3 * time.Second}))
	slow := h.tenants[0].Servers[len(h.tenants[0].Servers)-1]
	var wg sync.WaitGroup
	for range 2 { // max_concurrent_per_server is 2 in tenanttest.Limits
		wg.Go(func() {
			h.call(t, userA, ToolGetServerInfo, map[string]any{"server": "slow"})
		})
	}
	k := targetKey{tA, slow.ID}
	busyNow := func() bool {
		h.gw.transferMu.Lock()
		defer h.gw.transferMu.Unlock()
		return h.gw.transfers[k] == 2
	}
	if !leakcheck.Poll(10*time.Second, busyNow) {
		t.Fatal("the probes never held two slots")
	}
	t.Run("sad/a third probe is busy", func(t *testing.T) {
		wantErr(t, h.call(t, userA, ToolGetServerInfo, map[string]any{"server": "slow"}), CodeBusy, "")
		e := wantErr(t, h.call(t, userA, ToolDownloadFile, map[string]any{"server": "slow", "path": "/tmp/x"}), CodeBusy, "")
		if e.Message != "a limit is reached: max_concurrent_per_server" {
			t.Fatalf("message %q", e.Message)
		}
	})
	t.Run("happy/another server is served", func(t *testing.T) {
		var r ServerInfoResult
		decode(t, h.call(t, userA, ToolGetServerInfo, map[string]any{"server": "srv-1"}), &r)
		if !r.Reachable {
			t.Fatalf("info = %+v", r)
		}
	})
	wg.Wait()
	t.Run("happy/the slots come back", func(t *testing.T) {
		h.gw.transferMu.Lock()
		n := len(h.gw.transfers)
		h.gw.transferMu.Unlock()
		if n != 0 {
			t.Fatalf("%d servers still hold transfer slots", n)
		}
		var r ServerInfoResult
		decode(t, h.call(t, userA, ToolGetServerInfo, map[string]any{"server": "slow"}), &r)
		if !r.Reachable {
			t.Fatalf("info = %+v", r)
		}
	})
}

// TestKillReplyLost: on a server that ends the command and closes the
// channel before answering the signal request, kill_command reports
// signal_delivered and the command ends killed, not "may still run".
func TestKillReplyLost(t *testing.T) {
	h := newHarness(t, withServer(t, "lossy", sshtest.Options{ExitBeforeSignalReply: true}))
	r := h.run(t, userA, "lossy", "echo ready; sleep 30", map[string]any{"wait_seconds": 0})
	if !leakcheck.Poll(10*time.Second, func() bool {
		var o ReadResult
		decode(t, h.call(t, userA, ToolReadOutput, map[string]any{"command_id": r.CommandID, "wait_seconds": 1}), &o)
		return o.Data == "ready\n"
	}) {
		t.Fatal("the command never printed ready")
	}
	var k KillResult
	decode(t, h.call(t, userA, ToolKillCommand, map[string]any{"command_id": r.CommandID}), &k)
	if !k.SignalDelivered {
		t.Fatalf("kill = %+v", k)
	}
	if got := h.waitDone(t, userA, r.CommandID); got.Status != "killed" || got.Signal != "TERM" || got.MayStillRun {
		t.Fatalf("read = %+v", got)
	}
}
