package auth

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/audit"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/accesstest"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/leakcheck"
)

// The synthetic principals of the test tenant.
const (
	userEmail   = "operator@example.net"
	serviceCN   = "svc-client-0001.access"
	otherEmail  = "stranger@example.net"
	otherCN     = "svc-client-9999.access"
	tenantID    = tenant.TenantID("default")
	tenant2ID   = tenant.TenantID("second")
	user2Email  = "second@example.net"
	service2CN  = "svc-client-0002.access"
	clockOrigin = 1_900_000_000 // 2030-03-17, a fixed instant
)

// clock is an injectable, manually advanced clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Unix(clockOrigin, 0).UTC()} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func testTenants() []tenant.Tenant {
	return []tenant.Tenant{
		{ID: tenantID, Principals: []tenant.PrincipalMatch{{Email: userEmail}, {CommonName: serviceCN}}},
		{ID: tenant2ID, Principals: []tenant.PrincipalMatch{{Email: user2Email}, {CommonName: service2CN}}},
	}
}

// fixture is an issuer, a clock and an authenticator wired to them.
type fixture struct {
	is   *accesstest.Issuer
	clk  *clock
	a    *CloudflareAccess
	logs *syncBuffer
}

// syncBuffer is a goroutine-safe log sink.
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

// newFixture builds an authenticator for a fresh issuer. It does not
// start it; load() fetches once.
func newFixture(t *testing.T, opts ...Option) *fixture {
	t.Helper()
	clk := newClock()
	is := accesstest.New(t, clk.Now)
	tr := &http.Transport{}
	t.Cleanup(tr.CloseIdleConnections)
	logs := &syncBuffer{}
	all := append([]Option{
		WithClock(clk.Now),
		WithHTTPClient(&http.Client{Transport: tr}),
		WithLogger(slog.New(slog.NewJSONHandler(logs, nil))),
	}, opts...)
	a, err := NewCloudflareAccess(is.Config(), testTenants(), all...)
	if err != nil {
		t.Fatal(err)
	}
	a.keys.fetchTimeout = 2 * time.Second
	t.Cleanup(a.Close)
	return &fixture{is: is, clk: clk, a: a, logs: logs}
}

// load fetches the key set once and fails the test if that fails.
func (f *fixture) load(t *testing.T) {
	t.Helper()
	if err := f.a.keys.refresh(context.Background()); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if !f.a.Ready() {
		t.Fatal("not ready after a successful fetch")
	}
}

// request returns a request carrying tok in the token header (none when
// tok is empty).
func request(tok string) *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "http://127.0.0.1/mcp", http.NoBody)
	if tok != "" {
		r.Header.Set(Header, tok)
	}
	return r
}

func reasonOf(t *testing.T, err error) Reason {
	t.Helper()
	if err == nil {
		return Reason{}
	}
	ae, ok := err.(*Error) //nolint:errorlint // Authenticate returns *Error itself, never a wrapped one; this asserts that
	if !ok {
		t.Fatalf("error %T is not *auth.Error", err)
	}
	return ae.Reason()
}

// auditLog opens a real audit log in a private temp dir.
func auditLog(t *testing.T) (logger *audit.Logger, path string) {
	t.Helper()
	path = filepath.Join(leakcheck.PrivDir(t), "audit.jsonl")
	logger, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	return logger, path
}

// readAudit returns the audit log's events.
func readAudit(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // G304: a path in the test's temp dir
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("audit line %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

// okHandler records that it ran and what the context held.
type okHandler struct {
	mu    sync.Mutex
	calls int
	p     tenant.Principal
	id    string
}

func (h *okHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	h.p = MustPrincipal(r.Context())
	h.id, _ = RequestIDFrom(r.Context())
	w.WriteHeader(http.StatusOK)
}

func (h *okHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}
