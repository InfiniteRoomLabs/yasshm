package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/audit"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// AuditWriter is where the middleware records auth.accepted and
// auth.rejected; *audit.Logger is one.
type AuditWriter interface {
	Write(ev audit.Event) error
}

// RejectAllowance is how many rejections made before a signature was
// verified are written to the audit log one by one in a minute; the rest of
// that minute's are counted and written as one summary event (spec 5.7).
const RejectAllowance = 60

// rejectWindow is the length of the allowance's window.
const rejectWindow = time.Minute

// The fixed response bodies. None says which check failed: the reason is in
// the audit event only.
const (
	bodyUnauthorized = `{"error":"unauthorized"}`
	bodyForbidden    = `{"error":"forbidden"}`
	bodyUnavailable  = `{"error":"unavailable"}`
	bodyInternal     = `{"error":"internal"}`
)

// MiddlewareOption configures NewGuard and Middleware.
type MiddlewareOption func(*Guard)

// WithMiddlewareLogger sets the logger for audit write failures; the
// default discards.
func WithMiddlewareLogger(l *slog.Logger) MiddlewareOption {
	return func(g *Guard) { g.log = l }
}

// WithMiddlewareClock sets the clock of the rejection allowance; the
// default is time.Now.
func WithMiddlewareClock(now func() time.Time) MiddlewareOption {
	return func(g *Guard) { g.now = now }
}

// Guard authenticates every request before the handler it wraps sees it.
// Build it with NewGuard, wrap handlers with Wrap, and Close it at shutdown
// so a pending summary of suppressed rejections is written.
type Guard struct {
	a   Authenticator
	w   AuditWriter
	log *slog.Logger
	now func() time.Time

	mu          sync.Mutex // guards the allowance
	windowStart time.Time
	written     int   // pre-signature rejections written in this window
	suppressed  int64 // pre-signature rejections not written in this window
}

// NewGuard builds a Guard for a and w.
func NewGuard(a Authenticator, w AuditWriter, opts ...MiddlewareOption) *Guard {
	g := &Guard{a: a, w: w, log: slog.New(slog.DiscardHandler), now: time.Now}
	for _, o := range opts {
		o(g)
	}
	return g
}

// Middleware is NewGuard(a, w, opts...).Wrap, for a caller that does not
// need Close: a pending summary is then written at the next window's first
// request only.
func Middleware(a Authenticator, w AuditWriter, opts ...MiddlewareOption) func(http.Handler) http.Handler {
	return NewGuard(a, w, opts...).Wrap
}

// Wrap returns a handler that, for each request, makes a request id (128
// random bits, lower-case hex) and:
//
//   - on a rejection writes auth.rejected (the reason, the request id, and
//     for an unmapped principal its kind and subject) and answers 401, 403
//     or 503 (with Retry-After for keys_unavailable) with a fixed JSON body;
//     the answer is sent even when the audit write fails. A rejection made
//     before a signature was verified is written only within the allowance
//     (RejectAllowance a minute) and counted otherwise;
//   - when the Authenticator returns no error but an incomplete principal
//     (no tenant, no subject or no valid kind), writes auth.rejected
//     (invalid_token) and answers 500;
//   - on success writes auth.accepted BEFORE calling next (spec D12) and, if
//     that write fails, answers 503 and does not call next; otherwise it
//     calls next with the principal and the request id in the context
//     (PrincipalFrom, RequestIDFrom).
func (g *Guard) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		g.serve(next, rw, r)
	})
}

