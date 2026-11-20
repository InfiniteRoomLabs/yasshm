package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/accesstest"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/edgetest"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/leakcheck"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/loopback"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/sshtest"
)

const operator = "operator@example.net"

// syncBuffer is a writer safe for concurrent use.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// serveFixture is a config for serve against an sshtest target and the
// local issuer, with a private data directory and audit log.
type serveFixture struct {
	ssh     *sshtest.Server
	is      *accesstest.Issuer
	dataDir string
	audit   string
	config  string
}

func newServeFixture(t *testing.T, listen string) *serveFixture {
	t.Helper()
	f := &serveFixture{ssh: sshtest.Start(t, sshtest.Options{}), is: accesstest.New(t, nil), dataDir: leakcheck.PrivDir(t)}
	f.audit = filepath.Join(leakcheck.PrivDir(t), "audit.jsonl")
	f.config = f.write(t, listen, f.dataDir, f.audit)
	return f
}

// write writes a config file and returns its path.
func (f *serveFixture) write(t *testing.T, listen, dataDir, auditLog string) string {
	t.Helper()
	text := fmt.Sprintf(`version: 1
server:
  listen: %s
  data_dir: %s
  audit_log: %s
auth:
  provider: cloudflare-access
  cloudflare_access:
    team_domain: %s
    audience: %s
    certs_url: %s
tenants:
  - id: default
    principals: [{email: %s}]
    identities:
      look: {user: %s, private_key_file: %s}
    servers:
      - {id: lab-1, host: 127.0.0.1, port: %d, identity: look, host_key: %q, access: look}
    limits: {kill_grace_seconds: 1, shutdown_grace_seconds: 5, default_wait_seconds: 1}
`, listen, dataDir, auditLog, accesstest.TeamDomain, accesstest.Audience, f.is.CertsURL(), operator,
		f.ssh.User, f.ssh.ClientKeyFile, f.ssh.Port, f.ssh.HostKeyLine())
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// running is a Serve call in progress.
type running struct {
	stderr *syncBuffer
	done   chan int
	cancel context.CancelFunc
}

func startServe(t *testing.T, args ...string) *running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{stderr: &syncBuffer{}, done: make(chan int, 1), cancel: cancel}
	go func() { r.done <- Serve(ctx, args, io.Discard, r.stderr) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(60 * time.Second):
			t.Error("serve did not return")
		}
	})
	return r
}

// logLine finds the first JSON log line with msg and returns it.
func (r *running) logLine(msg string) map[string]any {
	sc := bufio.NewScanner(strings.NewReader(r.stderr.String()))
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil && m["msg"] == msg {
			return m
		}
	}
	return nil
}

// addr waits for the "listening" line and returns the bound address.
func (r *running) addr(t *testing.T) string {
	t.Helper()
	var addr string
	ok := leakcheck.Poll(30*time.Second, func() bool {
		if m := r.logLine("listening"); m != nil {
			addr, _ = m["listen"].(string)
		}
		return addr != ""
	})
	if !ok {
		t.Fatalf("serve never listened:\n%s", r.stderr.String())
	}
	return addr
}

// exit waits for Serve's exit code.
func (r *running) exit(t *testing.T) int {
	t.Helper()
	select {
	case code := <-r.done:
		r.done <- code
		return code
	case <-time.After(60 * time.Second):
		t.Fatalf("serve did not return:\n%s", r.stderr.String())
		return -1
	}
}

func get(t *testing.T, url string) (code int, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.WithoutCancel(t.Context()), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

// auditEvents reads the audit log's events.
func auditEvents(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: the test's own audit log
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// TestServe runs the gateway in-process against the local issuer and an
// sshtest target: one tool call through the stand-in edge, /healthz, a
// second gateway on the same data directory refused, and a graceful
// shutdown with a command running, which ends killed. It leaves nothing
// running.
func TestServe(t *testing.T) {
	leakcheck.Check(t)
	f := newServeFixture(t, "127.0.0.1:0")
	r := startServe(t, "--config", f.config)
	addr := r.addr(t)
	base := "http://" + addr
	if !leakcheck.Poll(20*time.Second, func() bool { code, _ := get(t, base+"/healthz"); return code == http.StatusOK }) {
		t.Fatalf("never ready:\n%s", r.stderr.String())
	}
	t.Run("happy/healthz", func(t *testing.T) {
		code, body := get(t, base+"/healthz")
		if code != http.StatusOK || body != `{"status":"ok","version":"dev"}` {
			t.Fatalf("healthz = %d %s", code, body)
		}
	})
	t.Run("sad/straight to the gateway without a token", func(t *testing.T) {
		if code, _ := get(t, base+"/mcp"); code != http.StatusUnauthorized {
			t.Fatalf("status = %d", code)
		}
	})
	edge := edgetest.New(t, base, f.is, operator)
	c := mcp.NewClient(&mcp.Implementation{Name: "serve-test", Version: "test"}, nil)
	cs, err := c.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: edge.URL + "/mcp", DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var commandID string
	t.Run("happy/a tool call through the edge", func(t *testing.T) {
		res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "run_command", Arguments: map[string]any{
			"server": "lab-1", "command": "echo serving; sleep 30", "wait_seconds": 0,
		}})
		if err != nil || res.IsError {
			t.Fatalf("run_command: %v %+v", err, res)
		}
		b, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			CommandID string `json:"command_id"`
			Status    string `json:"status"`
		}
		if err := json.Unmarshal(b, &out); err != nil || out.Status != "running" {
			t.Fatalf("run = %s", b)
		}
		commandID = out.CommandID
	})
	_ = cs.Close()
	t.Run("sad/a second gateway on the same data directory", func(t *testing.T) {
		second := f.write(t, "127.0.0.1:0", f.dataDir, filepath.Join(leakcheck.PrivDir(t), "audit2.jsonl"))
		r2 := startServe(t, "--config", second)
		if code := r2.exit(t); code != exitError {
			t.Fatalf("exit = %d", code)
		}
		if m := r2.logLine("start-up failed"); m == nil || m["reason"] != "the data directory is in use by another gateway" {
			t.Fatalf("stderr:\n%s", r2.stderr.String())
		}
	})
	t.Run("happy/graceful shutdown", func(t *testing.T) {
		r.cancel()
		if code := r.exit(t); code != exitOK {
			t.Fatalf("exit = %d:\n%s", code, r.stderr.String())
		}
		events := auditEvents(t, f.audit)
		first, last := events[0], events[len(events)-1]
		if first["event"] != "gateway.started" || first["listen"] != addr || first["version"] != "dev" {
			t.Fatalf("first event %v", first)
		}
		if last["event"] != "gateway.stopped" || last["reason"] != "signal" {
			t.Fatalf("last event %v", last)
		}
		killed := false
		for _, e := range events {
			if e["event"] == "command.finished" && e["command_id"] == commandID && e["status"] == "killed" {
				killed = true
			}
		}
		if !killed {
			t.Fatalf("the running command did not end killed: %v", events)
		}
	})
	t.Run("happy/the logs hold no token, command, host or key path", func(t *testing.T) {
		logs := r.stderr.String()
		for _, bad := range []string{"eyJ", "sleep 30", "serving", f.ssh.ClientKeyFile, "127.0.0.1:" + strconv.Itoa(f.ssh.Port), accesstest.Audience} {
			if strings.Contains(logs, bad) {
				t.Fatalf("log holds %q:\n%s", bad, logs)
			}
		}
		for _, msg := range []string{"config loaded", "listening", "readiness", "shutting down", "stopped"} {
			if r.logLine(msg) == nil {
				t.Fatalf("no %q line:\n%s", msg, logs)
			}
		}
		if m := r.logLine("config loaded"); m["tenants"] != float64(1) || m["servers"] != float64(1) {
			t.Fatalf("config loaded = %v", m)
		}
	})
}

