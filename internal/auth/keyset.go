package auth

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// Key set limits (spec 5.7).
const (
	// FetchTimeout bounds one key set fetch, connection to last byte.
	FetchTimeout = 10 * time.Second
	// MaxKeySetBytes caps the key set response body.
	MaxKeySetBytes = 1 << 20
	// MinRefetchInterval is the least time between two fetches caused by an
	// unknown kid (or by a request while no key set is loaded).
	MinRefetchInterval = time.Minute
	// DefaultRefreshInterval is how often the key set is refreshed.
	DefaultRefreshInterval = time.Hour
	// MaxKeyAge is how long a key set stays trusted without a successful
	// fetch: the vendor keeps a retired key valid for seven days (F8), so a
	// set older than that may hold a key the vendor no longer honours.
	MaxKeyAge = 7 * 24 * time.Hour
	// minRSABits is the smallest RSA modulus a served key may have.
	minRSABits = 2048
	// maxRedirects is how many same-host redirects a fetch follows.
	maxRedirects = 3
)

// fetchFailure names why a fetch failed, for the log. It is a closed set:
// the log never carries the URL, a network error text or the body.
type fetchFailure string

const (
	failNetwork     fetchFailure = "network"
	failStatus      fetchFailure = "status"
	failContentType fetchFailure = "content_type"
	failTooLarge    fetchFailure = "too_large"
	failDecode      fetchFailure = "decode"
	failNoUsableKey fetchFailure = "no_usable_key"
	failClosed      fetchFailure = "closed"
	failRedirect    fetchFailure = "redirect"
)

func (f fetchFailure) Error() string { return "auth: key set fetch failed: " + string(f) }

// fetchCall is one fetch in flight; every caller that wants a fetch while
// it runs waits for it instead of starting another.
type fetchCall struct {
	done chan struct{}
	err  error
}

// keySet holds the verified RS256 keys by kid, fetches them, and limits
// fetches caused by traffic to one per MinRefetchInterval.
type keySet struct {
	url          string
	client       *http.Client
	now          func() time.Time
	log          *slog.Logger
	fetchTimeout time.Duration
	minRefetch   time.Duration

	ctx    context.Context // cancelled by close: ends every fetch in flight
	cancel context.CancelFunc
	wg     sync.WaitGroup // fetch goroutines

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	loaded    bool
	lastOK    time.Time // when the keys in use were fetched
	lastFetch time.Time // start of the last fetch, any cause
	inflight  *fetchCall
	closed    bool
}

func newKeySet(rawURL string, client *http.Client, now func() time.Time, log *slog.Logger) *keySet {
	ctx, cancel := context.WithCancel(context.Background())
	ks := &keySet{
		url: rawURL, now: now, log: log,
		fetchTimeout: FetchTimeout, minRefetch: MinRefetchInterval,
		ctx: ctx, cancel: cancel,
	}
	ks.client = fetchClient(client, rawURL)
	return ks
}

// fetchClient returns a copy of base (or of a fresh client) that sends no
// cookies, follows at most maxRedirects redirects and only to the same host
// over https (or over http to a loopback address, for tests), and has a
// total timeout of FetchTimeout.
func fetchClient(base *http.Client, rawURL string) *http.Client {
	c := &http.Client{}
	if base != nil {
		c.Transport = base.Transport
	}
	c.Jar = nil
	c.Timeout = FetchTimeout
	origin, _ := url.Parse(rawURL)
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return failRedirect
		}
		if origin == nil || req.URL.Host != origin.Host || !allowedScheme(req.URL) {
			return failRedirect
		}
		return nil
	}
	return c
}

// allowedScheme accepts https, and http only to a loopback IP literal.
func allowedScheme(u *url.URL) bool {
	if u.Scheme == "https" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return u.Scheme == "http" && ip != nil && ip.IsLoopback()
}

// ready reports whether a key set is loaded and was fetched at most
// MaxKeyAge ago.
func (ks *keySet) ready() bool {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	return ks.freshLocked()
}

// freshLocked is ready with ks.mu held.
func (ks *keySet) freshLocked() bool {
	return ks.loaded && ks.now().Sub(ks.lastOK) <= MaxKeyAge
}

// lookup returns the key for kid. When kid is unknown, or no fresh key set
// is loaded (none yet, or the last success is older than MaxKeyAge), it
// joins the fetch in flight, or starts one if MinRefetchInterval has passed
// since the last fetch began, and looks again once that fetch ends.
// Otherwise it answers at once: ReasonKeysUnavailable while no fresh set is
// loaded, ReasonUnknownKey after.
func (ks *keySet) lookup(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	ks.mu.Lock()
	if k, ok := ks.keys[kid]; ok && ks.freshLocked() {
		ks.mu.Unlock()
		return k, nil
	}
	call := ks.inflight
	if call == nil && !ks.closed && ks.now().Sub(ks.lastFetch) >= ks.minRefetch {
		call = ks.startLocked() //nolint:contextcheck // the fetch is shared by every waiting caller, so it runs under the key set's context, which Close cancels; this caller waits on its own ctx
	}
	ks.mu.Unlock()
	if call != nil {
		select {
		case <-call.done:
		case <-ctx.Done():
		}
	}
	ks.mu.Lock()
	defer ks.mu.Unlock()
	if !ks.freshLocked() {
		return nil, reject(ReasonKeysUnavailable)
	}
	if k, ok := ks.keys[kid]; ok {
		return k, nil
	}
	return nil, reject(ReasonUnknownKey)
}

