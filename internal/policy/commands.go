package policy

import (
	"regexp"
	"strings"
)

// commandRule is one entry of a command denylist: a stable name (what
// Decision.Rule and the audit log carry), its RE2 source and the compiled
// expression, matched unanchored against the whole command. Built-in and
// operator rules share this type.
type commandRule struct {
	Name    string
	Pattern string
	// Needs, when set, lists literals of which every string the pattern
	// matches contains at least one (the command word or words a rule is
	// about, or a fixed piece of its arguments). A command that contains
	// none of them cannot match, so the expression is not run: the rails
	// run as a nondeterministic automaton whose cost is the input length
	// times the pattern size, and on a command of the maximum size each
	// rule costs tens of milliseconds (spec 5.3 callout,
	// BenchmarkDecideMaxCommand). Decisions do not change: FuzzCommandGuardRail
	// checks for every input that a rule whose literals are absent does
	// not match. Operator rules have none.
	Needs []string
	re    *regexp.Regexp
}

// mayMatch reports whether c contains one of the rule's literals (always
// true for a rule without any).
func (r commandRule) mayMatch(c string) bool {
	if len(r.Needs) == 0 {
		return true
	}
	for _, n := range r.Needs {
		if strings.Contains(c, n) {
			return true
		}
	}
	return false
}

// Building blocks. Most rules match a command word only where a shell would
// run it as a command, so that `grep sudo /var/log/auth.log`, `man reboot`,
// `cat /etc/sudoers` and `grep -E 'sshd|sudo' log` stay allowed while
// `sudo -l`, `ls; sudo id`, `$(sudo id)`, `/usr/bin/sudo id`, `\sudo id`,
// `"sudo" id`, `env X=1 nohup sudo id`, `eval sudo id`,
// `find . -exec sudo id \;` and `sh -c 'ls; sudo id'` are denied. That is a
// deliberate trade: a word that is only an argument is text, not an action.
// These are regular expressions, not a shell parser; the package doc lists
// what they accept.
const (
	// lineStart: where a scan for a command start may begin: the start of
	// the command, or any line start, since a newline always starts a new
	// command. After a newline, the first quote on the line may close a
	// quote opened on an earlier line, so it is taken as a plain character.
	// That fails closed: in a multi-line script a quoted separator on a
	// later line can be over-denied, and a line inside a multi-line quoted
	// string that begins with a denied word is denied. That is the accepted
	// cost of never letting a quote hide a line.
	lineStart = `(?:^|[\n\r](?:[^'"\n\r]*['"])?)`
	// outsideQuotes: text before a command start on the same line, with
	// quoted strings and backslash escapes taken whole, so a separator
	// inside quotes does not count. A quoted span is skipped only when it
	// opens and closes on the same line; an unterminated quote is never
	// skipped, so it can hide at most the rest of its own line, which the
	// shell treats as quoted data or as a comment. ANSI-C strings ($'a\'b')
	// are taken whole with their escapes. A single-quoted span may also be
	// read with backslash escapes ('a\'b'), as fish reads it: the login
	// shell on the target is not known, and either reading may reveal a
	// separator, so both are tried (failing closed).
	outsideQuotes = `(?:[^'"\\\n\r]|\\[^\n\r]|\$?'(?:[^'\\\n\r]|\\[^\n\r])*'|'[^'\n\r]*'|"(?:[^"\\\n\r]|\\[^\n\r])*")*?`
	// separator: what ends one command and starts the next, outside quotes:
	// ; & | ( ) { } ! a backtick, a newline, $( and find's -exec/-execdir.
	separator = `(?:[;&|(){}!\x60\n\r]|\$\(|(?:^|\s)-exec(?:dir)?\s)`
	// dqSubstitution: a $( or backtick inside double quotes on the same
	// line, which still runs a command.
	dqSubstitution = `"(?:[^"\\\n\r]|\\[^\n\r])*?(?:\$\(|\x60)`
	// shellC: the script of `sh -c` (optionally quoted), at its start or
	// after a separator inside it.
	shellC = `\b(?:sh|bash|zsh|dash|ksh|mksh|ash|fish|csh|tcsh)\s+(?:-\w+\s+)*-\w*c\w*\s+['"]?(?:[^'"]*?(?:[;&|(){}!\n\r\x60]|\$\())?`
	// cmdStart: the start of the command, a separator, a substitution in
	// double quotes, or the script of `sh -c`; then white space.
	cmdStart = `(?:` + lineStart + `(?:` + outsideQuotes + `(?:` + separator + `|` + dqSubstitution + `))?|` + shellC + `)\s*`
	// wrappers: zero or more words that run the next word as a command
	// (with options, or numeric arguments such as `nice -n 5` and
	// `timeout 5s`), shell keywords that precede a command, and VAR=value
	// assignments. `command` is a wrapper only as `command` or `command -p`:
	// `command -v sudo` only looks the name up.
	wrappers = `(?:(?:command(?:\s+-p)?|(?:[A-Za-z_][A-Za-z0-9_]*=\S*|sudo|doas|exec|eval|nohup|time|nice|ionice|env|xargs|builtin|setsid|stdbuf|timeout|unbuffer|busybox|watch|strace|then|do|else|elif|if|while|until)(?:\s+(?:-\S+|\d\S*))*)\s+)*`
	// wordQuote: a backslash or quotes on the command word (`\sudo`,
	// `"sudo"`, `'pkexec'`) do not change what runs.
	wordQuote = `['"\\]*`
	// pathPrefix: an optional directory, as in /usr/bin/sudo or ./sudo.
	pathPrefix = `(?:[^\s;&|()'"\x60]*/)?`
	// wordEnd: the command word ends here.
	wordEnd = `(?:$|[\s;&|()<>'"\x60}\\])`
	// q: an optional quote around a target such as "/" or "/dev/sda".
	q = `['"]?`
	// blockDev: the names of block devices under /dev. /dev/null, /dev/zero,
	// /dev/stdout and the like are not among them.
	blockDev = `/dev/(?:sd|hd|vd|xvd|nvme|mmcblk|dm-|md|loop|mtd|sr|mapper/|disk/)`
	// recursive: an rm option that recurses.
	recursive = `(?:-[a-zA-Z]*[rR][a-zA-Z]*|--recursive)`
	// shells: shells a download is piped into; always denied.
	shells = `(?:(?:ba|z|da|k|mk|a|c|tc)?sh|fish)` + wordEnd
	// interpreters: denied only when they read their program from the pipe,
	// that is with no script, module or -e/-c/-m/-r/-E style argument after
	// them, so `| python3 -m json.tool` and `| perl -pe ...` pass.
	interpreters = `(?:python[0-9.]*|perl|ruby|node|php)(?:\s+-[^\s;&|()'"ecmrE]*)*\s*(?:$|[;&|)])`
	// pipeGap: what may sit between a download command and the pipe:
	// anything but a newline or ; & (a backslash-newline continues the line).
	pipeGap = `(?:[^\n;&]|\\\n)*?`
)

