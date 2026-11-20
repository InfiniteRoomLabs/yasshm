// Package auth verifies the identity token the edge puts on every request
// and maps it to a tenant principal (spec 5.7, D4). There is no
// unauthenticated mode.
//
// The one implementation, for Cloudflare Access (F7, F8), reads only the
// header Cf-Access-Jwt-Assertion -- never the CF_Authorization cookie, a
// query parameter or Authorization -- and refuses a request with more than
// one value of it or a token over MaxTokenBytes before parsing anything.
// The token is verified with github.com/go-jose/go-jose/v4: the algorithm
// list given to the parser is RS256 alone, so alg none, HS256, ES256, PS256
// and RS384 are refused at parse time, before any key is looked up; the kid
// must name a key in the key set; the signature is checked with that key;
// iss must equal "https://" + team domain exactly; aud (a string or an
// array) must contain the configured audience; exp, nbf and iat are checked
// with Leeway (30 seconds) each way by the library. The verified payload is
// decoded with the library's own JSON decoder (case-sensitive member names,
// duplicate members refused). This package requires
// exp and iat on every token and nbf on a user token (the library checks a
// time claim only when it is present); the vendor's service tokens carry no
// nbf (F7), so on a service token nbf is checked when present.
//
// The key set comes from certs_url, or https://<team_domain>/cdn-cgi/access/certs.
// It is fetched when Start runs, refreshed every refresh interval (default
// one hour; one minute after a failed fetch), and refetched when a token
// names an unknown kid -- at most once per MinRefetchInterval whatever the
// traffic, and concurrent callers share one fetch. A failed fetch keeps the
// last good keys, but only for MaxKeyAge (seven days, how long the vendor
// keeps a retired key valid) after the last successful fetch. Until the first fetch succeeds every request whose token
// gets as far as the key lookup is ReasonKeysUnavailable (a missing,
// oversized or malformed token, a wrong alg or a missing kid is refused for
// that reason first, without a fetch), and Ready and Status say so.
//
// Errors are *Error values with a Reason from a closed set; their text is
// the reason's name and nothing else. The token, its segments, key material
// and network error text never appear in an error, a log line or an audit
// event. Middleware writes auth.accepted or auth.rejected for every request
// and puts the principal in the request context; a user's subject is the
// email in ASCII lower case. The middleware writes rejections made before a
// signature is verified only RejectAllowance times a minute and summarises
// the rest.
package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/json"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/InfiniteRoomLabs/yasshm/internal/config"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// Header is the only place a token is read from (F8).
const Header = "Cf-Access-Jwt-Assertion"

// MaxTokenBytes caps the token before it is parsed.
const MaxTokenBytes = 16 << 10

// Leeway is the clock skew allowed on exp, nbf and iat (spec 5.7).
const Leeway = 30 * time.Second

// certsPath is the vendor's JWKS path on the team domain (F8).
const certsPath = "/cdn-cgi/access/certs"

// Authenticator turns a request into a verified principal. An error is an
// *Error unless the implementation is not this package's.
type Authenticator interface {
	Authenticate(r *http.Request) (tenant.Principal, error)
}

// CloudflareAccess verifies Cloudflare Access application tokens. Build it
// with NewCloudflareAccess, call Start, and Close it when done.
type CloudflareAccess struct {
	issuer   string
	audience string
	tenants  []tenant.Tenant
	now      func() time.Time
	log      *slog.Logger
	interval time.Duration
	retry    time.Duration // the wait after a failed fetch; MinRefetchInterval, shorter in tests
	keys     *keySet

	mu      sync.Mutex // guards started and closed
	started bool
	closed  bool
	stop    chan struct{}
	wg      sync.WaitGroup // the refresh loop
}

// Option configures NewCloudflareAccess.
type Option func(*options)

type options struct {
	now      func() time.Time
	client   *http.Client
	interval time.Duration
	log      *slog.Logger
}

// WithClock sets the clock used for token times and the refetch limit; the
// default is time.Now.
func WithClock(now func() time.Time) Option { return func(o *options) { o.now = now } }

// WithHTTPClient sets the client whose Transport fetches the key set. Its
// Jar, Timeout and CheckRedirect are replaced: no cookies, FetchTimeout,
// same-host https redirects only.
func WithHTTPClient(c *http.Client) Option { return func(o *options) { o.client = c } }

// WithRefreshInterval sets how often the key set is refreshed; the default
// is DefaultRefreshInterval.
func WithRefreshInterval(d time.Duration) Option { return func(o *options) { o.interval = d } }

// WithLogger sets the logger for key set events; the default discards.
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.log = l } }

// NewCloudflareAccess builds the authenticator for cfg and tenants. It does
// not fetch: Start does, without blocking. It fails when the team domain or
// audience is empty, the certs URL is not https (or http to a loopback
// address), or the tenants cannot be mapped unambiguously (no tenant, a
// tenant without an id, a duplicate id, or two tenants that admit the same
// principal).
func NewCloudflareAccess(cfg config.CloudflareAccess, tenants []tenant.Tenant, opts ...Option) (*CloudflareAccess, error) {
	o := options{now: time.Now, interval: DefaultRefreshInterval, log: slog.New(slog.DiscardHandler)}
	for _, opt := range opts {
		opt(&o)
	}
	if cfg.TeamDomain == "" || cfg.Audience == "" {
		return nil, errors.New("auth: team domain and audience are required")
	}
	if o.interval <= 0 {
		return nil, errors.New("auth: the refresh interval must be positive")
	}
	certs := cfg.CertsURL
	if certs == "" {
		certs = "https://" + cfg.TeamDomain + certsPath
	}
	u, err := url.Parse(certs)
	if err != nil || u.Host == "" || u.User != nil || !allowedScheme(u) {
		return nil, errors.New("auth: the certs URL must be https, or http to a loopback address")
	}
	if err := checkTenants(tenants); err != nil {
		return nil, err
	}
	return &CloudflareAccess{
		issuer:   "https://" + cfg.TeamDomain,
		audience: cfg.Audience,
		tenants:  append([]tenant.Tenant(nil), tenants...),
		now:      o.now,
		log:      o.log,
		interval: o.interval,
		retry:    MinRefetchInterval,
		keys:     newKeySet(certs, o.client, o.now, o.log),
		stop:     make(chan struct{}),
	}, nil
}