func (g *Guard) serve(next http.Handler, rw http.ResponseWriter, r *http.Request) {
	id := newRequestID()
	g.rollover()
	p, err := g.a.Authenticate(r)
	if err == nil && !complete(p) {
		g.rejected(id, ReasonInvalidToken, tenant.Principal{})
		respond(rw, http.StatusInternalServerError, false)
		return
	}
	if err != nil {
		reason, who := ReasonInvalidToken, tenant.Principal{}
		var ae *Error
		if errors.As(err, &ae) {
			reason, who = ae.reason, ae.who
		}
		if !reason.preSignature() || g.allow() {
			g.rejected(id, reason, who)
		}
		respond(rw, reason.Status(), reason == ReasonKeysUnavailable)
		return
	}
	if werr := g.w.Write(audit.AuthAccepted(id, p)); werr != nil {
		g.log.Error("auth: audit write failed; request refused", "request_id", id, "error", werr.Error())
		respond(rw, http.StatusServiceUnavailable, false)
		return
	}
	ctx := context.WithValue(r.Context(), principalKey{}, p)
	ctx = context.WithValue(ctx, requestIDKey{}, id)
	next.ServeHTTP(rw, r.WithContext(ctx))
}

// rejected writes auth.rejected, logging a failed write.
func (g *Guard) rejected(id string, reason Reason, who tenant.Principal) {
	if werr := g.w.Write(audit.AuthRejected(id, reason.Audit(), who)); werr != nil {
		g.log.Error("auth: audit write failed for a rejected request", "request_id", id, "reason", reason.String(), "error", werr.Error())
	}
}

// allow reports whether one more pre-signature rejection may be written in
// the current window, and counts it as suppressed if not.
func (g *Guard) allow() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.written < RejectAllowance {
		g.written++
		return true
	}
	g.suppressed++
	return false
}

// rollover starts a new window when the current one has ended, writing the
// summary of the ended window's suppressed rejections first.
func (g *Guard) rollover() {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if !g.windowStart.IsZero() && now.Sub(g.windowStart) < rejectWindow {
		return
	}
	g.flushLocked()
	g.windowStart, g.written = now, 0
}

// flushLocked writes the summary event if anything was suppressed. g.mu
// must be held, so the summary is written once and before the next window's
// events.
func (g *Guard) flushLocked() {
	if g.suppressed == 0 {
		return
	}
	n := g.suppressed
	g.suppressed = 0
	if werr := g.w.Write(audit.AuthRejectionsSuppressed(n)); werr != nil {
		g.log.Error("auth: audit write failed for the suppressed-rejections summary", "count", n, "error", werr.Error())
	}
}

// Close writes the summary of rejections suppressed in the current window,
// if any. The Guard keeps working after Close.
func (g *Guard) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.flushLocked()
}

// preSignature reports whether a rejection with this reason is made before
// a signature was verified, so anyone who reaches the listener can cause it.
func (r Reason) preSignature() bool {
	switch r {
	case ReasonMissingToken, ReasonMalformedToken, ReasonOversizedToken, ReasonBadAlgorithm, ReasonUnknownKey, ReasonKeysUnavailable:
		return true
	default:
		return false
	}
}

// respond writes one of the fixed bodies for status; retryAfter adds
// Retry-After (only keys_unavailable: an audit log that failed does not
// recover without a restart, so a retry would not help).
func respond(rw http.ResponseWriter, status int, retryAfter bool) {
	body := bodyUnauthorized
	switch status {
	case http.StatusForbidden:
		body = bodyForbidden
	case http.StatusServiceUnavailable:
		body = bodyUnavailable
	case http.StatusInternalServerError:
		body = bodyInternal
	}
	h := rw.Header()
	if retryAfter {
		h.Set("Retry-After", strconv.Itoa(int(MinRefetchInterval.Seconds())))
	}
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

// complete reports whether p names a tenant, a valid kind and a subject.
func complete(p tenant.Principal) bool {
	return p.Tenant != "" && p.Subject != "" && (p.Kind == tenant.KindUser || p.Kind == tenant.KindService)
}

// PrincipalFrom returns the verified principal Middleware put in ctx.
func PrincipalFrom(ctx context.Context) (tenant.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(tenant.Principal)
	return p, ok && complete(p)
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
			respond(rw, http.StatusInternalServerError, false)
			return
		}
		next.ServeHTTP(rw, r)
	})
}
