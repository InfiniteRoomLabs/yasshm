package edgetest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/InfiniteRoomLabs/yasshm/internal/auth"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/accesstest"
)

// seenReq is what the origin saw.
type seenReq struct {
	host   string
	tokens []string
}

func origin(t *testing.T) (srv *httptest.Server, last func() seenReq) {
	t.Helper()
	var mu sync.Mutex
	var seen seenReq
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = seenReq{host: r.Host, tokens: r.Header.Values(auth.Header)}
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv, func() seenReq { mu.Lock(); defer mu.Unlock(); return seen }
}

func get(t *testing.T, url, header string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.WithoutCancel(t.Context()), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	if header != "" {
		req.Header.Set(auth.Header, header)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

func TestEdge(t *testing.T) {
	is := accesstest.New(t, nil)
	srv, last := origin(t)
	e := New(t, srv.URL, is, "alice@example.net")
	t.Run("happy/adds a token and the public host", func(t *testing.T) {
		get(t, e.URL+"/mcp", "")
		s := last()
		if s.host != PublicHost || len(s.tokens) != 1 || accesstest.Segments(s.tokens[0])[2] == "" {
			t.Fatalf("origin saw %+v", s)
		}
	})
	t.Run("raging/a client-supplied token is replaced", func(t *testing.T) {
		get(t, e.URL+"/mcp", "forged")
		s := last()
		if len(s.tokens) != 1 || s.tokens[0] == "forged" {
			t.Fatalf("origin saw %+v", s)
		}
	})
	t.Run("sad/no token", func(t *testing.T) {
		e.SetMode(ModeNoToken)
		defer e.SetMode(ModeToken)
		get(t, e.URL+"/mcp", "forged")
		if s := last(); len(s.tokens) != 0 {
			t.Fatalf("origin saw %+v", s)
		}
	})
	t.Run("sad/bad token", func(t *testing.T) {
		e.SetMode(ModeBadToken)
		defer e.SetMode(ModeToken)
		get(t, e.URL+"/mcp", "")
		good := is.User("alice@example.net").Sign()
		if s := last(); len(s.tokens) != 1 || accesstest.Segments(s.tokens[0])[2] == accesstest.Segments(good)[2] {
			t.Fatalf("origin saw %+v", s)
		}
	})
	t.Run("happy/a service identity", func(t *testing.T) {
		e.As("svc.access")
		defer e.As("alice@example.net")
		get(t, e.URL+"/mcp", "")
		if s := last(); len(s.tokens) != 1 {
			t.Fatalf("origin saw %+v", s)
		}
	})
}
