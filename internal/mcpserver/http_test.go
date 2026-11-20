package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/InfiniteRoomLabs/yasshm/internal/audit"
	"github.com/InfiniteRoomLabs/yasshm/internal/auth"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/edgetest"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/leakcheck"
)

// rawResp is an HTTP answer read whole.
type rawResp struct {
	code   int
	header http.Header
	body   []byte
}

// do sends one request straight to url and reads the whole answer.
func do(t *testing.T, method, url string, body []byte, header map[string]string) rawResp {
	t.Helper()
	req, err := http.NewRequestWithContext(context.WithoutCancel(t.Context()), method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	c := &http.Client{Timeout: 60 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return rawResp{code: resp.StatusCode, header: resp.Header, body: b}
}

// mcpHeaders are the headers of a legacy-protocol JSON-RPC POST, which a
// stateless server answers without an initialize.
func (h *harness) mcpHeaders(who string) map[string]string {
	m := map[string]string{
		"Content-Type":         "application/json",
		"Accept":               "application/json, text/event-stream",
		"Mcp-Protocol-Version": "2025-11-25",
	}
	if who != "" {
		m[auth.Header] = h.tokenFor(who)
	}
	return m
}

// rawCall posts one tools/call and returns the answer.
func (h *harness) rawCall(t *testing.T, who, tool string, args any) rawResp {
	t.Helper()
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
	if err != nil {
		t.Fatal(err)
	}
	return do(t, http.MethodPost, h.url+PathMCP, b, h.mcpHeaders(who))
}

// checkHeaders asserts the security headers and the absence of CORS.
func checkHeaders(t *testing.T, r rawResp) {
	t.Helper()
	if r.header.Get("Cache-Control") != "no-store" || r.header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers = %v", r.header)
	}
	for k := range r.header {
		if strings.HasPrefix(strings.ToLower(k), "access-control-") {
			t.Fatalf("CORS header %s", k)
		}
	}
}

type fakeHealth struct{}

func (fakeHealth) Ready() bool    { return false }
func (fakeHealth) Status() string { return "key set not loaded" }

func TestHTTPSurface(t *testing.T) {
	h := newHarness(t)
	t.Run("happy/healthz", func(t *testing.T) {
		r := do(t, http.MethodGet, h.url+PathHealthz, nil, nil)
		if r.code != http.StatusOK || string(r.body) != `{"status":"ok","version":"test"}` {
			t.Fatalf("healthz = %d %s", r.code, r.body)
		}
		checkHeaders(t, r)
		if r := do(t, http.MethodHead, h.url+PathHealthz, nil, nil); r.code != http.StatusOK {
			t.Fatalf("HEAD = %d", r.code)
		}
	})
	t.Run("sad/healthz takes GET only", func(t *testing.T) {
		r := do(t, http.MethodPost, h.url+PathHealthz, nil, nil)
		if r.code != http.StatusMethodNotAllowed || r.header.Get("Allow") != "GET, HEAD" {
			t.Fatalf("POST healthz = %d %v", r.code, r.header)
		}
	})
	t.Run("sad/healthz degraded while no key set is loaded", func(t *testing.T) {
		g, err := New(Config{Version: "test", Tenants: h.tenants, Policy: h.pe, Jobs: h.e, Audit: h.au, Guard: h.guard, Health: fakeHealth{}})
		if err != nil {
			t.Fatal(err)
		}
		url := serve(t, g.HTTPServer())
		r := do(t, http.MethodGet, url+PathHealthz, nil, nil)
		if r.code != http.StatusServiceUnavailable || string(r.body) != `{"status":"degraded","detail":"key set not loaded","version":"test"}` {
			t.Fatalf("healthz = %d %s", r.code, r.body)
		}
	})
	for _, p := range []string{"/", "/mcp/", "//mcp", "/MCP", "/healthz/", "/mcp?x=1/../", "/metrics", "/.well-known/oauth-authorization-server"} {
		t.Run("sad/404 "+p, func(t *testing.T) {
			r := do(t, http.MethodGet, h.url+p, nil, nil)
			if strings.HasPrefix(p, "/mcp?") {
				if r.code != http.StatusUnauthorized {
					t.Fatalf("%s = %d", p, r.code)
				}
				return
			}
			if r.code != http.StatusNotFound || string(r.body) != `{"error":"not found"}` {
				t.Fatalf("%s = %d %s", p, r.code, r.body)
			}
			checkHeaders(t, r)
		})
	}
	t.Run("sad/no token is 401 for every method", func(t *testing.T) {
		for _, m := range []string{http.MethodPost, http.MethodGet, http.MethodDelete, http.MethodPut} {
			r := do(t, m, h.url+PathMCP, []byte(`{}`), h.mcpHeaders(""))
			if r.code != http.StatusUnauthorized || string(r.body) != `{"error":"unauthorized"}` {
				t.Fatalf("%s = %d %s", m, r.code, r.body)
			}
			checkHeaders(t, r)
		}
	})
	t.Run("sad/GET and DELETE with a token are the SDK's 405", func(t *testing.T) {
		for _, m := range []string{http.MethodGet, http.MethodDelete} {
			r := do(t, m, h.url+PathMCP, nil, h.mcpHeaders(userA))
			if r.code != http.StatusMethodNotAllowed {
				t.Fatalf("%s = %d %s", m, r.code, r.body)
			}
			checkHeaders(t, r)
		}
	})
	t.Run("raging/a body over 2 MiB is 413", func(t *testing.T) {
		big := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_servers","arguments":{"x":"` + strings.Repeat("a", MaxBodyBytes) + `"}}}`)
		r := do(t, http.MethodPost, h.url+PathMCP, big, h.mcpHeaders(userA))
		if r.code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d", r.code)
		}
		if r := do(t, http.MethodPost, h.url+PathMCP, big, h.mcpHeaders("")); r.code != http.StatusUnauthorized {
			t.Fatalf("without a token = %d", r.code)
		}
	})
	t.Run("raging/headers over the cap are refused", func(t *testing.T) {
		hd := h.mcpHeaders(userA)
		hd["X-Pad"] = strings.Repeat("p", 2*MaxHeaderBytes)
		if r := do(t, http.MethodPost, h.url+PathMCP, []byte(`{}`), hd); r.code != http.StatusRequestHeaderFieldsTooLarge {
			t.Fatalf("status = %d", r.code)
		}
	})
	t.Run("raging/malformed JSON-RPC stays a protocol error", func(t *testing.T) {
		for _, body := range []string{`{not json`, `{"jsonrpc":"2.0","method":"tools/call"}`, `[]`, `"x"`} {
			r := do(t, http.MethodPost, h.url+PathMCP, []byte(body), h.mcpHeaders(userA))
			if r.code != http.StatusBadRequest {
				t.Fatalf("%s = %d %s", body, r.code, r.body)
			}
		}
	})
	t.Run("raging/an unknown tool is a protocol error, not a tool result", func(t *testing.T) {
		_, err := h.callErr(t, userA, "exec_shell", map[string]any{})
		if err == nil {
			t.Fatal("unknown tool answered")
		}
		r := h.rawCall(t, userA, "exec_shell", map[string]any{})
		var msg struct {
			Error  *struct{ Code int } `json:"error"`
			Result json.RawMessage     `json:"result"`
		}
		if err := json.Unmarshal(r.body, &msg); err != nil || msg.Error == nil || msg.Result != nil {
			t.Fatalf("answer = %d %s", r.code, r.body)
		}
	})
	t.Run("raging/methods a tools-only server does not answer", func(t *testing.T) {
		for _, m := range []string{"resources/list", "prompts/list", "logging/setLevel", "completion/complete", "subscriptions/listen"} {
			b := fmt.Appendf(nil, `{"jsonrpc":"2.0","id":7,"method":%q,"params":{}}`, m)
			r := do(t, http.MethodPost, h.url+PathMCP, b, h.mcpHeaders(userA))
			if !bytes.Contains(r.body, []byte(`"error"`)) || bytes.Contains(r.body, []byte(`"result"`)) {
				t.Fatalf("%s = %d %s", m, r.code, r.body)
			}
		}
	})
	t.Run("happy/every audit event of a request carries its id", func(t *testing.T) {
		r := h.rawCall(t, userA, ToolRunCommand, map[string]any{"server": "srv-1", "command": "true"})
		if r.code != http.StatusOK {
			t.Fatalf("status = %d", r.code)
		}
		var ids []string
		for _, l := range h.au.lines(t) {
			if l.Event == audit.EventCommandFinished {
				ids = append(ids, l.RequestID)
			}
		}
		rid := ids[len(ids)-1]
		got := h.events(t, func(l auditLine) bool { return l.RequestID == rid })
		want := []string{audit.EventAuthAccepted, audit.EventToolCalled, audit.EventCommandRequested, audit.EventCommandStarted, audit.EventCommandFinished}
		if !slices.Equal(got, want) || len(rid) != 32 {
			t.Fatalf("events of %s = %v", rid, got)
		}
	})
	t.Run("happy/the server's timeouts", func(t *testing.T) {
		s := h.gw.HTTPServer()
		if s.ReadHeaderTimeout != 10*time.Second || s.MaxHeaderBytes != 32<<10 || s.WriteTimeout <= 10*time.Second || s.ReadTimeout == 0 || s.IdleTimeout == 0 {
			t.Fatalf("server = %+v", s)
		}
	})
}

