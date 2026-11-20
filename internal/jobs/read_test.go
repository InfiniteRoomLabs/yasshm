package jobs

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/audit"
	"github.com/InfiniteRoomLabs/yasshm/internal/store"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/leakcheck"
)

func TestReadOutput(t *testing.T) {
	t.Parallel()
	t.Run("happy/pages from the start and from the end", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		r := h.wait(h.start("printf 0123456789", StartOptions{}).ID)
		cases := []struct {
			off, max   int64
			data       string
			start, nxt int64
			eof        bool
		}{
			{0, 4, "0123", 0, 4, false},
			{4, 100, "456789", 4, 10, true},
			{-3, 0, "789", 7, 10, true},
			{-30, 2, "01", 0, 2, false},
			{10, 5, "", 10, 10, true},
			{99, 5, "", 10, 10, true},
		}
		for _, c := range cases {
			o, err := h.e.ReadOutput(t.Context(), tA, r.ID, ReadRequest{Stream: StreamStdout, Offset: c.off, MaxBytes: c.max})
			if err != nil || string(o.Data) != c.data || o.Start != c.start || o.Next != c.nxt || o.EOF != c.eof || o.Size != 10 || o.Record.Status != StatusSucceeded {
				t.Errorf("offset %d max %d: %v %+v", c.off, c.max, err, o)
			}
		}
	})
	t.Run("edge/max_result_bytes caps every read", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withLimits(func(l *tenant.Limits) { l.MaxResultBytes = 3 }))
		r := h.wait(h.start("printf abcdef", StartOptions{}).ID)
		o, err := h.e.ReadOutput(t.Context(), tA, r.ID, ReadRequest{Stream: StreamStdout, MaxBytes: 1000})
		if err != nil || string(o.Data) != "abc" || o.Next != 3 || o.EOF {
			t.Fatalf("%v %+v", err, o)
		}
	})
	t.Run("happy/separate streams; merged stderr is empty with eof following the command", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		sep := h.wait(h.start("echo out; echo err >&2", StartOptions{MergeStderr: boolp(false)}).ID)
		if out, errs := h.read(sep.ID, StreamStdout), h.read(sep.ID, StreamStderr); out != "out\n" || errs != "err\n" {
			t.Fatalf("%q %q", out, errs)
		}
		for _, p := range []string{"stdout", "stderr"} {
			fi, err := os.Stat(filepath.Join(h.dir, "tenants", string(tA), "commands", string(sep.ID), p))
			if err != nil || fi.Mode().Perm() != 0o600 {
				t.Fatalf("%s %v %v", p, fi, err)
			}
		}
		running := h.start("echo x >&2; sleep 30", StartOptions{})
		o, err := h.e.ReadOutput(t.Context(), tA, running.ID, ReadRequest{Stream: StreamStderr})
		if err != nil || len(o.Data) != 0 || o.EOF {
			t.Fatalf("running merged stderr: %v %+v", err, o)
		}
		_, _, _ = h.e.Kill(t.Context(), tA, h.actorA, running.ID, SignalKill)
		h.wait(running.ID)
		o, err = h.e.ReadOutput(t.Context(), tA, running.ID, ReadRequest{Stream: StreamStderr})
		if err != nil || len(o.Data) != 0 || !o.EOF {
			t.Fatalf("finished merged stderr: %v %+v", err, o)
		}
		if h.read(running.ID, StreamStdout) != "x\n" {
			t.Fatal("stderr not merged into stdout")
		}
	})
	t.Run("sad/an unknown stream is invalid_argument", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		r := h.wait(h.start("true", StartOptions{}).ID)
		for _, s := range []string{"", "STDOUT", "both", "../stdout"} {
			if _, err := h.e.ReadOutput(t.Context(), tA, r.ID, ReadRequest{Stream: s}); CodeOf(err) != CodeInvalidArgument {
				t.Errorf("%q: %v", s, err)
			}
		}
	})
	t.Run("happy/paging while the command is still writing loses and repeats nothing", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		r := h.start("for i in 1 2 3 4 5; do echo line$i; sleep 0.1; done", StartOptions{})
		var got strings.Builder
		var off int64
		for range 200 {
			o, err := h.e.ReadOutput(t.Context(), tA, r.ID, ReadRequest{Stream: StreamStdout, Offset: off, MaxBytes: 4, Wait: 10 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			if o.Start != off {
				t.Fatalf("start %d, want %d", o.Start, off)
			}
			got.Write(o.Data)
			off = o.Next
			if o.EOF {
				break
			}
		}
		if got.String() != "line1\nline2\nline3\nline4\nline5\n" {
			t.Fatalf("%q", got.String())
		}
	})
	t.Run("happy/a waiting reader is woken by output", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		r := h.start("sleep 0.3; echo late; sleep 30", StartOptions{})
		begin := time.Now()
		o, err := h.e.ReadOutput(t.Context(), tA, r.ID, ReadRequest{Stream: StreamStdout, Wait: 20 * time.Second})
		if err != nil || string(o.Data) != "late\n" || o.EOF || time.Since(begin) > 10*time.Second {
			t.Fatalf("%v %+v", err, o)
		}
	})
	t.Run("happy/a waiting reader is woken by the finish", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		r := h.start("sleep 0.3", StartOptions{})
		o, err := h.e.ReadOutput(t.Context(), tA, r.ID, ReadRequest{Stream: StreamStdout, Wait: 20 * time.Second})
		if err != nil || len(o.Data) != 0 || !o.EOF || o.Record.Status != StatusSucceeded {
			t.Fatalf("%v %+v", err, o)
		}
	})
	t.Run("edge/a waiting reader returns empty when the wait expires", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		r := h.start("sleep 30", StartOptions{})
		o, err := h.e.ReadOutput(t.Context(), tA, r.ID, ReadRequest{Stream: StreamStdout, Wait: 150 * time.Millisecond})
		if err != nil || len(o.Data) != 0 || o.EOF || o.Record.Status != StatusRunning {
			t.Fatalf("%v %+v", err, o)
		}
	})
	t.Run("edge/a waiting reader returns when its context ends", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		r := h.start("sleep 30", StartOptions{})
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(100*time.Millisecond, cancel)
		o, err := h.e.ReadOutput(ctx, tA, r.ID, ReadRequest{Stream: StreamStdout, Wait: time.Minute})
		if err != nil || len(o.Data) != 0 || o.EOF {
			t.Fatalf("%v %+v", err, o)
		}
		// The caller went away; the command did not (D10).
		if g, _ := h.e.Get(t.Context(), tA, r.ID); g.Status != StatusRunning {
			t.Fatalf("%+v", g)
		}
	})
	t.Run("edge/Wait returns the running record when its time is up", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		r := h.start("sleep 30", StartOptions{})
		w, err := h.e.Wait(t.Context(), tA, r.ID, 50*time.Millisecond)
		if err != nil || w.Status != StatusRunning {
			t.Fatalf("%v %+v", err, w)
		}
		w, err = h.e.Wait(t.Context(), tA, r.ID, 0)
		if err != nil || w.Status != StatusRunning {
			t.Fatalf("%v %+v", err, w)
		}
	})
	t.Run("happy/truncation at max_output_bytes keeps draining the remote side", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withLimits(func(l *tenant.Limits) { l.MaxOutputBytes = 1000 }))
		r := h.wait(h.start("head -c 3000000 /dev/zero; echo done >&2", StartOptions{}).ID)
		if r.Status != StatusSucceeded || !r.Truncated || r.StoredBytes != 1000 || r.StdoutBytes != 3000000 || r.StderrBytes != 5 {
			t.Fatalf("%+v", r)
		}
		o, err := h.e.ReadOutput(t.Context(), tA, r.ID, ReadRequest{Stream: StreamStdout, Offset: -10})
		if err != nil || o.Size != 1000 || o.Next != 1000 || !o.EOF {
			t.Fatalf("%v %+v", err, o)
		}
	})
}

