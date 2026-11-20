package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/configtest"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/leakcheck"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/loopback"
)

// runMainEnv makes the test binary act as yasshm: TestMain calls main
// instead of running the tests, so the tests below run the real entry point
// in a child process with real exit codes.
const runMainEnv = "YASSHM_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

func runBinary(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, args...) //nolint:gosec // G204: the test binary itself, with test arguments
	cmd.Env = append(os.Environ(), runMainEnv+"=1")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, out.String(), errb.String()
	case errors.As(err, &ee):
		return ee.ExitCode(), out.String(), errb.String()
	default:
		t.Fatal(err)
		return 0, "", ""
	}
}

func TestConfigCheckOnDisk(t *testing.T) {
	files := configtest.Write(t)
	t.Run("happy/valid config file exits 0 with one line", func(t *testing.T) {
		code, out, errs := runBinary(t, "config", "check", "--config", files.Config)
		if code != 0 || out != "config ok: 1 tenant, 2 servers\n" || errs != "" {
			t.Fatalf("code %d, stdout %q, stderr %q", code, out, errs)
		}
	})
	t.Run("sad/invalid config file exits 1 with the problem lines", func(t *testing.T) {
		bad := files.Rewrite(t, "listen: 127.0.0.1:8822", "listen: 192.0.2.1:8822")
		code, out, errs := runBinary(t, "config", "check", "--config", bad)
		if code != 1 || out != "" || !strings.HasPrefix(errs, "server.listen: must be a loopback IP address") {
			t.Fatalf("code %d, stdout %q, stderr %q", code, out, errs)
		}
	})
	t.Run("sad/missing --config exits 2", func(t *testing.T) {
		code, out, errs := runBinary(t, "config", "check")
		if code != 2 || out != "" || !strings.Contains(errs, "--config") {
			t.Fatalf("code %d, stdout %q, stderr %q", code, out, errs)
		}
	})
	t.Run("edge/no arguments exits 2", func(t *testing.T) {
		if code, _, _ := runBinary(t); code != 2 {
			t.Fatalf("code %d", code)
		}
	})
}

// TestServeSignal runs the real entry point's serve in a child process and
// stops it with SIGTERM: it exits 0 and the audit log ends with
// gateway.stopped for a signal. The key set URL is a loopback port that
// refuses connections, so nothing outside the machine is reached.
func TestServeSignal(t *testing.T) {
	files := configtest.Write(t)
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}
	auditLog := filepath.Join(dir, "audit.jsonl")
	b, err := os.ReadFile(files.Config)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.NewReplacer(
		"listen: 127.0.0.1:8822", "listen: 127.0.0.1:0",
		"data_dir: /var/lib/yasshm", "data_dir: "+data,
		"audit_log: /var/log/yasshm/audit.jsonl", "audit_log: "+auditLog,
		"audience: "+configtest.Audience, "audience: "+configtest.Audience+"\n    certs_url: http://127.0.0.1:"+strconv.Itoa(loopback.RefusingPort(t))+"/certs",
	).Replace(string(b))
	cfg := filepath.Join(dir, "config.yaml")
	if werr := os.WriteFile(cfg, []byte(text), 0o600); werr != nil { //nolint:gosec // G703: a path under t.TempDir
		t.Fatal(werr)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "serve", "--config", cfg) //nolint:gosec // G204: the test binary itself, with test arguments
	cmd.Env = append(os.Environ(), runMainEnv+"=1")
	var errb lockedBuffer
	cmd.Stderr = &errb
	if serr := cmd.Start(); serr != nil {
		t.Fatal(serr)
	}
	if !leakcheck.Poll(30*time.Second, func() bool { return strings.Contains(errb.String(), `"msg":"listening"`) }) {
		_ = cmd.Process.Kill()
		t.Fatalf("serve never listened:\n%s", errb.String())
	}
	if serr := cmd.Process.Signal(syscall.SIGTERM); serr != nil {
		t.Fatal(serr)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case werr := <-done:
		if werr != nil {
			t.Fatalf("exit: %v\n%s", werr, errb.String())
		}
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("serve did not stop:\n%s", errb.String())
	}
	log, err := os.ReadFile(auditLog) //nolint:gosec // G304: the test's own audit log
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(log)), "\n")
	if !strings.Contains(lines[0], `"event":"gateway.started"`) || !strings.Contains(lines[len(lines)-1], `"event":"gateway.stopped","reason":"signal"`) {
		t.Fatalf("audit log:\n%s", log)
	}
}

// lockedBuffer is a buffer a child process's output can be copied into
// while the test reads it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
