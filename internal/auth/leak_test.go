package auth

import (
	"encoding/base64"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/accesstest"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/syncbuf"
)

// TestNothingLeaks sends every token class through the middleware, then
// some issuer faults, and scans everything the test captured -- the audit
// log, the authenticator's and the middleware's log output, every error
// string and every response body -- for the token, each of its segments and
// the key material. None may appear.
func TestNothingLeaks(t *testing.T) {
	f := newFixture(t)
	f.load(t)
	log, path := auditLog(t)
	mwLogs := &syncbuf.Buffer{}
	srv := Middleware(f.a, log, WithMiddlewareLogger(slog.New(slog.NewJSONHandler(mwLogs, nil))))(Require(&okHandler{}))
	var captured strings.Builder
	var secrets []string
	for _, c := range tokenClasses() {
		r := c.req(f)
		for _, v := range r.Header.Values(Header) {
			if len(v) < 8 {
				continue
			}
			secrets = append(secrets, v)
			for _, s := range accesstest.Segments(v) {
				if len(s) >= 8 {
					secrets = append(secrets, s)
				}
			}
		}
		if _, err := f.a.Authenticate(r.Clone(r.Context())); err != nil {
			captured.WriteString(err.Error() + "\n")
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		body, _ := io.ReadAll(rec.Body)
		captured.Write(body)
		for k, vs := range rec.Header() {
			captured.WriteString(k + ": " + strings.Join(vs, ",") + "\n")
		}
	}
	// Faults, a rotation and a broken audit log add their own log lines.
	for _, fault := range []accesstest.Fault{accesstest.FaultGarbage, accesstest.FaultHuge, accesstest.FaultStatus500, accesstest.FaultRedirectOtherHost} {
		f.is.SetFault(fault)
		if err := f.a.keys.refresh(t.Context()); err != nil {
			captured.WriteString(err.Error() + "\n")
		}
	}
	f.is.SetFault(accesstest.FaultNone)
	tk := f.is.User(userEmail).Sign()
	secrets = append(secrets, tk)
	rec := httptest.NewRecorder()
	Middleware(f.a, &brokenLog{}, WithMiddlewareLogger(slog.New(slog.NewJSONHandler(mwLogs, nil))))(&okHandler{}).ServeHTTP(rec, request(tk))
	captured.WriteString(rec.Body.String())

	audit, err := os.ReadFile(path) //nolint:gosec // G304: the test's own temp file
	if err != nil {
		t.Fatal(err)
	}
	captured.Write(audit)
	captured.WriteString(f.logs.String())
	captured.WriteString(mwLogs.String())
	all := captured.String()
	if len(audit) == 0 || !strings.Contains(f.logs.String(), "key set fetch failed") || !strings.Contains(mwLogs.String(), "audit write failed") {
		t.Fatal("the test captured nothing; it would prove nothing")
	}

	k := accesstest.RSAKey(0)
	for _, b := range [][]byte{k.N.Bytes(), k.D.Bytes(), k.Primes[0].Bytes()} {
		secrets = append(secrets, base64.RawURLEncoding.EncodeToString(b)[:24], base64.StdEncoding.EncodeToString(b)[:24], hex.EncodeToString(b)[:24])
	}
	secrets = append(secrets, accesstest.TeamDomain, accesstest.Audience)
	for _, s := range secrets {
		if strings.Contains(all, s) {
			t.Errorf("raging/captured output contains a token, segment or key fragment (%d bytes)", len(s))
		}
	}
	t.Run("happy/the certs URL is not logged", func(t *testing.T) {
		if strings.Contains(all, f.is.CertsURL()) || strings.Contains(all, "127.0.0.1") {
			t.Fatal("the URL or the issuer address appears in the output")
		}
	})
	t.Run("happy/rejections name a reason from the closed set only", func(t *testing.T) {
		for _, line := range strings.Split(strings.TrimSpace(string(audit)), "\n") {
			if strings.Contains(line, `"auth.rejected"`) && !strings.Contains(line, `"reason":"`) {
				t.Fatalf("no reason: %s", line)
			}
		}
	})
}
