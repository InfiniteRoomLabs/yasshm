package audit

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

func encode(t *testing.T, ev Event) map[string]any {
	t.Helper()
	ff := &fakeFile{}
	if err := newLogger(ff, WithClock(clock)).Write(ev); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(ff.buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func i64(v int64) *int64 { return &v }
func yes() *bool         { b := true; return &b }

func TestEvents(t *testing.T) {
	exit := 3
	svc := tenant.Principal{Kind: tenant.KindService, Subject: "abc.access"}
	tests := []struct {
		name string
		ev   Event
		want map[string]any
	}{
		{"happy/auth.accepted", AuthAccepted("r1", alice), map[string]any{
			"event": "auth.accepted", "request_id": "r1", "tenant": "default", "principal": "user:operator@example.net", "decision": "allow"}},
		{"sad/auth.rejected without a token", AuthRejected("r2", RejectMissingToken, tenant.Principal{}), map[string]any{
			"event": "auth.rejected", "request_id": "r2", "decision": "deny", "reason": "missing_token"}},
		{"sad/auth.rejected for an unmapped principal", AuthRejected("r3", RejectUnmappedPrincipal, svc), map[string]any{
			"event": "auth.rejected", "request_id": "r3", "principal": "service:abc.access", "decision": "deny", "reason": "unmapped_principal"}},
		{"raging/auth.rejected summary of suppressed rejections", AuthRejectionsSuppressed(940, SuppressedUnverified), map[string]any{
			"event": "auth.rejected", "decision": "deny", "reason": "suppressed", "count": float64(940), "stage": "unverified"}},
		{"raging/auth.rejected summary of suppressed verified-token rejections", AuthRejectionsSuppressed(7, SuppressedVerified), map[string]any{
			"event": "auth.rejected", "decision": "deny", "reason": "suppressed", "count": float64(7), "stage": "verified"}},
		{"happy/tool.called", ToolCalled(actor, "run_command", "lab-1", ToolArgs{Command: "uptime", WaitSeconds: i64(5), MergeStderr: yes()}), map[string]any{
			"event": "tool.called", "request_id": "req-1", "tenant": "default", "principal": "user:operator@example.net", "tool": "run_command", "server": "lab-1",
			"args": map[string]any{"command": "uptime", "wait_seconds": float64(5), "merge_stderr": true}}},
		{"edge/tool.called without a server", ToolCalled(actor, "list_servers", "", ToolArgs{}), map[string]any{
			"event": "tool.called", "request_id": "req-1", "tenant": "default", "principal": "user:operator@example.net", "tool": "list_servers", "args": map[string]any{}}},
		{"sad/policy.denied", PolicyDenied(actor, "run_command", "lab-1", "policy_denied", "sudo"), map[string]any{
			"event": "policy.denied", "request_id": "req-1", "tenant": "default", "principal": "user:operator@example.net", "tool": "run_command", "server": "lab-1",
			"decision": "deny", "code": "policy_denied", "rule": "sudo"}},
		{"sad/tool.failed", ToolFailed(actor, "run_command", "lab-1", "busy"), map[string]any{
			"event": "tool.failed", "request_id": "req-1", "tenant": "default", "principal": "user:operator@example.net", "tool": "run_command", "server": "lab-1",
			"code": "busy"}},
		{"happy/command.requested", CommandRequested(actor, "lab-1", "c1", "uptime", 600), map[string]any{
			"event": "command.requested", "request_id": "req-1", "tenant": "default", "principal": "user:operator@example.net", "tool": "run_command",
			"server": "lab-1", "command_id": "c1", "command": "uptime", "timeout_seconds": float64(600)}},
		{"happy/command.started", CommandStarted(actor, "lab-1", "c1"), map[string]any{
			"event": "command.started", "request_id": "req-1", "tenant": "default", "principal": "user:operator@example.net", "server": "lab-1", "command_id": "c1"}},
		{"happy/command.finished", CommandFinished(actor, "lab-1", "c1", Finish{Status: "failed", ExitCode: &exit, StdoutBytes: 10, StderrBytes: 2, Duration: 1500 * time.Millisecond}), map[string]any{
			"event": "command.finished", "request_id": "req-1", "tenant": "default", "principal": "user:operator@example.net", "server": "lab-1", "command_id": "c1",
			"status": "failed", "exit_code": float64(3), "stdout_bytes": float64(10), "stderr_bytes": float64(2), "truncated": false, "duration_ms": float64(1500)}},
		{"edge/command.finished by a signal with no output", CommandFinished(actor, "lab-1", "c1", Finish{Status: "killed", Signal: "TERM", Truncated: true}), map[string]any{
			"event": "command.finished", "request_id": "req-1", "tenant": "default", "principal": "user:operator@example.net", "server": "lab-1", "command_id": "c1",
			"status": "killed", "signal": "TERM", "stdout_bytes": float64(0), "stderr_bytes": float64(0), "truncated": true, "duration_ms": float64(0)}},
		{"happy/command.kill_requested", CommandKillRequested(actor, "c1", "TERM"), map[string]any{
			"event": "command.kill_requested", "request_id": "req-1", "tenant": "default", "principal": "user:operator@example.net", "tool": "kill_command",
			"command_id": "c1", "signal": "TERM"}},
		{"happy/file.downloaded requested", FileDownloadRequested(actor, "lab-1", "/var/log/syslog", 0, 4096), map[string]any{
			"event": "file.downloaded", "request_id": "req-1", "tenant": "default", "principal": "user:operator@example.net", "tool": "download_file",
			"server": "lab-1", "stage": "requested", "path": "/var/log/syslog", "offset": float64(0), "bytes": float64(4096)}},
		{"happy/file.downloaded done", FileDownloaded(actor, "lab-1", "/var/log/syslog", 0, 100), map[string]any{
			"event": "file.downloaded", "request_id": "req-1", "tenant": "default", "principal": "user:operator@example.net", "tool": "download_file",
			"server": "lab-1", "stage": "done", "path": "/var/log/syslog", "offset": float64(0), "bytes": float64(100)}},
		{"happy/file.uploaded requested", FileUploadRequested(actor, "lab-1", "/tmp/x", 12, "0644", false), map[string]any{
			"event": "file.uploaded", "request_id": "req-1", "tenant": "default", "principal": "user:operator@example.net", "tool": "upload_file",
			"server": "lab-1", "stage": "requested", "path": "/tmp/x", "bytes": float64(12), "mode": "0644", "overwrite": false}},
		{"happy/file.uploaded done", FileUploaded(actor, "lab-1", "/tmp/x", 12), map[string]any{
			"event": "file.uploaded", "request_id": "req-1", "tenant": "default", "principal": "user:operator@example.net", "tool": "upload_file",
			"server": "lab-1", "stage": "done", "path": "/tmp/x", "bytes": float64(12)}},
		{"happy/gateway.started", GatewayStarted("dev", "127.0.0.1:8822"), map[string]any{"event": "gateway.started", "version": "dev", "listen": "127.0.0.1:8822"}},
		{"happy/gateway.stopped", GatewayStopped(StopSignal), map[string]any{"event": "gateway.stopped", "reason": "signal"}},
	}
	seen := map[string]bool{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := encode(t, tt.ev)
			if got["ts"] != "2026-11-19T19:30:00.123Z" {
				t.Fatalf("ts = %v", got["ts"])
			}
			delete(got, "ts")
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got  %v\nwant %v", got, tt.want)
			}
			if tt.ev.Name() != tt.want["event"] {
				t.Fatalf("Name() = %q", tt.ev.Name())
			}
		})
		seen[tt.ev.Name()] = true
	}
	t.Run("happy/every event of spec 5.4 has a constructor under test", func(t *testing.T) {
		for _, name := range []string{EventAuthAccepted, EventAuthRejected, EventToolCalled, EventPolicyDenied, EventToolFailed, EventCommandRequested,
			EventCommandStarted, EventCommandFinished, EventCommandKillRequested, EventFileDownloaded, EventFileUploaded,
			EventGatewayStarted, EventGatewayStopped} {
			if !seen[name] {
				t.Errorf("no test builds %s", name)
			}
		}
	})
	t.Run("happy/stop reasons are fixed strings", func(t *testing.T) {
		if StopSignal.String() != "signal" || StopError.String() != "error" {
			t.Fatal(StopSignal, StopError)
		}
	})
	t.Run("happy/reject reasons are fixed strings", func(t *testing.T) {
		got := []string{RejectMissingToken.String(), RejectInvalidToken.String(), RejectUnmappedPrincipal.String(), RejectKeysUnavailable.String()}
		if strings.Join(got, ",") != "missing_token,invalid_token,unmapped_principal,keys_unavailable" {
			t.Fatalf("got %v", got)
		}
		got = []string{RejectMalformedToken.String(), RejectOversizedToken.String(), RejectBadAlgorithm.String(), RejectUnknownKey.String(),
			RejectBadSignature.String(), RejectWrongIssuer.String(), RejectWrongAudience.String(), RejectExpired.String(),
			RejectNotYetValid.String(), RejectIssuedInFuture.String(), RejectMissingClaim.String(), RejectAmbiguousIdentity.String()}
		want := "malformed_token,oversized_token,bad_algorithm,unknown_key,bad_signature,wrong_issuer,wrong_audience,expired,not_yet_valid,issued_in_future,missing_claim,ambiguous_identity"
		if strings.Join(got, ",") != want {
			t.Fatalf("got %v", got)
		}
	})
}

