// Package syncbuf is a byte buffer that a child process, a logger and a
// test can share: writes and reads are serialised by a mutex. It is test
// support and is imported only by tests.
package syncbuf

import (
	"bytes"
	"sync"
)

// Buffer is a bytes.Buffer safe for concurrent use. The zero value is an
// empty buffer.
type Buffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

// Write appends p.
func (s *Buffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

// String returns everything written so far.
func (s *Buffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