// TestBehindTheEdge: localhost protection is off because every request is
// authenticated at the origin: a public Host with a valid token works, and
// no token, a bad token or an unmapped identity is refused whatever the Host.
func TestBehindTheEdge(t *testing.T) {
	h := newHarness(t)
	e := edgetest.New(t, h.url, h.is, userA)
	t.Run("happy/a public Host with a valid token", func(t *testing.T) {
		cs, err := connect(t, e.URL, func() string { return "" }, "")
		if err != nil {
			t.Fatal(err)
		}
		res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolListServers})
		if err != nil {
			t.Fatal(err)
		}
		var ls ListServersResult
		decode(t, res, &ls)
		if len(ls.Servers) != 2 {
			t.Fatalf("servers = %+v", ls)
		}
	})
	for _, tc := range []struct {
		name string
		mode edgetest.Mode
		who  string
		code int
	}{
		{"sad/no token", edgetest.ModeNoToken, userA, http.StatusUnauthorized},
		{"sad/bad token", edgetest.ModeBadToken, userA, http.StatusUnauthorized},
		{"sad/unmapped principal", edgetest.ModeToken, "mallory@example.net", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e.SetMode(tc.mode)
			e.As(tc.who)
			defer func() { e.SetMode(edgetest.ModeToken); e.As(userA) }()
			r := do(t, http.MethodPost, e.URL+PathMCP, []byte(`{}`), h.mcpHeaders(""))
			if r.code != tc.code {
				t.Fatalf("status = %d %s", r.code, r.body)
			}
		})
	}
	t.Run("sad/a client-supplied token does not survive the edge", func(t *testing.T) {
		e.SetMode(edgetest.ModeNoToken)
		defer e.SetMode(edgetest.ModeToken)
		if r := do(t, http.MethodPost, e.URL+PathMCP, []byte(`{}`), h.mcpHeaders(userA)); r.code != http.StatusUnauthorized {
			t.Fatalf("status = %d", r.code)
		}
	})
	t.Run("sad/around the edge there is no token", func(t *testing.T) {
		hd := h.mcpHeaders("")
		hd["Host"] = edgetest.PublicHost
		if r := do(t, http.MethodPost, h.url+PathMCP, []byte(`{}`), hd); r.code != http.StatusUnauthorized {
			t.Fatalf("status = %d", r.code)
		}
	})
	t.Run("happy/a public Host straight to the gateway with a valid token", func(t *testing.T) {
		hd := h.mcpHeaders(userA)
		hd["Host"] = "evil.example.net"
		b := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_servers"}}`)
		if r := do(t, http.MethodPost, h.url+PathMCP, b, hd); r.code != http.StatusOK || !bytes.Contains(r.body, []byte("srv-1")) {
			t.Fatalf("status = %d %s", r.code, r.body)
		}
	})
}

