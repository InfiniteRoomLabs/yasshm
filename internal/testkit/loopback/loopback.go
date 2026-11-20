// Package loopback is test support for "nothing answers here": a loopback
// TCP port that is reserved for the whole test and refuses every
// connection. It is imported only by tests.
//
// Closing a listener to free a port and then dialling that port races with
// every other test process on the machine: the kernel may hand the port to
// another server in between, and the dial then reaches it. RefusingPort
// avoids that by holding a socket bound to the port without listening on
// it: Linux answers a SYN to such a port with a reset (ECONNREFUSED), and
// since the socket does not set SO_REUSEADDR no other socket can bind the
// port while it is held.
package loopback

import (
	"syscall"
	"testing"
)

// RefusingPort returns a loopback (127.0.0.1) TCP port that refuses every
// connection and that no other socket can bind until the test ends.
func RefusingPort(t testing.TB) int {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("loopback: socket: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	if berr := syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); berr != nil {
		t.Fatalf("loopback: bind: %v", berr)
	}
	sa, err := syscall.Getsockname(fd)
	if err != nil {
		t.Fatalf("loopback: getsockname: %v", err)
	}
	in4, ok := sa.(*syscall.SockaddrInet4)
	if !ok || in4.Port == 0 {
		t.Fatalf("loopback: unexpected address %#v", sa)
	}
	return in4.Port
}
