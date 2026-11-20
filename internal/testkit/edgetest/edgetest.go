// Package edgetest is a stand-in for the identity-aware edge (spec section
// 7): a reverse proxy on a loopback port that forwards every request to the
// gateway with the edge's token header, the way Cloudflare Access does in
// front of a tunnel. It strips any copy of the header the client sent (as
// the real edge does), sets a public-looking Host, and adds a token minted
// by an accesstest issuer for the identity it is told to act as. It can
// also be told to send no token, a bad token or another identity's token. A
// request that goes around it, straight to the gateway, has no token and
// gets 401. It is test support and is imported only by tests.
package edgetest

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/auth"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/accesstest"
)

// PublicHost is the Host header the edge sends, a name no test resolves.
const PublicHost = "yasshm.example.net"

// Mode is what the edge puts in the token header.
type Mode int

// The modes. ModeToken is the default.
const (
	// ModeToken sends a valid token for the current identity.
	ModeToken Mode = iota
	// ModeNoToken sends no token.
	ModeNoToken
	// ModeBadToken sends the current identity's token with its signature
	// tampered.
	ModeBadToken
)

// Edge is a running stand-in edge. Its methods are safe for concurrent use.
type Edge struct {
	// URL is the edge's base URL (http://127.0.0.1:<port>).
	URL string

	is   *accesstest.Issuer
	mu   sync.Mutex
	who  string
	mode Mode
	srv  *http.Server
	done chan struct{}
}

// New starts an edge on a random loopback port that forwards to origin (a
// base URL) as who: an address with "@" is a user, anything else a service
// token's client id. It is closed when the test ends.
func New(t testing.TB, origin string, is *accesstest.Issuer, who string) *Edge {
	t.Helper()
	target, err := url.Parse(origin)
	if err != nil {
		t.Fatal(err)
	}
	e := &Edge{is: is, who: who, done: make(chan struct{})}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = PublicHost
			// The edge owns this header: whatever the client sent is gone.
			r.Out.Header.Del(auth.Header)
			if tok := e.token(); tok != "" {
				r.Out.Header.Set(auth.Header, tok)
			}
		},
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	e.URL = "http://" + ln.Addr().String()
	e.srv = &http.Server{Handler: proxy, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		defer close(e.done)
		_ = e.srv.Serve(ln)
	}()
	t.Cleanup(e.Close)
	return e
}

// As makes the edge act for who from now on.
func (e *Edge) As(who string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.who = who
}

// SetMode sets what the edge sends.
func (e *Edge) SetMode(m Mode) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.mode = m
}

// token mints the header value for the current mode and identity.
func (e *Edge) token() string {
	e.mu.Lock()
	who, mode := e.who, e.mode
	e.mu.Unlock()
	if mode == ModeNoToken {
		return ""
	}
	tok := e.is.For(who).Sign()
	if mode == ModeBadToken {
		tok = accesstest.TamperSignature(tok)
	}
	return tok
}

// Close stops the edge and waits for it.
func (e *Edge) Close() {
	_ = e.srv.Close()
	<-e.done
}
