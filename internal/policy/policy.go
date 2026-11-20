// Package policy decides whether a principal may run a tool on a server with
// given arguments (spec 5.3).
//
// Decide makes three checks, in order: the server belongs to the principal's
// tenant (else not_found, never access_denied, so ids do not leak across
// tenants); the level the tool requires is within what the server grants
// (else access_denied); the arguments pass the guard rails (else
// policy_denied, with the name of the rule that matched).
//
// The guard rails are a built-in command denylist (commands.go), a built-in
// path denylist (paths.go) and the operator's additions from the config.
// The denylist is a guard rail, not a boundary. A shell command can be
// written in endless ways and no list can catch them all; what a command can
// change is decided by the unprivileged account it runs as on the target
// (spec D6 and section 6).
//
// Known limitations, accepted for a guard rail (spec 5.3): a command word
// built at run time (`$(echo sudo) id`, `x=$(curl U); eval "$x"`), options
// that take a value before the command (`env -u X sudo id`, `telinit -t 5 0`,
// `systemctl -H h reboot`), wrappers it does not know (`flock /tmp/l su`),
// commands inside the script of `eval "..."`, other tools that do the same
// harm (`systemctl start reboot.target`, `blkdiscard`, `cp x /dev/sda`,
// `find / -delete`, `rm -rf /etc`), devices it does not list (`/dev/nbd0`,
// LVM paths), a download saved and run later (`curl -o f U; sh f`), a fork
// bomb written as `function f { f|f& }`, and on the path side custom key
// names, `.envrc` and `/proc/*/cmdline`. Single- and double-quoted text is
// skipped when looking for a command start, except for a $( or backtick
// inside double quotes, but only when the quote opens and closes on one
// line: every line start is a command start, so a quote in a comment, a
// here-doc body or an unterminated string never hides a later line. The
// accepted cost, failing closed: a line inside a multi-line quoted string
// that begins with a denied word is denied, and so can be a quoted
// separator on a later line of a multi-line script. A single-quoted span
// is read both without escapes (sh, bash) and with them (fish), since the
// target's login shell is not known.
//
// Fail closed: a principal without a tenant or a kind is not_found, a tool
// that declares no level (AccessNone) or an undefined one is access_denied,
// and NewEngine refuses a tenant with an empty id.
package policy

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// Code is the stable result code of a denied decision. An allowed decision
// has the empty code.
type Code string

// The decision codes (spec 5.3). They are also tool error codes (spec 5.8).
const (
	// CodeNotFound: the server does not exist in the principal's tenant.
	CodeNotFound Code = "not_found"
	// CodeAccessDenied: the server does not grant the level the tool needs.
	CodeAccessDenied Code = "access_denied"
	// CodePolicyDenied: the arguments hit a guard rail.
	CodePolicyDenied Code = "policy_denied"
)

// Rule names for the checks that are not denylist entries. Each is what
// Decision.Rule and the audit log's rule field carry.
const (
	RuleServerNotFound    = "server-not-found"
	RuleAccessLevel       = "access-level"
	RuleUnknownInput      = "tool-input-unknown"
	RuleCommandEmpty      = "command-empty"
	RuleCommandNUL        = "command-nul"
	RuleCommandBadUTF8    = "command-invalid-utf8"
	RuleCommandTooLong    = "command-too-long"
	RulePathEmpty         = "path-empty"
	RulePathNUL           = "path-nul"
	RulePathBadUTF8       = "path-invalid-utf8"
	RulePathNotAbsolute   = "path-not-absolute"
	RulePathTooLong       = "path-too-long"
	operatorCommandPrefix = "config:deny_commands"
	operatorPathPrefix    = "config:deny_paths"
)

// MaxPathBytes is the longest path a file tool may name (Linux PATH_MAX).
const MaxPathBytes = 4096

// Input says which argument of a tool the guard rails check.
type Input uint8

// The kinds of tool input.
const (
	// InputNone: the tool takes no command and no path.
	InputNone Input = iota
	// InputCommand: Args.Command is checked against the command guard rails.
	InputCommand
	// InputPath: Args.Path is checked against the path guard rails.
	InputPath
)

// Tool describes a tool as the policy engine sees it. Phase 5 fills a table
// of these for the eight tools.
type Tool struct {
	// Name is the MCP tool name.
	Name string
	// Requires is the level the server must grant.
	Requires tenant.AccessLevel
	// Input is the argument the guard rails check.
	Input Input
	// WritesFiles marks a tool that writes to the target. Such a tool always
	// requires AccessFull, whatever Requires says.
	WritesFiles bool
}

// Args are a tool call's arguments that policy checks.
type Args struct {
	// Command is the command text, for an InputCommand tool.
	Command string
	// Path is the remote path, for an InputPath tool.
	Path string
}

// Decision is the result of Decide.
type Decision struct {
	// Allow is true when the call may go ahead.
	Allow bool
	// Code is empty when allowed, else why not.
	Code Code
	// Rule names the check or denylist entry that decided a denial.
	Rule string
}

var allow = Decision{Allow: true}

func deny(code Code, rule string) Decision {
	return Decision{Code: code, Rule: rule}
}

// tenantRules is what the engine keeps per tenant.
type tenantRules struct {
	maxCommandBytes int
	commands        []commandRule
	paths           []pathRule
}

// Engine holds the compiled guard rails of every tenant. It is safe for
// concurrent use and never changes after NewEngine.
type Engine struct {
	tenants map[tenant.TenantID]*tenantRules
}