// Start begins the first fetch and the periodic refresh, and returns at
// once. The refresh stops when ctx ends or Close runs. A second call, or a
// call after Close, does nothing.
func (a *CloudflareAccess) Start(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started || a.closed {
		return
	}
	a.started = true
	a.wg.Add(1)
	go a.run(ctx)
}

func (a *CloudflareAccess) run(ctx context.Context) {
	defer a.wg.Done()
	for {
		t := time.NewTimer(retryWait(a.interval, a.retry, a.keys.refresh(ctx)))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-a.stop:
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// retryWait is how long the refresh loop waits after a fetch: the
// interval, or at most retry after a failure.
func retryWait(interval, retry time.Duration, err error) time.Duration {
	if err != nil && interval > retry {
		return retry
	}
	return interval
}

// Close stops the refresh, cancels any fetch in flight and waits for both.
// After Close no fetch starts, so a request needing one is refused. It is
// safe to call more than once.
func (a *CloudflareAccess) Close() {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	close(a.stop)
	a.mu.Unlock()
	a.keys.close()
	a.wg.Wait()
}

// Ready reports whether a key set has been loaded, for /healthz.
func (a *CloudflareAccess) Ready() bool { return a.keys.ready() }

// Status is a short text for /healthz: "key set loaded" or "key set not
// loaded". It never holds the URL or a network error.
func (a *CloudflareAccess) Status() string {
	if a.Ready() {
		return "key set loaded"
	}
	return "key set not loaded"
}

// Authenticate verifies the request's token and maps it to a principal.
// Every error is an *Error.
func (a *CloudflareAccess) Authenticate(r *http.Request) (tenant.Principal, error) {
	vals := r.Header.Values(Header)
	switch {
	case len(vals) == 0 || (len(vals) == 1 && vals[0] == ""):
		return tenant.Principal{}, reject(ReasonMissingToken)
	case len(vals) > 1:
		return tenant.Principal{}, reject(ReasonMalformedToken)
	case len(vals[0]) > MaxTokenBytes:
		return tenant.Principal{}, reject(ReasonOversizedToken)
	}
	return a.verify(r.Context(), vals[0])
}

func (a *CloudflareAccess) verify(ctx context.Context, raw string) (tenant.Principal, error) {
	// Everything that can be refused without a key is refused first, so a
	// malformed token, a wrong alg or a missing kid never causes a fetch.
	jws, err := jose.ParseSignedCompact(raw, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		var alg *jose.ErrUnexpectedSignatureAlgorithm
		if errors.As(err, &alg) {
			return tenant.Principal{}, reject(ReasonBadAlgorithm)
		}
		return tenant.Principal{}, reject(ReasonMalformedToken)
	}
	if len(jws.Signatures) != 1 {
		return tenant.Principal{}, reject(ReasonMalformedToken)
	}
	kid := jws.Signatures[0].Header.KeyID
	if kid == "" {
		return tenant.Principal{}, reject(ReasonUnknownKey)
	}
	key, err := a.keys.lookup(ctx, kid)
	if err != nil {
		return tenant.Principal{}, err
	}
	payload, err := jws.Verify(key)
	if err != nil {
		return tenant.Principal{}, reject(ReasonBadSignature)
	}
	// The library's own decoder: member names match case-sensitively and a
	// duplicate member is an error, as in jwt.JSONWebToken.Claims.
	var std jwt.Claims
	var id identity
	if json.Unmarshal(payload, &std) != nil || json.Unmarshal(payload, &id) != nil {
		return tenant.Principal{}, reject(ReasonMalformedToken)
	}
	if err := a.checkClaims(&std, &id); err != nil {
		return tenant.Principal{}, err
	}
	return resolve(id, a.tenants)
}

// checkClaims runs the library's issuer, audience and time checks with
// Leeway, after requiring the time claims the library treats as optional.
func (a *CloudflareAccess) checkClaims(std *jwt.Claims, id *identity) error {
	if std.Expiry == nil || std.IssuedAt == nil || (std.NotBefore == nil && id.Email != "") {
		return reject(ReasonMissingClaim)
	}
	err := std.ValidateWithLeeway(jwt.Expected{
		Issuer:      a.issuer,
		AnyAudience: jwt.Audience{a.audience},
		Time:        a.now(),
	}, Leeway)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, jwt.ErrInvalidIssuer):
		return reject(ReasonWrongIssuer)
	case errors.Is(err, jwt.ErrInvalidAudience):
		return reject(ReasonWrongAudience)
	case errors.Is(err, jwt.ErrExpired):
		return reject(ReasonExpired)
	case errors.Is(err, jwt.ErrNotValidYet):
		return reject(ReasonNotYetValid)
	case errors.Is(err, jwt.ErrIssuedInTheFuture):
		return reject(ReasonIssuedInFuture)
	default:
		return reject(ReasonMalformedToken)
	}
}

var _ Authenticator = (*CloudflareAccess)(nil)

// String names the authenticator without its URL or audience.
func (a *CloudflareAccess) String() string {
	return fmt.Sprintf("auth.CloudflareAccess(%s)", a.Status())
}
