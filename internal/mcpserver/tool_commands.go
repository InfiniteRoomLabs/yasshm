package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/audit"
	"github.com/InfiniteRoomLabs/yasshm/internal/jobs"
	"github.com/InfiniteRoomLabs/yasshm/internal/policy"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// runArgs is run_command's input.
type runArgs struct {
	Server         string `json:"server" jsonschema:"the server id, from list_servers"`
	Command        string `json:"command" jsonschema:"the shell command; the server's login shell runs it exactly as given"`
	WaitSeconds    *int64 `json:"wait_seconds,omitempty" jsonschema:"how long to wait for the command to finish; the tenant's default when absent, capped at its max_wait_seconds"`
	TimeoutSeconds *int64 `json:"timeout_seconds,omitempty" jsonschema:"the command is stopped after this long; the tenant's default when absent or 0, at most its max_timeout_seconds"`
	MergeStderr    *bool  `json:"merge_stderr,omitempty" jsonschema:"write stderr into stdout in arrival order (default true)"`
	MaxBytes       *int64 `json:"max_bytes,omitempty" jsonschema:"the most output bytes to return; clamped so the result fits max_result_bytes"`
}

// readArgs is read_output's input.
type readArgs struct {
	CommandID   string `json:"command_id" jsonschema:"the command id, from run_command or list_commands"`
	Stream      string `json:"stream,omitempty" jsonschema:"stdout (default) or stderr"`
	Offset      *int64 `json:"offset,omitempty" jsonschema:"where to read: from the start when 0 or more, from the end when negative"`
	MaxBytes    *int64 `json:"max_bytes,omitempty" jsonschema:"the most bytes to return; clamped so the result fits max_result_bytes"`
	WaitSeconds *int64 `json:"wait_seconds,omitempty" jsonschema:"wait up to this long for output past offset (default 0, capped at max_wait_seconds)"`
}

// listArgs is list_commands' input.
type listArgs struct {
	Server string `json:"server,omitempty" jsonschema:"only commands of this server"`
	Status string `json:"status,omitempty" jsonschema:"only commands with this status"`
	Limit  *int64 `json:"limit,omitempty" jsonschema:"page size, 1 to 100 (default 20)"`
	Cursor string `json:"cursor,omitempty" jsonschema:"next_cursor of the previous page"`
}

// killArgs is kill_command's input.
type killArgs struct {
	CommandID string `json:"command_id" jsonschema:"the command id"`
	Signal    string `json:"signal,omitempty" jsonschema:"TERM (default), INT or KILL"`
}

// CommandStatus is the part of a command record every command tool shows.
type CommandStatus struct {
	CommandID string `json:"command_id"`
	Server    string `json:"server"`
	Status    string `json:"status"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	Signal    string `json:"signal,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
	// MayStillRun is set when the gateway cannot tell that the remote
	// process stopped.
	MayStillRun bool `json:"may_still_run,omitempty"`
}

// RunResult is run_command's structured content. Its output fields equal
// read_output's at offset 0 with the same max_bytes; with merge_stderr false
// stderr gets what stdout leaves of the result.
type RunResult struct {
	CommandStatus
	MergeStderr      bool   `json:"merge_stderr"`
	Stdout           string `json:"stdout"`
	Stderr           string `json:"stderr"`
	NextOffset       int64  `json:"next_offset"`
	StderrNextOffset int64  `json:"stderr_next_offset"`
	EOF              bool   `json:"eof"`
	Lossy            bool   `json:"lossy"`
	Truncated        bool   `json:"truncated"`
	StdoutBytes      int64  `json:"stdout_bytes"`
	StderrBytes      int64  `json:"stderr_bytes"`
}

// ReadResult is read_output's structured content.
type ReadResult struct {
	CommandStatus
	Stream     string `json:"stream"`
	Offset     int64  `json:"offset"`
	Data       string `json:"data"`
	NextOffset int64  `json:"next_offset"`
	Size       int64  `json:"size"`
	EOF        bool   `json:"eof"`
	Lossy      bool   `json:"lossy"`
	Truncated  bool   `json:"truncated"`
}

