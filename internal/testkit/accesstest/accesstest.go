// Package accesstest is a local stand-in for the Cloudflare Access issuer
// (spec section 7): an HTTP server on a loopback port that serves a JWKS at
// the vendor's path (/cdn-cgi/access/certs), counts the fetches, and can be
// switched into the faults the authenticator must survive. A Builder mints
// every token class the authenticator's tests need, including the hostile
// ones (alg none, HS256 signed with the public key, tampered segments,
// ES256, PS256, RS384).
//
// Every token is built by hand here with encoding/base64, encoding/json and
// the standard crypto packages: minting is test code, and building it by hand
// is the only way to produce the malformed classes. Nothing here verifies a
// token. The team domain, audience and keys are synthetic; the keys are
// generated once per test binary.
//
// It is imported only by tests.
package accesstest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/config"
)

// The synthetic identifiers every issuer uses.
const (
	// TeamDomain is the synthetic team host name; the tokens' iss is
	// "https://" + TeamDomain.
	TeamDomain = "example-team.cloudflareaccess.com"
	// Audience is a synthetic AUD tag made of characters no real tag has.
	Audience = "aud-tag-for-tests-only-zzzz-zzzz-zzzz"
	// CertsPath is the vendor's JWKS path.
	CertsPath = "/cdn-cgi/access/certs"
)

// Fault is a misbehaviour of the issuer's JWKS endpoint.
type Fault int

// The faults. FaultNone serves the key set normally.
const (
	// FaultNone serves the key set as application/json.
	FaultNone Fault = iota
	// FaultSlow holds the response until the request is cancelled or the
	// issuer closes, past any client timeout.
	FaultSlow
	// FaultGarbage answers 200 application/json with a body that is not JSON.
	FaultGarbage
	// FaultHuge answers 200 application/json with a body of more than 2 MiB.
	FaultHuge
	// FaultRedirectOtherHost answers 302 to the same path on another host
	// name of this issuer (localhost instead of 127.0.0.1), which serves
	// the key set.
	FaultRedirectOtherHost
	// FaultRedirectNonLoopback answers 302 to an https URL on example.net,
	// a host no test may reach.
	FaultRedirectNonLoopback
	// FaultRedirectSameHost answers 302 to another path on the same host,
	// which serves the key set.
	FaultRedirectSameHost
	// FaultWrongContentType serves the key set as text/html.
	FaultWrongContentType
	// FaultEmptyKeySet serves {"keys":[]}.
	FaultEmptyKeySet
	// FaultNoUsableKey serves a set whose only key is an EC key.
	FaultNoUsableKey
	// FaultStatus500 answers 500 with the key set as its body.
	FaultStatus500
)

// Keys generated once per test binary: generating RSA keys is slow under
// -race, and no test needs a fresh one.
var (
	keyOnce [4]sync.Once
	keys    [4]*rsa.PrivateKey
	ecOnce  sync.Once
	ecKey   *ecdsa.PrivateKey
)

// RSAKey returns the i-th (0 to 3) RSA test key, generating it on first use.
// Keys 0 and 1 are the issuer's (initial and rotated); keys 2 and 3 are
// never served and stand in for an attacker's.
func RSAKey(i int) *rsa.PrivateKey {
	keyOnce[i].Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		keys[i] = k
	})
	return keys[i]
}

// ECKey returns the P-256 test key, generating it on first use.
func ECKey() *ecdsa.PrivateKey {
	ecOnce.Do(func() {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			panic(err)
		}
		ecKey = k
	})
	return ecKey
}

// Key is a signing key the issuer may serve.
type Key struct {
	// ID is the key's kid.
	ID string
	// Private is the RSA private key.
	Private *rsa.PrivateKey
}

// Issuer is the local issuer. Its methods are safe for concurrent use.
type Issuer struct {
	addr    string
	mu      sync.Mutex
	srv     *http.Server
	served  []Key
	signing Key
	fault   Fault
	hold    chan struct{}
	gate    chan struct{}
	down    bool
	closed  bool
	fetches atomic.Int64
	now     func() time.Time
}

