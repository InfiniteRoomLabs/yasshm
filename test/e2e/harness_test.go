package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/accesstest"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/edgetest"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/leakcheck"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/sshtest"
)

// The identities the edge acts as. operator is the tenant's principal;
// stranger is a valid Access user no tenant maps.
const (
	operator = "operator@example.net"
	stranger = "stranger@example.net"
)

// Values stamped into the binary, as scripts/build.sh does with -X.
const (
	buildVersion = "0.0.0-e2e"
	buildCommit  = "e2e0000000000000000000000000000000000000"
	buildDate    = "2026-11-20T00:00:00Z"
	cliPkg       = "github.com/InfiniteRoomLabs/yasshm/internal/cli"
)

// Bounds of the waits on the child process.
const (
	startBound = 30 * time.Second
	// stopBound is how long a SIGTERM may take: the tenant's
	// shutdown_grace_seconds is 5 in every scenario, plus the gateway's
	// 5 s for requests still answering, plus slack.
	stopBound = 30 * time.Second
	callBound = 60 * time.Second
)

var (
	binDir   string
	buildMu  sync.Mutex
	builtBin string
	buildErr error
)

// TestMain owns the directory the binary is built into. The build itself is
// lazy (binary), so `go test -list` and a run that matches no scenario
// build nothing.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "yasshm-e2e-")
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "e2e:", err)
		os.Exit(1)
	}
	binDir = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// binary builds the real binary once per test run, with the flags of
// scripts/build.sh (CGO off, -trimpath, -s -w and the -X version
// variables), by the Go toolchain this test binary was built with (the
// `go` on PATH that mise put there; its version must match), and returns
// its path.
func binary(t *testing.T) string {
	t.Helper()
	buildMu.Lock()
	defer buildMu.Unlock()
	if builtBin == "" && buildErr == nil {
		builtBin, buildErr = build()
	}
	if buildErr != nil {
		t.Fatalf("building yasshm: %v", buildErr)
	}
	return builtBin
}

func build() (string, error) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		return "", fmt.Errorf("no go on PATH (run the suite through mise): %w", err)
	}
	ver, err := exec.Command(goBin, "env", "GOVERSION").Output() //nolint:gosec // G204: the toolchain found on PATH, fixed arguments
	if err != nil {
		return "", fmt.Errorf("go env GOVERSION: %w", err)
	}
	if got := strings.TrimSpace(string(ver)); got != runtime.Version() {
		return "", fmt.Errorf("the go on PATH is %s but the suite was built with %s (run it through mise)", got, runtime.Version())
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		return "", err
	}
	out := filepath.Join(binDir, "yasshm")
	ldflags := fmt.Sprintf("-s -w -X %[1]s.Version=%[2]s -X %[1]s.Commit=%[3]s -X %[1]s.Date=%[4]s", cliPkg, buildVersion, buildCommit, buildDate)
	cmd := exec.Command(goBin, "build", "-trimpath", "-ldflags", ldflags, "-o", out, "./cmd/yasshm") //nolint:gosec // G204: the toolchain found on PATH, fixed arguments
	cmd.Dir = root
	cmd.Env = append(cleanEnv(os.Environ(), "CGO_ENABLED", "GOFLAGS", "GOOS", "GOARCH"), "CGO_ENABLED=0", "GOFLAGS=")
	var errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &errb, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w\n%s", err, errb.String())
	}
	return out, nil
}

// cleanEnv drops the named variables from env.
func cleanEnv(env []string, names ...string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(names, k) {
			out = append(out, kv)
		}
	}
	return out
}

// target is a server of the scenario's tenant: an sshtest server and the
// id the config gives it.
type target struct {
	id  string
	srv *sshtest.Server
}

// fixture is one scenario's world: a private directory with the data
// directory, the audit log and the config, the local issuer, the targets
// and the tenant's limits.
type fixture struct {
	t       *testing.T
	is      *accesstest.Issuer
	dataDir string
	audit   string
	config  string
	targets []target
	limits  map[string]int64
	// certsURL overrides the issuer's key set URL (a refusing port).
	certsURL string
	// denyCommands are operator deny_commands patterns.
	denyCommands []string
	// extraTenant adds a second tenant to the config text.
	extraTenant bool
}