// NewEngine compiles the operator's deny lists and takes the limits of each
// tenant. It accepts any access level a server grants, full included (spec
// D5: the config loader is what refuses full in v0.1). It fails on a
// duplicate tenant id, a tenant whose max_command_bytes is not positive, and
// an operator entry that does not compile.
func NewEngine(tenants []tenant.Tenant) (*Engine, error) {
	e := &Engine{tenants: map[tenant.TenantID]*tenantRules{}}
	var errs []error
	for i := range tenants {
		t := &tenants[i]
		if t.ID == "" {
			errs = append(errs, errors.New("a tenant has an empty id"))
			continue
		}
		if _, dup := e.tenants[t.ID]; dup {
			errs = append(errs, fmt.Errorf("tenant %q appears twice", t.ID))
			continue
		}
		if t.Limits.MaxCommandBytes <= 0 {
			errs = append(errs, fmt.Errorf("tenant %q: max_command_bytes must be positive", t.ID))
			continue
		}
		r := &tenantRules{maxCommandBytes: int(t.Limits.MaxCommandBytes)}
		for j, p := range t.Policy.DenyCommands {
			re, err := regexp.Compile(p)
			if err != nil {
				errs = append(errs, fmt.Errorf("tenant %q: deny_commands[%d]: %w", t.ID, j, err))
				continue
			}
			r.commands = append(r.commands, commandRule{Name: fmt.Sprintf("%s[%d]", operatorCommandPrefix, j), Pattern: p, re: re})
		}
		for j, g := range t.Policy.DenyPaths {
			if !validGlob(g) {
				errs = append(errs, fmt.Errorf("tenant %q: deny_paths[%d]: not a valid glob", t.ID, j))
				continue
			}
			r.paths = append(r.paths, pathRule{Name: fmt.Sprintf("%s[%d]", operatorPathPrefix, j), Globs: []string{g}})
		}
		e.tenants[t.ID] = r
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return e, nil
}

// Decide checks whether principal p may call tool on srv with args. srv is
// the result of a tenant-scoped lookup (nil when nothing was found); a server
// of another tenant is treated exactly like a missing one.
func (e *Engine) Decide(p tenant.Principal, srv *tenant.Server, tool Tool, args Args) Decision {
	rules, ok := e.tenants[p.Tenant]
	if !ok || p.Tenant == "" || p.Kind == 0 || srv == nil || srv.Tenant != p.Tenant {
		return deny(CodeNotFound, RuleServerNotFound)
	}
	required := tool.Requires
	if tool.WritesFiles {
		required = tenant.AccessFull
	}
	// A tool must declare at least look (a zero-value Tool is never
	// allowed); Permits(required, look) says required is look or above.
	if !tenant.Permits(required, tenant.AccessLook) || !tenant.Permits(srv.Access, required) {
		return deny(CodeAccessDenied, RuleAccessLevel)
	}
	switch tool.Input {
	case InputNone:
		return allow
	case InputCommand:
		return rules.checkCommand(args.Command)
	case InputPath:
		return rules.checkPath(args.Path)
	default:
		return deny(CodePolicyDenied, RuleUnknownInput)
	}
}

// checkCommand applies the command guard rails. Rules are matched against
// the command with leading and trailing white space removed, which is not
// part of what a shell runs; the byte checks look at the command as given.
func (r *tenantRules) checkCommand(cmd string) Decision {
	if rule := checkText(cmd, r.maxCommandBytes, RuleCommandEmpty, RuleCommandNUL, RuleCommandBadUTF8, RuleCommandTooLong); rule != "" {
		return deny(CodePolicyDenied, rule)
	}
	c := strings.TrimSpace(cmd)
	for _, b := range builtinCommands {
		if b.mayMatch(c) && b.re.MatchString(c) {
			return deny(CodePolicyDenied, b.Name)
		}
	}
	for _, o := range r.commands {
		if o.re.MatchString(c) {
			return deny(CodePolicyDenied, o.Name)
		}
	}
	return allow
}

// checkText returns the rule a text argument breaks, or "".
func checkText(s string, maxBytes int, empty, nul, badUTF8, tooLong string) string {
	switch {
	case strings.TrimSpace(s) == "":
		return empty
	case strings.IndexByte(s, 0) >= 0:
		return nul
	case !utf8.ValidString(s):
		return badUTF8
	case len(s) > maxBytes:
		return tooLong
	}
	return ""
}

// CleanPath is the form of a path that policy decides on. A file tool must
// send the target exactly this path, so the decision and the action agree.
func CleanPath(p string) string {
	return path.Clean(p)
}

// checkPath applies the path guard rails to the cleaned path. Cleaning is
// lexical: a symbolic link on the target can still lead somewhere the list
// would deny, which is one reason the list is a guard rail and not a
// boundary.
func (r *tenantRules) checkPath(p string) Decision {
	if rule := checkText(p, MaxPathBytes, RulePathEmpty, RulePathNUL, RulePathBadUTF8, RulePathTooLong); rule != "" {
		return deny(CodePolicyDenied, rule)
	}
	if !strings.HasPrefix(p, "/") {
		return deny(CodePolicyDenied, RulePathNotAbsolute)
	}
	clean := CleanPath(p)
	for _, rule := range builtinPaths {
		if rule.matches(clean) {
			return deny(CodePolicyDenied, rule.Name)
		}
	}
	for _, rule := range r.paths {
		if rule.matches(clean) {
			return deny(CodePolicyDenied, rule.Name)
		}
	}
	return allow
}
