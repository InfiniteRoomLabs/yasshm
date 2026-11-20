// Package e2e is the end-to-end suite (spec section 7): it builds the real
// yasshm binary once per test run with the flags of scripts/build.sh, starts
// it as a child process (`yasshm serve` on a loopback port 0, its own data
// directory, audit log, config and keys per scenario), and drives it with
// the MCP Go SDK client through the stand-in edge. The package holds no code
// of its own: only this file and tests.
//
// What the suite may import, and why (enforced by internal/deprule):
//
//   - internal/testkit/... -- the world around the binary: sshtest is the
//     target, accesstest the issuer, edgetest the edge, configtest a
//     config, leakcheck and loopback bounded polls and refusing ports.
//   - internal/audit and internal/store -- only to read what the gateway
//     wrote (the audit log; the database only after the gateway stopped).
//     The suite parses the audit log itself today and imports neither.
//
// It never imports the packages that compute the gateway's answers
// (internal/mcpserver, internal/jobs, internal/policy, internal/auth,
// internal/sshclient, internal/cli): every answer a scenario checks comes
// from the binary.
//
// Every top-level test is named TestE2E<scenario>, and the gate's e2e step
// counts them with `go test -list` and fails unless as many report PASS. The
// step is the only gate step that runs this package; the test step leaves
// it out.
package e2e
