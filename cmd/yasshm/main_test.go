package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/configtest"
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
