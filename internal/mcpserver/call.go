package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/InfiniteRoomLabs/yasshm/internal/audit"
	"github.com/InfiniteRoomLabs/yasshm/internal/jobs"
	"github.com/InfiniteRoomLabs/yasshm/internal/policy"
	"github.com/InfiniteRoomLabs/yasshm/internal/sshclient"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// The ten tool error codes of spec 5.8, and no others.
const (
	CodeInvalidArgument = "invalid_argument"
	CodeNotFound        = "not_found"
	CodeAccessDenied    = "access_denied"
	CodePolicyDenied    = "policy_denied"
	CodeBusy            = "busy"
	CodeSSHUnreachable  = "ssh_unreachable"
	CodeSSHAuthFailed   = "ssh_auth_failed"
	CodeHostKeyMismatch = "host_key_mismatch"
	CodeTimeout         = "timeout"
	CodeInternal        = "internal"
)

// Codes lists the ten tool error codes.
var Codes = []string{
	CodeInvalidArgument, CodeNotFound, CodeAccessDenied, CodePolicyDenied, CodeBusy,
	CodeSSHUnreachable, CodeSSHAuthFailed, CodeHostKeyMismatch, CodeTimeout, CodeInternal,
}

// The fixed messages. A message never holds a host, user, key path, team
// domain, audience, gateway path, command text or another tenant's id.
const (
	msgInternal        = "internal error"
	msgShuttingDown    = "shutting down, retry later"
	msgAudit           = "the audit log could not be written, so nothing was done"
	msgNoServer        = "no such server"
	msgNoCommand       = "no such command"
	msgNoFile          = "no such file on the server"
	msgNotRegular      = "the path is not a regular file"
	msgFileExists      = "the file exists; set overwrite to replace it"
	msgTargetDenied    = "the account on the server may not access this path"
	msgAccessDenied    = "the server does not grant the access level this tool requires"
	msgPolicyDenied    = "the arguments hit a guard rail (a guard rail, not a boundary)"
	msgUnreachable     = "the server could not be reached"
	msgAuthFailed      = "the server refused the gateway's key"
	msgHostKeyMismatch = "the server's host key does not match the pinned key"
	msgTimeout         = "the operation timed out"
	msgTooLarge        = "the result would exceed max_result_bytes"
)

// ToolError is the structured content of a failed tool call (isError true).
type ToolError struct {
	// Code is one of Codes.
	Code string `json:"code"`
	// Message is fixed text.
	Message string `json:"message"`
	// Rule names the policy rule of a denial.
	Rule string `json:"rule,omitempty"`
}

// Error renders the error as its text content.
func (e *ToolError) Error() string {
	if e.Rule != "" {
		return fmt.Sprintf("error %s: %s (rule %s)", e.Code, e.Message, e.Rule)
	}
	return fmt.Sprintf("error %s: %s", e.Code, e.Message)
}

func toolErr(code, msg string) *ToolError { return &ToolError{Code: code, Message: msg} }

// errAudit marks a failed audit write of the gateway's own (tool.called,
// policy.denied, the requested form of a file event).
var errAudit = errors.New("audit write failed")

// result is a tool's successful answer: its structured content and its text
// rendering.
type result struct {
	structured any
	text       string
}

// call is the state of one tool call: who, in which tenant, and the
// target once resolved.
type call struct {
	actor audit.Actor
	t     *tenant.Tenant
	srv   *tenant.Server
	rec   jobs.Record
	// budget is how many bytes the encoded result may take.
	budget int
}

// plan is what a tool's prepare step found: the arguments as audited, the
// server the call is about, the policy input (nil when the call takes no
// policy decision), an error to answer instead of acting, and the action.
type plan struct {
	server tenant.ServerID
	args   audit.ToolArgs
	policy *policy.Args
	err    error
	act    func(ctx context.Context) (result, error)
}

// envelopeReserve is kept out of every result's budget for the JSON-RPC
// envelope around it (version, id, the result key), so the whole HTTP body
// stays within max_result_bytes for any ordinary request id.
const envelopeReserve = 512