// TestRequestCap: over the cap of concurrent /mcp requests the answer is
// 503 with Retry-After, logged, not audited.
func TestRequestCap(t *testing.T) {
	h := newHarness(t, withInFlight(1))
	h.session(userA)
	done := make(chan rawResp, 1)
	go func() {
		done <- h.rawCall(t, userA, ToolRunCommand, map[string]any{"server": "srv-1", "command": "sleep 3", "wait_seconds": 8})
	}()
	if !leakcheck.Poll(10*time.Second, func() bool { return len(h.gw.slots) == 1 }) {
		t.Fatal("the first request never took the slot")
	}
	authEvents := func() int {
		return len(h.events(t, func(l auditLine) bool { return strings.HasPrefix(l.Event, "auth.") }))
	}
	audited := authEvents()
	r := do(t, http.MethodPost, h.url+PathMCP, []byte(`{}`), h.mcpHeaders(userA))
	if r.code != http.StatusServiceUnavailable || r.header.Get("Retry-After") != "5" || string(r.body) != `{"error":"busy"}` {
		t.Fatalf("over the cap = %d %v %s", r.code, r.header, r.body)
	}
	checkHeaders(t, r)
	if got := authEvents(); got != audited {
		t.Fatalf("the refusal was audited (%d -> %d events)", audited, got)
	}
	if !strings.Contains(h.logs.String(), "request cap reached") {
		t.Fatal("no log line for the refusal")
	}
	if first := <-done; first.code != http.StatusOK {
		t.Fatalf("first = %d", first.code)
	}
}

func TestDerivedLimits(t *testing.T) {
	ts := newHarnessTenants()
	if got := MaxInFlightFor(ts); got != 8+4*4+4*4 {
		t.Fatalf("MaxInFlightFor = %d", got)
	}
	if got := WriteTimeoutFor(ts); got != 5*time.Second+transferExtra+WriteMargin {
		t.Fatalf("WriteTimeoutFor = %v", got)
	}
	ts[0].Limits.MaxWaitSeconds = 110
	if got := WriteTimeoutFor(ts); got != 110*time.Second+WriteMargin {
		t.Fatalf("WriteTimeoutFor = %v", got)
	}
}
