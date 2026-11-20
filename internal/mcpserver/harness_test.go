package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/InfiniteRoomLabs/yasshm/internal/auth"
	"github.com/InfiniteRoomLabs/yasshm/internal/jobs"
	"github.com/InfiniteRoomLabs/yasshm/internal/policy"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/accesstest"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/audittest"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/leakcheck"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/sshtest"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/syncbuf"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/tenanttest"
)

const (
	tA tenant.TenantID = "tenant-a"
	tB tenant.TenantID = "tenant-b"

	userA = "alice@example.net"
	svcB  = "svc-b.access"
)

// harness is a gateway with two tenants built in code, real sshtest
// targets, a real audit log, a real job engine on SQLite and the local
// issuer, served on a loopback port.
type harness struct {
	t       *testing.T
	sa, sb  *sshtest.Server
	is      *accesstest.Issuer
	au      *audittest.FailLog
	e       *jobs.Engine
	gw      *Gateway
	url     string
	logs    *syncbuf.Buffer
	tenants []tenant.Tenant
	// inFlight is the gateway's MaxInFlight (0: derived).
	inFlight int
	pe       *policy.Engine
	guard    *auth.Guard
	// decisions counts the policy decisions the gateway took.
	decisions atomic.Int64

	mu       sync.Mutex
	sessions map[string]*mcp.ClientSession
	schemas  map[string]*jsonschema.Resolved
}

type hopt func(h *harness, ts []tenant.Tenant)

func withLimits(f func(l *tenant.Limits)) hopt {
	return func(_ *harness, ts []tenant.Tenant) {
		for i := range ts {
			f(&ts[i].Limits)
		}
	}
}

func withInFlight(n int) hopt {
	return func(h *harness, _ []tenant.Tenant) { h.inFlight = n }
}

func withTenants(f func(ts []tenant.Tenant)) hopt {
	return func(_ *harness, ts []tenant.Tenant) { f(ts) }
}

func newHarness(t *testing.T, opts ...hopt) *harness {
	t.Helper()
	h := &harness{t: t, logs: &syncbuf.Buffer{}, sessions: map[string]*mcp.ClientSession{}}
	h.sa = sshtest.Start(t, sshtest.Options{})
	h.sb = sshtest.Start(t, sshtest.Options{})
	h.tenants = []tenant.Tenant{
		{
			ID: tA, Limits: tenanttest.Limits(), Principals: []tenant.PrincipalMatch{{Email: userA}},
			Servers: []tenant.Server{tenanttest.Server(tA, "srv-1", h.sa), tenanttest.Server(tA, "srv-2", h.sa)},
		},
		{
			ID: tB, Limits: tenanttest.Limits(), Principals: []tenant.PrincipalMatch{{CommonName: svcB}},
			Servers: []tenant.Server{tenanttest.Server(tB, "srv-1", h.sb), tenanttest.Server(tB, "only-b", h.sb)},
		},
	}
	for _, o := range opts {
		o(h, h.tenants)
	}
	h.au = audittest.Open(t)
	logger := slog.New(slog.NewJSONHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	var err error
	h.e, err = jobs.New(t.Context(), jobs.Config{DataDir: leakcheck.PrivDir(t), Tenants: h.tenants, Audit: h.au, SSHOptions: tenanttest.SSHOptions})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = h.e.Shutdown(ctx)
	})
	pe, err := policy.NewEngine(h.tenants)
	if err != nil {
		t.Fatal(err)
	}
	h.pe = pe
	h.is = accesstest.New(t, nil)
	ca, err := auth.NewCloudflareAccess(h.is.Config(), h.tenants)
	if err != nil {
		t.Fatal(err)
	}
	ca.Start(t.Context())
	t.Cleanup(ca.Close)
	waitReady(t, ca)
	guard := auth.NewGuard(ca, h.au, auth.WithMiddlewareLogger(logger))
	t.Cleanup(guard.Close)
	h.guard = guard
	h.gw, err = New(Config{
		Version: "test", Tenants: h.tenants, Policy: countingDecider{pe, &h.decisions}, Jobs: h.e, Audit: h.au, Guard: guard,
		Health: ca, SSHOptions: tenanttest.SSHOptions, Logger: logger, MaxInFlight: h.inFlight,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.url = serve(t, h.gw.HTTPServer())
	return h
}

// countingDecider counts the decisions of the policy engine it wraps.
type countingDecider struct {
	e *policy.Engine
	n *atomic.Int64
}

func (c countingDecider) Decide(p tenant.Principal, srv *tenant.Server, tool policy.Tool, args policy.Args) policy.Decision {
	c.n.Add(1)
	return c.e.Decide(p, srv, tool, args)
}

// newHarnessTenants returns the harness's two tenants without servers.
func newHarnessTenants() []tenant.Tenant {
	return []tenant.Tenant{{ID: tA, Limits: tenanttest.Limits()}, {ID: tB, Limits: tenanttest.Limits()}}
}

// waitReady waits until the authenticator has loaded the issuer's keys.
func waitReady(t *testing.T, ca *auth.CloudflareAccess) {
	t.Helper()
	if !leakcheck.Poll(10*time.Second, ca.Ready) {
		t.Fatal("the authenticator never loaded the key set")
	}
}

// serve runs srv on a loopback port until the test ends and returns its
// base URL.
func serve(t *testing.T, srv *http.Server) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ln)
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		<-done
	})
	return "http://" + ln.Addr().String()
}