func TestList(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	var ids []tenant.CommandID
	for i := range 5 {
		sid := tenant.ServerID("srv-1")
		if i%2 == 1 {
			sid = "srv-2"
		}
		r, err := h.e.Start(t.Context(), tA, h.actorA, sid, "exit "+string(rune('0'+i%2)), StartOptions{})
		if err != nil {
			t.Fatal(err)
		}
		h.wait(r.ID)
		ids = append(ids, r.ID)
	}
	t.Run("happy/newest first, paged with a cursor", func(t *testing.T) {
		var seen []tenant.CommandID
		cursor := ""
		for range 10 {
			p, err := h.e.List(t.Context(), tA, ListFilter{Limit: 2, Cursor: cursor})
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range p.Records {
				seen = append(seen, r.ID)
			}
			if p.NextCursor == "" {
				break
			}
			cursor = p.NextCursor
		}
		if len(seen) != 5 || seen[0] != ids[4] || seen[4] != ids[0] {
			t.Fatalf("%v vs %v", seen, ids)
		}
	})
	t.Run("happy/filters by server and status; the default limit is 20", func(t *testing.T) {
		p, err := h.e.List(t.Context(), tA, ListFilter{Server: "srv-2", Status: StatusFailed})
		if err != nil || len(p.Records) != 2 || p.NextCursor != "" {
			t.Fatalf("%v %+v", err, p)
		}
		for _, r := range p.Records {
			if r.Server != "srv-2" || r.Status != StatusFailed {
				t.Fatalf("%+v", r)
			}
		}
	})
	t.Run("sad/bad filters and cursors are invalid_argument", func(t *testing.T) {
		good := encodeCursor(&Record{CreatedAt: time.Now(), ID: ids[0]})
		bad := []ListFilter{
			{Limit: -1}, {Limit: 101}, {Server: "A B"}, {Status: "done"},
			{Cursor: "x"}, {Cursor: "c1.!!"}, {Cursor: good + "="}, {Cursor: "c1." + strings.Repeat("A", 200)},
			{Cursor: "c1." + base64.RawURLEncoding.EncodeToString([]byte("12.notanid"))},
			{Cursor: "c1." + base64.RawURLEncoding.EncodeToString([]byte("-5."+string(ids[0])))},
			{Cursor: "c1." + base64.RawURLEncoding.EncodeToString([]byte("012."+string(ids[0])))},
			{Cursor: "c1." + base64.RawURLEncoding.EncodeToString([]byte(string(ids[0])))},
		}
		for _, f := range bad {
			if _, err := h.e.List(t.Context(), tA, f); CodeOf(err) != CodeInvalidArgument {
				t.Errorf("%+v: %v", f, err)
			}
		}
		if _, err := h.e.List(t.Context(), "tenant-z", ListFilter{}); CodeOf(err) != CodeNotFound {
			t.Error(err)
		}
	})
	t.Run("raging/tenant a's cursor in tenant b lists only tenant b", func(t *testing.T) {
		p, err := h.e.List(t.Context(), tA, ListFilter{Limit: 1})
		if err != nil || p.NextCursor == "" {
			t.Fatal(err)
		}
		pb, err := h.e.List(t.Context(), tB, ListFilter{Cursor: p.NextCursor})
		if err != nil || len(pb.Records) != 0 {
			t.Fatalf("%v %+v", err, pb)
		}
	})
}

