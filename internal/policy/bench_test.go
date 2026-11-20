package policy

import (
	"strings"
	"testing"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// maxCommandBytes is the largest max_command_bytes the config accepts
// (spec 5.1): the longest command policy.Decide is ever given.
const maxCommandBytes = 64 << 10

// largeCommands are allowed commands of exactly maxCommandBytes bytes, in
// the shapes that cost the command rails the most: one long line of words,
// many short lines, many quoted spans, many separators, words that name
// denied commands but are arguments, and every rule's literal at once.
func largeCommands() map[string]string {
	fill := func(unit string) string {
		s := strings.Repeat(unit, maxCommandBytes/len(unit)+1)
		return s[:maxCommandBytes]
	}
	return map[string]string{
		"one-line":   "echo " + fill("alpha beta gamma ")[5:],
		"many-lines": fill("ls -la /var/log\n"),
		"quotes":     "echo " + fill(`"a; b | c" 'd & e' `)[5:],
		"separators": fill("ls; id && uptime | wc -l; "),
		"arguments":  "grep " + fill("sudo reboot mkfs curl sh ")[5:],
		// Every rule's literal (but --no-preserve-root, which alone
		// denies), so no rule is skipped: the worst case.
		"every-literal": "grep " + fill("sudo doas pkexec run0 reboot init systemctl mkfs of=x /dev/x rm wget 'f()' ")[5:],
	}
}

// BenchmarkDecideMaxCommand measures Decide on a command of the largest
// allowed size (spec 5.3 callout records the result).
func BenchmarkDecideMaxCommand(b *testing.B) {
	e, ts := benchEngine(b)
	srv := &ts[0].Servers[0]
	p := tenant.Principal{Tenant: ts[0].ID, Kind: tenant.KindUser, Subject: "a@example.net"}
	tool := Tool{Requires: tenant.AccessLook, Input: InputCommand}
	for name, cmd := range largeCommands() {
		b.Run(name, func(b *testing.B) {
			if d := e.Decide(p, srv, tool, Args{Command: cmd}); !d.Allow {
				b.Fatalf("%s denied by %s", name, d.Rule)
			}
			b.SetBytes(int64(len(cmd)))
			for b.Loop() {
				e.Decide(p, srv, tool, Args{Command: cmd})
			}
		})
	}
}

// BenchmarkCommandRules measures each built-in rule alone on each large
// command, to show which patterns dominate.
func BenchmarkCommandRules(b *testing.B) {
	for name, cmd := range largeCommands() {
		for _, r := range builtinCommands {
			b.Run(name+"/"+r.Name, func(b *testing.B) {
				for b.Loop() {
					r.re.MatchString(cmd)
				}
			})
		}
	}
}

func benchEngine(b *testing.B) (*Engine, []tenant.Tenant) {
	b.Helper()
	ts := []tenant.Tenant{{
		ID: "t", Limits: tenant.Limits{MaxCommandBytes: maxCommandBytes},
		Servers: []tenant.Server{{Tenant: "t", ID: "s", Access: tenant.AccessLook}},
	}}
	e, err := NewEngine(ts)
	if err != nil {
		b.Fatal(err)
	}
	return e, ts
}
