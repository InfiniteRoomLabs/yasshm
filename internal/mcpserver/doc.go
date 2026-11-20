// Package mcpserver is yasshm's MCP endpoint (spec 5.8): the eight tools and
// the HTTP surface that serves them over stateless Streamable HTTP with
// plain JSON responses.
//
// The HTTP surface (Gateway.Handler) is POST /mcp behind a concurrency cap,
// the authenticator's guard and auth.Require, GET /healthz without
// authentication, and 404 for everything else. For every /mcp request the
// gateway builds one MCP server whose tool handlers hold the verified
// principal and request id in a closure (spec F6); nothing the client sends
// is used as identity.
//
// Every tool takes one call path (Gateway.invoke): the tenant of the
// principal, strict argument decoding, the target looked up inside that
// tenant only, tool.called written before anything else (nothing happens if
// it cannot be written, spec D12), the policy decision (policy.denied on a
// denial), then the action. Results carry structured content and a text
// rendering whose encoded size stays within the tenant's max_result_bytes;
// failures are tool results with isError and one of ten codes, whose
// messages are fixed text: never a host, user, key path, team domain,
// audience, gateway path or another tenant's id (spec D8).
//
// Commands go through internal/jobs; file transfers and the probe call
// internal/sshclient directly with a deadline. It may import every internal
// package except cli.
package mcpserver
