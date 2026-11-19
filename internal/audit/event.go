package audit

import (
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// The event names of spec 5.4.
const (
	EventAuthAccepted         = "auth.accepted"
	EventAuthRejected         = "auth.rejected"
	EventToolCalled           = "tool.called"
	EventPolicyDenied         = "policy.denied"
	EventCommandRequested     = "command.requested"
	EventCommandStarted       = "command.started"
	EventCommandFinished      = "command.finished"
	EventCommandKillRequested = "command.kill_requested"
	EventFileDownloaded       = "file.downloaded"
	EventFileUploaded         = "file.uploaded"
	EventGatewayStarted       = "gateway.started"
	EventGatewayStopped       = "gateway.stopped"
)

// The stages of a file event: the "requested" form is written before the
// transfer (audit-before-act), the "done" form after it.
const (
	StageRequested = "requested"
	StageDone      = "done"
)

// Event is one audit record. Its fields are unexported: an Event can only be
// built by the constructors in this file, so no caller can log a free-form
// map, and no event has a field for a token, key material, file content or
// command output.
type Event struct {
	rec record
}

// Name returns the event's name, for example "command.requested".
func (e Event) Name() string { return e.rec.Event }

// record is the JSON form of an event. Common fields first (spec 5.4), then
// the event-specific ones. Every field is omitted when empty.
type record struct {
	TS        string           `json:"ts"`
	Event     string           `json:"event"`
	RequestID string           `json:"request_id,omitempty"`
	Tenant    tenant.TenantID  `json:"tenant,omitempty"`
	Principal string           `json:"principal,omitempty"`
	Tool      string           `json:"tool,omitempty"`
	Server    tenant.ServerID  `json:"server,omitempty"`
	CommandID tenant.CommandID `json:"command_id,omitempty"`
	Decision  string           `json:"decision,omitempty"`
	Code      string           `json:"code,omitempty"`
	Rule      string           `json:"rule,omitempty"`

	Reason         string    `json:"reason,omitempty"`
	Args           *ToolArgs `json:"args,omitempty"`
	Command        string    `json:"command,omitempty"`
	TimeoutSeconds int64     `json:"timeout_seconds,omitempty"`
	Status         string    `json:"status,omitempty"`
	ExitCode       *int      `json:"exit_code,omitempty"`
	Signal         string    `json:"signal,omitempty"`
	ErrorCode      string    `json:"error_code,omitempty"`
	StdoutBytes    *int64    `json:"stdout_bytes,omitempty"`
	StderrBytes    *int64    `json:"stderr_bytes,omitempty"`
	Truncated      *bool     `json:"truncated,omitempty"`
	DurationMS     *int64    `json:"duration_ms,omitempty"`
	Stage          string    `json:"stage,omitempty"`
	Path           string    `json:"path,omitempty"`
	Offset         *int64    `json:"offset,omitempty"`
	Bytes          *int64    `json:"bytes,omitempty"`
	Mode           string    `json:"mode,omitempty"`
	Overwrite      *bool     `json:"overwrite,omitempty"`
	Version        string    `json:"version,omitempty"`
	Listen         string    `json:"listen,omitempty"`
}

// Actor is who caused an event: the request it came in and the verified
// principal. The principal's tenant is the event's tenant.
type Actor struct {
	// RequestID correlates the events of one HTTP request.
	RequestID string
	// Principal is the verified caller.
	Principal tenant.Principal
}

func (a Actor) record(event string) record {
	r := record{Event: event, RequestID: a.RequestID, Tenant: a.Principal.Tenant}
	if a.Principal.Kind != 0 {
		r.Principal = a.Principal.String()
	}
	return r
}

// RejectReason says why a request was not authenticated. The values are
// fixed: the reason can never carry the token or any other caller text.
type RejectReason struct{ s string }

// The reasons for auth.rejected.
var (
	// RejectMissingToken: the request had no token header.
	RejectMissingToken = RejectReason{"missing_token"}
	// RejectInvalidToken: the token failed verification.
	RejectInvalidToken = RejectReason{"invalid_token"}
	// RejectUnmappedPrincipal: a valid token whose subject maps to no tenant.
	RejectUnmappedPrincipal = RejectReason{"unmapped_principal"}
	// RejectKeysUnavailable: the signing keys could not be fetched.
	RejectKeysUnavailable = RejectReason{"keys_unavailable"}
)

// String returns the reason's text.
func (r RejectReason) String() string { return r.s }

// ToolArgs are a tool call's arguments as audited. There is deliberately no
// field for upload content: only its size is recorded.
type ToolArgs struct {
	CommandID      tenant.CommandID `json:"command_id,omitempty"`
	Command        string           `json:"command,omitempty"`
	WaitSeconds    *int64           `json:"wait_seconds,omitempty"`
	TimeoutSeconds *int64           `json:"timeout_seconds,omitempty"`
	MergeStderr    *bool            `json:"merge_stderr,omitempty"`
	MaxBytes       *int64           `json:"max_bytes,omitempty"`
	Stream         string           `json:"stream,omitempty"`
	Offset         *int64           `json:"offset,omitempty"`
	Status         string           `json:"status,omitempty"`
	Limit          *int64           `json:"limit,omitempty"`
	Cursor         string           `json:"cursor,omitempty"`
	Signal         string           `json:"signal,omitempty"`
	Path           string           `json:"path,omitempty"`
	Mode           string           `json:"mode,omitempty"`
	Overwrite      *bool            `json:"overwrite,omitempty"`
	ContentBytes   *int64           `json:"content_bytes,omitempty"`
}

// AuthAccepted records a request whose token verified and mapped to p.
func AuthAccepted(requestID string, p tenant.Principal) Event {
	r := Actor{RequestID: requestID, Principal: p}.record(EventAuthAccepted)
	r.Decision = "allow"
	return Event{r}
}

// AuthRejected records a request that was not authenticated. p is the zero
// Principal unless the token was valid but mapped to no tenant, in which case
// p carries its kind and subject (and no tenant).
func AuthRejected(requestID string, reason RejectReason, p tenant.Principal) Event {
	r := Actor{RequestID: requestID, Principal: p}.record(EventAuthRejected)
	r.Decision, r.Reason = "deny", reason.s
	return Event{r}
}

// ToolCalled records every tool call with its arguments.
func ToolCalled(a Actor, tool string, server tenant.ServerID, args ToolArgs) Event {
	r := a.record(EventToolCalled)
	r.Tool, r.Server = tool, server
	r.Args = &args
	return Event{r}
}

// PolicyDenied records a policy decision that refused a call: code and rule
// are the decision's.
func PolicyDenied(a Actor, tool string, server tenant.ServerID, code, rule string) Event {
	r := a.record(EventPolicyDenied)
	r.Tool, r.Server, r.Decision, r.Code, r.Rule = tool, server, "deny", code, rule
	return Event{r}
}

// CommandRequested is written before the SSH session starts and carries the
// full command text (spec 5.4).
func CommandRequested(a Actor, server tenant.ServerID, id tenant.CommandID, command string, timeoutSeconds int64) Event {
	r := a.record(EventCommandRequested)
	r.Tool, r.Server, r.CommandID, r.Command, r.TimeoutSeconds = "run_command", server, id, command, timeoutSeconds
	return Event{r}
}

// CommandStarted records that the remote command started.
func CommandStarted(a Actor, server tenant.ServerID, id tenant.CommandID) Event {
	r := a.record(EventCommandStarted)
	r.Server, r.CommandID = server, id
	return Event{r}
}

// Finish is how a command ended.
type Finish struct {
	// Status is the record's final status (succeeded, failed, killed, ...).
	Status string
	// ExitCode is nil when the command did not exit (a signal or an error).
	ExitCode *int
	// Signal names the signal that ended it, if any.
	Signal string
	// ErrorCode is set when the gateway could not run it.
	ErrorCode string
	// StdoutBytes and StderrBytes count the bytes received.
	StdoutBytes, StderrBytes int64
	// Truncated is set when output passed max_output_bytes.
	Truncated bool
	// Duration is the time from start to end.
	Duration time.Duration
}

// CommandFinished records how a command ended. It carries counts, never
// output.
func CommandFinished(a Actor, server tenant.ServerID, id tenant.CommandID, f Finish) Event {
	r := a.record(EventCommandFinished)
	r.Server, r.CommandID = server, id
	r.Status, r.ExitCode, r.Signal, r.ErrorCode = f.Status, f.ExitCode, f.Signal, f.ErrorCode
	ms := f.Duration.Milliseconds()
	r.StdoutBytes, r.StderrBytes, r.Truncated, r.DurationMS = &f.StdoutBytes, &f.StderrBytes, &f.Truncated, &ms
	return Event{r}
}

// CommandKillRequested is written before a signal is sent.
func CommandKillRequested(a Actor, id tenant.CommandID, signal string) Event {
	r := a.record(EventCommandKillRequested)
	r.Tool, r.CommandID, r.Signal = "kill_command", id, signal
	return Event{r}
}

// FileDownloadRequested is the "requested" form of file.downloaded, written
// before the read starts. path is the cleaned path policy decided on.
func FileDownloadRequested(a Actor, server tenant.ServerID, path string, offset, maxBytes int64) Event {
	r := a.record(EventFileDownloaded)
	r.Tool, r.Server, r.Stage, r.Path, r.Offset, r.Bytes = "download_file", server, StageRequested, path, &offset, &maxBytes
	return Event{r}
}

// FileDownloaded is the "done" form: bytes is how many were returned.
func FileDownloaded(a Actor, server tenant.ServerID, path string, offset, bytes int64) Event {
	r := a.record(EventFileDownloaded)
	r.Tool, r.Server, r.Stage, r.Path, r.Offset, r.Bytes = "download_file", server, StageDone, path, &offset, &bytes
	return Event{r}
}

// FileUploadRequested is the "requested" form of file.uploaded, written
// before the write starts. It records the size and mode, never the content.
func FileUploadRequested(a Actor, server tenant.ServerID, path string, size int64, mode string, overwrite bool) Event {
	r := a.record(EventFileUploaded)
	r.Tool, r.Server, r.Stage, r.Path, r.Bytes, r.Mode, r.Overwrite = "upload_file", server, StageRequested, path, &size, mode, &overwrite
	return Event{r}
}

// FileUploaded is the "done" form: bytes is how many were written.
func FileUploaded(a Actor, server tenant.ServerID, path string, bytes int64) Event {
	r := a.record(EventFileUploaded)
	r.Tool, r.Server, r.Stage, r.Path, r.Bytes = "upload_file", server, StageDone, path, &bytes
	return Event{r}
}

// GatewayStarted records the start of the gateway.
func GatewayStarted(version, listen string) Event {
	return Event{record{Event: EventGatewayStarted, Version: version, Listen: listen}}
}

// GatewayStopped records a clean stop and why (for example "signal").
func GatewayStopped(reason string) Event {
	return Event{record{Event: EventGatewayStopped, Reason: reason}}
}
