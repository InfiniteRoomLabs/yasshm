package auth

import (
	"net/http"

	"github.com/InfiniteRoomLabs/yasshm/internal/audit"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// Reason says why a request was not authenticated. The values are a closed
// set: a reason never carries the token or any other caller text. Each one
// has the same name as the audit.RejectReason it is recorded as.
type Reason struct{ r audit.RejectReason }

// The reasons. Every one is HTTP 401 except ReasonUnmappedPrincipal (403)
// and ReasonKeysUnavailable (503).
var (
	// ReasonMissingToken: no Cf-Access-Jwt-Assertion header, or an empty one.
	ReasonMissingToken = Reason{audit.RejectMissingToken}
	// ReasonMalformedToken: more than one header value, or a token that is
	// not a compact JWS with a JSON header and JSON claims of the right types.
	ReasonMalformedToken = Reason{audit.RejectMalformedToken}
	// ReasonOversizedToken: a token longer than MaxTokenBytes.
	ReasonOversizedToken = Reason{audit.RejectOversizedToken}
	// ReasonBadAlgorithm: an alg other than RS256 (none, HS256, ES256, PS256,
	// RS384 and the rest), refused before any key is looked up.
	ReasonBadAlgorithm = Reason{audit.RejectBadAlgorithm}
	// ReasonUnknownKey: no kid, or a kid that is not in the key set even
	// after the refetch the limiter allows.
	ReasonUnknownKey = Reason{audit.RejectUnknownKey}
	// ReasonBadSignature: the signature does not verify with the named key.
	ReasonBadSignature = Reason{audit.RejectBadSignature}
	// ReasonWrongIssuer: iss is not exactly "https://" + team domain.
	ReasonWrongIssuer = Reason{audit.RejectWrongIssuer}
	// ReasonWrongAudience: aud does not contain the configured audience.
	ReasonWrongAudience = Reason{audit.RejectWrongAudience}
	// ReasonExpired: exp is more than Leeway in the past.
	ReasonExpired = Reason{audit.RejectExpired}
	// ReasonNotYetValid: nbf is more than Leeway in the future.
	ReasonNotYetValid = Reason{audit.RejectNotYetValid}
	// ReasonIssuedInFuture: iat is more than Leeway in the future.
	ReasonIssuedInFuture = Reason{audit.RejectIssuedInFuture}
	// ReasonMissingClaim: exp or iat is absent, nbf is absent from a user
	// token, or the token names neither an email nor a common_name.
	ReasonMissingClaim = Reason{audit.RejectMissingClaim}
	// ReasonAmbiguousIdentity: the token names both an email and a
	// common_name, or a common_name with a non-empty sub.
	ReasonAmbiguousIdentity = Reason{audit.RejectAmbiguousIdentity}
	// ReasonUnmappedPrincipal: a valid token whose subject no tenant admits.
	ReasonUnmappedPrincipal = Reason{audit.RejectUnmappedPrincipal}
	// ReasonKeysUnavailable: no key set has been fetched yet.
	ReasonKeysUnavailable = Reason{audit.RejectKeysUnavailable}
	// ReasonInvalidToken: an Authenticator other than this package's
	// returned an error that is not an *Error; the middleware records it so.
	ReasonInvalidToken = Reason{audit.RejectInvalidToken}
)

// String returns the reason's name, for example "wrong_audience".
func (r Reason) String() string { return r.r.String() }

// Audit returns the audit reason the rejection is recorded with.
func (r Reason) Audit() audit.RejectReason { return r.r }

// Status returns the HTTP status for the reason: 403 for an unmapped
// principal, 503 when no keys are loaded, 401 otherwise.
func (r Reason) Status() int {
	switch r {
	case ReasonUnmappedPrincipal:
		return http.StatusForbidden
	case ReasonKeysUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusUnauthorized
	}
}

// Error is the one error type Authenticate returns. Its text is
// "auth: " + the reason and nothing else: it never wraps a library error,
// the token, a key or a network error.
type Error struct {
	reason Reason
	// who is the verified caller of an unmapped token (kind and subject, no
	// tenant), recorded in auth.rejected; zero for every other reason.
	who tenant.Principal
}

func reject(r Reason) *Error { return &Error{reason: r} }

// Error returns "auth: <reason>".
func (e *Error) Error() string { return "auth: " + e.reason.String() }

// Reason returns the reason.
func (e *Error) Reason() Reason { return e.reason }

// Is matches another *Error with the same reason, so
// errors.Is(err, auth.ErrUnmapped) works.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.reason == e.reason
}

// Sentinels for errors.Is, one per reason a caller is likely to branch on.
var (
	// ErrUnmapped matches an unmapped-principal rejection.
	ErrUnmapped = reject(ReasonUnmappedPrincipal)
	// ErrKeysUnavailable matches a rejection because no keys are loaded.
	ErrKeysUnavailable = reject(ReasonKeysUnavailable)
)