// tokenFor mints a valid token for who: userA is a user, anything else a
// service token.
func (h *harness) tokenFor(who string) string { return h.is.For(who).Sign() }

// tokenTransport sets the token header on every request.
type tokenTransport struct {
	base  http.RoundTripper
	token func() string
}

func (tt tokenTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if tok := tt.token(); tok != "" {
		r.Header.Set(auth.Header, tok)
	}
	return tt.base.RoundTrip(r)
}

// connect opens an MCP client session as who against url.
func connect(t *testing.T, url string, token func() string, version string) (*mcp.ClientSession, error) {
	t.Helper()
	hc := &http.Client{Transport: tokenTransport{base: http.DefaultTransport, token: token}, Timeout: 60 * time.Second}
	c := mcp.NewClient(&mcp.Implementation{Name: "yasshm-test", Version: "test"}, nil)
	var opts *mcp.ClientSessionOptions
	if version != "" {
		opts = &mcp.ClientSessionOptions{ProtocolVersion: version}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cs, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: url + PathMCP, HTTPClient: hc, DisableStandaloneSSE: true, MaxRetries: -1}, opts)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, nil
}

// session returns a cached client session for who.
func (h *harness) session(who string) *mcp.ClientSession {
	h.t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if cs, ok := h.sessions[who]; ok {
		return cs
	}
	cs, err := connect(h.t, h.url, func() string { return h.tokenFor(who) }, "")
	if err != nil {
		h.t.Fatal(err)
	}
	h.sessions[who] = cs
	return cs
}

// call calls tool as who with args (a JSON object text or a Go value) and
// returns the result. A protocol error fails the test.
func (h *harness) call(t *testing.T, who, tool string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := h.callErr(t, who, tool, args)
	if err != nil {
		t.Fatalf("%s: protocol error: %v", tool, err)
	}
	return res
}

func (h *harness) callErr(t *testing.T, who, tool string, args any) (*mcp.CallToolResult, error) {
	t.Helper()
	a := args
	if s, ok := args.(string); ok {
		a = json.RawMessage(s)
	}
	// Without the test's cancellation: cleanups call tools too.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 60*time.Second)
	defer cancel()
	res, err := h.session(who).CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: a})
	if err == nil {
		h.checkShape(t, tool, res)
	}
	return res, err
}

