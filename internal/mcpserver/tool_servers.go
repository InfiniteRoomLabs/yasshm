package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/policy"
	"github.com/InfiniteRoomLabs/yasshm/internal/sshclient"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// listServersArgs is list_servers' input: nothing.
type listServersArgs struct{}

// serverArgs is get_server_info's input.
type serverArgs struct {
	Server string `json:"server" jsonschema:"the server id, from list_servers"`
}

// ServerEntry is one server as the model may see it: never its host, port,
// user or key (D8).
type ServerEntry struct {
	ID          string   `json:"id"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Access      string   `json:"access"`
}

// ListServersResult is list_servers' structured content.
type ListServersResult struct {
	Servers []ServerEntry `json:"servers"`
	// Truncated is set when the list was cut to fit max_result_bytes.
	Truncated bool `json:"truncated,omitempty"`
}

// Facts are what the probe learned. Every value comes from the target and is
// untrusted. The login name the probe also reads is not shown (D8).
type Facts struct {
	Kernel        string `json:"kernel,omitempty"`
	Arch          string `json:"arch,omitempty"`
	OS            string `json:"os,omitempty"`
	UptimeSeconds int64  `json:"uptime_seconds,omitempty"`
}

// ServerInfoResult is get_server_info's structured content.
type ServerInfoResult struct {
	ServerEntry
	// Reachable is true when the SSH handshake completed.
	Reachable bool `json:"reachable"`
	// LatencyMS is how long the probe took, connect included.
	LatencyMS int64 `json:"latency_ms"`
	// HostKeyOK is whether the server's host key matched the pin; absent
	// when the probe never got that far.
	HostKeyOK *bool `json:"host_key_ok,omitempty"`
	// Facts is set when the probe ran.
	Facts *Facts `json:"facts,omitempty"`
	// ErrorCode says why the probe failed.
	ErrorCode string `json:"error_code,omitempty"`
}

func entry(s *tenant.Server) ServerEntry {
	e := ServerEntry{ID: string(s.ID), Description: s.Description, Access: s.Access.String()}
	if len(s.Tags) > 0 {
		e.Tags = append([]string(nil), s.Tags...)
	}
	return e
}

// prepareListServers lists the caller's tenant's servers. It names no server,
// so it takes no policy decision: a verified principal is enough, and only
// the principal's own tenant is read.
func prepareListServers(_ context.Context, _ *Gateway, c *call, raw json.RawMessage) plan {
	var a listServersArgs
	if err := decodeArgs(raw, nil, maxArgBytes, &a); err != nil {
		return plan{err: err}
	}
	return plan{act: func(context.Context) (result, error) {
		res := ListServersResult{Servers: make([]ServerEntry, 0, len(c.t.Servers))}
		var text strings.Builder
		cost := encodedLen(successResult(result{structured: ListServersResult{Servers: []ServerEntry{}, Truncated: true}, text: truncNote})) + fitSlack
		for i := range c.t.Servers {
			e := entry(&c.t.Servers[i])
			line := serverLine(e)
			b, err := json.Marshal(e)
			if err != nil {
				return result{}, err
			}
			cost += len(b) + 1 + jsonStringCost(line) - 2
			if cost > c.budget {
				res.Truncated = true
				text.WriteString(truncNote)
				break
			}
			res.Servers = append(res.Servers, e)
			text.WriteString(line)
		}
		if len(res.Servers) == 0 && !res.Truncated {
			text.WriteString("no servers\n")
		}
		return result{structured: res, text: text.String()}, nil
	}}
}

// truncNote ends a list that was cut to fit the result.
const truncNote = "(list truncated to fit max_result_bytes)\n"

func serverLine(e ServerEntry) string {
	line := e.ID + " (" + e.Access + ")"
	if e.Description != "" {
		line += ": " + e.Description
	}
	if len(e.Tags) > 0 {
		line += " [" + strings.Join(e.Tags, ", ") + "]"
	}
	return line + "\n"
}

// resolveServer looks the server up in the caller's tenant only; nil when
// it is not there (policy then answers not_found, the same for a server of
// another tenant).
func (c *call) resolveServer(id string) {
	if s, ok := c.t.Server(tenant.ServerID(id)); ok {
		c.srv = s
	}
}

func prepareServerInfo(_ context.Context, g *Gateway, c *call, raw json.RawMessage) plan {
	var a serverArgs
	if err := decodeArgs(raw, []string{"server"}, maxArgBytes, &a); err != nil {
		return plan{err: err}
	}
	if err := checkServerID(a.Server, true); err != nil {
		return plan{err: err}
	}
	c.resolveServer(a.Server)
	return plan{server: sid(a.Server), policy: &policy.Args{}, act: func(ctx context.Context) (result, error) {
		return g.serverInfo(ctx, c)
	}}
}

// serverInfo runs the probe with a deadline. A failed probe is a successful
// answer with reachable false and the error code.
func (g *Gateway) serverInfo(ctx context.Context, c *call) (result, error) {
	l := c.t.Limits
	pctx, cancel := context.WithTimeout(ctx, time.Duration(l.ConnectTimeoutSeconds)*time.Second+probeExtra)
	defer cancel()
	start := time.Now()
	f, err := g.ssh[c.t.ID].Probe(pctx, c.t.ID, c.srv)
	res := ServerInfoResult{ServerEntry: entry(c.srv), LatencyMS: time.Since(start).Milliseconds()}
	if err == nil {
		res.Reachable, res.HostKeyOK = true, boolp(true)
		res.Facts = &Facts{Kernel: f.Kernel, Arch: f.Arch, OS: f.OS, UptimeSeconds: int64(f.Uptime / time.Second)}
	} else {
		if errors.Is(ctx.Err(), context.Canceled) {
			return result{}, ctx.Err() // the caller went away
		}
		code := sshclient.CodeOf(err)
		res.ErrorCode = string(code)
		switch code {
		case sshclient.CodeHostKeyMismatch:
			res.Reachable, res.HostKeyOK = true, boolp(false)
		case sshclient.CodeAuthFailed:
			res.Reachable, res.HostKeyOK = true, boolp(true)
		}
	}
	return result{structured: res, text: serverInfoText(res)}, nil
}

func serverInfoText(r ServerInfoResult) string {
	var b strings.Builder
	b.WriteString(serverLine(r.ServerEntry))
	fmt.Fprintf(&b, "reachable: %v, latency %d ms", r.Reachable, r.LatencyMS)
	if r.HostKeyOK != nil {
		fmt.Fprintf(&b, ", host key ok: %v", *r.HostKeyOK)
	}
	if r.ErrorCode != "" {
		fmt.Fprintf(&b, ", error: %s", r.ErrorCode)
	}
	b.WriteString("\n")
	if r.Facts != nil {
		fmt.Fprintf(&b, "kernel: %s\narch: %s\nos: %s\nuptime: %d s\n", r.Facts.Kernel, r.Facts.Arch, r.Facts.OS, r.Facts.UptimeSeconds)
	}
	return b.String()
}
