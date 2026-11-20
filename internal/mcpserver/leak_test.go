package mcpserver

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/accesstest"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/sshtest"
)

// capture records every response the client gets, headers and body.
type capture struct {
	base http.RoundTripper
	mu   sync.Mutex
	seen bytes.Buffer
}

func (c *capture) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := c.base.RoundTrip(r)
	if err != nil {
		return resp, err
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, vs := range resp.Header {
		c.seen.WriteString(k + ": " + strings.Join(vs, ",") + "\n")
	}
	c.seen.Write(body)
	c.seen.WriteString("\n")
	return resp, err
}

func (c *capture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen.String()
}

// TestNothingFromTheConfigLeaks (D8): the config values the model must never
// learn are unique marker strings, and none appears in any tool result,
// error, description or schema, in /healthz, in a response header or in a
// log line.
func TestNothingFromTheConfigLeaks(t *testing.T) {
	const (
		markHost     = "zq-host-marker-1.example.net"
		markUser     = "zqusermarker2"
		markKey      = "/zq-keypath-marker-3/id_ed25519"
		markKnown    = "/zq-knownhosts-marker-4/known_hosts"
		markIdentity = "zq-identity-marker-5"
	)
	var su *sshtest.Server
	h := newHarness(t, withFull("full-1"), withTenants(func(ts []tenant.Tenant) {
		su = sshtest.Start(t, sshtest.Options{User: markUser})
		u := serverFor(tA, "srv-u", su)
		ghost := serverFor(tA, "ghost", su)
		ghost.Host, ghost.Identity.PrivateKeyFile = markHost, markKey
		ghostKH := serverFor(tA, "ghost-kh", su)
		ghostKH.Host, ghostKH.HostKey, ghostKH.KnownHostsFile = markHost, "", markKnown
		ts[0].Servers = append(ts[0].Servers, u, ghost, ghostKH)
		for i := range ts {
			for j := range ts[i].Servers {
				ts[i].Servers[j].Identity.ID = markIdentity
			}
		}
	}))
	markers := []string{
		markHost, markUser, markKey, markKnown, markIdentity,
		accesstest.TeamDomain, accesstest.Audience, string(tB),
		h.sa.ClientKeyFile, h.sb.ClientKeyFile, su.ClientKeyFile, h.au.path,
		h.sa.Host + ":" + strconv.Itoa(h.sa.Port), h.sb.Host + ":" + strconv.Itoa(h.sb.Port), su.Host + ":" + strconv.Itoa(su.Port),
	}

	rec := &capture{base: http.DefaultTransport}
	hc := &http.Client{Transport: tokenTransport{base: rec, token: func() string { return h.tokenFor(userA) }}, Timeout: 60 * time.Second}
	c := mcp.NewClient(&mcp.Implementation{Name: "leak", Version: "test"}, nil)
	cs, err := c.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: h.url + PathMCP, HTTPClient: hc, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	if _, err := cs.ListTools(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	callAll := func(name string, args map[string]any) {
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		defer cancel()
		if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	callAll(ToolListServers, nil)
	for _, s := range []string{"srv-1", "srv-u", "ghost", "ghost-kh", "nope", "only-b"} {
		callAll(ToolGetServerInfo, map[string]any{"server": s})
		callAll(ToolRunCommand, map[string]any{"server": s, "command": "echo ok"})
		callAll(ToolDownloadFile, map[string]any{"server": s, "path": "/nonexistent/file"})
		callAll(ToolUploadFile, map[string]any{"server": s, "path": "/tmp/x", "content": "x"})
		callAll(ToolListCommands, map[string]any{"server": s})
	}
	callAll(ToolRunCommand, map[string]any{"server": "srv-1", "command": "sudo id"})
	callAll(ToolUploadFile, map[string]any{"server": "full-1", "path": "/nonexistent-dir/x", "content": "x"})
	var lc ListCommandsResult
	decode(t, h.call(t, userA, ToolListCommands, nil), &lc)
	for _, cmd := range lc.Commands {
		callAll(ToolReadOutput, map[string]any{"command_id": cmd.CommandID})
		callAll(ToolKillCommand, map[string]any{"command_id": cmd.CommandID})
	}
	callAll(ToolListCommands, nil)
	callAll(ToolReadOutput, map[string]any{"command_id": "bad"})

	var out strings.Builder
	out.WriteString(rec.String())
	for _, r := range []rawResp{
		do(t, http.MethodGet, h.url+PathHealthz, nil, nil),
		do(t, http.MethodGet, h.url+"/nope", nil, nil),
		do(t, http.MethodPost, h.url+PathMCP, nil, nil),
		do(t, http.MethodPost, h.url+PathMCP, []byte("{"), h.mcpHeaders(userA)),
		do(t, http.MethodPost, h.url+PathMCP, nil, h.mcpHeaders("mallory@example.net")),
	} {
		for k, vs := range r.header {
			out.WriteString(k + ": " + strings.Join(vs, ",") + "\n")
		}
		out.Write(r.body)
	}
	all := out.String() + "\n" + h.logs.String()
	if len(all) < 10000 || !strings.Contains(all, "srv-u") || !strings.Contains(all, "ssh_unreachable") && !strings.Contains(all, "internal") {
		t.Fatalf("the test saw too little (%d bytes)", len(all))
	}
	for _, m := range markers {
		if strings.Contains(all, m) {
			i := strings.Index(all, m)
			t.Errorf("marker %q leaked: ...%s...", m, all[max(i-200, 0):min(i+200, len(all))])
		}
	}
}
