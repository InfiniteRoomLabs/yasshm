package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/InfiniteRoomLabs/yasshm/internal/audit"
	"github.com/InfiniteRoomLabs/yasshm/internal/auth"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// headerAuth is an Authenticator for the F6 proof: the header X-F6-Token
// names the principal's subject. Only the guard reads it.
type headerAuth struct{}

func (headerAuth) Authenticate(r *http.Request) (tenant.Principal, error) {
	s := r.Header.Get("X-F6-Token")
	if s == "" {
		return tenant.Principal{}, errors.New("no token")
	}
	return tenant.Principal{Tenant: "t-f6", Kind: tenant.KindUser, Subject: s}, nil
}

type nopAudit struct{}

func (nopAudit) Write(audit.Event) error { return nil }

// headerTransport adds headers to every request of the SDK client.
type headerTransport struct {
	base http.RoundTripper
	set  map[string]string
}

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h.set {
		r.Header.Set(k, v)
	}
	return h.base.RoundTrip(r)
}

// seen is what the F6 tool observed in one call.
type seen struct {
	Closure     string `json:"closure"`      // the subject captured when the server was built
	CtxSubject  string `json:"ctx_subject"`  // auth.PrincipalFrom on the handler's context
	ExtraHeader string `json:"extra_header"` // the client-set header as req.Extra carries it
	HasToken    bool   `json:"has_token"`    // req.Extra.TokenInfo != nil
}

// TestF6IdentityReachesHandler settles spec F6: how the principal the guard
// verified reaches a tool handler in stateless mode. getServer runs per
// request and builds a server whose handler captures the principal from the
// HTTP request's context in a closure. The test also records what the
// alternatives carry: the handler's own context holds the same values (the
// SDK connects the per-request session with the HTTP request's context), and
// req.Extra.Header is whatever the client sent, with no token info (so it is
// never used for identity).
func TestF6IdentityReachesHandler(t *testing.T) {
	var mu sync.Mutex
	builds := 0
	getServer := func(r *http.Request) *mcp.Server {
		p, ok := auth.PrincipalFrom(r.Context())
		if !ok {
			return nil
		}
		mu.Lock()
		builds++
		mu.Unlock()
		s := mcp.NewServer(&mcp.Implementation{Name: "f6", Version: "test"}, nil)
		s.AddTool(&mcp.Tool{Name: "whoami", InputSchema: json.RawMessage(`{"type":"object"}`)},
			func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				var o seen
				o.Closure = p.Subject
				if cp, ok := auth.PrincipalFrom(ctx); ok {
					o.CtxSubject = cp.Subject
				}
				if req.Extra != nil {
					o.ExtraHeader = req.Extra.Header.Get("X-F6-Token")
					o.HasToken = req.Extra.TokenInfo != nil
				}
				b, err := json.Marshal(o)
				if err != nil {
					return nil, err
				}
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil
			})
		return s
	}
	sdk := mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	guard := auth.NewGuard(headerAuth{}, nopAudit{})
	srv := httptest.NewServer(guard.Wrap(auth.Require(sdk)))
	t.Cleanup(srv.Close)

	for _, version := range []string{"", "2025-11-25"} {
		t.Run("happy/protocol "+version, func(t *testing.T) {
			hc := &http.Client{Transport: headerTransport{base: http.DefaultTransport, set: map[string]string{"X-F6-Token": "alice@example.net"}}}
			c := mcp.NewClient(&mcp.Implementation{Name: "f6-client", Version: "test"}, nil)
			var opts *mcp.ClientSessionOptions
			if version != "" {
				opts = &mcp.ClientSessionOptions{ProtocolVersion: version}
			}
			cs, err := c.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: hc, DisableStandaloneSSE: true}, opts)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cs.Close() }()
			res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "whoami"})
			if err != nil {
				t.Fatal(err)
			}
			var o seen
			if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &o); err != nil {
				t.Fatal(err)
			}
			t.Logf("F6 observed (protocol %q): %+v", version, o)
			if o.Closure != "alice@example.net" {
				t.Fatalf("closure principal = %q", o.Closure)
			}
			// Observed with v1.8.0: in stateless mode the session is connected
			// with the HTTP request's context, so its values (the guard's
			// principal) also reach the handler. The gateway does not depend
			// on that; the closure is the mechanism.
			if o.CtxSubject != o.Closure {
				t.Fatalf("handler context principal = %q, closure %q", o.CtxSubject, o.Closure)
			}
			if o.ExtraHeader != "alice@example.net" || o.HasToken {
				t.Fatalf("req.Extra = header %q, token info %v; want the raw client header and no token info", o.ExtraHeader, o.HasToken)
			}
		})
	}
	t.Run("sad/no principal, no server", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
		auth.Require(sdk).ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d", rec.Code)
		}
	})
	mu.Lock()
	defer mu.Unlock()
	if builds == 0 {
		t.Fatal("getServer never built a server")
	}
}
