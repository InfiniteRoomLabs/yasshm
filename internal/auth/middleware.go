package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/InfiniteRoomLabs/yasshm/internal/audit"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// AuditWriter is where the middleware records auth.accepted and
// auth.rejected; *audit.Logger is one.
type AuditWriter interface {
	Write(ev audit.Event) error
}

// The fixed response bodies. None says which check failed: the reason is in
// the audit event only.
const (
	bodyUnauthorized = `{"error":"unauthorized"}`
	bodyForbidden    = `{"error":"forbidden"}`
	bodyUnavailable  = `{"error":"unavailable"}`
	bodyInternal     = `{"error":"internal"}`
)

// MiddlewareOption configures Middleware.
type MiddlewareOption func(*middleware)

// WithMiddlewareLogger sets the logger for audit write failures; the
// default discards.
func WithMiddlewareLogger(l *slog.Logger) MiddlewareOption {
	return func(m *middleware) { m.log = l }
}

type middleware struct {
	a   Authenticator
	w   AuditWriter
	log *slog.Logger
}

// Middleware authenticates every request before next sees it. For each
// request it makes a request id (128 random bits, lower-case hex) and:
//
//   - on a rejection writes auth.rejected (the reason, the request id, and
//     for an unmapped principal its kind and subject) and answers 401, 403
//     or 503 (with Retry-After) with a fixed JSON body; the answer is sent
//     even when the audit write fails;
//   - on success writes auth.accepted BEFORE calling next (spec D12) and, if
//     that write fails, answers 503 and does not call next; otherwise it
//     calls next with the principal and the request id in the context
//     (PrincipalFrom, RequestIDFrom).
func Middleware(a Authenticator, w AuditWriter, opts ...MiddlewareOption) func(http.Handler) http.Handler {
	m := &middleware{a: a, w: w, log: slog.New(slog.DiscardHandler)}
	for _, o := range opts {
		o(m)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			m.serve(next, rw, r)
		})
	}
}

func (m *middleware) serve(next http.Handler, rw http.ResponseWriter, r *http.Request) {
	id := newRequestID()
	p, err := m.a.Authenticate(r)
	if err != nil {
		reason, who := ReasonInvalidToken, tenant.Principal{}
		var ae *Error
		if errors.As(err, &ae) {
			reason, who = ae.reason, ae.who
		}
		if werr := m.w.Write(audit.AuthRejected(id, reason.Audit(), who)); werr != nil {
			m.log.Error("auth: audit write failed for a rejected request", "request_id", id, "reason", reason.String())
		}
		respond(rw, reason.Status())
		return
	}
	if werr := m.w.Write(audit.AuthAccepted(id, p)); werr != nil {
		m.log.Error("auth: audit write failed; request refused", "request_id", id)
		respond(rw, http.StatusServiceUnavailable)
		return
	}
	ctx := context.WithValue(r.Context(), principalKey{}, p)
	ctx = context.WithValue(ctx, requestIDKey{}, id)
	next.ServeHTTP(rw, r.WithContext(ctx))
}

// respond writes one of the fixed bodies for status.
func respond(rw http.ResponseWriter, status int) {
	body := bodyUnauthorized
	switch status {
	case http.StatusForbidden:
		body = bodyForbidden
	case http.StatusServiceUnavailable:
		body = bodyUnavailable
		rw.Header().Set("Retry-After", strconv.Itoa(int(MinRefetchInterval.Seconds())))
	case http.StatusInternalServerError:
		body = bodyInternal
	}
	h := rw.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	rw.WriteHeader(status)
	_, _ = rw.Write([]byte(body))
}

// newRequestID returns 128 random bits as 32 lower-case hex digits.
// crypto/rand.Read does not fail (it crashes the program if the system's
// source does).
func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

type principalKey struct{}

type requestIDKey struct{}

// PrincipalFrom returns the verified principal Middleware put in ctx.
func PrincipalFrom(ctx context.Context) (tenant.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(tenant.Principal)
	return p, ok && p.Tenant != "" && p.Subject != "" && (p.Kind == tenant.KindUser || p.Kind == tenant.KindService)
}

// RequestIDFrom returns the request id Middleware put in ctx.
func RequestIDFrom(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(requestIDKey{}).(string)
	return id, ok && id != ""
}

// MustPrincipal returns the principal in ctx and panics when there is none:
// code that runs only behind Middleware uses it, so a routing mistake fails
// loudly in tests.
func MustPrincipal(ctx context.Context) tenant.Principal {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		panic("auth: no verified principal in the request context (handler mounted outside auth.Middleware)")
	}
	return p
}

// Require wraps a handler that must only run behind Middleware: a request
// without a principal in its context gets 500 and a fixed body, and next
// does not run. It is the production guard against a routing mistake.
func Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if _, ok := PrincipalFrom(r.Context()); !ok {
			respond(rw, http.StatusInternalServerError)
			return
		}
		next.ServeHTTP(rw, r)
	})
}
