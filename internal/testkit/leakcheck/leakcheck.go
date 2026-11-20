// Package leakcheck is test support shared by the SSH and job engine tests:
// a deadline-bounded poll, a goroutine leak check and a private temporary
// directory. It is imported only by tests.
package leakcheck

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Poll calls cond every 10ms until it is true or d passes, and reports
// whether it became true.
func Poll(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond) // the polling interval of a deadline-bounded poll
	}
}

// Goroutines returns the stacks, by goroutine id, of the goroutines that
// belong to this module, x/crypto/ssh or pkg/sftp and are not test
// goroutines.
func Goroutines() map[string]string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	out := map[string]string{}
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, "testing.tRunner") || strings.Contains(g, "testing.(*T).Run") || strings.Contains(g, "testing.runCleanup") {
			continue
		}
		if !strings.Contains(g, "InfiniteRoomLabs/yasshm/") && !strings.Contains(g, "golang.org/x/crypto/ssh") && !strings.Contains(g, "github.com/pkg/sftp") {
			continue
		}
		head, _, _ := strings.Cut(g, "\n")
		out[strings.Fields(head + " x x")[1]] = g
	}
	return out
}

// Check registers a cleanup, run after every cleanup registered later (the
// servers' Close and the engines' Shutdown included), that fails the test
// when a goroutine started during it is still alive 5 seconds on.
func Check(t testing.TB) {
	t.Helper()
	base := Goroutines()
	t.Cleanup(func() {
		var leaked []string
		ok := Poll(5*time.Second, func() bool {
			leaked = leaked[:0]
			for id, g := range Goroutines() {
				if _, old := base[id]; !old {
					leaked = append(leaked, g)
				}
			}
			return len(leaked) == 0
		})
		if !ok {
			t.Errorf("%d goroutines leaked:\n%s", len(leaked), strings.Join(leaked, "\n\n"))
		}
	})
}

// PrivDir returns a new temporary directory with mode 0700 (t.TempDir gives
// 0755, which the store refuses).
func PrivDir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // G302: a directory needs the x bit; 0700 is the mode under test
		t.Fatal(err)
	}
	return dir
}
