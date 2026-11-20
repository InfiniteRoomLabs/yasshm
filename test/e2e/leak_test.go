package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fdCount returns how many file descriptors process pid has open.
func fdCount(t *testing.T, pid int) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/" + strconv.Itoa(pid) + "/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(ents)
}

// procStatus returns a field of /proc/<pid>/status in its first unit
// (kB for VmRSS, a count for Threads).
func procStatus(t *testing.T, pid int, field string) int64 {
	t.Helper()
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, field+":"); ok {
			n, err := strconv.ParseInt(strings.Fields(v)[0], 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
	}
	t.Fatalf("no %s in /proc/%d/status", field, pid)
	return 0
}

// closeIdle closes the idle keep-alive connections of the shared transport
// (the clients' and the edge's), so the gateway's side of them closes too.
func closeIdle() {
	if tr, ok := http.DefaultTransport.(*http.Transport); ok {
		tr.CloseIdleConnections()
	}
}

// settledFDs closes idle connections and waits (bounded) until the
// gateway's descriptor count reads the same five times in a row, 50 ms
// apart, and returns it.
func settledFDs(t *testing.T, pid int, bound time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(bound)
	last, same := -1, 0
	for same < 5 {
		closeIdle()
		n := fdCount(t, pid)
		if n == last {
			same++
		} else {
			last, same = n, 1
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond) // the interval of a bounded settle poll on /proc
	}
	return last
}

