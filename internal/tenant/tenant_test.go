package tenant

import (
	"encoding/json"
	"testing"
)

func TestAccessLevelOrderAndText(t *testing.T) {
	t.Run("happy/zero value is none", func(t *testing.T) {
		var l AccessLevel
		if l != AccessNone || l.String() != "none" {
			t.Fatalf("zero value = %v (%d), want none", l, l)
		}
	})
	t.Run("happy/ordered none < look < full", func(t *testing.T) {
		if AccessNone >= AccessLook || AccessLook >= AccessFull {
			t.Fatal("levels are not ordered none < look < full")
		}
	})
	for _, name := range []string{"none", "look", "full"} {
		t.Run("happy/round trip "+name, func(t *testing.T) {
			l, err := ParseAccessLevel(name)
			if err != nil {
				t.Fatal(err)
			}
			b, err := l.MarshalText()
			if err != nil || string(b) != name || l.String() != name {
				t.Fatalf("MarshalText = %q, %v; String = %q; want %q", b, err, l.String(), name)
			}
			var back AccessLevel
			if err := back.UnmarshalText(b); err != nil || back != l {
				t.Fatalf("UnmarshalText = %v, %v; want %v", back, err, l)
			}
		})
	}
	t.Run("happy/json uses the text form", func(t *testing.T) {
		b, err := json.Marshal(struct{ A AccessLevel }{AccessLook})
		if err != nil || string(b) != `{"A":"look"}` {
			t.Fatalf("json = %s, %v", b, err)
		}
	})
	for _, bad := range []string{"", "Look", "LOOK", " look", "look ", "admin", "read"} {
		t.Run("sad/parse "+bad, func(t *testing.T) {
			if _, err := ParseAccessLevel(bad); err == nil {
				t.Fatalf("ParseAccessLevel(%q) succeeded", bad)
			}
			l := AccessLook
			if err := l.UnmarshalText([]byte(bad)); err == nil || l != AccessLook {
				t.Fatalf("UnmarshalText(%q) = %v, level %v", bad, err, l)
			}
		})
	}
	t.Run("edge/out of range level", func(t *testing.T) {
		l := AccessLevel(200)
		if l.String() != "level(200)" {
			t.Fatalf("String = %q", l.String())
		}
		if _, err := l.MarshalText(); err == nil {
			t.Fatal("MarshalText of an undefined level succeeded")
		}
	})
}

func TestPermits(t *testing.T) {
	levels := []AccessLevel{AccessNone, AccessLook, AccessFull}
	for _, granted := range levels {
		for _, required := range levels {
			want := required <= granted
			class := "sad/"
			if want {
				class = "happy/"
			}
			if granted == required {
				class = "edge/"
			}
			t.Run(class+"granted "+granted.String()+" required "+required.String(), func(t *testing.T) {
				if got := Permits(granted, required); got != want {
					t.Fatalf("Permits(%v, %v) = %v, want %v", granted, required, got, want)
				}
			})
		}
	}
	t.Run("corner/undefined grant above full", func(t *testing.T) {
		if !Permits(AccessLevel(9), AccessFull) {
			t.Fatal("an out-of-range grant compares numerically")
		}
	})
}

func TestPrincipal(t *testing.T) {
	t.Run("happy/string forms", func(t *testing.T) {
		u := Principal{Tenant: "default", Kind: KindUser, Subject: "operator@example.net"}
		s := Principal{Tenant: "default", Kind: KindService, Subject: "abc.access"}
		if u.String() != "user:operator@example.net" || s.String() != "service:abc.access" {
			t.Fatalf("got %q and %q", u, s)
		}
	})
	t.Run("sad/zero kind", func(t *testing.T) {
		if (Principal{}).String() != "unknown:" {
			t.Fatalf("got %q", Principal{}.String())
		}
	})
}

