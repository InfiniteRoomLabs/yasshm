package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/audit"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/accesstest"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/syncbuf"
)

// brokenLog refuses every write.
type brokenLog struct{ writes int }

func (b *brokenLog) Write(audit.Event) error { b.writes++; return errors.New("disk full") }

// fixedAuth returns a fixed answer.
type fixedAuth struct {
	p   tenant.Principal
	err error
}

func (f fixedAuth) Authenticate(*http.Request) (tenant.Principal, error) { return f.p, f.err }

func TestMiddlewareAuditFailure(t *testing.T) {
	logs := &syncbuf.Buffer{}
	opt := WithMiddlewareLogger(slog.New(slog.NewJSONHandler(logs, nil)))
	t.Run("raging/an accepted request whose audit write fails is refused and next never runs", func(t *testing.T) {
		h := &okHandler{}
		bl := &brokenLog{}
		rec := httptest.NewRecorder()
		Middleware(fixedAuth{p: user}, bl, opt)(h).ServeHTTP(rec, request("tok"))
		if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != bodyUnavailable || h.count() != 0 || bl.writes != 1 {
			t.Fatalf("status %d body %q calls %d writes %d", rec.Code, rec.Body.String(), h.count(), bl.writes)
		}
		if rec.Header().Get("Retry-After") != "" {
			t.Fatal("Retry-After on an audit failure: the log does not recover without a restart")
		}
		if !strings.Contains(logs.String(), "request refused") || !strings.Contains(logs.String(), "disk full") {
			t.Fatalf("log = %s", logs.String())
		}
	})
	t.Run("raging/a rejection is still answered when the audit log is broken", func(t *testing.T) {
		for _, r := range []Reason{ReasonMissingToken, ReasonUnmappedPrincipal, ReasonKeysUnavailable} {
			rec := httptest.NewRecorder()
			Middleware(fixedAuth{err: reject(r)}, &brokenLog{}, opt)(&okHandler{}).ServeHTTP(rec, request(""))
			if rec.Code != r.Status() {
				t.Fatalf("%s: status %d", r, rec.Code)
			}
		}
		if strings.Count(logs.String(), "audit write failed for a rejected request") != 3 || strings.Count(logs.String(), "disk full") != 4 {
			t.Fatalf("log = %s", logs.String())
		}
	})
}

func TestMiddlewareForeignError(t *testing.T) {
	log, path := auditLog(t)
	rec := httptest.NewRecorder()
	Middleware(fixedAuth{err: errors.New("something odd")}, log)(&okHandler{}).ServeHTTP(rec, request("x"))
	ev := readAudit(t, path)
	if rec.Code != http.StatusUnauthorized || len(ev) != 1 || ev[0]["reason"] != "invalid_token" {
		t.Fatalf("sad/a non-*Error: status %d events %v", rec.Code, ev)
	}
}

func TestRequestIDs(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		id := newRequestID()
		if !requestIDPattern.MatchString(id) || seen[id] {
			t.Fatalf("happy/request id %q is malformed or repeated", id)
		}
		seen[id] = true
	}
}

func TestContextHelpers(t *testing.T) {
	t.Run("sad/an empty context has nothing", func(t *testing.T) {
		if _, ok := PrincipalFrom(context.Background()); ok {
			t.Fatal("principal")
		}
		if _, ok := RequestIDFrom(context.Background()); ok {
			t.Fatal("request id")
		}
	})
	t.Run("raging/an incomplete principal is not a principal", func(t *testing.T) {
		for _, p := range []tenant.Principal{{Kind: tenant.KindUser, Subject: "a"}, {Tenant: "t", Subject: "a"}, {Tenant: "t", Kind: tenant.KindUser}, {Tenant: "t", Kind: 9, Subject: "a"}} {
			ctx := context.WithValue(context.Background(), principalKey{}, p)
			if _, ok := PrincipalFrom(ctx); ok {
				t.Fatalf("%+v accepted", p)
			}
		}
	})
	t.Run("sad/MustPrincipal panics without a principal", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("no panic")
			}
		}()
		MustPrincipal(context.Background())
	})
	t.Run("sad/a handler reached outside the middleware panics in tests", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("no panic")
			}
		}()
		(&okHandler{}).ServeHTTP(httptest.NewRecorder(), request(""))
	})
	t.Run("sad/Require answers 500 and does not call next", func(t *testing.T) {
		h := &okHandler{}
		rec := httptest.NewRecorder()
		Require(h).ServeHTTP(rec, request(""))
		if rec.Code != http.StatusInternalServerError || rec.Body.String() != bodyInternal || h.count() != 0 {
			t.Fatalf("status %d calls %d", rec.Code, h.count())
		}
	})
	t.Run("happy/Require passes a request behind the middleware", func(t *testing.T) {
		log, _ := auditLog(t)
		h := &okHandler{}
		rec := httptest.NewRecorder()
		Middleware(fixedAuth{p: service}, log)(Require(h)).ServeHTTP(rec, request("x"))
		if rec.Code != http.StatusOK || h.count() != 1 || h.p != service || !requestIDPattern.MatchString(h.id) {
			t.Fatalf("status %d calls %d p %+v id %q", rec.Code, h.count(), h.p, h.id)
		}
	})
}

