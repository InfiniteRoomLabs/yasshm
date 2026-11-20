package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/accesstest"
)

// tokenClass is one kind of token (or of request) and what must happen to it.
type tokenClass struct {
	name string
	// req builds the request; most classes set only the token header.
	req  func(f *fixture) *http.Request
	want Reason // zero Reason: accepted
	// who is the principal accepted, or recorded for an unmapped token.
	who tenant.Principal
}

func tok(build func(f *fixture) string) func(f *fixture) *http.Request {
	return func(f *fixture) *http.Request { return request(build(f)) }
}

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

// userPayload is a valid user token's claims without email, as JSON, with
// extra (raw JSON members) appended: the way to send duplicate or
// case-variant members, which a map cannot hold.
func userPayload(f *fixture, extra string) string {
	b, err := json.Marshal(f.is.User(userEmail).Without("email").Claims())
	if err != nil {
		panic(err)
	}
	return strings.TrimSuffix(string(b), "}") + "," + extra + "}"
}

// signedPayload signs an arbitrary payload with the issuer's current key.
func signedPayload(f *fixture, payload string) string {
	k := f.is.SigningKey()
	h := `{"alg":"RS256","kid":"` + k.ID + `","typ":"JWT"}`
	return accesstest.SignRaw(b64(h)+"."+b64(payload), accesstest.AlgRS256, k.Private, nil)
}

var (
	user    = tenant.Principal{Tenant: tenantID, Kind: tenant.KindUser, Subject: userEmail}
	service = tenant.Principal{Tenant: tenantID, Kind: tenant.KindService, Subject: serviceCN}
)