// New starts an issuer on a random loopback port serving key 0 as kid
// "key-0", and closes it when the test ends. now is the clock tokens are
// stamped with; nil means time.Now.
func New(t testing.TB, now func() time.Time) *Issuer {
	t.Helper()
	if now == nil {
		now = time.Now
	}
	k := Key{ID: "key-0", Private: RSAKey(0)}
	is := &Issuer{served: []Key{k}, signing: k, now: now, hold: make(chan struct{})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	is.addr = ln.Addr().String()
	srv := &http.Server{Handler: http.HandlerFunc(is.handle), ReadHeaderTimeout: 5 * time.Second}
	is.srv = srv
	go func() { _ = srv.Serve(&downListener{Listener: ln, is: is}) }()
	t.Cleanup(is.Close)
	return is
}

// downListener keeps the issuer's address bound for its whole life: while
// the issuer is down it accepts each connection and resets it at once, the
// way a dead endpoint answers, so no other server can be given the port a
// test still names.
type downListener struct {
	net.Listener
	is *Issuer
}

// Accept returns the next connection, resetting those that arrive while the
// issuer is down.
func (l *downListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if !l.is.isDown() {
			return c, nil
		}
		reset(c)
	}
}

// reset closes c with an RST rather than a FIN.
func reset(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetLinger(0)
	}
	_ = c.Close()
}

func (is *Issuer) isDown() bool {
	is.mu.Lock()
	defer is.mu.Unlock()
	return is.down
}

// Close stops the issuer and releases every held response.
func (is *Issuer) Close() {
	is.mu.Lock()
	defer is.mu.Unlock()
	if is.closed {
		return
	}
	is.closed = true
	close(is.hold)
	if is.srv != nil {
		_ = is.srv.Close()
		is.srv = nil
	}
}

// Down makes the issuer a dead endpoint until Up: every connection,
// new or kept alive, is reset before a request is read, and nothing is
// counted as a fetch. The address stays bound, so no other server can take
// the port while a test still names it.
func (is *Issuer) Down() {
	is.mu.Lock()
	is.down = true
	srv := is.srv
	is.mu.Unlock()
	if srv != nil {
		srv.SetKeepAlivesEnabled(false) // closes the idle kept-alive connections
	}
}

// Up serves again on the same address after Down.
func (is *Issuer) Up() {
	is.mu.Lock()
	is.down = false
	srv := is.srv
	is.mu.Unlock()
	if srv != nil {
		srv.SetKeepAlivesEnabled(true)
	}
}

// SetFault switches the JWKS endpoint's behaviour.
func (is *Issuer) SetFault(f Fault) {
	is.mu.Lock()
	defer is.mu.Unlock()
	is.fault = f
}

// Fetches returns how many requests reached the JWKS endpoint (any path of
// this issuer, redirects included).
func (is *Issuer) Fetches() int64 { return is.fetches.Load() }

// CertsURL returns the JWKS URL, http on the loopback address.
func (is *Issuer) CertsURL() string { return "http://" + is.addr + CertsPath }

// Config returns the auth config that points at this issuer.
func (is *Issuer) Config() config.CloudflareAccess {
	return config.CloudflareAccess{TeamDomain: TeamDomain, Audience: Audience, CertsURL: is.CertsURL()}
}

// Rotate adds key 1 as kid "key-1" to the served set and makes it the
// signing key; the old key stays served until DropOld.
func (is *Issuer) Rotate() Key {
	is.mu.Lock()
	defer is.mu.Unlock()
	k := Key{ID: "key-1", Private: RSAKey(1)}
	is.served = append(is.served, k)
	is.signing = k
	return k
}

// DropOld serves only the current signing key.
func (is *Issuer) DropOld() {
	is.mu.Lock()
	defer is.mu.Unlock()
	is.served = []Key{is.signing}
}

// Hold makes every request that arrives from now on wait (after it is
// counted) until the returned release function is called, the request is
// cancelled or the issuer closes. It lets a test keep one fetch in flight.
func (is *Issuer) Hold() (release func()) {
	is.mu.Lock()
	defer is.mu.Unlock()
	g := make(chan struct{})
	is.gate = g
	var once sync.Once
	return func() {
		once.Do(func() {
			is.mu.Lock()
			if is.gate == g {
				is.gate = nil
			}
			is.mu.Unlock()
			close(g)
		})
	}
}