// fopt changes a fixture before its config is written.
type fopt func(f *fixture)

// withLimits sets limits on top of the scenario defaults.
func withLimits(kv map[string]int64) fopt {
	return func(f *fixture) {
		for k, v := range kv {
			f.limits[k] = v
		}
	}
}

// withTarget adds a target with the given sshtest options.
func withTarget(id string, o sshtest.Options) fopt {
	return func(f *fixture) {
		f.targets = append(f.targets, target{id: id, srv: sshtest.Start(f.t, o)})
	}
}

// withCertsURL points the authenticator at url instead of the issuer.
func withCertsURL(url string) fopt { return func(f *fixture) { f.certsURL = url } }

// withDenyCommands adds operator deny_commands patterns.
func withDenyCommands(p ...string) fopt {
	return func(f *fixture) { f.denyCommands = append(f.denyCommands, p...) }
}

// withTwoTenants writes a second tenant into the config.
func withTwoTenants() fopt { return func(f *fixture) { f.extraTenant = true } }

// newFixture writes a config with one tenant ("default", principal
// operator) and the target lab-1 plus any added ones. Every scenario's
// default limits keep it fast: a short kill grace and shutdown grace and a
// one-second default wait.
func newFixture(t *testing.T, opts ...fopt) *fixture {
	t.Helper()
	f := &fixture{
		t: t, is: accesstest.New(t, nil), dataDir: leakcheck.PrivDir(t),
		limits: map[string]int64{"kill_grace_seconds": 1, "shutdown_grace_seconds": 5, "default_wait_seconds": 1, "connect_timeout_seconds": 5},
	}
	f.audit = filepath.Join(leakcheck.PrivDir(t), "audit.jsonl")
	f.targets = []target{{id: "lab-1", srv: sshtest.Start(t, sshtest.Options{})}}
	for _, o := range opts {
		o(f)
	}
	f.config = f.write(t)
	// After every scenario (cleanups run last-in first-out, so this runs
	// after the gateway has stopped): no command the targets started is
	// left running.
	t.Cleanup(func() {
		for _, tg := range f.targets {
			if !leakcheck.Poll(15*time.Second, func() bool { return tg.srv.Running() == 0 }) {
				t.Errorf("target %s: %d commands still running after the scenario", tg.id, tg.srv.Running())
			}
		}
	})
	return f
}

// target returns the sshtest server behind id.
func (f *fixture) target(id string) *sshtest.Server {
	for _, tg := range f.targets {
		if tg.id == id {
			return tg.srv
		}
	}
	f.t.Fatalf("no target %s", id)
	return nil
}

// write renders the config file and returns its path.
func (f *fixture) write(t *testing.T) string {
	t.Helper()
	certs := f.certsURL
	if certs == "" {
		certs = f.is.CertsURL()
	}
	var b strings.Builder
	fmt.Fprintf(&b, `version: 1
server:
  listen: 127.0.0.1:0
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
`, f.dataDir, f.audit, accesstest.TeamDomain, accesstest.Audience, certs, operator)
	for i, tg := range f.targets {
		fmt.Fprintf(&b, "      id-%d: {user: %s, private_key_file: %s}\n", i, tg.srv.User, tg.srv.ClientKeyFile)
	}
	b.WriteString("    servers:\n")
	for i, tg := range f.targets {
		fmt.Fprintf(&b, "      - {id: %s, description: \"e2e target %s\", tags: [e2e], host: 127.0.0.1, port: %d, identity: id-%d, host_key: %q, access: look}\n",
			tg.id, tg.id, tg.srv.Port, i, tg.srv.HostKeyLine())
	}
	keys := make([]string, 0, len(f.limits))
	for k := range f.limits {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %d", k, f.limits[k]))
	}
	fmt.Fprintf(&b, "    limits: {%s}\n", strings.Join(parts, ", "))
	if len(f.denyCommands) > 0 {
		q := make([]string, 0, len(f.denyCommands))
		for _, p := range f.denyCommands {
			q = append(q, fmt.Sprintf("%q", p))
		}
		fmt.Fprintf(&b, "    policy: {deny_commands: [%s]}\n", strings.Join(q, ", "))
	}
	if f.extraTenant {
		tg := f.targets[0]
		fmt.Fprintf(&b, `  - id: second
    principals: [{email: second@example.net}]
    identities:
      id-b: {user: %s, private_key_file: %s}
    servers:
      - {id: lab-b, host: 127.0.0.1, port: %d, identity: id-b, host_key: %q, access: look}
`, tg.srv.User, tg.srv.ClientKeyFile, tg.srv.Port, tg.srv.HostKeyLine())
	}
	p := filepath.Join(leakcheck.PrivDir(t), "config.yaml")
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// run runs the binary to completion with args and returns its exit code
// and output.
func run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), callBound)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary(t), args...) //nolint:gosec // G204: the binary under test with test arguments
	cmd.Env = childEnv()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, out.String(), errb.String()
	case errors.As(err, &ee) && ctx.Err() == nil:
		return ee.ExitCode(), out.String(), errb.String()
	default:
		t.Fatalf("running yasshm %v: %v\n%s", args, err, errb.String())
		return 0, "", ""
	}
}

