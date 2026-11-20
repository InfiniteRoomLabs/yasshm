package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/accesstest"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/leakcheck"
)

func randomKid(t *testing.T) string {
	t.Helper()
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// TestRefetchLimit: a thousand tokens with random kids cause at most one
// fetch per minute of the injected clock.
func TestRefetchLimit(t *testing.T) {
	f := newFixture(t)
	f.load(t)
	base := f.is.Fetches()
	user := f.is.User(userEmail)
	tokens := make([]string, 1000)
	for i := range tokens {
		tokens[i] = user.Header("kid", randomKid(t)).Sign()
	}
	run := func(t *testing.T) {
		t.Helper()
		var wg sync.WaitGroup
		for _, tk := range tokens {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := f.a.Authenticate(request(tk)); reasonOf(t, err) != ReasonUnknownKey {
					t.Errorf("reason = %v", err)
				}
			}()
		}
		wg.Wait()
	}
	t.Run("raging/inside the minute after a fetch no refetch happens", func(t *testing.T) {
		run(t)
		if got := f.is.Fetches() - base; got != 0 {
			t.Fatalf("%d fetches", got)
		}
	})
	t.Run("raging/59 seconds on still none", func(t *testing.T) {
		f.clk.Add(59 * time.Second)
		run(t)
		if got := f.is.Fetches() - base; got != 0 {
			t.Fatalf("%d fetches", got)
		}
	})
	t.Run("raging/a minute on exactly one", func(t *testing.T) {
		f.clk.Add(time.Second)
		run(t)
		if got := f.is.Fetches() - base; got != 1 {
			t.Fatalf("%d fetches", got)
		}
	})
	t.Run("raging/ten minutes of a stream: one per minute", func(t *testing.T) {
		for range 10 {
			f.clk.Add(time.Minute)
			run(t)
		}
		if got := f.is.Fetches() - base; got != 11 {
			t.Fatalf("%d fetches", got)
		}
	})
	t.Run("raging/refused tokens never cause a fetch", func(t *testing.T) {
		f.clk.Add(time.Hour)
		before := f.is.Fetches()
		for _, b := range []*accesstest.Builder{
			f.is.User(userEmail).Header("kid", randomKid(t)).Alg(accesstest.AlgNone),
			f.is.User(userEmail).Header("kid", randomKid(t)).HS256WithPublicKey(),
			f.is.User(userEmail).Header("kid", randomKid(t)).Alg(accesstest.AlgES256),
			f.is.User(userEmail).WithoutHeader("kid"),
		} {
			if _, err := f.a.Authenticate(request(b.Sign())); err == nil {
				t.Fatal("accepted")
			}
		}
		if _, err := f.a.Authenticate(request("x.y.z")); err == nil {
			t.Fatal("accepted")
		}
		if f.is.Fetches() != before {
			t.Fatalf("%d fetches for tokens refused before the key lookup", f.is.Fetches()-before)
		}
	})
}

