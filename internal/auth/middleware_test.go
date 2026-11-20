package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/InfiniteRoomLabs/yasshm/internal/audit"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
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
	logs := &syncBuffer{}
	opt := WithMiddlewareLogger(slog.New(slog.NewJSONHandler(logs, nil)))
	t.Run("raging/an accepted request whose audit write fails is refused and next never runs", func(t *testing.T) {
		h := &okHandler{}
		bl := &brokenLog{}
		rec := httptest.NewRecorder()
		Middleware(fixedAuth{p: user}, bl, opt)(h).ServeHTTP(rec, request("tok"))
		if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != bodyUnavailable || h.count() != 0 || bl.writes != 1 {
			t.Fatalf("status %d body %q calls %d writes %d", rec.Code, rec.Body.String(), h.count(), bl.writes)
		}
		if !strings.Contains(logs.String(), "request refused") {
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