func TestTenantIsolation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ra := h.start("echo secret-a; sleep 30", StartOptions{})
	rb, err := h.e.Start(t.Context(), tB, h.actorB, "srv-1", "echo b", StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.e.Wait(t.Context(), tB, rb.ID, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	t.Run("raging/tenant b cannot reach tenant a's command by any method", func(t *testing.T) {
		ctx := t.Context()
		if _, err := h.e.Get(ctx, tB, ra.ID); CodeOf(err) != CodeNotFound {
			t.Error("get", err)
		}
		if _, err := h.e.Wait(ctx, tB, ra.ID, time.Millisecond); CodeOf(err) != CodeNotFound {
			t.Error("wait", err)
		}
		if _, err := h.e.ReadOutput(ctx, tB, ra.ID, ReadRequest{Stream: StreamStdout}); CodeOf(err) != CodeNotFound {
			t.Error("read", err)
		}
		if _, _, err := h.e.Kill(ctx, tB, h.actorB, ra.ID, ""); CodeOf(err) != CodeNotFound {
			t.Error("kill", err)
		}
		p, err := h.e.List(ctx, tB, ListFilter{})
		if err != nil || len(p.Records) != 1 || p.Records[0].ID != rb.ID {
			t.Errorf("list %v %+v", err, p)
		}
		if strings.Contains(h.au.names(t, ra.ID)[len(h.au.names(t, ra.ID))-1], "kill") {
			t.Error("a kill was audited for the other tenant's command")
		}
		if g, _ := h.e.Get(ctx, tA, ra.ID); g.Status != StatusRunning {
			t.Errorf("tenant a's command changed: %+v", g)
		}
	})
	t.Run("raging/a principal of tenant b cannot kill in tenant a", func(t *testing.T) {
		if _, _, err := h.e.Kill(t.Context(), tA, h.actorB, ra.ID, ""); CodeOf(err) != CodeInvalidArgument {
			t.Fatal(err)
		}
	})
	t.Run("happy/output paths never overlap", func(t *testing.T) {
		pa, _ := commandDir(h.dir, tA, ra.ID)
		pb, _ := commandDir(h.dir, tB, rb.ID)
		if strings.HasPrefix(pa, filepath.Dir(filepath.Dir(pb))) || strings.HasPrefix(pb, filepath.Dir(filepath.Dir(pa))) {
			t.Fatalf("%s %s", pa, pb)
		}
		pa2, _ := commandDir(h.dir, tB, ra.ID)
		if pa2 == pa {
			t.Fatal("the same id maps to the same path in two tenants")
		}
	})
	t.Run("raging/hostile ids through the public API are invalid_argument", func(t *testing.T) {
		ctx := t.Context()
		for _, id := range []tenant.CommandID{"", "../../../etc/passwd", "..", "0123456789abcdef0123456789abcdef/../x", "' OR 1=1 --", tenant.CommandID(strings.ToUpper(string(ra.ID)))} {
			if _, err := h.e.Get(ctx, tA, id); CodeOf(err) != CodeInvalidArgument {
				t.Errorf("get %q: %v", id, err)
			}
			if _, err := h.e.ReadOutput(ctx, tA, id, ReadRequest{Stream: StreamStdout}); CodeOf(err) != CodeInvalidArgument {
				t.Errorf("read %q: %v", id, err)
			}
			if _, _, err := h.e.Kill(ctx, tA, h.actorA, id, ""); CodeOf(err) != CodeInvalidArgument {
				t.Errorf("kill %q: %v", id, err)
			}
			if _, err := h.e.Wait(ctx, tA, id, 0); CodeOf(err) != CodeInvalidArgument {
				t.Errorf("wait %q: %v", id, err)
			}
		}
		for _, tid := range []tenant.TenantID{"", "..", "../tenant-a"} {
			if _, err := h.e.Get(ctx, tid, ra.ID); CodeOf(err) != CodeNotFound {
				t.Errorf("tenant %q: %v", tid, err)
			}
		}
		if _, _, err := h.e.Kill(ctx, tA, h.actorA, ra.ID, "HUP"); CodeOf(err) != CodeInvalidArgument {
			t.Error("HUP accepted", err)
		}
	})
}

func TestKillAudit(t *testing.T) {
	t.Parallel()
	t.Run("sad/no kill_requested event, no signal", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		r := h.start("sleep 30", StartOptions{})
		h.au.setFail(audit.EventCommandKillRequested, true)
		_, delivered, err := h.e.Kill(t.Context(), tA, h.actorA, r.ID, SignalKill)
		if !errors.Is(err, ErrAuditFailed) || delivered {
			t.Fatalf("%v %v", delivered, err)
		}
		for _, req := range h.sa.Requests() {
			if strings.HasPrefix(req, "signal:") {
				t.Fatalf("a signal was sent: %v", h.sa.Requests())
			}
		}
		if g, _ := h.e.Get(t.Context(), tA, r.ID); g.Status != StatusRunning || !g.KillRequestedAt.IsZero() {
			t.Fatalf("%+v", g)
		}
	})
	t.Run("sad/failed after-the-fact events go to the error hook and hide nothing", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.au.setFail(audit.EventCommandStarted, true)
		h.au.setFail(audit.EventCommandFinished, true)
		r := h.wait(h.start("echo ok", StartOptions{}).ID)
		if r.Status != StatusSucceeded {
			t.Fatalf("%+v", r)
		}
		if len(h.errors()) < 2 {
			t.Fatalf("errors: %v", h.errors())
		}
	})
}