// childEnv is the environment of the binary: the test's, without the
// variables that would change what the binary does (the SDK's debug knob).
func childEnv() []string {
	return cleanEnv(os.Environ(), "MCPGODEBUG", "GODEBUG", "GOTRACEBACK")
}

// gateway is a running `yasshm serve` child process.
type gateway struct {
	t   *testing.T
	f   *fixture
	cmd *exec.Cmd
	// URL is the gateway's own base URL (http://127.0.0.1:<port>).
	URL string
	pid int

	mu      sync.Mutex
	lines   []string
	changed chan struct{}
	exited  chan struct{}
	code    int
	waitErr error
}

// start starts `yasshm serve` on f's config in its own process group,
// waits for the "listening" log line and reads the bound address from it.
// The child is stopped with SIGTERM at cleanup (and killed with its group
// if it does not exit within stopBound, which fails the test); its stderr
// is attached to the test log when the test failed.
func (f *fixture) start(t *testing.T) *gateway {
	t.Helper()
	g := &gateway{t: t, f: f, changed: make(chan struct{}), exited: make(chan struct{})}
	cmd := exec.Command(binary(t), "serve", "--config", f.config) //nolint:gosec // G204: the binary under test with test arguments
	cmd.Env = childEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	g.cmd, g.pid = cmd, cmd.Process.Pid
	go g.read(stderr)
	t.Cleanup(g.cleanup)
	line, ok := g.waitLog(startBound, func(m map[string]any) bool { return m["msg"] == "listening" })
	if !ok {
		t.Fatalf("the gateway never logged listening:\n%s", g.stderr())
	}
	addr, _ := line["listen"].(string)
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("listening on %q", addr)
	}
	g.URL = "http://" + addr
	return g
}

// read copies the child's stderr into lines until EOF, then reaps it.
func (g *gateway) read(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		g.mu.Lock()
		g.lines = append(g.lines, sc.Text())
		close(g.changed)
		g.changed = make(chan struct{})
		g.mu.Unlock()
	}
	_, _ = io.Copy(io.Discard, r)
	err := g.cmd.Wait()
	g.mu.Lock()
	g.waitErr = err
	g.code = g.cmd.ProcessState.ExitCode()
	g.mu.Unlock()
	close(g.exited)
}

// stderr returns everything the child wrote to stderr so far.
func (g *gateway) stderr() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return strings.Join(g.lines, "\n")
}

// waitLog waits up to d for a JSON log line that match accepts and returns
// it; it gives up early when the child exits.
func (g *gateway) waitLog(d time.Duration, match func(map[string]any) bool) (map[string]any, bool) {
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	seen := 0
	for {
		g.mu.Lock()
		lines, changed := g.lines[seen:], g.changed
		seen = len(g.lines)
		g.mu.Unlock()
		for _, l := range lines {
			var m map[string]any
			if json.Unmarshal([]byte(l), &m) == nil && match(m) {
				return m, true
			}
		}
		select {
		case <-changed:
		case <-g.exited:
			g.mu.Lock()
			done := seen == len(g.lines)
			g.mu.Unlock()
			if done {
				return nil, false
			}
		case <-deadline.C:
			return nil, false
		}
	}
}

// signal sends sig to the gateway process.
func (g *gateway) signal(sig syscall.Signal) {
	g.t.Helper()
	if err := syscall.Kill(g.pid, sig); err != nil {
		g.t.Fatalf("signal %v: %v", sig, err)
	}
}