func TestServeStartFailures(t *testing.T) {
	f := newServeFixture(t, "127.0.0.1:0")
	for _, tc := range []struct {
		name   string
		args   []string
		code   int
		reason string
	}{
		{"sad/no config flag", nil, exitUsage, ""},
		{"sad/unknown flag", []string{"--nope"}, exitUsage, ""},
		{"sad/extra argument", []string{"--config", f.config, "extra"}, exitUsage, ""},
		{"sad/missing config", []string{"--config", filepath.Join(t.TempDir(), "none.yaml")}, exitError, "the config is not valid"},
		{"sad/non-loopback listen", []string{"--config", f.write(t, "0.0.0.0:0", f.dataDir, f.audit)}, exitError, "the config is not valid"},
		{"sad/missing data directory", []string{"--config", f.write(t, "127.0.0.1:0", filepath.Join(f.dataDir, "nope"), f.audit)}, exitError, "the job engine cannot start (is the data directory there?)"},
		{"sad/data directory not private", []string{"--config", f.write(t, "127.0.0.1:0", t.TempDir(), f.audit)}, exitError, "the data directory or database is not private to this user (0700 directory, 0600 files)"},
		{"sad/audit log is a directory", []string{"--config", f.write(t, "127.0.0.1:0", f.dataDir, t.TempDir())}, exitError, "the audit log cannot be opened (it must be a regular file owned by this user, mode 0600)"},
		{"sad/listen address taken", []string{"--config", f.write(t, "127.0.0.1:"+strconv.Itoa(loopback.RefusingPort(t)), f.dataDir, f.audit)}, exitError, "the listen address cannot be bound"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := startServe(t, tc.args...)
			if code := r.exit(t); code != tc.code {
				t.Fatalf("exit = %d:\n%s", code, r.stderr.String())
			}
			if tc.reason != "" {
				if m := r.logLine("start-up failed"); m == nil || m["reason"] != tc.reason {
					t.Fatalf("stderr:\n%s", r.stderr.String())
				}
			}
		})
	}
	t.Run("happy/help", func(t *testing.T) {
		var out bytes.Buffer
		if code := Serve(t.Context(), []string{"--help"}, &out, io.Discard); code != exitOK || !strings.Contains(out.String(), "usage: yasshm serve") {
			t.Fatalf("help = %d %q", code, out.String())
		}
	})
	t.Run("sad/config problems are logged one per line", func(t *testing.T) {
		r := startServe(t, "--config", f.write(t, "0.0.0.0:0", f.dataDir, f.audit))
		r.exit(t)
		if m := r.logLine("config problem"); m == nil || !strings.Contains(m["problem"].(string), "server.listen") {
			t.Fatalf("stderr:\n%s", r.stderr.String())
		}
	})
}

func TestWatchSignals(t *testing.T) {
	ch := make(chan os.Signal, 2)
	stop := make(chan struct{})
	cancelled, forced := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchSignals(ch, stop, func() { close(cancelled) }, func() { close(forced) })
	}()
	ch <- syscall.SIGTERM
	<-cancelled
	select {
	case <-forced:
		t.Fatal("the first signal forced the exit")
	default:
	}
	ch <- syscall.SIGINT
	<-forced
	<-done
	t.Run("happy/stop ends the watch", func(_ *testing.T) {
		stop2 := make(chan struct{})
		end := make(chan struct{})
		go func() {
			defer close(end)
			watchSignals(make(chan os.Signal), stop2, func() {}, func() {})
		}()
		close(stop2)
		<-end
	})
	close(stop)
}
