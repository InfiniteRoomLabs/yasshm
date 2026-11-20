package jobs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/audit"
	"github.com/InfiniteRoomLabs/yasshm/internal/sshclient"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/leakcheck"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/loopback"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/sshtest"
)

const (
	tA tenant.TenantID = "tenant-a"
	tB tenant.TenantID = "tenant-b"
)

func testLimits() tenant.Limits {
	return tenant.Limits{
		DefaultWaitSeconds: 5, MaxWaitSeconds: 10, DefaultTimeoutSeconds: 30, MaxTimeoutSeconds: 60,
		MaxConcurrentCommands: 4, MaxConcurrentPerServer: 2, MaxCommandBytes: 8192, MaxOutputBytes: 1 << 20,
		MaxResultBytes: 100000, RetentionDays: 7, MaxDataBytes: 1 << 30, MaxUploadBytes: 1 << 20,
		KillGraceSeconds: 1, ConnectTimeoutSeconds: 5, ShutdownGraceSeconds: 10,
	}
}

// testSSH has short timings so the suite stays fast.
func testSSH(tenant.Limits) sshclient.Options {
	return sshclient.Options{ConnectTimeout: 5 * time.Second, KillGrace: 300 * time.Millisecond, KillWait: time.Second}
}

// serverFor describes an sshtest server as a server of tid.
func serverFor(tid tenant.TenantID, sid tenant.ServerID, s *sshtest.Server) tenant.Server {
	return tenant.Server{
		Tenant: tid, ID: sid, Host: s.Host, Port: s.Port, Access: tenant.AccessLook, HostKey: s.HostKeyLine(),
		Identity: tenant.Identity{ID: "look", User: s.User, PrivateKeyFile: s.ClientKeyFile},
	}
}

// failAudit is the real audit logger with switches that make a named event
// fail.
type failAudit struct {
	l    *audit.Logger
	path string
	mu   sync.Mutex
	fail map[string]bool
}

func (a *failAudit) setFail(name string, on bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fail[name] = on
}

func (a *failAudit) Write(ev audit.Event) error {
	a.mu.Lock()
	f := a.fail[ev.Name()]
	a.mu.Unlock()
	if f {
		return errors.New("audit write failed (test)")
	}
	return a.l.Write(ev)
}

func (a *failAudit) Before(ev audit.Event, act func() error) error {
	if err := a.Write(ev); err != nil {
		return err
	}
	return act()
}

// auditLine is the part of an audit line the tests read.
type auditLine struct {
	Event     string `json:"event"`
	Tenant    string `json:"tenant"`
	CommandID string `json:"command_id"`
	Status    string `json:"status"`
	ErrorCode string `json:"error_code"`
	Signal    string `json:"signal"`
	Command   string `json:"command"`
}

// events returns the audit events of command id, in order.
func (a *failAudit) events(t *testing.T, id tenant.CommandID) []auditLine {
	t.Helper()
	f, err := os.Open(a.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []auditLine
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l auditLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatal(err)
		}
		if l.CommandID == string(id) {
			out = append(out, l)
		}
	}
	return out
}

func (a *failAudit) names(t *testing.T, id tenant.CommandID) []string {
	t.Helper()
	var out []string
	for _, l := range a.events(t, id) {
		out = append(out, l.Event)
	}
	return out
}

// harness is an engine with two tenants: tenant-a has srv-1 and srv-2 on
// one sshtest server, tenant-b has srv-1 on another.
type harness struct {
	t       *testing.T
	dir     string
	e       *Engine
	cfg     Config
	sa, sb  *sshtest.Server
	au      *failAudit
	errsMu  sync.Mutex
	errs    []error
	actorA  audit.Actor
	actorB  audit.Actor
	clockMu sync.Mutex
	clock   time.Time // zero: the real clock
}

type opt func(h *harness, cfg *Config)

func withLimits(f func(l *tenant.Limits)) opt {
	return func(_ *harness, cfg *Config) {
		for i := range cfg.Tenants {
			f(&cfg.Tenants[i].Limits)
		}
	}
}

func withServer(o sshtest.Options) opt {
	return func(h *harness, cfg *Config) {
		h.sa = sshtest.Start(h.t, o)
		for i := range cfg.Tenants[0].Servers {
			srv := serverFor(tA, cfg.Tenants[0].Servers[i].ID, h.sa)
			cfg.Tenants[0].Servers[i] = srv
		}
	}
}

// withRefusingPort points tenant-a's servers at a loopback port that is
// reserved for the test and refuses every connection. Closing the sshtest
// server instead would free its port, and a parallel test's server could be
// given it before the dial.
func withRefusingPort() opt {
	return func(h *harness, cfg *Config) {
		port := loopback.RefusingPort(h.t)
		for i := range cfg.Tenants[0].Servers {
			cfg.Tenants[0].Servers[i].Port = port
		}
	}
}

// newCheckedHarness is newHarness with a goroutine leak check that runs
// after every cleanup. The check sees every goroutine of the process, so it
// is used only in tests that do not run in parallel with others (TestLeaks).
func newCheckedHarness(t *testing.T, opts ...opt) *harness {
	t.Helper()
	leakcheck.Check(t)
	return newHarness(t, opts...)
}

