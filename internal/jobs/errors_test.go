package jobs

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestErrors(t *testing.T) {
	t.Run("happy/codes match with errors.Is and CodeOf", func(t *testing.T) {
		cause := errors.New("dial tcp 192.0.2.1:22: refused")
		err := fmt.Errorf("wrapped: %w", internal("could not record the command", cause))
		if !errors.Is(err, CodeInternal) || errors.Is(err, CodeBusy) || CodeOf(err) != CodeInternal || !errors.Is(err, cause) {
			t.Fatal(err)
		}
		if CodeInternal.Error() != "internal" || CodeOf(nil) != "" || CodeOf(errors.New("x")) != CodeInternal {
			t.Fatal("codes")
		}
		if strings.Contains(err.Error(), "192.0.2.1") {
			t.Fatalf("the message names the cause: %v", err)
		}
		b := busy(LimitPerServer)
		if !errors.Is(b, CodeBusy) || b.Limit() != LimitPerServer || b.Unwrap() != nil {
			t.Fatal(b)
		}
		if !errors.Is(shuttingDown(), ErrShuttingDown) || errors.Is(shuttingDown(), ErrAuditFailed) || errors.Is(b, ErrShuttingDown) {
			t.Fatal("sentinels")
		}
		if (&Error{code: CodeNotFound}).Error() != "jobs: not_found" {
			t.Fatal("bare message")
		}
	})
}
