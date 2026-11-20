package mcpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/InfiniteRoomLabs/yasshm/internal/audit"
	"github.com/InfiniteRoomLabs/yasshm/internal/policy"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/audittest"
)

// hints are the annotations each tool must carry: read-only only where the
// tool cannot change anything.
var hints = map[string]struct{ readOnly, destructive, idempotent, openWorld bool }{
	ToolListServers:   {true, false, true, false},
	ToolGetServerInfo: {true, false, true, true},
	ToolRunCommand:    {false, true, false, true},
	ToolReadOutput:    {true, false, true, false},
	ToolListCommands:  {true, false, true, false},
	ToolKillCommand:   {false, true, true, true},
	ToolDownloadFile:  {true, false, true, true},
	ToolUploadFile:    {false, true, false, true},
}

func TestToolsList(t *testing.T) {
	h := newHarness(t)
	res, err := h.session(userA).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) != 8 {
		t.Fatalf("%d tools", len(res.Tools))
	}
	for _, tl := range res.Tools {
		t.Run("happy/"+tl.Name, func(t *testing.T) {
			want, ok := hints[tl.Name]
			if !ok {
				t.Fatalf("unexpected tool %s", tl.Name)
			}
			a := tl.Annotations
			if a == nil || a.ReadOnlyHint != want.readOnly || a.IdempotentHint != want.idempotent ||
				a.DestructiveHint == nil || *a.DestructiveHint != want.destructive || a.OpenWorldHint == nil || *a.OpenWorldHint != want.openWorld {
				t.Fatalf("annotations = %+v", a)
			}
			in, _ := json.Marshal(tl.InputSchema)
			out, _ := json.Marshal(tl.OutputSchema)
			for _, s := range [][]byte{in, out} {
				var m map[string]any
				if err := json.Unmarshal(s, &m); err != nil || m["type"] != "object" {
					t.Fatalf("schema %s", s)
				}
			}
			if !strings.Contains(string(in), `"additionalProperties":false`) {
				t.Fatalf("input schema admits unknown arguments: %s", in)
			}
			d := tl.Description
			switch tl.Name {
			case ToolListServers:
				if !strings.Contains(d, "signed-in caller") {
					t.Fatalf("description %q", d)
				}
			case ToolUploadFile:
				if !strings.Contains(d, "access level full") || !strings.Contains(d, "access_denied") {
					t.Fatalf("description %q", d)
				}
			default:
				if !strings.Contains(d, "access level look") {
					t.Fatalf("description %q does not state the level", d)
				}
			}
			if want.openWorld && tl.Name != ToolKillCommand && !strings.Contains(d, "untrusted") && tl.Name != ToolUploadFile {
				t.Fatalf("description %q does not label target output as untrusted", d)
			}
			if (tl.Name == ToolRunCommand || tl.Name == ToolDownloadFile) && !strings.Contains(d, "guard rail, not a boundary") {
				t.Fatalf("description %q has no guard-rail caveat", d)
			}
		})
	}
	t.Run("happy/policy descriptors", func(t *testing.T) {
		p := Policy()
		if len(p) != 8 || p[ToolRunCommand].Input != policy.InputCommand || p[ToolDownloadFile].Input != policy.InputPath ||
			!p[ToolUploadFile].WritesFiles || p[ToolUploadFile].Requires != tenant.AccessFull || p[ToolKillCommand].Requires != tenant.AccessLook {
			t.Fatalf("descriptors = %+v", p)
		}
	})
}

// TestPanicRecovered: a panic inside a tool is recovered, audited and
// answered as internal without its text.
func TestPanicRecovered(t *testing.T) {
	h := newHarness(t)
	d := &toolDef{name: "boom", prepare: func(context.Context, *Gateway, *call, json.RawMessage) plan {
		panic("secret panic text")
	}}
	a := audit.Actor{RequestID: "rid-panic", Principal: tenant.Principal{Tenant: tA, Kind: tenant.KindUser, Subject: userA}}
	res := h.gw.invoke(t.Context(), request{actor: a}, d, nil)
	e := wantErr(t, res, CodeInternal, "")
	if strings.Contains(e.Message, "secret") || strings.Contains(h.logs.String(), "secret panic text") {
		t.Fatal("the panic text escaped")
	}
	got := h.events(t, func(l audittest.Line) bool { return l.RequestID == "rid-panic" })
	if strings.Join(got, ",") != "tool.called,tool.failed" {
		t.Fatalf("events = %v", got)
	}
	t.Run("sad/panic in the action", func(t *testing.T) {
		d := &toolDef{name: "boom2", prepare: func(context.Context, *Gateway, *call, json.RawMessage) plan {
			return plan{act: func(context.Context) (result, error) { panic("later") }}
		}}
		wantErr(t, h.gw.invoke(t.Context(), request{actor: a}, d, nil), CodeInternal, "")
	})
	t.Run("sad/no principal is internal", func(t *testing.T) {
		wantErr(t, h.gw.invoke(t.Context(), request{actor: audit.Actor{RequestID: "r"}}, toolDefs()[0], nil), CodeInternal, "")
		wantErr(t, h.gw.invoke(t.Context(), request{actor: audit.Actor{RequestID: "r", Principal: tenant.Principal{Tenant: "ghost", Kind: tenant.KindUser, Subject: "x"}}}, toolDefs()[0], nil), CodeInternal, "")
	})
	t.Run("sad/a result over the budget is refused", func(t *testing.T) {
		d := &toolDef{name: "huge", prepare: func(context.Context, *Gateway, *call, json.RawMessage) plan {
			return plan{act: func(context.Context) (result, error) {
				return oneBlock(map[string]string{"x": strings.Repeat("y", 200000)}, "z"), nil
			}}
		}}
		e := wantErr(t, h.gw.invoke(t.Context(), request{actor: a}, d, nil), CodeInternal, "")
		if e.Message != msgTooLarge {
			t.Fatalf("message = %q", e.Message)
		}
	})
}

