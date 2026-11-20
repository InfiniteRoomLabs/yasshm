package e2e

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/edgetest"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/loopback"
)

// initializeBody is a raw MCP initialize request.
const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"yasshm-e2e","version":"test"}}}`

// mcpHeader is what a Streamable HTTP client sends with a POST.
func mcpHeader() http.Header {
	return http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json, text/event-stream"}}
}

// TestE2EAuth proves the first guarantee of spec 6 on the binary: no
// request reaches a tool without a verified principal. Through the edge, no
// token and a bad token are 401 and an unmapped principal is 403, each
// with its fixed body and an auth.rejected event, and none of them reaches
// a tool (no tool.called) or the target.
func TestE2EAuth(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	g := f.start(t)
	g.waitReady(t)
	e := g.edge(t)
	srv := f.target("lab-1")
	reqs, conns := sshRequests(srv)

	for _, tc := range []struct {
		name   string
		mode   edgetest.Mode
		who    string
		status int
		body   string
		reason string
	}{
		{"sad/no token", edgetest.ModeNoToken, operator, http.StatusUnauthorized, `{"error":"unauthorized"}`, "missing_token"},
		{"sad/bad token", edgetest.ModeBadToken, operator, http.StatusUnauthorized, `{"error":"unauthorized"}`, "bad_signature"},
		{"sad/unmapped principal", edgetest.ModeToken, stranger, http.StatusForbidden, `{"error":"forbidden"}`, "unmapped_principal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e.SetMode(tc.mode)
			e.As(tc.who)
			defer func() { e.SetMode(edgetest.ModeToken); e.As(operator) }()
			before := len(f.events(t, func(l auditLine) bool { return l.Event == "auth.rejected" && l.Reason == tc.reason }))
			status, body := httpDo(t, http.MethodPost, e.URL+"/mcp", mcpHeader(), []byte(initializeBody))
			if status != tc.status || strings.TrimSpace(body) != tc.body {
				t.Fatalf("got %d %q, want %d %q", status, body, tc.status, tc.body)
			}
			after := len(f.events(t, func(l auditLine) bool { return l.Event == "auth.rejected" && l.Reason == tc.reason }))
			if after != before+1 {
				t.Fatalf("auth.rejected (%s) events: %d before, %d after", tc.reason, before, after)
			}
			// The MCP client cannot even connect.
			if _, err := dial(t, e); err == nil {
				t.Fatal("an MCP client connected")
			}
		})
	}
	t.Run("happy/a mapped principal is accepted", func(t *testing.T) {
		c := connect(t, e)
		var r listServersResult
		c.ok(t, "list_servers", map[string]any{}, &r)
		if len(r.Servers) != 1 {
			t.Fatalf("servers = %+v", r)
		}
	})
	t.Run("happy/no rejected request reached a tool or the target", func(t *testing.T) {
		lines, _ := f.auditLog(t)
		for _, l := range lines {
			if l.Event == "tool.called" && l.Tool != "list_servers" {
				t.Fatalf("tool.called %+v", l)
			}
			if l.Event == "auth.rejected" && l.Tenant != "" {
				t.Fatalf("a rejection carries a tenant: %+v", l)
			}
		}
		if r, c := sshRequests(srv); r != reqs || c != conns {
			t.Fatalf("the target saw %d requests and %d connections", r-reqs, c-conns)
		}
	})
}

// TestE2EHTTPSurface checks the HTTP surface of spec 5.8 on the binary: a
// request that goes around the edge (straight to the gateway, no token) is
// 401, a JSON-RPC batch is refused with 400 even with a valid token, and an
// unknown path is 404.
func TestE2EHTTPSurface(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	g := f.start(t)
	g.waitReady(t)
	e := g.edge(t)

	t.Run("sad/a request around the edge is 401", func(t *testing.T) {
		status, body := httpDo(t, http.MethodPost, g.URL+"/mcp", mcpHeader(), []byte(initializeBody))
		if status != http.StatusUnauthorized || strings.TrimSpace(body) != `{"error":"unauthorized"}` {
			t.Fatalf("got %d %q", status, body)
		}
	})
	t.Run("raging/a forged token around the edge is 401", func(t *testing.T) {
		h := mcpHeader()
		h.Set("Cf-Access-Jwt-Assertion", "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0.")
		status, _ := httpDo(t, http.MethodPost, g.URL+"/mcp", h, []byte(initializeBody))
		if status != http.StatusUnauthorized {
			t.Fatalf("got %d", status)
		}
	})
	t.Run("raging/a JSON-RPC batch is refused", func(t *testing.T) {
		status, body := httpDo(t, http.MethodPost, e.URL+"/mcp", mcpHeader(), []byte("  ["+initializeBody+","+initializeBody+"]"))
		if status != http.StatusBadRequest || strings.TrimSpace(body) != `{"error":"batch requests are not supported"}` {
			t.Fatalf("got %d %q", status, body)
		}
	})
	t.Run("sad/an unknown path is 404", func(t *testing.T) {
		status, body := httpDo(t, http.MethodGet, e.URL+"/admin", nil, nil)
		if status != http.StatusNotFound || strings.TrimSpace(body) != `{"error":"not found"}` {
			t.Fatalf("got %d %q", status, body)
		}
	})
	t.Run("sad/GET on the MCP path is 405 in stateless mode", func(t *testing.T) {
		if status, _ := httpDo(t, http.MethodGet, e.URL+"/mcp", mcpHeader(), nil); status != http.StatusMethodNotAllowed {
			t.Fatalf("got %d", status)
		}
	})
}

// TestE2EHealthz checks /healthz on the binary: unauthenticated, 200 with
// the status and the stamped version once the key set is loaded, and 503
// degraded when the issuer cannot be reached at start (its key set URL is a
// loopback port that refuses connections).
func TestE2EHealthz(t *testing.T) {
	t.Parallel()
	t.Run("happy/ok with the version", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		g := f.start(t)
		g.waitReady(t)
		status, body := g.healthz(t)
		if status != http.StatusOK || strings.TrimSpace(body) != `{"status":"ok","version":"`+buildVersion+`"}` {
			t.Fatalf("got %d %q", status, body)
		}
		if code := g.stop(); code != 0 {
			t.Fatalf("exit %d", code)
		}
	})
	t.Run("sad/degraded when the issuer is down at start", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, withCertsURL("http://127.0.0.1:"+strconv.Itoa(loopback.RefusingPort(t))+"/cdn-cgi/access/certs"))
		g := f.start(t)
		status, body := g.healthz(t)
		if status != http.StatusServiceUnavailable || strings.TrimSpace(body) != `{"status":"degraded","detail":"key set not loaded","version":"`+buildVersion+`"}` {
			t.Fatalf("got %d %q", status, body)
		}
		// A request through the edge is refused while no key is loaded.
		e := g.edge(t)
		if status, _ := httpDo(t, http.MethodPost, e.URL+"/mcp", mcpHeader(), []byte(initializeBody)); status != http.StatusServiceUnavailable {
			t.Fatalf("an MCP request with no key set: %d", status)
		}
		if code := g.stop(); code != 0 {
			t.Fatalf("exit %d", code)
		}
	})
}
