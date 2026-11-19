// Package tenant holds the domain types every other package shares: tenant,
// principal, access level, server, identity and limits (spec section 5.2).
//
// It imports nothing from this module. Every lookup of a server, command or
// path is scoped by a TenantID: there is no "current tenant" global, and no
// function in the module takes a server id, command id or path without one.
package tenant

import (
	"fmt"
	"strings"
)

// TenantID names a tenant. Every domain object, store row, output path and
// audit event carries one.
type TenantID string //nolint:revive // the name is fixed by spec 5.2 and reads clearly at call sites

// ServerID names a server inside one tenant. Two tenants may use the same
// ServerID for different machines.
type ServerID string

// CommandID names a command record inside one tenant.
type CommandID string

// IdentityID names an SSH identity inside one tenant.
type IdentityID string

// PrincipalKind says whether a principal is a person or a service token.
type PrincipalKind uint8

// The principal kinds. The zero value is not a valid kind.
const (
	// KindUser is a person, identified by an email address.
	KindUser PrincipalKind = iota + 1
	// KindService is a service token, identified by its client id.
	KindService
)

// String returns "user", "service" or "unknown".
func (k PrincipalKind) String() string {
	switch k {
	case KindUser:
		return "user"
	case KindService:
		return "service"
	default:
		return "unknown"
	}
}

// Principal is a verified caller, already mapped to its tenant.
type Principal struct {
	// Tenant is the tenant the principal belongs to.
	Tenant TenantID
	// Kind is user or service.
	Kind PrincipalKind
	// Subject is the email address of a user or the client id of a service
	// token.
	Subject string
}

// String renders the principal as "kind:subject", the form the audit log uses.
func (p Principal) String() string {
	return p.Kind.String() + ":" + p.Subject
}

// AccessLevel is what a server grants and what a tool requires. The levels
// are ordered none < look < full, and the zero value is AccessNone.
type AccessLevel uint8

// The access levels, in order.
const (
	// AccessNone grants nothing. It is the zero value.
	AccessNone AccessLevel = iota
	// AccessLook is read-only by construction of the target account.
	AccessLook
	// AccessFull is reserved: the v0.1 config loader rejects it (spec D5).
	AccessFull
)

var levelNames = [...]string{AccessNone: "none", AccessLook: "look", AccessFull: "full"}

// String returns "none", "look" or "full", or "level(<n>)" for a value
// outside the defined range.
func (l AccessLevel) String() string {
	if int(l) < len(levelNames) {
		return levelNames[l]
	}
	return fmt.Sprintf("level(%d)", uint8(l))
}

// ParseAccessLevel parses "none", "look" or "full" (exact, lower case).
func ParseAccessLevel(s string) (AccessLevel, error) {
	for i, name := range levelNames {
		if s == name {
			return AccessLevel(i), nil
		}
	}
	return AccessNone, fmt.Errorf("unknown access level %q", s)
}