// wait waits up to d for the child to exit and returns its exit code.
func (g *gateway) wait(d time.Duration) (int, bool) {
	select {
	case <-g.exited:
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.code, true
	case <-time.After(d):
		return 0, false
	}
}

// stop sends SIGTERM and waits for the exit; it returns the exit code and
// fails the test if the child does not exit within stopBound.
func (g *gateway) stop() int {
	g.t.Helper()
	g.signal(syscall.SIGTERM)
	code, ok := g.wait(stopBound)
	if !ok {
		g.killGroup()
		g.t.Fatalf("the gateway did not exit within %v of SIGTERM:\n%s", stopBound, g.stderr())
	}
	return code
}

// killGroup kills the child's process group and reaps it.
func (g *gateway) killGroup() {
	_ = syscall.Kill(-g.pid, syscall.SIGKILL)
	<-g.exited
}

// cleanup stops a child the scenario left running (SIGTERM within
// stopBound, else a failure and SIGKILL), checks that no process of its
// group is left, and attaches its stderr to the log of a failed test.
func (g *gateway) cleanup() {
	select {
	case <-g.exited:
	default:
		_ = syscall.Kill(g.pid, syscall.SIGTERM)
		if _, ok := g.wait(stopBound); !ok {
			g.killGroup()
			g.t.Errorf("the gateway did not exit within %v of SIGTERM at cleanup", stopBound)
		}
	}
	if err := syscall.Kill(-g.pid, 0); !errors.Is(err, syscall.ESRCH) {
		_ = syscall.Kill(-g.pid, syscall.SIGKILL)
		g.t.Errorf("a process of the gateway's group was still alive after it exited (kill -0: %v)", err)
	}
	if g.t.Failed() {
		g.t.Logf("gateway stderr:\n%s", g.stderr())
	}
}

// healthz GETs /healthz from the gateway itself and returns the status and
// body.
func (g *gateway) healthz(t *testing.T) (status int, body string) {
	t.Helper()
	return httpDo(t, http.MethodGet, g.URL+"/healthz", nil, nil)
}

// waitReady waits until /healthz answers 200: the authenticator has loaded
// the issuer's key set.
func (g *gateway) waitReady(t *testing.T) {
	t.Helper()
	var code int
	var body string
	if !leakcheck.Poll(startBound, func() bool { code, body = g.healthz(t); return code == http.StatusOK }) {
		t.Fatalf("/healthz never answered 200 (last %d %s):\n%s", code, body, g.stderr())
	}
}

// httpDo sends one request to a loopback URL the test built and returns
// the status and body.
func httpDo(t *testing.T, method, url string, header http.Header, body []byte) (status int, respBody string) {
	t.Helper()
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("refusing a non-loopback URL %q", url)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), callBound)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

// edge starts a stand-in edge in front of g that acts as operator.
func (g *gateway) edge(t *testing.T) *edgetest.Edge {
	t.Helper()
	return edgetest.New(t, g.URL, g.f.is, operator)
}

// client is an MCP client session through an edge.
type client struct {
	cs *mcp.ClientSession
}

// connect opens an MCP client session through e.
func connect(t *testing.T, e *edgetest.Edge) *client {
	t.Helper()
	cs, err := dial(t, e)
	if err != nil {
		t.Fatalf("connecting through the edge: %v", err)
	}
	return &client{cs: cs}
}

func dial(t *testing.T, e *edgetest.Edge) (*mcp.ClientSession, error) {
	t.Helper()
	if !strings.HasPrefix(e.URL, "http://127.0.0.1:") {
		t.Fatalf("refusing a non-loopback edge URL %q", e.URL)
	}
	hc := &http.Client{Timeout: callBound}
	c := mcp.NewClient(&mcp.Implementation{Name: "yasshm-e2e", Version: "test"}, nil)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), callBound)
	defer cancel()
	cs, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: e.URL + "/mcp", HTTPClient: hc, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, nil
}

// call calls tool with args; a protocol error fails the test.
func (c *client) call(t *testing.T, tool string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := c.callErr(t, tool, args)
	if err != nil {
		t.Fatalf("%s: protocol error: %v", tool, err)
	}
	return res
}

