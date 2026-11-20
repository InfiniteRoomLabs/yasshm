package auth

import (
	"errors"
	"fmt"
	"strings"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// maxSubjectBytes caps a subject that may map, and that may be recorded in
// an unmapped rejection. An email address is at most 254 bytes.
const maxSubjectBytes = 320

// identity holds the verified identity claims of a token (F7).
type identity struct {
	// Email is the email claim of a user token.
	Email string
	// CommonName is the common_name claim of a service token (its client id).
	CommonName string
	// Subject is the sub claim; empty in a service token.
	Subject string
}

// resolve maps verified identity claims to a principal through the
// tenants' principals lists. It is a pure function:
//
//   - a non-empty email and no common_name is a user (KindUser, subject = the
//     email); a non-empty common_name, an empty sub and no email is a service
//     token (KindService, subject = the common name);
//   - both email and common_name, or a common_name with a non-empty sub, is
//     ReasonAmbiguousIdentity; neither is ReasonMissingClaim;
//   - a subject that is not printable ASCII (a control character, a byte
//     at or above 0x80) or longer than maxSubjectBytes maps to no tenant;
//   - exactly one tenant must admit the subject (tenant.Tenant.Admits);
//     none, or more than one, is ReasonUnmappedPrincipal.
//
// For an unmapped subject that is printable ASCII and short enough, the
// returned error carries the kind and subject (no tenant) for the audit
// event; otherwise it carries nothing.
func resolve(id identity, tenants []tenant.Tenant) (tenant.Principal, error) {
	var kind tenant.PrincipalKind
	var subject string
	switch {
	case id.Email != "" && id.CommonName != "":
		return tenant.Principal{}, reject(ReasonAmbiguousIdentity)
	case id.Email != "":
		kind, subject = tenant.KindUser, id.Email
	case id.CommonName != "":
		if id.Subject != "" {
			return tenant.Principal{}, reject(ReasonAmbiguousIdentity)
		}
		kind, subject = tenant.KindService, id.CommonName
	default:
		return tenant.Principal{}, reject(ReasonMissingClaim)
	}
	if !printable(subject) {
		return tenant.Principal{}, reject(ReasonUnmappedPrincipal)
	}
	var found *tenant.Tenant
	matches := 0
	for i := range tenants {
		if tenants[i].Admits(kind, subject) {
			found = &tenants[i]
			matches++
		}
	}
	if matches != 1 || found.ID == "" {
		return tenant.Principal{}, &Error{reason: ReasonUnmappedPrincipal, who: tenant.Principal{Kind: kind, Subject: subject}}
	}
	return tenant.Principal{Tenant: found.ID, Kind: kind, Subject: subject}, nil
}

// printable reports whether s is 1 to maxSubjectBytes bytes of printable
// ASCII (0x21 to 0x7e): no space, no control character, nothing non-ASCII.
func printable(s string) bool {
	if s == "" || len(s) > maxSubjectBytes {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] <= 0x20 || s[i] >= 0x7f {
			return false
		}
	}
	return true
}

// checkTenants refuses a tenant list the authenticator cannot map
// unambiguously: no tenant, a tenant without an id, a duplicate id, or two
// tenants whose principals lists admit the same caller (the same email,
// compared as tenant.PrincipalMatch does, or the same common name). The
// config loader allows exactly one tenant, so the last case is possible only
// for tenants built in code.
func checkTenants(tenants []tenant.Tenant) error {
	if len(tenants) == 0 {
		return errors.New("auth: no tenants")
	}
	seen := map[tenant.TenantID]bool{}
	for i := range tenants {
		t := &tenants[i]
		if t.ID == "" {
			return fmt.Errorf("auth: tenant %d has no id", i)
		}
		if seen[t.ID] {
			return fmt.Errorf("auth: tenant %q appears twice", t.ID)
		}
		seen[t.ID] = true
		for j := range tenants[:i] {
			if overlap(t.Principals, tenants[j].Principals) {
				return fmt.Errorf("auth: tenants %q and %q admit the same principal", tenants[j].ID, t.ID)
			}
		}
	}
	return nil
}

func overlap(a, b []tenant.PrincipalMatch) bool {
	for _, x := range a {
		for _, y := range b {
			if x.Email != "" && y.Email != "" && strings.EqualFold(x.Email, y.Email) {
				return true
			}
			if x.CommonName != "" && x.CommonName == y.CommonName {
				return true
			}
		}
	}
	return false
}