// jsonFields lists the JSON names of a struct type's fields.
func jsonFields(typ reflect.Type) []string {
	var out []string
	for f := range typ.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// TestNoSecretFields pins the field sets of the event record and of the
// audited tool arguments. A new field fails this test until it is added
// here, which is the moment to check that it cannot hold a token, key
// material, file content or command output.
func TestNoSecretFields(t *testing.T) {
	forbidden := []string{"token", "key", "secret", "password", "passphrase", "content", "output", "stdout", "stderr", "data", "authorization", "jwt", "assertion"}
	allowedAnyway := map[string]bool{"stdout_bytes": true, "stderr_bytes": true, "content_bytes": true, "merge_stderr": true}
	cases := []struct {
		name string
		typ  reflect.Type
		want string
	}{
		{"happy/record", reflect.TypeFor[record](), "args,bytes,code,command,command_id,count,decision,duration_ms,error_code,event,exit_code,listen,mode,offset,overwrite,path,principal,reason,request_id,rule,server,signal,stage,status,stderr_bytes,stdout_bytes,tenant,timeout_seconds,tool,truncated,ts,version"},
		{"happy/tool arguments", reflect.TypeFor[ToolArgs](), "command,command_id,content_bytes,cursor,limit,max_bytes,merge_stderr,mode,offset,overwrite,path,signal,status,stream,timeout_seconds,wait_seconds"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := jsonFields(c.typ)
			if strings.Join(got, ",") != c.want {
				t.Fatalf("fields changed:\ngot  %s\nwant %s", strings.Join(got, ","), c.want)
			}
			for _, f := range got {
				for _, bad := range forbidden {
					if strings.Contains(f, bad) && !allowedAnyway[f] {
						t.Errorf("field %q looks like it could hold %s", f, bad)
					}
				}
			}
		})
	}
	t.Run("sad/Event exposes no fields", func(t *testing.T) {
		for f := range reflect.TypeFor[Event]().Fields() {
			if f.IsExported() {
				t.Fatalf("Event has an exported field %s", f.Name)
			}
		}
	})
}