// tokenClasses is every class of the GOAL contract and spec section 7, and
// the request shapes around them.
func tokenClasses() []tokenClass {
	now := func(f *fixture) time.Time { return f.clk.Now() }
	return []tokenClass{
		{name: "happy/valid user", req: tok(func(f *fixture) string { return f.is.User(userEmail).Sign() }), who: user},
		{name: "happy/valid service token", req: tok(func(f *fixture) string { return f.is.Service(serviceCN).Sign() }), who: service},
		{name: "happy/user of the second tenant", req: tok(func(f *fixture) string { return f.is.User(user2Email).Sign() }),
			who: tenant.Principal{Tenant: tenant2ID, Kind: tenant.KindUser, Subject: user2Email}},
		{name: "happy/audience array with several entries", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).Claim("aud", []string{"other-app-1", accesstest.Audience, "other-app-2"}).Sign()
		}), who: user},
		{name: "edge/audience as a single string", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).Claim("aud", accesstest.Audience).Sign()
		}), who: user},
		{name: "edge/email in another ASCII case", req: tok(func(f *fixture) string {
			return f.is.User("Operator@Example.NET").Sign()
		}), who: user},
		{name: "edge/exp exactly 30 seconds ago", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).Times(now(f), -600, -600, -30).Sign()
		}), who: user},
		{name: "edge/nbf and iat exactly 30 seconds ahead", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).Times(now(f), 30, 30, 600).Sign()
		}), who: user},
		{name: "edge/service token without nbf", req: tok(func(f *fixture) string {
			return f.is.Service(serviceCN).Without("nbf").Sign()
		}), who: service},
		{name: "sad/expired", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).Times(now(f), -600, -600, -31).Sign()
		}), want: ReasonExpired},
		{name: "sad/not yet valid", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).Times(now(f), 0, 31, 600).Sign()
		}), want: ReasonNotYetValid},
		{name: "sad/service token with nbf in the future", req: tok(func(f *fixture) string {
			return f.is.Service(serviceCN).Claim("nbf", now(f).Unix()+31).Sign()
		}), want: ReasonNotYetValid},
		{name: "sad/issued in the future", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).Times(now(f), 31, 0, 600).Sign()
		}), want: ReasonIssuedInFuture},
		{name: "sad/wrong audience", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).Claim("aud", []string{"another-application"}).Sign()
		}), want: ReasonWrongAudience},
		{name: "sad/audience that differs in case", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).Claim("aud", strings.ToUpper(accesstest.Audience)).Sign()
		}), want: ReasonWrongAudience},
		{name: "sad/empty audience array", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).Claim("aud", []string{}).Sign()
		}), want: ReasonWrongAudience},
		{name: "sad/wrong issuer", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).Claim("iss", "https://other-team.cloudflareaccess.com").Sign()
		}), want: ReasonWrongIssuer},
		{name: "sad/issuer with a trailing slash", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).Claim("iss", "https://"+accesstest.TeamDomain+"/").Sign()
		}), want: ReasonWrongIssuer},
		{name: "sad/issuer over http", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).Claim("iss", "http://"+accesstest.TeamDomain).Sign()
		}), want: ReasonWrongIssuer},
		{name: "sad/issuer in another case", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).Claim("iss", "https://"+strings.ToUpper(accesstest.TeamDomain)).Sign()
		}), want: ReasonWrongIssuer},
		{name: "sad/unknown kid", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).Header("kid", "no-such-key").Sign()
		}), want: ReasonUnknownKey},
		{name: "sad/no kid", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).WithoutHeader("kid").Sign()
		}), want: ReasonUnknownKey},
		{name: "sad/missing exp", req: tok(func(f *fixture) string { return f.is.User(userEmail).Without("exp").Sign() }), want: ReasonMissingClaim},
		{name: "sad/missing iat", req: tok(func(f *fixture) string { return f.is.User(userEmail).Without("iat").Sign() }), want: ReasonMissingClaim},
		{name: "sad/missing nbf on a user token", req: tok(func(f *fixture) string { return f.is.User(userEmail).Without("nbf").Sign() }), want: ReasonMissingClaim},
		{name: "sad/missing iat on a service token", req: tok(func(f *fixture) string { return f.is.Service(serviceCN).Without("iat").Sign() }), want: ReasonMissingClaim},
		{name: "sad/missing iss", req: tok(func(f *fixture) string { return f.is.User(userEmail).Without("iss").Sign() }), want: ReasonWrongIssuer},
		{name: "sad/missing aud", req: tok(func(f *fixture) string { return f.is.User(userEmail).Without("aud").Sign() }), want: ReasonWrongAudience},
		{name: "sad/no email and no common_name", req: tok(func(f *fixture) string { return f.is.User(userEmail).Without("email").Sign() }), want: ReasonMissingClaim},
		{name: "sad/empty email", req: tok(func(f *fixture) string { return f.is.User("").Sign() }), want: ReasonMissingClaim},
		{name: "sad/email and common_name", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).Claim("common_name", serviceCN).Sign()
		}), want: ReasonAmbiguousIdentity},
		{name: "sad/service token with a sub", req: tok(func(f *fixture) string {
			return f.is.Service(serviceCN).Claim("sub", "someone").Sign()
		}), want: ReasonAmbiguousIdentity},
		{name: "sad/unmapped user", req: tok(func(f *fixture) string { return f.is.User(otherEmail).Sign() }), want: ReasonUnmappedPrincipal,
			who: tenant.Principal{Kind: tenant.KindUser, Subject: otherEmail}},
		{name: "sad/unmapped service token", req: tok(func(f *fixture) string { return f.is.Service(otherCN).Sign() }), want: ReasonUnmappedPrincipal,
			who: tenant.Principal{Kind: tenant.KindService, Subject: otherCN}},
		{name: "sad/user named by a service token's client id", req: tok(func(f *fixture) string {
			return f.is.Service(userEmail).Sign()
		}), want: ReasonUnmappedPrincipal, who: tenant.Principal{Kind: tenant.KindService, Subject: userEmail}},
		{name: "sad/no header", req: tok(func(*fixture) string { return "" }), want: ReasonMissingToken},
		{name: "sad/empty header", req: func(*fixture) *http.Request {
			r := request("")
			r.Header[Header] = []string{""}
			return r
		}, want: ReasonMissingToken},
		{name: "sad/token only in the CF_Authorization cookie", req: func(f *fixture) *http.Request {
			r := request("")
			r.AddCookie(&http.Cookie{Name: "CF_Authorization", Value: f.is.User(userEmail).Sign()}) //nolint:gosec // G124: a request cookie built to prove the cookie is ignored
			return r
		}, want: ReasonMissingToken},
		{name: "sad/token only in Authorization", req: func(f *fixture) *http.Request {
			r := request("")
			r.Header.Set("Authorization", "Bearer "+f.is.User(userEmail).Sign())
			return r
		}, want: ReasonMissingToken},
		{name: "sad/token only in a query parameter", req: func(f *fixture) *http.Request {
			r := request("")
			r.URL.RawQuery = "token=" + f.is.User(userEmail).Sign()
			return r
		}, want: ReasonMissingToken},
		{name: "raging/two header values", req: func(f *fixture) *http.Request {
			r := request(f.is.User(userEmail).Sign())
			r.Header.Add(Header, f.is.User(userEmail).Sign())
			return r
		}, want: ReasonMalformedToken},
		{name: "raging/a valid header beside a bad one", req: func(f *fixture) *http.Request {
			r := request("garbage")
			r.Header.Add(Header, f.is.User(userEmail).Sign())
			return r
		}, want: ReasonMalformedToken},
		{name: "raging/oversized token", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).Claim("padding", strings.Repeat("x", MaxTokenBytes)).Sign()
		}), want: ReasonOversizedToken},
		{name: "edge/token of exactly the size cap is parsed", req: tok(func(*fixture) string {
			return strings.Repeat("a", MaxTokenBytes)
		}), want: ReasonMalformedToken},
		{name: "raging/tampered payload", req: tok(func(f *fixture) string {
			b := f.is.User(otherEmail)
			c := b.Claims()
			c["email"] = userEmail
			return accesstest.TamperPayload(b.Sign(), c)
		}), want: ReasonBadSignature},
		{name: "raging/tampered signature", req: tok(func(f *fixture) string {
			return accesstest.TamperSignature(f.is.User(userEmail).Sign())
		}), want: ReasonBadSignature},
		{name: "raging/signed by a key the issuer never served", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).SignWith(accesstest.RSAKey(2)).Sign()
		}), want: ReasonBadSignature},
		{name: "raging/empty signature", req: tok(func(f *fixture) string {
			s := accesstest.Segments(f.is.User(userEmail).Sign())
			return s[0] + "." + s[1] + "."
		}), want: ReasonBadSignature},
		{name: "raging/alg none", req: tok(func(f *fixture) string { return f.is.User(userEmail).Alg(accesstest.AlgNone).Sign() }), want: ReasonBadAlgorithm},
		{name: "raging/alg None in another case", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).Alg(accesstest.AlgNone).Header("alg", "None").Sign()
		}), want: ReasonBadAlgorithm},
		{name: "raging/HS256 signed with the public key", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).HS256WithPublicKey().Sign()
		}), want: ReasonBadAlgorithm},
		{name: "raging/ES256", req: tok(func(f *fixture) string { return f.is.User(userEmail).Alg(accesstest.AlgES256).Sign() }), want: ReasonBadAlgorithm},
		{name: "raging/PS256", req: tok(func(f *fixture) string { return f.is.User(userEmail).Alg(accesstest.AlgPS256).Sign() }), want: ReasonBadAlgorithm},
		{name: "raging/RS384", req: tok(func(f *fixture) string { return f.is.User(userEmail).Alg(accesstest.AlgRS384).Sign() }), want: ReasonBadAlgorithm},
		{name: "raging/no alg", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).WithoutHeader("alg").Sign()
		}), want: ReasonBadAlgorithm},
		{name: "raging/not a JWT", req: tok(func(*fixture) string { return "not-a-token" }), want: ReasonMalformedToken},
		{name: "raging/four segments", req: tok(func(f *fixture) string { return f.is.User(userEmail).Sign() + ".x" }), want: ReasonMalformedToken},
		{name: "raging/header is not JSON", req: tok(func(f *fixture) string {
			s := accesstest.Segments(f.is.User(userEmail).Sign())
			return b64("{not json") + "." + s[1] + "." + s[2]
		}), want: ReasonMalformedToken},
		{name: "raging/padded base64", req: tok(func(f *fixture) string {
			s := accesstest.Segments(f.is.User(userEmail).Sign())
			return s[0] + "==." + s[1] + "." + s[2]
		}), want: ReasonMalformedToken},
		{name: "raging/claims are a JSON array", req: tok(func(f *fixture) string { return signedPayload(f, `[1,2]`) }), want: ReasonMalformedToken},
		{name: "raging/claims are not JSON", req: tok(func(f *fixture) string { return signedPayload(f, `email=x`) }), want: ReasonMalformedToken},
		{name: "raging/aud is a number", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).Claim("aud", 42).Sign()
		}), want: ReasonMalformedToken},
		{name: "raging/exp is a string", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).Claim("exp", "tomorrow").Sign()
		}), want: ReasonMalformedToken},
		{name: "raging/email is an object", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).Claim("email", map[string]string{"a": "b"}).Sign()
		}), want: ReasonMalformedToken},
		{name: "raging/email with a Kelvin sign", req: tok(func(f *fixture) string {
			return f.is.User("operator@example.net\u212a").Sign()
		}), want: ReasonUnmappedPrincipal},
		{name: "raging/email with a control character", req: tok(func(f *fixture) string {
			return f.is.User("operator@example.net\n").Sign()
		}), want: ReasonUnmappedPrincipal},
		{name: "corner/expired and wrong audience", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).Claim("aud", "x").Times(now(f), -600, -600, -60).Sign()
		}), want: ReasonWrongAudience},
		{name: "raging/duplicate email member", req: tok(func(f *fixture) string {
			return signedPayload(f, userPayload(f, `"email":"`+otherEmail+`","email":"`+userEmail+`"`))
		}), want: ReasonMalformedToken},
		{name: "raging/duplicate aud member", req: tok(func(f *fixture) string {
			return signedPayload(f, userPayload(f, `"email":"`+userEmail+`","aud":["x"]`))
		}), want: ReasonMalformedToken},
		{name: "raging/Email in another case alone", req: tok(func(f *fixture) string {
			return signedPayload(f, userPayload(f, `"Email":"`+userEmail+`"`))
		}), want: ReasonMissingClaim},
		{name: "raging/email for a stranger and EMAIL for the operator", req: tok(func(f *fixture) string {
			return signedPayload(f, userPayload(f, `"email":"`+otherEmail+`","EMAIL":"`+userEmail+`"`))
		}), want: ReasonUnmappedPrincipal, who: tenant.Principal{Kind: tenant.KindUser, Subject: otherEmail}},
		{name: "raging/ISS in another case does not stand in for iss", req: tok(func(f *fixture) string {
			b, _ := json.Marshal(f.is.User(userEmail).Without("iss").Claims())
			return signedPayload(f, strings.TrimSuffix(string(b), "}")+`,"ISS":"https://`+accesstest.TeamDomain+`"}`)
		}), want: ReasonWrongIssuer},
		{name: "raging/embedded jwk header with the attacker's key", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).Header("jwk", accesstest.RSAJWK("key-0", &accesstest.RSAKey(2).PublicKey)).SignWith(accesstest.RSAKey(2)).Sign()
		}), want: ReasonBadSignature},
		{name: "raging/embedded jwk header without a kid", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).WithoutHeader("kid").Header("jwk", accesstest.RSAJWK("x", &accesstest.RSAKey(2).PublicKey)).SignWith(accesstest.RSAKey(2)).Sign()
		}), want: ReasonUnknownKey},
		{name: "raging/jku header to the attacker's key set", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).Header("jku", "https://example.net/certs").SignWith(accesstest.RSAKey(2)).Sign()
		}), want: ReasonBadSignature},
		{name: "raging/x5c header", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).Header("x5c", []string{"AAAA"}).SignWith(accesstest.RSAKey(2)).Sign()
		}), want: ReasonMalformedToken},
		{name: "raging/unknown crit header", req: tok(func(f *fixture) string {
			return f.is.User(userEmail).Header("crit", []string{"x-yasshm-test"}).Header("x-yasshm-test", 1).Sign()
		}), want: ReasonBadSignature},
		{name: "raging/JWS JSON serialization", req: tok(func(f *fixture) string {
			sg := accesstest.Segments(f.is.User(userEmail).Sign())
			return `{"payload":"` + sg[1] + `","protected":"` + sg[0] + `","signature":"` + sg[2] + `"}`
		}), want: ReasonMalformedToken},
		{name: "raging/five segments", req: tok(func(f *fixture) string { return f.is.User(userEmail).Sign() + ".x.y" }), want: ReasonMalformedToken},
		{name: "corner/alg none with a valid RS256 signature appended", req: tok(func(f *fixture) string {
			good := accesstest.Segments(f.is.User(userEmail).Sign())
			bad := accesstest.Segments(f.is.User(userEmail).Alg(accesstest.AlgNone).Sign())
			return bad[0] + "." + good[1] + "." + good[2]
		}), want: ReasonBadAlgorithm},
	}
}

var requestIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestTokenClasses(t *testing.T) {
	f := newFixture(t)
	f.load(t)
	for _, c := range tokenClasses() {
		t.Run(c.name, func(t *testing.T) {
			p, err := f.a.Authenticate(c.req(f))
			if got := reasonOf(t, err); got != c.want {
				t.Fatalf("reason = %q, want %q", got, c.want)
			}
			if c.want == (Reason{}) && p != c.who {
				t.Fatalf("principal = %+v, want %+v", p, c.who)
			}
			if err != nil && err.Error() != "auth: "+c.want.String() {
				t.Fatalf("error text = %q", err.Error())
			}
			if c.want == ReasonUnmappedPrincipal {
				var ae *Error
				if !errors.As(err, &ae) || ae.who != c.who {
					t.Fatalf("recorded principal = %+v, want %+v", ae.who, c.who)
				}
			}
		})
	}
}

// TestTokenClassesThroughMiddleware sends every class through Middleware
// with a real audit log and checks the status, the body and the event.
func TestTokenClassesThroughMiddleware(t *testing.T) {
	f := newFixture(t)
	f.load(t)
	log, path := auditLog(t)
	h := &okHandler{}
	srv := Middleware(f.a, log)(h)
	for i, c := range tokenClasses() {
		t.Run(c.name, func(t *testing.T) {
			before := h.count()
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, c.req(f))
			events := readAudit(t, path)
			if len(events) != i+1 {
				t.Fatalf("%d audit events, want %d", len(events), i+1)
			}
			ev := events[i]
			if id, _ := ev["request_id"].(string); !requestIDPattern.MatchString(id) {
				t.Fatalf("request_id = %v", ev["request_id"])
			}
			if c.want == (Reason{}) {
				if rec.Code != http.StatusOK || h.count() != before+1 {
					t.Fatalf("status %d, handler calls %d", rec.Code, h.count()-before)
				}
				if ev["event"] != "auth.accepted" || ev["principal"] != c.who.String() || ev["tenant"] != string(c.who.Tenant) {
					t.Fatalf("event = %v", ev)
				}
				if h.p != c.who || h.id != ev["request_id"] {
					t.Fatalf("context principal %+v id %q, event id %v", h.p, h.id, ev["request_id"])
				}
				return
			}
			if h.count() != before {
				t.Fatal("the handler ran for a rejected request")
			}
			if rec.Code != c.want.Status() {
				t.Fatalf("status = %d, want %d", rec.Code, c.want.Status())
			}
			wantBody := map[int]string{401: bodyUnauthorized, 403: bodyForbidden, 503: bodyUnavailable}[rec.Code]
			if body, _ := io.ReadAll(rec.Body); string(body) != wantBody {
				t.Fatalf("body = %q", body)
			}
			if ev["event"] != "auth.rejected" || ev["reason"] != c.want.String() || ev["decision"] != "deny" {
				t.Fatalf("event = %v", ev)
			}
			if _, has := ev["tenant"]; has {
				t.Fatalf("a rejection carries a tenant: %v", ev)
			}
			wantWho, _ := ev["principal"].(string)
			if c.who.Kind != 0 && wantWho != c.who.String() {
				t.Fatalf("principal = %q, want %q", wantWho, c.who.String())
			}
			if c.who.Kind == 0 && wantWho != "" {
				t.Fatalf("principal recorded for %s: %q", c.want, wantWho)
			}
		})
	}
}

