package loopback

import (
	"context"
	"errors"
	"net"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestRefusingPort(t *testing.T) {
	port := RefusingPort(t)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	t.Run("sad/a dial is refused at once", func(t *testing.T) {
		d := net.Dialer{Timeout: 2 * time.Second}
		c, err := d.DialContext(context.Background(), "tcp", addr)
		if err == nil {
			_ = c.Close()
			t.Fatal("connected")
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			t.Fatalf("err = %v, want connection refused", err)
		}
	})
	t.Run("raging/no listener can take the port", func(t *testing.T) {
		var lc net.ListenConfig
		l, err := lc.Listen(context.Background(), "tcp", addr)
		if err == nil {
			_ = l.Close()
			t.Fatal("another listener bound the reserved port")
		}
	})
}