func (c *client) callErr(t *testing.T, tool string, args any) (*mcp.CallToolResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), callBound)
	defer cancel()
	return c.cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
}

// ok calls tool and decodes its structured content into dst; a tool error
// fails the test.
func (c *client) ok(t *testing.T, tool string, args, dst any) {
	t.Helper()
	decodeResult(t, tool, c.call(t, tool, args), dst)
}

// decodeResult decodes a successful result's structured content into dst;
// a tool error fails the test.
func decodeResult(t *testing.T, tool string, res *mcp.CallToolResult, dst any) {
	t.Helper()
	if res.IsError {
		t.Fatalf("%s: tool error %s", tool, texts(t, res))
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		t.Fatalf("%s: decoding %s: %v", tool, b, err)
	}
}

// toolError is the one text block of an error result.
type toolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Rule    string `json:"rule"`
}

// fail calls tool and returns its tool error; a success fails the test.
func (c *client) fail(t *testing.T, tool string, args any) toolError {
	t.Helper()
	res := c.call(t, tool, args)
	return asError(t, tool, res)
}

func asError(t *testing.T, tool string, res *mcp.CallToolResult) toolError {
	t.Helper()
	if !res.IsError {
		t.Fatalf("%s: want a tool error, got %v", tool, texts(t, res))
	}
	if res.StructuredContent != nil {
		t.Fatalf("%s: an error result carries structured content", tool)
	}
	b := texts(t, res)
	if len(b) != 1 {
		t.Fatalf("%s: error blocks %q", tool, b)
	}
	var e toolError
	if err := json.Unmarshal([]byte(b[0]), &e); err != nil || e.Code == "" {
		t.Fatalf("%s: error text %q: %v", tool, b[0], err)
	}
	return e
}

// texts returns a result's text blocks.
func texts(t *testing.T, r *mcp.CallToolResult) []string {
	t.Helper()
	out := make([]string, 0, len(r.Content))
	for _, c := range r.Content {
		tc, ok := c.(*mcp.TextContent)
		if !ok {
			t.Fatalf("content is %T", c)
		}
		out = append(out, tc.Text)
	}
	return out
}