func TestNewRefuses(t *testing.T) {
	h := newHarness(t)
	ok := Config{Version: "v", Tenants: h.tenants, Policy: h.pe, Jobs: h.e, Audit: h.au, Guard: h.guard, Health: fakeHealth{}}
	for name, mut := range map[string]func(c *Config){
		"sad/no policy":        func(c *Config) { c.Policy = nil },
		"sad/no tenant":        func(c *Config) { c.Tenants = nil },
		"sad/duplicate tenant": func(c *Config) { c.Tenants = []tenant.Tenant{h.tenants[0], h.tenants[0]} },
		"sad/tiny result cap": func(c *Config) {
			ts := []tenant.Tenant{h.tenants[0]}
			ts[0].Limits.MaxResultBytes = MinResultBytes - 1
			c.Tenants = ts
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := ok
			mut(&c)
			if _, err := New(c); err == nil {
				t.Fatal("New accepted it")
			}
		})
	}
}

func TestFitText(t *testing.T) {
	for _, tc := range []struct {
		name   string
		in     string
		budget int
		final  bool
		text   string
		used   int
		lossy  bool
	}{
		{"happy/all fits", "abc", 100, true, "abc", 3, false},
		{"edge/budget cuts", "abcdef", 3, true, "abc", 3, false},
		{"edge/escapes cost more", "<<<", 12, true, "<<", 2, false},
		{"edge/control characters", "\x01\x02", 6, true, "\x01", 1, false},
		{"happy/invalid byte replaced", "a\xffb", 100, true, "a\ufffdb", 3, true},
		{"edge/split character held back", "a\xc3", 100, false, "a", 1, false},
		{"edge/split character at the end of the stream replaced", "a\xc3", 100, true, "a\ufffd", 2, true},
		{"edge/a lone partial character moves on", "\xc3", 100, false, "\ufffd", 1, true},
		{"edge/zero budget", "abc", 0, true, "", 0, false},
		{"edge/a valid replacement character is not lossy", "\ufffd", 100, true, "\ufffd", 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := fitText([]byte(tc.in), tc.budget, tc.final)
			if p.text != tc.text || p.used != tc.used || p.lossy != tc.lossy || !utf8.ValidString(p.text) {
				t.Fatalf("fitText = %+v", p)
			}
			b, _ := json.Marshal(p.text)
			if len(b)-2 > tc.budget {
				t.Fatalf("encoded %d bytes over budget %d", len(b)-2, tc.budget)
			}
		})
	}
}

func FuzzFitText(f *testing.F) {
	for _, s := range []string{"abc", "a\xffb", "<>&\"\\\n\t\x00", "\u2028\u00e9\xc3", strings.Repeat("\xe2\x82", 7)} {
		f.Add([]byte(s), 10, true)
	}
	f.Fuzz(func(t *testing.T, data []byte, budget int, final bool) {
		if budget < 0 || budget > 1<<16 {
			return
		}
		p := fitText(data, budget, final)
		if !utf8.ValidString(p.text) || p.used < 0 || p.used > len(data) {
			t.Fatalf("fitText = %+v", p)
		}
		b, err := json.Marshal(p.text)
		if err != nil || len(b) > jsonStringCost(p.text) || jsonStringCost(p.text)-2 > budget {
			t.Fatalf("encoded %d, cost %d, budget %d", len(b), jsonStringCost(p.text), budget)
		}
		if utf8.Valid(data[:p.used]) && !p.lossy && p.text != string(data[:p.used]) {
			t.Fatalf("valid input changed: %q -> %q", data[:p.used], p.text)
		}
	})
}

func TestDecodeArgs(t *testing.T) {
	type args struct {
		A string `json:"a"`
		B *int64 `json:"b,omitempty"`
	}
	for _, tc := range []struct {
		name, in string
		err      error
	}{
		{"happy/object", `{"a":"x","b":1}`, nil},
		{"happy/absent", ``, nil},
		{"happy/null", `null`, nil},
		{"sad/unknown key", `{"c":1}`, errArgsUnknown},
		{"sad/key in another case", `{"A":"x"}`, errArgsUnknown},
		{"raging/duplicate key", `{"a":"x","a":"y"}`, errArgsDup},
		{"raging/array", `[]`, errArgsShape},
		{"raging/trailing", `{} 1`, errArgsShape},
		{"raging/truncated", `{"a":`, errArgsShape},
		{"raging/wrong type", `{"b":"1"}`, errArgsType},
		{"raging/too large", `{"a":"` + strings.Repeat("x", 100) + `"}`, errArgsTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var a args
			err := decodeArgs(json.RawMessage(tc.in), []string{"a", "b"}, 64, &a)
			if err != tc.err { //nolint:errorlint // the fixed errors are compared by identity
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
		})
	}
}

// BenchmarkNewServer measures the per-request cost of the F6 mechanism:
// one MCP server with the eight tools, built for every /mcp request.
func BenchmarkNewServer(b *testing.B) {
	g := &Gateway{defs: toolDefs(), log: slog.New(slog.DiscardHandler), sdkLogs: slog.New(slog.DiscardHandler)}
	a := audit.Actor{RequestID: "r", Principal: tenant.Principal{Tenant: tA, Kind: tenant.KindUser, Subject: userA}}
	b.ReportAllocs()
	for b.Loop() {
		_ = g.newServer(request{actor: a})
	}
}