// MarshalText implements encoding.TextMarshaler. It refuses a value outside
// the defined range rather than writing something that cannot be read back.
func (l AccessLevel) MarshalText() ([]byte, error) {
	if int(l) >= len(levelNames) {
		return nil, fmt.Errorf("unknown access level %d", uint8(l))
	}
	return []byte(levelNames[l]), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (l *AccessLevel) UnmarshalText(b []byte) error {
	v, err := ParseAccessLevel(string(b))
	if err != nil {
		return err
	}
	*l = v
	return nil
}

// Permits reports whether a grant of level granted covers a tool that
// requires level required. This is the only place in the module where access
// levels are compared (spec 5.2): enabling a level later changes no caller.
// It fails closed: a level outside none..full on either side permits
// nothing.
func Permits(granted, required AccessLevel) bool {
	if granted > AccessFull || required > AccessFull {
		return false
	}
	return required <= granted
}

// Identity is an SSH login on a target: a user name and the path of its
// private key. It never holds key material; the SSH layer reads the key when
// it connects.
type Identity struct {
	// ID is the identity's name in the tenant's identities map.
	ID IdentityID
	// User is the login name on the target.
	User string
	// PrivateKeyFile is the absolute path of the private key on the gateway.
	PrivateKeyFile string
}

// Server is one SSH target as the operator configured it.
type Server struct {
	// Tenant is the tenant that owns the server.
	Tenant TenantID
	// ID is the server's id inside its tenant.
	ID ServerID
	// Description is free text shown to the model.
	Description string
	// Host is a DNS name or an IP address.
	Host string
	// Port is the SSH port.
	Port int
	// Identity is the one SSH identity this server uses in v0.1. Callers
	// get it through IdentityFor, never directly.
	Identity Identity
	// HostKey is the pinned host key in authorized_keys form ("type base64"),
	// or empty when KnownHostsFile is set.
	HostKey string
	// KnownHostsFile is the absolute path of a known_hosts file, or empty
	// when HostKey is set.
	KnownHostsFile string
	// Access is the highest level the server grants.
	Access AccessLevel
	// Tags are free labels shown to the model.
	Tags []string
}

// IdentityFor returns the identity to connect with for a tool that requires
// level. In v0.1 every server has exactly one identity, so the level does not
// change the answer; this method is the seam for a per-level identity later
// (spec 5.2). Do not inline it.
func (s *Server) IdentityFor(_ AccessLevel) Identity {
	return s.Identity
}

// PrincipalMatch is one entry of a tenant's principals list: exactly one of
// Email (a user) or CommonName (a service token's client id) is set.
type PrincipalMatch struct {
	// Email matches a user principal. It is ASCII and compared without
	// regard to ASCII case only: no Unicode folding, so a look-alike such as
	// the Kelvin sign never matches "k".
	Email string
	// CommonName matches a service token principal, compared exactly.
	CommonName string
}

// Matches reports whether the entry admits a caller of the given kind and
// subject.
func (m PrincipalMatch) Matches(kind PrincipalKind, subject string) bool {
	switch kind {
	case KindUser:
		return m.Email != "" && isASCII(m.Email) && isASCII(subject) && strings.EqualFold(m.Email, subject)
	case KindService:
		return m.CommonName != "" && m.CommonName == subject
	default:
		return false
	}
}

// isASCII reports whether s holds only ASCII bytes. strings.EqualFold on
// two ASCII strings folds ASCII letters only.
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// Policy holds a tenant's operator additions to the built-in guard rails, as
// written in the config. The policy package compiles them.
type Policy struct {
	// DenyCommands are RE2 expressions matched unanchored against a command.
	DenyCommands []string
	// DenyPaths are path globs; see the policy package for how they match.
	DenyPaths []string
}

// Limits are a tenant's numeric limits. The config package owns their
// defaults and allowed ranges.
type Limits struct {
	DefaultWaitSeconds     int64 // how long run_command waits by default
	MaxWaitSeconds         int64 // the longest wait a caller may ask for
	DefaultTimeoutSeconds  int64 // a command's timeout by default
	MaxTimeoutSeconds      int64 // the longest timeout a caller may ask for
	MaxConcurrentCommands  int64 // running commands per tenant
	MaxConcurrentPerServer int64 // running commands per server
	MaxCommandBytes        int64 // the longest command text
	MaxOutputBytes         int64 // stored output per command
	MaxResultBytes         int64 // output bytes in one tool result
	RetentionDays          int64 // how long finished commands are kept
	MaxDataBytes           int64 // total stored output per tenant
	MaxUploadBytes         int64 // the largest upload
	KillGraceSeconds       int64 // TERM to KILL delay
	ConnectTimeoutSeconds  int64 // SSH dial and handshake timeout
	ShutdownGraceSeconds   int64 // how long shutdown waits for commands
}

// Tenant is one tenant: who may act for it, which identities and servers it
// has, its operator guard rails and its limits.
type Tenant struct {
	// ID is the tenant's id.
	ID TenantID
	// Principals lists who maps to this tenant.
	Principals []PrincipalMatch
	// Identities are the tenant's SSH identities by id.
	Identities map[IdentityID]Identity
	// Servers are the tenant's servers, in config order.
	Servers []Server
	// Policy holds the operator's deny lists.
	Policy Policy
	// Limits holds the tenant's limits.
	Limits Limits
}

// Server returns the tenant's server with the given id. A server of another
// tenant is never returned, because only this tenant's servers are searched.
func (t *Tenant) Server(id ServerID) (*Server, bool) {
	for i := range t.Servers {
		if t.Servers[i].ID == id && t.Servers[i].Tenant == t.ID {
			return &t.Servers[i], true
		}
	}
	return nil, false
}

// Admits reports whether a caller of the given kind and subject maps to this
// tenant.
func (t *Tenant) Admits(kind PrincipalKind, subject string) bool {
	for _, m := range t.Principals {
		if m.Matches(kind, subject) {
			return true
		}
	}
	return false
}