func TestReasonStatus(t *testing.T) {
	all := []Reason{ReasonMissingToken, ReasonMalformedToken, ReasonOversizedToken, ReasonBadAlgorithm, ReasonUnknownKey,
		ReasonBadSignature, ReasonWrongIssuer, ReasonWrongAudience, ReasonExpired, ReasonNotYetValid, ReasonIssuedInFuture,
		ReasonMissingClaim, ReasonAmbiguousIdentity, ReasonUnmappedPrincipal, ReasonKeysUnavailable, ReasonInvalidToken}
	seen := map[string]bool{}
	for _, r := range all {
		t.Run("happy/"+r.String(), func(t *testing.T) {
			if seen[r.String()] || r.String() == "" || r.String() != r.Audit().String() {
				t.Fatalf("reason %q duplicated, empty or differs from its audit name", r)
			}
			seen[r.String()] = true
			want := http.StatusUnauthorized
			switch r {
			case ReasonUnmappedPrincipal:
				want = http.StatusForbidden
			case ReasonKeysUnavailable:
				want = http.StatusServiceUnavailable
			}
			if r.Status() != want {
				t.Fatalf("status = %d, want %d", r.Status(), want)
			}
		})
	}
	t.Run("happy/errors.Is matches by reason", func(t *testing.T) {
		if !reject(ReasonUnmappedPrincipal).Is(ErrUnmapped) || reject(ReasonExpired).Is(ErrUnmapped) || reject(ReasonExpired).Is(io.EOF) {
			t.Fatal("Is is wrong")
		}
	})
}