// invoke is the one call path every tool takes (spec 5.8): the principal
// from the closure, the tenant, the arguments and the target, tool.called
// before anything else (nothing happens if it cannot be written), the policy
// decision (policy.denied on a denial), then the action. A panic is
// recovered, logged and answered as internal without its text.
func (g *Gateway) invoke(ctx context.Context, a audit.Actor, d *toolDef, raw json.RawMessage) (res *mcp.CallToolResult) {
	var server tenant.ServerID
	called := false
	defer func() {
		if r := recover(); r != nil {
			g.log.Error("tool panicked", "request_id", a.RequestID, "tool", d.name)
			if !called {
				_ = g.audit.Write(audit.ToolCalled(a, d.name, server, audit.ToolArgs{}))
			}
			res = g.fail(a, d.name, server, toolErr(CodeInternal, msgInternal))
		}
	}()
	c, terr := g.newCall(a)
	if terr != nil {
		called = true
		if err := g.audit.Write(audit.ToolCalled(a, d.name, "", audit.ToolArgs{})); err != nil {
			return g.auditFailed(a, d.name, err)
		}
		return g.fail(a, d.name, "", terr)
	}
	p := d.prepare(ctx, g, c, raw)
	server = p.server
	called = true
	if err := g.audit.Write(audit.ToolCalled(a, d.name, p.server, p.args)); err != nil {
		return g.auditFailed(a, d.name, err)
	}
	if p.err != nil {
		return g.fail(a, d.name, server, g.toolError(p.err))
	}
	if p.policy != nil {
		dec := g.policy.Decide(a.Principal, c.srv, d.pol, *p.policy)
		if !dec.Allow {
			if err := g.audit.Write(audit.PolicyDenied(a, d.name, server, string(dec.Code), dec.Rule)); err != nil {
				return g.auditFailed(a, d.name, err)
			}
			return errorResult(denial(dec))
		}
	}
	out, err := p.act(ctx)
	if err != nil {
		return g.fail(a, d.name, server, g.toolError(err))
	}
	r := successResult(out)
	if n := encodedLen(r); n > c.budget {
		g.log.Error("tool result over budget", "request_id", a.RequestID, "tool", d.name, "bytes", n, "budget", c.budget)
		return g.fail(a, d.name, server, toolErr(CodeInternal, msgTooLarge))
	}
	return r
}

// newCall resolves the caller's tenant. A missing principal (a handler
// mounted outside the guard) or a tenant the gateway does not have is
// internal: the guard maps every principal to a configured tenant.
func (g *Gateway) newCall(a audit.Actor) (*call, *ToolError) {
	if a.Principal.Tenant == "" || a.RequestID == "" {
		return nil, toolErr(CodeInternal, msgInternal)
	}
	t, ok := g.tenants[a.Principal.Tenant]
	if !ok {
		return nil, toolErr(CodeInternal, msgInternal)
	}
	return &call{actor: a, t: t, budget: int(t.Limits.MaxResultBytes) - envelopeReserve}, nil
}

// denial turns a policy decision into the tool error.
func denial(d policy.Decision) *ToolError {
	e := &ToolError{Code: string(d.Code), Rule: d.Rule}
	switch d.Code {
	case policy.CodeNotFound:
		e.Message = msgNoServer
	case policy.CodeAccessDenied:
		e.Message = msgAccessDenied
	default:
		e.Code, e.Message = CodePolicyDenied, msgPolicyDenied
	}
	return e
}

// fail writes tool.failed and answers the error. A failed write is logged:
// the call has already been refused.
func (g *Gateway) fail(a audit.Actor, tool string, server tenant.ServerID, e *ToolError) *mcp.CallToolResult {
	if err := g.audit.Write(audit.ToolFailed(a, tool, server, e.Code)); err != nil {
		g.log.Error("audit write failed", "request_id", a.RequestID, "tool", tool, "event", audit.EventToolFailed)
	}
	if e.Code == CodeInternal {
		g.log.Warn("tool failed", "request_id", a.RequestID, "tool", tool, "code", e.Code)
	}
	return errorResult(e)
}

// auditFailed answers a call whose own audit event could not be written:
// nothing was done, and the log refuses every later write until a restart.
func (g *Gateway) auditFailed(a audit.Actor, tool string, _ error) *mcp.CallToolResult {
	g.log.Error("audit write failed; tool call refused", "request_id", a.RequestID, "tool", tool)
	return errorResult(toolErr(CodeInternal, msgAudit))
}