// CommandEntry is one command in list_commands.
type CommandEntry struct {
	CommandStatus
	// Command is the first 200 characters of the command text.
	Command          string `json:"command"`
	CommandTruncated bool   `json:"command_truncated,omitempty"`
	CreatedAt        string `json:"created_at"`
	StartedAt        string `json:"started_at,omitempty"`
	FinishedAt       string `json:"finished_at,omitempty"`
	StdoutBytes      int64  `json:"stdout_bytes"`
	StderrBytes      int64  `json:"stderr_bytes"`
}

// ListCommandsResult is list_commands' structured content.
type ListCommandsResult struct {
	Commands   []CommandEntry `json:"commands"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

// KillResult is kill_command's structured content.
type KillResult struct {
	CommandStatus
	SignalDelivered bool `json:"signal_delivered"`
}

// outputReserve is kept out of an output page's budget for everything that
// is not output (fields, the text header), so run_command and read_output
// give the same page for the same max_bytes.
const outputReserve = 2048

// commandPreviewRunes is how much of a command list_commands shows.
const commandPreviewRunes = 200

func status(r *jobs.Record) CommandStatus {
	return CommandStatus{
		CommandID: string(r.ID), Server: string(r.Server), Status: string(r.Status),
		ExitCode: r.ExitCode, Signal: r.Signal, ErrorCode: r.ErrorCode, MayStillRun: r.MayStillRun,
	}
}

func statusLine(s CommandStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "command %s on %s: %s", s.CommandID, s.Server, s.Status)
	if s.ExitCode != nil {
		fmt.Fprintf(&b, ", exit code %d", *s.ExitCode)
	}
	if s.Signal != "" {
		fmt.Fprintf(&b, ", signal %s", s.Signal)
	}
	if s.ErrorCode != "" {
		fmt.Fprintf(&b, ", error %s", s.ErrorCode)
	}
	if s.MayStillRun {
		b.WriteString(", may still run")
	}
	return b.String()
}

func secs(n int64) time.Duration { return time.Duration(n) * time.Second }

// clampBytes is the most raw bytes an output read asks for: max_bytes when
// given and smaller, else the result budget (the JSON cost of a byte is at
// least 1, so more than that can never fit).
func (c *call) clampBytes(maxBytes *int64) int64 {
	n := int64(c.budget)
	if maxBytes != nil && *maxBytes > 0 && *maxBytes < n {
		n = *maxBytes
	}
	return n
}

// payloadBudget is the JSON cost one copy of an output page may take.
func (c *call) payloadBudget() int { return max((c.budget-outputReserve)/2, 0) }

func prepareRun(_ context.Context, g *Gateway, c *call, raw json.RawMessage) plan {
	var a runArgs
	if err := decodeArgs(raw, []string{"server", "command", "wait_seconds", "timeout_seconds", "merge_stderr", "max_bytes"}, maxArgBytes, &a); err != nil {
		return plan{err: err}
	}
	p := plan{
		server: sid(a.Server), policy: &policy.Args{Command: a.Command},
		args: audit.ToolArgs{Command: a.Command, WaitSeconds: a.WaitSeconds, TimeoutSeconds: a.TimeoutSeconds, MergeStderr: a.MergeStderr, MaxBytes: a.MaxBytes},
	}
	l := c.t.Limits
	switch {
	case checkServerID(a.Server, true) != nil:
		p.err = checkServerID(a.Server, true)
	case checkNonNegative(a.WaitSeconds, "wait_seconds") != nil:
		p.err = checkNonNegative(a.WaitSeconds, "wait_seconds")
	case checkNonNegative(a.TimeoutSeconds, "timeout_seconds") != nil:
		p.err = checkNonNegative(a.TimeoutSeconds, "timeout_seconds")
	case a.TimeoutSeconds != nil && *a.TimeoutSeconds > l.MaxTimeoutSeconds:
		p.err = argError("timeout_seconds must not be above the tenant's max_timeout_seconds")
	case checkNonNegative(a.MaxBytes, "max_bytes") != nil:
		p.err = checkNonNegative(a.MaxBytes, "max_bytes")
	}
	if p.err != nil {
		return p
	}
	c.resolveServer(a.Server)
	p.act = func(ctx context.Context) (result, error) {
		wait := l.DefaultWaitSeconds
		if a.WaitSeconds != nil {
			wait = *a.WaitSeconds
		}
		wait = min(wait, l.MaxWaitSeconds)
		var timeout int64
		if a.TimeoutSeconds != nil {
			timeout = *a.TimeoutSeconds
		}
		rec, err := g.jobs.Start(ctx, c.t.ID, c.actor, sid(a.Server), a.Command, jobs.StartOptions{TimeoutSeconds: timeout, MergeStderr: a.MergeStderr})
		if err != nil {
			return result{}, err
		}
		if wait > 0 {
			if _, err := g.jobs.Wait(ctx, c.t.ID, rec.ID, secs(wait)); err != nil {
				return result{}, err
			}
		}
		return g.runPage(ctx, c, rec.ID, a.MaxBytes)
	}
	return p
}

// runPage reads both streams at offset 0 and renders run_command's answer.
func (g *Gateway) runPage(ctx context.Context, c *call, id tenant.CommandID, maxBytes *int64) (result, error) {
	n := c.clampBytes(maxBytes)
	out, err := g.jobs.ReadOutput(ctx, c.t.ID, id, jobs.ReadRequest{Stream: jobs.StreamStdout, MaxBytes: n})
	if err != nil {
		return result{}, err
	}
	errOut, err := g.jobs.ReadOutput(ctx, c.t.ID, id, jobs.ReadRequest{Stream: jobs.StreamStderr, MaxBytes: n})
	if err != nil {
		return result{}, err
	}
	rec := out.Record
	return c.fit(func(payload int) (result, error) {
		payload = min(payload, c.payloadBudget())
		so := fitText(out.Data, payload, out.EOF)
		se := fitText(errOut.Data, payload-jsonStringCost(so.text)+2, errOut.EOF)
		r := RunResult{
			CommandStatus: status(&rec), MergeStderr: rec.MergeStderr,
			Stdout: so.text, Stderr: se.text,
			NextOffset: out.Start + int64(so.used), StderrNextOffset: errOut.Start + int64(se.used),
			EOF:   out.EOF && so.used == len(out.Data) && errOut.EOF && se.used == len(errOut.Data),
			Lossy: so.lossy || se.lossy, Truncated: rec.Truncated,
			StdoutBytes: rec.StdoutBytes, StderrBytes: rec.StderrBytes,
		}
		var b strings.Builder
		b.WriteString(statusLine(r.CommandStatus))
		fmt.Fprintf(&b, "; stdout bytes 0..%d of %d", r.NextOffset, out.Size)
		if r.Lossy {
			b.WriteString(", lossy")
		}
		if r.EOF {
			b.WriteString(", eof")
		}
		b.WriteString("\n")
		b.WriteString(r.Stdout)
		if !rec.MergeStderr {
			fmt.Fprintf(&b, "\n--- stderr bytes 0..%d of %d ---\n", r.StderrNextOffset, errOut.Size)
			b.WriteString(r.Stderr)
		}
		return result{structured: r, text: b.String()}, nil
	})
}

// resolveCommand reads the record in the caller's tenant and resolves its
// server there too, for the policy decision.
func (c *call) resolveCommand(ctx context.Context, g *Gateway, id string) error {
	rec, err := g.jobs.Get(ctx, c.t.ID, tenant.CommandID(id))
	if err != nil {
		return err
	}
	c.rec = rec
	c.resolveServer(string(rec.Server))
	return nil
}

func prepareRead(ctx context.Context, g *Gateway, c *call, raw json.RawMessage) plan {
	var a readArgs
	if err := decodeArgs(raw, []string{"command_id", "stream", "offset", "max_bytes", "wait_seconds"}, maxArgBytes, &a); err != nil {
		return plan{err: err}
	}
	p := plan{args: audit.ToolArgs{CommandID: tenant.CommandID(a.CommandID), Stream: a.Stream, Offset: a.Offset, MaxBytes: a.MaxBytes, WaitSeconds: a.WaitSeconds}}
	if a.Stream == "" {
		a.Stream = jobs.StreamStdout
	}
	switch {
	case checkCommandID(a.CommandID) != nil:
		p.err = checkCommandID(a.CommandID)
	case a.Stream != jobs.StreamStdout && a.Stream != jobs.StreamStderr:
		p.err = argError("stream must be stdout or stderr")
	case checkNonNegative(a.MaxBytes, "max_bytes") != nil:
		p.err = checkNonNegative(a.MaxBytes, "max_bytes")
	case checkNonNegative(a.WaitSeconds, "wait_seconds") != nil:
		p.err = checkNonNegative(a.WaitSeconds, "wait_seconds")
	}
	if p.err != nil {
		return p
	}
	if err := c.resolveCommand(ctx, g, a.CommandID); err != nil {
		p.err = err
		return p
	}
	p.server, p.policy = c.rec.Server, &policy.Args{}
	p.act = func(ctx context.Context) (result, error) {
		req := jobs.ReadRequest{Stream: a.Stream, MaxBytes: c.clampBytes(a.MaxBytes)}
		if a.Offset != nil {
			req.Offset = *a.Offset
		}
		if a.WaitSeconds != nil {
			req.Wait = secs(min(*a.WaitSeconds, c.t.Limits.MaxWaitSeconds))
		}
		out, err := g.jobs.ReadOutput(ctx, c.t.ID, c.rec.ID, req)
		if err != nil {
			return result{}, err
		}
		rec := out.Record
		return c.fit(func(payload int) (result, error) {
			payload = min(payload, c.payloadBudget())
			t := fitText(out.Data, payload, out.EOF)
			r := ReadResult{
				CommandStatus: status(&rec), Stream: a.Stream, Offset: out.Start, Data: t.text,
				NextOffset: out.Start + int64(t.used), Size: out.Size,
				EOF: out.EOF && t.used == len(out.Data), Lossy: t.lossy, Truncated: rec.Truncated,
			}
			var b strings.Builder
			b.WriteString(statusLine(r.CommandStatus))
			fmt.Fprintf(&b, "; %s bytes %d..%d of %d", r.Stream, r.Offset, r.NextOffset, r.Size)
			if r.Lossy {
				b.WriteString(", lossy")
			}
			if r.EOF {
				b.WriteString(", eof")
			}
			b.WriteString("\n")
			b.WriteString(r.Data)
			return result{structured: r, text: b.String()}, nil
		})
	}
	return p
}

func prepareList(_ context.Context, g *Gateway, c *call, raw json.RawMessage) plan {
	var a listArgs
	if err := decodeArgs(raw, []string{"server", "status", "limit", "cursor"}, maxArgBytes, &a); err != nil {
		return plan{err: err}
	}
	p := plan{server: sid(a.Server), args: audit.ToolArgs{Status: a.Status, Limit: a.Limit, Cursor: a.Cursor}}
	switch {
	case checkServerID(a.Server, false) != nil:
		p.err = checkServerID(a.Server, false)
	case checkStatus(a.Status) != nil:
		p.err = checkStatus(a.Status)
	case a.Limit != nil && (*a.Limit < 1 || *a.Limit > 100):
		p.err = argError("limit must be 1 to 100")
	case a.Cursor != "" && !cursorPattern.MatchString(a.Cursor):
		p.err = argError("cursor is not one list_commands returned")
	}
	if p.err != nil {
		return p
	}
	if a.Server != "" {
		// A named server is decided like any other: not_found for one that
		// is not in the caller's tenant.
		c.resolveServer(a.Server)
		p.policy = &policy.Args{}
	}
	p.act = func(ctx context.Context) (result, error) {
		f := jobs.ListFilter{Server: sid(a.Server), Status: jobs.Status(a.Status), Limit: jobs.DefaultListLimit, Cursor: a.Cursor}
		if a.Limit != nil {
			f.Limit = int(*a.Limit)
		}
		return g.listPage(ctx, c, f)
	}
	return p
}

// listPage lists one page and, when it does not fit the result budget,
// lists again with a smaller limit, so next_cursor continues right after
// the last command shown.
func (g *Gateway) listPage(ctx context.Context, c *call, f jobs.ListFilter) (result, error) {
	for range 4 {
		page, err := g.jobs.List(ctx, c.t.ID, f)
		if err != nil {
			return result{}, err
		}
		res := ListCommandsResult{Commands: make([]CommandEntry, 0, len(page.Records)), NextCursor: page.NextCursor}
		var text strings.Builder
		cost := encodedLen(successResult(result{structured: res, text: "no commands\n"})) + fitSlack
		fits := len(page.Records)
		for i := range page.Records {
			e := commandEntry(&page.Records[i])
			line := commandLine(e)
			b, err := json.Marshal(e)
			if err != nil {
				return result{}, err
			}
			cost += len(b) + 1 + jsonStringCost(line) - 2
			if cost > c.budget {
				fits = i
				break
			}
			res.Commands = append(res.Commands, e)
			text.WriteString(line)
		}
		if fits < len(page.Records) && fits > 0 {
			f.Limit = fits
			continue
		}
		if len(res.Commands) == 0 {
			text.WriteString("no commands\n")
		}
		if res.NextCursor != "" {
			text.WriteString("more: pass next_cursor " + res.NextCursor + "\n")
		}
		return result{structured: res, text: text.String()}, nil
	}
	return result{}, toolErr(CodeInternal, msgTooLarge)
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func commandEntry(r *jobs.Record) CommandEntry {
	cmd, cut := cutRunes(r.Command, commandPreviewRunes)
	return CommandEntry{
		CommandStatus: status(r), Command: cmd, CommandTruncated: cut,
		CreatedAt: stamp(r.CreatedAt), StartedAt: stamp(r.StartedAt), FinishedAt: stamp(r.FinishedAt),
		StdoutBytes: r.StdoutBytes, StderrBytes: r.StderrBytes,
	}
}

func commandLine(e CommandEntry) string {
	cmd := e.Command
	if e.CommandTruncated {
		cmd += "..."
	}
	return fmt.Sprintf("%s (created %s): %q\n", statusLine(e.CommandStatus), e.CreatedAt, cmd)
}

func prepareKill(ctx context.Context, g *Gateway, c *call, raw json.RawMessage) plan {
	var a killArgs
	if err := decodeArgs(raw, []string{"command_id", "signal"}, maxArgBytes, &a); err != nil {
		return plan{err: err}
	}
	p := plan{args: audit.ToolArgs{CommandID: tenant.CommandID(a.CommandID), Signal: a.Signal}}
	if a.Signal == "" {
		a.Signal = jobs.SignalTerm
	}
	switch {
	case checkCommandID(a.CommandID) != nil:
		p.err = checkCommandID(a.CommandID)
	case a.Signal != jobs.SignalTerm && a.Signal != jobs.SignalInt && a.Signal != jobs.SignalKill:
		p.err = argError("signal must be TERM, INT or KILL")
	}
	if p.err != nil {
		return p
	}
	if err := c.resolveCommand(ctx, g, a.CommandID); err != nil {
		p.err = err
		return p
	}
	p.server, p.policy = c.rec.Server, &policy.Args{}
	p.act = func(ctx context.Context) (result, error) {
		rec, delivered, err := g.jobs.Kill(ctx, c.t.ID, c.actor, c.rec.ID, a.Signal)
		if err != nil {
			return result{}, err
		}
		r := KillResult{CommandStatus: status(&rec), SignalDelivered: delivered}
		text := statusLine(r.CommandStatus)
		if delivered {
			text += "; " + a.Signal + " delivered\n"
		} else {
			text += "; no signal delivered\n"
		}
		return result{structured: r, text: text}, nil
	}
	return p
}
