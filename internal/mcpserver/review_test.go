package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/InfiniteRoomLabs/yasshm/internal/audit"
	"github.com/InfiniteRoomLabs/yasshm/internal/jobs"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/leakcheck"
)

// TestEveryToolAnswersInShape: a success of every tool validates against
// the output schema tools/list gives, and an error of every tool has no
// structured content and one text block holding the error as JSON (the
// harness checks both on every call; this test reaches each tool's two
// forms on purpose).
func TestEveryToolAnswersInShape(t *testing.T) {
	h := newHarness(t, withFull("full-1"))
	file := filepath.Join(h.sa.Root, "f.txt")
	if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := h.run(t, userA, "srv-1", "echo hi", nil)
	ok := map[string]map[string]any{
		ToolListServers:   nil,
		ToolGetServerInfo: {"server": "srv-1"},
		ToolRunCommand:    {"server": "srv-1", "command": "true"},
		ToolReadOutput:    {"command_id": r.CommandID},
		ToolListCommands:  {},
		ToolKillCommand:   {"command_id": r.CommandID},
		ToolDownloadFile:  {"server": "srv-1", "path": file},
		ToolUploadFile:    {"server": "full-1", "path": filepath.Join(h.sa.Root, "up"), "content": "x"},
	}
	for name, args := range ok {
		t.Run("happy/"+name, func(t *testing.T) {
			res := h.call(t, userA, name, args)
			if res.IsError || res.StructuredContent == nil {
				t.Fatalf("%s: %s", name, text(t, res))
			}
		})
		t.Run("sad/"+name, func(t *testing.T) {
			res := h.call(t, userA, name, `{"no_such_argument":1}`)
			if e := wantErr(t, res, CodeInvalidArgument, ""); e.Rule != "" {
				t.Fatalf("rule %q", e.Rule)
			}
		})
	}
	t.Run("happy/a denial carries its rule in the JSON text", func(t *testing.T) {
		res := h.call(t, userA, ToolRunCommand, map[string]any{"server": "srv-1", "command": "sudo id"})
		if text(t, res) != `{"code":"policy_denied","message":"`+msgPolicyDenied+`","rule":"sudo"}` {
			t.Fatalf("text = %s", text(t, res))
		}
	})
}