// SigningKey returns the key new tokens are signed with.
func (is *Issuer) SigningKey() Key {
	is.mu.Lock()
	defer is.mu.Unlock()
	return is.signing
}

// ServedKeys returns the keys the endpoint serves now.
func (is *Issuer) ServedKeys() []Key {
	is.mu.Lock()
	defer is.mu.Unlock()
	return append([]Key(nil), is.served...)
}

func (is *Issuer) handle(w http.ResponseWriter, r *http.Request) {
	if is.isDown() {
		// A request read on a connection accepted just before Down.
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				reset(c)
			}
		}
		return
	}
	is.fetches.Add(1)
	is.mu.Lock()
	fault, served, hold, gate := is.fault, append([]Key(nil), is.served...), is.hold, is.gate
	is.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		case <-hold:
			return
		}
	}
	if r.URL.Query().Get("redirected") == "1" {
		fault = FaultNone
	}
	body := JWKS(served)
	switch fault {
	case FaultSlow:
		select {
		case <-r.Context().Done():
		case <-hold:
		}
		return
	case FaultGarbage:
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("<html>not a key set"))
		return
	case FaultHuge:
		w.Header().Set("Content-Type", "application/json")
		chunk := []byte(strings.Repeat(" ", 64*1024))
		_, _ = w.Write([]byte(`{"keys":[`))
		for i := 0; i < 40; i++ { // 2.5 MiB of white space inside a valid array
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
		_, _ = w.Write([]byte(`]}`))
		return
	case FaultRedirectOtherHost:
		_, port, _ := net.SplitHostPort(is.addr)
		http.Redirect(w, r, "http://localhost:"+port+CertsPath+"?redirected=1", http.StatusFound)
		return
	case FaultRedirectNonLoopback:
		http.Redirect(w, r, "https://example.net"+CertsPath, http.StatusFound)
		return
	case FaultRedirectSameHost:
		http.Redirect(w, r, "/elsewhere?redirected=1", http.StatusFound)
		return
	case FaultWrongContentType:
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(body)
		return
	case FaultEmptyKeySet:
		body = []byte(`{"keys":[]}`)
	case FaultNoUsableKey:
		body = ecJWKS()
	case FaultStatus500:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(body)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(body)
}

// b64 is base64url without padding (RFC 7515 section 2).
func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// RSAJWK renders an RSA public key as a JWK with kid, alg RS256 and use sig.
func RSAJWK(id string, pub *rsa.PublicKey) map[string]any {
	return map[string]any{
		"kid": id, "kty": "RSA", "alg": "RS256", "use": "sig",
		"n": b64(pub.N.Bytes()), "e": b64(big.NewInt(int64(pub.E)).Bytes()),
	}
}

// JWKS renders keys as a key set in the vendor's shape (keys plus the
// certificate fields the vendor also serves, left empty here).
func JWKS(ks []Key) []byte {
	list := make([]map[string]any, 0, len(ks))
	for _, k := range ks {
		list = append(list, RSAJWK(k.ID, &k.Private.PublicKey))
	}
	b, err := json.Marshal(map[string]any{"keys": list, "public_certs": []any{}})
	if err != nil {
		panic(err)
	}
	return b
}

func ecJWKS() []byte {
	raw, err := ECKey().PublicKey.Bytes() // 0x04 || X || Y
	if err != nil {
		panic(err)
	}
	b, err := json.Marshal(map[string]any{"keys": []any{map[string]any{
		"kid": "ec-0", "kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig",
		"x": b64(raw[1:33]), "y": b64(raw[33:]),
	}}})
	if err != nil {
		panic(err)
	}
	return b
}

// WaitFetches blocks until at least n fetches arrived or ctx ends.
func (is *Issuer) WaitFetches(ctx context.Context, n int64) error {
	for is.Fetches() < n {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond): // the polling interval of a context-bounded wait
		}
	}
	return nil
}
