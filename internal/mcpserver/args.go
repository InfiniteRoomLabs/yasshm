package mcpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"slices"
	"unicode/utf8"

	"github.com/InfiniteRoomLabs/yasshm/internal/jobs"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// The patterns ids and enums must match before they are used (spec 5.1 and
// 5.6). Each expression is written once: the validator, its message and the
// schema's pattern all use these strings.
const (
	serverIDExpr  = `^[a-z0-9][a-z0-9-]{0,62}$`
	commandIDExpr = `^[0-9a-f]{32}$`
	// cursorExpr is the shape of a jobs cursor: "c1." then base64url. The
	// engine checks its content; this only bounds what reaches it.
	cursorExpr = `^[A-Za-z0-9._-]{1,256}$`
	// modeExpr is an octal permission mode of at most 0777.
	modeExpr = `^0?[0-7]{3}$`
)

var (
	serverIDPattern  = regexp.MustCompile(serverIDExpr)
	commandIDPattern = regexp.MustCompile(commandIDExpr)
	cursorPattern    = regexp.MustCompile(cursorExpr)
	modePattern      = regexp.MustCompile(modeExpr)
)

// maxArgBytes bounds the arguments object of every tool but upload_file. A
// command is at most 65536 bytes (the largest max_command_bytes) and a path
// 4096; the rest are short.
const maxArgBytes = 96 << 10

// argError is an invalid_argument with a fixed message: it names the
// argument and the rule, never the value the caller sent.
type argError string

func (e argError) Error() string { return string(e) }

// errArgsShape and the rest are the fixed messages of decoding failures.
const (
	errArgsShape    argError = "arguments must be one JSON object"
	errArgsTooLarge argError = "arguments are too large"
	errArgsDup      argError = "an argument is given more than once"
	errArgsUnknown  argError = "an argument is not one this tool takes"
	errArgsType     argError = "an argument has the wrong type"
)

// decodeArgs decodes raw into dst strictly: raw must be one JSON object (an
// absent or null object is {}), every key must be one of allowed exactly
// (case-sensitive) and appear once, and every value must have the field's
// type. limit bounds the encoded size. The errors are fixed texts.
func decodeArgs(raw json.RawMessage, allowed []string, limit int, dst any) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		raw = []byte("{}")
	}
	if len(raw) > limit {
		return errArgsTooLarge
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return errArgsShape
	}
	seen := map[string]bool{}
	for dec.More() {
		k, kerr := dec.Token()
		if kerr != nil {
			return errArgsShape
		}
		key, ok := k.(string)
		if !ok {
			return errArgsShape
		}
		if !slices.Contains(allowed, key) {
			return errArgsUnknown
		}
		if seen[key] {
			return errArgsDup
		}
		seen[key] = true
		var skip json.RawMessage
		if derr := dec.Decode(&skip); derr != nil {
			return errArgsShape
		}
	}
	if _, err := dec.Token(); err != nil {
		return errArgsShape
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errArgsShape
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return errArgsType
	}
	return nil
}

// checkServerID validates a server argument.
func checkServerID(s string, required bool) error {
	if s == "" {
		if required {
			return argError("server is required")
		}
		return nil
	}
	if !serverIDPattern.MatchString(s) {
		return argError("server must match " + serverIDExpr)
	}
	return nil
}

// checkCommandID validates a command_id argument.
func checkCommandID(s string) error {
	if s == "" {
		return argError("command_id is required")
	}
	if !commandIDPattern.MatchString(s) {
		return argError("command_id must be 32 lower-case hex digits")
	}
	return nil
}

// checkNonNegative validates an optional count argument.
func checkNonNegative(v *int64, name string) error {
	if v != nil && *v < 0 {
		return argError(name + " must not be negative")
	}
	return nil
}

// checkStatus validates a status filter.
func checkStatus(s string) error {
	if s != "" && !jobs.Status(s).Valid() {
		return argError("status must be one of running, succeeded, failed, killed, timed_out, lost, error")
	}
	return nil
}

// argIf is an invalid_argument with msg when bad is true, else nil; with
// cmp.Or it reads the checks in order and keeps the first failure.
func argIf(bad bool, msg string) error {
	if bad {
		return argError(msg)
	}
	return nil
}

// sid converts a validated server argument.
func sid(s string) tenant.ServerID { return tenant.ServerID(s) }

// auditInvalid stands for an id or enum argument that did not match its
// pattern: it matches no pattern itself, so it cannot be mistaken for one.
const auditInvalid = "(invalid)"

// auditTruncated marks a free-text argument cut for the audit log.
const auditTruncated = " [cut for the audit log]"

// auditPathBytes bounds a path in the audit log (policy refuses longer).
const auditPathBytes = 4096

// auditEnum returns s for the audit log when it is empty or valid, else
// auditInvalid: an unvalidated value never reaches an id field.
func auditEnum(s string, valid bool) string {
	if s == "" || valid {
		return s
	}
	return auditInvalid
}

// auditServer is auditEnum for a server id.
func auditServer(s string) tenant.ServerID {
	return tenant.ServerID(auditEnum(s, serverIDPattern.MatchString(s)))
}

// auditText cuts a free-text argument at n bytes (on a character boundary)
// and marks the cut, so no event carries more than about n bytes of it.
func auditText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + auditTruncated
}