// The result shapes the suite reads (a subset of each tool's output).
type (
	serverEntry struct {
		ID          string   `json:"id"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
		Access      string   `json:"access"`
	}
	listServersResult struct {
		Servers []serverEntry `json:"servers"`
	}
	serverInfoResult struct {
		ID        string `json:"id"`
		Reachable bool   `json:"reachable"`
		HostKeyOK *bool  `json:"host_key_ok"`
		ErrorCode string `json:"error_code"`
		Facts     *struct {
			Kernel string `json:"kernel"`
			Arch   string `json:"arch"`
		} `json:"facts"`
	}
	commandResult struct {
		CommandID   string `json:"command_id"`
		Server      string `json:"server"`
		Status      string `json:"status"`
		ExitCode    *int   `json:"exit_code"`
		Signal      string `json:"signal"`
		ErrorCode   string `json:"error_code"`
		MayStillRun bool   `json:"may_still_run"`
		Stdout      string `json:"stdout"`
		Stderr      string `json:"stderr"`
		NextOffset  int64  `json:"next_offset"`
		EOF         bool   `json:"eof"`
		Truncated   bool   `json:"truncated"`
		StdoutBytes int64  `json:"stdout_bytes"`
		StderrBytes int64  `json:"stderr_bytes"`
	}
	readResult struct {
		CommandID  string `json:"command_id"`
		Status     string `json:"status"`
		ErrorCode  string `json:"error_code"`
		ExitCode   *int   `json:"exit_code"`
		Data       string `json:"data"`
		Offset     int64  `json:"offset"`
		NextOffset int64  `json:"next_offset"`
		Size       int64  `json:"size"`
		EOF        bool   `json:"eof"`
		Truncated  bool   `json:"truncated"`
	}
	commandEntry struct {
		CommandID string `json:"command_id"`
		Server    string `json:"server"`
		Status    string `json:"status"`
		Command   string `json:"command"`
	}
	listCommandsResult struct {
		Commands   []commandEntry `json:"commands"`
		NextCursor string         `json:"next_cursor"`
	}
	killResult struct {
		CommandID       string `json:"command_id"`
		Status          string `json:"status"`
		Signal          string `json:"signal"`
		SignalDelivered bool   `json:"signal_delivered"`
	}
	downloadResult struct {
		Size       int64  `json:"size"`
		Encoding   string `json:"encoding"`
		Data       string `json:"data"`
		DataBase64 string `json:"data_base64"`
		NextOffset int64  `json:"next_offset"`
		EOF        bool   `json:"eof"`
	}
)

// runCommand starts command on server and returns the result.
func (c *client) runCommand(t *testing.T, server, command string, extra map[string]any) commandResult {
	t.Helper()
	args := map[string]any{"server": server, "command": command}
	for k, v := range extra {
		args[k] = v
	}
	var r commandResult
	c.ok(t, "run_command", args, &r)
	return r
}

// readOutput reads a command's output.
func (c *client) readOutput(t *testing.T, id string, extra map[string]any) readResult {
	t.Helper()
	args := map[string]any{"command_id": id}
	for k, v := range extra {
		args[k] = v
	}
	var r readResult
	c.ok(t, "read_output", args, &r)
	return r
}

// waitStatus reads the command until it is no longer running (each read
// waits up to five seconds for more output, bounded by bound overall) and
// returns the last read.
func (c *client) waitStatus(t *testing.T, id string, bound time.Duration) readResult {
	t.Helper()
	deadline := time.Now().Add(bound)
	for {
		r := c.readOutput(t, id, map[string]any{"offset": -1, "wait_seconds": 5})
		if r.Status != "running" {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("command %s still running after %v", id, bound)
		}
	}
}

// waitOutput reads stdout from offset 0 until it contains marker (each read
// waits for more output) and returns it.
func (c *client) waitOutput(t *testing.T, id, marker string) string {
	t.Helper()
	deadline := time.Now().Add(callBound)
	var out strings.Builder
	var off int64
	for {
		r := c.readOutput(t, id, map[string]any{"offset": off, "wait_seconds": 5})
		out.WriteString(r.Data)
		off = r.NextOffset
		if strings.Contains(out.String(), marker) {
			return out.String()
		}
		if r.Status != "running" && r.EOF {
			t.Fatalf("command %s ended (%s) without %q: %q", id, r.Status, marker, out.String())
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %q in the output of %s after %v: %q", marker, id, callBound, out.String())
		}
	}
}

// auditLine is the part of an audit event the suite reads.
type auditLine struct {
	Event     string          `json:"event"`
	Tenant    string          `json:"tenant"`
	Principal string          `json:"principal"`
	Tool      string          `json:"tool"`
	Server    string          `json:"server"`
	CommandID string          `json:"command_id"`
	Code      string          `json:"code"`
	Rule      string          `json:"rule"`
	Reason    string          `json:"reason"`
	Status    string          `json:"status"`
	Stage     string          `json:"stage"`
	Signal    string          `json:"signal"`
	Listen    string          `json:"listen"`
	Args      json.RawMessage `json:"args"`
}

// auditLog reads the scenario's audit log. Every complete line must parse;
// a last line without a newline (a write the gateway could not finish) is
// returned in rest.
func (f *fixture) auditLog(t *testing.T) (lines []auditLine, rest string) {
	t.Helper()
	b, err := os.ReadFile(f.audit)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if i := strings.LastIndexByte(text, '\n'); i < len(text)-1 {
		rest = text[i+1:]
		text = text[:i+1]
	}
	for _, l := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if l == "" {
			continue
		}
		var a auditLine
		if err := json.Unmarshal([]byte(l), &a); err != nil {
			t.Fatalf("audit line %q: %v", l, err)
		}
		lines = append(lines, a)
	}
	return lines, rest
}

// events returns the names of the audit events that match.
func (f *fixture) events(t *testing.T, match func(auditLine) bool) []string {
	t.Helper()
	lines, _ := f.auditLog(t)
	var out []string
	for _, l := range lines {
		if match(l) {
			out = append(out, l.Event)
		}
	}
	return out
}

// sshRequests returns how many requests a target has seen and how many
// connections it accepted: what a scenario compares to prove that nothing
// was sent.
func sshRequests(s *sshtest.Server) (requests, accepted int) {
	return len(s.Requests()), s.Accepted()
}
