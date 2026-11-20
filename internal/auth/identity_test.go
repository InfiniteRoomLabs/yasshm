package auth

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/InfiniteRoomLabs/yasshm/internal/config"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

func TestResolve(t *testing.T) {
	ts := testTenants()
	cases := []struct {
		name string
		id   identity
		want tenant.Principal
		err  Reason
		who  tenant.Principal
	}{
		{name: "happy/user", id: identity{Email: userEmail, Subject: "u1"}, want: user},
		{name: "happy/service token", id: identity{CommonName: serviceCN}, want: service},
		{name: "edge/a user subject is lower-cased", id: identity{Email: "Operator@Example.NET"}, want: user},
		{name: "edge/an unmapped user subject is recorded lower-cased", id: identity{Email: "Stranger@Example.NET"}, err: ReasonUnmappedPrincipal,
			who: tenant.Principal{Kind: tenant.KindUser, Subject: otherEmail}},
		{name: "edge/a service subject keeps its case", id: identity{CommonName: strings.ToUpper(serviceCN)}, err: ReasonUnmappedPrincipal,
			who: tenant.Principal{Kind: tenant.KindService, Subject: strings.ToUpper(serviceCN)}},
		{name: "happy/second tenant", id: identity{CommonName: service2CN}, want: tenant.Principal{Tenant: tenant2ID, Kind: tenant.KindService, Subject: service2CN}},
		{name: "sad/neither", id: identity{Subject: "u1"}, err: ReasonMissingClaim},
		{name: "sad/both", id: identity{Email: userEmail, CommonName: serviceCN}, err: ReasonAmbiguousIdentity},
		{name: "sad/service with a sub", id: identity{CommonName: serviceCN, Subject: "x"}, err: ReasonAmbiguousIdentity},
		{name: "sad/unmapped user", id: identity{Email: otherEmail}, err: ReasonUnmappedPrincipal, who: tenant.Principal{Kind: tenant.KindUser, Subject: otherEmail}},
		{name: "sad/an email as a client id does not cross kinds", id: identity{CommonName: userEmail}, err: ReasonUnmappedPrincipal, who: tenant.Principal{Kind: tenant.KindService, Subject: userEmail}},
		{name: "sad/a client id as an email does not cross kinds", id: identity{Email: serviceCN}, err: ReasonUnmappedPrincipal, who: tenant.Principal{Kind: tenant.KindUser, Subject: serviceCN}},
		{name: "raging/control character", id: identity{Email: userEmail + "\x00"}, err: ReasonUnmappedPrincipal},
		{name: "raging/space", id: identity{Email: " " + userEmail}, err: ReasonUnmappedPrincipal},
		{name: "raging/non-ASCII", id: identity{CommonName: serviceCN + "\u00e9"}, err: ReasonUnmappedPrincipal},
		{name: "raging/DEL", id: identity{Email: userEmail + "\x7f"}, err: ReasonUnmappedPrincipal},
		{name: "edge/longest subject", id: identity{Email: strings.Repeat("a", maxSubjectBytes)}, err: ReasonUnmappedPrincipal, who: tenant.Principal{Kind: tenant.KindUser, Subject: strings.Repeat("a", maxSubjectBytes)}},
		{name: "edge/one byte too long", id: identity{Email: strings.Repeat("a", maxSubjectBytes+1)}, err: ReasonUnmappedPrincipal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := resolve(c.id, ts)
			if c.err == (Reason{}) {
				if err != nil || p != c.want {
					t.Fatalf("p = %+v, err %v", p, err)
				}
				return
			}
			var ae *Error
			if !errors.As(err, &ae) || ae.Reason() != c.err || ae.who != c.who || p != (tenant.Principal{}) {
				t.Fatalf("p = %+v, err %v, who %+v", p, err, ae)
			}
		})
	}
	t.Run("corner/two tenants admitting one subject map to none", func(t *testing.T) {
		dup := []tenant.Tenant{
			{ID: "a", Principals: []tenant.PrincipalMatch{{Email: userEmail}}},
			{ID: "b", Principals: []tenant.PrincipalMatch{{Email: strings.ToUpper(userEmail)}}},
		}
		if _, err := resolve(identity{Email: userEmail}, dup); !errors.Is(err, ErrUnmapped) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("corner/a tenant without an id maps nothing", func(t *testing.T) {
		if _, err := resolve(identity{Email: userEmail}, []tenant.Tenant{{Principals: []tenant.PrincipalMatch{{Email: userEmail}}}}); !errors.Is(err, ErrUnmapped) {
			t.Fatalf("err = %v", err)
		}
	})
}

// FuzzResolve checks the properties of the claims-to-principal mapping
// (spec section 7): it never panics; an accepted principal's tenant admits
// its subject and has its kind; a user subject is the email in ASCII lower
// case; a subject that is not printable ASCII never
// maps; a user subject never maps as a service and the other way round.
func FuzzResolve(f *testing.F) {
	for _, s := range [][3]string{
		{userEmail, "", ""}, {"", serviceCN, ""}, {"OPERATOR@example.net", "", "x"}, {"", userEmail, ""},
		{serviceCN, "", ""}, {userEmail, serviceCN, ""}, {"", serviceCN, "sub"}, {"", "", ""},
		{"operator@example.net\u212a", "", ""}, {"op\x00@example.net", "", ""}, {user2Email, "", ""},
	} {
		f.Add(s[0], s[1], s[2])
	}
	ts := testTenants()
	f.Fuzz(func(t *testing.T, email, cn, sub string) {
		p, err := resolve(identity{Email: email, CommonName: cn, Subject: sub}, ts)
		if err != nil {
			var ae *Error
			if !errors.As(err, &ae) || p != (tenant.Principal{}) {
				t.Fatalf("err %v with principal %+v", err, p)
			}
			if ae.who.Tenant != "" || (ae.who.Kind != 0 && !printable(ae.who.Subject)) {
				t.Fatalf("recorded %+v", ae.who)
			}
			return
		}
		if !printable(p.Subject) {
			t.Fatalf("subject %q mapped", p.Subject)
		}
		matches := 0
		for i := range ts {
			if ts[i].Admits(p.Kind, p.Subject) {
				matches++
				if ts[i].ID != p.Tenant {
					t.Fatalf("subject %q admitted by %s but mapped to %s", p.Subject, ts[i].ID, p.Tenant)
				}
			}
		}
		if matches != 1 {
			t.Fatalf("%d tenants admit %+v", matches, p)
		}
		switch p.Kind {
		case tenant.KindUser:
			if p.Subject != strings.ToLower(email) || cn != "" {
				t.Fatalf("user %+v from email %q cn %q", p, email, cn)
			}
		case tenant.KindService:
			if p.Subject != cn || email != "" || sub != "" {
				t.Fatalf("service %+v from email %q cn %q sub %q", p, email, cn, sub)
			}
		default:
			t.Fatalf("kind %v", p.Kind)
		}
	})
}

func TestNewCloudflareAccess(t *testing.T) {
	good := config.CloudflareAccess{TeamDomain: "example-team.cloudflareaccess.com", Audience: "aud"}
	t.Run("happy/the default certs URL is the team domain's", func(t *testing.T) {
		a, err := NewCloudflareAccess(good, testTenants())
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		if a.keys.url != "https://example-team.cloudflareaccess.com/cdn-cgi/access/certs" || a.issuer != "https://example-team.cloudflareaccess.com" {
			t.Fatalf("url %q issuer %q", a.keys.url, a.issuer)
		}
		if a.keys.client.Timeout != FetchTimeout || a.interval != DefaultRefreshInterval {
			t.Fatal("defaults not applied")
		}
	})
	t.Run("happy/the request has no credentials", func(t *testing.T) {
		a, err := NewCloudflareAccess(good, testTenants(), WithHTTPClient(&http.Client{}))
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		if a.keys.client.Jar != nil {
			t.Fatal("a cookie jar")
		}
	})
	bad := []struct {
		name    string
		cfg     config.CloudflareAccess
		tenants []tenant.Tenant
		opts    []Option
	}{
		{"sad/no team domain", config.CloudflareAccess{Audience: "a"}, testTenants(), nil},
		{"sad/no audience", config.CloudflareAccess{TeamDomain: "t.example.net"}, testTenants(), nil},
		{"sad/http certs URL to a remote host", config.CloudflareAccess{TeamDomain: "t.example.net", Audience: "a", CertsURL: "http://example.net/certs"}, testTenants(), nil},
		{"sad/certs URL with credentials", config.CloudflareAccess{TeamDomain: "t.example.net", Audience: "a", CertsURL: "https://someone@example.net/certs"}, testTenants(), nil},
		{"sad/certs URL that does not parse", config.CloudflareAccess{TeamDomain: "t.example.net", Audience: "a", CertsURL: "https://%zz"}, testTenants(), nil},
		{"sad/zero refresh interval", good, testTenants(), []Option{WithRefreshInterval(0)}},
		{"sad/no tenants", good, nil, nil},
		{"sad/a tenant without an id", good, []tenant.Tenant{{Principals: []tenant.PrincipalMatch{{Email: userEmail}}}}, nil},
		{"sad/a duplicate tenant id", good, []tenant.Tenant{{ID: "a"}, {ID: "a"}}, nil},
		{"corner/two tenants share an email in another case", good, []tenant.Tenant{
			{ID: "a", Principals: []tenant.PrincipalMatch{{Email: userEmail}}},
			{ID: "b", Principals: []tenant.PrincipalMatch{{Email: strings.ToUpper(userEmail)}}},
		}, nil},
		{"corner/two tenants share a client id", good, []tenant.Tenant{
			{ID: "a", Principals: []tenant.PrincipalMatch{{CommonName: serviceCN}}},
			{ID: "b", Principals: []tenant.PrincipalMatch{{Email: userEmail}, {CommonName: serviceCN}}},
		}, nil},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if a, err := NewCloudflareAccess(c.cfg, c.tenants, c.opts...); err == nil {
				a.Close()
				t.Fatal("accepted")
			}
		})
	}
	t.Run("edge/an email and a client id with the same text do not overlap", func(t *testing.T) {
		a, err := NewCloudflareAccess(good, []tenant.Tenant{
			{ID: "a", Principals: []tenant.PrincipalMatch{{Email: userEmail}}},
			{ID: "b", Principals: []tenant.PrincipalMatch{{CommonName: userEmail}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		a.Close()
	})
}