func newHarness(t *testing.T, opts ...opt) *harness {
	t.Helper()
	h := &harness{t: t, dir: leakcheck.PrivDir(t)}
	h.sa = sshtest.Start(t, sshtest.Options{})
	h.sb = sshtest.Start(t, sshtest.Options{})
	logPath := filepath.Join(leakcheck.PrivDir(t), "audit.jsonl")
	l, err := audit.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	h.au = &failAudit{l: l, path: logPath, fail: map[string]bool{}}
	h.actorA = audit.Actor{RequestID: "req-a", Principal: tenant.Principal{Tenant: tA, Kind: tenant.KindUser, Subject: "a@example.net"}}
	h.actorB = audit.Actor{RequestID: "req-b", Principal: tenant.Principal{Tenant: tB, Kind: tenant.KindService, Subject: "svc-b"}}
	h.cfg = Config{
		DataDir: h.dir,
		Tenants: []tenant.Tenant{
			{ID: tA, Limits: testLimits(), Servers: []tenant.Server{serverFor(tA, "srv-1", h.sa), serverFor(tA, "srv-2", h.sa)}},
			{ID: tB, Limits: testLimits(), Servers: []tenant.Server{serverFor(tB, "srv-1", h.sb)}},
		},
		Audit:      h.au,
		OnError:    func(err error) { h.errsMu.Lock(); h.errs = append(h.errs, err); h.errsMu.Unlock() },
		SSHOptions: testSSH,
		Now: func() time.Time {
			h.clockMu.Lock()
			defer h.clockMu.Unlock()
			if h.clock.IsZero() {
				return time.Now()
			}
			return h.clock
		},
	}
	for _, o := range opts {
		o(h, &h.cfg)
	}
	h.e = h.open()
	return h
}

// open starts an engine on the harness's data directory and shuts it down
// at cleanup.
func (h *harness) open() *Engine {
	h.t.Helper()
	e, err := New(h.t.Context(), h.cfg)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = e.Shutdown(ctx)
	})
	return e
}

// settle waits until the janitor pass that any finished command asked for
// has run, so a test that changes the clock or the janitor's hooks next is
// not raced by it.
func (h *harness) settle() {
	h.t.Helper()
	reply := make(chan struct{})
	select {
	case h.e.settleReq <- reply:
	case <-time.After(30 * time.Second):
		h.t.Fatal("the janitor did not take the settle request")
	}
	select {
	case <-reply:
	case <-time.After(30 * time.Second):
		h.t.Fatal("the janitor did not finish its pass")
	}
}

func (h *harness) setClock(t time.Time) {
	h.clockMu.Lock()
	h.clock = t
	h.clockMu.Unlock()
}

func (h *harness) errors() []error {
	h.errsMu.Lock()
	defer h.errsMu.Unlock()
	return append([]error(nil), h.errs...)
}

// start starts command on tenant-a's srv-1 and fails the test on an error.
func (h *harness) start(command string, o StartOptions) Record {
	h.t.Helper()
	r, err := h.e.Start(h.t.Context(), tA, h.actorA, "srv-1", command, o)
	if err != nil {
		h.t.Fatalf("Start(%q): %v", command, err)
	}
	return r
}

// wait waits for a command of tenant-a to finish.
func (h *harness) wait(id tenant.CommandID) Record {
	h.t.Helper()
	r, err := h.e.Wait(h.t.Context(), tA, id, 30*time.Second)
	if err != nil {
		h.t.Fatal(err)
	}
	if r.Status == StatusRunning {
		h.t.Fatalf("command %s still running", id)
	}
	return r
}

// read reads a whole stream of a finished command.
func (h *harness) read(id tenant.CommandID, stream string) string {
	h.t.Helper()
	o, err := h.e.ReadOutput(h.t.Context(), tA, id, ReadRequest{Stream: stream})
	if err != nil {
		h.t.Fatal(err)
	}
	return string(o.Data)
}

// slots returns the tenant's and the server's reserved slots.
func (h *harness) slots(e *Engine, tid tenant.TenantID, sid tenant.ServerID) (perTenant, perServer int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.tenants[tid].running, e.tenants[tid].perServer[sid]
}

func (h *harness) noSlots(e *Engine) {
	h.t.Helper()
	for _, tid := range []tenant.TenantID{tA, tB} {
		for _, sid := range []tenant.ServerID{"srv-1", "srv-2"} {
			if a, b := h.slots(e, tid, sid); a != 0 || b != 0 {
				h.t.Fatalf("%s/%s: %d tenant slots and %d server slots still reserved", tid, sid, a, b)
			}
		}
	}
}

func boolp(b bool) *bool { return &b }

// procFD lists this process's open files.
const procFD = "/proc/self/fd"

// openFilesUnder returns the files this process has open under dir.
func openFilesUnder(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(procFD)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		p, err := os.Readlink(filepath.Join(procFD, e.Name()))
		if err == nil && strings.HasPrefix(p, dir+string(filepath.Separator)) {
			out = append(out, p)
		}
	}
	return out
}