func TestPrincipalMatch(t *testing.T) {
	email := PrincipalMatch{Email: "Operator@Example.net"}
	svc := PrincipalMatch{CommonName: "abc.access"}
	tests := []struct {
		name    string
		m       PrincipalMatch
		kind    PrincipalKind
		subject string
		want    bool
	}{
		{"happy/email", email, KindUser, "operator@example.net", true},
		{"happy/service", svc, KindService, "abc.access", true},
		{"sad/other email", email, KindUser, "someone@example.net", false},
		{"sad/email entry does not admit a service", email, KindService, "Operator@Example.net", false},
		{"sad/service entry does not admit a user", svc, KindUser, "abc.access", false},
		{"sad/service name is case sensitive", svc, KindService, "ABC.access", false},
		{"edge/empty subject", PrincipalMatch{}, KindUser, "", false},
		{"edge/empty service subject", PrincipalMatch{}, KindService, "", false},
		{"raging/unknown kind", email, PrincipalKind(7), "operator@example.net", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.m.Matches(tt.kind, tt.subject); got != tt.want {
				t.Fatalf("Matches = %v, want %v", got, tt.want)
			}
		})
	}
}

func twoTenants() (a, b Tenant) {
	idA := Identity{ID: "look", User: "yasshm-look", PrivateKeyFile: "/keys/a"}
	idB := Identity{ID: "look", User: "other", PrivateKeyFile: "/keys/b"}
	a = Tenant{
		ID:         "alpha",
		Principals: []PrincipalMatch{{Email: "a@example.net"}},
		Identities: map[IdentityID]Identity{"look": idA},
		Servers:    []Server{{Tenant: "alpha", ID: "lab-1", Host: "lab-1.example.net", Identity: idA, Access: AccessLook}},
	}
	b = Tenant{
		ID:         "beta",
		Principals: []PrincipalMatch{{CommonName: "svc"}},
		Identities: map[IdentityID]Identity{"look": idB},
		Servers: []Server{
			{Tenant: "beta", ID: "lab-1", Host: "other.example.net", Identity: idB, Access: AccessLook},
			{Tenant: "alpha", ID: "stray", Host: "stray.example.net"},
		},
	}
	return a, b
}

func TestTenantServer(t *testing.T) {
	a, b := twoTenants()
	t.Run("happy/own server", func(t *testing.T) {
		s, ok := a.Server("lab-1")
		if !ok || s.Host != "lab-1.example.net" {
			t.Fatalf("got %+v, %v", s, ok)
		}
	})
	t.Run("happy/same id in another tenant is a different server", func(t *testing.T) {
		s, ok := b.Server("lab-1")
		if !ok || s.Host != "other.example.net" || s.Tenant != "beta" {
			t.Fatalf("got %+v, %v", s, ok)
		}
	})
	t.Run("sad/unknown id", func(t *testing.T) {
		if _, ok := a.Server("nope"); ok {
			t.Fatal("found a server that does not exist")
		}
	})
	t.Run("corner/a server stamped with another tenant is never returned", func(t *testing.T) {
		if _, ok := b.Server("stray"); ok {
			t.Fatal("returned a server whose tenant is not this tenant")
		}
	})
	t.Run("edge/empty id", func(t *testing.T) {
		if _, ok := a.Server(""); ok {
			t.Fatal("found a server with an empty id")
		}
	})
}

func TestTenantAdmits(t *testing.T) {
	a, b := twoTenants()
	t.Run("happy/user of tenant a", func(t *testing.T) {
		if !a.Admits(KindUser, "A@example.net") {
			t.Fatal("not admitted")
		}
	})
	t.Run("sad/user of tenant a is not admitted by tenant b", func(t *testing.T) {
		if b.Admits(KindUser, "a@example.net") {
			t.Fatal("admitted across tenants")
		}
	})
	t.Run("edge/no principals", func(t *testing.T) {
		if (&Tenant{}).Admits(KindUser, "") {
			t.Fatal("an empty tenant admitted someone")
		}
	})
}

func TestIdentityFor(t *testing.T) {
	a, _ := twoTenants()
	s := &a.Servers[0]
	for _, l := range []AccessLevel{AccessNone, AccessLook, AccessFull} {
		t.Run("happy/one identity in v0.1 for "+l.String(), func(t *testing.T) {
			if got := s.IdentityFor(l); got != s.Identity {
				t.Fatalf("IdentityFor(%v) = %+v", l, got)
			}
		})
	}
	t.Run("edge/zero server", func(t *testing.T) {
		var z Server
		if got := z.IdentityFor(AccessLook); got != (Identity{}) {
			t.Fatalf("got %+v", got)
		}
	})
}