// checkShape holds every result to the wire rules of spec 5.8: a success's
// structured content validates against the output schema tools/list gave
// (as a schema-validating client checks it); an error has no structured
// content and one text block that parses to a code of the closed set.
func (h *harness) checkShape(t *testing.T, tool string, res *mcp.CallToolResult) {
	t.Helper()
	if res.IsError {
		if res.StructuredContent != nil {
			t.Fatalf("%s: an error result carries structured content", tool)
		}
		parseErr(t, res)
		return
	}
	schema := h.outputSchema(t, tool)
	if schema == nil {
		return
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(v); err != nil {
		t.Fatalf("%s: structured content does not validate against the output schema: %v\n%s", tool, err, b)
	}
}

// outputSchema returns the resolved output schema of tool from tools/list,
// fetched once per harness.
func (h *harness) outputSchema(t *testing.T, tool string) *jsonschema.Resolved {
	t.Helper()
	h.mu.Lock()
	loaded := h.schemas != nil
	h.mu.Unlock()
	if !loaded {
		res, err := h.session(userA).ListTools(context.WithoutCancel(t.Context()), nil)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]*jsonschema.Resolved{}
		for _, tl := range res.Tools {
			b, err := json.Marshal(tl.OutputSchema)
			if err != nil {
				t.Fatal(err)
			}
			var sc jsonschema.Schema
			if uerr := json.Unmarshal(b, &sc); uerr != nil {
				t.Fatal(uerr)
			}
			r, err := sc.Resolve(nil)
			if err != nil {
				t.Fatalf("%s: %v", tl.Name, err)
			}
			m[tl.Name] = r
		}
		h.mu.Lock()
		h.schemas = m
		h.mu.Unlock()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.schemas[tool]
}

// blocks is a result's text blocks.
func blocks(t *testing.T, r *mcp.CallToolResult) []string {
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

// text is a result's first text block, the gateway's own.
func text(t *testing.T, r *mcp.CallToolResult) string {
	t.Helper()
	b := blocks(t, r)
	if len(b) == 0 {
		t.Fatal("no content")
	}
	return b[0]
}

// strictDecode decodes JSON b into dst, refusing unknown fields.
func strictDecode(t *testing.T, b []byte, dst any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		t.Fatalf("decoding %s: %v", b, err)
	}
}

// decode decodes a successful result's structured content into dst.
func decode(t *testing.T, r *mcp.CallToolResult, dst any) {
	t.Helper()
	if r.IsError {
		t.Fatalf("tool error: %s", text(t, r))
	}
	b, err := json.Marshal(r.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	strictDecode(t, b, dst)
}

// parseErr parses an error result's one text block: one line of JSON
// {code, message, rule?} with a code of the closed set.
func parseErr(t *testing.T, r *mcp.CallToolResult) ToolError {
	t.Helper()
	b := blocks(t, r)
	if len(b) != 1 || strings.Contains(b[0], "\n") {
		t.Fatalf("error blocks = %q", b)
	}
	var e ToolError
	strictDecode(t, []byte(b[0]), &e)
	if !slices.Contains(Codes, e.Code) || len(Codes) != 10 || e.Message == "" {
		t.Fatalf("code %q is not one of the ten of spec 5.8", e.Code)
	}
	if b[0] != e.Error() {
		t.Fatalf("text %q is not the error's one-line form", b[0])
	}
	return e
}

// wantErr asserts r is a tool error with code (and rule when given) and
// returns it.
func wantErr(t *testing.T, r *mcp.CallToolResult, code, rule string) ToolError {
	t.Helper()
	if !r.IsError {
		t.Fatalf("want error %s, got success: %s", code, text(t, r))
	}
	if r.StructuredContent != nil {
		t.Fatal("an error result carries structured content")
	}
	e := parseErr(t, r)
	if e.Code != code || (rule != "" && e.Rule != rule) {
		t.Fatalf("error = %+v, want code %s rule %q", e, code, rule)
	}
	return e
}

// run starts command on server as who and returns the result.
func (h *harness) run(t *testing.T, who, server, command string, extra map[string]any) RunResult {
	t.Helper()
	args := map[string]any{"server": server, "command": command}
	for k, v := range extra {
		args[k] = v
	}
	var r RunResult
	decode(t, h.call(t, who, ToolRunCommand, args), &r)
	return r
}

// events returns the names of the audit events that match.
func (h *harness) events(t *testing.T, match func(audittest.Line) bool) []string {
	t.Helper()
	var out []string
	for _, l := range h.au.Lines(t) {
		if match(l) {
			out = append(out, l.Event)
		}
	}
	return out
}