// TestIncompletePrincipal: a nil error with an incomplete principal is not
// trusted.
func TestIncompletePrincipal(t *testing.T) {
	for _, p := range []tenant.Principal{{}, {Kind: tenant.KindUser, Subject: "a"}, {Tenant: "t", Subject: "a"}, {Tenant: "t", Kind: tenant.KindUser}, {Tenant: "t", Kind: 9, Subject: "a"}} {
		t.Run("raging/"+p.String(), func(t *testing.T) {
			log, path := auditLog(t)
			h := &okHandler{}
			rec := httptest.NewRecorder()
			Middleware(fixedAuth{p: p}, log)(h).ServeHTTP(rec, request("x"))
			ev := readAudit(t, path)
			if rec.Code != http.StatusInternalServerError || rec.Body.String() != bodyInternal || h.count() != 0 {
				t.Fatalf("status %d calls %d", rec.Code, h.count())
			}
			if len(ev) != 1 || ev[0]["event"] != "auth.rejected" || ev[0]["reason"] != "invalid_token" {
				t.Fatalf("events = %v", ev)
			}
		})
	}
}

// TestRejectionAllowance: rejections made before a signature is verified
// are written one by one only RejectAllowance times a minute; the rest are
// summarised once. Rejections of verified tokens are never suppressed, and
// every request is answered.
func TestRejectionAllowance(t *testing.T) {
	f := newFixture(t)
	f.load(t)
	log, path := auditLog(t)
	g := NewGuard(f.a, log, WithMiddlewareClock(f.clk.Now))
	srv := g.Wrap(&okHandler{})
	send := func(t *testing.T, r *http.Request, want int) {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		if rec.Code != want {
			t.Fatalf("status %d, want %d", rec.Code, want)
		}
	}
	count := func(events []map[string]any, reason string) (n int) {
		for _, e := range events {
			if e["reason"] == reason {
				n++
			}
		}
		return n
	}
	t.Run("raging/1000 pre-signature rejections in a minute write 60 events", func(t *testing.T) {
		for i := range 1000 {
			if i%2 == 0 {
				send(t, request(""), http.StatusUnauthorized)
			} else {
				send(t, request("not-a-token"), http.StatusUnauthorized)
			}
		}
		ev := readAudit(t, path)
		if len(ev) != RejectAllowance || count(ev, "suppressed") != 0 {
			t.Fatalf("%d events", len(ev))
		}
	})
	t.Run("raging/verified-token rejections are never suppressed", func(t *testing.T) {
		send(t, request(f.is.User(otherEmail).Sign()), http.StatusForbidden)
		send(t, request(f.is.User(userEmail).Claim("aud", "x").Sign()), http.StatusUnauthorized)
		send(t, request(accesstest.TamperSignature(f.is.User(userEmail).Sign())), http.StatusUnauthorized)
		send(t, request(f.is.User(userEmail).Sign()), http.StatusOK)
		ev := readAudit(t, path)
		if len(ev) != RejectAllowance+4 || count(ev, "unmapped_principal") != 1 || count(ev, "wrong_audience") != 1 || count(ev, "bad_signature") != 1 {
			t.Fatalf("%d events", len(ev))
		}
	})
	t.Run("raging/the next minute writes one summary of 940 and starts fresh", func(t *testing.T) {
		f.clk.Add(time.Minute)
		send(t, request(""), http.StatusUnauthorized)
		ev := readAudit(t, path)
		if len(ev) != RejectAllowance+6 {
			t.Fatalf("%d events", len(ev))
		}
		sum := ev[RejectAllowance+4]
		if sum["reason"] != "suppressed" || sum["count"] != float64(940) || sum["event"] != "auth.rejected" || ev[RejectAllowance+5]["reason"] != "missing_token" {
			t.Fatalf("summary %v, then %v", sum, ev[RejectAllowance+5])
		}
		for range RejectAllowance - 1 {
			send(t, request(""), http.StatusUnauthorized)
		}
		if n := len(readAudit(t, path)); n != 2*RejectAllowance+5 {
			t.Fatalf("%d events: the new window did not allow %d", n, RejectAllowance)
		}
	})
	t.Run("raging/Close writes a pending summary once", func(t *testing.T) {
		before := len(readAudit(t, path))
		send(t, request(""), http.StatusUnauthorized)
		send(t, request(""), http.StatusUnauthorized)
		g.Close()
		g.Close()
		ev := readAudit(t, path)
		if len(ev) != before+1 || ev[before]["reason"] != "suppressed" || ev[before]["count"] != float64(2) {
			t.Fatalf("events after Close: %v", ev[before:])
		}
	})
	t.Run("sad/a summary write that fails is logged", func(t *testing.T) {
		logs := &syncbuf.Buffer{}
		bg := NewGuard(fixedAuth{err: reject(ReasonMissingToken)}, &brokenLog{}, WithMiddlewareLogger(slog.New(slog.NewJSONHandler(logs, nil))))
		for range RejectAllowance + 1 {
			bg.Wrap(&okHandler{}).ServeHTTP(httptest.NewRecorder(), request(""))
		}
		bg.Close()
		if !strings.Contains(logs.String(), "suppressed-rejections summary") {
			t.Fatalf("log = %s", logs.String())
		}
	})
	t.Run("happy/the pre-signature set", func(t *testing.T) {
		pre := map[Reason]bool{ReasonMissingToken: true, ReasonMalformedToken: true, ReasonOversizedToken: true, ReasonBadAlgorithm: true, ReasonUnknownKey: true, ReasonKeysUnavailable: true}
		for _, r := range []Reason{ReasonMissingToken, ReasonMalformedToken, ReasonOversizedToken, ReasonBadAlgorithm, ReasonUnknownKey,
			ReasonBadSignature, ReasonWrongIssuer, ReasonWrongAudience, ReasonExpired, ReasonNotYetValid, ReasonIssuedInFuture,
			ReasonMissingClaim, ReasonAmbiguousIdentity, ReasonUnmappedPrincipal, ReasonKeysUnavailable, ReasonInvalidToken} {
			if r.preSignature() != pre[r] {
				t.Errorf("%s: preSignature = %t", r, r.preSignature())
			}
		}
	})
}