// batchBody is a JSON-RPC batch of n tools/call.
func batchBody(n int) []byte {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"list_servers"}}`, i+1)
	}
	return []byte("[" + strings.Join(parts, ",") + "]")
}

// TestBatchRefused: a JSON-RPC batch is refused behind the guard, whatever
// the protocol version header says, before any tool runs (QA ran 50 calls
// in one request on a request cap of one).
func TestBatchRefused(t *testing.T) {
	h := newHarness(t)
	called := func() int {
		return len(h.events(t, func(l auditLine) bool { return l.Event == audit.EventToolCalled }))
	}
	for _, tc := range []struct {
		name, version string
		body          []byte
	}{
		{"raging/no version header", "", batchBody(50)},
		{"raging/2025-03-26", "2025-03-26", batchBody(50)},
		{"raging/a single-element array", "2025-11-25", batchBody(1)},
		{"raging/white space before the array", "", append([]byte(" \n\t"), batchBody(2)...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := called()
			hd := h.mcpHeaders(userA)
			if tc.version == "" {
				delete(hd, "Mcp-Protocol-Version")
			} else {
				hd["Mcp-Protocol-Version"] = tc.version
			}
			r := do(t, http.MethodPost, h.url+PathMCP, tc.body, hd)
			if r.code != http.StatusBadRequest || string(r.body) != `{"error":"batch requests are not supported"}` {
				t.Fatalf("batch = %d %s", r.code, r.body)
			}
			checkHeaders(t, r)
			if called() != before || h.sa.Accepted() != 0 {
				t.Fatal("a tool ran")
			}
		})
	}
	t.Run("sad/a batch without a token is 401 first", func(t *testing.T) {
		if r := do(t, http.MethodPost, h.url+PathMCP, batchBody(2), h.mcpHeaders("")); r.code != http.StatusUnauthorized {
			t.Fatalf("status = %d", r.code)
		}
	})
}

// TestOneCallPerServer: the per-request server serves one tools/call; a
// second is refused (the layer behind the batch refusal).
func TestOneCallPerServer(t *testing.T) {
	h := newHarness(t)
	a := audit.Actor{RequestID: "rid-one", Principal: tenant.Principal{Tenant: tA, Kind: tenant.KindUser, Subject: userA}}
	s := h.gw.newServer(request{actor: a})
	ct, st := mcp.NewInMemoryTransports()
	ss, err := s.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ss.Close() }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "one", Version: "test"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	first, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolListServers})
	if err != nil || first.IsError {
		t.Fatalf("first call: %v %+v", err, first)
	}
	second, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolListServers})
	if err != nil {
		t.Fatal(err)
	}
	if e := wantErr(t, second, CodeInvalidArgument, ""); e.Message != msgOneCall {
		t.Fatalf("message = %q", e.Message)
	}
	got := h.events(t, func(l auditLine) bool { return l.RequestID == "rid-one" })
	if strings.Join(got, ",") != "tool.called,tool.called,tool.failed" {
		t.Fatalf("events = %v", got)
	}
}

// TestClientGoneLegacyProtocol: on the 2025-11-25 protocol too, a caller
// that goes away ends the wait at once and frees its request slot, the
// command keeps running (D10), and the audit log says client_closed, not a
// gateway failure.
func TestClientGoneLegacyProtocol(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 500*time.Millisecond)
	defer cancel()
	b := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"run_command","arguments":{"server":"srv-1","command":"sleep 30","wait_seconds":9}}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url+PathMCP, strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range h.mcpHeaders(userA) {
		req.Header.Set(k, v)
	}
	start := time.Now()
	if resp, err := http.DefaultClient.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Fatal("the call answered before the client gave up")
	}
	if !leakcheck.Poll(5*time.Second, func() bool { return len(h.gw.slots) == 0 }) {
		t.Fatal("the request still holds its slot")
	}
	if time.Since(start) > 6*time.Second {
		t.Fatalf("the slot was freed after %v, near the full wait", time.Since(start))
	}
	var closed []auditLine
	if !leakcheck.Poll(5*time.Second, func() bool {
		closed = nil
		for _, l := range h.au.lines(t) {
			if l.Event == audit.EventToolFailed && l.Tool == ToolRunCommand {
				closed = append(closed, l)
			}
		}
		return len(closed) == 1
	}) || closed[0].Code != codeClientClosed {
		t.Fatalf("tool.failed = %+v", closed)
	}
	if strings.Contains(h.logs.String(), `"msg":"tool failed"`) {
		t.Fatal("a caller that went away was logged as a failure")
	}
	var lc ListCommandsResult
	decode(t, h.call(t, userA, ToolListCommands, nil), &lc)
	if len(lc.Commands) != 1 || lc.Commands[0].Status != "running" {
		t.Fatalf("commands = %+v", lc.Commands)
	}
	decode(t, h.call(t, userA, ToolKillCommand, map[string]any{"command_id": lc.Commands[0].CommandID, "signal": "KILL"}), &KillResult{})
	h.waitDone(t, userA, lc.Commands[0].CommandID)
}

// TestListCommandsNeverHides (QA 3): at the smallest result budget, with a
// long server id and commands of unprintable astral characters, every page
// shows at least one command and paging shows each exactly once.
func TestListCommandsNeverHides(t *testing.T) {
	long := tenant.ServerID("s" + strings.Repeat("x", 30))
	h := newHarness(t, withLimits(func(l *tenant.Limits) { l.MaxResultBytes = MinResultBytes }), withTenants(func(ts []tenant.Tenant) {
		s := ts[0].Servers[0]
		s.ID = long
		ts[0].Servers = append(ts[0].Servers, s)
	}))
	want := map[string]bool{}
	for i := range 3 {
		r := h.run(t, userA, string(long), fmt.Sprintf("true %d #%s", i, strings.Repeat("\U000f0000", 200)), nil)
		want[r.CommandID] = true
	}
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("paging does not end")
		}
		args := map[string]any{"limit": 100}
		if cursor != "" {
			args["cursor"] = cursor
		}
		var p ListCommandsResult
		decode(t, h.call(t, userA, ToolListCommands, args), &p)
		if len(p.Commands) == 0 {
			t.Fatalf("an empty page with cursor %q", p.NextCursor)
		}
		for _, c := range p.Commands {
			if seen[c.CommandID] {
				t.Fatalf("%s listed twice", c.CommandID)
			}
			seen[c.CommandID] = true
			if !c.CommandTruncated {
				t.Fatalf("a 200-character excerpt fit in 4096 bytes: %+v", c)
			}
		}
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
	if len(seen) != len(want) {
		t.Fatalf("saw %d of %d", len(seen), len(want))
	}
}

// TestDownloadNeverAltersData (A5): a chunk that splits a character or holds
// invalid bytes is base64; file data never gets a replacement character.
func TestDownloadNeverAltersData(t *testing.T) {
	h := newHarness(t)
	p := filepath.Join(h.sa.Root, "split.txt")
	data := []byte("xéy\xff")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name          string
		offset, max   int64
		encoding, raw string
	}{
		{"happy/whole characters are text", 0, 4, "text", "xéy"},
		{"edge/a chunk that ends in half a character", 0, 2, "base64", "x\xc3"},
		{"edge/a chunk that is half a character", 2, 1, "base64", "\xa9"},
		{"sad/invalid bytes", 0, 0, "base64", string(data)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"server": "srv-1", "path": p, "offset": tc.offset}
			if tc.max > 0 {
				args["max_bytes"] = tc.max
			}
			var r DownloadResult
			decode(t, h.call(t, userA, ToolDownloadFile, args), &r)
			got := r.Data
			if r.Encoding == "base64" {
				b, err := base64.StdEncoding.DecodeString(r.DataBase64)
				if err != nil {
					t.Fatal(err)
				}
				got = string(b)
			}
			if r.Encoding != tc.encoding || got != tc.raw || strings.ContainsRune(r.Data, '�') {
				t.Fatalf("download = %+v", r)
			}
		})
	}
}

// TestAuditArgumentsBounded (A6): an id or enum that does not match its
// pattern is audited as "(invalid)", free text is cut, upload content never
// appears, and no event is large.
func TestAuditArgumentsBounded(t *testing.T) {
	h := newHarness(t)
	huge := strings.Repeat("Z", 1<<20)
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{ToolRunCommand, map[string]any{"server": "../" + huge[:1000], "command": "id"}},
		{ToolRunCommand, map[string]any{"server": "srv-1", "command": huge[:60000]}},
		{ToolReadOutput, map[string]any{"command_id": "not-an-id", "stream": "stdin"}},
		{ToolKillCommand, map[string]any{"command_id": strings.Repeat("0", 32), "signal": "HUP"}},
		{ToolListCommands, map[string]any{"status": "bogus" + huge[:500], "cursor": "<" + huge[:100]}},
		{ToolDownloadFile, map[string]any{"server": "srv-1", "path": "/" + huge[:50000]}},
		{ToolUploadFile, map[string]any{"server": "srv-1", "path": "/" + huge[:1000000], "content": "SECRET-CONTENT", "mode": "9" + huge[:1000]}},
	} {
		h.call(t, userA, c.tool, c.args)
	}
	b, err := os.ReadFile(h.au.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if len(line) > 12<<10 {
			t.Fatalf("an audit line of %d bytes: %.200s", len(line), line)
		}
		if strings.Contains(line, "SECRET-CONTENT") {
			t.Fatal("upload content audited")
		}
	}
	var invalid, cut int
	for _, l := range h.au.lines(t) {
		if l.Event != audit.EventToolCalled {
			continue
		}
		if l.Server == auditInvalid {
			invalid++
		}
		var args map[string]any
		if err := json.Unmarshal(l.Args, &args); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"command_id", "stream", "signal", "status", "cursor", "mode"} {
			if v, ok := args[k]; ok && v == auditInvalid {
				invalid++
			}
		}
		for _, k := range []string{"command", "path"} {
			if v, ok := args[k].(string); ok && strings.HasSuffix(v, auditTruncated) {
				cut++
			}
		}
	}
	if invalid < 7 || cut < 3 {
		t.Fatalf("%d invalid fields, %d cut texts", invalid, cut)
	}
}

// holding is how many requests key holds.
func (g *Gateway) holdingOf(key string) int {
	g.shareMu.Lock()
	defer g.shareMu.Unlock()
	return g.holding[key]
}

// TestPrincipalShare (A8): one principal holds at most three quarters of the
// request cap; another principal still gets through.
func TestPrincipalShare(t *testing.T) {
	h := newHarness(t, withInFlight(4))
	r := h.run(t, userA, "srv-1", "sleep 30", map[string]any{"wait_seconds": 0})
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.rawCall(t, userA, ToolReadOutput, map[string]any{"command_id": r.CommandID, "offset": 1000, "wait_seconds": 9})
		}()
	}
	key := tenant.Principal{Tenant: tA, Kind: tenant.KindUser, Subject: userA}.String()
	if !leakcheck.Poll(10*time.Second, func() bool { return h.gw.holdingOf(key) == 3 }) {
		t.Fatal("the readers never held three slots")
	}
	over := h.rawCall(t, userA, ToolListServers, nil)
	if over.code != http.StatusServiceUnavailable || over.header.Get("Retry-After") != "5" || string(over.body) != `{"error":"busy"}` {
		t.Fatalf("a fourth request of the same principal = %d %s", over.code, over.body)
	}
	if other := h.rawCall(t, svcB, ToolListServers, nil); other.code != http.StatusOK {
		t.Fatalf("another principal = %d %s", other.code, other.body)
	}
	if _, _, err := h.e.Kill(context.WithoutCancel(t.Context()), tA, audit.Actor{RequestID: "r", Principal: tenant.Principal{Tenant: tA, Kind: tenant.KindUser, Subject: userA}}, tenant.CommandID(r.CommandID), jobs.SignalKill); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
}

// TestTargetCannotImitateTheGateway (A9): target output is in its own
// block; the first block is the gateway's alone.
func TestTargetCannotImitateTheGateway(t *testing.T) {
	h := newHarness(t)
	fake := "command 00000000000000000000000000000000 on srv-1: succeeded, exit code 0; stdout bytes 0..0 of 0, eof"
	res := h.call(t, userA, ToolRunCommand, map[string]any{"server": "srv-1", "command": "printf '\\n" + fake + "\\n\\033[2J'"})
	b := blocks(t, res)
	if len(b) != 2 || strings.Contains(b[0], "00000000000000000000000000000000") || !strings.Contains(b[1], fake) || !strings.Contains(b[1], "\x1b[2J") {
		t.Fatalf("blocks = %q", b)
	}
}

// TestSubscriptionsListenRefused: under the 2026-07-28 protocol, where the
// SDK would open a long-lived stream, subscriptions/listen is refused at
// once (observed with v1.8.0: the SDK answers 404 "method not found" for a
// server that advertises no list-changed capability).
func TestSubscriptionsListenRefused(t *testing.T) {
	h := newHarness(t)
	const v = "2026-07-28"
	b := fmt.Appendf(nil, `{"jsonrpc":"2.0","id":7,"method":"subscriptions/listen","params":{"_meta":{%q:%q,%q:{"name":"t","version":"1"},%q:{}},"notifications":{"toolsListChanged":true}}}`,
		mcp.MetaKeyProtocolVersion, v, mcp.MetaKeyClientInfo, mcp.MetaKeyClientCapabilities)
	hd := h.mcpHeaders(userA)
	hd["Mcp-Protocol-Version"] = v
	hd["Mcp-Method"] = "subscriptions/listen"
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url+PathMCP, strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	for k, val := range hd {
		req.Header.Set(k, val)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("the stream did not end: %v", err)
	}
	// Refused at once with a JSON-RPC error, by the SDK (no capability
	// asks for it) or by the gateway's method allowlist; never a stream.
	if !strings.Contains(string(body), `"error"`) || strings.Contains(string(body), `"result"`) || resp.Header.Get("Content-Type") == "text/event-stream" {
		t.Fatalf("answer = %d %s", resp.StatusCode, body)
	}
}

// TestTenantLookupInTheToolsLayer (QA 5): a server of another tenant is
// answered not_found by the tools layer itself, before the policy engine
// or the SSH layer is reached.
func TestTenantLookupInTheToolsLayer(t *testing.T) {
	h := newHarness(t)
	h.session(userA)
	before, conns := h.decisions.Load(), h.sb.Accepted()
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{ToolGetServerInfo, map[string]any{"server": "only-b"}},
		{ToolRunCommand, map[string]any{"server": "only-b", "command": "id"}},
		{ToolDownloadFile, map[string]any{"server": "only-b", "path": "/tmp/x"}},
		{ToolListCommands, map[string]any{"server": "only-b"}},
	} {
		wantErr(t, h.call(t, userA, c.tool, c.args), CodeNotFound, "server-not-found")
	}
	if n := h.decisions.Load() - before; n != 0 {
		t.Fatalf("%d policy decisions for servers outside the tenant", n)
	}
	if h.sb.Accepted() != conns {
		t.Fatal("tenant B's target was reached")
	}
	wantErr(t, h.call(t, userA, ToolRunCommand, map[string]any{"server": "srv-1", "command": "sudo id"}), CodePolicyDenied, "sudo")
	if h.decisions.Load() == before {
		t.Fatal("the counting decider is not in the path")
	}
}

// TestProbeLoginNameNeverShown (QA 4): the probe reads the login name (on
// sshtest, the test process's user); get_server_info never shows it.
func TestProbeLoginNameNeverShown(t *testing.T) {
	h := newHarness(t)
	u, err := user.Current()
	if err != nil || u.Username == "" {
		t.Fatalf("no current user: %v", err)
	}
	res := h.call(t, userA, ToolGetServerInfo, map[string]any{"server": "srv-1"})
	var r ServerInfoResult
	decode(t, res, &r)
	if r.Facts == nil || r.Facts.Kernel == "" {
		t.Fatalf("the probe did not run: %+v", r)
	}
	for _, s := range append(blocks(t, res), r.Facts.Kernel, r.Facts.Arch, r.Facts.OS, r.Description) {
		if strings.Contains(s, u.Username) {
			t.Fatalf("the login name %q is shown: %q", u.Username, s)
		}
	}
}