// toolError maps an error from the arguments, the engine or the SSH layer to
// a tool error with fixed text.
func (g *Gateway) toolError(err error) *ToolError {
	var te *ToolError
	if errors.As(err, &te) {
		return te
	}
	var ae argError
	if errors.As(err, &ae) {
		return toolErr(CodeInvalidArgument, string(ae))
	}
	switch {
	case errors.Is(err, errAudit), errors.Is(err, jobs.ErrAuditFailed):
		return toolErr(CodeInternal, msgAudit)
	case errors.Is(err, jobs.ErrShuttingDown):
		return toolErr(CodeInternal, msgShuttingDown)
	}
	switch code := string(jobs.CodeOf(err)); code {
	case CodeBusy:
		var je *jobs.Error
		limit := "a concurrency limit"
		if errors.As(err, &je) && je.Limit() != "" {
			limit = je.Limit()
		}
		return toolErr(CodeBusy, "a limit is reached: "+limit)
	case CodeNotFound:
		return toolErr(CodeNotFound, msgNoCommand)
	case CodeInvalidArgument:
		return toolErr(CodeInvalidArgument, "an argument is out of range")
	case CodeSSHUnreachable:
		return toolErr(code, msgUnreachable)
	case CodeSSHAuthFailed:
		return toolErr(code, msgAuthFailed)
	case CodeHostKeyMismatch:
		return toolErr(code, msgHostKeyMismatch)
	case CodeTimeout:
		return toolErr(code, msgTimeout)
	default:
		return toolErr(CodeInternal, msgInternal)
	}
}

// fileError maps an error of an SFTP transfer: the file-specific cases
// first, then the SSH codes.
func (g *Gateway) fileError(err error) error {
	switch {
	case errors.Is(err, sshclient.ErrAccessLevel):
		return toolErr(CodeAccessDenied, msgAccessDenied)
	case sshclient.CodeOf(err) != sshclient.CodeInternal:
		return err
	case errors.Is(err, fs.ErrNotExist):
		return toolErr(CodeNotFound, msgNoFile)
	case errors.Is(err, sshclient.ErrNotRegular):
		return toolErr(CodeInvalidArgument, msgNotRegular)
	case errors.Is(err, fs.ErrExist):
		return toolErr(CodeInvalidArgument, msgFileExists)
	case errors.Is(err, fs.ErrPermission):
		return toolErr(CodeAccessDenied, msgTargetDenied)
	}
	return err
}

// successResult renders a result: the structured content encoded once
// (with HTML escaping, the larger form) and the text block.
func successResult(r result) *mcp.CallToolResult {
	b, err := json.Marshal(r.structured)
	if err != nil {
		return errorResult(toolErr(CodeInternal, msgInternal))
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: r.text}},
		StructuredContent: json.RawMessage(b),
	}
}

// errorResult renders a tool error: isError, the structured error and the
// same as text.
func errorResult(e *ToolError) *mcp.CallToolResult {
	b, err := json.Marshal(e)
	if err != nil {
		b = []byte(`{"code":"internal","message":"internal error"}`)
	}
	return &mcp.CallToolResult{
		IsError:           true,
		Content:           []mcp.Content{&mcp.TextContent{Text: e.Error()}},
		StructuredContent: json.RawMessage(b),
	}
}

// encodedLen is the size of r as the result member of a JSON-RPC response.
func encodedLen(r *mcp.CallToolResult) int {
	b, err := json.Marshal(r)
	if err != nil {
		return 1 << 30
	}
	return len(b)
}

// fitSlack covers numbers in a result growing between the empty build and
// the real one (offsets, counts).
const fitSlack = 96

// fit builds a result that fits c's budget. build makes the result for a
// payload budget: the JSON cost of the output it carries, counted once
// (fit assumes the payload appears twice, in the structured content and in
// the text). The first try is what an empty payload leaves; a result that
// still does not fit is rebuilt with less.
func (c *call) fit(build func(payload int) (result, error)) (result, error) {
	empty, err := build(0)
	if err != nil {
		return result{}, err
	}
	payload := (c.budget - encodedLen(successResult(empty)) - fitSlack) / 2
	for range 8 {
		r, err := build(max(payload, 0))
		if err != nil {
			return result{}, err
		}
		n := encodedLen(successResult(r))
		if n <= c.budget {
			return r, nil
		}
		payload -= (n-c.budget)/2 + fitSlack
	}
	return result{}, toolErr(CodeInternal, msgTooLarge)
}