func TestJanitor(t *testing.T) {
	t.Parallel()
	day := 24 * time.Hour
	t.Run("happy/retention deletes old finished commands, row then files, never running ones", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
		h.setClock(t0)
		old := h.wait(h.start("echo old", StartOptions{}).ID)
		running := h.start("sleep 30", StartOptions{})
		h.setClock(t0.Add(6 * day))
		recent := h.wait(h.start("echo recent", StartOptions{}).ID)
		rb, err := h.e.Start(t.Context(), tB, h.actorB, "srv-1", "echo b", StartOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.e.Wait(t.Context(), tB, rb.ID, 30*time.Second); err != nil {
			t.Fatal(err)
		}
		h.settle()
		h.setClock(t0.Add(7*day + time.Hour))
		if err := h.e.Sweep(t.Context()); err != nil {
			t.Fatal(err)
		}
		if _, err := h.e.Get(t.Context(), tA, old.ID); CodeOf(err) != CodeNotFound {
			t.Fatal("old record kept", err)
		}
		dir, _ := commandDir(h.dir, tA, old.ID)
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("old output kept", err)
		}
		for _, id := range []tenant.CommandID{recent.ID, running.ID} {
			if _, err := h.e.Get(t.Context(), tA, id); err != nil {
				t.Fatal(id, err)
			}
		}
		// Tenant b's command finished at the same (fake) time as recent.
		if _, err := h.e.Get(t.Context(), tB, rb.ID); err != nil {
			t.Fatal(err)
		}
		h.settle()
		h.setClock(t0.Add(30 * day))
		if err := h.e.Sweep(t.Context()); err != nil {
			t.Fatal(err)
		}
		if g, err := h.e.Get(t.Context(), tA, running.ID); err != nil || g.Status != StatusRunning {
			t.Fatalf("the running command was touched: %v %+v", err, g)
		}
	})
	t.Run("happy/a finishing command wakes the janitor: the oldest finished go first, per tenant, without a Sweep call", func(t *testing.T) {
		t.Parallel()
		const cmd = "printf 0123456789"
		per := int64(10 + len(cmd) + store.RowCost) // one finished record's usage
		h := newHarness(t, withLimits(func(l *tenant.Limits) { l.MaxDataBytes = 2*per + 10; l.MaxOutputBytes = 10 }))
		t0 := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
		var ids []tenant.CommandID
		for i := range 4 {
			h.setClock(t0.Add(time.Duration(i) * time.Minute))
			ids = append(ids, h.wait(h.start(cmd, StartOptions{}).ID).ID)
			if i >= 2 {
				old := ids[i-2]
				if !leakcheck.Poll(10*time.Second, func() bool { _, err := h.e.Get(t.Context(), tA, old); return CodeOf(err) == CodeNotFound }) {
					t.Fatalf("command %d was not deleted after command %d finished", i-2, i)
				}
			}
		}
		rb, err := h.e.Start(t.Context(), tB, h.actorB, "srv-1", cmd, StartOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.e.Wait(t.Context(), tB, rb.ID, 30*time.Second); err != nil {
			t.Fatal(err)
		}
		for i, id := range ids {
			_, err := h.e.Get(t.Context(), tA, id)
			if (i < 2) != (CodeOf(err) == CodeNotFound) {
				t.Errorf("command %d: %v", i, err)
			}
		}
		if _, err := h.e.Get(t.Context(), tB, rb.ID); err != nil {
			t.Fatal("tenant b's command was deleted for tenant a's quota", err)
		}
	})
	t.Run("happy/the start-up sweep removes output directories with no record", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		kept := h.wait(h.start("echo kept", StartOptions{}).ID)
		orphan, _ := commandDir(h.dir, tA, "0123456789abcdef0123456789abcdef")
		if err := mkdirPrivate(orphan); err != nil {
			t.Fatal(err)
		}
		stray := filepath.Join(h.dir, "tenants", string(tA), "commands", "not-an-id")
		if err := mkdirPrivate(stray); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(h.dir, "tenants", "README"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := h.e.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
		e2 := h.open()
		if _, err := os.Stat(orphan); !os.IsNotExist(err) {
			t.Fatal("orphan kept", err)
		}
		if _, err := os.Stat(stray); err != nil {
			t.Fatal("a name that is not an id was touched", err)
		}
		if _, err := e2.ReadOutput(t.Context(), tA, kept.ID, ReadRequest{Stream: StreamStdout}); err != nil {
			t.Fatal(err)
		}
		if len(h.errors()) < 2 {
			t.Fatalf("unexpected entries not reported: %v", h.errors())
		}
	})
	t.Run("happy/the janitor goroutine runs on its interval and stops with Shutdown", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		if err := h.e.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
		h.cfg.JanitorInterval = time.Millisecond
		h.cfg.Tenants[0].Limits.RetentionDays = 1
		t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
		h.setClock(t0)
		e := h.open()
		r, err := e.Start(t.Context(), tA, h.actorA, "srv-1", "true", StartOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Wait(t.Context(), tA, r.ID, 30*time.Second); err != nil {
			t.Fatal(err)
		}
		h.setClock(t0.Add(2 * day))
		if !leakcheck.Poll(10*time.Second, func() bool { _, err := e.Get(t.Context(), tA, r.ID); return CodeOf(err) == CodeNotFound }) {
			t.Fatal("the janitor did not run")
		}
	})
}