// refresh fetches now, whatever the limiter says (the periodic refresh and
// the first fetch), joining a fetch already in flight. It returns that
// fetch's error.
func (ks *keySet) refresh(ctx context.Context) error {
	ks.mu.Lock()
	if ks.closed {
		ks.mu.Unlock()
		return failClosed
	}
	call := ks.inflight
	if call == nil {
		call = ks.startLocked() //nolint:contextcheck // as in lookup: a shared fetch runs under the key set's context
	}
	ks.mu.Unlock()
	select {
	case <-call.done:
		return call.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// startLocked starts a fetch in its own goroutine. ks.mu must be held and
// ks.closed false.
func (ks *keySet) startLocked() *fetchCall {
	call := &fetchCall{done: make(chan struct{})}
	ks.inflight = call
	ks.lastFetch = ks.now()
	ks.wg.Add(1)
	go func() {
		defer ks.wg.Done()
		keys, err := ks.fetch()
		ks.mu.Lock()
		if err == nil {
			ks.keys, ks.loaded, ks.lastOK = keys, true, ks.now()
		}
		ks.inflight = nil
		ks.mu.Unlock()
		// Log before the waiters are released, so a caller that sees the
		// fetch's result also sees its log line (a test read the log
		// empty once, under -count=2).
		if err != nil {
			ks.log.Warn("auth: key set fetch failed; keeping the last good keys", "reason", string(asFailure(err)), "keys_loaded", ks.ready())
		} else {
			ks.log.Info("auth: key set loaded", "keys", len(keys))
		}
		call.err = err
		close(call.done)
	}()
	return call
}

func asFailure(err error) fetchFailure {
	var f fetchFailure
	if errors.As(err, &f) {
		return f
	}
	return failNetwork
}

// close cancels every fetch in flight and waits for its goroutine. Later
// lookups start no fetch.
func (ks *keySet) close() {
	ks.mu.Lock()
	ks.closed = true
	ks.mu.Unlock()
	ks.cancel()
	ks.wg.Wait()
}

// fetch gets and parses the key set. Its errors are fetchFailure values.
func (ks *keySet) fetch() (map[string]*rsa.PublicKey, error) {
	ctx, cancel := context.WithTimeout(ks.ctx, ks.fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ks.url, http.NoBody) //nolint:gosec // G704: the URL is the operator's certs_url or built from the team domain, never request input
	if err != nil {
		return nil, failNetwork
	}
	req.Header.Set("Accept", "application/json")
	resp, err := ks.client.Do(req) //nolint:gosec // G704: see the request above; redirects are limited to the same host
	if err != nil {
		if ks.ctx.Err() != nil {
			return nil, failClosed
		}
		if errors.Is(err, failRedirect) {
			return nil, failRedirect
		}
		return nil, failNetwork
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, failStatus
	}
	mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || (mt != "application/json" && mt != "application/jwk-set+json") {
		return nil, failContentType
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxKeySetBytes+1))
	if err != nil {
		return nil, failNetwork
	}
	if len(body) > MaxKeySetBytes {
		return nil, failTooLarge
	}
	return parseKeySet(body)
}

// parseKeySet keeps the usable keys of a JWKS: RSA public keys (go-jose
// parses and validates each JWK) with a kid, alg RS256 or none given, use
// sig or none given, and a modulus of at least minRSABits. A key that does
// not parse, a private key, a duplicate kid and every other key type are
// skipped. A set with no usable key is a failure.
func parseKeySet(body []byte) (map[string]*rsa.PublicKey, error) {
	var raw struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, failDecode
	}
	keys := map[string]*rsa.PublicKey{}
	for _, r := range raw.Keys {
		var k jose.JSONWebKey
		if err := k.UnmarshalJSON(r); err != nil {
			continue
		}
		pub, ok := k.Key.(*rsa.PublicKey)
		if !ok || k.KeyID == "" || pub.N.BitLen() < minRSABits {
			continue
		}
		if (k.Algorithm != "" && k.Algorithm != string(jose.RS256)) || (k.Use != "" && k.Use != "sig") {
			continue
		}
		if _, dup := keys[k.KeyID]; dup {
			continue
		}
		keys[k.KeyID] = pub
	}
	if len(keys) == 0 {
		return nil, failNoUsableKey
	}
	return keys, nil
}
