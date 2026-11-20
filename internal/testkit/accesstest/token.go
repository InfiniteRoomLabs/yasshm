package accesstest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/json"
	"maps"
	"strings"
	"time"
)

// The algorithms a Builder can sign with. Only AlgRS256 is ever accepted;
// the others exist to prove that they are refused.
const (
	AlgRS256 = "RS256"
	AlgRS384 = "RS384"
	AlgPS256 = "PS256"
	AlgES256 = "ES256"
	AlgHS256 = "HS256"
	AlgNone  = "none"
)

// Builder mints one token. Every part is overridable: header fields, claims,
// the algorithm and the key; Sign renders it. A Builder is not safe for
// concurrent use.
type Builder struct {
	header map[string]any
	claims map[string]any
	alg    string
	key    *rsa.PrivateKey
	hmac   []byte
}

// User returns a builder for a valid identity token for email, signed by
// the current signing key, valid from now for ten minutes, with the
// vendor's claim set (F7).
func (is *Issuer) User(email string) *Builder {
	b := is.base()
	b.claims["email"] = email
	b.claims["sub"] = "00000000-0000-4000-8000-000000000001"
	b.claims["nbf"] = b.claims["iat"]
	b.claims["identity_nonce"] = "nonce-for-tests"
	b.claims["country"] = "XX"
	b.claims["type"] = "app"
	return b
}

// Service returns a builder for a valid service token for the client id cn,
// in the vendor's shape: common_name set, sub empty, no email and no nbf.
func (is *Issuer) Service(cn string) *Builder {
	b := is.base()
	b.claims["common_name"] = cn
	b.claims["sub"] = ""
	b.claims["type"] = "app"
	return b
}

// For returns a builder for a valid token for who: an address with "@" is a
// user (User), anything else a service token's client id (Service).
func (is *Issuer) For(who string) *Builder {
	if strings.Contains(who, "@") {
		return is.User(who)
	}
	return is.Service(who)
}

func (is *Issuer) base() *Builder {
	k := is.SigningKey()
	now := is.now().Unix()
	return &Builder{
		header: map[string]any{"alg": AlgRS256, "kid": k.ID, "typ": "JWT"},
		claims: map[string]any{
			"iss": "https://" + TeamDomain,
			"aud": []string{Audience},
			"iat": now,
			"exp": now + 600,
		},
		alg: AlgRS256,
		key: k.Private,
	}
}

// Claim sets a claim to v (any JSON value).
func (b *Builder) Claim(name string, v any) *Builder { b.claims[name] = v; return b }

// Without removes a claim.
func (b *Builder) Without(name string) *Builder { delete(b.claims, name); return b }

// Times sets iat, nbf and exp relative to at (offsets in seconds); nbf is
// set only when the claim is present.
func (b *Builder) Times(at time.Time, iat, nbf, exp int64) *Builder {
	b.claims["iat"] = at.Unix() + iat
	if _, ok := b.claims["nbf"]; ok {
		b.claims["nbf"] = at.Unix() + nbf
	}
	b.claims["exp"] = at.Unix() + exp
	return b
}

// Header sets a header field to v.
func (b *Builder) Header(name string, v any) *Builder { b.header[name] = v; return b }

// WithoutHeader removes a header field.
func (b *Builder) WithoutHeader(name string) *Builder { delete(b.header, name); return b }

// SignWith signs with key (an RSA key the issuer may not serve), keeping the
// header's kid.
func (b *Builder) SignWith(key *rsa.PrivateKey) *Builder { b.key = key; return b }

// Alg sets the header's alg and signs with that algorithm: RS384 and PS256
// with the RSA key, ES256 with the test EC key, none with an empty signature.
func (b *Builder) Alg(alg string) *Builder {
	b.alg = alg
	b.header["alg"] = alg
	return b
}

// HS256WithPublicKey sets alg HS256 and signs with HMAC-SHA256 keyed with
// the DER bytes of the signing key's public key: the classic key-confusion
// attack on a verifier that lets the token choose the algorithm.
func (b *Builder) HS256WithPublicKey() *Builder {
	der, err := x509.MarshalPKIXPublicKey(&b.key.PublicKey)
	if err != nil {
		panic(err)
	}
	b.hmac = der
	return b.Alg(AlgHS256)
}

// Sign renders the token as header.claims.signature.
func (b *Builder) Sign() string {
	h, err := json.Marshal(b.header)
	if err != nil {
		panic(err)
	}
	c, err := json.Marshal(b.claims)
	if err != nil {
		panic(err)
	}
	return SignRaw(b64(h)+"."+b64(c), b.alg, b.key, b.hmac)
}

// SignRaw signs the already-encoded signing input with alg and appends the
// signature segment.
func SignRaw(input, alg string, key *rsa.PrivateKey, hmacKey []byte) string {
	var sig []byte
	var err error
	switch alg {
	case AlgRS256:
		d := sha256.Sum256([]byte(input))
		sig, err = rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, d[:])
	case AlgRS384:
		d := sha512.Sum384([]byte(input))
		sig, err = rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA384, d[:])
	case AlgPS256:
		d := sha256.Sum256([]byte(input))
		sig, err = rsa.SignPSS(rand.Reader, key, crypto.SHA256, d[:], nil)
	case AlgES256:
		d := sha256.Sum256([]byte(input))
		rr, ss, eerr := ecdsa.Sign(rand.Reader, ECKey(), d[:])
		err = eerr
		if err == nil {
			sig = make([]byte, 64)
			rr.FillBytes(sig[:32])
			ss.FillBytes(sig[32:])
		}
	case AlgHS256:
		m := hmac.New(sha256.New, hmacKey)
		m.Write([]byte(input))
		sig = m.Sum(nil)
	case AlgNone:
		sig = nil
	default:
		panic("accesstest: unknown alg " + alg)
	}
	if err != nil {
		panic(err)
	}
	return input + "." + b64(sig)
}

// Segments splits a token into its three segments.
func Segments(tok string) [3]string {
	var s [3]string
	copy(s[:], strings.SplitN(tok, ".", 3))
	return s
}

// TamperPayload replaces the claims segment of a signed token with claims,
// keeping the original header and signature.
func TamperPayload(tok string, claims map[string]any) string {
	s := Segments(tok)
	c, err := json.Marshal(claims)
	if err != nil {
		panic(err)
	}
	return s[0] + "." + b64(c) + "." + s[2]
}

// TamperSignature flips one bit in the middle of the signature.
func TamperSignature(tok string) string {
	s := Segments(tok)
	sig := []byte(s[2])
	i := len(sig) / 2
	if sig[i] == 'A' {
		sig[i] = 'B'
	} else {
		sig[i] = 'A'
	}
	return s[0] + "." + s[1] + "." + string(sig)
}

// Claims returns a copy of the builder's claims, for TamperPayload.
func (b *Builder) Claims() map[string]any {
	return maps.Clone(b.claims)
}