func TestConcurrency(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withLimits(func(l *tenant.Limits) { l.MaxConcurrentCommands = 8; l.MaxConcurrentPerServer = 4 }))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var ids []tenant.CommandID
	var busyN int
	for i := range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sid := tenant.ServerID("srv-1")
			if i%2 == 1 {
				sid = "srv-2"
			}
			// Every command holds its slot until the test releases it, so
			// none can finish during the burst and free a slot for a
			// ninth start (seen once in CI with "sleep 0.2").
			r, err := h.e.Start(context.Background(), tA, h.actorA, sid, "echo $$; while [ ! -e release ]; do sleep 0.05; done", StartOptions{})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case errors.Is(err, CodeBusy):
				busyN++
			case err != nil:
				t.Error(err)
			default:
				ids = append(ids, r.ID)
			}
		}()
	}
	wg.Wait()
	if len(ids) == 0 || len(ids) > 8 || len(ids)+busyN != 24 {
		t.Fatalf("%d started, %d busy", len(ids), busyN)
	}
	if err := os.WriteFile(filepath.Join(h.sa.Root, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		wg.Add(3)
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				_, _, _ = h.e.Kill(context.Background(), tA, h.actorA, id, SignalTerm)
			}
		}()
		go func() {
			defer wg.Done()
			_, _ = h.e.ReadOutput(context.Background(), tA, id, ReadRequest{Stream: StreamStdout, Wait: 5 * time.Second})
		}()
		go func() {
			defer wg.Done()
			_, _ = h.e.List(context.Background(), tA, ListFilter{})
			_, _ = h.e.Get(context.Background(), tA, id)
		}()
	}
	wg.Wait()
	for _, id := range ids {
		r := h.wait(id)
		if r.Status != StatusSucceeded && r.Status != StatusKilled {
			t.Fatalf("%+v", r)
		}
	}
	h.noSlots(h.e)
	if err := h.e.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.Open(t.Context(), h.dir); n != nil {
		_ = n.Close(t.Context())
	}
}