// TestE2EResourceBounds is the leak check around the running binary: a
// burst of 200 tool calls of mixed kinds (successes, tool errors of every
// path, and a killed command) through the edge, then the gateway's open
// file descriptors (read from /proc/<pid>/fd) return to their idle value
// within a bound, and its RSS stays under a generous ceiling. Descriptors
// stand in for goroutines and threads too: a leaked request, SSH session
// or output file holds one.
func TestE2EResourceBounds(t *testing.T) {
	t.Parallel()
	f := newFixture(t, withLimits(map[string]int64{"max_concurrent_commands": 8, "max_concurrent_per_server": 4}))
	g := f.start(t)
	g.waitReady(t)
	e := g.edge(t)
	srv := f.target("lab-1")
	file := filepath.Join(srv.Root, "burst.txt")
	if err := os.WriteFile(file, []byte(strings.Repeat("burst\n", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	warm := connect(t, e)
	done := warm.runCommand(t, "lab-1", "echo done", map[string]any{"wait_seconds": 10})

	type call struct {
		tool string
		args map[string]any
	}
	kinds := []call{
		{"list_servers", map[string]any{}},
		{"run_command", map[string]any{"server": "lab-1", "command": "echo burst; echo err >&2", "wait_seconds": 10}},
		{"read_output", map[string]any{"command_id": done.CommandID}},
		{"list_commands", map[string]any{"limit": 5}},
		{"get_server_info", map[string]any{"server": "lab-1"}},
		{"download_file", map[string]any{"server": "lab-1", "path": file, "max_bytes": 64}},
		{"upload_file", map[string]any{"server": "lab-1", "path": file, "content": "x"}},                // access_denied
		{"run_command", map[string]any{"server": "lab-1", "command": "sudo id"}},                        // policy_denied
		{"read_output", map[string]any{"command_id": strings.Repeat("0", 32)}},                          // not_found
		{"run_command", map[string]any{"server": "Not An Id", "command": "id"}},                         // invalid_argument
		{"download_file", map[string]any{"server": "lab-1", "path": filepath.Join(srv.Root, "absent")}}, // not_found
	}
	// One of each kind before the idle value is read, so lazily opened
	// files (the database's WAL, the outputs directory) are counted.
	for _, k := range kinds {
		_, _ = warm.callErr(t, k.tool, k.args)
	}
	idle := settledFDs(t, g.pid, 10*time.Second)
	rss0, threads0 := procStatus(t, g.pid, "VmRSS"), procStatus(t, g.pid, "Threads")

	const total, workers = 200, 4 // plus the killed command
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[string]int{}
	next := make(chan int, total)
	for i := range total {
		next <- i
	}
	close(next)
	peak := 0
	for range workers {
		c := connect(t, e)
		wg.Go(func() {
			for i := range next {
				k := kinds[i%len(kinds)]
				var key string
				res, err := c.callErr(t, k.tool, k.args)
				switch {
				case err != nil:
					t.Errorf("%s: protocol error: %v", k.tool, err)
					return
				case res.IsError:
					key = k.tool + ":" + errorCode(res)
				default:
					key = k.tool + ":ok"
				}
				n := fdCount(t, g.pid)
				mu.Lock()
				codes[key]++
				peak = max(peak, n)
				mu.Unlock()
			}
		})
	}
	// The killed command of the burst, from the test goroutine while the
	// workers run.
	victim := warm.runCommand(t, "lab-1", "echo victim; sleep 300", map[string]any{"wait_seconds": 0})
	warm.ok(t, "kill_command", map[string]any{"command_id": victim.CommandID, "signal": "KILL"}, &killResult{})
	if r := warm.waitStatus(t, victim.CommandID, callBound); r.Status != "killed" {
		t.Errorf("the victim ended %s", r.Status)
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	t.Logf("burst outcomes: %v", codes)
	for _, want := range []string{"run_command:ok", "run_command:policy_denied", "run_command:invalid_argument", "upload_file:access_denied", "read_output:not_found", "download_file:not_found"} {
		if codes[want] == 0 {
			t.Errorf("the burst had no %s", want)
		}
	}
	t.Run("happy/descriptors return to idle after the burst", func(t *testing.T) {
		var after int
		closeIdle()
		deadline := time.Now().Add(20 * time.Second)
		for after = fdCount(t, g.pid); after > idle && time.Now().Before(deadline); after = fdCount(t, g.pid) {
			closeIdle()
			time.Sleep(50 * time.Millisecond) // the interval of a bounded poll on /proc
		}
		t.Logf("gateway descriptors: idle %d, peak during the burst %d, after %d", idle, peak, after)
		if after > idle {
			ents, _ := os.ReadDir("/proc/" + strconv.Itoa(g.pid) + "/fd")
			var links []string
			for _, en := range ents {
				l, _ := os.Readlink("/proc/" + strconv.Itoa(g.pid) + "/fd/" + en.Name())
				links = append(links, en.Name()+" -> "+l)
			}
			t.Fatalf("descriptors did not return to idle (%d > %d):\n%s", after, idle, strings.Join(links, "\n"))
		}
	})
	t.Run("happy/RSS and threads stay bounded", func(t *testing.T) {
		rss, threads := procStatus(t, g.pid, "VmRSS"), procStatus(t, g.pid, "Threads")
		t.Logf("gateway RSS %d kB -> %d kB, threads %d -> %d", rss0, rss, threads0, threads)
		const ceilingKB = 256 << 10
		if rss > ceilingKB {
			t.Fatalf("RSS %d kB is over the ceiling of %d kB", rss, ceilingKB)
		}
		if threads > threads0+16 {
			t.Fatalf("threads grew from %d to %d", threads0, threads)
		}
	})
	t.Run("happy/no command is left on the target", func(t *testing.T) {
		g.stop(t)
		if n := srv.Running(); n != 0 {
			t.Fatalf("%d commands still running", n)
		}
		if got := len(f.events(t, func(l auditLine) bool { return l.Event == "tool.called" })); got < total {
			t.Fatalf("%d tool.called events for %d calls", got, total)
		}
	})
}

// errorCode returns the code of an error result, or "unparsable".
func errorCode(res *mcp.CallToolResult) string {
	if len(res.Content) != 1 {
		return "unparsable"
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	var e toolError
	if !ok || json.Unmarshal([]byte(tc.Text), &e) != nil || e.Code == "" {
		return "unparsable"
	}
	return e.Code
}