// cmdArgs matches word (an RE2 fragment) in command position followed by
// args (an RE2 fragment that starts right after the word).
func cmdArgs(word, args string) string {
	return cmdStart + wrappers + wordQuote + pathPrefix + `(?:` + word + `)` + wordQuote + args
}

// cmd matches word in command position.
func cmd(word string) string {
	return cmdArgs(word, wordEnd)
}

// builtinCommands is the built-in command denylist (spec 5.3). The order
// decides which name a command that hits several rules is reported under.
// Every entry has must-match and must-not-match cases in commands_test.go,
// and that test fails for an entry without both.
var builtinCommands = compileCommands([]commandRule{
	// Privilege escalation. `visudo -c`, `sudoedit`, `which sudo`,
	// `command -v sudo`, `cat /etc/sudoers` and `pseudocode` are not
	// invocations of sudo. `sudo -l` and `sudo -n true` are: the rule
	// denies sudo in any form.
	{Name: "sudo", Pattern: cmd(`sudo`), Needs: []string{"sudo"}},
	// `su` as a command; `sum`, `sudo` (its own rule) and `su` as an
	// argument (`id -nG su`) are not.
	{Name: "su", Pattern: cmd(`su`), Needs: []string{"su"}},
	{Name: "doas", Pattern: cmd(`doas`), Needs: []string{"doas"}},
	{Name: "pkexec", Pattern: cmd(`pkexec`), Needs: []string{"pkexec"}},
	{Name: "run0", Pattern: cmd(`run0`), Needs: []string{"run0"}},
	// Power control. `last reboot` and `man shutdown` only name the word.
	{Name: "power-control", Pattern: cmd(`shutdown|reboot|poweroff|halt`), Needs: []string{"shutdown", "reboot", "poweroff", "halt"}},
	// SysV init run levels: `init 0`, `telinit 6`. `init` with no level or
	// `systemd-analyze` is not matched.
	{Name: "init-runlevel", Pattern: cmdArgs(`(?:tel)?init`, `\s+(?:[0-6sSqQ]|single)`+wordEnd), Needs: []string{"init"}},
	// systemd power and isolation verbs. `systemctl status`, `is-active`
	// and `list-units` are allowed.
	{Name: "systemctl-power", Pattern: cmdArgs(`systemctl`, `(?:\s+-\S+)*\s+(?:poweroff|reboot|soft-reboot|halt|kexec|suspend|hibernate|hybrid-sleep|suspend-then-hibernate|rescue|emergency|isolate|default)`+wordEnd), Needs: []string{"systemctl"}},
	// File system creation and wiping. `mkdir` and `lsblk -f` are allowed.
	{Name: "fs-create", Pattern: cmd(`mkfs(?:\.\w+)?|mke2fs|mkswap|mkdosfs|mkntfs|wipefs`), Needs: []string{"mkfs", "mke2fs", "mkswap", "mkdosfs", "mkntfs", "wipefs"}},
	// dd onto a block device. `dd if=/dev/zero of=/dev/null` (a benchmark)
	// and dd into a file are allowed.
	{Name: "dd-to-device", Pattern: cmdArgs(`dd`, `[^;&|\n]*\bof=`+q+blockDev), Needs: []string{"of="}},
	// A redirect or tee onto a block device, outside quotes. `> /dev/null`
	// and `echo '> /dev/sda'` are allowed.
	{Name: "write-to-device", Pattern: lineStart + outsideQuotes + `>\|?\s*` + q + blockDev + `|` + cmdArgs(`tee`, `[^;&|\n]*\s`+q+blockDev), Needs: []string{"/dev/"}},
	// A recursive rm whose arguments include / or /* (quoted or not, the
	// option before or after it). `rm -rf /tmp/x` and `rm -r ./build/` are
	// allowed.
	{Name: "rm-root", Pattern: cmdArgs(`rm`, `[^;&|\n]*\s`+recursive+`[^;&|\n]*\s`+q+`/+\*?`+q+wordEnd) + `|` +
		cmdArgs(`rm`, `[^;&|\n]*\s`+q+`/+\*?`+q+`\s[^;&|\n]*`+recursive), Needs: []string{"rm"}},
	// The option exists only to delete /. Matched anywhere.
	{Name: "rm-no-preserve-root", Pattern: `--no-preserve-root\b`, Needs: []string{"--no-preserve-root"}},
	// A download piped (| or |&) into a shell, possibly through wrappers
	// (`| env A=1 bash`, `| exec sh`, `| sudo bash`), or into an interpreter
	// that reads its program from the pipe. `curl ... | grep sh`,
	// `curl ... | shasum` and `curl ... | python3 -m json.tool` are allowed.
	{Name: "pipe-to-shell", Pattern: `\b(?:curl|wget|fetch)\b` + pipeGap + `\|&?\s*` + wrappers + wordQuote + pathPrefix + `(?:` + shells + `|` + interpreters + `)`, Needs: []string{"curl", "wget", "fetch"}},
	// A download run through a substitution: `bash <(curl ...)`,
	// `sh -c "$(wget -O- ...)"`, `eval $(curl ...)`, `. <(curl ...)`.
	// `echo $(curl -s https://example.net/ip)` is allowed.
	{Name: "download-substitution", Pattern: `(?:\b(?:(?:ba|z|da|k|mk|fi|a|c|tc)?sh|source|eval)|(?:^|[\s;&|(])\.)\s+[^\n]*?(?:<\(|\$\(|\x60)\s*(?:curl|wget|fetch)\b`, Needs: []string{"curl", "wget", "fetch"}},
	// A function that pipes into itself in the background: `:(){ :|:& };:`
	// and named variants. `f() { ls | wc -l; }` and `f() { a | b && c; }`
	// are allowed. Without back-references RE2 cannot check that both
	// sides are the same name.
	{Name: "fork-bomb", Pattern: `[\w:.]+\s*\(\)\s*\{[^}]*\|\s*[\w:.]+\s*&(?:[^&}][^}]*)?\}`, Needs: []string{"()"}},
})

func compileCommands(rules []commandRule) []commandRule {
	for i := range rules {
		rules[i].re = regexp.MustCompile(rules[i].Pattern)
	}
	return rules
}