// TestSharedFetch: requests that arrive while a fetch is in flight wait for
// it and do not start another.
func TestSharedFetch(t *testing.T) {
	f := newFixture(t)
	f.load(t)
	newKey := f.is.Rotate()
	f.clk.Add(MinRefetchInterval)
	base := f.is.Fetches()
	release := f.is.Hold()
	defer release()
	tk := f.is.User(userEmail).Sign()
	if f.is.SigningKey().ID != newKey.ID {
		t.Fatal("token not signed by the new key")
	}
	const n = 50
	errs := make(chan error, n)
	for range n {
		go func() {
			_, err := f.a.Authenticate(request(tk))
			errs <- err
		}()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.is.WaitFetches(ctx, base+1); err != nil {
		t.Fatal(err)
	}
	release()
	for range n {
		if err := <-errs; err != nil {
			t.Errorf("corner/request during a refetch: %v", err)
		}
	}
	if got := f.is.Fetches() - base; got != 1 {
		t.Fatalf("corner/%d fetches for %d concurrent requests, want 1", got, n)
	}
}

func TestRotation(t *testing.T) {
	f := newFixture(t)
	f.load(t)
	oldTok := f.is.User(userEmail).Sign()
	f.is.Rotate()
	newTok := f.is.User(userEmail).Sign()
	t.Run("sad/a new key inside the refetch limit is unknown", func(t *testing.T) {
		if _, err := f.a.Authenticate(request(newTok)); reasonOf(t, err) != ReasonUnknownKey {
			t.Fatalf("reason = %v", err)
		}
	})
	t.Run("happy/a new key is picked up by the unknown-kid refetch", func(t *testing.T) {
		f.clk.Add(MinRefetchInterval)
		if _, err := f.a.Authenticate(request(newTok)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("happy/the old key stays valid while it is served", func(t *testing.T) {
		if _, err := f.a.Authenticate(request(oldTok)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("sad/a dropped key is refused after the next refresh", func(t *testing.T) {
		f.is.DropOld()
		if err := f.a.keys.refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := f.a.Authenticate(request(oldTok)); reasonOf(t, err) != ReasonUnknownKey {
			t.Fatalf("reason = %v", err)
		}
		if _, err := f.a.Authenticate(request(newTok)); err != nil {
			t.Fatal(err)
		}
	})
}

// TestIssuerDownAtStart: the authenticator starts without keys, refuses
// with keys_unavailable, and starts passing once the issuer is back and the
// limiter allows a fetch.
func TestIssuerDownAtStart(t *testing.T) {
	f := newFixture(t)
	f.is.Down()
	tk := f.is.User(userEmail).Sign()
	if err := f.a.keys.refresh(context.Background()); err == nil {
		t.Fatal("fetch from a stopped issuer succeeded")
	}
	t.Run("sad/not ready, every request is keys_unavailable", func(t *testing.T) {
		if f.a.Ready() {
			t.Fatal("ready")
		}
		if _, err := f.a.Authenticate(request(tk)); reasonOf(t, err) != ReasonKeysUnavailable {
			t.Fatalf("reason = %v", err)
		}
		if !strings.Contains(f.logs.String(), `"reason":"network"`) {
			t.Fatalf("log = %s", f.logs.String())
		}
	})
	f.is.Up()
	t.Run("sad/back up but inside the limit: still unavailable, no fetch", func(t *testing.T) {
		if _, err := f.a.Authenticate(request(tk)); reasonOf(t, err) != ReasonKeysUnavailable {
			t.Fatalf("reason = %v", err)
		}
		if f.is.Fetches() != 0 {
			t.Fatalf("%d fetches", f.is.Fetches())
		}
	})
	t.Run("happy/a minute on the request fetches and passes", func(t *testing.T) {
		f.clk.Add(MinRefetchInterval)
		if _, err := f.a.Authenticate(request(tk)); err != nil {
			t.Fatal(err)
		}
		if !f.a.Ready() || f.a.Status() != "key set loaded" || f.is.Fetches() != 1 {
			t.Fatalf("ready %t, %d fetches", f.a.Ready(), f.is.Fetches())
		}
	})
}

// TestIssuerFaults: no fault makes the authenticator hang, crash or accept a
// token; a failed fetch before the first success leaves it unavailable, and
// one after keeps the last good keys.
func TestIssuerFaults(t *testing.T) {
	cases := []struct {
		name  string
		fault accesstest.Fault
		want  fetchFailure
	}{
		{"raging/slow issuer past the timeout", accesstest.FaultSlow, failNetwork},
		{"raging/garbage body", accesstest.FaultGarbage, failDecode},
		{"raging/huge body", accesstest.FaultHuge, failTooLarge},
		{"raging/redirect to another host", accesstest.FaultRedirectOtherHost, failRedirect},
		{"raging/redirect to a non-loopback host", accesstest.FaultRedirectNonLoopback, failRedirect},
		{"sad/wrong content type", accesstest.FaultWrongContentType, failContentType},
		{"sad/empty key set", accesstest.FaultEmptyKeySet, failNoUsableKey},
		{"sad/no usable key", accesstest.FaultNoUsableKey, failNoUsableKey},
		{"sad/status 500", accesstest.FaultStatus500, failStatus},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.a.keys.fetchTimeout = 300 * time.Millisecond
			f.is.SetFault(c.fault)
			tk := f.is.User(userEmail).Sign()
			start := time.Now()
			err := f.a.keys.refresh(context.Background())
			if !errors.Is(err, c.want) {
				t.Fatalf("fetch error = %v, want %v", err, c.want)
			}
			if time.Since(start) > 5*time.Second {
				t.Fatalf("fetch took %v", time.Since(start))
			}
			if f.a.Ready() {
				t.Fatal("ready after a failed fetch")
			}
			if _, aerr := f.a.Authenticate(request(tk)); reasonOf(t, aerr) != ReasonKeysUnavailable {
				t.Fatalf("reason = %v", aerr)
			}
			if c.fault == accesstest.FaultRedirectOtherHost && f.is.Fetches() != 1 {
				t.Fatalf("the other host was fetched: %d requests", f.is.Fetches())
			}
			// Recovery: the fault clears, a refresh loads, then the fault
			// returns and a failed refresh keeps the good keys.
			f.is.SetFault(accesstest.FaultNone)
			f.load(t)
			f.is.SetFault(c.fault)
			if rerr := f.a.keys.refresh(context.Background()); !errors.Is(rerr, c.want) {
				t.Fatalf("second fetch error = %v", rerr)
			}
			if _, aerr := f.a.Authenticate(request(tk)); aerr != nil || !f.a.Ready() {
				t.Fatalf("the last good keys were dropped: %v", aerr)
			}
		})
	}
	t.Run("happy/a redirect on the same host is followed", func(t *testing.T) {
		f := newFixture(t)
		f.is.SetFault(accesstest.FaultRedirectSameHost)
		f.load(t)
		if f.is.Fetches() != 2 {
			t.Fatalf("%d requests", f.is.Fetches())
		}
	})
}

// TestFetchCancel: a request that gives up stops waiting at once, and Close
// cancels the fetch itself.
func TestFetchCancel(t *testing.T) {
	leakcheck.Check(t)
	f := newFixture(t)
	f.a.keys.fetchTimeout = time.Minute
	f.is.SetFault(accesstest.FaultSlow)
	ctx, cancel := context.WithCancel(context.Background())
	r := request(f.is.User(userEmail).Sign()).WithContext(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := f.a.Authenticate(r)
		done <- err
	}()
	wctx, wcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wcancel()
	if err := f.is.WaitFetches(wctx, 1); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if reasonOf(t, err) != ReasonKeysUnavailable {
			t.Fatalf("edge/cancelled request: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("edge/a cancelled request kept waiting for the fetch")
	}
	closed := make(chan struct{})
	go func() { f.a.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("edge/Close did not cancel the fetch in flight")
	}
	if !strings.Contains(f.logs.String(), `"reason":"closed"`) {
		t.Fatalf("log = %s", f.logs.String())
	}
	t.Run("sad/after Close no fetch starts", func(t *testing.T) {
		f.is.SetFault(accesstest.FaultNone)
		f.clk.Add(time.Hour)
		before := f.is.Fetches()
		if _, err := f.a.Authenticate(request(f.is.User(userEmail).Sign())); reasonOf(t, err) != ReasonKeysUnavailable {
			t.Fatalf("reason = %v", err)
		}
		if err := f.a.keys.refresh(context.Background()); !errors.Is(err, failClosed) {
			t.Fatalf("refresh after close = %v", err)
		}
		if f.is.Fetches() != before {
			t.Fatal("a fetch started after Close")
		}
	})
}

// TestStartClose: Start fetches without blocking and refreshes on its
// interval; Close stops it and leaves no goroutine.
func TestStartClose(t *testing.T) {
	leakcheck.Check(t)
	f := newFixture(t, WithRefreshInterval(20*time.Millisecond))
	f.a.Start(context.Background())
	f.a.Start(context.Background()) // a second Start does nothing
	if !leakcheck.Poll(5*time.Second, f.a.Ready) {
		t.Fatal("happy/never became ready")
	}
	if !leakcheck.Poll(5*time.Second, func() bool { return f.is.Fetches() >= 3 }) {
		t.Fatalf("happy/periodic refresh: %d fetches", f.is.Fetches())
	}
	f.a.Close()
	f.a.Close()
	after := f.is.Fetches()
	f.a.Start(context.Background()) // Start after Close does nothing
	if f.is.Fetches() != after {
		t.Fatal("sad/fetched after Close")
	}
	t.Run("happy/the refresh ends with its context", func(t *testing.T) {
		g := newFixture(t, WithRefreshInterval(time.Hour))
		ctx, cancel := context.WithCancel(context.Background())
		g.a.Start(ctx)
		if !leakcheck.Poll(5*time.Second, g.a.Ready) {
			t.Fatal("never ready")
		}
		cancel()
		g.a.Close()
	})
	t.Run("sad/after a failed fetch the next try is a minute away, not an hour", func(t *testing.T) {
		g := newFixture(t, WithRefreshInterval(time.Hour))
		g.is.Down()
		g.a.Start(context.Background())
		// The loop's retry wait is real time; check the value it chose.
		if wait := retryWait(time.Hour, errors.New("x")); wait != MinRefetchInterval {
			t.Fatalf("wait = %v", wait)
		}
		if wait := retryWait(time.Hour, nil); wait != time.Hour {
			t.Fatalf("wait = %v", wait)
		}
		if wait := retryWait(time.Second, errors.New("x")); wait != time.Second {
			t.Fatalf("wait = %v", wait)
		}
		g.a.Close()
	})
}

// TestParseKeySet covers the key filter on its own.
func TestParseKeySet(t *testing.T) {
	good := accesstest.RSAJWK("good", &accesstest.RSAKey(0).PublicKey)
	small, err := rsa.GenerateKey(rand.Reader, 1024) //nolint:gosec // G403: a key below the minimum is what this test feeds in
	if err != nil {
		t.Fatal(err)
	}
	with := func(k map[string]any, field string, v any) map[string]any {
		c := map[string]any{}
		for kk, vv := range k {
			c[kk] = vv
		}
		if v == nil {
			delete(c, field)
		} else {
			c[field] = v
		}
		return c
	}
	priv := with(good, "kid", "private")
	priv["d"] = b64(string(accesstest.RSAKey(0).D.Bytes()))
	cases := []struct {
		name string
		keys []any
		want []string
	}{
		{"happy/one good key", []any{good}, []string{"good"}},
		{"happy/no alg and no use are accepted", []any{with(with(good, "alg", nil), "use", nil)}, []string{"good"}},
		{"sad/a key without kid is skipped", []any{with(good, "kid", nil)}, nil},
		{"sad/an RS384 key is skipped", []any{with(good, "alg", "RS384")}, nil},
		{"sad/an encryption key is skipped", []any{with(good, "use", "enc")}, nil},
		{"sad/a 1024-bit key is skipped", []any{accesstest.RSAJWK("small", &small.PublicKey)}, nil},
		{"raging/a private key is skipped", []any{priv}, nil},
		{"raging/a key that does not parse is skipped", []any{map[string]any{"kid": "x", "kty": "RSA", "n": "!!", "e": "AQAB"}, good}, []string{"good"}},
		{"raging/an unknown key type is skipped", []any{map[string]any{"kid": "x", "kty": "OKP", "crv": "X448", "x": "AA"}, good}, []string{"good"}},
		{"corner/a duplicate kid keeps the first", []any{good, with(accesstest.RSAJWK("good", &accesstest.RSAKey(1).PublicKey), "kid", "good")}, []string{"good"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"keys": c.keys})
			if err != nil {
				t.Fatal(err)
			}
			keys, err := parseKeySet(body)
			if len(c.want) == 0 {
				if !errors.Is(err, failNoUsableKey) {
					t.Fatalf("err = %v, keys %v", err, keys)
				}
				return
			}
			if err != nil || len(keys) != len(c.want) || keys[c.want[0]] == nil {
				t.Fatalf("keys = %v, err %v", keys, err)
			}
			if c.name == "corner/a duplicate kid keeps the first" && keys["good"].N.Cmp(accesstest.RSAKey(0).N) != 0 {
				t.Fatal("the second key won")
			}
		})
	}
	t.Run("raging/a set of non-objects has no usable key", func(t *testing.T) {
		if _, err := parseKeySet([]byte(`{"keys":[1,"x",null]}`)); !errors.Is(err, failNoUsableKey) {
			t.Fatalf("err = %v", err)
		}
	})
	for _, body := range []string{`not json`, `{"keys":{}}`, `[]`} {
		t.Run("raging/body "+body, func(t *testing.T) {
			if _, err := parseKeySet([]byte(body)); !errors.Is(err, failDecode) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	t.Run("edge/the exponent is read", func(t *testing.T) {
		keys, err := parseKeySet(accesstest.JWKS([]accesstest.Key{{ID: "k", Private: accesstest.RSAKey(0)}}))
		if err != nil || keys["k"].E != 65537 || keys["k"].N.Cmp(big.NewInt(0)) <= 0 {
			t.Fatal(keys, err)
		}
	})
}

// TestFetchClient checks the redirect policy and the client settings.
func TestFetchClient(t *testing.T) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := fetchClient(&http.Client{Jar: jar, Timeout: time.Hour}, "https://example-team.cloudflareaccess.com/cdn-cgi/access/certs")
	if c.Jar != nil || c.Timeout != FetchTimeout {
		t.Fatalf("jar %v, timeout %v", c.Jar, c.Timeout)
	}
	mk := func(raw string) *http.Request {
		r, err := http.NewRequestWithContext(context.Background(), http.MethodGet, raw, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	via := []*http.Request{mk("https://example-team.cloudflareaccess.com/cdn-cgi/access/certs")}
	cases := []struct {
		name string
		to   string
		via  int
		ok   bool
	}{
		{"happy/same host https", "https://example-team.cloudflareaccess.com/other", 1, true},
		{"sad/other host", "https://example.net/cdn-cgi/access/certs", 1, false},
		{"sad/downgrade to http", "http://example-team.cloudflareaccess.com/cdn-cgi/access/certs", 1, false},
		{"sad/other port", "https://example-team.cloudflareaccess.com:8443/x", 1, false},
		{"raging/too many redirects", "https://example-team.cloudflareaccess.com/x", maxRedirects + 1, false},
	}
	for _, cs := range cases {
		t.Run(cs.name, func(t *testing.T) {
			v := via
			for len(v) < cs.via {
				v = append(v, via[0])
			}
			err := c.CheckRedirect(mk(cs.to), v)
			if (err == nil) != cs.ok {
				t.Fatalf("err = %v", err)
			}
		})
	}
	t.Run("edge/loopback http is allowed only as a loopback IP", func(t *testing.T) {
		for raw, ok := range map[string]bool{"http://127.0.0.1:1/x": true, "http://[::1]:1/x": true, "http://localhost:1/x": false, "ftp://127.0.0.1/x": false} {
			if allowedScheme(mk(raw).URL) != ok {
				t.Errorf("%s: want %t", raw, ok)
			}
		}
	})
}