// TestStatusText checks what /healthz will show.
func TestStatusText(t *testing.T) {
	f := newFixture(t)
	t.Run("sad/not loaded", func(t *testing.T) {
		if f.a.Ready() || f.a.Status() != "key set not loaded" || !strings.Contains(f.a.String(), "not loaded") {
			t.Fatal(f.a.Status())
		}
	})
	f.load(t)
	t.Run("happy/loaded", func(t *testing.T) {
		if !f.a.Ready() || f.a.Status() != "key set loaded" || strings.Contains(f.a.String(), "127.0.0.1") {
			t.Fatal(f.a.Status(), f.a.String())
		}
	})
}

func TestResponseHeaders(t *testing.T) {
	f := newFixture(t)
	log, path := auditLog(t)
	srv := Middleware(f.a, log)(&okHandler{})
	t.Run("sad/keys_unavailable: 503 with Retry-After and its event", func(t *testing.T) {
		f.is.Down()
		if err := f.a.keys.refresh(context.Background()); err == nil {
			t.Fatal("a fetch from a stopped issuer succeeded")
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, request(f.is.User(userEmail).Sign()))
		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "60" || rec.Body.String() != bodyUnavailable {
			t.Fatalf("status = %d, headers %v", rec.Code, rec.Header())
		}
		ev := readAudit(t, path)
		if len(ev) != 1 || ev[0]["event"] != "auth.rejected" || ev[0]["reason"] != "keys_unavailable" {
			t.Fatalf("events = %v", ev)
		}
	})
	t.Run("sad/401 is JSON and not cached", func(t *testing.T) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, request(""))
		h := rec.Header()
		if rec.Code != 401 || h.Get("Content-Type") != "application/json" || h.Get("Cache-Control") != "no-store" || h.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%d %v", rec.Code, h)
		}
		if h.Get("Retry-After") != "" || h.Get("Www-Authenticate") != "" {
			t.Fatalf("unexpected headers %v", h)
		}
	})
}
